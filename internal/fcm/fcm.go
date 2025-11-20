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

type FcmRequest struct {
	Message FcmMessage `json:"message"`
}

type FcmMessage struct {
	Token        string            `json:"token"`
	Notification Notification      `json:"notification"`
	Data         map[string]string `json:"data"`
}

type Notification struct {
	Title string `json:"title"`
	Body  string `json:"body"`
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
	projectID := rawJSON["project_id"].(string)
	httpClient := oauth2.NewClient(ctx, creds.TokenSource)
	httpClient.Timeout = 10 * time.Second

	return &Client{httpClient: httpClient, projectID: projectID}, nil
}

func (c *Client) Send(ctx context.Context, sub *storage.Subscription, payload *dispatch.NotificationPayload) error {
	message := FcmMessage{
		Token: sub.Token,
		Notification: Notification{
			Title: payload.Title,
			Body:  payload.Body,
		},
	}

	payloadBytes, err := json.Marshal(message)

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
