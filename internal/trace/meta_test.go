package trace

import (
	"context"
	"regexp"
	"testing"
	"time"
)

// TestNewIDShape pins 038's turn-id format: UTC second, a dash, 4 hex.
func TestNewIDShape(t *testing.T) {
	at := time.Date(2026, 9, 28, 18, 15, 2, 0, time.FixedZone("HKT", 8*3600))
	id := NewID(at)
	if !regexp.MustCompile(`^20260928T101502Z-[0-9a-f]{4}$`).MatchString(id) {
		t.Fatalf("NewID = %q, want 20260928T101502Z-xxxx", id)
	}
}

func TestVerbRoundTrip(t *testing.T) {
	if got := VerbFrom(context.Background()); got != "" {
		t.Fatalf("VerbFrom(empty) = %q", got)
	}
	if got := VerbFrom(WithVerb(context.Background(), "query")); got != "query" {
		t.Fatalf("VerbFrom = %q, want query", got)
	}
}
