package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type PushboyService struct {
	store            storage.Store
	broadcastTopicID string // ID of the broadcast topic (all users auto-subscribe)
}

func NewPushBoyService(s storage.Store, broadcastTopicID string) *PushboyService {
	return &PushboyService{store: s, broadcastTopicID: broadcastTopicID}
}

// validateScheduledAt validates that the scheduledAt string is in RFC3339 format and is in the future
func validateScheduledAt(scheduledAt string) error {
	if scheduledAt == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, scheduledAt)
	if err != nil {
		return fmt.Errorf("invalid scheduledAt format, must be RFC3339 (e.g., 2025-01-15T10:00:00Z): %w", err)
	}
	if t.Before(time.Now()) {
		return fmt.Errorf("scheduledAt must be in the future")
	}
	return nil
}

// subscribeToBroadcast subscribes a user to the broadcast topic if configured
func (s *PushboyService) subscribeToBroadcast(ctx context.Context, userID string) {
	if s.broadcastTopicID == "" {
		return
	}
	sub := &storage.UserTopicSubscription{
		UserID:  userID,
		TopicID: s.broadcastTopicID,
	}
	if _, err := s.store.SubscribeUserToTopic(ctx, sub); err != nil && !errors.Is(err, storage.Errors.AlreadyExists) {
		log.Printf("Warning: failed to subscribe user %s to broadcast topic: %v", userID, err)
	}
}

// User operations

func (s *PushboyService) CreateUser(ctx context.Context, userID string) (*storage.User, error) {
	if userID == "" {
		userID = uuid.New().String()
	}

	user := &storage.User{ID: userID}
	user, err := s.store.CreateUser(ctx, user)
	if err != nil {
		return nil, err
	}

	// Auto-subscribe to broadcast topic
	s.subscribeToBroadcast(ctx, userID)

	return user, nil
}

func (s *PushboyService) GetUser(ctx context.Context, userID string) (*storage.User, error) {
	return s.store.GetUser(ctx, userID)
}

func (s *PushboyService) ListUsers(ctx context.Context, query storage.PageQuery) ([]storage.User, error) {
	return s.store.ListUsers(ctx, query)
}

func (s *PushboyService) requireUser(ctx context.Context, userID string) (*storage.User, error) {
	return s.store.GetUser(ctx, userID)
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

func (s *PushboyService) DeleteUser(ctx context.Context, userID string) error {
	return s.store.DeleteUser(ctx, userID)
}

// Token operations

func (s *PushboyService) RegisterToken(ctx context.Context, userID string, platform model.Platform, tokenValue string) (*storage.Token, *storage.User, error) {
	if platform != "apns" && platform != "fcm" {
		return nil, nil, fmt.Errorf("invalid platform: must be 'apns' or 'fcm'")
	}

	// Generate user ID if not provided
	if userID == "" {
		userID = uuid.New().String()
	}

	// Try to get existing user, create if not exists
	user, err := s.store.GetUser(ctx, userID)
	isNewUser := false
	if err != nil {
		if errors.Is(err, storage.Errors.NotFound) {
			// User doesn't exist, create them
			user = &storage.User{ID: userID}
			user, err = s.store.CreateUser(ctx, user)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to create user: %w", err)
			}
			isNewUser = true
		} else {
			return nil, nil, fmt.Errorf("failed to get user: %w", err)
		}
	}

	// Auto-subscribe new users to broadcast topic
	if isNewUser {
		s.subscribeToBroadcast(ctx, userID)
	}

	// Register the token
	token := &storage.Token{
		ID:       uuid.New().String(),
		UserID:   userID,
		Platform: platform,
		Token:    tokenValue,
	}

	token, err = s.store.CreateToken(ctx, token)
	if err != nil {
		return nil, user, err
	}

	return token, user, nil
}

func (s *PushboyService) GetUserTokens(ctx context.Context, userID string) ([]storage.Token, error) {
	return s.store.GetTokensByUserID(ctx, userID)
}

func (s *PushboyService) DeleteToken(ctx context.Context, tokenID string) error {
	return s.store.DeleteToken(ctx, tokenID)
}

// Topic operations

func (s *PushboyService) CreateTopic(ctx context.Context, ID string, name string) (*storage.Topic, error) {
	ID = strings.TrimSpace(ID)
	name = strings.TrimSpace(name)
	if ID == "" || name == "" {
		return nil, fmt.Errorf("topic id and name are required")
	}
	if strings.Contains(ID, " ") {
		return nil, fmt.Errorf("topic id cannot contain spaces")
	}

	topic := &storage.Topic{ID: ID, Name: name}

	err := s.store.CreateTopic(ctx, topic)
	if err != nil {
		return nil, err
	}

	return topic, nil
}

func (s *PushboyService) ListTopics(ctx context.Context) ([]storage.Topic, error) {
	return s.store.ListTopics(ctx)
}

