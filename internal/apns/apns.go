package apns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
	signingKey []byte
	endpoint   string
}

type ApsPayload struct {
	Alert Alert `json:"alert"`
}

type Alert struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type ApnsRequest struct {
	Aps ApsPayload `json:"aps"`
}

func NewClient(p8KeyBytes []byte, keyID string, teamID string, useSandbox bool) *Client {
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

func (c *Client) Send(ctx context.Context, sub *storage.Subscription, payload *dispatch.NotificationPayload) error {
	jwt, err := c.generateJWT()
	if err != nil {
		return err
	}

	payloadData := ApnsRequest{
		Aps: ApsPayload{
			Alert: Alert{
				Title: payload.Title,
				Body:  payload.Body,
			},
		},
	}

	payloadBytes, err := json.Marshal(payloadData)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/3/device/%s", c.endpoint, sub.Token)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(payloadBytes))

	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", jwt))
	req.Header.Set("apns-topic", "hardcoded.app")

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
