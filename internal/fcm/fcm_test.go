package fcm

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mithileshchellappan/pushboy/internal/model"
	"golang.org/x/oauth2"
)

func TestParseFCMError(t *testing.T) {
	const fcmDetails = `"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError"`
	const unregisteredBody = `{"error":{"message":"secret-body-sentinel","details":[{` + fcmDetails + `,"errorCode":"UNREGISTERED"}]}}`
	for _, test := range []struct {
		name    string
		body    string
		status  int
		invalid bool
		want    string
	}{
		{"typed unregistered", unregisteredBody, http.StatusNotFound, true, "registration-token-not-registered"},
		{"wrong status", unregisteredBody, http.StatusBadRequest, false, "provider request failed"},
		{"wrong detail type", `{"error":{"details":[{"errorCode":"UNREGISTERED"}]}}`, http.StatusNotFound, false, "provider request failed"},
		{"third-party auth rejection", `{"error":{"status":"UNAUTHENTICATED","details":[{` + fcmDetails + `,"errorCode":"THIRD_PARTY_AUTH_ERROR"}]}}`, http.StatusUnauthorized, false, "provider request failed"},
		{"wrong namespace", `{"error":{"status":"PERMISSION_DENIED","details":[{` + fcmDetails + `,"errorCode":"SENDER_ID_MISMATCH"}]}}`, http.StatusForbidden, false, "provider request failed"},
		{"provider message only", `{"error":{"message":"secret-body-sentinel UNREGISTERED","status":"secret-status-sentinel"}}`, http.StatusNotFound, false, "provider request failed"},
		{"malformed json", `secret-error-sentinel`, http.StatusInternalServerError, false, "provider request failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := parseFCMError([]byte(test.body), test.status)
			if err == nil || err.Error() != test.want || errors.Is(err, model.ErrProviderInvalidToken) != test.invalid {
				t.Fatalf("FCM provider error = %v, want %q with invalid=%t", err, test.want, test.invalid)
			}
		})
	}
}

func TestSendMessageUsesConfiguredRetryLimit(t *testing.T) {
	var attempts atomic.Int32
	client := &Client{
		httpClients: []*http.Client{{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				attempts.Add(1)
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Status:     "500 Internal Server Error",
					Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"temporary"}}`)),
					Header:     make(http.Header),
				}, nil
			}),
		}},
		sem:        make(chan struct{}, 1),
		projectID:  "project",
		maxRetries: 1,
	}

	err := client.sendMessage(context.Background(), FcmMessage{Token: "token"})
	if err == nil {
		t.Fatal("sendMessage error = nil, want retry exhaustion")
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want initial attempt plus one retry", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestSendWithAdmissionRunsAfterCapacityAndCredentialsAndBeforeRetry(t *testing.T) {
	ctx := context.Background()
	attempts := 0
	checks := 0
	sem := make(chan struct{}, 1)
	client := &Client{
		httpClients: []*http.Client{{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			if checks != attempts || req.Header.Get("Authorization") != "Bearer synthetic" || req.GetBody != nil {
				t.Fatalf("request sent outside guarded attempt: checks=%d attempts=%d", checks, attempts)
			}
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Status:     "503 secret-status-sentinel",
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"secret-provider-sentinel"}}`)),
				Header:     make(http.Header),
			}, nil
		})}},
		oauthGate: make(chan struct{}, 1),
		loadToken: func(context.Context) (*oauth2.Token, error) {
			return &oauth2.Token{AccessToken: "synthetic"}, nil
		},
		projectID:  "configured-project",
		sem:        sem,
		maxRetries: 1,
	}
	err := client.SendWithAdmission(ctx, "device-token-sentinel", &model.NotificationPayload{Body: "secret-payload-sentinel"}, func(context.Context) error {
		if len(sem) != 1 {
			t.Fatal("admission ran before capacity was acquired")
		}
		checks++
		if checks == 2 {
			return errors.New("secret-admission-database-sentinel")
		}
		return nil
	})
	if err != model.ErrProviderAdmissionCheck || checks != 2 || attempts != 1 || len(sem) != 0 {
		t.Fatalf("dispatch returned %v, checks=%d attempts=%d capacity=%d", err, checks, attempts, len(sem))
	}
}

