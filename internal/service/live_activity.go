package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type LiveActivityDispatchRequest struct {
	Action       model.LiveActivityAction
	ActivityType string
	JobID        string
	UserID       string
	TopicID      string
	Payload      json.RawMessage
	Options      json.RawMessage
	ExpiresAt    string
}

type LiveActivityDispatchResult struct {
	Job      *storage.LiveActivityJob
	Dispatch *storage.LiveActivityDispatch
	Status   string
}

func validateLiveActivityExpiry(expiresAt string) error {
	if expiresAt == "" {
		return nil
	}

	t, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return fmt.Errorf("invalid expiresAt format, must be RFC3339: %w", err)
	}
	if t.Before(time.Now()) {
		return fmt.Errorf("expiresAt must be in the future")
	}
	return nil
}

func (s *PushboyService) ensureUser(ctx context.Context, userID string) (*storage.User, error) {
	user, err := s.store.GetUser(ctx, userID)
	if err == nil {
		return user, nil
	}
	if !errors.Is(err, storage.Errors.NotFound) {
		return nil, err
	}

	return s.CreateUser(ctx, userID)
}

func (s *PushboyService) RegisterLiveActivityToken(ctx context.Context, userID string, platform model.Platform, tokenType model.LiveActivityTokenType, tokenValue string, topicID string, expiresAt string) (*storage.LiveActivityToken, *storage.User, error) {
	if userID == "" {
		return nil, nil, fmt.Errorf("userId is required")
	}
	if tokenValue == "" {
		return nil, nil, fmt.Errorf("token is required")
	}
	if err := validateLiveActivityExpiry(expiresAt); err != nil {
		return nil, nil, err
	}

	user, err := s.ensureUser(ctx, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to ensure user: %w", err)
	}

	if topicID != "" {
		if _, err := s.store.GetTopicByID(ctx, topicID); err != nil {
			return nil, nil, fmt.Errorf("topic not found: %w", err)
		}
		sub := &storage.LiveActivityUserTopicSubscription{
			ID:        uuid.New().String(),
			UserID:    userID,
			TopicID:   topicID,
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}
		if _, err := s.store.SubscribeUserToLiveActivityTopic(ctx, sub); err != nil {
			return nil, nil, fmt.Errorf("failed to subscribe user to live activity topic: %w", err)
		}
	}

	token := &storage.LiveActivityToken{
		ID:         uuid.New().String(),
		UserID:     userID,
		Platform:   platform,
		TokenType:  tokenType,
		Token:      tokenValue,
		ExpiresAt:  expiresAt,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastSeenAt: time.Now().UTC().Format(time.RFC3339),
	}

	stored, err := s.store.UpsertLiveActivityToken(ctx, token)
	if err != nil {
		return nil, nil, err
	}
	return stored, user, nil
}

