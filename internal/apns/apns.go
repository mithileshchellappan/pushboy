package apns

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/providerhttp"
)

const (
	DevelopmentEndpoint = "https://api.sandbox.push.apple.com"
	ProductionEndpoint  = "https://api.push.apple.com"

	jwtRefreshBuffer  = 5 * time.Minute
	jwtValidityPeriod = 1 * time.Hour

	initialBackoff = 100 * time.Millisecond
	maxBackoff     = 2 * time.Second
)

type Client struct {
	httpClients       []*http.Client
	next              atomic.Uint32
	sem               chan struct{} // caps in-flight requests; nil = unlimited
	keyID             string
	teamID            string
	bundleID          string
	signingKey        []byte
	endpoint          string
	broadcastEndpoint string
	maxRetries        int

	channelManagementEndpoint string

	jwtMutex  sync.RWMutex
	cachedJWT string
	jwtExpiry time.Time
}

type Alert struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
}

type ApsPayload struct {
	Alert            *Alert `json:"alert,omitempty"`
	Sound            string `json:"sound,omitempty"`
	Badge            *int   `json:"badge,omitempty"`
	ContentAvailable int    `json:"content-available,omitempty"`
	MutableContent   int    `json:"mutable-content,omitempty"`
	ThreadID         string `json:"thread-id,omitempty"`
	Category         string `json:"category,omitempty"`
}

type ApnsRequest map[string]interface{}

type responseError struct {
	status int
	reason string
}

func (e *responseError) Error() string {
	if e.reason == "" {
		return fmt.Sprintf("APNs error: %d %s", e.status, http.StatusText(e.status))
	}
	return fmt.Sprintf("APNs error: %d %s (reason: %s)", e.status, http.StatusText(e.status), e.reason)
}

func (e *responseError) ProviderReason() string {
	return e.reason
}

func (e *responseError) Is(target error) bool {
	switch target {
	case model.ErrProviderInvalidToken:
		return (e.status == http.StatusBadRequest && e.reason == "BadDeviceToken") ||
			(e.status == http.StatusGone && (e.reason == "Unregistered" || e.reason == "ExpiredToken"))
	case model.ErrProviderAuthorization:
		switch e.reason {
		case "BadCertificate", "BadCertificateEnvironment", "Forbidden", "InvalidProviderToken", "MissingProviderToken", "TopicMismatch":
			return e.status == http.StatusForbidden
		case "TopicDisallowed":
			return e.status == http.StatusBadRequest || e.status == http.StatusForbidden
		}
	case ErrChannelRequestRejected:
		if e.Is(model.ErrProviderAuthorization) {
			return true
		}
		switch e.status {
		case http.StatusForbidden:
			return e.reason == "ExpiredProviderToken"
		case http.StatusBadRequest:
			switch e.reason {
			case "BadChannelId", "BadExpirationDate", "BadMessageId", "BadMessageStoragePolicy", "BadPriority", "BadPushType",
				"BadRequestParams", "BadRequestPayload", "BadTopic", "CannotCreateChannelConfig", "ChannelNotRegistered",
				"DuplicateHeaders", "FeatureNotEnabled", "InvalidPushType", "MissingChannelId", "MissingPushType", "MissingTopic",
				"PayloadEmpty":
				return true
			}
		case http.StatusNotFound:
			return e.reason == "BadPath"
		case http.StatusMethodNotAllowed:
			return e.reason == "MethodNotAllowed"
		case http.StatusRequestEntityTooLarge:
			return e.reason == "PayloadTooLarge"
		case http.StatusTooManyRequests:
			return e.reason == "TooManyRequests" || e.reason == "TooManyProviderTokenUpdates"
		}
	}
	return false
}

func newResponseError(status int, body []byte) *responseError {
	var response struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal(body, &response) != nil {
		return &responseError{status: status}
	}
	switch response.Reason {
	case "BadCertificate", "BadCertificateEnvironment", "BadChannelId", "BadDeviceToken", "BadExpirationDate",
		"BadMessageId", "BadMessageStoragePolicy", "BadPath", "BadPriority", "BadPushType", "BadRequestParams",
		"BadRequestPayload", "BadTopic", "CannotCreateChannelConfig", "ChannelNotRegistered", "DeviceTokenNotForTopic",
		"DuplicateHeaders", "ExpiredProviderToken", "ExpiredToken", "FeatureNotEnabled", "Forbidden", "IdleTimeout",
		"InternalServerError", "InvalidProviderToken", "InvalidPushType", "MethodNotAllowed", "MissingChannelId",
		"MissingDeviceToken", "MissingProviderToken", "MissingPushType", "MissingTopic", "PayloadEmpty", "PayloadTooLarge",
		"ServiceUnavailable", "Shutdown", "TooManyProviderTokenUpdates", "TooManyRequests", "TopicDisallowed", "TopicMismatch",
		"Unregistered":
		return &responseError{status: status, reason: response.Reason}
	}
	return &responseError{status: status}
}