func TestFCMRejectedBearerRefresh(t *testing.T) {
	const rejectedBody = `{"error":{"status":"UNAUTHENTICATED","message":"secret-provider-sentinel"}}`
	const permissionDenied = `{"error":{"status":"PERMISSION_DENIED","message":"secret-provider-sentinel"}}`
	for _, tc := range []struct {
		name                        string
		repeat, stale, refreshed    bool
		denied                      bool
		attempts, checks, exchanges int
		want                        error
	}{
		{name: "refresh and re-admit", attempts: 2, checks: 2, exchanges: 2},
		{name: "stale after refresh", stale: true, attempts: 1, checks: 2, exchanges: 2, want: model.ErrProviderAdmission},
		{name: "one refresh per send", repeat: true, attempts: 2, checks: 2, exchanges: 2, want: model.ErrProviderUnavailable},
		{name: "late rejection keeps newer bearer", refreshed: true, attempts: 2, checks: 2, exchanges: 1},
		{name: "permanent IAM rejection", denied: true, attempts: 1, checks: 1, exchanges: 1, want: model.ErrProviderAuthorization},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempts, checks, exchanges := 0, 0, 0
			client := newClient("target-project", 1, 1, 0, nil)
			client.oauthGate = make(chan struct{}, 1)
			client.loadToken = func(context.Context) (*oauth2.Token, error) {
				exchanges++
				return &oauth2.Token{AccessToken: fmt.Sprintf("bearer%d", exchanges)}, nil
			}
			client.httpClients[0].Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				attempts++
				wantBearer := fmt.Sprintf("Bearer bearer%d", attempts)
				if req.Header.Get("Authorization") != wantBearer || checks != attempts {
					t.Fatalf("send %d did not use a fresh, admitted bearer", attempts)
				}
				status, body := http.StatusOK, ""
				switch {
				case tc.denied:
					status, body = http.StatusForbidden, permissionDenied
				case attempts == 1 || tc.repeat:
					status, body = http.StatusUnauthorized, rejectedBody
				}
				if tc.refreshed && attempts == 1 {
					client.oauthToken.Store(&oauth2.Token{AccessToken: "bearer2"})
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			err := client.SendWithAdmission(t.Context(), "registration", &model.NotificationPayload{}, func(context.Context) error {
				checks++
				if exchanges == 0 || len(client.sem) != 1 {
					t.Fatal("admission ran before OAuth credentials or capacity were ready")
				}
				if tc.stale && checks == 2 {
					return model.ErrProviderAdmission
				}
				return nil
			})
			if err != tc.want || attempts != tc.attempts || checks != tc.checks || exchanges != tc.exchanges {
				t.Fatalf("send returned %v, attempts=%d checks=%d exchanges=%d", err, attempts, checks, exchanges)
			}
			if tc.repeat && client.oauthToken.Load() != nil {
				t.Fatal("the second rejected bearer remained cached")
			}
		})
	}
}

func TestFCMTransportAndOAuthErrorsRedactRawText(t *testing.T) {
	admit := func(context.Context) error { return nil }
	secret := errors.New("secret-transport-sentinel https://not-real/api/secret-token-sentinel")
	client := &Client{
		httpClients: []*http.Client{{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, secret
		})}},
		oauthGate: make(chan struct{}, 1),
		loadToken: func(context.Context) (*oauth2.Token, error) {
			return &oauth2.Token{AccessToken: "synthetic"}, nil
		},
		sem:       make(chan struct{}, 1),
		projectID: "configured-project",
	}
	if err := client.SendWithAdmission(t.Context(), "device-token-sentinel", &model.NotificationPayload{}, admit); err != model.ErrProviderOutcomeUnknown {
		t.Fatalf("transport error = %v, want safe unknown outcome", err)
	}
	client.oauthToken.Store(nil)
	client.loadToken = func(context.Context) (*oauth2.Token, error) {
		return nil, &oauth2.RetrieveError{
			Response:  &http.Response{StatusCode: http.StatusBadRequest},
			Body:      []byte("oauth-body-sentinel"),
			ErrorCode: "invalid_grant",
		}
	}
	if err := client.SendWithAdmission(t.Context(), "device-token-sentinel", &model.NotificationPayload{}, admit); err != model.ErrProviderUnavailable {
		t.Fatalf("OAuth error = %v, want provider unavailable", err)
	}
	client.loadToken = func(context.Context) (*oauth2.Token, error) {
		return nil, fmt.Errorf("oauth-secret-sentinel: %w", context.DeadlineExceeded)
	}
	admit = func(context.Context) error {
		t.Fatal("admission ran after OAuth timeout")
		return nil
	}
	if err := client.SendWithAdmission(t.Context(), "device-token-sentinel", &model.NotificationPayload{}, admit); err != model.ErrProviderUnavailable {
		t.Fatalf("OAuth timeout = %v, want provider unavailable", err)
	}
}

