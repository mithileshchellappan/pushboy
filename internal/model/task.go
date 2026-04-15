package model

type JobType string

const (
	JobTypePush JobType = "push"
	JobTypeLA   JobType = "la"
)

type Status string

const (
	Failed  Status = "failed"
	Success Status = "success"
)

type SendTarget struct {
	TokenID  string
	Token    string
	Platform string
}

type SendTask struct {
	Target SendTarget
	Job    *JobItem
}

type SendResult struct {
	JobID        string
	TokenID      string
	Status       string
	StatusReason Status
}

type JobItem struct {
	ID      string
	JobType JobType
	Payload *NotificationPayload
	//TODO: Add LA Payload when working on
	TopicID string
	UserID  string
}
