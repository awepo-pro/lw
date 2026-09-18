// title_keep_test.go pins the Transcript title's kept-id state machine and
// the nothing-staged hint (005 contract §6, as amended by R-509/D-5L — the
// user's U3 finding: an Ask turn that auto-rejected its own empty changeset
// made the id vanish from the title the moment the answer finished, and the
// UI said nothing about the session that had survived it). Every assertion
// here is made against the rendered top border, exactly what the shell
// draws, never against the pane's fields alone.
package ask

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

func TestTranscriptTitleKeepsTheLastSession(t *testing.T) {
	// empty_self_opened_turn_keeps_id_as_answered is U3 itself, as re-read
	// by A-806: after the turn's own auto-reject takes the changeset away,
	// the title keeps the id and names it `answered` — a cleanly answered
	// (DoneEv) question that staged nothing, not a verdict.
	t.Run("empty_self_opened_turn_keeps_id_as_answered", func(t *testing.T) {
		m, _, _, rejectedID := submitEmptyTurn(t)
		want := "Transcript — " + ui.ShortID(rejectedID) + " · answered"
		got := titleLine(t, m)
		if !strings.Contains(got, want) {
			t.Fatalf("the top border reads %q, want it to contain %q", got, want)
		}
		if strings.Contains(got, "rejected") {
			t.Fatalf("the top border reads %q, want no `rejected` for an answered question", got)
		}
	})

	// errored_empty_turn_keeps_id_as_rejected: the ErrorEv twin of that same
	// empty, self-opened turn — there `rejected` is the truth, and
	// `answered` must not show.
	t.Run("errored_empty_turn_keeps_id_as_rejected", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		engine, err := stage.OpenEngine(root)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		t.Cleanup(func() { engine.Close() })
		ag := &fakeTurnAgent{
			sessions: agent.NewFileSessions(root),
			script:   []agent.Event{agent.ErrorEv{Err: errors.New("the provider gave up")}},
		}
		m := New(liveDeps(t, engine, ag)).(*Model)
		m, cmd := typeAndSubmit(t, m, "still nothing to stage")
		if cmd == nil {
			t.Fatal("submit produced no command")
		}
		var seen []tea.Msg
		m = runCmd(t, m, cmd, &seen).(*Model)

		want := "Transcript — " + ui.ShortID(rejectedChangesetID(t, root)) + " · rejected"
		got := titleLine(t, m)
		if !strings.Contains(got, want) {
			t.Fatalf("the top border reads %q, want it to contain %q", got, want)
		}
		if strings.Contains(got, "answered") {
			t.Fatalf("the top border reads %q, want no `answered` for an errored turn", got)
		}
	})

	// empty_turn_adds_the_session_hint: right after that turn's terminal
	// line sits ONE status entry telling the curator where the conversation
	// went — after the done line for a finished turn, after the error entry
	// for a failed one.
	t.Run("empty_turn_adds_the_session_hint", func(t *testing.T) {
		m, _, _, rejectedID := submitEmptyTurn(t)
		want := "nothing staged · conversation kept · lw session show " + ui.ShortID(rejectedID)
		hint := lastEntry(m)
		if hint.kind != kindStatus || hint.text != want {
			t.Fatalf("last entry = %#v, want the kindStatus hint %q", hint, want)
		}
		done := m.entries[len(m.entries)-2]
		if done.kind != kindStatus || done.text != "done · 1 rounds" {
			t.Fatalf("entry before the hint = %#v, want the done line", done)
		}

		// The ErrorEv twin: the hint follows the error entry, and nowhere
		// else in the scrollback does `lw session show` appear.
		root := testutil.CopyFixture(t, "minimal")
		engine, err := stage.OpenEngine(root)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		t.Cleanup(func() { engine.Close() })
		ag := &fakeTurnAgent{
			sessions: agent.NewFileSessions(root),
			script:   []agent.Event{agent.ErrorEv{Err: errors.New("the provider gave up")}},
		}
		m = New(liveDeps(t, engine, ag)).(*Model)
		m, cmd := typeAndSubmit(t, m, "still nothing to stage")
		if cmd == nil {
			t.Fatal("submit produced no command")
		}
		var errSeen []tea.Msg
		m = runCmd(t, m, cmd, &errSeen).(*Model)

		errWant := "nothing staged · conversation kept · lw session show " + ui.ShortID(rejectedChangesetID(t, root))
		errHint := lastEntry(m)
		if errHint.kind != kindStatus || errHint.text != errWant {
			t.Fatalf("last entry = %#v, want the kindStatus hint %q", errHint, errWant)
		}
		errEntry := m.entries[len(m.entries)-2]
		if errEntry.kind != kindError || !strings.Contains(errEntry.text, "the provider gave up") {
			t.Fatalf("entry before the hint = %#v, want the error entry", errEntry)
		}
	})

	// turn_that_staged_work_gets_no_hint: the keep-it scenario — a turn
	// whose changeset survives because something was staged mid-flight —
	// never hints, and its still-open changeset keeps the bare id.
	t.Run("turn_that_staged_work_gets_no_hint", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		engine, err := stage.OpenEngine(root)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		t.Cleanup(func() { engine.Close() })

		release := make(chan struct{})
		entered := make(chan struct{})
		ag := &fakeTurnAgent{
			sessions: agent.NewFileSessions(root),
			release:  release,
			entered:  entered,
			script:   []agent.Event{agent.DoneEv{Reason: "stop", Rounds: 1}},
		}
		m := New(liveDeps(t, engine, ag)).(*Model)

		m, cmd := typeAndSubmit(t, m, "stage a small edit for me")
		if cmd == nil {
			t.Fatal("submit produced no command")
		}
		<-entered
		stageKVCachePatch(t, engine)
		close(release)
		var seen []tea.Msg
		m = runCmd(t, m, cmd, &seen).(*Model)

		cs, err := engine.Current()
		if err != nil {
			t.Fatalf("Current after a turn that staged something = %v, want the changeset still open", err)
		}
		got := titleLine(t, m)
		if !strings.Contains(got, "Transcript — "+ui.ShortID(cs.ID)) || strings.Contains(got, " · ") {
			t.Fatalf("the top border reads %q, want the open changeset's bare id", got)
		}
		for _, e := range m.entries {
			if strings.Contains(e.text, "lw session show") {
				t.Fatalf("entry %#v hints though the turn staged work", e)
			}
		}
	})

	// review_commit_keeps_id_as_committed: the empty broadcast review's
	// Commit emits keeps the id with its state — and adds no hint, a review
	// verdict is not a turn that staged nothing.
	t.Run("review_commit_keeps_id_as_committed", func(t *testing.T) {
		_, engine, csID := liveVault(t)
		stageKVCachePatch(t, engine)
		m := New(liveDeps(t, engine, nil)).(*Model)

		if _, err := engine.Commit("review accepted"); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		var seen []tea.Msg
		m = feedMsg(t, m, ui.StageChangedMsg{}, &seen).(*Model)

		want := "Transcript — " + ui.ShortID(csID) + " · committed"
		if got := titleLine(t, m); !strings.Contains(got, want) {
			t.Fatalf("the top border reads %q, want it to contain %q", got, want)
		}
		assertNoHintEntries(t, m)
	})

	// review_reject_keeps_id_as_rejected: the same through Engine.Reject.
	t.Run("review_reject_keeps_id_as_rejected", func(t *testing.T) {
		_, engine, csID := liveVault(t)
		stageKVCachePatch(t, engine)
		m := New(liveDeps(t, engine, nil)).(*Model)

		if err := engine.Reject("review declined"); err != nil {
			t.Fatalf("Reject: %v", err)
		}
		var seen []tea.Msg
		m = feedMsg(t, m, ui.StageChangedMsg{}, &seen).(*Model)

		want := "Transcript — " + ui.ShortID(csID) + " · rejected"
		if got := titleLine(t, m); !strings.Contains(got, want) {
			t.Fatalf("the top border reads %q, want it to contain %q", got, want)
		}
		assertNoHintEntries(t, m)
	})

	// open_changeset_title_has_no_suffix: the render_test.go fixture — a
	// changeset still open shows the bare id, so the approved ask-* grids
	// never move.
	t.Run("open_changeset_title_has_no_suffix", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		v := uitest.PublicVault(t, "title-keep-open")
		m := New(uitest.Deps(v, true, nil)).(*Model)
		cs, err := v.Engine.Current()
		if err != nil {
			t.Fatalf("precondition: the fixture changeset is not open: %v", err)
		}

		got := titleLine(t, m)
		if !strings.Contains(got, "Transcript — "+ui.ShortID(cs.ID)) || strings.Contains(got, " · ") {
			t.Fatalf("the top border reads %q, want the open changeset's bare id", got)
		}
	})

	// next_changeset_replaces_the_kept_id: a populated broadcast — or a
	// started turn — names the changeset open now, and the kept id gives
	// way entirely.
	t.Run("next_changeset_replaces_the_kept_id", func(t *testing.T) {
		m, engine, _, rejectedID := submitEmptyTurn(t)

		cs, err := engine.OpenChangeset("the next one", stage.Author{Kind: "human"})
		if err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		var seen []tea.Msg
		m = feedMsg(t, m, ui.StageChangedMsg{ChangesetID: cs.ID}, &seen).(*Model)

		got := titleLine(t, m)
		if !strings.Contains(got, "Transcript — "+ui.ShortID(cs.ID)) || strings.Contains(got, " · ") {
			t.Fatalf("the top border reads %q, want the new changeset's bare id", got)
		}
		if strings.Contains(got, ui.ShortID(rejectedID)) {
			t.Fatalf("the top border %q still shows the replaced id %q", got, ui.ShortID(rejectedID))
		}
	})

	// vault_reload_keeps_the_kept_id: a reload that finds nothing open
	// leaves the kept id — suffix and all — exactly where it was.
	t.Run("vault_reload_keeps_the_kept_id", func(t *testing.T) {
		m, _, _, rejectedID := submitEmptyTurn(t)
		before := titleLine(t, m)
		if !strings.Contains(before, ui.ShortID(rejectedID)+" · answered") {
			t.Fatalf("precondition: the top border reads %q, want the kept id with its state", before)
		}

		var seen []tea.Msg
		m = feedMsg(t, m, ui.VaultReloadedMsg{}, &seen).(*Model)
		if got := titleLine(t, m); got != before {
			t.Fatalf("a reload moved the kept title from %q to %q", before, got)
		}
	})

	// stale_fate_is_ignored: a state answer for some other id changes
	// nothing — between a lookup leaving and its answer landing, the kept
	// id may have moved on, and yesterday's answer must not pin today's
	// title.
	t.Run("stale_fate_is_ignored", func(t *testing.T) {
		m, _, _, _ := submitEmptyTurn(t)
		before := titleLine(t, m)

		pane, cmd := m.Update(titleFateMsg{id: "cs-somethingelse", state: "committed"})
		m = pane.(*Model)
		if cmd != nil {
			t.Fatalf("a stale fate produced a command (%#v), want nil", cmd)
		}
		if got := titleLine(t, m); got != before {
			t.Fatalf("a stale fate moved the title from %q to %q", before, got)
		}
	})

	// fate_is_resolved_off_the_render_path: the empty broadcast leaves
	// Update with a command in hand — the id bare, no state guessed — and
	// it is that command, run outside Update, which produces the
	// titleFateMsg naming the id and its on-disk state.
	t.Run("fate_is_resolved_off_the_render_path", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		v := uitest.PublicVault(t, "title-keep-fate")
		m := New(uitest.Deps(v, true, nil)).(*Model)
		cs, err := v.Engine.Current()
		if err != nil {
			t.Fatalf("precondition: the fixture changeset is not open: %v", err)
		}
		if err := v.Engine.Reject("fate-path test reject"); err != nil {
			t.Fatalf("Reject: %v", err)
		}

		pane, cmd := m.Update(ui.StageChangedMsg{})
		m = pane.(*Model)
		if cmd == nil {
			t.Fatal("the empty broadcast produced no command — the state lookup is missing")
		}
		if got := titleLine(t, m); !strings.Contains(got, ui.ShortID(cs.ID)) || strings.Contains(got, " · ") {
			t.Fatalf("before the command runs the title reads %q, want the bare id with no suffix", got)
		}

		msg := cmd()
		fate, ok := msg.(titleFateMsg)
		if !ok {
			t.Fatalf("the broadcast's command produced %#v (%T), want titleFateMsg", msg, msg)
		}
		if fate.id != cs.ID || fate.state != "rejected" {
			t.Fatalf("titleFateMsg = %#v, want {id: %s state: rejected}", fate, cs.ID)
		}
	})
}

