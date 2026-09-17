// engine_busy_test.go is 008 A-801's review-side evidence (MASTER R-802):
// the pane reports EngineBusy while a load command it returned has not yet
// delivered its loadedMsg back to Update, and idle afterwards — counting
// overlapping loads, since a commit batches one beside the broadcast
// handlers' own (G5 review I-1: loadCmd reads the Engine on a tea.Cmd
// goroutine, so the shell must not run ReloadIfChanged under it).
package review

import (
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// engineBusyOf reads p's EngineBusy through the shell's interface, failing
// t when the pane does not implement it at all.
func engineBusyOf(t *testing.T, p ui.Pane) bool {
	t.Helper()
	eu, ok := p.(ui.EngineUser)
	if !ok {
		t.Fatalf("pane is %T, want a ui.EngineUser", p)
	}
	return eu.EngineBusy()
}

func TestReviewEngineBusy(t *testing.T) {
	t.Run("busy_while_loading", func(t *testing.T) {
		d, e, _ := newTestDeps(t, "minimal")
		if _, err := e.OpenChangeset("busy while loading", stage.Author{Kind: "agent", Model: "test"}); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		appendRawIngest(t, e, "raw/articles/agent-memory.md", "# Agent Memory\n\nNotes outlive the session.\n")
		m := New(d)

		cmd := m.Init() // the load round trip is issued, not yet answered
		if !engineBusyOf(t, m) {
			t.Fatal("EngineBusy = false with Init's loadCmd in flight, want true")
		}
		m = runCmd(t, m, cmd).(*Model)
		if engineBusyOf(t, m) {
			t.Fatal("EngineBusy = true after loadedMsg was applied, want false")
		}

		// Two loads can be in flight at once (a StageChangedMsg and a
		// VaultReloadedMsg landing back to back): the pane counts them, so
		// it stays busy until the LAST one lands.
		_, first := m.Update(ui.StageChangedMsg{})
		_, second := m.Update(ui.VaultReloadedMsg{})
		if !engineBusyOf(t, m) {
			t.Fatal("EngineBusy = false with two loads in flight, want true")
		}
		m = feedMsg(t, m, first())
		if !engineBusyOf(t, m) {
			t.Fatal("EngineBusy = false with one of two loads still in flight, want true")
		}
		m = feedMsg(t, m, second())
		if engineBusyOf(t, m) {
			t.Fatal("EngineBusy = true after both loads delivered, want false")
		}
	})

	t.Run("idle_after_load", func(t *testing.T) {
		m, _, _ := newRawOnlyModel(t) // initModel drove Init to quiescence

		if engineBusyOf(t, m) {
			t.Fatal("EngineBusy = true after the load settled, want false")
		}
		// A key that issues no load keeps it idle.
		m = send(t, m, keyPress('j'))
		if engineBusyOf(t, m) {
			t.Fatal("EngineBusy = true after a load-free key, want false")
		}
	})
}
