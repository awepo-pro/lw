package lint_test

import (
	"testing"

	"github.com/awepo-pro/lw/internal/lint"
)

// TestCiteSource pins check 18 (034 T2): a raw/ source cited in the body
// but absent from sources: earns exactly one finding per distinct source
// per page, at its first marker's line; a listed source and a wiki/ cite
// never fire.
func TestCiteSource(t *testing.T) {
	t.Run("unlisted source earns one finding", func(t *testing.T) {
		ctx := buildVault(t, map[string]string{
			"wiki/concepts/cited.md": citePageBody(nil,
				"# Cite Page\n\nClaim one.^[raw/papers/plain.md]\n"),
		})

		report := lint.Run(ctx, []string{"cite-source"})
		if len(report.Findings) != 1 {
			t.Fatalf("got %d findings, want 1: %+v", len(report.Findings), report.Findings)
		}
		f := report.Findings[0]
		if f.Check != "cite-source" {
			t.Errorf("Check = %q, want cite-source", f.Check)
		}
		if f.Path != "wiki/concepts/cited.md" {
			t.Errorf("Path = %q, want wiki/concepts/cited.md", f.Path)
		}
		if f.Line != 3 {
			t.Errorf("Line = %d, want 3 (the first marker's body line)", f.Line)
		}
		if f.Severity != lint.SevWarn {
			t.Errorf("Severity = %v, want SevWarn", f.Severity)
		}
		want := "cites raw/papers/plain.md in the body but sources: does not list it; add it to sources:"
		if f.Message != want {
			t.Errorf("Message = %q, want %q", f.Message, want)
		}
	})

	t.Run("listed source stays silent", func(t *testing.T) {
		ctx := buildVault(t, map[string]string{
			"raw/papers/plain.md": rawSourceFixture,
			"wiki/concepts/cited.md": citePageBody([]string{"raw/papers/plain.md"},
				"# Cite Page\n\nClaim one.^[raw/papers/plain.md]\n"),
		})

		report := lint.Run(ctx, []string{"cite-source"})
		if len(report.Findings) != 0 {
			t.Fatalf("got %d findings, want 0: %+v", len(report.Findings), report.Findings)
		}
	})

	t.Run("one source cited three times earns one finding", func(t *testing.T) {
		ctx := buildVault(t, map[string]string{
			"wiki/concepts/cited.md": citePageBody(nil,
				"# Cite Page\n\nOne.^[raw/papers/plain.md]\n\n"+
					"Two.^[raw/papers/plain.md]\n\n"+
					"Three.^[raw/papers/plain.md]\n"),
		})

		report := lint.Run(ctx, []string{"cite-source"})
		if len(report.Findings) != 1 {
			t.Fatalf("got %d findings, want 1: %+v", len(report.Findings), report.Findings)
		}
		if got := report.Findings[0].Line; got != 3 {
			t.Fatalf("Line = %d, want 3 (the first marker's line)", got)
		}
	})

	t.Run("wiki cite is not a provenance source", func(t *testing.T) {
		ctx := buildVault(t, map[string]string{
			"wiki/concepts/cited.md": citePageBody(nil,
				"# Cite Page\n\nSee the other page.^[wiki/concepts/other.md]\n"),
		})

		report := lint.Run(ctx, []string{"cite-source"})
		if len(report.Findings) != 0 {
			t.Fatalf("got %d findings, want 0: %+v", len(report.Findings), report.Findings)
		}
	})
}
