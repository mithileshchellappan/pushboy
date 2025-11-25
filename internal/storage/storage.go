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

	CreatePublishJob(ctx context.Context, job *PublishJob) (*PublishJob, error)
	FetchPendingJobs(ctx context.Context, limit int) ([]PublishJob, error)
	UpdateJobStatus(ctx context.Context, jobID string, status string) error
	GetJobStatus(ctx context.Context, jobID string) (*PublishJob, error)

	ListSubscriptionsByTopic(ctx context.Context, topicID string) ([]Subscription, error)
	GetSubscriptionsByExternalID(ctx context.Context, externalID string) ([]Subscription, error)
	RegisterUserToken(ctx context.Context, sub *Subscription) (*Subscription, error)
	RecordDeliveryReceipt(ctx context.Context, receipt *DeliveryReceipt) error
	IncrementJobCounters(ctx context.Context, jobID string, success int, failure int) error
	BulkInsertReceipts(ctx context.Context, receipts []DeliveryReceipt) error
}

type Subscription struct {
	ID         string
	TopicID    string
	Platform   string
	Token      string
	ExternalID string
	CreatedAt  string
}

type PublishJob struct {
	ID           string
	TopicID      string
	Title        string
	Body         string
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
