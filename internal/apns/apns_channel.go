package apns

import (
	"bytes"
	"context"
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
	// Apple's broadcast request examples use a separate host from device pushes.
	developmentBroadcastEndpoint = "https://api-broadcast.sandbox.push.apple.com"
	productionBroadcastEndpoint  = "https://api-broadcast.push.apple.com"
)

var ErrChannelCreateRejected = errors.New("APNs channel creation did not succeed")

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
	if errors.As(err, &apnsErr) && apnsErr.status == http.StatusBadRequest && apnsErr.reason == "ChannelNotRegistered" {
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
		c.broadcastEndpoint,
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
	for attempt := 0; ; attempt++ {
		jwtToken, err := c.getJWT()
		if err != nil {
			return nil, fmt.Errorf("%w: cannot sign APNs channel request", ErrChannelCreateRejected)
		}
		headers, err := c.doChannelManagementRequest(ctx, method, body, channelID, jwtToken)
		var providerErr *responseError
		if !errors.As(err, &providerErr) || providerErr.reason != "ExpiredProviderToken" || providerErr.status != http.StatusForbidden {
			return headers, err
		}
		c.jwtMutex.Lock()
		if c.cachedJWT == jwtToken {
			c.cachedJWT = ""
		}
		c.jwtMutex.Unlock()
		if attempt == 1 {
			return nil, err
		}
	}
}

func (c *Client) doChannelManagementRequest(ctx context.Context, method string, body []byte, channelID, jwtToken string) (http.Header, error) {
	if c.bundleID == "" || c.channelManagementEndpoint == "" {
		return nil, fmt.Errorf("%w: missing APNs channel configuration", ErrChannelCreateRejected)
	}

	endpoint := fmt.Sprintf(
		"%s/1/apps/%s/channels",
		c.channelManagementEndpoint,
		url.PathEscape(c.bundleID),
	)
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid APNs channel request", ErrChannelCreateRejected)
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", jwtToken))
	if channelID != "" {
		req.Header.Set("apns-channel-id", channelID)
	}

	if err := c.acquire(ctx); err != nil {
		return nil, errors.Join(ErrChannelCreateRejected, err)
	}
	client := *c.httpClients[c.next.Add(1)%uint32(len(c.httpClients))]
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := client.Do(req)
	c.release()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("APNs channel request failed")
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

	return nil, newResponseError(resp.StatusCode, responseBody)
}
