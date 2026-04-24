package apns

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mithileshchellappan/pushboy/internal/model"
)

const laTopicSuffix = ".push-type.liveactivity"

type laAlert struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
	Sound string `json:"sound,omitempty"`
}

type laOptions struct {
	Alert          *laAlert        `json:"alert,omitempty"`
	AttributesType string          `json:"attributesType,omitempty"`
	Attributes     json.RawMessage `json:"attributes,omitempty"`
	Timestamp      any             `json:"timestamp,omitempty"`
	StaleDate      any             `json:"staleDate,omitempty"`
	DismissalDate  any             `json:"dismissalDate,omitempty"`
	Expiration     any             `json:"expiration,omitempty"`
	Priority       int             `json:"priority,omitempty"`
	CollapseID     string          `json:"collapseId,omitempty"`
	RelevanceScore *float64        `json:"relevanceScore,omitempty"`
}

func (c *Client) SendLiveActivity(ctx context.Context, token string, request *model.LiveActivityRequest) error {
	if token == "" {
		return fmt.Errorf("apns live activity token is required")
	}
	if request == nil {
		return fmt.Errorf("live activity request is required")
	}

	jwtToken, err := c.getJWT()
	if err != nil {
		return fmt.Errorf("failed to get JWT: %w", err)
	}

	options, err := parseLAOptions(request.Options)
	if err != nil {
		return err
	}

	payloadBytes, headers, err := c.buildLAMessage(request, options)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/3/device/%s", c.endpoint, token)
	return c.sendWithRetry(ctx, url, payloadBytes, jwtToken, headers)
}

func (c *Client) buildLAMessage(request *model.LiveActivityRequest, options laOptions) ([]byte, map[string]string, error) {
	if c.bundleID == "" {
		return nil, nil, fmt.Errorf("apns bundle id is not configured")
	}

	now := time.Now().UTC()
	timestamp, ok, err := parseLATime(options.Timestamp)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid la options.timestamp: %w", err)
	}
	if !ok {
		timestamp = now.Unix()
	}

	contentState, err := decodeOptionalJSON(request.Payload)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid la payload: %w", err)
	}

	aps := map[string]any{
		"timestamp": timestamp,
		"event":     string(request.Action),
	}
	if contentState != nil {
		aps["content-state"] = contentState
	}

	if request.Action == model.LiveActivityActionStart {
		if options.AttributesType == "" {
			return nil, nil, fmt.Errorf("apns live activity start requires options.attributesType")
		}
		attributes, err := decodeOptionalJSON(options.Attributes)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid la options.attributes: %w", err)
		}
		if attributes == nil {
			return nil, nil, fmt.Errorf("apns live activity start requires options.attributes")
		}
		aps["attributes-type"] = options.AttributesType
		aps["attributes"] = attributes
	} else if request.Action != model.LiveActivityActionUpdate && request.Action != model.LiveActivityActionEnd {
		return nil, nil, fmt.Errorf("unsupported live activity action: %s", request.Action)
	}

	if options.Alert != nil {
		aps["alert"] = options.Alert
	}
	if options.RelevanceScore != nil {
		aps["relevance-score"] = *options.RelevanceScore
	}
	if staleDate, ok, err := parseLATime(options.StaleDate); err != nil {
		return nil, nil, fmt.Errorf("invalid la options.staleDate: %w", err)
	} else if ok {
		aps["stale-date"] = staleDate
	}
	if dismissalDate, ok, err := parseLATime(options.DismissalDate); err != nil {
		return nil, nil, fmt.Errorf("invalid la options.dismissalDate: %w", err)
	} else if ok {
		aps["dismissal-date"] = dismissalDate
	}

	body, err := json.Marshal(map[string]any{"aps": aps})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal la request: %w", err)
	}

	headers := map[string]string{
		"apns-topic":      c.laTopic(),
		"apns-push-type":  "liveactivity",
		"apns-priority":   "10",
		"apns-expiration": "0",
	}
	if options.Priority != 0 {
		if options.Priority != 5 && options.Priority != 10 {
			return nil, nil, fmt.Errorf("live activity options.priority must be 5 or 10")
		}
		headers["apns-priority"] = strconv.Itoa(options.Priority)
	}
	if expiration, ok, err := parseLATime(options.Expiration); err != nil {
		return nil, nil, fmt.Errorf("invalid la options.expiration: %w", err)
	} else if ok {
		headers["apns-expiration"] = strconv.FormatInt(expiration, 10)
	}
	if options.CollapseID != "" {
		headers["apns-collapse-id"] = options.CollapseID
	}

	return body, headers, nil
}

func (c *Client) laTopic() string {
	if c.bundleID == "" {
		return ""
	}
	if strings.HasSuffix(c.bundleID, laTopicSuffix) {
		return c.bundleID
	}
	return c.bundleID + laTopicSuffix
}

func parseLAOptions(raw json.RawMessage) (laOptions, error) {
	var options laOptions
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return options, nil
	}
	if err := json.Unmarshal([]byte(trimmed), &options); err != nil {
		return laOptions{}, fmt.Errorf("invalid live activity options: %w", err)
	}
	return options, nil
}

func decodeOptionalJSON(raw json.RawMessage) (any, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}

	var value any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		return nil, err
	}
	return value, nil
}

func parseLATime(value any) (int64, bool, error) {
	switch v := value.(type) {
	case nil:
		return 0, false, nil
	case float64:
		return int64(v), true, nil
	case string:
		v = strings.TrimSpace(v)
		if v == "" {
			return 0, false, nil
		}
		if unix, err := strconv.ParseInt(v, 10, 64); err == nil {
			return unix, true, nil
		}
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return 0, false, fmt.Errorf("must be unix seconds or RFC3339")
		}
		return t.Unix(), true, nil
	default:
		return 0, false, fmt.Errorf("must be unix seconds or RFC3339")
	}
}
