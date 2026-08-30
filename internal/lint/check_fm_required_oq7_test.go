package lint_test

import (
	"testing"

	"github.com/awepo-pro/lw/internal/lint"
)

// pageWithDate builds a page whose named date field (created or updated)
// carries value verbatim, unquoted, so a malformed value reaches
// Date.UnmarshalYAML exactly as a human would have typed it.
func pageWithDate(field, value string) string {
	dates := map[string]string{"created": "2026-01-01", "updated": "2026-01-02"}
	dates[field] = value
	return "---\n" +
		"title: Bad Date Page\n" +
		"created: " + dates["created"] + "\n" +
		"updated: " + dates["updated"] + "\n" +
		"type: concept\n" +
		"---\n\n# Bad Date Page\n"
}

// TestFMRequiredNamesBadDateField is OQ-7 (MASTER §9 D-AB): a bad date
// value fails inside Date.UnmarshalYAML, so the whole page lands in
// ParseErrors and fm-required absorbs it. The message must name the
// offending field and value rather than the generic "fix the YAML" —
// derived from the wrapped error text, not hard-coded, so "updated"
// works identically to "created".
func TestFMRequiredNamesBadDateField(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value string
		want  string
	}{
		{
			name:  "bad created date",
			field: "created",
			value: "not-a-date",
			want:  `created "not-a-date" is not a valid YYYY-MM-DD date; fix it`,
		},
		{
			name:  "bad updated date",
			field: "updated",
			value: "13-13-2026",
			want:  `updated "13-13-2026" is not a valid YYYY-MM-DD date; fix it`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := buildVault(t, map[string]string{
				"wiki/concepts/bad-date.md": pageWithDate(tc.field, tc.value),
			})
			report := lint.Run(ctx, []string{"fm-required"})
			if len(report.Findings) != 1 {
				t.Fatalf("got %d findings, want 1: %+v", len(report.Findings), report.Findings)
			}
			f := report.Findings[0]
			if f.Path != "wiki/concepts/bad-date.md" {
				t.Errorf("Path = %q, want wiki/concepts/bad-date.md", f.Path)
			}
			if f.Message != tc.want {
				t.Errorf("Message = %q, want %q", f.Message, tc.want)
			}
		})
	}
}

// TestFMRequiredMalformedMessageUnchanged proves malformed.md's message —
// a page whose frontmatter block never closes at all, a distinct failure
// mode from a bad date value — stays byte-identical to
// EXPECTED-LINT.md's golden row.
func TestFMRequiredMalformedMessageUnchanged(t *testing.T) {
	ctx := openFixtureContext(t, "dirty")
	report := lint.Run(ctx, []string{"fm-required"})

	var found bool
	for _, f := range report.Findings {
		if f.Path != "wiki/concepts/malformed.md" {
			continue
		}
		found = true
		want := "frontmatter block never closes; add the closing --- delimiter or fix the YAML"
		if f.Message != want {
			t.Errorf("Message = %q, want %q", f.Message, want)
		}
	}
	if !found {
		t.Fatalf("no fm-required finding for wiki/concepts/malformed.md in: %+v", report.Findings)
	}
}
