package apns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/mithileshchellappan/pushboy/internal/dispatch"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

const (
	DevelopmentEndpoint = "https://api.sandbox.push.apple.com"
	ProductionEndpoint  = "https://api.push.apple.com"
)

type Client struct {
	httpClient *http.Client
	keyID      string
	teamID     string
	topicID    string
	signingKey []byte
	endpoint   string
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

	return &Client{
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		keyID:      keyID,
		teamID:     teamID,
		topicID:    topicID,
		signingKey: p8KeyBytes,
		endpoint:   endpoint,
	}
}

func (c *Client) generateJWT() (string, error) {
	token := jwt.New(jwt.SigningMethodES256)

	claims := token.Claims.(jwt.MapClaims)
	claims["iss"] = c.teamID
	claims["iat"] = time.Now().Unix()
	claims["exp"] = time.Now().Add(time.Hour * 24).Unix()

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
	jwt, err := c.generateJWT()
	if err != nil {
		return err
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

	// Sound
	if payload.Sound != "" {
		aps.Sound = payload.Sound
	}

	// Badge (nil means don't change, explicit value sets it)
	if payload.Badge != nil {
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

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return err
	}

	// Required headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", jwt))
	req.Header.Set("apns-topic", c.topicID)

	// Push type: background for silent, alert for regular
	if payload.Silent {
		req.Header.Set("apns-push-type", "background")
	} else {
		req.Header.Set("apns-push-type", "alert")
	}

	// Priority: 10 = high (immediate), 5 = normal (power-considerate)
	if payload.Priority == "normal" {
		req.Header.Set("apns-priority", "5")
	} else {
		req.Header.Set("apns-priority", "10")
	}

	// Collapse ID for coalescing notifications
	if payload.CollapseID != "" {
		req.Header.Set("apns-collapse-id", payload.CollapseID)
	}

	// TTL / Expiration
	if payload.TTL > 0 {
		expiration := time.Now().Add(time.Duration(payload.TTL) * time.Second).Unix()
		req.Header.Set("apns-expiration", strconv.FormatInt(expiration, 10))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to send notification: %s", resp.Status)
	}

	return nil
}
