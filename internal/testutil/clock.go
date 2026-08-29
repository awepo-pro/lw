package testutil

import (
	"testing"
	"time"
)

// FixedClock returns a func() time.Time frozen at 2026-08-29T12:00:00Z UTC,
// for injecting into code that takes a `now func() time.Time` field so tests
// see a stable timestamp.
func FixedClock() func() time.Time {
	t := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

// ClockAt parses rfc3339 and returns a func() time.Time frozen at that
// instant, in UTC. It fails t via t.Fatalf if rfc3339 does not parse.
func ClockAt(t *testing.T, rfc3339 string) func() time.Time {
	t.Helper()

	parsed, err := parseRFC3339UTC(rfc3339)
	if err != nil {
		t.Fatalf("testutil: ClockAt(%q): %v", rfc3339, err)
	}
	return func() time.Time { return parsed }
}

// parseRFC3339UTC parses s as RFC 3339 and normalizes it to UTC. Kept
// separate from ClockAt so the error path is directly testable without
// needing to observe a *testing.T failure.
func parseRFC3339UTC(s string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}