func (s *PushboyService) GetTopicByID(ctx context.Context, topicID string) (*storage.Topic, error) {
	return s.store.GetTopicByID(ctx, topicID)
}

func (s *PushboyService) requireTopic(ctx context.Context, topicID string) (*storage.Topic, error) {
	return s.store.GetTopicByID(ctx, topicID)
}

func (s *PushboyService) DeleteTopic(ctx context.Context, topicID string) error {
	return s.store.DeleteTopic(ctx, topicID)
}

func (s *PushboyService) GetTopicSubscriberCount(ctx context.Context, topicID string) (int, error) {
	return s.store.GetTopicSubscriberCount(ctx, topicID)
}

func (s *PushboyService) ListTopicSubscribers(ctx context.Context, topicID string, query storage.PageQuery) ([]storage.TopicSubscriber, error) {
	if _, err := s.requireTopic(ctx, topicID); err != nil {
		return nil, err
	}

	return s.store.ListTopicSubscribers(ctx, topicID, query)
}

// User-Topic subscription operations

func (s *PushboyService) SubscribeUserToTopic(ctx context.Context, userID string, topicID string) (*storage.UserTopicSubscription, error) {
	// Verify user exists
	_, err := s.requireUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("user not found: %w", err)
	}

	// Verify topic exists
	_, err = s.requireTopic(ctx, topicID)
	if err != nil {
		return nil, fmt.Errorf("topic not found: %w", err)
	}

	sub := &storage.UserTopicSubscription{
		UserID:  userID,
		TopicID: topicID,
	}

	return s.store.SubscribeUserToTopic(ctx, sub)
}

func (s *PushboyService) UnsubscribeUserFromTopic(ctx context.Context, userID string, topicID string) error {
	return s.store.UnsubscribeUserFromTopic(ctx, userID, topicID)
}

func (s *PushboyService) GetUserSubscriptions(ctx context.Context, userID string) ([]storage.UserTopicSubscription, error) {
	return s.store.GetUserSubscriptions(ctx, userID)
}

// Send to user operations

func (s *PushboyService) SendToUser(ctx context.Context, userID string, payload *model.NotificationPayload, scheduledAt string) (*storage.PublishJob, error) {
	// Validate scheduledAt format
	if err := validateScheduledAt(scheduledAt); err != nil {
		return nil, err
	}

	job := &storage.PublishJob{
		ID:           uuid.New().String(),
		UserID:       userID,
		Payload:      payload,
		Status:       notificationJobStatusForSchedule(scheduledAt),
		TotalCount:   0,
		SuccessCount: 0,
		FailureCount: 0,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		ScheduledAt:  scheduledAt,
	}

	return s.store.CreateUserPublishJob(ctx, job)
}

// Publish job operations

func (s *PushboyService) CreatePublishJob(ctx context.Context, topicID string, payload *model.NotificationPayload, scheduledAt string) (*storage.PublishJob, error) {
	if _, err := s.requireTopic(ctx, topicID); err != nil {
		return nil, err
	}

	// Validate scheduledAt format
	if err := validateScheduledAt(scheduledAt); err != nil {
		return nil, err
	}

	job := &storage.PublishJob{
		ID:           uuid.New().String(),
		TopicID:      topicID,
		Payload:      payload,
		Status:       notificationJobStatusForSchedule(scheduledAt),
		TotalCount:   0,
		SuccessCount: 0,
		FailureCount: 0,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		ScheduledAt:  scheduledAt,
	}

	return s.store.CreatePublishJob(ctx, job)
}

func (s *PushboyService) GetJobStatus(ctx context.Context, jobID string) (*storage.PublishJob, error) {
	return s.store.GetJobStatus(ctx, jobID)
}

func (s *PushboyService) ListTopicNotifications(ctx context.Context, topicID string, query storage.NotificationListQuery) ([]storage.PublishJob, error) {
	if _, err := s.requireTopic(ctx, topicID); err != nil {
		return nil, err
	}

	return s.store.ListTopicNotifications(ctx, topicID, query)
}

func (s *PushboyService) ListUserNotifications(ctx context.Context, userID string, query storage.NotificationListQuery) ([]storage.PublishJob, error) {
	if _, err := s.requireUser(ctx, userID); err != nil {
		return nil, err
	}

	return s.store.ListUserNotifications(ctx, userID, query)
}

func (s *PushboyService) UpdateJobStatus(ctx context.Context, jobID string, status model.NotificationJobStatus) error {
	return s.store.UpdateJobStatus(ctx, jobID, status)
}

func notificationJobStatusForSchedule(scheduledAt string) model.NotificationJobStatus {
	if scheduledAt == "" {
		return model.NotificationJobStatusQueued
	}
	return model.NotificationJobStatusScheduled
}
