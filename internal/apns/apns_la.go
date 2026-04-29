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

	options, err := model.ParseLiveActivityOptions(request.Options)
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

func (c *Client) buildLAMessage(request *model.LiveActivityRequest, options model.ParsedLiveActivityOptions) ([]byte, map[string]string, error) {
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

	switch request.Action {
	case model.LiveActivityActionStart:
		if options.Alert == nil {
			return nil, nil, fmt.Errorf("apns live activity start requires options.alert")
		}
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
	case model.LiveActivityActionUpdate, model.LiveActivityActionEnd:
	default:
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
	switch options.Priority {
	case "normal":
		headers["apns-priority"] = "5"
	case "high":
		headers["apns-priority"] = "10"
	}
	if expiration, ok, err := parseLATime(options.Expiration); err != nil {
		return nil, nil, fmt.Errorf("invalid la options.expiration: %w", err)
	} else {
		switch {
		case ok:
			headers["apns-expiration"] = strconv.FormatInt(expiration, 10)
		case options.TTL < 0:
			return nil, nil, fmt.Errorf("live activity options.ttl must be >= 0")
		case options.TTL > 0:
			expiration = now.Add(time.Duration(options.TTL) * time.Second).Unix()
			headers["apns-expiration"] = strconv.FormatInt(expiration, 10)
		}
	}
	collapseID := options.CollapseID
	if collapseID == "" {
		collapseID = request.ActivityID
	}
	if collapseID != "" {
		headers["apns-collapse-id"] = collapseID
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
