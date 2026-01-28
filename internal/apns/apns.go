package apns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/mithileshchellappan/pushboy/internal/dispatch"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

const (
	DevelopmentEndpoint = "https://api.sandbox.push.apple.com"
	ProductionEndpoint  = "https://api.push.apple.com"

	jwtRefreshBuffer  = 5 * time.Minute
	jwtValidityPeriod = 1 * time.Hour

	maxRetries     = 3
	initialBackoff = 100 * time.Millisecond
	maxBackoff     = 2 * time.Second
)

type Client struct {
	httpClient *http.Client
	keyID      string
	teamID     string
	topicID    string
	signingKey []byte
	endpoint   string

	jwtMutex  sync.RWMutex
	cachedJWT string
	jwtExpiry time.Time
}

// Alert represents the visible notification content
type Alert struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
}

// ApsPayload is the APNs-specific payload structure
type ApsPayload struct {
	Alert            *Alert `json:"alert,omitempty"`
	Sound            string `json:"sound,omitempty"`
	Badge            *int   `json:"badge,omitempty"`
	ContentAvailable int    `json:"content-available,omitempty"`
	MutableContent   int    `json:"mutable-content,omitempty"`
	ThreadID         string `json:"thread-id,omitempty"`
	Category         string `json:"category,omitempty"`
}

// ApnsRequest is the full request body sent to APNs
// Custom data fields are added at the top level alongside "aps"
type ApnsRequest map[string]interface{}

func NewClient(p8KeyBytes []byte, keyID string, teamID string, topicID string, useSandbox bool) *Client {
	var endpoint string
	if useSandbox {
		endpoint = DevelopmentEndpoint
	} else {
		endpoint = ProductionEndpoint
	}

	transport := &http.Transport{
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	return &Client{
		httpClient: &http.Client{
			Timeout:   10 * time.Second,
			Transport: transport,
		},
		keyID:      keyID,
		teamID:     teamID,
		topicID:    topicID,
		signingKey: p8KeyBytes,
		endpoint:   endpoint,
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

func (c *Client) Send(ctx context.Context, token *storage.Token, payload *dispatch.NotificationPayload) error {
	jwtToken, err := c.getJWT()
	if err != nil {
		return fmt.Errorf("failed to get JWT: %w", err)
	}

	// Build the APS payload
	aps := ApsPayload{}

	// For silent notifications, don't include alert
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

	// Silent/background notification
	if payload.Silent {
		aps.ContentAvailable = 1
	}

	// Mutable content for rich notifications (images, etc.)
	// Enable if image_url is provided so app's Notification Service Extension can fetch it
	if payload.ImageURL != "" {
		aps.MutableContent = 1
	}

	// Thread ID for grouping
	if payload.ThreadID != "" {
		aps.ThreadID = payload.ThreadID
	}

	// Category for actionable notifications
	if payload.Category != "" {
		aps.Category = payload.Category
	}

	// Build full request with custom data
	apnsRequest := ApnsRequest{
		"aps": aps,
	}

	// Add custom data at top level (outside aps)
	for key, value := range payload.Data {
		apnsRequest[key] = value
	}

	// Pass image URL in custom data for Service Extension to fetch
	if payload.ImageURL != "" {
		apnsRequest["image_url"] = payload.ImageURL
	}

	payloadBytes, err := json.Marshal(apnsRequest)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/3/device/%s", c.endpoint, token.Token)

	return c.sendWithRetry(ctx, url, payloadBytes, jwtToken, payload)
}

func (c *Client) sendWithRetry(ctx context.Context, url string, payloadBytes []byte, jwtToken string, payload *dispatch.NotificationPayload) error {
	backoff := initialBackoff

	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payloadBytes))
		if err != nil {
			return err
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", jwtToken))
		req.Header.Set("apns-topic", c.topicID)

		if payload.Silent {
			req.Header.Set("apns-push-type", "background")
		} else {
			req.Header.Set("apns-push-type", "alert")
		}

		if payload.Silent {
			if payload.Priority == "high" {
				log.Printf("Warning: Silent notification requested with high priority - forcing priority 5 per APNs requirements")
			}
			req.Header.Set("apns-priority", "5")
		} else if payload.Priority == "normal" {
			req.Header.Set("apns-priority", "5")
		} else {
			req.Header.Set("apns-priority", "10")
		}

		if payload.CollapseID != "" {
			req.Header.Set("apns-collapse-id", payload.CollapseID)
		}

		if payload.TTL > 0 {
			expiration := time.Now().Add(time.Duration(payload.TTL) * time.Second).Unix()
			req.Header.Set("apns-expiration", strconv.FormatInt(expiration, 10))
		} else if payload.Silent {
			expiration := time.Now().Add(1 * time.Hour).Unix()
			req.Header.Set("apns-expiration", strconv.FormatInt(expiration, 10))
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			// Retry on connection errors (GOAWAY, connection reset, etc.)
			if attempt < maxRetries && isRetryableError(err) {
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
			return err
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			return nil
		}

		// Parse APNs error response for better debugging
		var apnsError struct {
			Reason    string `json:"reason"`
			Timestamp int64  `json:"timestamp,omitempty"`
		}
		if err := json.Unmarshal(body, &apnsError); err == nil && apnsError.Reason != "" {
			// Log specific error for debugging
			log.Printf("APNs error for device: %s (reason: %s)", resp.Status, apnsError.Reason)
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			if attempt < maxRetries {
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
			return fmt.Errorf("rate limited after %d retries: %s", maxRetries, resp.Status)
		}

		// Include APNs reason in error if available
		var apnsReason struct {
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(body, &apnsReason); err == nil && apnsReason.Reason != "" {
			return fmt.Errorf("APNs error: %s (reason: %s)", resp.Status, apnsReason.Reason)
		}
		return fmt.Errorf("failed to send notification: %s", resp.Status)
	}

	return fmt.Errorf("max retries exceeded")
}

// isRetryableError checks if an error is a transient connection issue worth retrying
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "GOAWAY") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "EOF") ||
		strings.Contains(errStr, "timeout")
}
