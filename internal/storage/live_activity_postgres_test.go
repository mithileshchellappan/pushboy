package storage

import (
	"testing"

	"github.com/mithileshchellappan/pushboy/internal/model"
)

func TestIsLAInvalidToken(t *testing.T) {
	tests := []struct {
		reason string
		want   bool
	}{
		{reason: "APNs error: 400 Bad Request (reason: BadDeviceToken)", want: true},
		{reason: "APNs error: 410 Gone (reason: Unregistered)", want: true},
		{reason: "APNs error: 400 Bad Request (reason: ExpiredToken)", want: true},
		{reason: "FCM error: registration-token-not-registered", want: true},
		{reason: "FCM error: backend unavailable", want: false},
		{reason: "rate limited after 3 retries: 429 Too Many Requests", want: false},
		{reason: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			if got := isLAInvalidToken(tt.reason); got != tt.want {
				t.Fatalf("isLAInvalidToken(%q) = %v, want %v", tt.reason, got, tt.want)
			}
		})
	}
}

func TestSummarizeLAOutcomes(t *testing.T) {
	outcomes := []model.LASendOutcome{
		{
			Receipt: model.DeliveryReceipt{
				JobID:   "dispatch-1",
				TokenID: "token-1",
				Status:  model.DeliveryStatusSuccess,
			},
		},
		{
			Receipt: model.DeliveryReceipt{
				JobID:        "dispatch-1",
				TokenID:      "token-2",
				Status:       model.DeliveryStatusFailed,
				StatusReason: "APNs error: 400 Bad Request (reason: BadDeviceToken)",
			},
		},
		{
			Receipt: model.DeliveryReceipt{
				JobID:        "dispatch-1",
				TokenID:      "token-3",
				Status:       model.DeliveryStatusFailed,
				StatusReason: "FCM error: backend unavailable",
			},
		},
		{
			Receipt: model.DeliveryReceipt{
				JobID:        "dispatch-2",
				TokenID:      "token-4",
				Status:       model.DeliveryStatusFailed,
				StatusReason: "FCM error: registration-token-not-registered",
			},
		},
		{
			Task: model.LASendTask{
				ChannelID: "channel-1",
				LAJob: &model.LAJobItem{
					ActivityID: "activity-1",
				},
			},
			Receipt: model.DeliveryReceipt{
				JobID:        "dispatch-2",
				TokenID:      "not-a-token",
				Status:       model.DeliveryStatusFailed,
				StatusReason: "APNs error: 410 Gone (reason: Unregistered)",
			},
		},
		{
			Task: model.LASendTask{
				ChannelID: "channel-stale",
				LAJob: &model.LAJobItem{
					ActivityID: "activity-stale",
				},
			},
			Receipt: model.DeliveryReceipt{
				JobID:  "dispatch-3",
				Status: model.DeliveryStatusFailed,
			},
			ProviderReason: "ChannelNotRegistered",
		},
		{
			Task: model.LASendTask{
				ChannelID: "channel-similar-reason",
				LAJob: &model.LAJobItem{
					ActivityID: "activity-similar-reason",
				},
			},
			Receipt: model.DeliveryReceipt{
				JobID:  "dispatch-3",
				Status: model.DeliveryStatusFailed,
			},
			ProviderReason: "ChannelNotRegisteredLater",
		},
		{
			Task: model.LASendTask{
				ChannelID: "channel-success",
				LAJob: &model.LAJobItem{
					ActivityID: "activity-success",
				},
			},
			Receipt: model.DeliveryReceipt{
				JobID:  "dispatch-3",
				Status: model.DeliveryStatusSuccess,
			},
			ProviderReason: "ChannelNotRegistered",
		},
	}

	deltas, invalidTokenIDs, staleChannels := summarizeLAOutcomes(outcomes)

	if got := deltas["dispatch-1"]; got.success != 1 || got.failure != 2 {
		t.Fatalf("dispatch-1 delta = %+v, want 1 success / 2 failure", got)
	}
	if got := deltas["dispatch-2"]; got.success != 0 || got.failure != 2 {
		t.Fatalf("dispatch-2 delta = %+v, want 0 success / 2 failures", got)
	}
	if got := deltas["dispatch-3"]; got.success != 1 || got.failure != 2 {
		t.Fatalf("dispatch-3 delta = %+v, want 1 success / 2 failures", got)
	}
	if _, ok := invalidTokenIDs["token-2"]; !ok {
		t.Fatalf("token-2 should be marked invalid")
	}
	if _, ok := invalidTokenIDs["token-4"]; !ok {
		t.Fatalf("token-4 should be marked invalid")
	}
	if _, ok := invalidTokenIDs["token-3"]; ok {
		t.Fatalf("token-3 generic failure should not be marked invalid")
	}
	if _, ok := invalidTokenIDs["not-a-token"]; ok {
		t.Fatalf("channel failure must not invalidate a token")
	}
	if len(staleChannels) != 1 {
		t.Fatalf("stale channel count = %d, want 1", len(staleChannels))
	}
	if _, ok := staleChannels[laChannelMapping{
		activityID: "activity-stale",
		channelID:  "channel-stale",
	}]; !ok {
		t.Fatal("exact ChannelNotRegistered failure should retire its matching mapping")
	}
}
