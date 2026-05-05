package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/pipeline"
	"github.com/mithileshchellappan/pushboy/internal/service"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type Server struct {
	service     *service.PushboyService
	jobPipeline pipeline.Pipeline[model.JobItem]
	httpServer  *http.Server
	router      chi.Router
}

const immediateEnqueueTimeout = 2 * time.Second

func New(s *service.PushboyService, jobPipeline pipeline.Pipeline[model.JobItem]) *Server {
	return &Server{service: s, jobPipeline: jobPipeline}
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
		r.Get("/notifications/{jobID}", s.handleGetJobStatus)

		// User routes
		r.Route("/users", func(r chi.Router) {
			r.Get("/", s.handleListUsers)
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
			r.Get("/{userID}/notifications", s.handleListUserNotifications)

			// Send to user
			r.Post("/{userID}/send", s.handleSendToUser)
		})

		// Topic routes
		r.Route("/topics", func(r chi.Router) {
			r.Post("/", s.handleCreateTopic)
			r.Get("/", s.handleListTopics)
			r.Get("/{topicID}", s.handleGetTopicByID)
			r.Delete("/{topicID}", s.handleDeleteTopic)
			r.Get("/{topicID}/count", s.handleGetTopicSubscriberCount)
			r.Get("/{topicID}/subscribers", s.handleListTopicSubscribers)
			r.Get("/{topicID}/notifications", s.handleListTopicNotifications)

			r.Post("/{topicID}/publish", s.handlePublishToTopic)
		})

		r.Route("/live-activity", func(r chi.Router) {
			r.Post("/tokens", s.handleRegisterLAToken)
			r.Delete("/tokens", s.handleDeleteLAToken)
			r.Post("/topics/{topicID}/users/{userID}", s.handleRegisterUserToLATopic)
			r.Post("/jobs", s.handleCreateLAJob)
		})
	})

	return r
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) enqueueImmediateJob(jobID string, jobItem model.JobItem) error {
	enqueueCtx, cancel := context.WithTimeout(context.Background(), immediateEnqueueTimeout)
	defer cancel()

	if err := s.jobPipeline.Submit(enqueueCtx, jobItem); err != nil {
		statusCtx, statusCancel := context.WithTimeout(context.Background(), immediateEnqueueTimeout)
		defer statusCancel()

		if updateErr := s.service.UpdateJobStatus(statusCtx, jobID, model.NotificationJobStatusFailed); updateErr != nil {
			log.Printf("Failed to mark job %s as FAILED after enqueue error: %v", jobID, updateErr)
		}
		return err
	}

	return nil
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

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	const route = "/v1/users"

	query, limit, err := pageQueryFromRequest(r, route)
	if err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	users, err := s.service.ListUsers(r.Context(), query)
	if err != nil {
		http.Error(w, "Error listing users", http.StatusInternalServerError)
		log.Printf("Error listing users: %v", err)
		return
	}

	response := makeListResponse(users, limit, route, "", func(user storage.User) (string, string) {
		return user.CreatedAt, user.ID
	})
	s.respond(w, r, response, http.StatusOK)
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

	if req.Token == "" {
		http.Error(w, "token is required", http.StatusBadRequest)
		return
	}

	platform, err := model.ParsePlatform(req.Platform)
	if err != nil {
		http.Error(w, "Invalid platform (must be 'apns' or 'fcm')", http.StatusBadRequest)
		return
	}

	token, user, err := s.service.RegisterToken(r.Context(), req.ID, platform, req.Token)
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

func (s *Server) handleListUserNotifications(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "userID")
	if userID == "" {
		http.Error(w, "userID is required", http.StatusBadRequest)
		return
	}

	route := "/v1/users/" + userID + "/notifications"
	query, limit, err := notificationListQueryFromRequest(r, route)
	if err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	jobs, err := s.service.ListUserNotifications(r.Context(), userID, query)
	if err != nil {
		if errors.Is(err, storage.Errors.NotFound) {
			http.Error(w, "User not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Error listing user notifications", http.StatusInternalServerError)
		log.Printf("Error listing user notifications: %v", err)
		return
	}

	response := makeListResponse(jobs, limit, route, query.Status, func(job storage.PublishJob) (string, string) {
		return notificationJobCursorTime(job), job.ID
	})
	s.respond(w, r, response, http.StatusOK)
}

// Send to user handler

type SendNotificationRequest struct {
	model.NotificationPayload
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

	payload := &model.NotificationPayload{
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
		if strings.Contains(err.Error(), "scheduledAt") {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, "Error sending notification", http.StatusInternalServerError)
		log.Printf("Error sending notification to user: %v", err)
		return
	}

	jobItem := model.JobItem{
		ID:       job.ID,
		JobType:  model.JobTypePush,
		MaxRetry: 3,
		Payload:  payload,
		UserID:   userID,
	}

	if job.ScheduledAt == "" {
		if err := s.enqueueImmediateJob(job.ID, jobItem); err != nil {
			log.Printf("Error enqueueing immediate user job %s: %v", job.ID, err)
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, pipeline.ErrClosed) {
				http.Error(w, "Job queue unavailable", http.StatusServiceUnavailable)
				return
			}
			http.Error(w, "Error queueing notification", http.StatusInternalServerError)
			return
		}
	}

	s.respond(w, r, map[string]string{"status": "queued", "job_id": job.ID}, http.StatusAccepted)
}

