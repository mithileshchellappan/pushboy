package apns

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
		})
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
