// harness_test.go is the review tests' shared harness: the Deps builder,
// the key/message drivers that stand in for tea.Program's event loop, and
// the small engine/model accessors the action, stale and preview tests
// all read through. No test lives here — only the plumbing they share.
package review

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/ui"
)

// newTestDeps builds ui.Deps with a real Engine over a private copy of
// fixture, and lw's compiled-in Theme/KeyMap. XDG_CONFIG_HOME is pointed
// at an empty temp dir so these tests never pick up a real user config —
// mirrors internal/ui's own (unexported, different-package) testDeps
// helper.
func newTestDeps(t *testing.T, fixture string) (ui.Deps, *stage.Engine, string) {
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

	return ui.Deps{Engine: e, Theme: theme, Keys: keys}, e, root
}

// keyPress builds a synthetic tea.KeyPressMsg for a single printable rune,
// the shape internal/ui's own tests use (app_test.go: Code and Text both
// set) — never via tea.Program.Run(), which does not return on EOF (C-83).
func keyPress(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Text: string(r)}
}

// runCmd executes cmd, if non-nil, against m, expanding any tea.BatchMsg it
// returns into its constituent commands and feeding every resulting
// message back through m.Update, until nothing is left to run. Headless
// tests drive Update/View directly; this is the harness's stand-in for
// what tea.Program's event loop would otherwise do.
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

// send drives msg through m.Update and drains every tea.Cmd it (and
// whatever those in turn produce) returns, so a test can send one key and
// trust the model has fully settled — including its own reload — before
// asserting anything.
func send(t *testing.T, m ui.Pane, msg tea.Msg) ui.Pane {
	t.Helper()
	updated, cmd := m.Update(msg)
	return runCmd(t, updated, cmd)
}

// initModel constructs a review.Model over d and drives its Init() to
// completion (loading the open changeset and its diff, if any).
func initModel(t *testing.T, d ui.Deps) ui.Pane {
	t.Helper()
	m := New(d)
	return runCmd(t, m, m.Init())
}

// findFileDiff returns the FileDiff d carries for opID, failing t if there
// is none.
func findFileDiff(t *testing.T, d stage.Diff, opID string) stage.FileDiff {
	t.Helper()
	for _, f := range d.Files {
		if f.OpID == opID {
			return f
		}
	}
	t.Fatalf("Diff has no FileDiff for op %s (files: %+v)", opID, d.Files)
	return stage.FileDiff{}
}

// mustDiff calls e.Diff(), failing t on error.
func mustDiff(t *testing.T, e *stage.Engine) stage.Diff {
	t.Helper()
	d, err := e.Diff()
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	return d
}

// statusOf reads m's StatusReporter message, failing t when the pane
// implements none (contract §5: a refusal lives in the footer's status
// path, not in View — panes no longer draw a status line of their own).
func statusOf(t *testing.T, m ui.Pane) (string, ui.StatusLevel) {
	t.Helper()
	sr, ok := m.(ui.StatusReporter)
	if !ok {
		t.Fatal("review pane does not implement ui.StatusReporter")
		return "", 0
	}
	return sr.Status()
}

// appendTwoHunkPatch appends the two-hunk patch_page op on kv-cache.md the
// review-action tests walk, returning its op id.
func appendTwoHunkPatch(t *testing.T, e *stage.Engine) string {
	t.Helper()
	page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
	if !ok {
		t.Fatal("fixture missing wiki/concepts/kv-cache.md")
	}
	const (
		oldFlash = "- [[flash-attention]] — a kernel design that reduces the memory-bandwidth cost"
		newFlash = "- [[flash-attention]] — an even better kernel design that reduces bandwidth"
		oldSpec  = "- [[speculative-decoding]] — both the draft and target model read the cache"
		newSpec  = "- [[speculative-decoding]] — the draft model proposes; the target model verifies"
	)
	if !strings.Contains(page.Body, oldFlash) || !strings.Contains(page.Body, oldSpec) {
		t.Fatalf("fixture body does not contain the expected lines:\n%s", page.Body)
	}
	rewritten := *page
	rewritten.Body = strings.Replace(page.Body, oldFlash, newFlash, 1)
	rewritten.Body = strings.Replace(rewritten.Body, oldSpec, newSpec, 1)

	opID, err := e.Append(stage.Op{
		Kind:      stage.OpPatchPage,
		Path:      page.Path,
		Section:   "## Related",
		Before:    page.SHA256(),
		Content:   rewritten.Serialize(),
		Rationale: "sharpen two related-links",
		Hunks: []stage.Hunk{
			{ID: "h1", Path: page.Path, Del: []string{oldFlash}, Add: []string{newFlash}},
			{ID: "h2", Path: page.Path, Del: []string{oldSpec}, Add: []string{newSpec}},
		},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	return opID
}

// mustOp fetches opID from the engine's current changeset.
func mustOp(t *testing.T, e *stage.Engine, opID string) (*stage.Op, bool) {
	t.Helper()
	cs, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	return cs.Op(opID)
}
