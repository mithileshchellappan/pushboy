package fcm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mithileshchellappan/pushboy/internal/model"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	fcmScope    = "https://www.googleapis.com/auth/firebase.messaging"
	fcmEndpoint = "https://fcm.googleapis.com/v1/projects/%s/messages:send"
)

// Client sends through a pool of HTTP clients, each with its own transport
// (and so its own HTTP/2 connections to FCM, ~100 streams each). The semaphore
// caps in-flight requests so excess senders wait here cheaply instead of
// queueing inside a transport until the request timeout kills them.
type Client struct {
	httpClients []*http.Client
	next        atomic.Uint32
	sem         chan struct{}
	projectID   string
}

type FcmRequest struct {
	Message FcmMessage `json:"message"`
}

type FcmMessage struct {
	Token        string            `json:"token"`
	Notification *Notification     `json:"notification,omitempty"`
	Data         map[string]string `json:"data,omitempty"`
	Android      *AndroidConfig    `json:"android,omitempty"`
}

type Notification struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
	Image string `json:"image,omitempty"`
}

type AndroidConfig struct {
	CollapseKey  string               `json:"collapse_key,omitempty"`
	Priority     string               `json:"priority,omitempty"`
	TTL          string               `json:"ttl,omitempty"`
	Notification *AndroidNotification `json:"notification,omitempty"`
}

type AndroidNotification struct {
	Sound       string `json:"sound,omitempty"`
	Tag         string `json:"tag,omitempty"`
	ClickAction string `json:"click_action,omitempty"`
}

func NewClient(ctx context.Context, serviceAccountJson []byte, poolSize int, maxConcurrent int) (*Client, error) {
	creds, err := google.CredentialsFromJSON(ctx, serviceAccountJson, fcmScope)
	if err != nil {
		return nil, err
	}
	var rawJSON map[string]any
	if err := json.Unmarshal(serviceAccountJson, &rawJSON); err != nil {
		return nil, err
	}
	projectID, ok := rawJSON["project_id"].(string)
	if !ok {
		return nil, fmt.Errorf("project_id not found in service account JSON")
	}
	if poolSize < 1 {
		poolSize = 1
	}
	if maxConcurrent < 1 {
		maxConcurrent = poolSize * 90
	}

	tokenSource := oauth2.ReuseTokenSource(nil, creds.TokenSource)
	httpClients := make([]*http.Client, poolSize)
	for i := range httpClients {
		httpClients[i] = &http.Client{
			Timeout: 10 * time.Second,
			Transport: &oauth2.Transport{
				Source: tokenSource,
				Base: &http.Transport{
					ForceAttemptHTTP2:   true,
					MaxIdleConns:        100,
					MaxIdleConnsPerHost: 100,
					IdleConnTimeout:     90 * time.Second,
					TLSHandshakeTimeout: 10 * time.Second,
				},
			},
		}
	}

	return &Client{
		httpClients: httpClients,
		sem:         make(chan struct{}, maxConcurrent),
		projectID:   projectID,
	}, nil
}

func (c *Client) Send(ctx context.Context, token string, payload *model.NotificationPayload) error {
	message := FcmMessage{
		Token: token,
	}

	if !payload.Silent {
		notification := &Notification{
			Title: payload.Title,
			Body:  payload.Body,
		}
		if payload.ImageURL != "" {
			notification.Image = payload.ImageURL
		}
		message.Notification = notification
	}

	if len(payload.Data) > 0 {
		message.Data = payload.Data
	}

	if payload.Silent && len(payload.Data) == 0 {
		log.Printf("Warning: Silent notification sent without data payload")
	}

	android := &AndroidConfig{}
	hasAndroidConfig := false

	if payload.CollapseID != "" {
		android.CollapseKey = payload.CollapseID
		hasAndroidConfig = true
	}

	if payload.Silent {
		if payload.Priority == "high" {
			log.Printf("Warning: Silent notification requested with high priority - forcing NORMAL per FCM requirements")
		}
		android.Priority = "NORMAL"
		hasAndroidConfig = true
	} else if payload.Priority == "normal" {
		android.Priority = "NORMAL"
		hasAndroidConfig = true
	} else {
		android.Priority = "HIGH"
		hasAndroidConfig = true
	}

	if payload.TTL > 0 {
		android.TTL = fmt.Sprintf("%ds", payload.TTL)
		hasAndroidConfig = true
	}

	if !payload.Silent {
		androidNotif := &AndroidNotification{}
		hasAndroidNotif := false

		if payload.Sound != "" {
			androidNotif.Sound = payload.Sound
			hasAndroidNotif = true
		}

		if payload.ThreadID != "" {
			androidNotif.Tag = payload.ThreadID
			hasAndroidNotif = true
		}

		if payload.Category != "" {
			androidNotif.ClickAction = payload.Category
			hasAndroidNotif = true
		}

		if hasAndroidNotif {
			android.Notification = androidNotif
			hasAndroidConfig = true
		}
	}

	if hasAndroidConfig {
		message.Android = android
	}

	return c.sendMessage(ctx, message)
}

func (c *Client) sendMessage(ctx context.Context, message FcmMessage) error {
	payloadBytes, err := json.Marshal(FcmRequest{Message: message})
	if err != nil {
		log.Printf("Error marshalling message: %v", err)
		return err
	}

	const maxRetries = 2
	backoff := 500 * time.Millisecond

	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf(fcmEndpoint, c.projectID), bytes.NewReader(payloadBytes))
		if err != nil {
			log.Printf("Error creating request: %v", err)
			return err
		}
		req.Header.Set("Content-Type", "application/json")

		select {
		case c.sem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		httpClient := c.httpClients[c.next.Add(1)%uint32(len(c.httpClients))]
		resp, err := httpClient.Do(req)
		<-c.sem

		if err != nil {
			// dropped connections (GOAWAY, reset, timeout) are retryable;
			// the next attempt round-robins onto a different transport
			if attempt < maxRetries && isRetryableTransportError(err) {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(backoff):
				}
				backoff *= 2
				continue
			}
			log.Printf("Error sending request: %v", err)
			return err
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			return nil
		}
		if attempt < maxRetries && (resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode == http.StatusServiceUnavailable ||
			resp.StatusCode == http.StatusInternalServerError) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
			continue
		}
		return parseFCMError(body, resp.Status)
	}
}

func isRetryableTransportError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "GOAWAY") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "EOF") ||
		strings.Contains(errStr, "timeout")
}

func parseFCMError(body []byte, fallbackStatus string) error {
	var fcmError struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
			Details []struct {
				Type      string `json:"@type"`
				ErrorCode string `json:"errorCode"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &fcmError); err == nil {
		for _, detail := range fcmError.Error.Details {
			if strings.EqualFold(detail.ErrorCode, "UNREGISTERED") {
				return fmt.Errorf("FCM error: registration-token-not-registered")
			}
		}
		if fcmError.Error.Message != "" {
			return fmt.Errorf("FCM error: %s (status: %s, code: %d)", fcmError.Error.Message, fcmError.Error.Status, fcmError.Error.Code)
		}
	}
	return fmt.Errorf("failed to send notification: %s", fallbackStatus)
}
