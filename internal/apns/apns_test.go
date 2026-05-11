package apns

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

			client := &Client{httpClient: server.Client()}
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

	client := &Client{httpClient: server.Client()}
	if err := client.sendWithRetry(context.Background(), server.URL, []byte(`{}`), "jwt", nil); err != nil {
		t.Fatalf("sendWithRetry error = %v", err)
	}
}
