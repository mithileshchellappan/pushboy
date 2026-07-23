package model

import (
	"encoding/json"
	"time"
)

type LAJobItem struct {
	ID         string
	Action     LiveActivityAction
	JobID      string
	DispatchID string
	ActivityID string
	Activity   string
	Payload    json.RawMessage
	Options    json.RawMessage

	TopicID    string
	UserID     string
	TotalCount int
	CreatedAt  time.Time
}

type LASendTask struct {
	Target SendTarget
	LAJob  *LAJobItem
}

type LASendOutcome struct {
	Task    LASendTask
	Receipt DeliveryReceipt
}
