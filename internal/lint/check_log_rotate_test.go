package lint_test

import (
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/lint"
)

// TestLogRotateDirty isolates log-rotate's one row on spec/fixtures/dirty.
// dirty/log.md is 507 file lines but 505 entries (S1 correction C-9); the
// message must say 505, proving the check counts "- "-prefixed lines, not
// file lines.
func TestLogRotateDirty(t *testing.T) {
	ctx := openFixtureContext(t, "dirty")
	report := lint.Run(ctx, []string{"log-rotate"})

	if len(report.Findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(report.Findings), report.Findings)
	}
	f := report.Findings[0]
	if f.Path != "log.md" {
		t.Errorf("Path = %q, want log.md", f.Path)
	}
	want := "505 entries exceeds the 500-entry rotation threshold; rotate to log-2026.md"
	if f.Message != want {
		t.Errorf("Message = %q, want %q", f.Message, want)
	}
}

// logWithEntries builds a log.md with n entries, all dated 2026, plus one
// blank line and one non-entry heading line that must not be counted.
func logWithEntries(n int) string {
	var b strings.Builder
	b.WriteString("# Log\n\n")
	for i := 0; i < n; i++ {
		b.WriteString("- 2026-01-01T00:00:00Z create_page wiki/concepts/x.md\n")
	}
	return b.String()
}

// TestLogRotateBoundary proves the threshold is strictly greater than 500
// entries: exactly 500 must not fire, 501 must.
func TestLogRotateBoundary(t *testing.T) {
	tests := []struct {
		name    string
		entries int
		want    int
	}{
		{"exactly 500 entries does not fire", 500, 0},
		{"501 entries fires", 501, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := buildVault(t, map[string]string{
				"log.md": logWithEntries(tc.entries),
			})
			report := lint.Run(ctx, []string{"log-rotate"})
			if len(report.Findings) != tc.want {
				t.Fatalf("got %d findings, want %d: %+v", len(report.Findings), tc.want, report.Findings)
			}
		})
	}
}
