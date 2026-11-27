package model

import (
	"time"

	"github.com/google/uuid"
)

type JobType string

const (
	JobTypePush JobType = "push"
	JobTypeLA   JobType = "la"
)

type DeliveryStatus string

const (
	DeliveryStatusPending DeliveryStatus = "PENDING"
	DeliveryStatusSending DeliveryStatus = "SENDING"
	DeliveryStatusSkipped DeliveryStatus = "SKIPPED"
	DeliveryStatusUnknown DeliveryStatus = "UNKNOWN"
	DeliveryStatusFailed  DeliveryStatus = "FAILED"
	DeliveryStatusSuccess DeliveryStatus = "SUCCESS"
)

const (
	TenantPushReasonRecipientStale      = "recipient_stale"
	TenantPushReasonProviderRejected    = "provider_rejected"
	TenantPushReasonProviderUnavailable = "provider_unavailable"
	TenantPushReasonInvalidRegistration = "invalid_registration"
	TenantPushReasonDeliveryUnknown     = "delivery_unknown"
	TenantPushReasonEnqueueFailed       = "enqueue_failed"
)

type SendTarget struct {
	ReceiptID string
	TokenID   string
	Token     string
	Platform  Platform
}

type SendTask struct {
	Target SendTarget
	Job    *JobItem
}

type JobItem struct {
	ID       string
	JobType  JobType
	Payload  *NotificationPayload
	MaxRetry int

	TenantID   uuid.UUID
	TopicID    string
	UserID     string
	TotalCount int
}

type SendOutcome struct {
	Task    SendTask
	Receipt DeliveryReceipt
}

type DeliveryReceipt struct {
	ID                     string
	JobID                  string
	TokenID                string
	Status                 DeliveryStatus
	StatusReason           string
	DispatchedAt           time.Time
	TenantID               uuid.UUID
	ClaimID                uuid.UUID
	InvalidateRegistration bool
}
