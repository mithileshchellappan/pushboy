package model_test

import (
	"testing"

	"github.com/mithileshchellappan/pushboy/internal/model"
)

func TestParsePlatform(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    model.Platform
		wantErr bool
	}{
		{name: "apns", raw: "apns", want: model.APNS},
		{name: "fcm", raw: "fcm", want: model.FCM},
		{name: "empty", raw: "", wantErr: true},
		{name: "unknown", raw: "web", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := model.ParsePlatform(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParsePlatform(%q) error = nil, want error", tt.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePlatform(%q) error = %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("ParsePlatform(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestParseNotificationJobStatus(t *testing.T) {
	valid := []model.NotificationJobStatus{
		model.NotificationJobStatusQueued,
		model.NotificationJobStatusScheduled,
		model.NotificationJobStatusInProgress,
		model.NotificationJobStatusDispatched,
		model.NotificationJobStatusCompleted,
		model.NotificationJobStatusFailed,
	}

	for _, status := range valid {
		t.Run(string(status), func(t *testing.T) {
			got, err := model.ParseNotificationJobStatus(string(status))
			if err != nil {
				t.Fatalf("ParseNotificationJobStatus(%q) error = %v", status, err)
			}
			if got != status {
				t.Fatalf("ParseNotificationJobStatus(%q) = %q, want %q", status, got, status)
			}
		})
	}

	if _, err := model.ParseNotificationJobStatus("queued"); err == nil {
		t.Fatalf("lowercase status should be rejected")
	}
	if _, err := model.ParseNotificationJobStatus(""); err == nil {
		t.Fatalf("empty status should be rejected by parser")
	}
}