func NewClient(p8KeyBytes []byte, keyID string, teamID string, bundleID string, useSandbox bool, endpointOverride string, poolSize int, maxConcurrent int, maxRetries int) *Client {
	var endpoint string
	if endpointOverride != "" {
		endpoint = endpointOverride
	} else if useSandbox {
		endpoint = DevelopmentEndpoint
	} else {
		endpoint = ProductionEndpoint
	}

	if poolSize < 1 {
		poolSize = 1
	}
	if maxConcurrent < 1 {
		// APNs allows ~1000 concurrent streams per connection once
		// authenticated; stay under that per pooled transport.
		maxConcurrent = poolSize * 900
	}
	if maxRetries < 0 {
		maxRetries = 0
	}

	// Pool of transports: each owns its TCP connection(s) to APNs, so streams
	// spread across congestion windows instead of sharing one pipe. The
	// semaphore keeps excess senders waiting here cheaply instead of queueing
	// inside a transport until the request timeout kills them.
	//
	// Idle pool is sized to the concurrency cap: HTTP/2 multiplexes over a
	// handful of connections anyway, but HTTP/1.1 paths (proxies, test
	// endpoints) would otherwise churn sockets into TIME_WAIT under load.
	idlePerClient := maxConcurrent/poolSize + 10
	clients := make([]*http.Client, poolSize)
	for i := range clients {
		clients[i] = &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          idlePerClient,
				MaxIdleConnsPerHost:   idlePerClient,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			},
		}
	}

	channelManagementEndpoint := productionChannelManagementEndpoint
	broadcastEndpoint := productionBroadcastEndpoint
	if useSandbox {
		channelManagementEndpoint = developmentChannelManagementEndpoint
		broadcastEndpoint = developmentBroadcastEndpoint
	}
	if endpointOverride != "" {
		channelManagementEndpoint = endpointOverride
		broadcastEndpoint = endpointOverride
	}

	return &Client{
		httpClients:       clients,
		sem:               make(chan struct{}, maxConcurrent),
		keyID:             keyID,
		teamID:            teamID,
		bundleID:          bundleID,
		signingKey:        p8KeyBytes,
		endpoint:          endpoint,
		broadcastEndpoint: broadcastEndpoint,
		maxRetries:        maxRetries,

		channelManagementEndpoint: channelManagementEndpoint,
	}
}

func (c *Client) getJWT() (string, error) {
	c.jwtMutex.RLock()
	if c.cachedJWT != "" && time.Now().Add(jwtRefreshBuffer).Before(c.jwtExpiry) {
		jwt := c.cachedJWT
		c.jwtMutex.RUnlock()
		return jwt, nil
	}
	c.jwtMutex.RUnlock()

	c.jwtMutex.Lock()
	defer c.jwtMutex.Unlock()

	if c.cachedJWT != "" && time.Now().Add(jwtRefreshBuffer).Before(c.jwtExpiry) {
		return c.cachedJWT, nil
	}

	jwt, err := c.generateJWT()
	if err != nil {
		return "", err
	}

	c.cachedJWT = jwt
	c.jwtExpiry = time.Now().Add(jwtValidityPeriod)
	return jwt, nil
}

func (c *Client) generateJWT() (string, error) {
	token := jwt.New(jwt.SigningMethodES256)

	now := time.Now()
	claims := token.Claims.(jwt.MapClaims)
	claims["iss"] = c.teamID
	claims["iat"] = now.Unix()
	claims["exp"] = now.Add(jwtValidityPeriod).Unix()

	token.Header["kid"] = c.keyID
	key, err := jwt.ParseECPrivateKeyFromPEM(c.signingKey)
	if err != nil {
		return "", err
	}

	tokenString, err := token.SignedString(key)
	if err != nil {
		return "", err
	}
	return tokenString, nil
}

func (c *Client) Send(ctx context.Context, token string, payload *model.NotificationPayload) error {
	return c.SendWithAdmission(ctx, token, payload, nil)
}

