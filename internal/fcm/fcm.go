package fcm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/providerhttp"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	fcmScope     = "https://www.googleapis.com/auth/firebase.messaging"
	fcmEndpoint  = "https://fcm.googleapis.com/v1/projects/%s/messages:send"
	oauthTimeout = 10 * time.Second
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
	maxRetries  int
	oauthGate   chan struct{}
	oauthToken  atomic.Pointer[oauth2.Token]
	loadToken   func(context.Context) (*oauth2.Token, error)
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

func NewClient(ctx context.Context, serviceAccountJSON []byte, poolSize int, maxConcurrent int, maxRetries int) (*Client, error) {
	var account struct {
		ProjectID string `json:"project_id"`
	}
	if json.Unmarshal(serviceAccountJSON, &account) != nil || account.ProjectID == "" {
		return nil, errors.New("missing FCM service account project ID")
	}
	creds, err := google.CredentialsFromJSON(ctx, serviceAccountJSON, fcmScope)
	if err != nil {
		return nil, errors.New("invalid FCM service account credentials")
	}
	return newClient(account.ProjectID, poolSize, maxConcurrent, maxRetries, oauth2.ReuseTokenSource(nil, creds.TokenSource)), nil
}

func NewClientForProject(ctx context.Context, serviceAccountJSON []byte, projectID string, poolSize int, maxConcurrent int, maxRetries int) (*Client, error) {
	if projectID == "" {
		return nil, errors.New("missing FCM target project ID")
	}
	if _, err := google.CredentialsFromJSON(ctx, serviceAccountJSON, fcmScope); err != nil {
		return nil, errors.New("invalid FCM service account credentials")
	}
	credentials := bytes.Clone(serviceAccountJSON)
	client := newClient(projectID, poolSize, maxConcurrent, maxRetries, nil)
	client.oauthGate = make(chan struct{}, 1)
	client.loadToken = func(ctx context.Context) (*oauth2.Token, error) {
		authClient := &http.Client{
			Transport:     oauthTransport{ctx: ctx},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		ctx = context.WithValue(ctx, oauth2.HTTPClient, authClient)
		creds, err := google.CredentialsFromJSON(ctx, credentials, fcmScope)
		if err != nil {
			return nil, err
		}
		return creds.TokenSource.Token()
	}

	return client, nil
}

func newClient(projectID string, poolSize, maxConcurrent, maxRetries int, legacyTokenSource oauth2.TokenSource) *Client {
	if poolSize < 1 {
		poolSize = 1
	}
	if maxConcurrent < 1 {
		maxConcurrent = poolSize * 90
	}
	if maxRetries < 0 {
		maxRetries = 0
	}

	httpClients := make([]*http.Client, poolSize)
	for i := range httpClients {
		httpClients[i] = &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				ForceAttemptHTTP2:   true,
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 100,
				IdleConnTimeout:     90 * time.Second,
				TLSHandshakeTimeout: 10 * time.Second,
			},
		}
		if legacyTokenSource != nil {
			httpClients[i].Transport = &oauth2.Transport{Source: legacyTokenSource, Base: httpClients[i].Transport}
		}
	}

	return &Client{
		httpClients: httpClients,
		sem:         make(chan struct{}, maxConcurrent),
		projectID:   projectID,
		maxRetries:  maxRetries,
	}
}

func (c *Client) Send(ctx context.Context, token string, payload *model.NotificationPayload) error {
	return c.SendWithAdmission(ctx, token, payload, nil)
}

func (c *Client) SendWithAdmission(ctx context.Context, token string, payload *model.NotificationPayload, admit model.ProviderAdmission) error {
	if payload == nil || token == "" {
		return model.ErrProviderFailed
	}
	if admit != nil && c.loadToken == nil {
		return model.ErrProviderAdmissionCheck
	}
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

	err := c.sendMessageWithAdmission(ctx, message, admit)
	if admit != nil {
		return model.SafeProviderError(err)
	}
	return err
}

func (c *Client) sendMessage(ctx context.Context, message FcmMessage) error {
	return c.sendMessageWithAdmission(ctx, message, nil)
}

