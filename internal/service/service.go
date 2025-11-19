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
}

func NewPushBoyService(s storage.Store, dispatchers map[string]dispatch.Dispatcher) *PushboyService {
	return &PushboyService{store: s, dispatchers: dispatchers}
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

func (s *PushboyService) SubscribeToTopic(ctx context.Context, topicId string, platform string, token string) (*storage.Subscription, error) {
	if platform != "apns" && platform != "fcm" {
		return nil, fmt.Errorf("invalid platform for platform")
	}

	sub, err := s.store.SubscribeToTopic(ctx, &storage.Subscription{
		TopicID:  topicId,
		Platform: platform,
		Token:    token,
	})
	if err != nil {
		return nil, err
	}
	return sub, nil
}

func (s *PushboyService) CreatePublishJob(ctx context.Context, topicID string) (*storage.PublishJob, error) {
	job, err := s.store.CreatePublishJob(ctx, topicID)
	if err != nil {
		return nil, err
	}
	return job, nil
}

func (s *PushboyService) ProcessPendingJobs(ctx context.Context) error {
	jobs, err := s.store.FetchPendingJobs(ctx, 10)
	if err != nil {
		return err
	}

	for _, job := range jobs {
		log.Printf("WORKER: Processing job... %s", job.ID)

		// Mark job as in progress
		if err := s.store.UpdateJobStatus(ctx, job.ID, "IN_PROGRESS"); err != nil {
			log.Printf("WORKER: Error marking job %s as IN_PROGRESS: %v", job.ID, err)
			continue
		}

		subscriptions, err := s.store.ListSubscriptionsByTopic(ctx, job.TopicID)
		if err != nil {
			log.Printf("WORKER: Error listing subscriptions for topic %s: %v", job.TopicID, err)
			s.store.UpdateJobStatus(ctx, job.ID, "FAILED")
			continue
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
					Title: "Test Title",
					Body:  "Test Body",
				}
				sendErr = dispatcher.Send(ctx, &sub, payload)
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

			if sendErr != nil {
				receipt.Status = "FAILED"
				receipt.StatusReason = sendErr.Error()
				s.store.IncrementJobCounters(ctx, job.ID, 0, 1)
			} else {
				receipt.Status = "SENT"
				receipt.StatusReason = statusReason
				s.store.IncrementJobCounters(ctx, job.ID, 0, 1)
			}

			if err := s.store.RecordDeliveryReceipt(ctx, receipt); err != nil {
				log.Printf("WORKER: -> CRITICAL: failed to record delivery receipt for job %s: %v", job.ID, err)
			}
		}

		if err := s.store.UpdateJobStatus(ctx, job.ID, "COMPLETED"); err != nil {
			log.Printf("WORKER: Error marking job %s as COMPLETED: %v", job.ID, err)
			continue
		}

		log.Printf("WORKER: Job %s completed successfully", job.ID)
	}
	return nil
}
