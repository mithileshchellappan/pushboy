package apns

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/mithileshchellappan/pushboy/internal/model"
)

func TestSendWithRetryClassifiesNonRetryableResponses(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		body         string
		wantContains string
	}{
		{
			name:         "bad device token reason",
			status:       http.StatusBadRequest,
			body:         `{"reason":"BadDeviceToken"}`,
			wantContains: "BadDeviceToken",
		},
		{
			name:         "unregistered reason",
			status:       http.StatusGone,
			body:         `{"reason":"Unregistered"}`,
			wantContains: "Unregistered",
		},
		{
			name:         "generic malformed body",
			status:       http.StatusBadRequest,
			body:         `{`,
			wantContains: "failed to send notification: 400 Bad Request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			client := &Client{httpClients: []*http.Client{server.Client()}}
			err := client.sendWithRetry(context.Background(), server.URL, []byte(`{}`), "jwt", map[string]string{"apns-topic": "bundle"})
			if err == nil {
				t.Fatalf("sendWithRetry error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantContains) {
				t.Fatalf("sendWithRetry error = %q, want containing %q", err, tt.wantContains)
			}
			if tt.name == "bad device token reason" {
				var providerErr interface{ ProviderReason() string }
				if !errors.As(err, &providerErr) {
					t.Fatalf("sendWithRetry error type = %T, want provider reason", err)
				}
				if got := providerErr.ProviderReason(); got != "BadDeviceToken" {
					t.Fatalf("provider reason = %q, want BadDeviceToken", got)
				}
			}
		})
	}
}

func TestChannelCreationFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		status                   int
		body                     string
		permanentAuth, safeRetry bool
	}{
		{http.StatusForbidden, `{"reason":"InvalidProviderToken"}`, true, true},
		{http.StatusForbidden, `{"reason":"ExpiredProviderToken"}`, false, true},
		{http.StatusForbidden, `{"reason":"ChannelNotRegistered"}`, false, false},
		{http.StatusForbidden, `{"reason":"unrecognized"}`, false, false},
		{http.StatusInternalServerError, `{"reason":"InvalidProviderToken"}`, false, false},
		{http.StatusForbidden, `{"reason":"TopicMismatch"}`, true, true},
		{http.StatusBadRequest, `{"reason":"TopicMismatch"}`, false, false},
		{http.StatusBadRequest, `{"reason":"CannotCreateChannelConfig"}`, false, true},
		{http.StatusBadRequest, `{"reason":"FeatureNotEnabled"}`, false, true},
		{http.StatusBadRequest, `{"reason":"BadRequestParams"}`, false, true},
		{http.StatusBadRequest, `{"reason":"MissingPushType"}`, false, true},
		{http.StatusBadRequest, `{"reason":"UnknownChannelRejection"}`, false, false},
		{http.StatusNotFound, `{"reason":"BadPath"}`, false, true},
		{http.StatusMethodNotAllowed, `{"reason":"MethodNotAllowed"}`, false, true},
		{http.StatusRequestEntityTooLarge, `{"reason":"PayloadTooLarge"}`, false, true},
		{http.StatusTooManyRequests, `{"reason":"TooManyProviderTokenUpdates"}`, false, true},
		{http.StatusServiceUnavailable, `{"reason":"CannotCreateChannelConfig"}`, false, false},
		{http.StatusBadRequest, `{"reason":"BadTopic"}`, false, true},
	} {
		err := newResponseError(tc.status, []byte(tc.body))
		if errors.Is(err, model.ErrProviderAuthorization) != tc.permanentAuth || errors.Is(err, ErrChannelRequestRejected) != tc.safeRetry {
			t.Errorf("[%d %s] auth=%t, safe retry=%t", tc.status, tc.body,
				errors.Is(err, model.ErrProviderAuthorization), errors.Is(err, ErrChannelRequestRejected))
		}
	}
}

func TestSendWithRetrySuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &Client{httpClients: []*http.Client{server.Client()}}
	if err := client.sendWithRetry(context.Background(), server.URL, []byte(`{}`), "jwt", nil); err != nil {
		t.Fatalf("sendWithRetry error = %v", err)
	}
}

