package apns

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mithileshchellappan/pushboy/internal/model"
)

func TestCreateLiveActivityChannelUsesNoStorage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/1/apps/com.example.app/channels" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["message-storage-policy"] != float64(0) {
			t.Fatalf("message-storage-policy = %#v, want 0", body["message-storage-policy"])
		}
		if body["push-type"] != "LiveActivity" {
			t.Fatalf("push-type = %#v, want LiveActivity", body["push-type"])
		}
		w.Header().Set("apns-channel-id", "channel-1")
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := testChannelClient(server)
	channelID, err := client.CreateLiveActivityChannel(context.Background())
	if err != nil {
		t.Fatalf("CreateLiveActivityChannel error = %v", err)
	}
	if channelID != "channel-1" {
		t.Fatalf("channelID = %q, want channel-1", channelID)
	}
}

func TestCreateLiveActivityChannelIsOneShot(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := testChannelClient(server)
	client.maxRetries = 3
	if _, err := client.CreateLiveActivityChannel(context.Background()); err == nil {
		t.Fatal("CreateLiveActivityChannel error = nil, want APNs failure")
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want one-shot channel creation", attempts.Load())
	}
}

func TestSendLiveActivityBroadcastUsesExistingProviderPath(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/4/broadcasts/apps/com.example.app" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("apns-channel-id"); got != "channel-1" {
			t.Fatalf("apns-channel-id = %q, want channel-1", got)
		}
		if got := r.Header.Get("apns-expiration"); got != "0" {
			t.Fatalf("apns-expiration = %q, want 0", got)
		}
		if got := r.Header.Get("apns-topic"); got != "" {
			t.Fatalf("apns-topic = %q, want omitted", got)
		}
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := testChannelClient(server)
	err := client.SendLiveActivityBroadcast(context.Background(), "channel-1", &model.LiveActivityRequest{
		Action:       model.LiveActivityActionUpdate,
		ActivityID:   "activity-1",
		ActivityType: "RaceAttributes",
		Payload:      json.RawMessage(`{"lap":42}`),
		CreatedAt:    time.Unix(1_700_000_000, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("SendLiveActivityBroadcast error = %v", err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want existing APNs retry path", attempts.Load())
	}
}

func TestDeleteLiveActivityChannelTreatsMissingChannelAsDeleted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Fatalf("method = %s, want DELETE", r.Method)
		}
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte(`{"reason":"ChannelNotRegistered"}`))
	}))
	defer server.Close()

	client := testChannelClient(server)
	if err := client.DeleteLiveActivityChannel(context.Background(), "channel-1"); err != nil {
		t.Fatalf("DeleteLiveActivityChannel error = %v", err)
	}
}

func TestBuildLAStartAddsOnlyTheSelectedInput(t *testing.T) {
	client := &Client{bundleID: "com.example.app"}
	options := model.ParsedLiveActivityOptions{
		Alert:          &model.LiveActivityAlert{Title: "Race started"},
		AttributesType: "RaceAttributes",
		Attributes:     json.RawMessage(`{"raceId":"race-1"}`),
	}
	tests := []struct {
		name    string
		request model.LiveActivityRequest
		key     string
		want    any
	}{
		{
			name: "channel",
			request: model.LiveActivityRequest{
				Action:           model.LiveActivityActionStart,
				InputPushChannel: " channel-1 ",
			},
			key:  "input-push-channel",
			want: " channel-1 ",
		},
		{
			name: "token",
			request: model.LiveActivityRequest{
				Action:             model.LiveActivityActionStart,
				RequestUpdateToken: true,
			},
			key:  "input-push-token",
			want: float64(1),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body, _, err := client.buildLAMessage(&test.request, options)
			if err != nil {
				t.Fatalf("buildLAMessage error = %v", err)
			}
			var wire struct {
				APS map[string]any `json:"aps"`
			}
			if err := json.Unmarshal(body, &wire); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if got := wire.APS[test.key]; got != test.want {
				t.Fatalf("%s = %#v, want %#v", test.key, got, test.want)
			}
			otherKey := "input-push-token"
			if test.key == otherKey {
				otherKey = "input-push-channel"
			}
			if _, exists := wire.APS[otherKey]; exists {
				t.Fatalf("%s must be omitted", otherKey)
			}
		})
	}

	request := &model.LiveActivityRequest{
		Action:             model.LiveActivityActionStart,
		InputPushChannel:   "channel-1",
		RequestUpdateToken: true,
	}
	if _, _, err := client.buildLAMessage(request, options); err == nil {
		t.Fatal("buildLAMessage accepted both channel and token inputs")
	}
}

func testChannelClient(server *httptest.Server) *Client {
	return &Client{
		httpClients:               []*http.Client{server.Client()},
		bundleID:                  "com.example.app",
		endpoint:                  server.URL,
		channelManagementEndpoint: server.URL,
		cachedJWT:                 "jwt",
		jwtExpiry:                 time.Now().Add(time.Hour),
		maxRetries:                1,
	}
}
