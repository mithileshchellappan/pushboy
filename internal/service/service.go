package service

import (
	"context"
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

func (s *PushboyService) CreateTopic(ctx context.Context, name string) (*storage.Topic, error) {
	topic := &storage.Topic{Name: name}

	err := s.store.CreateTopic(ctx, topic)
	if err != nil {
		return nil, err
	}

	return topic, nil
}

func (s *PushboyService) ListTopics(ctx context.Context) ([]storage.Topic, error) {
	topics, err := s.store.ListTopics(ctx)

	if err != nil {
		return nil, err
	}

	return topics, nil
}

func (s *PushboyService) GetTopicByID(ctx context.Context, topicID string) (*storage.Topic, error) {
	topic, err := s.store.GetTopicByID(ctx, topicID)

	if err != nil {
		return nil, err
	}

	return topic, nil
}

func (s *PushboyService) DeleteTopic(ctx context.Context, topicID string) error {
	err := s.store.DeleteTopic(ctx, topicID)
	if err != nil {
		return err
	}

	return nil
}

func (s *PushboyService) SubscribeToTopic(ctx context.Context, topicId string, platform string, token string, externalID string) (*storage.Subscription, error) {
	if platform != "apns" && platform != "fcm" {
		return nil, fmt.Errorf("invalid platform for platform")
	}

	if externalID == "" {
		externalID = uuid.New().String()
	}

	sub, err := s.store.SubscribeToTopic(ctx, &storage.Subscription{
		TopicID:    topicId,
		Platform:   platform,
		Token:      token,
		ExternalID: externalID,
	})
	if err != nil {
		return nil, err
	}
	return sub, nil
}

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

// func (s *PushboyService) ProcessPendingJobs(ctx context.Context) error {
// 	jobs, err := s.store.FetchPendingJobs(ctx, 10)
// 	if err != nil {
// 		return err
// 	}

// 	for _, job := range jobs {
// 		if err := s.ProcessJobs(ctx, &job); err != nil {
// 			log.Printf("Error processing job %s: %v", job.ID, err)
// 		}
// 	}
// 	return nil
// }

func (s *PushboyService) ProcessJobs(ctx context.Context, job *storage.PublishJob) error {
	log.Printf("WORKER: Processing job... %s", job.ID)

	// Mark job as in progress
	if err := s.store.UpdateJobStatus(ctx, job.ID, "IN_PROGRESS"); err != nil {
		return fmt.Errorf("marking in_progress failed: %w", err)
	}

	subscriptions, err := s.store.ListSubscriptionsByTopic(ctx, job.TopicID)
	if err != nil {
		s.store.UpdateJobStatus(ctx, job.ID, "FAILED")
		return fmt.Errorf("listing subscriptions failed: %w", err)
	}

	for _, sub := range subscriptions {

		log.Printf("WORKER: Dispatching message to subscription platform=%s token=%s", sub.Platform, sub.Token)

		dispatcher, ok := s.dispatchers[sub.Platform]
		var sendErr error
		var statusReason string

		if !ok {
			sendErr = fmt.Errorf("dispatcher not found for platform %s", sub.Platform)
			statusReason = "UNSUPPORTED_PLATFORM"
		} else {
			payload := &dispatch.NotificationPayload{
				Title: job.Title,
				Body:  job.Body,
			}
			start := time.Now()
			sendErr = dispatcher.Send(ctx, &sub, payload)
			duration := time.Since(start)

			if sendErr != nil {
				statusReason = sendErr.Error()
				log.Printf("WORKER: -> Error sending notification to platform %s: %v", sub.Platform, sendErr)
			} else {
				statusReason = "OK"
				log.Printf("WORKER: -> Notification sent to platform %s in %s", sub.Platform, duration)
			}
		}

		receipt := &storage.DeliveryReceipt{
			ID:             uuid.New().String(),
			JobID:          job.ID,
			SubscriptionID: sub.ID,
			DispatchedAt:   time.Now().UTC().Format(time.RFC3339),
		}
		s.resultsChan <- *receipt

		if sendErr != nil {
			receipt.Status = "FAILED"
			receipt.StatusReason = sendErr.Error()
			s.store.IncrementJobCounters(ctx, job.ID, 0, 1)
		} else {
			receipt.Status = "SENT"
			receipt.StatusReason = statusReason
			s.store.IncrementJobCounters(ctx, job.ID, 0, 1)
		}
	}

	if err := s.store.UpdateJobStatus(ctx, job.ID, "COMPLETED"); err != nil {
		return fmt.Errorf("updating job status failed: %w", err)
	}

	log.Printf("WORKER: Job %s completed successfully", job.ID)
	return nil
}

func (s *PushboyService) GetJobStatus(ctx context.Context, jobID string) (*storage.PublishJob, error) {
	job, err := s.store.GetJobStatus(ctx, jobID)
	if err != nil {
		return nil, err
	}
	return job, nil
}

func (s *PushboyService) SendToUser(ctx context.Context, externalID string, title string, body string) error {
	subscriptions, err := s.store.GetSubscriptionsByExternalID(ctx, externalID)
	if err != nil {
		return fmt.Errorf("failed to get subscriptions for external ID: %w", err)
	}

	if len(subscriptions) == 0 {
		return fmt.Errorf("no subscriptions found for external ID: %s", externalID)
	}

	for _, sub := range subscriptions {
		log.Printf("WORKER: Dispatching user notification to subscription platform=%s token=%s", sub.Platform, sub.Token)

		dispatcher, ok := s.dispatchers[sub.Platform]
		var sendErr error

		if !ok {
			sendErr = fmt.Errorf("dispatcher not found for platform %s", sub.Platform)
		} else {
			payload := &dispatch.NotificationPayload{
				Title: title,
				Body:  body,
			}
			start := time.Now()
			sendErr = dispatcher.Send(ctx, &sub, payload)
			duration := time.Since(start)

			if sendErr != nil {
				log.Printf("WORKER: -> Error sending user notification to platform %s: %v", sub.Platform, sendErr)
			} else {
				log.Printf("WORKER: -> User notification sent to platform %s in %s", sub.Platform, duration)
			}
		}

		// For user notifications, we don't create delivery receipts since there's no job
		// But we could log the delivery attempt for debugging/monitoring purposes
		if sendErr != nil {
			log.Printf("WORKER: -> User notification failed for platform %s: %v", sub.Platform, sendErr)
		}
	}

	return nil
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