func (c *Client) sendMessageWithAdmission(ctx context.Context, message FcmMessage, admit model.ProviderAdmission) error {
	payloadBytes, err := json.Marshal(FcmRequest{Message: message})
	if err != nil {
		return model.ErrProviderFailed
	}

	backoff := 500 * time.Millisecond
	retriedBearer := false

	for retries := 0; ; {
		req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf(fcmEndpoint, url.PathEscape(c.projectID)), bytes.NewReader(payloadBytes))
		if err != nil {
			return model.ErrProviderFailed
		}
		req.Header.Set("Content-Type", "application/json")

		select {
		case c.sem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		var bearer *oauth2.Token
		if c.loadToken != nil {
			bearer, err = c.accessToken(ctx)
			if err != nil {
				<-c.sem
				return err
			}
			bearer.SetAuthHeader(req)
		}
		resp, retry, err := c.attempt(req, admit)
		<-c.sem

		if err != nil {
			if retries < c.maxRetries && retry && ctx.Err() == nil {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(backoff):
				}
				backoff *= 2
				retries++
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
		if retries < c.maxRetries && (resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode == http.StatusServiceUnavailable ||
			resp.StatusCode == http.StatusInternalServerError) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
			retries++
			continue
		}
		err = parseFCMError(body, resp.StatusCode)
		if err != errBearerRejected {
			return err
		}
		if bearer == nil {
			return model.ErrProviderUnavailable
		}
		c.evictBearer(bearer.AccessToken)
		if retriedBearer {
			return model.ErrProviderUnavailable
		}
		retriedBearer = true
	}
}

func (c *Client) attempt(req *http.Request, admit model.ProviderAdmission) (*http.Response, bool, error) {
	client := c.httpClients[c.next.Add(1)%uint32(len(c.httpClients))]
	if admit == nil && c.loadToken == nil {
		resp, err := client.Do(req)
		if err != nil {
			return nil, isRetryableTransportError(err), model.ErrProviderFailed
		}
		return resp, false, nil
	}
	return providerhttp.Attempt(client, req, admit)
}

func isRetryableTransportError(err error) bool {
	errStr := err.Error()
	return strings.Contains(errStr, "GOAWAY") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "EOF") ||
		strings.Contains(errStr, "timeout")
}

type oauthTransport struct{ ctx context.Context }

func (t oauthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return http.DefaultTransport.RoundTrip(req.WithContext(t.ctx))
}

func (c *Client) accessToken(ctx context.Context) (*oauth2.Token, error) {
	ctx, cancel := context.WithTimeout(ctx, oauthTimeout)
	defer cancel()
	select {
	case c.oauthGate <- struct{}{}:
		defer func() { <-c.oauthGate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cached := c.oauthToken.Load(); cached.Valid() {
		return cached, nil
	}
	token, err := c.loadToken(ctx)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, model.ErrProviderUnavailable
	}
	if !token.Valid() {
		return nil, model.ErrProviderUnavailable
	}
	c.oauthToken.Store(token)
	return token, nil
}

func (c *Client) evictBearer(rejected string) {
	for cached := c.oauthToken.Load(); cached != nil && cached.AccessToken == rejected; cached = c.oauthToken.Load() {
		if c.oauthToken.CompareAndSwap(cached, nil) {
			return
		}
	}
}

var errBearerRejected = errors.New("FCM bearer rejected")

type unregisteredTokenError struct{}

func (unregisteredTokenError) Error() string          { return "registration-token-not-registered" }
func (unregisteredTokenError) ProviderReason() string { return "registration-token-not-registered" }
func (unregisteredTokenError) Is(target error) bool   { return target == model.ErrProviderInvalidToken }

func parseFCMError(body []byte, status int) error {
	var response struct {
		Error struct {
			Status  string `json:"status"`
			Details []struct {
				Type      string `json:"@type"`
				ErrorCode string `json:"errorCode"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &response) != nil {
		return model.ErrProviderFailed
	}
	for _, detail := range response.Error.Details {
		if detail.Type != "type.googleapis.com/google.firebase.fcm.v1.FcmError" {
			continue
		}
		switch detail.ErrorCode {
		case "UNREGISTERED":
			if status == http.StatusNotFound {
				return unregisteredTokenError{}
			}
		case "SENDER_ID_MISMATCH", "THIRD_PARTY_AUTH_ERROR":
			return model.ErrProviderFailed
		}
	}
	if status == http.StatusUnauthorized && response.Error.Status == "UNAUTHENTICATED" {
		return errBearerRejected
	}
	if status == http.StatusForbidden && response.Error.Status == "PERMISSION_DENIED" {
		return model.ErrProviderAuthorization
	}
	return model.ErrProviderFailed
}
