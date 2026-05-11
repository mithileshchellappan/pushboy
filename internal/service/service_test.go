package service

import (
	"strings"
	"testing"
	"time"
)

func TestParseScheduledAt(t *testing.T) {
	futureUTC := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	futureOffset := time.Date(2035, 1, 2, 3, 4, 5, 0, time.FixedZone("IST", 5*60*60+30*60))

	tests := []struct {
		name        string
		raw         string
		wantNil     bool
		wantUTC     *time.Time
		errContains string
	}{
		{name: "empty", raw: "", wantNil: true},
		{name: "future utc", raw: futureUTC.Format(time.RFC3339), wantUTC: &futureUTC},
		{name: "future offset converts to utc", raw: futureOffset.Format(time.RFC3339), wantUTC: ptrTime(futureOffset.UTC())},
		{name: "invalid format", raw: "tomorrow", errContains: "invalid scheduledAt format"},
		{name: "past", raw: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), errContains: "scheduledAt must be in the future"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseScheduledAt(tt.raw)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("parseScheduledAt(%q) error = %v, want containing %q", tt.raw, err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseScheduledAt(%q) error = %v", tt.raw, err)
			}
			if tt.wantNil {
				if got != nil {
					t.Fatalf("parseScheduledAt(%q) = %v, want nil", tt.raw, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("parseScheduledAt(%q) = nil, want %v", tt.raw, tt.wantUTC)
			}
			if !got.Equal(*tt.wantUTC) {
				t.Fatalf("parseScheduledAt(%q) = %v, want %v", tt.raw, got, tt.wantUTC)
			}
			if got.Location() != time.UTC {
				t.Fatalf("parseScheduledAt(%q) location = %v, want UTC", tt.raw, got.Location())
			}
		})
	}
}

func ptrTime(value time.Time) *time.Time {
	return &value
}
