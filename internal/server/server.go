package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/mithileshchellappan/pushboy/internal/service"
	"github.com/mithileshchellappan/pushboy/internal/storage"
	"github.com/mithileshchellappan/pushboy/internal/worker"
)

type Server struct {
	service    *service.PushboyService
	workerPool *worker.Pool
	httpServer *http.Server
	router     chi.Router
}

func New(s *service.PushboyService, pool *worker.Pool) *Server {
	return &Server{service: s, workerPool: pool}
}

func (s *Server) Start(addr string) error {

	log.Printf("Starting server on %s", addr)
	s.router = s.setupRouter()
	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: s.router,
	}

	return s.httpServer.ListenAndServe()
}

func (s *Server) setupRouter() chi.Router {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Route("/v1", func(r chi.Router) {
		r.Get("/ping", s.handlePing)

		r.Route("/users", func(r chi.Router) {
			r.Post("/{externalID}/send", s.handleSendToUser)
		})

		r.Route("/topics", func(r chi.Router) {
			r.Post("/", s.handleCreateTopic)
			r.Get("/", s.handleListTopics)
			r.Get("/{topicID}", s.handleGetTopicByID)
			r.Delete("/{topicID}", s.handleDeleteTopic)

			r.Post("/{topicID}/subscribe", s.handleSubscribeToTopic)
			r.Post("/{topicID}/publish", s.handlePublishToTopic)
			r.Get("/{topicID}/publish/{jobID}", s.handleGetJobStatus)
		})
	})

	return r
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("pong"))
}

func (s *Server) handleCreateTopic(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	topic, err := s.service.CreateTopic(r.Context(), req.Name)

	if err != nil {
		if errors.Is(err, storage.Errors.AlreadyExists) {
			http.Error(w, "Topic already exists", http.StatusConflict)
			return
		}
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		log.Printf("Error creating topic %v", err)
		return
	}

	s.respond(w, r, topic, http.StatusCreated)

}

func (s *Server) handleListTopics(w http.ResponseWriter, r *http.Request) {
	topics, err := s.service.ListTopics(r.Context())

	if err != nil {
		http.Error(w, "Error Listing topics", http.StatusInternalServerError)
		log.Printf("Error creating topic %v", err)
		return
	}
	s.respond(w, r, topics, http.StatusOK)
}

func (s *Server) handleGetTopicByID(w http.ResponseWriter, r *http.Request) {
	topicID := chi.URLParam(r, "topicID")
	print(topicID)
	if topicID == "" {
		http.Error(w, "topicID is required", http.StatusBadRequest)
		return
	}

	topic, err := s.service.GetTopicByID(r.Context(), topicID)
	if err != nil {
		http.Error(w, "Error getting topic", http.StatusInternalServerError)
		log.Printf("Error getting topic %v", err)
		return
	}

	s.respond(w, r, topic, http.StatusOK)
}

func (s *Server) handleDeleteTopic(w http.ResponseWriter, r *http.Request) {
	topicID := chi.URLParam(r, "topicID")
	if topicID == "" {
		http.Error(w, "topicID is required", http.StatusBadRequest)
		return
	}
	err := s.service.DeleteTopic(r.Context(), topicID)
	if err != nil {
		if err.Error() == "Topic not found" {
			http.Error(w, "Topic not found", http.StatusNotFound)
			return
		}

		http.Error(w, "Error deleting topic", http.StatusInternalServerError)
		log.Printf("Error deleting topic %v", err)
		return
	}
	s.respond(w, r, nil, http.StatusNoContent)
}

func (s *Server) handleSubscribeToTopic(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Platform   string `json:"platform"`
		Token      string `json:"token"`
		ExternalID string `json:"external_id,omitempty"`
	}
	topicID := chi.URLParam(r, "topicID")
	if topicID == "" {
		http.Error(w, "topicID is required", http.StatusBadRequest)
		return
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	fmt.Println(req)
	if req.Platform != "apns" && req.Platform != "fcm" {
		http.Error(w, "Invalid platform", http.StatusBadRequest)
		return
	}

	if req.Token == "" {
		http.Error(w, "token is required", http.StatusBadRequest)
		return
	}

	sub, err := s.service.SubscribeToTopic(r.Context(), topicID, req.Platform, req.Token, req.ExternalID)
	if err != nil {
		http.Error(w, "Error subscribing to topic", http.StatusInternalServerError)
		log.Printf("Error subscribing to topic %v", err)
		return
	}
	s.respond(w, r, sub, http.StatusCreated)

}

func (s *Server) handlePublishToTopic(w http.ResponseWriter, r *http.Request) {
	topicID := chi.URLParam(r, "topicID")

	var req struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	if topicID == "" {
		http.Error(w, "topicID is required", http.StatusBadRequest)
		return
	}
	job, err := s.service.CreatePublishJob(r.Context(), topicID, req.Title, req.Body)
	if err != nil {
		http.Error(w, "Error creating publish job", http.StatusInternalServerError)
		log.Printf("Error creating publish job %v", err)
		return
	}

	s.workerPool.Submit(job)
	s.respond(w, r, job, http.StatusCreated)
}

func (s *Server) handleGetJobStatus(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")
	if jobID == "" {
		http.Error(w, "jobID is required", http.StatusBadRequest)
		return
	}

	job, err := s.service.GetJobStatus(r.Context(), jobID)
	if err != nil {
		http.Error(w, "Error getting job status", http.StatusInternalServerError)
		log.Printf("Error getting job status %v", err)
		return
	}
	if job == nil {
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}
	s.respond(w, r, job, http.StatusOK)
}

func (s *Server) handleSendToUser(w http.ResponseWriter, r *http.Request) {
	externalID := chi.URLParam(r, "externalID")

	var req struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	if externalID == "" {
		http.Error(w, "externalID is required", http.StatusBadRequest)
		return
	}

	err := s.service.SendToUser(r.Context(), externalID, req.Title, req.Body)
	if err != nil {
		http.Error(w, "Error sending notification to user", http.StatusInternalServerError)
		log.Printf("Error sending notification to user %v", err)
		return
	}

	s.respond(w, r, map[string]string{"status": "sent"}, http.StatusOK)
}

// MARK: Helpers
func (s *Server) respond(w http.ResponseWriter, r *http.Request, data interface{}, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if data != nil {
		if err := json.NewEncoder(w).Encode(data); err != nil {
			log.Printf("Error encoding response for request %s: %v", middleware.GetReqID(r.Context()), err)
		}
	}
}
