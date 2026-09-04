package stage

import (
	"errors"
	"testing"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/lint"
)

// TestProjectedReportPredictsPostCommit pins MASTER §9 D-CC / §10 OR-11:
// ProjectedReport is what `lw lint` would print if the open changeset were
// committed right now, so `lw commit` can run D-AG's
// Report.Regresses(prev) BEFORE writing a byte. Changeset.Checks cannot
// serve — it carries Lint "pass"/"fail" and no counts at all.
//
// The equality asserted here is only true after S2-T8 (C-65) aligned the
// projection with what Commit actually writes; before it, retract and
// split_page diverged.
func TestProjectedReportPredictsPostCommit(t *testing.T) {
	danglingPage := []byte("---\ntitle: Dangling\ncreated: 2026-08-29\nupdated: 2026-08-29\n" +
		"type: concept\ntags: [inference]\nconfidence: medium\n---\n\n" +
		"# Dangling\n\nSee [[no-such-one]] and [[no-such-two]].\n")

	cases := []struct {
		name  string
		stage func(t *testing.T, e *Engine)
	}{
		{"create_page", func(t *testing.T, e *Engine) {
			appendCreate(t, e, "wiki/concepts/brand-new.md", newConceptPageContent("Brand New"))
		}},
		{"create_page with dangling links", func(t *testing.T, e *Engine) {
			appendCreate(t, e, "wiki/concepts/dangling.md", danglingPage)
		}},
		{"rename_page", func(t *testing.T, e *Engine) {
			if _, err := e.Append(Op{Kind: OpRenamePage,
				From: "wiki/concepts/kv-cache.md", To: "wiki/concepts/kv-caching.md"}); err != nil {
				t.Fatalf("Append rename_page: %v", err)
			}
		}},
		{"retract", func(t *testing.T, e *Engine) {
			if _, err := e.Append(Op{Kind: OpRetract,
				Path: "wiki/concepts/kv-cache.md", Rationale: "test"}); err != nil {
				t.Fatalf("Append retract: %v", err)
			}
		}},
		{"split_page", func(t *testing.T, e *Engine) {
			p1, p2 := "wiki/concepts/kv-a.md", "wiki/concepts/kv-b.md"
			if _, err := e.Append(Op{Kind: OpSplitPage,
				Path: "wiki/concepts/kv-cache.md", Sources: []string{p1, p2}}); err != nil {
				t.Fatalf("Append split_page: %v", err)
			}
			appendCreate(t, e, p1, newConceptPageContent("KV A"))
			appendCreate(t, e, p2, newConceptPageContent("KV B"))
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := newTestEngine(t)
			if _, err := e.OpenChangeset(tc.name, testAuthor); err != nil {
				t.Fatalf("OpenChangeset: %v", err)
			}
			tc.stage(t, e)

			pre, err := e.ProjectedReport()
			if err != nil {
				t.Fatalf("ProjectedReport: %v", err)
			}
			if _, err := e.Commit(tc.name); err != nil {
				t.Fatalf("Commit: %v", err)
			}
			v := e.Vault()
			post := lint.Run(&lint.Context{Vault: v, Index: index.Build(v), Graph: v.Graph()}, nil)

			if pre.Errors != post.Errors || pre.Warns != post.Warns {
				t.Errorf("ProjectedReport{errors=%d warns=%d} != post-commit{errors=%d warns=%d}",
					pre.Errors, pre.Warns, post.Errors, post.Warns)
			}
		})
	}
}

// TestProjectedReportNeedsAnOpenChangeset pins the ErrNoChangeset arm —
// `lw commit` with nothing staged must fail the same way every other verb
// does, not panic on a nil changeset.
func TestProjectedReportNeedsAnOpenChangeset(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.ProjectedReport(); !errors.Is(err, ErrNoChangeset) {
		t.Fatalf("ProjectedReport with no open changeset = %v, want ErrNoChangeset", err)
	}
}

// TestProjectedReportCarriesCountsChecksCannot is the load-bearing half:
// Changeset.Checks says only "fail", while the regression check D-AG names
// needs the number.
func TestProjectedReportCarriesCountsChecksCannot(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("dangling", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	appendCreate(t, e, "wiki/concepts/dangling.md", []byte(
		"---\ntitle: Dangling\ncreated: 2026-08-29\nupdated: 2026-08-29\n"+
			"type: concept\ntags: [inference]\nconfidence: medium\n---\n\n"+
			"# Dangling\n\nSee [[no-such-one]] and [[no-such-two]].\n"))

	c, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if c.Checks.Lint != "fail" {
		t.Fatalf("Checks.Lint = %q, want %q", c.Checks.Lint, "fail")
	}
	rep, err := e.ProjectedReport()
	if err != nil {
		t.Fatalf("ProjectedReport: %v", err)
	}
	if rep.Errors != 2 {
		t.Errorf("ProjectedReport().Errors = %d, want 2 (both dangling links)", rep.Errors)
	}
	// The regression check itself, end to end.
	if !rep.Regresses(lint.Report{Errors: 0}) {
		t.Error("Regresses(prev errors=0) = false, want true — this is D-AG's whole check")
	}
	if rep.Regresses(lint.Report{Errors: 5}) {
		t.Error("Regresses(prev errors=5) = true, want false — an already-dirtier vault must not block a commit")
	}
}

// appendCreate appends a well-formed create_page op for path with content.
func appendCreate(t *testing.T, e *Engine, path string, content []byte) {
	t.Helper()
	if _, err := e.Append(Op{
		Kind: OpCreatePage, Path: path, Content: content, Rationale: "test",
		Provenance: []string{"raw/articles/kv-cache-explained.md"},
	}); err != nil {
		t.Fatalf("Append create_page %s: %v", path, err)
	}
}
