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
	ActivityID   string
	ActivityType string
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

const defaultLiveActivityJobLifetime = 8 * time.Hour

func normalizeLiveActivityExpiryAt(expiresAt string, now time.Time) (string, error) {
	if expiresAt == "" {
		return "", nil
	}

	t, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return "", fmt.Errorf("invalid expiresAt format, must be RFC3339: %w", err)
	}
	if t.Before(now) {
		return "", fmt.Errorf("expiresAt must be in the future")
	}
	return t.UTC().Format(time.RFC3339), nil
}

func normalizeLiveActivityExpiry(expiresAt string) (string, error) {
	return normalizeLiveActivityExpiryAt(expiresAt, time.Now())
}

func normalizeLiveActivityTokenExpiry(tokenType model.LiveActivityTokenType, expiresAt string, now time.Time) (string, error) {
	normalizedExpiresAt, err := normalizeLiveActivityExpiryAt(expiresAt, now)
	if err != nil {
		return "", err
	}
	if tokenType != model.LiveActivityTokenTypeUpdate {
		return normalizedExpiresAt, nil
	}

	maxExpiry := now.UTC().Add(defaultLiveActivityJobLifetime)
	if normalizedExpiresAt == "" {
		return maxExpiry.Format(time.RFC3339), nil
	}

	expiryTime, err := time.Parse(time.RFC3339, normalizedExpiresAt)
	if err != nil {
		return "", err
	}
	if expiryTime.After(maxExpiry) {
		return maxExpiry.Format(time.RFC3339), nil
	}
	return normalizedExpiresAt, nil
}

func isLiveActivityExpired(expiresAt string) bool {
	if expiresAt == "" {
		return false
	}

	t, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return false
	}
	return !t.After(time.Now())
}

