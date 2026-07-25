package apns

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/mithileshchellappan/pushboy/internal/model"
)

const (
	developmentChannelManagementEndpoint = "https://api-manage-broadcast.sandbox.push.apple.com:2195"
	productionChannelManagementEndpoint  = "https://api-manage-broadcast.push.apple.com:2196"
)

func (c *Client) CreateLiveActivityChannel(ctx context.Context) (string, error) {
	body := []byte(`{"message-storage-policy":0,"push-type":"LiveActivity"}`)
	headers, err := c.channelManagementRequest(ctx, http.MethodPost, body, "")
	if err != nil {
		return "", err
	}
	channelID := headers.Get("apns-channel-id")
	if channelID == "" {
		return "", errors.New("APNs channel response is missing apns-channel-id")
	}
	return channelID, nil
}

func (c *Client) DeleteLiveActivityChannel(ctx context.Context, channelID string) error {
	if channelID == "" {
		return errors.New("live activity channel id is required")
	}

	_, err := c.channelManagementRequest(ctx, http.MethodDelete, nil, channelID)
	var apnsErr *responseError
	if errors.As(err, &apnsErr) && apnsErr.reason == "ChannelNotRegistered" {
		return nil
	}
	return err
}

func (c *Client) SendLiveActivityBroadcast(
	ctx context.Context,
	channelID string,
	request *model.LiveActivityRequest,
) error {
	if channelID == "" {
		return errors.New("live activity channel id is required")
	}
	if request == nil {
		return errors.New("live activity request is required")
	}
	if request.Action != model.LiveActivityActionUpdate &&
		request.Action != model.LiveActivityActionEnd {
		return fmt.Errorf("unsupported live activity broadcast action: %s", request.Action)
	}

	options, err := model.ParseLiveActivityOptions(request.Options)
	if err != nil {
		return err
	}
	body, headers, err := c.buildLAMessage(request, options)
	if err != nil {
		return err
	}
	delete(headers, "apns-topic")
	delete(headers, "apns-collapse-id")
	headers["apns-channel-id"] = channelID
	headers["apns-expiration"] = "0"

	jwtToken, err := c.getJWT()
	if err != nil {
		return fmt.Errorf("failed to get JWT: %w", err)
	}
	endpoint := fmt.Sprintf(
		"%s/4/broadcasts/apps/%s",
		c.endpoint,
		url.PathEscape(c.bundleID),
	)
	return c.sendWithRetry(ctx, endpoint, body, jwtToken, headers)
}

func (c *Client) channelManagementRequest(
	ctx context.Context,
	method string,
	body []byte,
	channelID string,
) (http.Header, error) {
	if c.bundleID == "" {
		return nil, errors.New("apns bundle id is not configured")
	}
	if c.channelManagementEndpoint == "" {
		return nil, errors.New("apns channel management endpoint is not configured")
	}

	jwtToken, err := c.getJWT()
	if err != nil {
		return nil, fmt.Errorf("failed to get JWT: %w", err)
	}
	endpoint := fmt.Sprintf(
		"%s/1/apps/%s/channels",
		c.channelManagementEndpoint,
		url.PathEscape(c.bundleID),
	)
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", jwtToken))
	if channelID != "" {
		req.Header.Set("apns-channel-id", channelID)
	}

	if err := c.acquire(ctx); err != nil {
		return nil, err
	}
	resp, err := c.httpClients[c.next.Add(1)%uint32(len(c.httpClients))].Do(req)
	c.release()
	if err != nil {
		return nil, err
	}
	responseBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	expectedStatus := http.StatusCreated
	if method == http.MethodDelete {
		expectedStatus = http.StatusNoContent
	}
	if resp.StatusCode == expectedStatus {
		return resp.Header, nil
	}

	var errorBody struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(responseBody, &errorBody)
	return nil, &responseError{
		status: resp.Status,
		reason: errorBody.Reason,
	}
}
