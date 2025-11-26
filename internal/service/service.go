package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/dispatch"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type PushboyService struct {
	store       storage.Store
	dispatchers map[string]dispatch.Dispatcher
	resultsChan chan storage.DeliveryReceipt
}

func NewPushBoyService(s storage.Store, dispatchers map[string]dispatch.Dispatcher) *PushboyService {
	return &PushboyService{store: s, dispatchers: dispatchers, resultsChan: make(chan storage.DeliveryReceipt, 2000)}
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

func (s *PushboyService) SendToUser(ctx context.Context, userID string, title string, body string) error {
	tokens, err := s.store.GetTokensByUserID(ctx, userID)
	if err != nil {
		return fmt.Errorf("failed to get tokens for user: %w", err)
	}

	if len(tokens) == 0 {
		return fmt.Errorf("no tokens found for user: %s", userID)
	}

	for _, token := range tokens {
		log.Printf("SERVICE: Dispatching user notification platform=%s token=%s", token.Platform, token.Token)

		dispatcher, ok := s.dispatchers[token.Platform]
		if !ok {
			log.Printf("SERVICE: -> Dispatcher not found for platform %s", token.Platform)
			continue
		}

		payload := &dispatch.NotificationPayload{
			Title: title,
			Body:  body,
		}

		start := time.Now()
		sendErr := dispatcher.Send(ctx, &token, payload)
		duration := time.Since(start)

		if sendErr != nil {
			log.Printf("SERVICE: -> Error sending notification to platform %s: %v", token.Platform, sendErr)
		} else {
			log.Printf("SERVICE: -> Notification sent to platform %s in %s", token.Platform, duration)
		}
	}

	return nil
}

// Publish job operations

func (s *PushboyService) CreatePublishJob(ctx context.Context, topicID string, title string, body string) (*storage.PublishJob, error) {
	job := &storage.PublishJob{
		ID:           uuid.New().String(),
		TopicID:      topicID,
		Title:        title,
		Body:         body,
		Status:       "PENDING",
		TotalCount:   0,
		SuccessCount: 0,
		FailureCount: 0,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	}

	job, err := s.store.CreatePublishJob(ctx, job)
	if err != nil {
		return nil, err
	}
	return job, nil
}

func (s *PushboyService) ProcessJobs(ctx context.Context, job *storage.PublishJob) error {
	log.Printf("WORKER: Processing job... %s", job.ID)

	// Mark job as in progress
	if err := s.store.UpdateJobStatus(ctx, job.ID, "IN_PROGRESS"); err != nil {
		return fmt.Errorf("marking in_progress failed: %w", err)
	}

	// Get all subscribers for this topic
	subscribers, err := s.store.GetTopicSubscribers(ctx, job.TopicID)
	if err != nil {
		s.store.UpdateJobStatus(ctx, job.ID, "FAILED")
		return fmt.Errorf("getting topic subscribers failed: %w", err)
	}

	// For each subscriber, get their tokens and send notifications
	for _, user := range subscribers {
		tokens, err := s.store.GetTokensByUserID(ctx, user.ID)
		if err != nil {
			log.Printf("WORKER: -> Error getting tokens for user %s: %v", user.ID, err)
			continue
		}

		for _, token := range tokens {
			log.Printf("WORKER: Dispatching message to user=%s platform=%s", user.ID, token.Platform)

			dispatcher, ok := s.dispatchers[token.Platform]
			var sendErr error
			var statusReason string

			if !ok {
				sendErr = fmt.Errorf("dispatcher not found for platform %s", token.Platform)
				statusReason = "UNSUPPORTED_PLATFORM"
			} else {
				payload := &dispatch.NotificationPayload{
					Title: job.Title,
					Body:  job.Body,
				}
				start := time.Now()
				sendErr = dispatcher.Send(ctx, &token, payload)
				duration := time.Since(start)

				if sendErr != nil {
					statusReason = sendErr.Error()
					log.Printf("WORKER: -> Error sending notification to platform %s: %v", token.Platform, sendErr)
				} else {
					statusReason = "OK"
					log.Printf("WORKER: -> Notification sent to platform %s in %s", token.Platform, duration)
				}
			}

			receipt := storage.DeliveryReceipt{
				ID:           uuid.New().String(),
				JobID:        job.ID,
				TokenID:      token.ID,
				DispatchedAt: time.Now().UTC().Format(time.RFC3339),
			}

			if sendErr != nil {
				receipt.Status = "FAILED"
				receipt.StatusReason = sendErr.Error()
				s.store.IncrementJobCounters(ctx, job.ID, 0, 1)
			} else {
				receipt.Status = "SENT"
				receipt.StatusReason = statusReason
				s.store.IncrementJobCounters(ctx, job.ID, 1, 0)
			}

			s.resultsChan <- receipt
		}
	}

	if err := s.store.UpdateJobStatus(ctx, job.ID, "COMPLETED"); err != nil {
		return fmt.Errorf("updating job status failed: %w", err)
	}

	log.Printf("WORKER: Job %s completed successfully", job.ID)
	return nil
}

func (s *PushboyService) GetJobStatus(ctx context.Context, jobID string) (*storage.PublishJob, error) {
	return s.store.GetJobStatus(ctx, jobID)
}

func (s *PushboyService) StartAggregator(ctx context.Context) {
	var batch []storage.DeliveryReceipt

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}

		if err := s.store.BulkInsertReceipts(ctx, batch); err != nil {
			log.Printf("WORKER: -> CRITICAL: failed to bulk insert delivery receipts: %v", err)
		}

		batch = batch[:0]
	}

	for {
		select {
		case receipt := <-s.resultsChan:
			batch = append(batch, receipt)
			if len(batch) >= 1000 {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-ctx.Done():
			flush()
			return
		}
	}
}
