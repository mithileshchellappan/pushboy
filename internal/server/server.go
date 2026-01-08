package server

import (
	"context"
	"encoding/json"
	"errors"
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

		// User routes
		r.Route("/users", func(r chi.Router) {
			r.Post("/", s.handleCreateUser)
			r.Get("/{userID}", s.handleGetUser)
			r.Delete("/{userID}", s.handleDeleteUser)

			// Token routes - register creates user if not exists
			r.Post("/tokens", s.handleRegisterToken)
			r.Get("/{userID}/tokens", s.handleGetUserTokens)
			r.Delete("/{userID}/tokens/{tokenID}", s.handleDeleteToken)

			// User subscription routes
			r.Post("/{userID}/subscriptions/{topicID}", s.handleSubscribeToTopic)
			r.Delete("/{userID}/subscriptions/{topicID}", s.handleUnsubscribeFromTopic)
			r.Get("/{userID}/subscriptions", s.handleGetUserSubscriptions)

			// Send to user
			r.Post("/{userID}/send", s.handleSendToUser)
		})

		// Topic routes
		r.Route("/topics", func(r chi.Router) {
			r.Post("/", s.handleCreateTopic)
			r.Get("/", s.handleListTopics)
			r.Get("/{topicID}", s.handleGetTopicByID)
			r.Delete("/{topicID}", s.handleDeleteTopic)

			r.Post("/{topicID}/publish", s.handlePublishToTopic)
			r.Get("/{topicID}/publish/{jobID}", s.handleGetJobStatus)
		})
	})

	return r
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// Health check
func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("pong"))
}

// User handlers

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	user, err := s.service.CreateUser(r.Context(), req.ID)
	if err != nil {
		if errors.Is(err, storage.Errors.AlreadyExists) {
			http.Error(w, "User already exists", http.StatusConflict)
			return
		}
		http.Error(w, "Error creating user", http.StatusInternalServerError)
		log.Printf("Error creating user: %v", err)
		return
	}

	s.respond(w, r, user, http.StatusCreated)
}

func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "userID")
	if userID == "" {
		http.Error(w, "userID is required", http.StatusBadRequest)
		return
	}

	user, err := s.service.GetUser(r.Context(), userID)
	if err != nil {
		if errors.Is(err, storage.Errors.NotFound) {
			http.Error(w, "User not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Error getting user", http.StatusInternalServerError)
		log.Printf("Error getting user: %v", err)
		return
	}

	s.respond(w, r, user, http.StatusOK)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "userID")
	if userID == "" {
		http.Error(w, "userID is required", http.StatusBadRequest)
		return
	}

	err := s.service.DeleteUser(r.Context(), userID)
	if err != nil {
		if errors.Is(err, storage.Errors.NotFound) {
			http.Error(w, "User not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Error deleting user", http.StatusInternalServerError)
		log.Printf("Error deleting user: %v", err)
		return
	}

	s.respond(w, r, nil, http.StatusNoContent)
}

// Token handlers

