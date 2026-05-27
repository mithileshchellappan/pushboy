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
	outcomes := []model.SendOutcome{
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
	}

	deltas, invalidTokenIDs := summarizeLAOutcomes(outcomes)

	if got := deltas["dispatch-1"]; got.success != 1 || got.failure != 2 {
		t.Fatalf("dispatch-1 delta = %+v, want 1 success / 2 failure", got)
	}
	if got := deltas["dispatch-2"]; got.success != 0 || got.failure != 1 {
		t.Fatalf("dispatch-2 delta = %+v, want 0 success / 1 failure", got)
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
}
