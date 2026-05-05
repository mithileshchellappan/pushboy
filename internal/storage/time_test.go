package storage

import (
	"database/sql"
	"testing"
	"time"
)

func TestRequiredTimePreservesSubsecondPrecision(t *testing.T) {
	input := time.Date(2026, 5, 1, 10, 30, 0, 123456789, time.FixedZone("test", 2*60*60))

	got := requiredTime(input)
	want := input.UTC()
	if !got.Equal(want) || got.Nanosecond() != 123456789 {
		t.Fatalf("expected UTC time with nanosecond precision, got %s", got.Format(time.RFC3339Nano))
	}
}

func TestNullableTimeHelpers(t *testing.T) {
	input := time.Date(2026, 5, 1, 10, 30, 0, 123456789, time.UTC)

	got := nullTimePtr(sql.NullTime{Time: input, Valid: true})
	if got == nil || !got.Equal(input) {
		t.Fatalf("expected nullable time pointer %s, got %v", input.Format(time.RFC3339Nano), got)
	}

	if got := nullTimePtr(sql.NullTime{}); got != nil {
		t.Fatalf("expected nil nullable time pointer, got %v", got)
	}
}
