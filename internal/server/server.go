package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/mithileshchellappan/pushboy/internal/service"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type Server struct {
	service *service.PushboyService
}

func New(s *service.PushboyService) *Server {
	return &Server{service: s}
}

func (s *Server) Start() http.Handler {

	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Route("/v1", func(r chi.Router) {
		r.Get("/ping", s.handlePing)

		r.Route("/topics", func(r chi.Router) {
			r.Post("/", s.handleCreateTopic)
			r.Get("/", s.handleListTopics)
			r.Get("/{topicID}", s.handleGetTopicByID)
			r.Delete("/{topicID}", s.handleDeleteTopic)

			r.Post("/{topicID}/subscribe", s.handleSubscribeToTopic)
		})
	})
	return r
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
		Platform string `json:"platform"`
		Token    string `json:"token"`
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

	sub, err := s.service.SubscribeToTopic(r.Context(), topicID, req.Platform, req.Token)
	if err != nil {
		http.Error(w, "Error subscribing to topic", http.StatusInternalServerError)
		log.Printf("Error subscribing to topic %v", err)
		return
	}
	s.respond(w, r, sub, http.StatusCreated)

}

func (s *Server) respond(w http.ResponseWriter, r *http.Request, data interface{}, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if data != nil {
		if err := json.NewEncoder(w).Encode(data); err != nil {
			log.Printf("Error encoding response for request %s: %v", middleware.GetReqID(r.Context()), err)
		}
	}
}
