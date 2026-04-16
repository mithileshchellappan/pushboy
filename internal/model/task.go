package model

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
	//TODO: Add LA Payload when working on
	TopicID string
	UserID  string
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