func TestSendWithRetryUsesConfiguredRetryLimit(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	client := &Client{
		httpClients: []*http.Client{server.Client()},
		maxRetries:  1,
	}
	err := client.sendWithRetry(context.Background(), server.URL, []byte(`{}`), "jwt", nil)
	if err == nil || !strings.Contains(err.Error(), "after 1 retries") {
		t.Fatalf("sendWithRetry error = %v, want configured retry exhaustion", err)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want initial attempt plus one retry", got)
	}
}

func TestSendWithRetryDoesNotRetryServerErrors(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := &Client{
		httpClients: []*http.Client{server.Client()},
		maxRetries:  3,
	}
	err := client.sendWithRetry(context.Background(), server.URL, []byte(`{}`), "jwt", nil)
	if err == nil || !strings.Contains(err.Error(), "500 Internal Server Error") {
		t.Fatalf("sendWithRetry error = %v, want original server error", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want no server-error retry", got)
	}
}

func TestSendWithRetryRespectsMaxConcurrent(t *testing.T) {
	const maxConcurrent = 3
	const totalRequests = 12

	var inFlight, peak atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-release
		inFlight.Add(-1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &Client{
		httpClients: []*http.Client{server.Client()},
		sem:         make(chan struct{}, maxConcurrent),
	}

	var wg sync.WaitGroup
	errs := make(chan error, totalRequests)
	for range totalRequests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- client.sendWithRetry(context.Background(), server.URL, []byte(`{}`), "jwt", nil)
		}()
	}

	// Let requests pile up against the semaphore, then release the server.
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("sendWithRetry error = %v", err)
		}
	}
	if got := peak.Load(); got > maxConcurrent {
		t.Fatalf("peak in-flight requests = %d, want <= %d", got, maxConcurrent)
	}
}

func TestNewClientPoolAndConcurrencyDefaults(t *testing.T) {
	client := NewClient([]byte("key"), "kid", "team", "bundle", false, "", 8, 0, 2)
	if got := len(client.httpClients); got != 8 {
		t.Fatalf("pool size = %d, want 8", got)
	}
	if got := cap(client.sem); got != 8*900 {
		t.Fatalf("max concurrent = %d, want %d", got, 8*900)
	}
	if got := client.maxRetries; got != 2 {
		t.Fatalf("max retries = %d, want 2", got)
	}

	client = NewClient([]byte("key"), "kid", "team", "bundle", false, "", 0, 50, 0)
	if got := len(client.httpClients); got != 1 {
		t.Fatalf("pool size = %d, want 1 when configured below minimum", got)
	}
	if got := cap(client.sem); got != 50 {
		t.Fatalf("max concurrent = %d, want 50", got)
	}
}

type apnsTestRoundTripper func(*http.Request) (*http.Response, error)

func (f apnsTestRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestAPNsAdmissionGuardsEachNormalPushAttempt(t *testing.T) {
	client := NewClient(nil, "kid", "team", "com.example.app", false, "https://apns.example", 1, 1, 1)

	attempts, checks := 0, 0
	client.httpClients[0].Transport = apnsTestRoundTripper(func(req *http.Request) (*http.Response, error) {
		attempts++
		if attempts != checks || req.URL.Path != "/3/device/device-token-sentinel" || req.GetBody != nil {
			t.Errorf("attempt %d used an unguarded request (checks %d)", attempts, checks)
		}
		if req.Header.Get("Apns-Topic") != "com.example.app" || req.Header.Get("Authorization") != "Bearer synthetic" {
			t.Error("APNs request did not use the route topic and credentials")
		}
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Retry-After": []string{"0"}},
			Body:       io.NopCloser(strings.NewReader(`{"reason":"provider-secret-sentinel"}`)),
		}, nil
	})
	admit := func(context.Context) error {
		if len(client.sem) != 1 || client.cachedJWT == "" {
			t.Error("admission ran before capacity and credentials")
		}
		checks++
		if checks == 2 {
			return fmt.Errorf("database-secret-sentinel: %w", model.ErrProviderAdmission)
		}
		return nil
	}
	client.cachedJWT = "synthetic"
	client.jwtExpiry = time.Now().Add(time.Hour)
	err := client.SendWithAdmission(t.Context(), "device-token-sentinel", &model.NotificationPayload{}, admit)
	if err != model.ErrProviderAdmission || attempts != 1 || checks != 2 || len(client.sem) != 0 {
		t.Fatalf("send returned %v, attempts=%d checks=%d capacity=%d", err, attempts, checks, len(client.sem))
	}
}

