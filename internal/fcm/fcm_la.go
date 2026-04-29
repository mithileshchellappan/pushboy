package fcm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mithileshchellappan/pushboy/internal/model"
)

const defaultFcmLATTLSeconds = 3600

func (c *Client) SendLiveActivity(ctx context.Context, token string, request *model.LiveActivityRequest) error {
	if token == "" {
		return fmt.Errorf("fcm live activity token is required")
	}
	if request == nil {
		return fmt.Errorf("live activity request is required")
	}
	if request.ActivityID == "" {
		return fmt.Errorf("live activity activityId is required")
	}
	if request.ActivityType == "" {
		return fmt.Errorf("live activity activityType is required")
	}
	switch request.Action {
	case model.LiveActivityActionStart, model.LiveActivityActionUpdate, model.LiveActivityActionEnd:
	default:
		return fmt.Errorf("unsupported live activity action: %s", request.Action)
	}

	payloadJSON, err := compactLAPayload(request.Payload)
	if err != nil {
		return err
	}

	options, err := model.ParseLiveActivityOptions(request.Options)
	if err != nil {
		return err
	}

	priority := "HIGH"
	switch options.Priority {
	case "normal":
		priority = "NORMAL"
	case "high":
		priority = "HIGH"
	}

	ttl := options.TTL
	if ttl < 0 {
		return fmt.Errorf("live activity options.ttl must be >= 0")
	}
	if ttl == 0 {
		ttl = defaultFcmLATTLSeconds
	}

	android := &AndroidConfig{
		Priority: priority,
		TTL:      fmt.Sprintf("%ds", ttl),
	}
	if options.CollapseID != "" {
		android.CollapseKey = options.CollapseID
	} else if request.Action == model.LiveActivityActionUpdate || request.Action == model.LiveActivityActionEnd {
		android.CollapseKey = request.ActivityID
	}

	data := map[string]string{
		"type":          "live_activity",
		"action":        string(request.Action),
		"activity_id":   request.ActivityID,
		"activity_type": request.ActivityType,
		"payload":       payloadJSON,
	}
	var payloadFields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payloadJSON), &payloadFields); err == nil {
		for key, rawValue := range payloadFields {
			if _, exists := data[key]; exists {
				continue
			}
			value, err := fcmDataString(rawValue)
			if err != nil {
				return err
			}
			if value != "" {
				data[key] = value
			}
		}
	}

	message := FcmMessage{
		Token:   token,
		Data:    data,
		Android: android,
	}

	return c.sendMessage(ctx, message)
}

func compactLAPayload(raw json.RawMessage) (string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		trimmed = `{}`
	}

	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(trimmed)); err != nil {
		return "", fmt.Errorf("invalid live activity payload: %w", err)
	}
	return buf.String(), nil
}

func fcmDataString(raw json.RawMessage) (string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "", nil
	}
	if strings.HasPrefix(trimmed, `"`) {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", fmt.Errorf("invalid live activity payload field: %w", err)
		}
		return value, nil
	}
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var buf bytes.Buffer
		if err := json.Compact(&buf, raw); err != nil {
			return "", fmt.Errorf("invalid live activity payload field: %w", err)
		}
		return buf.String(), nil
	}
	return trimmed, nil
}
