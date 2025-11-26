package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/dispatch"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type PushboyService struct {
	store       storage.Store
	dispatchers map[string]dispatch.Dispatcher
}

func NewPushBoyService(s storage.Store, dispatchers map[string]dispatch.Dispatcher) *PushboyService {
	return &PushboyService{store: s, dispatchers: dispatchers}
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

	return user, nil
}

func (s *PushboyService) GetUser(ctx context.Context, userID string) (*storage.User, error) {
	return s.store.GetUser(ctx, userID)
}

func (s *PushboyService) DeleteUser(ctx context.Context, userID string) error {
	return s.store.DeleteUser(ctx, userID)
}

// Token operations

func (s *PushboyService) RegisterToken(ctx context.Context, userID string, platform string, tokenValue string) (*storage.Token, *storage.User, error) {
	if platform != "apns" && platform != "fcm" {
		return nil, nil, fmt.Errorf("invalid platform: must be 'apns' or 'fcm'")
	}

	// Generate user ID if not provided
	if userID == "" {
		userID = uuid.New().String()
	}

	// Try to get existing user, create if not exists
	user, err := s.store.GetUser(ctx, userID)
	if err != nil {
		if errors.Is(err, storage.Errors.NotFound) {
			// User doesn't exist, create them
			user = &storage.User{ID: userID}
			user, err = s.store.CreateUser(ctx, user)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to create user: %w", err)
			}
		} else {
			return nil, nil, fmt.Errorf("failed to get user: %w", err)
		}
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

func (s *PushboyService) CreateTopic(ctx context.Context, name string) (*storage.Topic, error) {
	topic := &storage.Topic{Name: name}

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

func (s *PushboyService) DeleteTopic(ctx context.Context, topicID string) error {
	return s.store.DeleteTopic(ctx, topicID)
}

// User-Topic subscription operations

func (s *PushboyService) SubscribeUserToTopic(ctx context.Context, userID string, topicID string) (*storage.UserTopicSubscription, error) {
	// Verify user exists
	_, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("user not found: %w", err)
	}

	// Verify topic exists
	_, err = s.store.GetTopicByID(ctx, topicID)
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

func (s *PushboyService) SendToUser(ctx context.Context, userID string, title string, body string) (*storage.PublishJob, error) {
	job := &storage.PublishJob{
		ID:           uuid.New().String(),
		UserID:       userID,
		Title:        title,
		Body:         body,
		Status:       "QUEUED",
		TotalCount:   0,
		SuccessCount: 0,
		FailureCount: 0,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	}

	return s.store.CreateUserPublishJob(ctx, job)

}

// Publish job operations

func (s *PushboyService) CreatePublishJob(ctx context.Context, topicID string, title string, body string) (*storage.PublishJob, error) {
	job := &storage.PublishJob{
		ID:           uuid.New().String(),
		TopicID:      topicID,
		Title:        title,
		Body:         body,
		Status:       "QUEUED",
		TotalCount:   0,
		SuccessCount: 0,
		FailureCount: 0,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	}

	return s.store.CreatePublishJob(ctx, job)

}

func (s *PushboyService) GetJobStatus(ctx context.Context, jobID string) (*storage.PublishJob, error) {
	return s.store.GetJobStatus(ctx, jobID)
}
