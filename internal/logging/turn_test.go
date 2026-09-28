package logging

import (
	"context"
	"testing"
)

func TestTurnRoundTrip(t *testing.T) {
	if got := TurnFrom(context.Background()); got != "" {
		t.Fatalf("TurnFrom(empty) = %q", got)
	}
	if got := TurnFrom(WithTurn(context.Background(), "20260928T101502Z-3f9a")); got != "20260928T101502Z-3f9a" {
		t.Fatalf("TurnFrom = %q", got)
	}
}