// Topic handlers

func (s *Server) handleCreateTopic(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	topic, err := s.service.CreateTopic(r.Context(), req.ID, req.Name)
	if err != nil {
		if errors.Is(err, storage.Errors.AlreadyExists) {
			http.Error(w, "Topic already exists", http.StatusConflict)
			return
		}
		if strings.Contains(err.Error(), "topic id") {
			http.Error(w, err.Error(), http.StatusBadRequest)
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

func (s *Server) handleGetTopicSubscriberCount(w http.ResponseWriter, r *http.Request) {
	topicID := chi.URLParam(r, "topicID")
	if topicID == "" {
		http.Error(w, "topicID is required", http.StatusBadRequest)
		return
	}

	count, err := s.service.GetTopicSubscriberCount(r.Context(), topicID)
	if err != nil {
		http.Error(w, "Error getting subscriber count", http.StatusInternalServerError)
		log.Printf("Error getting subscriber count: %v", err)
		return
	}

	s.respond(w, r, map[string]int{"count": count}, http.StatusOK)
}

func (s *Server) handleListTopicSubscribers(w http.ResponseWriter, r *http.Request) {
	topicID := chi.URLParam(r, "topicID")
	if topicID == "" {
		http.Error(w, "topicID is required", http.StatusBadRequest)
		return
	}

	route := "/v1/topics/" + topicID + "/subscribers"
	query, limit, err := pageQueryFromRequest(r, route)
	if err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	subscribers, err := s.service.ListTopicSubscribers(r.Context(), topicID, query)
	if err != nil {
		if errors.Is(err, storage.Errors.NotFound) {
			http.Error(w, "Topic not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Error listing topic subscribers", http.StatusInternalServerError)
		log.Printf("Error listing topic subscribers: %v", err)
		return
	}

	response := makeListResponse(subscribers, limit, route, "", func(subscriber storage.TopicSubscriber) (string, string) {
		return subscriber.SubscribedAt, subscriber.ID
	})
	s.respond(w, r, response, http.StatusOK)
}

func (s *Server) handleListTopicNotifications(w http.ResponseWriter, r *http.Request) {
	topicID := chi.URLParam(r, "topicID")
	if topicID == "" {
		http.Error(w, "topicID is required", http.StatusBadRequest)
		return
	}

	route := "/v1/topics/" + topicID + "/notifications"
	query, limit, err := notificationListQueryFromRequest(r, route)
	if err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	jobs, err := s.service.ListTopicNotifications(r.Context(), topicID, query)
	if err != nil {
		if errors.Is(err, storage.Errors.NotFound) {
			http.Error(w, "Topic not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Error listing topic notifications", http.StatusInternalServerError)
		log.Printf("Error listing topic notifications: %v", err)
		return
	}

	response := makeListResponse(jobs, limit, route, query.Status, func(job storage.PublishJob) (string, string) {
		return notificationJobCursorTime(job), job.ID
	})
	s.respond(w, r, response, http.StatusOK)
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

	payload := &model.NotificationPayload{
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
		if errors.Is(err, storage.Errors.NotFound) {
			http.Error(w, "Topic not found", http.StatusNotFound)
			return
		}
		if strings.Contains(err.Error(), "scheduledAt") {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, "Error creating publish job", http.StatusInternalServerError)
		log.Printf("Error creating publish job: %v", err)
		return
	}

	jobItem := model.JobItem{
		ID:      job.ID,
		JobType: model.JobTypePush,
		Payload: payload,
		TopicID: topicID,
	}

	if job.ScheduledAt == "" {
		if err := s.enqueueImmediateJob(job.ID, jobItem); err != nil {
			log.Printf("Error enqueueing immediate topic job %s: %v", job.ID, err)
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, pipeline.ErrClosed) {
				http.Error(w, "Job queue unavailable", http.StatusServiceUnavailable)
				return
			}
			http.Error(w, "Error queueing publish job", http.StatusInternalServerError)
			return
		}
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