func TestAPNsCanceledAdmissionIsProviderUnavailable(t *testing.T) {
	client := NewClient(nil, "kid", "team", "com.example.app", false, "https://apns.example", 1, 1, 1)
	client.cachedJWT = "synthetic"
	client.jwtExpiry = time.Now().Add(time.Hour)
	client.httpClients[0].Transport = apnsTestRoundTripper(func(*http.Request) (*http.Response, error) {
		t.Fatal("transport ran after admission was canceled")
		return nil, nil
	})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := client.SendWithAdmission(ctx, "token", &model.NotificationPayload{}, func(context.Context) error {
		cancel()
		return nil
	})
	if err != model.ErrProviderUnavailable {
		t.Fatalf("send returned %v, want provider unavailable", err)
	}
}

func TestAPNsInvalidationRequiresRecognizedStatusAndReason(t *testing.T) {
	for _, test := range []struct {
		status  int
		body    string
		invalid bool
	}{
		{400, `{"reason":"BadDeviceToken"}`, true},
		{410, `{"reason":"Unregistered"}`, true},
		{400, `{"reason":"DeviceTokenNotForTopic"}`, false},
		{400, `{"reason":"provider-secret-sentinel BadDeviceToken"}`, false},
		{400, `{"reason":"Unregistered"}`, false},
		{410, `{"reason":"BadDeviceToken"}`, false},
		{400, `{"reason":"ChannelNotRegistered"}`, false},
	} {
		err := newResponseError(test.status, []byte(test.body))
		if errors.Is(model.SafeProviderError(err), model.ErrProviderInvalidToken) != test.invalid {
			t.Errorf("%d %s returned %v, want invalid=%t", test.status, test.body, err, test.invalid)
		}
		if strings.Contains(err.Error(), "provider-secret-sentinel") {
			t.Errorf("APNs leaked its response body in %v", err)
		}
	}
}

func TestAPNsLostReplyOrDialRefusalPreserveDefinitiveOutcome(t *testing.T) {
	for _, test := range []struct {
		name          string
		connecting    bool
		gotConnection bool
		transportErr  error
		want          error
		wantChecks    int
	}{
		{"lost reply", true, true, io.ErrUnexpectedEOF, model.ErrProviderOutcomeUnknown, 1},
		{"dial refusal after transport reused connection", true, true, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, model.ErrProviderOutcomeUnknown, 1},
		{"timeout after connecting", true, true, context.DeadlineExceeded, model.ErrProviderOutcomeUnknown, 1},
		{"timeout before connecting", false, false, context.DeadlineExceeded, model.ErrProviderUnavailable, 1},
		{"TLS failure before connecting", true, false, io.ErrUnexpectedEOF, model.ErrProviderUnavailable, 1},
		{"first dial refused", true, false, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, model.ErrProviderAdmission, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			attempts, checks := 0, 0
			client := &Client{maxRetries: 1, httpClients: []*http.Client{{
				Transport: apnsTestRoundTripper(func(req *http.Request) (*http.Response, error) {
					attempts++
					trace := httptrace.ContextClientTrace(req.Context())
					if test.connecting {
						trace.GetConn("apns.example:443")
					}
					if test.gotConnection {
						trace.GotConn(httptrace.GotConnInfo{})
					}
					return nil, test.transportErr
				}),
			}}}
			client.cachedJWT = "synthetic"
			client.jwtExpiry = time.Now().Add(time.Hour)
			err := client.sendWithRetryAdmission(t.Context(), "https://apns.example", []byte("{}"), "synthetic", nil, func(context.Context) error {
				checks++
				if checks > 1 {
					return model.ErrProviderAdmission
				}
				return nil
			})
			if err != test.want || attempts != 1 || checks != test.wantChecks {
				t.Fatalf("send returned %v, attempts=%d checks=%d; want %v and %d checks", err, attempts, checks, test.want, test.wantChecks)
			}
		})
	}
}
