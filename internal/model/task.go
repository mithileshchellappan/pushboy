package model

import (
	"time"
)

type JobType string

const (
	JobTypePush JobType = "push"
	JobTypeLA   JobType = "la"
)

type DeliveryStatus string

const (
	DeliveryStatusFailed  DeliveryStatus = "FAILED"
	DeliveryStatusSuccess DeliveryStatus = "SUCCESS"
)

type SendTarget struct {
	TokenID  string
	Token    string
	Platform Platform
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

	TopicID    string
	UserID     string
	TotalCount int
}

type SendOutcome struct {
	Task    SendTask
	Receipt DeliveryReceipt
}

type DeliveryReceipt struct {
	ID           string
	JobID        string
	TokenID      string
	Status       DeliveryStatus
	StatusReason string
	DispatchedAt time.Time
}
