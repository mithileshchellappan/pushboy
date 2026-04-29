package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type LADispatchRequest struct {
	Action       model.LiveActivityAction
	ActivityID   string
	ActivityType string
	UserID       string
	TopicID      string
	Payload      json.RawMessage
	Options      json.RawMessage
	ExpiresAt    string
}

type LADispatchResult struct {
	Job      *storage.LiveActivityJob
	Dispatch *storage.LiveActivityDispatch
	Status   string
}

const defaultLAJobLifetime = 8 * time.Hour

func parseLAExpiry(expiresAt string, now time.Time) (*time.Time, error) {
	if expiresAt == "" {
		return nil, nil
	}

	t, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return nil, fmt.Errorf("invalid expiresAt format, must be RFC3339: %w", err)
	}
	if !t.After(now) {
		return nil, fmt.Errorf("expiresAt must be in the future")
	}
	t = t.UTC()
	return &t, nil
}

func laExpired(expiresAt string, now time.Time) bool {
	if expiresAt == "" {
		return false
	}

	t, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return false
	}
	return !t.After(now)
}

func (s *PushboyService) RegisterLAToken(ctx context.Context, userID string, platform model.Platform, tokenType model.LiveActivityTokenType, tokenValue string, topicID string, expiresAt string) (*storage.LiveActivityToken, *storage.User, error) {
	if userID == "" {
		return nil, nil, fmt.Errorf("userId is required")
	}
	if tokenValue == "" {
		return nil, nil, fmt.Errorf("token is required")
	}

	now := time.Now().UTC()
	expiryTime, err := parseLAExpiry(expiresAt, now)
	if err != nil {
		return nil, nil, err
	}

	switch platform {
	case model.FCM:
		tokenType = model.LiveActivityTokenTypeUpdate
	case model.APNS:
		if tokenType == model.LiveActivityTokenTypeUpdate {
			maxExpiry := now.Add(defaultLAJobLifetime)
			if expiryTime == nil || expiryTime.After(maxExpiry) {
				expiryTime = &maxExpiry
			}
		}
	}

	expiresAt = ""
	if expiryTime != nil {
		expiresAt = expiryTime.Format(time.RFC3339)
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
		if _, err := s.store.SubscribeUserToLATopic(ctx, sub); err != nil {
			return nil, nil, fmt.Errorf("failed to subscribe user to LA topic: %w", err)
		}
	}

	token := &storage.LiveActivityToken{
		ID:         uuid.New().String(),
		UserID:     userID,
		Platform:   platform,
		TokenType:  tokenType,
		Token:      tokenValue,
		ExpiresAt:  expiresAt,
		CreatedAt:  now.Format(time.RFC3339),
		LastSeenAt: now.Format(time.RFC3339),
	}

	stored, err := s.store.UpsertLiveActivityToken(ctx, token)
	if err != nil {
		return nil, nil, err
	}
	return stored, user, nil
}

func (s *PushboyService) DeleteLAToken(ctx context.Context, userID string, tokenValue string) error {
	if userID == "" {
		return fmt.Errorf("userId is required")
	}
	if tokenValue == "" {
		return fmt.Errorf("token is required")
	}

	return s.store.InvalidateLiveActivityToken(ctx, userID, tokenValue)
}

func (s *PushboyService) RegisterUserToLATopic(ctx context.Context, userID string, topicID string) (*storage.LiveActivityUserTopicSubscription, error) {
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
	return s.store.SubscribeUserToLATopic(ctx, sub)
}

func (s *PushboyService) resolveLAJobByScope(ctx context.Context, activityType string, userID string, topicID string) (*storage.LiveActivityJob, error) {
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
		return s.store.FindLAJobByUserScope(ctx, activityType, userID)
	}
	return s.store.FindLAJobByTopicScope(ctx, activityType, topicID)
}

func (s *PushboyService) getLAJob(ctx context.Context, req LADispatchRequest) (*storage.LiveActivityJob, error) {
	if req.ActivityID != "" {
		return s.store.GetLAJobByActivityID(ctx, req.ActivityID)
	}
	return s.resolveLAJobByScope(ctx, req.ActivityType, req.UserID, req.TopicID)
}

func (s *PushboyService) UpdateLADispatchStatus(ctx context.Context, dispatchID string, status string) error {
	return s.store.UpdateLADispatchStatus(ctx, dispatchID, status)
}

func (s *PushboyService) FailLAJobIfActive(ctx context.Context, jobID string) error {
	if err := s.store.FailLAJobIfActive(ctx, jobID); err != nil && !errors.Is(err, storage.Errors.NotFound) {
		return err
	}
	return nil
}

func (s *PushboyService) CreateLADispatch(ctx context.Context, req LADispatchRequest) (*LADispatchResult, error) {
	if req.Action == "" {
		return nil, fmt.Errorf("action is required")
	}
	if req.ActivityID != "" && (req.UserID != "" || req.TopicID != "") && req.Action != model.LiveActivityActionStart {
		return nil, fmt.Errorf("provide either activityId or userId/topicId, not both")
	}
	payloadText := strings.TrimSpace(string(req.Payload))
	if req.Action != model.LiveActivityActionEnd && (payloadText == "" || payloadText == "null") {
		return nil, fmt.Errorf("payload is required")
	}

	now := time.Now().UTC()
	switch req.Action {
	case model.LiveActivityActionStart:
		return s.createLAStart(ctx, req, now)
	case model.LiveActivityActionUpdate:
		return s.createLAUpdate(ctx, req, now)
	case model.LiveActivityActionEnd:
		return s.createLAEnd(ctx, req, now)
	default:
		return nil, fmt.Errorf("unsupported action: %s", req.Action)
	}
}