func (s *Server) handleRegisterToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID       string `json:"id,omitempty"`
		Platform string `json:"platform"`
		Token    string `json:"token"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	if req.Platform != "apns" && req.Platform != "fcm" {
		http.Error(w, "Invalid platform (must be 'apns' or 'fcm')", http.StatusBadRequest)
		return
	}

	if req.Token == "" {
		http.Error(w, "token is required", http.StatusBadRequest)
		return
	}

	token, user, err := s.service.RegisterToken(r.Context(), req.ID, req.Platform, req.Token)
	if err != nil {
		if errors.Is(err, storage.Errors.AlreadyExists) {
			http.Error(w, "Token already registered", http.StatusConflict)
			return
		}
		http.Error(w, "Error registering token", http.StatusInternalServerError)
		log.Printf("Error registering token: %v", err)
		return
	}

	response := struct {
		User  *storage.User  `json:"user"`
		Token *storage.Token `json:"token"`
	}{
		User:  user,
		Token: token,
	}

	s.respond(w, r, response, http.StatusCreated)
}

func (s *Server) handleGetUserTokens(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "userID")
	if userID == "" {
		http.Error(w, "userID is required", http.StatusBadRequest)
		return
	}

	tokens, err := s.service.GetUserTokens(r.Context(), userID)
	if err != nil {
		http.Error(w, "Error getting tokens", http.StatusInternalServerError)
		log.Printf("Error getting tokens: %v", err)
		return
	}

	s.respond(w, r, tokens, http.StatusOK)
}

func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	tokenID := chi.URLParam(r, "tokenID")
	if tokenID == "" {
		http.Error(w, "tokenID is required", http.StatusBadRequest)
		return
	}

	err := s.service.DeleteToken(r.Context(), tokenID)
	if err != nil {
		if errors.Is(err, storage.Errors.NotFound) {
			http.Error(w, "Token not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Error deleting token", http.StatusInternalServerError)
		log.Printf("Error deleting token: %v", err)
		return
	}

	s.respond(w, r, nil, http.StatusNoContent)
}

// User subscription handlers

func (s *Server) handleSubscribeToTopic(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "userID")
	topicID := chi.URLParam(r, "topicID")

	if userID == "" {
		http.Error(w, "userID is required", http.StatusBadRequest)
		return
	}
	if topicID == "" {
		http.Error(w, "topicID is required", http.StatusBadRequest)
		return
	}

	sub, err := s.service.SubscribeUserToTopic(r.Context(), userID, topicID)
	if err != nil {
		if errors.Is(err, storage.Errors.AlreadyExists) {
			http.Error(w, "Already subscribed to topic", http.StatusConflict)
			return
		}
		if errors.Is(err, storage.Errors.NotFound) {
			http.Error(w, "User or topic not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Error subscribing to topic", http.StatusInternalServerError)
		log.Printf("Error subscribing to topic: %v", err)
		return
	}

	s.respond(w, r, sub, http.StatusCreated)
}

func (s *Server) handleUnsubscribeFromTopic(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "userID")
	topicID := chi.URLParam(r, "topicID")

	if userID == "" {
		http.Error(w, "userID is required", http.StatusBadRequest)
		return
	}
	if topicID == "" {
		http.Error(w, "topicID is required", http.StatusBadRequest)
		return
	}

	err := s.service.UnsubscribeUserFromTopic(r.Context(), userID, topicID)
	if err != nil {
		if errors.Is(err, storage.Errors.NotFound) {
			http.Error(w, "Subscription not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Error unsubscribing from topic", http.StatusInternalServerError)
		log.Printf("Error unsubscribing from topic: %v", err)
		return
	}

	s.respond(w, r, nil, http.StatusNoContent)
}

func (s *Server) handleGetUserSubscriptions(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "userID")
	if userID == "" {
		http.Error(w, "userID is required", http.StatusBadRequest)
		return
	}

	subs, err := s.service.GetUserSubscriptions(r.Context(), userID)
	if err != nil {
		http.Error(w, "Error getting subscriptions", http.StatusInternalServerError)
		log.Printf("Error getting subscriptions: %v", err)
		return
	}

	s.respond(w, r, subs, http.StatusOK)
}

// Send to user handler

type SendNotificationRequest struct {
	storage.NotificationPayload
	ScheduledAt string `json:"scheduled_at,omitempty"`
}

func (s *Server) handleSendToUser(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "userID")
	if userID == "" {
		http.Error(w, "userID is required", http.StatusBadRequest)
		return
	}

	var req SendNotificationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Printf("Error decoding JSON payload for user %s: %v", userID, err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	// Validate required fields
	if req.Title == "" && req.Body == "" && !req.Silent {
		http.Error(w, "title or body is required (unless silent notification)", http.StatusBadRequest)
		return
	}

	payload := &storage.NotificationPayload{
		Title:      req.Title,
		Body:       req.Body,
		ImageURL:   req.ImageURL,
		Sound:      req.Sound,
		Badge:      req.Badge,
		Data:       req.Data,
		Silent:     req.Silent,
		CollapseID: req.CollapseID,
		Priority:   req.Priority,
		TTL:        req.TTL,
		ThreadID:   req.ThreadID,
		Category:   req.Category,
	}

	job, err := s.service.SendToUser(r.Context(), userID, payload, req.ScheduledAt)
	if err != nil {
		if errors.Is(err, storage.Errors.NotFound) {
			http.Error(w, "User not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Error sending notification", http.StatusInternalServerError)
		log.Printf("Error sending notification to user: %v", err)
		return
	}
	if job.ScheduledAt == "" {
		s.workerPool.Submit(job)
	}

	s.respond(w, r, map[string]string{"status": "queued", "job_id": job.ID}, http.StatusAccepted)
}

// Topic handlers

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
		http.Error(w, "Error creating topic", http.StatusInternalServerError)
		log.Printf("Error creating topic: %v", err)
		return
	}

	s.respond(w, r, topic, http.StatusCreated)
}

func (s *Server) handleListTopics(w http.ResponseWriter, r *http.Request) {
	topics, err := s.service.ListTopics(r.Context())
	if err != nil {
		http.Error(w, "Error listing topics", http.StatusInternalServerError)
		log.Printf("Error listing topics: %v", err)
		return
	}

	s.respond(w, r, topics, http.StatusOK)
}

func (s *Server) handleGetTopicByID(w http.ResponseWriter, r *http.Request) {
	topicID := chi.URLParam(r, "topicID")
	if topicID == "" {
		http.Error(w, "topicID is required", http.StatusBadRequest)
		return
	}

	topic, err := s.service.GetTopicByID(r.Context(), topicID)
	if err != nil {
		if errors.Is(err, storage.Errors.NotFound) {
			http.Error(w, "Topic not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Error getting topic", http.StatusInternalServerError)
		log.Printf("Error getting topic: %v", err)
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
		if errors.Is(err, storage.Errors.NotFound) {
			http.Error(w, "Topic not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Error deleting topic", http.StatusInternalServerError)
		log.Printf("Error deleting topic: %v", err)
		return
	}

	s.respond(w, r, nil, http.StatusNoContent)
}

// Publish handlers

func (s *Server) handlePublishToTopic(w http.ResponseWriter, r *http.Request) {
	topicID := chi.URLParam(r, "topicID")
	if topicID == "" {
		http.Error(w, "topicID is required", http.StatusBadRequest)
		return
	}

	var req SendNotificationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	// Validate required fields
	if req.Title == "" && req.Body == "" && !req.Silent {
		http.Error(w, "title or body is required (unless silent notification)", http.StatusBadRequest)
		return
	}

	payload := &storage.NotificationPayload{
		Title:      req.Title,
		Body:       req.Body,
		ImageURL:   req.ImageURL,
		Sound:      req.Sound,
		Badge:      req.Badge,
		Data:       req.Data,
		Silent:     req.Silent,
		CollapseID: req.CollapseID,
		Priority:   req.Priority,
		TTL:        req.TTL,
		ThreadID:   req.ThreadID,
		Category:   req.Category,
	}

	job, err := s.service.CreatePublishJob(r.Context(), topicID, payload, req.ScheduledAt)
	if err != nil {
		http.Error(w, "Error creating publish job", http.StatusInternalServerError)
		log.Printf("Error creating publish job: %v", err)
		return
	}
	if job.ScheduledAt == "" {
		s.workerPool.Submit(job)
	}
	s.respond(w, r, map[string]string{"status": "queued", "job_id": job.ID}, http.StatusAccepted)
}

func (s *Server) handleGetJobStatus(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")
	if jobID == "" {
		http.Error(w, "jobID is required", http.StatusBadRequest)
		return
	}

	job, err := s.service.GetJobStatus(r.Context(), jobID)
	if err != nil {
		if errors.Is(err, storage.Errors.NotFound) {
			http.Error(w, "Job not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Error getting job status", http.StatusInternalServerError)
		log.Printf("Error getting job status: %v", err)
		return
	}

	s.respond(w, r, job, http.StatusOK)
}

// Helper

func (s *Server) respond(w http.ResponseWriter, r *http.Request, data interface{}, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if data != nil {
		if err := json.NewEncoder(w).Encode(data); err != nil {
			log.Printf("Error encoding response for request %s: %v", middleware.GetReqID(r.Context()), err)
		}
	}
}