func (s *PushboyService) RegisterLiveActivityToken(ctx context.Context, userID string, platform model.Platform, tokenType model.LiveActivityTokenType, tokenValue string, topicID string, expiresAt string) (*storage.LiveActivityToken, *storage.User, error) {
	if userID == "" {
		return nil, nil, fmt.Errorf("userId is required")
	}
	if tokenValue == "" {
		return nil, nil, fmt.Errorf("token is required")
	}
	now := time.Now().UTC()
	normalizedExpiresAt, err := normalizeLiveActivityTokenExpiry(tokenType, expiresAt, now)
	if err != nil {
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
			CreatedAt: now.Format(time.RFC3339),
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
		ExpiresAt:  normalizedExpiresAt,
		CreatedAt:  now.Format(time.RFC3339),
		LastSeenAt: now.Format(time.RFC3339),
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

	switch action {
	case model.LiveActivityActionStart:
		if err := s.store.MarkLiveActivityJobFailedIfActive(ctx, jobID); err != nil && !errors.Is(err, storage.Errors.NotFound) {
			return err
		}
	case model.LiveActivityActionEnd:
		if err := s.store.ReopenLiveActivityJobIfClosing(ctx, jobID); err != nil && !errors.Is(err, storage.Errors.NotFound) {
			return err
		}
	}
	return nil
}

func (s *PushboyService) CreateLiveActivityDispatch(ctx context.Context, req LiveActivityDispatchRequest) (*LiveActivityDispatchResult, error) {
	if req.Action == "" {
		return nil, fmt.Errorf("action is required")
	}
	if req.ActivityID != "" && (req.UserID != "" || req.TopicID != "") && req.Action != model.LiveActivityActionStart {
		return nil, fmt.Errorf("provide either activityId or userId/topicId, not both")
	}
	if req.Action != model.LiveActivityActionEnd && len(req.Payload) == 0 {
		return nil, fmt.Errorf("payload is required")
	}
	nowTime := time.Now().UTC()
	normalizedExpiresAt, normalizeErr := normalizeLiveActivityExpiryAt(req.ExpiresAt, nowTime)
	if normalizeErr != nil {
		return nil, normalizeErr
	}

	now := nowTime.Format(time.RFC3339)
	var job *storage.LiveActivityJob

	loadJob := func() (*storage.LiveActivityJob, error) {
		if req.ActivityID != "" {
			return s.store.GetLiveActivityJobByActivityID(ctx, req.ActivityID)
		}

		job, err := s.resolveLiveActivityJobByScope(ctx, req.ActivityType, req.UserID, req.TopicID)
		if err != nil {
			return nil, err
		}
		return job, nil
	}

	switch req.Action {
	case model.LiveActivityActionStart:
		if req.ActivityID == "" {
			return nil, fmt.Errorf("activityId is required")
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

		job = &storage.LiveActivityJob{
			ID:            uuid.New().String(),
			ActivityID:    req.ActivityID,
			ActivityType:  req.ActivityType,
			UserID:        req.UserID,
			TopicID:       req.TopicID,
			Status:        model.LiveActivityJobStatusActive,
			LatestPayload: req.Payload,
			Options:       req.Options,
			CreatedAt:     now,
			UpdatedAt:     now,
			ExpiresAt:     normalizedExpiresAt,
		}
		if job.ExpiresAt == "" {
			job.ExpiresAt = nowTime.Add(defaultLiveActivityJobLifetime).Format(time.RFC3339)
		}
		storedJob, created, err := s.store.CreateOrGetLiveActivityStartJob(ctx, job)
		if err != nil {
			return nil, err
		}
		job = storedJob
		if !created {
			return &LiveActivityDispatchResult{
				Job:    job,
				Status: "already_started",
			}, nil
		}

	case model.LiveActivityActionUpdate:
		if req.ActivityID == "" {
			if req.ActivityType == "" {
				return nil, fmt.Errorf("activityType is required")
			}
			if req.UserID == "" && req.TopicID == "" {
				return nil, fmt.Errorf("either userId or topicId is required")
			}
			if req.UserID != "" && req.TopicID != "" {
				return nil, fmt.Errorf("provide either userId or topicId, not both")
			}
		}

		var err error
		job, err = loadJob()
		if err != nil {
			if errors.Is(err, storage.Errors.NotFound) {
				return nil, storage.Errors.NotFound
			}
			return nil, err
		}
		if job == nil || job.ClosedAt != "" || job.Status == model.LiveActivityJobStatusClosed || job.Status == model.LiveActivityJobStatusFailed || isLiveActivityExpired(job.ExpiresAt) {
			return nil, storage.Errors.NotFound
		}
		if job.Status != model.LiveActivityJobStatusActive {
			return nil, fmt.Errorf("live activity job is not active")
		}

		if err := s.store.UpdateLiveActivityJobPayloadIfActive(ctx, job.ID, req.Payload, req.Options, now); err != nil {
			if errors.Is(err, storage.Errors.NotFound) {
				latestJob, latestErr := s.store.GetLiveActivityJob(ctx, job.ID)
				if latestErr != nil {
					if errors.Is(latestErr, storage.Errors.NotFound) {
						return nil, storage.Errors.NotFound
					}
					return nil, latestErr
				}
				if latestJob.ClosedAt != "" || isLiveActivityExpired(latestJob.ExpiresAt) || latestJob.Status == model.LiveActivityJobStatusClosed || latestJob.Status == model.LiveActivityJobStatusFailed {
					return nil, storage.Errors.NotFound
				}
				return nil, fmt.Errorf("live activity job is not active")
			}
			return nil, err
		}
		job.LatestPayload = req.Payload
		job.Options = req.Options
		job.UpdatedAt = now

	case model.LiveActivityActionEnd:
		if req.ActivityID == "" {
			if req.ActivityType == "" {
				return nil, fmt.Errorf("activityType is required")
			}
			if req.UserID == "" && req.TopicID == "" {
				return nil, fmt.Errorf("either userId or topicId is required")
			}
			if req.UserID != "" && req.TopicID != "" {
				return nil, fmt.Errorf("provide either userId or topicId, not both")
			}
		}

		var err error
		job, err = loadJob()
		if err != nil {
			if errors.Is(err, storage.Errors.NotFound) {
				return nil, storage.Errors.NotFound
			}
			return nil, err
		}
		if job == nil || job.ClosedAt != "" || isLiveActivityExpired(job.ExpiresAt) {
			return nil, storage.Errors.NotFound
		}
		if job.Status == model.LiveActivityJobStatusClosed || job.Status == model.LiveActivityJobStatusFailed {
			return nil, storage.Errors.NotFound
		}
		if job.Status == model.LiveActivityJobStatusClosing {
			return &LiveActivityDispatchResult{
				Job:    job,
				Status: "closing",
			}, nil
		}

		payloadToStore := json.RawMessage(nil)
		if len(req.Payload) > 0 {
			payloadToStore = req.Payload
		}
		if err := s.store.MarkLiveActivityJobClosingIfActive(ctx, job.ID, payloadToStore, req.Options, now); err != nil {
			if errors.Is(err, storage.Errors.NotFound) {
				latestJob, latestErr := s.store.GetLiveActivityJob(ctx, job.ID)
				if latestErr != nil {
					if errors.Is(latestErr, storage.Errors.NotFound) {
						return nil, storage.Errors.NotFound
					}
					return nil, latestErr
				}
				if latestJob.ClosedAt != "" || isLiveActivityExpired(latestJob.ExpiresAt) || latestJob.Status == model.LiveActivityJobStatusClosed || latestJob.Status == model.LiveActivityJobStatusFailed {
					return nil, storage.Errors.NotFound
				}
				if latestJob.Status == model.LiveActivityJobStatusClosing {
					return &LiveActivityDispatchResult{
						Job:    latestJob,
						Status: "closing",
					}, nil
				}
			}
			return nil, err
		}
		job.Status = model.LiveActivityJobStatusClosing
		if len(req.Payload) > 0 {
			job.LatestPayload = req.Payload
		}
		job.Options = req.Options
		job.UpdatedAt = now

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
