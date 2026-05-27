package server

import (
	"time"

	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

func formatAPITime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func formatAPIOptionalTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return formatAPITime(*value)
}

type userResponse struct {
	ID        string
	CreatedAt string
}

func toUserResponse(user storage.User) userResponse {
	return userResponse{
		ID:        user.ID,
		CreatedAt: formatAPITime(user.CreatedAt),
	}
}

func toUserResponsePtr(user *storage.User) *userResponse {
	if user == nil {
		return nil
	}
	response := toUserResponse(*user)
	return &response
}

type tokenResponse struct {
	ID        string
	UserID    string
	Platform  model.Platform
	Token     string
	CreatedAt string
}

func toTokenResponse(token storage.Token) tokenResponse {
	return tokenResponse{
		ID:        token.ID,
		UserID:    token.UserID,
		Platform:  token.Platform,
		Token:     token.Token,
		CreatedAt: formatAPITime(token.CreatedAt),
	}
}

func toTokenResponsePtr(token *storage.Token) *tokenResponse {
	if token == nil {
		return nil
	}
	response := toTokenResponse(*token)
	return &response
}

func toTokenResponses(tokens []storage.Token) []tokenResponse {
	responses := make([]tokenResponse, 0, len(tokens))
	for _, token := range tokens {
		responses = append(responses, toTokenResponse(token))
	}
	return responses
}

type subscriptionResponse struct {
	ID        string
	UserID    string
	TopicID   string
	CreatedAt string
}

func toSubscriptionResponse(sub storage.UserTopicSubscription) subscriptionResponse {
	return subscriptionResponse{
		ID:        sub.ID,
		UserID:    sub.UserID,
		TopicID:   sub.TopicID,
		CreatedAt: formatAPITime(sub.CreatedAt),
	}
}

func toSubscriptionResponses(subs []storage.UserTopicSubscription) []subscriptionResponse {
	responses := make([]subscriptionResponse, 0, len(subs))
	for _, sub := range subs {
		responses = append(responses, toSubscriptionResponse(sub))
	}
	return responses
}

type topicSubscriberResponse struct {
	ID           string
	CreatedAt    string
	SubscribedAt string
}

func toTopicSubscriberResponse(subscriber storage.TopicSubscriber) topicSubscriberResponse {
	return topicSubscriberResponse{
		ID:           subscriber.ID,
		CreatedAt:    formatAPITime(subscriber.CreatedAt),
		SubscribedAt: formatAPITime(subscriber.SubscribedAt),
	}
}

type publishJobResponse struct {
	ID           string
	TopicID      string
	UserID       string
	Payload      *model.NotificationPayload
	Status       model.NotificationJobStatus
	TotalCount   int
	SuccessCount int
	FailureCount int
	CreatedAt    string
	CompletedAt  string
	ScheduledAt  string
}

func toPublishJobResponse(job storage.PublishJob) publishJobResponse {
	return publishJobResponse{
		ID:           job.ID,
		TopicID:      job.TopicID,
		UserID:       job.UserID,
		Payload:      job.Payload,
		Status:       job.Status,
		TotalCount:   job.TotalCount,
		SuccessCount: job.SuccessCount,
		FailureCount: job.FailureCount,
		CreatedAt:    formatAPITime(job.CreatedAt),
		CompletedAt:  formatAPIOptionalTime(job.CompletedAt),
		ScheduledAt:  formatAPIOptionalTime(job.ScheduledAt),
	}
}

type laTokenResponse struct {
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

func toLATokenResponse(token storage.LiveActivityToken) laTokenResponse {
	return laTokenResponse{
		ID:            token.ID,
		UserID:        token.UserID,
		Platform:      token.Platform,
		TokenType:     token.TokenType,
		Token:         token.Token,
		CreatedAt:     formatAPITime(token.CreatedAt),
		LastSeenAt:    formatAPITime(token.LastSeenAt),
		ExpiresAt:     formatAPIOptionalTime(token.ExpiresAt),
		InvalidatedAt: formatAPIOptionalTime(token.InvalidatedAt),
	}
}

func toLATokenResponsePtr(token *storage.LiveActivityToken) *laTokenResponse {
	if token == nil {
		return nil
	}
	response := toLATokenResponse(*token)
	return &response
}

type laSubscriptionResponse struct {
	ID        string
	UserID    string
	TopicID   string
	CreatedAt string
}

func toLASubscriptionResponse(sub storage.LiveActivityUserTopicSubscription) laSubscriptionResponse {
	return laSubscriptionResponse{
		ID:        sub.ID,
		UserID:    sub.UserID,
		TopicID:   sub.TopicID,
		CreatedAt: formatAPITime(sub.CreatedAt),
	}
}
