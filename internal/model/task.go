package model

import "encoding/json"

type JobType string

const (
	JobTypePush JobType = "push"
	JobTypeLA   JobType = "la"
)

type Status string

const (
	Failed  Status = "FAILED"
	Success Status = "SUCCESS"
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

	LAAction     LiveActivityAction
	LAJobID      string
	LADispatchID string
	LAActivityID string
	LAActivity   string
	LAPayload    json.RawMessage
	LAOptions    json.RawMessage
}

type SendOutcome struct {
	Task    SendTask
	Receipt DeliveryReceipt
}

type DeliveryReceipt struct {
	ID           string
	JobID        string
	TokenID      string
	Status       string
	StatusReason string
	DispatchedAt string
}
