package lint_test

import (
	"testing"

	"github.com/awepo-pro/lw/internal/lint"
)

// TestIndexSyncDirty isolates index-sync's two rows on
// spec/fixtures/dirty: a page missing from index.md (Line 0) and an
// index.md entry that resolves nowhere (Line 14, the wikilink's real line
// in index.md as read — S1 correction C-6).
func TestIndexSyncDirty(t *testing.T) {
	ctx := openFixtureContext(t, "dirty")
	report := lint.Run(ctx, []string{"index-sync"})

	want := []lint.Finding{
		{
			Check: "index-sync", Path: "index.md", Line: 0, Severity: lint.SevError,
			Message: "wiki/concepts/thin-links.md has no line in index.md; add one",
		},
		{
			Check: "index-sync", Path: "index.md", Line: 14, Severity: lint.SevError,
			Message: "entry [[nonexistent-catalog-entry]] points to a page that does not exist; remove it or create the page",
		},
	}
	if len(report.Findings) != len(want) {
		t.Fatalf("got %d findings, want %d: %+v", len(report.Findings), len(want), report.Findings)
	}
	for i := range want {
		got := report.Findings[i]
		if got.Path != want[i].Path || got.Line != want[i].Line || got.Message != want[i].Message {
			t.Errorf("row %d: got %+v, want %+v", i, got, want[i])
		}
	}
}

// TestIndexSyncExcludesParseErrors proves a page that failed to parse is
// not part of the "wiki/ page set" that index-sync checks index.md
// against (backbone §2.8, MASTER §9 D-W): it must not generate a
// spurious "missing from index.md" finding.
func TestIndexSyncExcludesParseErrors(t *testing.T) {
	ctx := buildVault(t, map[string]string{
		"index.md": "# Index\n\n- [[good-page]] — the only valid page.\n",
		"wiki/concepts/good-page.md": `---
title: Good Page
created: 2026-01-01
updated: 2026-01-02
type: concept
---

# Good Page
`,
		// Never closes its frontmatter block — lands in ParseErrors, not
		// in Pages(), and so must not appear as a missing index-sync row.
		"wiki/concepts/broken.md": `---
title: Broken
created: 2026-01-01
This page never closes its frontmatter block.
`,
	})

	report := lint.Run(ctx, []string{"index-sync"})
	if len(report.Findings) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(report.Findings), report.Findings)
	}
}

// TestIndexSyncOneToOneIsClean proves a perfectly synced index.md
// produces no findings.
func TestIndexSyncOneToOneIsClean(t *testing.T) {
	ctx := buildVault(t, map[string]string{
		"index.md": "# Index\n\n- [[only-page]] — the only page.\n",
		"wiki/concepts/only-page.md": `---
title: Only Page
created: 2026-01-01
updated: 2026-01-02
type: concept
---

# Only Page
`,
	})

	report := lint.Run(ctx, []string{"index-sync"})
	if len(report.Findings) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(report.Findings), report.Findings)
	}
}
