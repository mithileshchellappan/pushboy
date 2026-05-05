package storage

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/mithileshchellappan/pushboy/internal/model"
)

// User represents a user in the system
type User struct {
	ID        string
	CreatedAt string
}

type PageCursor struct {
	SortValue string
	ID        string
}

type PageQuery struct {
	Limit  int
	Cursor PageCursor
}

type NotificationListQuery struct {
	Limit  int
	Cursor PageCursor
	Status model.NotificationJobStatus
}

// Token represents a device token for push notifications
type Token struct {
	ID        string
	UserID    string
	Platform  model.Platform // apns or fcm
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

type TopicSubscriber struct {
	ID           string
	CreatedAt    string
	SubscribedAt string
}

// NotificationPayload stores the full notification content as JSON
// This is separate from dispatch.NotificationPayload to avoid circular imports

// PublishJob represents a job to publish notifications to a topic or user
type PublishJob struct {
	ID           string
	TopicID      string
	UserID       string
	Payload      *model.NotificationPayload // Full notification content
	Status       model.NotificationJobStatus
	TotalCount   int
	SuccessCount int
	FailureCount int
	CreatedAt    string
	CompletedAt  string
	ScheduledAt  string
}

// DeliveryReceipt tracks the delivery status of a notification

type TokenBatch struct {
	Tokens     []Token
	NextCursor string
	HasMore    bool
}

type LiveActivityToken struct {
	ID            string
	UserID        string
	Platform      model.Platform
	TokenType     model.LiveActivityTokenType
	Token         string
	CreatedAt     string
	LastSeenAt    string
	ExpiresAt     string
	InvalidatedAt string
}

type LiveActivityUserTopicSubscription struct {
	ID        string
	UserID    string
	TopicID   string
	CreatedAt string
}

type LiveActivityJob struct {
	ID            string
	ActivityID    string
	ActivityType  string
	UserID        string
	TopicID       string
	Status        model.LiveActivityJobStatus
	LatestPayload json.RawMessage
	Options       json.RawMessage
	CreatedAt     string
	UpdatedAt     string
	ExpiresAt     string
	ClosedAt      string
}

type LiveActivityDispatch struct {
	ID                string
	LiveActivityJobID string
	Action            model.LiveActivityAction
	Payload           json.RawMessage
	Options           json.RawMessage
	Status            string
	TotalCount        int
	SuccessCount      int
	FailureCount      int
	CreatedAt         string
	CompletedAt       string
}

type LiveActivityTokenBatch struct {
	Tokens     []LiveActivityToken
	NextCursor string
	HasMore    bool
}

// Store defines the interface for data persistence
type Store interface {
	// User operations
	CreateUser(ctx context.Context, user *User) (*User, error)
	GetUser(ctx context.Context, userID string) (*User, error)
	ListUsers(ctx context.Context, query PageQuery) ([]User, error)
	DeleteUser(ctx context.Context, userID string) error

	// Token operations
	CreateToken(ctx context.Context, token *Token) (*Token, error)
	GetTokensByUserID(ctx context.Context, userID string) ([]Token, error)
	DeleteToken(ctx context.Context, tokenID string) error
	SoftDeleteToken(ctx context.Context, tokenID string) error
	BulkSoftDeleteToken(ctx context.Context, tokenIDs []string) error

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
	ListTopicSubscribers(ctx context.Context, topicID string, query PageQuery) ([]TopicSubscriber, error)
	GetTopicSubscriberCount(ctx context.Context, topicID string) (int, error)

	// Publish job operations
	CreatePublishJob(ctx context.Context, job *PublishJob) (*PublishJob, error)
	CreateUserPublishJob(ctx context.Context, job *PublishJob) (*PublishJob, error)
	FetchPendingJobs(ctx context.Context, limit int) ([]PublishJob, error)
	UpdateJobStatus(ctx context.Context, jobID string, status model.NotificationJobStatus) error
	FinalizeJobDispatch(ctx context.Context, jobID string, totalCount int) error
	GetJobStatus(ctx context.Context, jobID string) (*PublishJob, error)
	ListTopicNotifications(ctx context.Context, topicID string, query NotificationListQuery) ([]PublishJob, error)
	ListUserNotifications(ctx context.Context, userID string, query NotificationListQuery) ([]PublishJob, error)
	ApplyPushOutcomeBatch(ctx context.Context, receipts []model.DeliveryReceipt) error
	IncrementJobCounters(ctx context.Context, jobID string, success int, failure int) error
	CompleteJobIfDone(ctx context.Context, jobID string) error
	GetTokenBatchForTopic(ctx context.Context, topicID string, cursor string, batchSize int) (*TokenBatch, error)
	GetTokenBatchForUser(ctx context.Context, userID string, cursor string, batchSize int) (*TokenBatch, error)
	GetTokenCountForTopic(ctx context.Context, topicID string) (int, error)
	GetTokenCountForUser(ctx context.Context, userID string) (int, error)

	// Delivery receipt operations
	RecordDeliveryReceipt(ctx context.Context, receipt *model.DeliveryReceipt) error

	//Schedule operations
	GetScheduledJobs(ctx context.Context) ([]PublishJob, error)

	// Live Activity operations
	UpsertLiveActivityToken(ctx context.Context, token *LiveActivityToken) (*LiveActivityToken, error)
	InvalidateLiveActivityToken(ctx context.Context, userID string, tokenValue string) error
	SubscribeUserToLATopic(ctx context.Context, sub *LiveActivityUserTopicSubscription) (*LiveActivityUserTopicSubscription, error)
	CreateOrGetLAStartJob(ctx context.Context, job *LiveActivityJob) (*LiveActivityJob, bool, error)
	GetLAJob(ctx context.Context, jobID string) (*LiveActivityJob, error)
	GetLAJobByActivityID(ctx context.Context, activityID string) (*LiveActivityJob, error)
	FindLAJobByUserScope(ctx context.Context, activityType string, userID string) (*LiveActivityJob, error)
	FindLAJobByTopicScope(ctx context.Context, activityType string, topicID string) (*LiveActivityJob, error)
	UpdateLAJobPayloadIfActive(ctx context.Context, jobID string, payload json.RawMessage, options json.RawMessage, updatedAt string) error
	RollbackLAStartJob(ctx context.Context, jobID string) error
	CloseLAJobIfActive(ctx context.Context, jobID string, updatedAt string) error
	FailLAJobIfActive(ctx context.Context, jobID string) error
	CreateLADispatch(ctx context.Context, dispatch *LiveActivityDispatch) (*LiveActivityDispatch, error)
	UpdateLADispatchStatus(ctx context.Context, dispatchID string, status string) error
	GetLATokenBatchForDispatch(ctx context.Context, dispatchID string, cursor string, batchSize int) (*LiveActivityTokenBatch, error)
	CompleteLADispatchEnqueue(ctx context.Context, dispatchID string, totalCount int) error
	ApplyLAOutcomeBatch(ctx context.Context, outcomes []model.SendOutcome) error
	InvalidateExpiredLAUpdateTokens(ctx context.Context, limit int) (int, error)

	// Lifecycle
	Close() error
}

type errorCollection struct {
	AlreadyExists error
	NotFound      error
}

var Errors = errorCollection{
	AlreadyExists: errors.New("resource already exists"),
	NotFound:      errors.New("resource not found"),
}
