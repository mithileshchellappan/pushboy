package storage

import (
	"context"
	"errors"
)

// User represents a user in the system
type User struct {
	ID        string
	CreatedAt string
}

// Token represents a device token for push notifications
type Token struct {
	ID        string
	UserID    string
	Platform  string // apns or fcm
	Token     string
	CreatedAt string
}

// Topic represents a notification topic
type Topic struct {
	ID   string
	Name string
}

// UserTopicSubscription represents a user's subscription to a topic
type UserTopicSubscription struct {
	ID        string
	UserID    string
	TopicID   string
	CreatedAt string
}

// PublishJob represents a job to publish notifications to a topic
type PublishJob struct {
	ID           string
	TopicID      string
	UserID       string
	Title        string
	Body         string
	Status       string
	TotalCount   int
	SuccessCount int
	FailureCount int
	CreatedAt    string
}

// DeliveryReceipt tracks the delivery status of a notification
type DeliveryReceipt struct {
	ID           string
	JobID        string
	TokenID      string
	Status       string
	StatusReason string
	DispatchedAt string
}

type TokenBatch struct {
	Tokens     []Token
	NextCursor string
	HasMore    bool
}

// Store defines the interface for data persistence
type Store interface {
	// User operations
	CreateUser(ctx context.Context, user *User) (*User, error)
	GetUser(ctx context.Context, userID string) (*User, error)
	DeleteUser(ctx context.Context, userID string) error

	// Token operations
	CreateToken(ctx context.Context, token *Token) (*Token, error)
	GetTokensByUserID(ctx context.Context, userID string) ([]Token, error)
	DeleteToken(ctx context.Context, tokenID string) error

	// Topic operations
	CreateTopic(ctx context.Context, topic *Topic) error
	ListTopics(ctx context.Context) ([]Topic, error)
	GetTopicByID(ctx context.Context, topicID string) (*Topic, error)
	DeleteTopic(ctx context.Context, topicID string) error

	// User-Topic subscription operations
	SubscribeUserToTopic(ctx context.Context, sub *UserTopicSubscription) (*UserTopicSubscription, error)
	UnsubscribeUserFromTopic(ctx context.Context, userID, topicID string) error
	GetUserSubscriptions(ctx context.Context, userID string) ([]UserTopicSubscription, error)
	GetTopicSubscribers(ctx context.Context, topicID string) ([]User, error)

	// Publish job operations
	CreatePublishJob(ctx context.Context, job *PublishJob) (*PublishJob, error)
	FetchPendingJobs(ctx context.Context, limit int) ([]PublishJob, error)
	UpdateJobStatus(ctx context.Context, jobID string, status string) error
	GetJobStatus(ctx context.Context, jobID string) (*PublishJob, error)
	IncrementJobCounters(ctx context.Context, jobID string, success int, failure int) error
	GetTokenBatchForTopic(ctx context.Context, topicID string, cursor string, batchSize int) (*TokenBatch, error)
	GetTokenBatchForUser(ctx context.Context, userID string, cursor string, batchSize int) (*TokenBatch, error)

	// Delivery receipt operations
	RecordDeliveryReceipt(ctx context.Context, receipt *DeliveryReceipt) error
	BulkInsertReceipts(ctx context.Context, receipts []DeliveryReceipt) error
}

type errorCollection struct {
	AlreadyExists error
	NotFound      error
}

var Errors = errorCollection{
	AlreadyExists: errors.New("resource already exists"),
	NotFound:      errors.New("resource not found"),
}
