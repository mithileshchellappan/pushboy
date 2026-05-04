package storage

import (
	"testing"
	"time"
)

func TestFormatPostgresTimePreservesSubsecondPrecision(t *testing.T) {
	input := time.Date(2026, 5, 1, 10, 30, 0, 123456789, time.UTC)

	got := formatPostgresTime(input)
	if got != "2026-05-01T10:30:00.123456789Z" {
		t.Fatalf("expected nanosecond precision, got %q", got)
	}

	parsed, err := time.Parse(time.RFC3339Nano, got)
	if err != nil {
		t.Fatalf("parse formatted timestamp: %v", err)
	}
	if !parsed.Equal(input) {
		t.Fatalf("expected parsed time %s to equal input %s", parsed, input)
	}
}