func TestFCMLostReplyDoesNotRetryOrLoseUncertainty(t *testing.T) {
	attempts, checks := 0, 0
	client := &Client{
		httpClients: []*http.Client{{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			_, _ = io.Copy(io.Discard, req.Body)
			_ = req.Body.Close()
			return nil, io.ErrUnexpectedEOF
		})}},
		oauthGate: make(chan struct{}, 1),
		loadToken: func(context.Context) (*oauth2.Token, error) {
			return &oauth2.Token{AccessToken: "synthetic"}, nil
		},
		sem:        make(chan struct{}, 1),
		projectID:  "configured-project",
		maxRetries: 2,
	}
	err := client.SendWithAdmission(t.Context(), "device-token-sentinel", &model.NotificationPayload{}, func(context.Context) error {
		checks++
		if checks > 1 {
			return model.ErrProviderAdmission
		}
		return nil
	})
	if err != model.ErrProviderOutcomeUnknown || attempts != 1 || checks != 1 || len(client.sem) != 0 {
		t.Fatalf("lost reply returned %v, attempts=%d checks=%d capacity=%d", err, attempts, checks, len(client.sem))
	}
}

func TestFCMOAuthSigningKeyRejection(t *testing.T) {
	const invalidSignature = `{"error":"invalid_grant","error_description":"Invalid JWT Signature.","extra":"secret-body-sentinel"}`
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		untrusted bool
		want      error
	}{
		{"invalid signature", http.StatusBadRequest, invalidSignature, false, model.ErrProviderAuthorization},
		{"clock skew", http.StatusBadRequest, `{"error":"invalid_grant","error_description":"Invalid JWT: Token must be a short-lived token (60 minutes) and in a reasonable timeframe."}`, false, model.ErrProviderUnavailable},
		{"generic invalid grant", http.StatusBadRequest, `{"error":"invalid_grant"}`, false, model.ErrProviderUnavailable},
		{"other grant error", http.StatusBadRequest, `{"error":"invalid_client","error_description":"Invalid JWT Signature."}`, false, model.ErrProviderUnavailable},
		{"other error description", http.StatusBadRequest, `{"error":"invalid_grant","error_description":"Invalid JWT Signature. Extra text."}`, false, model.ErrProviderUnavailable},
		{"wrong status", http.StatusUnauthorized, invalidSignature, false, model.ErrProviderUnavailable},
		{"untrusted endpoint", http.StatusBadRequest, invalidSignature, true, model.ErrProviderUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil || r.FormValue("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" || r.FormValue("assertion") == "" {
					t.Error("expected a service-account assertion exchange")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer auth.Close()

			endpoint := googleTokenEndpoint
			if tc.untrusted {
				endpoint = auth.URL
			}
			localURL, err := url.Parse(auth.URL)
			if err != nil {
				t.Fatal(err)
			}
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				localReq := req.Clone(req.Context())
				localReq.URL = localURL
				resp, err := http.DefaultTransport.RoundTrip(localReq)
				if err == nil {
					resp.Request = req
				}
				return resp, err
			})
			raw := oauthTestServiceAccount(t, endpoint)
			client, err := newClientForProject(t.Context(), raw, "routed-project", 1, 1, 0, transport)
			if err != nil {
				t.Fatal(err)
			}
			client.httpClients[0].Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("FCM send ran after OAuth exchange failed")
				return nil, nil
			})
			err = client.SendWithAdmission(t.Context(), "device-token", &model.NotificationPayload{}, func(context.Context) error {
				t.Fatal("admission ran after OAuth exchange failed")
				return nil
			})
			if err != tc.want {
				t.Fatalf("OAuth exchange returned %v, want %v", err, tc.want)
			}
		})
	}
}

