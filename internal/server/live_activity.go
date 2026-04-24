package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/pipeline"
	"github.com/mithileshchellappan/pushboy/internal/service"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type registerLiveActivityTokenRequest struct {
	UserID    string `json:"userId"`
	TopicID   string `json:"topicId,omitempty"`
	Platform  string `json:"platform"`
	TokenType string `json:"tokenType"`
	Token     string `json:"token"`
	ExpiresAt string `json:"expiresAt,omitempty"`
}

type createLiveActivityJobRequest struct {
	Action       string          `json:"action"`
	ActivityID   string          `json:"activityId,omitempty"`
	ActivityType string          `json:"activityType,omitempty"`
	UserID       string          `json:"userId,omitempty"`
	TopicID      string          `json:"topicId,omitempty"`
	Payload      json.RawMessage `json:"payload,omitempty"`
	Options      json.RawMessage `json:"options,omitempty"`
	ExpiresAt    string          `json:"expiresAt,omitempty"`
}

func (s *Server) enqueueImmediateLADispatch(job *storage.LiveActivityJob, dispatch *storage.LiveActivityDispatch, jobItem model.JobItem) error {
	enqueueCtx, cancel := context.WithTimeout(context.Background(), immediateEnqueueTimeout)
	defer cancel()

	if err := s.jobPipeline.Submit(enqueueCtx, jobItem); err != nil {
		statusCtx, statusCancel := context.WithTimeout(context.Background(), immediateEnqueueTimeout)
		defer statusCancel()

		if updateErr := s.service.UpdateLADispatchStatus(statusCtx, dispatch.ID, "FAILED"); updateErr != nil {
			log.Printf("Failed to mark live activity dispatch %s as FAILED after enqueue error: %v", dispatch.ID, updateErr)
		}
		if dispatch.Action == model.LiveActivityActionStart {
			if failErr := s.service.FailLAJobIfActive(statusCtx, job.ID); failErr != nil {
				log.Printf("Failed to fail LA job %s after enqueue error on dispatch %s: %v", job.ID, dispatch.ID, failErr)
			}
		}
		return err
	}

	return nil
}

func (s *Server) handleRegisterLAToken(w http.ResponseWriter, r *http.Request) {
	var req registerLiveActivityTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	platform, err := model.ParsePlatform(req.Platform)
	if err != nil {
		http.Error(w, "Invalid platform (must be 'apns' or 'fcm')", http.StatusBadRequest)
		return
	}

	tokenType, err := model.ParseLiveActivityTokenType(req.TokenType)
	if err != nil {
		http.Error(w, "Invalid tokenType (must be 'start' or 'update')", http.StatusBadRequest)
		return
	}

	token, user, err := s.service.RegisterLAToken(
		r.Context(),
		req.UserID,
		platform,
		tokenType,
		req.Token,
		req.TopicID,
		req.ExpiresAt,
	)
	if err != nil {
		switch {
		case errors.Is(err, storage.Errors.NotFound):
			http.Error(w, "Topic not found", http.StatusNotFound)
		default:
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
		log.Printf("Error registering live activity token: %v", err)
		return
	}

	s.respond(w, r, map[string]any{
		"user":  user,
		"token": token,
	}, http.StatusCreated)
}

func (s *Server) handleRegisterUserToLATopic(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "userID")
	topicID := chi.URLParam(r, "topicID")
	if userID == "" || topicID == "" {
		http.Error(w, "userID and topicID are required", http.StatusBadRequest)
		return
	}

	sub, err := s.service.RegisterUserToLATopic(r.Context(), userID, topicID)
	if err != nil {
		switch {
		case errors.Is(err, storage.Errors.NotFound):
			http.Error(w, "User or topic not found", http.StatusNotFound)
		default:
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
		log.Printf("Error registering user to live activity topic: %v", err)
		return
	}

	s.respond(w, r, sub, http.StatusCreated)
}

func (s *Server) handleCreateLAJob(w http.ResponseWriter, r *http.Request) {
	var req createLiveActivityJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	action, err := model.ParseLiveActivityAction(req.Action)
	if err != nil {
		http.Error(w, "Invalid action (must be 'start', 'update', or 'end')", http.StatusBadRequest)
		return
	}

	result, err := s.service.CreateLADispatch(r.Context(), service.LADispatchRequest{
		Action:       action,
		ActivityID:   req.ActivityID,
		ActivityType: req.ActivityType,
		UserID:       req.UserID,
		TopicID:      req.TopicID,
		Payload:      req.Payload,
		Options:      req.Options,
		ExpiresAt:    req.ExpiresAt,
	})
	if err != nil {
		switch {
		case errors.Is(err, storage.Errors.NotFound):
			http.Error(w, "Live activity job not found", http.StatusNotFound)
		default:
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
		log.Printf("Error creating live activity dispatch: %v", err)
		return
	}

	if result.Dispatch != nil {
		jobItem := model.JobItem{
			ID:           result.Dispatch.ID,
			JobType:      model.JobTypeLA,
			TopicID:      result.Job.TopicID,
			UserID:       result.Job.UserID,
			LAAction:     result.Dispatch.Action,
			LAJobID:      result.Job.ID,
			LADispatchID: result.Dispatch.ID,
			LAActivityID: result.Job.ActivityID,
			LAActivity:   result.Job.ActivityType,
			LAPayload:    result.Dispatch.Payload,
			LAOptions:    result.Dispatch.Options,
		}

		if err := s.enqueueImmediateLADispatch(result.Job, result.Dispatch, jobItem); err != nil {
			log.Printf("Error enqueueing live activity dispatch %s: %v", result.Dispatch.ID, err)
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, pipeline.ErrClosed) {
				http.Error(w, "Job queue unavailable", http.StatusServiceUnavailable)
				return
			}
			http.Error(w, "Error queueing live activity dispatch", http.StatusInternalServerError)
			return
		}
	}

	response := map[string]any{
		"activityId": result.Job.ActivityID,
		"status":     result.Status,
	}
	if result.Dispatch != nil {
		response["dispatchId"] = result.Dispatch.ID
	}
	if result.Status == "already_started" || result.Status == "closed" {
		s.respond(w, r, response, http.StatusOK)
		return
	}

	s.respond(w, r, response, http.StatusAccepted)
}
