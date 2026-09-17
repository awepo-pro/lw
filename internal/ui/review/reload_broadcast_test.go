// reload_broadcast_test.go is 008 T-G's U4 evidence for the producer half
// (MASTER §5): Review's successful C must batch a ui.VaultReloadedMsg
// producer alongside its existing messages, so the shell's header counts
// refresh on an in-TUI commit — the path that had subscribers and no
// producer before 008.
package review

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// collectCmds executes cmd, expanding tea.BatchMsg one level at a time, and
// returns every message produced. Bounded by budget so a producer that
// re-arms itself can never loop a test forever.
func collectCmds(t *testing.T, cmd tea.Cmd, budget int) []tea.Msg {
	t.Helper()
	if cmd == nil || budget <= 0 {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var got []tea.Msg
		for _, c := range batch {
			got = append(got, collectCmds(t, c, budget-1)...)
		}
		return got
	}
	return []tea.Msg{msg}
}

// feedShell executes cmd and feeds everything it produces back through the
// shell's Update until the stream runs dry — the harness's stand-in for
// tea.Program's loop over the App. Enveloped pane answers, broadcasts and
// batches all re-enter App.Update, which unwraps and routes them; bounded
// by budget so a self-re-arming producer can never loop a test forever.
func feedShell(t *testing.T, app *ui.App, cmd tea.Cmd, budget int) {
	t.Helper()
	if cmd == nil || budget <= 0 {
		return
	}
	msg := cmd()
	if msg == nil {
		return
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			feedShell(t, app, c, budget-1)
		}
		return
	}
	_, next := app.Update(msg)
	feedShell(t, app, next, budget-1)
}

// newCommitModel stages one create_page and loads the pane to quiescence.
func newCommitModel(t *testing.T) (ui.Pane, ui.Deps, *stage.Engine) {
	t.Helper()
	d, e, _ := newTestDeps(t, "minimal")
	if _, err := e.OpenChangeset("in-tui commit", stage.Author{Kind: "agent", Model: "test"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	appendPlainCreate(t, e)
	return initModel(t, d), d, e
}

func TestCommitBroadcastsVaultReloaded(t *testing.T) {
	t.Run("commit_batch_contains_vault_reloaded", func(t *testing.T) {
		m, _, _ := newCommitModel(t)

		_, cmd := m.Update(keyPress('C'))
		got := collectCmds(t, cmd, 32)

		var reloaded int
		for _, msg := range got {
			if _, ok := msg.(ui.VaultReloadedMsg); ok {
				reloaded++
			}
		}
		if reloaded != 1 {
			t.Fatalf("successful C produced %d ui.VaultReloadedMsg, want exactly 1; msgs: %#v", reloaded, got)
		}
	})

	t.Run("header_counts_refresh_after_in_tui_commit", func(t *testing.T) {
		m, d, _ := newCommitModel(t)

		// The real shell with the real Review pane, ReloadEvery zero — the
		// producer path, not the tick (MASTER §5).
		app := ui.NewApp(ui.Options{
			Deps:  d,
			Panes: map[ui.Screen]ui.Pane{ui.ScreenReview: m},
			Start: ui.ScreenReview,
		})
		app.Update(tea.WindowSizeMsg{Width: 120, Height: 24})

		if before := app.View().Content; !strings.Contains(before, "4 pages") {
			t.Fatalf("header before the commit = %q, want it to show the fixture's 4 pages", before)
		}

		// Press C through the shell and feed every produced message back —
		// what tea.Program's loop would do. The reload must land on the
		// header with no second process involved.
		_, cmd := app.Update(keyPress('C'))
		feedShell(t, app, cmd, 64)

		if after := app.View().Content; !strings.Contains(after, "5 pages") {
			t.Fatalf("header after the in-TUI commit = %q, want 5 pages", after)
		}
	})
}
