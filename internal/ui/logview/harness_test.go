// harness_test.go is the log tests' shared plumbing: the Deps builder, the
// key and command drivers, and the seeded commit-then-revert-then-reject
// journal history the filter and revert tests read. No test lives here.
package logview

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/ui"
)

// newTestDeps builds ui.Deps with a real Engine over a private copy of
// fixture, and lw's compiled-in Theme/KeyMap. XDG_CONFIG_HOME is pointed
// at an empty temp dir so these tests never pick up a real user config
// (mirrors internal/ui/review's and internal/ui/browse's own helper of the
// same name, in a different package).
func newTestDeps(t *testing.T, fixture string) (ui.Deps, *stage.Engine) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	root := testutil.CopyFixture(t, fixture)
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { e.Close() })

	theme, err := ui.LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	keys, err := ui.LoadKeys()
	if err != nil {
		t.Fatalf("LoadKeys: %v", err)
	}

	return ui.Deps{Engine: e, Theme: theme, Keys: keys}, e
}

// keyMsg builds a tea.KeyPressMsg for a literal key name this screen
// matches on, or a bare printable rune — the same shape
// internal/ui/browse's own (unexported, different-package) helper uses.
// Never tea.KeyMsg (C-80).
func keyMsg(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	default:
		r := []rune(s)
		return tea.KeyPressMsg{Code: r[0], Text: s}
	}
}

// runCmd executes cmd, if non-nil, against m, expanding any tea.BatchMsg it
// returns into its constituent commands and feeding every resulting
// message back through m.Update, until nothing is left to run. Headless
// tests drive Update/View directly; this is the harness's stand-in for
// what tea.Program's event loop would otherwise do (mirrors
// internal/ui/review's own helper of the same name, in a different
// package — never Program.Run(), which does not return on EOF, C-83).
func runCmd(t *testing.T, m ui.Pane, cmd tea.Cmd) ui.Pane {
	t.Helper()
	if cmd == nil {
		return m
	}
	return feedMsg(t, m, cmd())
}

func feedMsg(t *testing.T, m ui.Pane, msg tea.Msg) ui.Pane {
	t.Helper()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			m = runCmd(t, m, c)
		}
		return m
	}
	updated, cmd := m.Update(msg)
	return runCmd(t, updated, cmd)
}

// collectMsgs runs cmd (which may be a tea.Batch of several commands) and
// returns every tea.Msg it produces, in the order Batch's own slice holds
// them — without feeding any of them back through Update. Used where a
// test must assert exactly which messages a key press emits, rather than
// their effect once applied.
func collectMsgs(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, collectMsgs(t, c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// initModel constructs a logview.Model over d and drives its Init() to
// completion (running the first journal query, if an engine is present).
func initModel(t *testing.T, d ui.Deps) ui.Pane {
	t.Helper()
	m := New(d)
	return runCmd(t, m, m.Init())
}

// conceptPageContent returns a well-formed wiki/concepts page body, valid
// against the minimal fixture's SCHEMA.md taxonomy and its own
// validateCreatePage rule (>= 2 outbound wikilinks to pages the minimal
// fixture already ships) — the same shape internal/stage's own
// (unexported, different-package) newConceptPageContent test helper
// builds.
func conceptPageContent(title string) []byte {
	return []byte("---\n" +
		"title: " + title + "\n" +
		"created: 2026-08-29\n" +
		"updated: 2026-08-29\n" +
		"type: concept\n" +
		"tags: [inference]\n" +
		"confidence: medium\n" +
		"---\n" +
		"\n# " + title + "\n\n" +
		"See [[kv-cache]] and [[gpt-4]] for background.\n")
}

// seedHistory drives e through one full commit-then-revert-then-reject
// cycle, producing at least one event of every kind the five filters
// (pinned item 4) need to distinguish:
//
//   - changeset_opened, op_proposed, commit_begin, commit_end — all
//     Actor.Kind == "agent", the commit_begin/commit_end pair carrying the
//     returned commitID.
//   - changeset_opened, op_proposed (the revert's own inverse op),
//     reverted, changeset_rejected — all Actor.Kind == "human" (Revert's
//     own OpenChangeset call, backbone §5.8), the reverted event also
//     carrying commitID.
//
// Returns the commit id so a test can address its commit_begin/commit_end
// rows directly.
func seedHistory(t *testing.T, e *stage.Engine) (commitID string) {
	t.Helper()

	if _, err := e.OpenChangeset("add a page", stage.Author{Kind: "agent", Model: "test-model"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if _, err := e.Append(stage.Op{
		Kind:       stage.OpCreatePage,
		Path:       "wiki/concepts/seed-page.md",
		Content:    conceptPageContent("Seed Page"),
		Rationale:  "test seed",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	}); err != nil {
		t.Fatalf("Append create_page: %v", err)
	}
	commitID, err := e.Commit("add a page")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if _, err := e.Revert(commitID); err != nil {
		t.Fatalf("Revert(%s): %v", commitID, err)
	}
	if err := e.Reject("test reject"); err != nil {
		t.Fatalf("Reject: %v", err)
	}

	return commitID
}
