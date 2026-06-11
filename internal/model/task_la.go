package model

import (
	"encoding/json"
)

type LAJobItem struct {
	ID string
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
}

type LASendTask struct {
	Target SendTarget
	LAJob    *LAJobItem
}

type LASendOutcome struct {
	Task    LASendTask
	Receipt DeliveryReceipt
}
