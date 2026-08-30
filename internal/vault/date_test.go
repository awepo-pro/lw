package vault

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// TestParseDate exercises the strict "2006-01-02" layout, including the
// rejections of loosely-formatted input.
func TestParseDate(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    time.Time
		wantErr bool
	}{
		{
			name: "valid date",
			in:   "2026-08-01",
			want: time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC),
		},
		{name: "missing zero-padding on day", in: "2026-08-1", wantErr: true},
		{name: "missing zero-padding on month", in: "2026-8-01", wantErr: true},
		{name: "has a time component", in: "2026-08-01T00:00:00Z", wantErr: true},
		{name: "not a date at all", in: "not-a-date", wantErr: true},
		{name: "empty string", in: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseDate(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseDate(%q) = %v, nil; want an error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDate(%q): %v", tt.in, err)
			}
			if !got.Time.Equal(tt.want) {
				t.Fatalf("ParseDate(%q) = %v, want %v", tt.in, got.Time, tt.want)
			}
		})
	}
}

// TestDateString proves the zero value renders as "", never "0001-01-01".
func TestDateString(t *testing.T) {
	var zero Date
	if got := zero.String(); got != "" {
		t.Fatalf("zero Date.String() = %q, want \"\"", got)
	}

	d, err := ParseDate("2026-08-01")
	if err != nil {
		t.Fatalf("ParseDate: %v", err)
	}
	if got := d.String(); got != "2026-08-01" {
		t.Fatalf("Date.String() = %q, want %q", got, "2026-08-01")
	}
}

// TestDateIsZero proves IsZero distinguishes a parsed date from the zero
// value.
func TestDateIsZero(t *testing.T) {
	var zero Date
	if !zero.IsZero() {
		t.Fatalf("zero Date.IsZero() = false, want true")
	}

	d, err := ParseDate("2026-08-01")
	if err != nil {
		t.Fatalf("ParseDate: %v", err)
	}
	if d.IsZero() {
		t.Fatalf("parsed Date.IsZero() = true, want false")
	}
}

// TestDateUnmarshalYAML exercises the yaml.Unmarshaler hook directly.
func TestDateUnmarshalYAML(t *testing.T) {
	var doc struct {
		D Date `yaml:"d"`
	}
	if err := yaml.Unmarshal([]byte("d: 2026-08-01\n"), &doc); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if got := doc.D.String(); got != "2026-08-01" {
		t.Fatalf("D.String() = %q, want %q", got, "2026-08-01")
	}

	if err := yaml.Unmarshal([]byte("d: not-a-date\n"), &doc); err == nil {
		t.Fatalf("yaml.Unmarshal(not-a-date) = nil error, want an error")
	}
}

// TestPageTypeValid proves Valid accepts exactly the five defined types.
func TestPageTypeValid(t *testing.T) {
	tests := []struct {
		t    PageType
		want bool
	}{
		{TypeEntity, true},
		{TypeConcept, true},
		{TypeComparison, true},
		{TypeQuery, true},
		{TypeSummary, true},
		{PageType("bogus"), false},
		{PageType(""), false},
	}
	for _, tt := range tests {
		if got := tt.t.Valid(); got != tt.want {
			t.Fatalf("PageType(%q).Valid() = %v, want %v", tt.t, got, tt.want)
		}
	}
}

// TestPageTypeDir proves Dir returns the plural directory for each type.
func TestPageTypeDir(t *testing.T) {
	tests := []struct {
		t    PageType
		want string
	}{
		{TypeEntity, "wiki/entities"},
		{TypeConcept, "wiki/concepts"},
		{TypeComparison, "wiki/comparisons"},
		{TypeQuery, "wiki/queries"},
		{TypeSummary, "wiki/summaries"},
		{PageType("bogus"), ""},
	}
	for _, tt := range tests {
		if got := tt.t.Dir(); got != tt.want {
			t.Fatalf("PageType(%q).Dir() = %q, want %q", tt.t, got, tt.want)
		}
	}
}

// TestConfidenceValid proves Valid accepts exactly the three defined
// levels, rejecting even the empty string (callers decide separately
// whether "unset" is acceptable).
func TestConfidenceValid(t *testing.T) {
	tests := []struct {
		c    Confidence
		want bool
	}{
		{ConfHigh, true},
		{ConfMedium, true},
		{ConfLow, true},
		{Confidence("very-sure"), false},
		{Confidence(""), false},
	}
	for _, tt := range tests {
		if got := tt.c.Valid(); got != tt.want {
			t.Fatalf("Confidence(%q).Valid() = %v, want %v", tt.c, got, tt.want)
		}
	}
}
