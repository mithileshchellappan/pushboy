package storage

import (
	"context"
	"errors"
)

type Topic struct {
	ID   string
	Name string
}

type Store interface {
	CreateTopic(ctx context.Context, topic *Topic) error
	ListTopics(ctx context.Context) ([]Topic, error)
	GetTopicByID(ctx context.Context, topicID string) (*Topic, error)
	DeleteTopic(ctx context.Context, topicID string) error
	SubscribeToTopic(ctx context.Context, sub *Subscription) (*Subscription, error)

	CreatePublishJob(ctx context.Context, topicID string) (*PublishJob, error)
	// FetchPendingJobs(ctx context.Context, limit int) ([]PublishJob, error)
}

type Subscription struct {
	ID        string
	TopicID   string
	Platform  string
	Token     string
	CreatedAt string
}

type PublishJob struct {
	ID           string
	TopicID      string
	Status       string
	TotalCount   int
	SuccessCount int
	FailureCount int
	CreatedAt    string
}

type DeliveryReceipt struct {
	ID             string
	JobID          string
	SubscriptionID string
	Status         string
	StatusReason   string
	DispatchedAt   string
}

type errorCollection struct {
	AlreadyExists error
}

var Errors = errorCollection{
	AlreadyExists: errors.New("resource already exists"),
}