func (s *PushboyService) RegisterUserToLiveActivityTopic(ctx context.Context, userID string, topicID string) (*storage.LiveActivityUserTopicSubscription, error) {
	if userID == "" || topicID == "" {
		return nil, fmt.Errorf("userId and topicId are required")
	}
	if _, err := s.store.GetUser(ctx, userID); err != nil {
		return nil, fmt.Errorf("user not found: %w", err)
	}
	if _, err := s.store.GetTopicByID(ctx, topicID); err != nil {
		return nil, fmt.Errorf("topic not found: %w", err)
	}

	sub := &storage.LiveActivityUserTopicSubscription{
		ID:        uuid.New().String(),
		UserID:    userID,
		TopicID:   topicID,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	return s.store.SubscribeUserToLiveActivityTopic(ctx, sub)
}

func (s *PushboyService) resolveLiveActivityJobByScope(ctx context.Context, activityType string, userID string, topicID string) (*storage.LiveActivityJob, error) {
	if userID != "" && topicID != "" {
		return nil, fmt.Errorf("provide either userId or topicId, not both")
	}
	if userID == "" && topicID == "" {
		return nil, fmt.Errorf("either userId or topicId is required")
	}
	if activityType == "" {
		return nil, fmt.Errorf("activityType is required")
	}

	if userID != "" {
		return s.store.FindLiveActivityJobByUserScope(ctx, activityType, userID)
	}
	return s.store.FindLiveActivityJobByTopicScope(ctx, activityType, topicID)
}

func (s *PushboyService) UpdateLiveActivityDispatchStatus(ctx context.Context, dispatchID string, status string) error {
	return s.store.UpdateLiveActivityDispatchStatus(ctx, dispatchID, status)
}

func (s *PushboyService) RecoverLiveActivityJobAfterDispatchFailure(ctx context.Context, jobID string, action model.LiveActivityAction) error {
	if action != model.LiveActivityActionStart && action != model.LiveActivityActionEnd {
		return nil
	}

	job, err := s.store.GetLiveActivityJob(ctx, jobID)
	if err != nil {
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	switch action {
	case model.LiveActivityActionStart:
		job.Status = model.LiveActivityJobStatusFailed
		job.ClosedAt = now
	case model.LiveActivityActionEnd:
		job.Status = model.LiveActivityJobStatusActive
		job.ClosedAt = ""
	}
	job.UpdatedAt = now

	return s.store.UpdateLiveActivityJob(ctx, job)
}

func (s *PushboyService) CreateLiveActivityDispatch(ctx context.Context, req LiveActivityDispatchRequest) (*LiveActivityDispatchResult, error) {
	if req.Action == "" {
		return nil, fmt.Errorf("action is required")
	}
	if req.JobID != "" && (req.UserID != "" || req.TopicID != "") {
		return nil, fmt.Errorf("provide either jobId or userId/topicId, not both")
	}
	if req.Action != model.LiveActivityActionEnd && len(req.Payload) == 0 {
		return nil, fmt.Errorf("payload is required")
	}
	if err := validateLiveActivityExpiry(req.ExpiresAt); err != nil {
		return nil, err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	var job *storage.LiveActivityJob
	var err error

	if req.JobID != "" {
		job, err = s.store.GetLiveActivityJob(ctx, req.JobID)
		if err != nil {
			return nil, err
		}
	} else {
		job, err = s.resolveLiveActivityJobByScope(ctx, req.ActivityType, req.UserID, req.TopicID)
		if err != nil && !errors.Is(err, storage.Errors.NotFound) {
			return nil, err
		}
	}

	switch req.Action {
	case model.LiveActivityActionStart:
		if req.JobID != "" {
			return nil, fmt.Errorf("jobId is not allowed for start")
		}
		if req.ActivityType == "" {
			return nil, fmt.Errorf("activityType is required")
		}
		if req.UserID == "" && req.TopicID == "" {
			return nil, fmt.Errorf("either userId or topicId is required")
		}
		if req.UserID != "" && req.TopicID != "" {
			return nil, fmt.Errorf("provide either userId or topicId, not both")
		}
		if req.UserID != "" {
			if _, err := s.store.GetUser(ctx, req.UserID); err != nil {
				return nil, fmt.Errorf("user not found: %w", err)
			}
		}
		if req.TopicID != "" {
			if _, err := s.store.GetTopicByID(ctx, req.TopicID); err != nil {
				return nil, fmt.Errorf("topic not found: %w", err)
			}
		}

		if job != nil {
			return &LiveActivityDispatchResult{
				Job:    job,
				Status: "already_active",
			}, nil
		}

		job = &storage.LiveActivityJob{
			ID:            uuid.New().String(),
			ActivityType:  req.ActivityType,
			UserID:        req.UserID,
			TopicID:       req.TopicID,
			Status:        model.LiveActivityJobStatusActive,
			LatestPayload: req.Payload,
			Options:       req.Options,
			CreatedAt:     now,
			UpdatedAt:     now,
			ExpiresAt:     req.ExpiresAt,
		}
		if _, err := s.store.CreateLiveActivityJob(ctx, job); err != nil {
			if errors.Is(err, storage.Errors.AlreadyExists) {
				existing, findErr := s.resolveLiveActivityJobByScope(ctx, req.ActivityType, req.UserID, req.TopicID)
				if findErr == nil {
					return &LiveActivityDispatchResult{Job: existing, Status: "already_active"}, nil
				}
			}
			return nil, err
		}

	case model.LiveActivityActionUpdate:
		if job == nil {
			return nil, storage.Errors.NotFound
		}
		if job.Status != model.LiveActivityJobStatusActive {
			return nil, fmt.Errorf("live activity job is not active")
		}

		job.LatestPayload = req.Payload
		job.Options = req.Options
		job.UpdatedAt = now
		if err := s.store.UpdateLiveActivityJob(ctx, job); err != nil {
			return nil, err
		}

	case model.LiveActivityActionEnd:
		if job == nil {
			return nil, storage.Errors.NotFound
		}
		if job.Status == model.LiveActivityJobStatusClosed || job.Status == model.LiveActivityJobStatusExpired || job.Status == model.LiveActivityJobStatusFailed {
			return nil, storage.Errors.NotFound
		}
		if job.Status == model.LiveActivityJobStatusClosing {
			return &LiveActivityDispatchResult{
				Job:    job,
				Status: "closing",
			}, nil
		}

		job.Status = model.LiveActivityJobStatusClosing
		if len(req.Payload) > 0 {
			job.LatestPayload = req.Payload
		}
		job.Options = req.Options
		job.UpdatedAt = now
		if err := s.store.UpdateLiveActivityJob(ctx, job); err != nil {
			return nil, err
		}

	default:
		return nil, fmt.Errorf("unsupported action: %s", req.Action)
	}

	dispatchPayload := req.Payload
	if len(dispatchPayload) == 0 {
		dispatchPayload = job.LatestPayload
	}
	if len(dispatchPayload) == 0 {
		dispatchPayload = json.RawMessage(`{}`)
	}

	dispatch := &storage.LiveActivityDispatch{
		ID:                uuid.New().String(),
		LiveActivityJobID: job.ID,
		Action:            req.Action,
		Payload:           dispatchPayload,
		Options:           req.Options,
		Status:            "QUEUED",
		CreatedAt:         now,
	}
	if _, err := s.store.CreateLiveActivityDispatch(ctx, dispatch); err != nil {
		return nil, err
	}

	status := "queued"
	switch req.Action {
	case model.LiveActivityActionStart:
		status = "started"
	case model.LiveActivityActionUpdate:
		status = "updated"
	case model.LiveActivityActionEnd:
		status = "closing"
	}

	return &LiveActivityDispatchResult{
		Job:      job,
		Dispatch: dispatch,
		Status:   status,
	}, nil
}