// titleLine renders m at 80×22 and returns the plain top border — the line
// the Transcript panel's title lives on.
func titleLine(t *testing.T, m *Model) string {
	t.Helper()
	_, plain := uitest.PaneScreen(m, 80, 22)
	return strings.Split(plain, "\n")[0]
}

// assertNoHintEntries fails t if any scrollback entry names `lw session
// show` — the hint only ever follows a turn that staged nothing.
func assertNoHintEntries(t *testing.T, m *Model) {
	t.Helper()
	for _, e := range m.entries {
		if strings.Contains(e.text, "lw session show") {
			t.Fatalf("entry %#v carries the hint, want none", e)
		}
	}
}

// rejectedChangesetID returns the one id under root's changesets/rejected/.
func rejectedChangesetID(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(root, ".llmwiki", "changesets", "rejected")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", dir, err)
	}
	if len(entries) != 1 {
		t.Fatalf("changesets/rejected/ has %d entries, want exactly 1: %v", len(entries), entries)
	}
	return entries[0].Name()
}

// stageKVCachePatch appends the one-line patch_page op the turn tests use,
// so a changeset has a staged op for review to commit or reject.
func stageKVCachePatch(t *testing.T, e *stage.Engine) {
	t.Helper()
	page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
	if !ok {
		t.Fatal("fixture missing wiki/concepts/kv-cache.md")
	}
	const oldLine = "- [[flash-attention]] — a kernel design that reduces the memory-bandwidth cost"
	const newLine = "- [[flash-attention]] — an even better kernel design that reduces bandwidth"
	if !strings.Contains(page.Body, oldLine) {
		t.Fatalf("fixture body does not contain the expected line:\n%s", page.Body)
	}
	rewritten := *page
	rewritten.Body = strings.Replace(page.Body, oldLine, newLine, 1)
	if _, err := e.Append(stage.Op{
		Kind:      stage.OpPatchPage,
		Path:      page.Path,
		Section:   "## Related",
		Before:    page.SHA256(),
		Content:   rewritten.Serialize(),
		Rationale: "the turn staged this before finishing",
		Hunks: []stage.Hunk{
			{ID: "h1", Path: page.Path, Del: []string{oldLine}, Add: []string{newLine}},
		},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
}

// submitEmptyTurn runs the auto-reject scenario to quiescence (the same one
// TestSubmitWithNoOpenChangesetRejectsWhenNothingStaged drives): a turn that
// opens its own changeset, stages nothing, and is rejected when Send
// returns. It returns the pane, the engine, the vault root and the id the
// rejected changeset landed under.
func submitEmptyTurn(t *testing.T) (m *Model, engine *stage.Engine, root, rejectedID string) {
	t.Helper()
	root = testutil.CopyFixture(t, "minimal")
	engine, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { engine.Close() })

	ag := &fakeTurnAgent{
		sessions: agent.NewFileSessions(root),
		script:   []agent.Event{agent.DoneEv{Reason: "stop", Rounds: 1}},
	}
	m = New(liveDeps(t, engine, ag)).(*Model)

	m, cmd := typeAndSubmit(t, m, "nothing to stage here")
	if cmd == nil {
		t.Fatal("submit produced no command")
	}
	var seen []tea.Msg
	m = runCmd(t, m, cmd, &seen).(*Model)
	return m, engine, root, rejectedChangesetID(t, root)
}
