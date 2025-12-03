package fcm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/mithileshchellappan/pushboy/internal/dispatch"
	"github.com/mithileshchellappan/pushboy/internal/storage"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	fcmScope    = "https://www.googleapis.com/auth/firebase.messaging"
	fcmEndpoint = "https://fcm.googleapis.com/v1/projects/%s/messages:send"
)

type Client struct {
	httpClient *http.Client
	projectID  string
}

// FcmRequest is the top-level FCM HTTP v1 request
type FcmRequest struct {
	Message FcmMessage `json:"message"`
}

// FcmMessage represents the FCM message structure
type FcmMessage struct {
	Token        string            `json:"token"`
	Notification *Notification     `json:"notification,omitempty"` // nil for silent/data-only messages
	Data         map[string]string `json:"data,omitempty"`
	Android      *AndroidConfig    `json:"android,omitempty"`
}

// Notification is the visible notification content
type Notification struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
	Image string `json:"image,omitempty"` // FCM supports image URLs natively
}

// AndroidConfig contains Android-specific options
type AndroidConfig struct {
	CollapseKey  string               `json:"collapse_key,omitempty"`
	Priority     string               `json:"priority,omitempty"` // "HIGH" or "NORMAL"
	TTL          string               `json:"ttl,omitempty"`      // Duration string like "3600s"
	Notification *AndroidNotification `json:"notification,omitempty"`
}

// AndroidNotification contains Android notification options
type AndroidNotification struct {
	Sound       string `json:"sound,omitempty"`
	Tag         string `json:"tag,omitempty"`          // Groups notifications (like thread-id)
	ClickAction string `json:"click_action,omitempty"` // Maps to category
}

func NewClient(ctx context.Context, serviceAccountJson []byte) (*Client, error) {
	creds, err := google.CredentialsFromJSON(ctx, serviceAccountJson, fcmScope)
	if err != nil {
		return nil, err
	}
	var rawJSON map[string]interface{}
	if err := json.Unmarshal(serviceAccountJson, &rawJSON); err != nil {
		return nil, err
	}
	projectID, ok := rawJSON["project_id"].(string)
	if !ok {
		return nil, fmt.Errorf("project_id not found in service account JSON")
	}
	httpClient := oauth2.NewClient(ctx, creds.TokenSource)
	httpClient.Timeout = 10 * time.Second

	return &Client{httpClient: httpClient, projectID: projectID}, nil
}

func (c *Client) Send(ctx context.Context, token *storage.Token, payload *dispatch.NotificationPayload) error {
	message := FcmMessage{
		Token: token.Token,
	}

	// For silent notifications, omit the notification block entirely
	// The app will receive it as a data-only message
	if !payload.Silent {
		notification := &Notification{
			Title: payload.Title,
			Body:  payload.Body,
		}
		// FCM supports image URLs natively
		if payload.ImageURL != "" {
			notification.Image = payload.ImageURL
		}
		message.Notification = notification
	}

	// Custom data payload
	if len(payload.Data) > 0 {
		message.Data = payload.Data
	}

	// Android-specific configuration
	android := &AndroidConfig{}
	hasAndroidConfig := false

	// Collapse key for coalescing notifications
	if payload.CollapseID != "" {
		android.CollapseKey = payload.CollapseID
		hasAndroidConfig = true
	}

	// Priority: HIGH for immediate, NORMAL for power-considerate
	if payload.Priority == "normal" {
		android.Priority = "NORMAL"
		hasAndroidConfig = true
	} else if payload.Priority == "high" || payload.Priority == "" {
		android.Priority = "HIGH"
		hasAndroidConfig = true
	}

	// TTL (time-to-live) in seconds -> FCM wants duration string like "3600s"
	if payload.TTL > 0 {
		android.TTL = fmt.Sprintf("%ds", payload.TTL)
		hasAndroidConfig = true
	}

	// Android notification options
	androidNotif := &AndroidNotification{}
	hasAndroidNotif := false

	if payload.Sound != "" {
		androidNotif.Sound = payload.Sound
		hasAndroidNotif = true
	}

	// Thread ID -> Android tag (groups notifications)
	if payload.ThreadID != "" {
		androidNotif.Tag = payload.ThreadID
		hasAndroidNotif = true
	}

	// Category -> click_action
	if payload.Category != "" {
		androidNotif.ClickAction = payload.Category
		hasAndroidNotif = true
	}

	if hasAndroidNotif {
		android.Notification = androidNotif
		hasAndroidConfig = true
	}

	if hasAndroidConfig {
		message.Android = android
	}

	request := FcmRequest{Message: message}

	payloadBytes, err := json.Marshal(request)
	if err != nil {
		log.Printf("Error marshalling message: %v", err)
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf(fcmEndpoint, c.projectID), bytes.NewBuffer(payloadBytes))
	if err != nil {
		log.Printf("Error creating request: %v", err)
		return err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.Printf("Error sending request: %v", err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to send notification: %s", resp.Status)
	}

	return nil
}