func (s *PushboyService) createLAStart(ctx context.Context, req LADispatchRequest, now time.Time) (*LADispatchResult, error) {
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
	_, err := model.ParseLiveActivityOptions(req.Options)
	if err != nil {
		return nil, err
	}

	expiryTime, err := parseLAExpiry(req.ExpiresAt, now)
	if err != nil {
		return nil, err
	}
	if expiryTime == nil {
		defaultExpiry := now.Add(defaultLAJobLifetime)
		expiryTime = &defaultExpiry
	}

	job := &storage.LiveActivityJob{
		ID:            uuid.New().String(),
		ActivityID:    req.ActivityID,
		ActivityType:  req.ActivityType,
		UserID:        req.UserID,
		TopicID:       req.TopicID,
		Status:        model.LiveActivityJobStatusActive,
		LatestPayload: req.Payload,
		Options:       req.Options,
		CreatedAt:     now.Format(time.RFC3339),
		UpdatedAt:     now.Format(time.RFC3339),
		ExpiresAt:     expiryTime.Format(time.RFC3339),
	}

	storedJob, created, err := s.store.CreateOrGetLAStartJob(ctx, job)
	if err != nil {
		return nil, err
	}
	if !created {
		return &LADispatchResult{
			Job:    storedJob,
			Status: "already_started",
		}, nil
	}

	dispatch, err := s.createLADispatchRow(ctx, storedJob, req.Action, req.Payload, req.Options, now)
	if err != nil {
		if rollbackErr := s.store.RollbackLAStartJob(ctx, storedJob.ID); rollbackErr != nil {
			return nil, fmt.Errorf("failed to create LA dispatch: %w (rollback failed: %v)", err, rollbackErr)
		}
		return nil, err
	}

	return &LADispatchResult{
		Job:      storedJob,
		Dispatch: dispatch,
		Status:   "started",
	}, nil
}

func (s *PushboyService) createLAUpdate(ctx context.Context, req LADispatchRequest, now time.Time) (*LADispatchResult, error) {
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

	job, err := s.getLAJob(ctx, req)
	if err != nil {
		if errors.Is(err, storage.Errors.NotFound) {
			return nil, storage.Errors.NotFound
		}
		return nil, err
	}
	if job == nil || job.Status != model.LiveActivityJobStatusActive || job.ClosedAt != "" || laExpired(job.ExpiresAt, now) {
		return nil, storage.Errors.NotFound
	}

	if err := s.store.UpdateLAJobPayloadIfActive(ctx, job.ID, req.Payload, req.Options, now.Format(time.RFC3339)); err != nil {
		if errors.Is(err, storage.Errors.NotFound) {
			return nil, storage.Errors.NotFound
		}
		return nil, err
	}
	job.LatestPayload = req.Payload
	job.Options = req.Options
	job.UpdatedAt = now.Format(time.RFC3339)

	dispatch, err := s.createLADispatchRow(ctx, job, req.Action, req.Payload, req.Options, now)
	if err != nil {
		return nil, err
	}

	return &LADispatchResult{
		Job:      job,
		Dispatch: dispatch,
		Status:   "updated",
	}, nil
}

func (s *PushboyService) createLAEnd(ctx context.Context, req LADispatchRequest, now time.Time) (*LADispatchResult, error) {
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

	job, err := s.getLAJob(ctx, req)
	if err != nil {
		if errors.Is(err, storage.Errors.NotFound) {
			return nil, storage.Errors.NotFound
		}
		return nil, err
	}
	if job == nil || laExpired(job.ExpiresAt, now) || job.Status == model.LiveActivityJobStatusFailed {
		return nil, storage.Errors.NotFound
	}
	if job.Status == model.LiveActivityJobStatusClosed {
		return &LADispatchResult{
			Job:    job,
			Status: "closed",
		}, nil
	}

	dispatch, err := s.createLADispatchRow(ctx, job, req.Action, req.Payload, req.Options, now)
	if err != nil {
		return nil, err
	}

	return &LADispatchResult{
		Job:      job,
		Dispatch: dispatch,
		Status:   "ended",
	}, nil
}

func (s *PushboyService) createLADispatchRow(ctx context.Context, job *storage.LiveActivityJob, action model.LiveActivityAction, payload json.RawMessage, options json.RawMessage, now time.Time) (*storage.LiveActivityDispatch, error) {
	dispatchPayload := payload
	dispatchPayloadText := strings.TrimSpace(string(dispatchPayload))
	if dispatchPayloadText == "" || dispatchPayloadText == "null" {
		dispatchPayload = job.LatestPayload
		dispatchPayloadText = strings.TrimSpace(string(dispatchPayload))
	}
	if dispatchPayloadText == "" || dispatchPayloadText == "null" {
		dispatchPayload = json.RawMessage(`{}`)
	}

	dispatch := &storage.LiveActivityDispatch{
		ID:                uuid.New().String(),
		LiveActivityJobID: job.ID,
		Action:            action,
		Payload:           dispatchPayload,
		Options:           options,
		Status:            "QUEUED",
		CreatedAt:         now.Format(time.RFC3339),
	}
	return s.store.CreateLADispatch(ctx, dispatch)
}
