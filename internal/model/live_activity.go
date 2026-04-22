package model

import (
	"encoding/json"
	"fmt"
)

type LiveActivityTokenType string

const (
	LiveActivityTokenTypeStart  LiveActivityTokenType = "start"
	LiveActivityTokenTypeUpdate LiveActivityTokenType = "update"
)

func ParseLiveActivityTokenType(s string) (LiveActivityTokenType, error) {
	switch LiveActivityTokenType(s) {
	case LiveActivityTokenTypeStart, LiveActivityTokenTypeUpdate:
		return LiveActivityTokenType(s), nil
	default:
		return "", fmt.Errorf("invalid live activity token type: %q", s)
	}
}

type LiveActivityAction string

const (
	LiveActivityActionStart  LiveActivityAction = "start"
	LiveActivityActionUpdate LiveActivityAction = "update"
	LiveActivityActionEnd    LiveActivityAction = "end"
)

func ParseLiveActivityAction(s string) (LiveActivityAction, error) {
	switch LiveActivityAction(s) {
	case LiveActivityActionStart, LiveActivityActionUpdate, LiveActivityActionEnd:
		return LiveActivityAction(s), nil
	default:
		return "", fmt.Errorf("invalid live activity action: %q", s)
	}
}

type LiveActivityJobStatus string

const (
	LiveActivityJobStatusActive LiveActivityJobStatus = "ACTIVE"
	LiveActivityJobStatusClosed LiveActivityJobStatus = "CLOSED"
	LiveActivityJobStatusFailed LiveActivityJobStatus = "FAILED"
)

type LiveActivityPayload = json.RawMessage
type LiveActivityOptions = json.RawMessage

type LiveActivityRequest struct {
	Action       LiveActivityAction
	ActivityType string
	Payload      LiveActivityPayload
	Options      LiveActivityOptions
}