func oauthTestServiceAccount(t *testing.T, endpoint string) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]string{
		"type":         "service_account",
		"project_id":   "synthetic-project",
		"client_email": "test@synthetic-project.iam.gserviceaccount.com",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})),
		"token_uri":    endpoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestFCMOAuthRefreshAndWaitingSendersCanBeCanceled(t *testing.T) {
	var exchanges atomic.Int32
	refreshing := make(chan struct{})
	refreshCanceled := make(chan struct{})
	release := make(chan struct{})
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if exchanges.Add(1) == 1 {
			close(refreshing)
			select {
			case <-r.Context().Done():
				close(refreshCanceled)
			case <-release:
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"synthetic","token_type":"Bearer","expires_in":3600}`)
	}))
	defer auth.Close()
	defer close(release)

	raw := oauthTestServiceAccount(t, auth.URL)
	client, err := NewClientForProject(t.Context(), raw, "routed-project", 1, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	var deliveries atomic.Int32
	client.httpClients[0].Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		deliveries.Add(1)
		if req.URL.Path != "/v1/projects/routed-project/messages:send" {
			t.Error("FCM did not use the route project ID")
		}
		if req.Header.Get("Authorization") != "Bearer synthetic" {
			t.Error("FCM delivery used an unexpected OAuth token")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	})
	ctx, timeout := context.WithTimeout(t.Context(), 5*time.Second)
	defer timeout()

	send := func(ctx context.Context) error {
		return client.SendWithAdmission(ctx, "device-token-sentinel", &model.NotificationPayload{}, func(context.Context) error { return nil })
	}
	sendAsync := func() (context.CancelFunc, <-chan error) {
		sendCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- send(sendCtx) }()
		return cancel, done
	}
	waitCanceled := func(done <-chan error) {
		t.Helper()
		select {
		case err := <-done:
			if !errors.Is(err, model.ErrProviderUnavailable) {
				t.Fatalf("send returned %v, want provider unavailable", err)
			}
		case <-ctx.Done():
			t.Fatal("canceled send did not return")
		}
	}

	cancelRefresh, first := sendAsync()
	defer cancelRefresh()
	select {
	case <-refreshing:
	case <-ctx.Done():
		t.Fatal("FCM OAuth exchange did not start")
	}

	cancelWait, second := sendAsync()
	defer cancelWait()
	for len(client.sem) != 2 {
		select {
		case <-ctx.Done():
			t.Fatal("second sender did not acquire capacity before waiting for OAuth")
		case <-time.After(time.Millisecond):
		}
	}
	cancelWait()
	waitCanceled(second)
	if len(client.sem) != 1 || exchanges.Load() != 1 {
		t.Fatalf("waiting sender leaked capacity or started another exchange: capacity=%d exchanges=%d", len(client.sem), exchanges.Load())
	}

	cancelRefresh()
	waitCanceled(first)
	select {
	case <-refreshCanceled:
	case <-ctx.Done():
		t.Fatal("FCM OAuth exchange did not observe request cancellation")
	}
	if len(client.sem) != 0 || deliveries.Load() != 0 {
		t.Fatalf("canceled attempts left capacity=%d or delivered=%d", len(client.sem), deliveries.Load())
	}
	for range 2 {
		if err := send(ctx); err != nil {
			t.Fatalf("send after OAuth cancellation: %v", err)
		}
	}
	if exchanges.Load() != 2 || deliveries.Load() != 2 {
		t.Fatalf("expected one successful cached refresh and two deliveries, exchanges=%d deliveries=%d", exchanges.Load(), deliveries.Load())
	}
}
