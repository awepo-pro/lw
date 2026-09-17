// shell_key_disarm_test.go is 008 A-801's M-1 evidence (MASTER R-802),
// driven through a real ui.App with the real Review pane: a key the SHELL
// consumes — tab's screen switch, the `?` overlay opening and closing —
// must disarm the raw-only commit confirmation exactly the way a
// pane-visible key does, so "a second C, with no other key between" holds
// for every key, not only the ones that reach the pane.
package review

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// newRawOnlyApp is newRawOnlyModel lifted to the shell: the real App over
// the real Review pane with the two-ingest raw-only changeset loaded, on
// the Review screen. It returns the app, the pane reference (stable —
// Update mutates the model in place), the engine and the vault root.
func newRawOnlyApp(t *testing.T) (*ui.App, ui.Pane, *stage.Engine, string) {
	t.Helper()
	d, e, root := newTestDeps(t, "minimal")
	if _, err := e.OpenChangeset("raw-only via the shell", stage.Author{Kind: "agent", Model: "test"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	appendRawIngest(t, e, "raw/articles/agent-memory.md", "# Agent Memory\n\nNotes outlive the session.\n")
	appendRawIngest(t, e, "raw/articles/context-window.md", "# Context Windows\n\nThe window is finite.\n")
	pane := initModel(t, d)
	app := ui.NewApp(ui.Options{
		Deps:  d,
		Panes: map[ui.Screen]ui.Pane{ui.ScreenReview: pane},
		Start: ui.ScreenReview,
	})
	app.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	return app, pane, e, root
}

// pressC sends C through the shell and drains whatever it produced.
func pressC(t *testing.T, a *ui.App) {
	t.Helper()
	_, cmd := a.Update(keyPress('C'))
	feedShell(t, a, cmd, 64)
}

func TestShellKeyDisarms(t *testing.T) {
	t.Run("tab_between_Cs_disarms", func(t *testing.T) {
		a, pane, _, root := newRawOnlyApp(t)

		pressC(t, a) // arms
		if msg, level := statusOf(t, pane); msg != wantRawOnlyWarn || level != ui.StatusWarn {
			t.Fatalf("setup: first C = (%q, %v), want the raw-only warning", msg, level)
		}

		// tab: the shell consumes the key and switches screens — the pane
		// being left must still hear that a key went by.
		_, cmd := a.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		feedShell(t, a, cmd, 64)
		if got := a.View().Content; !strings.Contains(got, "(ask screen not loaded yet)") {
			t.Fatalf("tab did not leave Review: %q", got)
		}

		// Back to Review the way a pane does it (ask's ctrl+r), then C.
		m, _ := a.Update(ui.SwitchScreenMsg{To: ui.ScreenReview})
		a = m.(*ui.App)
		pressC(t, a)

		if journalHasCommitBegin(t, root) {
			t.Fatal("C, tab, C committed — the screen switch did not disarm the confirmation")
		}
		if msg, level := statusOf(t, pane); msg != wantRawOnlyWarn || level != ui.StatusWarn {
			t.Fatalf("status after C, tab, C = (%q, %v), want the warning shown again", msg, level)
		}

		// Without the interloping key, C C still commits (008 contract §5).
		a2, pane2, e2, root2 := newRawOnlyApp(t)
		pressC(t, a2)
		pressC(t, a2)

		if !journalHasCommitBegin(t, root2) {
			t.Fatal("C, C did not commit — the guard broke the plain path")
		}
		if msg, level := statusOf(t, pane2); !strings.HasPrefix(msg, "committed ") || level != ui.StatusGood {
			t.Errorf("status after C, C = (%q, %v), want \"committed <id>\" at StatusGood", msg, level)
		}
		if _, err := e2.Current(); !errors.Is(err, stage.ErrNoChangeset) {
			t.Errorf("Current after C, C: %v, want ErrNoChangeset", err)
		}
	})

	t.Run("help_overlay_between_Cs_disarms", func(t *testing.T) {
		a, pane, _, root := newRawOnlyApp(t)

		pressC(t, a) // arms
		if msg, level := statusOf(t, pane); msg != wantRawOnlyWarn || level != ui.StatusWarn {
			t.Fatalf("setup: first C = (%q, %v), want the raw-only warning", msg, level)
		}

		// ? opens the overlay; esc closes it. Both keys are consumed by the
		// shell, so the pane must hear of them another way.
		m, cmd := a.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
		a = m.(*ui.App)
		feedShell(t, a, cmd, 64)
		if got := a.View().Content; !strings.Contains(got, "esc to close") {
			t.Fatal("setup: the ? overlay did not open")
		}
		m, cmd = a.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		a = m.(*ui.App)
		feedShell(t, a, cmd, 64)
		if got := a.View().Content; strings.Contains(got, "esc to close") {
			t.Fatal("setup: the overlay did not close on esc")
		}

		pressC(t, a)

		if journalHasCommitBegin(t, root) {
			t.Fatal("C, ?, esc, C committed — the overlay keys did not disarm the confirmation")
		}
		if msg, level := statusOf(t, pane); msg != wantRawOnlyWarn || level != ui.StatusWarn {
			t.Fatalf("status after C, ?, esc, C = (%q, %v), want the warning shown again", msg, level)
		}
	})
}
