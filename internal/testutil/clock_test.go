package testutil

import (
	"testing"
	"time"
)

func TestFixedClock(t *testing.T) {
	now := FixedClock()

	want := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	if got := now(); !got.Equal(want) {
		t.Fatalf("FixedClock()() = %v, want %v", got, want)
	}
	if loc := now().Location(); loc != time.UTC {
		t.Fatalf("FixedClock()() location = %v, want UTC", loc)
	}

	// Frozen: calling it again returns the same instant, not time.Now().
	first, second := now(), now()
	if !first.Equal(second) {
		t.Fatalf("FixedClock() is not frozen: %v != %v", first, second)
	}
}

func TestParseRFC3339UTC(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    time.Time
		wantErr bool
	}{
		{
			name: "UTC instant",
			in:   "2026-01-02T03:04:05Z",
			want: time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC),
		},
		{
			name: "offset instant normalized to UTC",
			in:   "2026-01-02T03:04:05+02:00",
			want: time.Date(2026, time.January, 2, 1, 4, 5, 0, time.UTC),
		},
		{
			name:    "not RFC 3339",
			in:      "2026-01-02 03:04:05",
			wantErr: true,
		},
		{
			name:    "empty string",
			in:      "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRFC3339UTC(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseRFC3339UTC(%q) = %v, nil; want an error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRFC3339UTC(%q): %v", tt.in, err)
			}
			if !got.Equal(tt.want) {
				t.Fatalf("parseRFC3339UTC(%q) = %v, want %v", tt.in, got, tt.want)
			}
			if loc := got.Location(); loc != time.UTC {
				t.Fatalf("parseRFC3339UTC(%q) location = %v, want UTC", tt.in, loc)
			}
		})
	}
}

func TestClockAt(t *testing.T) {
	tests := []struct {
		name    string
		rfc3339 string
		want    time.Time
	}{
		{
			name:    "UTC instant",
			rfc3339: "2026-01-02T03:04:05Z",
			want:    time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC),
		},
		{
			name:    "offset instant normalized to UTC",
			rfc3339: "2026-01-02T03:04:05+02:00",
			want:    time.Date(2026, time.January, 2, 1, 4, 5, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := ClockAt(t, tt.rfc3339)
			got := now()
			if !got.Equal(tt.want) {
				t.Fatalf("ClockAt(%q)() = %v, want %v", tt.rfc3339, got, tt.want)
			}
			if loc := got.Location(); loc != time.UTC {
				t.Fatalf("ClockAt(%q)() location = %v, want UTC", tt.rfc3339, loc)
			}
		})
	}
}
