package lint_test

import (
	"testing"

	"github.com/awepo-pro/lw/internal/lint"
)

// orphanQueryPage is a minimal page with no inbound link — the body is
// identical in both subtests so only the directory differs.
const orphanQueryPage = `---
title: Query Page
created: 2026-01-01
updated: 2026-01-02
type: query
---

An answer page that nothing links to.
`

// TestOrphanExemptsQueries pins the wiki/queries/ exemption in
// check_link_orphan: query pages are filed answers, and nothing is expected
// to link back to an answer, so they are never orphans — while the same
// page anywhere else under wiki/ still is.
func TestOrphanExemptsQueries(t *testing.T) {
	t.Run("concept_page_without_inbound_is_orphan", func(t *testing.T) {
		ctx := buildVault(t, map[string]string{"wiki/concepts/query-page.md": orphanQueryPage})
		report := lint.Run(ctx, []string{"link-orphan"})
		if len(report.Findings) != 1 {
			t.Fatalf("got %d findings, want 1: %+v", len(report.Findings), report.Findings)
		}
		f := report.Findings[0]
		if f.Path != "wiki/concepts/query-page.md" || f.Check != "link-orphan" {
			t.Errorf("finding = %+v, want link-orphan on wiki/concepts/query-page.md", f)
		}
	})

	t.Run("query_page_without_inbound_is_not_orphan", func(t *testing.T) {
		ctx := buildVault(t, map[string]string{"wiki/queries/query-page.md": orphanQueryPage})
		report := lint.Run(ctx, []string{"link-orphan"})
		if len(report.Findings) != 0 {
			t.Fatalf("got %d findings, want 0: %+v", len(report.Findings), report.Findings)
		}
	})
}