func (c *Client) SendWithAdmission(ctx context.Context, token string, payload *model.NotificationPayload, admit model.ProviderAdmission) error {
	if payload == nil || token == "" {
		return model.ErrProviderFailed
	}
	var jwtToken string
	if admit == nil {
		var err error
		jwtToken, err = c.getJWT()
		if err != nil {
			return model.ErrProviderFailed
		}
	}

	aps := ApsPayload{}

	if !payload.Silent {
		aps.Alert = &Alert{
			Title: payload.Title,
			Body:  payload.Body,
		}
	}

	if payload.Sound != "" && !payload.Silent {
		aps.Sound = payload.Sound
	}

	if payload.Badge != nil && !payload.Silent {
		aps.Badge = payload.Badge
	}

	if payload.Silent {
		aps.ContentAvailable = 1
	}

	if payload.ImageURL != "" {
		aps.MutableContent = 1
	}

	if payload.ThreadID != "" {
		aps.ThreadID = payload.ThreadID
	}

	if payload.Category != "" {
		aps.Category = payload.Category
	}

	apnsRequest := ApnsRequest{
		"aps": aps,
	}

	for key, value := range payload.Data {
		apnsRequest[key] = value
	}

	if payload.ImageURL != "" {
		apnsRequest["image_url"] = payload.ImageURL
	}

	payloadBytes, err := json.Marshal(apnsRequest)
	if err != nil {
		return model.ErrProviderFailed
	}

	endpoint := fmt.Sprintf("%s/3/device/%s", c.endpoint, url.PathEscape(token))

	headers := map[string]string{
		"apns-topic": c.bundleID,
	}

	if payload.Silent {
		headers["apns-push-type"] = "background"
	} else {
		headers["apns-push-type"] = "alert"
	}

	if payload.Silent {
		if payload.Priority == "high" {
			log.Printf("Warning: Silent notification requested with high priority - forcing priority 5 per APNs requirements")
		}
		headers["apns-priority"] = "5"
	} else if payload.Priority == "normal" {
		headers["apns-priority"] = "5"
	} else {
		headers["apns-priority"] = "10"
	}

	if payload.CollapseID != "" {
		headers["apns-collapse-id"] = payload.CollapseID
	}

	if payload.TTL > 0 {
		expiration := time.Now().Add(time.Duration(payload.TTL) * time.Second).Unix()
		headers["apns-expiration"] = strconv.FormatInt(expiration, 10)
	} else if payload.Silent {
		expiration := time.Now().Add(1 * time.Hour).Unix()
		headers["apns-expiration"] = strconv.FormatInt(expiration, 10)
	}

	err = c.sendWithRetryAdmission(ctx, endpoint, payloadBytes, jwtToken, headers, admit)
	if admit != nil {
		return model.SafeProviderError(err)
	}
	return err
}

func (c *Client) sendWithRetry(ctx context.Context, url string, payloadBytes []byte, jwtToken string, headers map[string]string) error {
	return c.sendWithRetryAdmission(ctx, url, payloadBytes, jwtToken, headers, nil)
}

func (c *Client) sendWithRetryAdmission(ctx context.Context, url string, payloadBytes []byte, jwtToken string, headers map[string]string, admit model.ProviderAdmission) error {
	backoff := initialBackoff

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payloadBytes))
		if err != nil {
			return errors.New("invalid APNs request")
		}

		req.Header.Set("Content-Type", "application/json")
		for key, value := range headers {
			if value != "" {
				req.Header.Set(key, value)
			}
		}

		if err := c.acquire(ctx); err != nil {
			return err
		}
		resp, retry, err := c.attempt(req, jwtToken, admit)
		c.release()
		if err != nil {
			if attempt < c.maxRetries && retry && ctx.Err() == nil {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(backoff):
				}
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
				continue
			}
			if ctx.Err() != nil && err == model.ErrProviderFailed {
				return ctx.Err()
			}
			return err
		}

		body := providerhttp.ReadResponseBody(resp.Body)

		if resp.StatusCode == http.StatusOK {
			return nil
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			if attempt < c.maxRetries {
				if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
					if seconds, err := strconv.Atoi(retryAfter); err == nil {
						backoff = time.Duration(seconds) * time.Second
					}
				}

				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(backoff):
				}

				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
				continue
			}
			return fmt.Errorf("APNs rate limited after %d retries", c.maxRetries)
		}

		providerErr := newResponseError(resp.StatusCode, body)
		if providerErr.reason != "" || errors.Is(providerErr, model.ErrProviderAuthorization) {
			return providerErr
		}
		return fmt.Errorf("failed to send notification: %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	return fmt.Errorf("max retries exceeded")
}

func (c *Client) attempt(req *http.Request, jwtToken string, admit model.ProviderAdmission) (*http.Response, bool, error) {
	client := c.httpClients[c.next.Add(1)%uint32(len(c.httpClients))]
	if admit == nil {
		req.Header.Set("Authorization", "Bearer "+jwtToken)
		resp, err := client.Do(req)
		if err != nil {
			return nil, isRetryableError(err), model.ErrProviderFailed
		}
		return resp, false, nil
	}

	jwtToken, err := c.getJWT()
	if err != nil {
		return nil, false, model.ErrProviderUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+jwtToken)
	return providerhttp.Attempt(client, req, admit)
}

// acquire blocks until an in-flight slot frees up. A nil semaphore (tests
// constructing Client directly) means unlimited concurrency.
func (c *Client) acquire(ctx context.Context) error {
	if c.sem == nil {
		return nil
	}
	select {
	case c.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) release() {
	if c.sem != nil {
		<-c.sem
	}
}

func isRetryableError(err error) bool {
	errStr := err.Error()
	return strings.Contains(errStr, "GOAWAY") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "EOF") ||
		strings.Contains(errStr, "timeout")
}
