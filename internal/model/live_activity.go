package model

import (
	"encoding/json"
	"fmt"
	"strings"
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

type LiveActivityAlert struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
	Sound string `json:"sound,omitempty"`
}

type ParsedLiveActivityOptions struct {
	Alert          *LiveActivityAlert
	AttributesType string
	Attributes     json.RawMessage
	Timestamp      any
	StaleDate      any
	DismissalDate  any
	Expiration     any
	Priority       string
	TTL            int
	CollapseID     string
	RelevanceScore *float64
}

func ParseLiveActivityOptions(raw json.RawMessage) (ParsedLiveActivityOptions, error) {
	var wire struct {
		Alert          *LiveActivityAlert `json:"alert,omitempty"`
		AttributesType string             `json:"attributesType,omitempty"`
		Attributes     json.RawMessage    `json:"attributes,omitempty"`
		Timestamp      any                `json:"timestamp,omitempty"`
		StaleDate      any                `json:"staleDate,omitempty"`
		DismissalDate  any                `json:"dismissalDate,omitempty"`
		Expiration     any                `json:"expiration,omitempty"`
		Priority       any                `json:"priority,omitempty"`
		TTL            int                `json:"ttl,omitempty"`
		CollapseID     string             `json:"collapse_id,omitempty"`
		RelevanceScore *float64           `json:"relevanceScore,omitempty"`
	}

	trimmed := strings.TrimSpace(string(raw))
	if trimmed != "" && trimmed != "null" {
		if err := json.Unmarshal([]byte(trimmed), &wire); err != nil {
			return ParsedLiveActivityOptions{}, fmt.Errorf("invalid live activity options: %w", err)
		}
	}

	priority, err := parseLiveActivityPriority(wire.Priority)
	if err != nil {
		return ParsedLiveActivityOptions{}, err
	}

	return ParsedLiveActivityOptions{
		Alert:          wire.Alert,
		AttributesType: wire.AttributesType,
		Attributes:     wire.Attributes,
		Timestamp:      wire.Timestamp,
		StaleDate:      wire.StaleDate,
		DismissalDate:  wire.DismissalDate,
		Expiration:     wire.Expiration,
		Priority:       priority,
		TTL:            wire.TTL,
		CollapseID:     strings.TrimSpace(wire.CollapseID),
		RelevanceScore: wire.RelevanceScore,
	}, nil
}

func parseLiveActivityPriority(value any) (string, error) {
	switch v := value.(type) {
	case nil:
		return "", nil
	case float64:
		switch v {
		case 5:
			return "normal", nil
		case 10:
			return "high", nil
		}
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "":
			return "", nil
		case "normal", "5":
			return "normal", nil
		case "high", "10":
			return "high", nil
		}
	}
	return "", fmt.Errorf("live activity options.priority must be 5, 10, 'high', or 'normal'")
}

type LiveActivityRequest struct {
	Action       LiveActivityAction
	ActivityID   string
	ActivityType string
	Payload      LiveActivityPayload
	Options      LiveActivityOptions
}
