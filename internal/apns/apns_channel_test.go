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

func TestSendLiveActivityBroadcastUsesBroadcastHost(t *testing.T) {
	for _, test := range []struct {
		name     string
		sandbox  bool
		override string
		wantHost string
	}{
		{"sandbox", true, "", "api-broadcast.sandbox.push.apple.com"},
		{"production", false, "", "api-broadcast.push.apple.com"},
		{"local override", false, "https://local.example", "local.example"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := NewClient(nil, "kid", "team", "com.example.app", test.sandbox, test.override, 1, 1, 0)
			client.cachedJWT = "jwt"
			client.jwtExpiry = time.Now().Add(time.Hour)
			transport := &broadcastHostTransport{}
			client.httpClients[0].Transport = transport
			err := client.SendLiveActivityBroadcast(context.Background(), "channel-1", &model.LiveActivityRequest{
				Action:       model.LiveActivityActionUpdate,
				ActivityID:   "activity-1",
				ActivityType: "RaceAttributes",
				Payload:      json.RawMessage(`{"lap":42}`),
				CreatedAt:    time.Unix(1_700_000_000, 0).UTC(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if transport.host != test.wantHost || transport.path != "/4/broadcasts/apps/com.example.app" {
				t.Fatalf("broadcast route = %s%s, want %s/4/broadcasts/apps/com.example.app", transport.host, transport.path, test.wantHost)
			}
		})
	}
}

type broadcastHostTransport struct {
	host string
	path string
}

func (t *broadcastHostTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.host, t.path = r.URL.Host, r.URL.Path
	return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: http.NoBody, Header: make(http.Header), Request: r}, nil
}

func TestDeleteLiveActivityChannelNotRegistered(t *testing.T) {
	for _, tc := range []struct {
		status      int
		wantSuccess bool
	}{
		{http.StatusBadRequest, true},
		{http.StatusGone, false},
		{http.StatusInternalServerError, false},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete {
					t.Errorf("method = %s, want DELETE", r.Method)
				}
				if r.URL.Path != "/1/apps/com.example.app/channels" {
					t.Errorf("path = %q", r.URL.Path)
				}
				if got := r.Header.Get("apns-channel-id"); got != "channel-1" {
					t.Errorf("apns-channel-id = %q, want channel-1", got)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"reason":"ChannelNotRegistered"}`))
			}))
			defer server.Close()

			client := testChannelClient(server)
			err := client.DeleteLiveActivityChannel(t.Context(), "channel-1")
			if (err == nil) != tc.wantSuccess {
				t.Fatalf("DeleteLiveActivityChannel error = %v, want success %t", err, tc.wantSuccess)
			}
		})
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
		broadcastEndpoint:         server.URL,
		channelManagementEndpoint: server.URL,
		cachedJWT:                 "jwt",
		jwtExpiry:                 time.Now().Add(time.Hour),
		maxRetries:                1,
	}
}
