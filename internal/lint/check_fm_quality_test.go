package lint_test

import (
	"testing"

	"github.com/awepo-pro/lw/internal/lint"
)

// TestFMQualityDirty isolates fm-quality's one row on spec/fixtures/dirty.
// Every dirty page that cites exactly one source also sets confidence, so
// this also guards against over-firing the "single source, no
// confidence" clause.
func TestFMQualityDirty(t *testing.T) {
	ctx := openFixtureContext(t, "dirty")
	report := lint.Run(ctx, []string{"fm-quality"})

	if len(report.Findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(report.Findings), report.Findings)
	}
	f := report.Findings[0]
	if f.Path != "wiki/concepts/thin-links.md" {
		t.Errorf("Path = %q, want wiki/concepts/thin-links.md", f.Path)
	}
	want := "confidence is low; corroborate with another source or raise the confidence"
	if f.Message != want {
		t.Errorf("Message = %q, want %q", f.Message, want)
	}
}

func pageWithFM(fm string) string {
	return "---\n" + fm + "\n---\n\n# Page\n"
}

// TestFMQualityConditions exercises the three independent triggers plus
// the negative cases that must not fire, so a false positive in any one
// clause is caught here rather than only against the fixed fixtures.
func TestFMQualityConditions(t *testing.T) {
	tests := []struct {
		name string
		fm   string
		want int
	}{
		{
			name: "confidence low fires",
			fm:   "title: T\ncreated: 2026-01-01\nupdated: 2026-01-02\ntype: concept\nconfidence: low",
			want: 1,
		},
		{
			name: "contested true fires",
			fm:   "title: T\ncreated: 2026-01-01\nupdated: 2026-01-02\ntype: concept\ncontested: true\nconfidence: high",
			want: 1,
		},
		{
			name: "single source no confidence fires",
			fm:   "title: T\ncreated: 2026-01-01\nupdated: 2026-01-02\ntype: concept\nsources: [raw/papers/a.md]",
			want: 1,
		},
		{
			name: "single source with confidence does not fire",
			fm:   "title: T\ncreated: 2026-01-01\nupdated: 2026-01-02\ntype: concept\nsources: [raw/papers/a.md]\nconfidence: medium",
			want: 0,
		},
		{
			name: "two sources no confidence does not fire",
			fm:   "title: T\ncreated: 2026-01-01\nupdated: 2026-01-02\ntype: concept\nsources: [raw/papers/a.md, raw/papers/b.md]",
			want: 0,
		},
		{
			name: "high confidence, not contested, no sources does not fire",
			fm:   "title: T\ncreated: 2026-01-01\nupdated: 2026-01-02\ntype: concept\nconfidence: high",
			want: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := buildVault(t, map[string]string{
				"wiki/concepts/page.md": pageWithFM(tc.fm),
			})
			report := lint.Run(ctx, []string{"fm-quality"})
			if len(report.Findings) != tc.want {
				t.Fatalf("got %d findings, want %d: %+v", len(report.Findings), tc.want, report.Findings)
			}
		})
	}
}
