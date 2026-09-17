// reload_busy_test.go is 008 A-801's App-side evidence (MASTER R-802): the
// periodic reload tick consults EVERY pane's EngineBusy before calling
// Engine.ReloadIfChanged. A pane busy on another goroutine — a live ask
// turn, a review load command, on screen or not — makes the tick skip the
// reload and only re-arm; the foreign commit is not lost, because the
// journal stamp still differs when the pane goes idle (G5 review I-1).
package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// busyPane is recordingPane plus an EngineBusy switch: the fake pane the
// busy-guard tests register. It records whatever the shell delivers, so a
// subtest can assert a skipped tick delivered nothing at all. Update is
// overridden to return the outer pane: the promoted recordingPane method
// would return its embedded receiver, and deliverTo would store that back
// into the shell's map — the busy flag lost with the first broadcast.
type busyPane struct {
	recordingPane
	busy bool
}

func (b *busyPane) Update(msg tea.Msg) (Pane, tea.Cmd) {
	b.recordingPane.Update(msg)
	return b, nil
}

func (b *busyPane) EngineBusy() bool { return b.busy }

var _ EngineUser = (*busyPane)(nil)

// countReloaded returns how many of msgs are VaultReloadedMsg.
func countReloaded(msgs []tea.Msg) int {
	var n int
	for _, msg := range msgs {
		if _, ok := msg.(VaultReloadedMsg); ok {
			n++
		}
	}
	return n
}

func TestReloadSkipsWhileBusy(t *testing.T) {
	t.Run("busy_pane_skips_reload_and_rearms", func(t *testing.T) {
		a, _ := newTickApp(t, time.Millisecond)
		bp := &busyPane{recordingPane: recordingPane{name: "busy"}, busy: true}
		a.panes[ScreenBrowse] = bp // the busy pane is the one on screen
		a.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
		bp.got = nil // drop the setup WindowSizeMsg

		foreignCommit(t, a.deps.Engine.Vault().Root())

		_, cmd := a.Update(reloadTickMsg{})
		if cmd == nil {
			t.Fatal("the tick over a busy pane returned a nil cmd, want the re-armed tick")
		}
		for _, msg := range collectInitMsgs(t, cmd, 32) {
			if _, ok := msg.(VaultReloadedMsg); ok {
				t.Fatal("the tick over a busy pane produced VaultReloadedMsg — the reload ran")
			}
		}
		if len(bp.got) != 0 {
			t.Fatalf("the skipped tick delivered %v to the busy pane, want nothing", bp.got)
		}
		if got := a.View().Content; !strings.Contains(got, "4 pages") || strings.Contains(got, "5 pages") {
			t.Fatalf("header after the busy tick = %q, want it still showing the fixture's 4 pages", got)
		}

		// The stamp still differs: the first tick after the pane goes idle
		// reloads, so skipping lost nothing.
		bp.busy = false
		_, cmd = a.Update(reloadTickMsg{})
		feedApp(t, a, cmd, 32)

		if got := a.View().Content; !strings.Contains(got, "5 pages") {
			t.Fatalf("header after the idle tick = %q, want the foreign commit's 5 pages", got)
		}
		if n := countReloaded(bp.got); n != 1 {
			t.Fatalf("the idle tick delivered %d VaultReloadedMsg, want exactly 1; got %#v", n, bp.got)
		}
	})

	t.Run("idle_panes_reload", func(t *testing.T) {
		a, rec := newTickApp(t, time.Millisecond) // the only pane is idle
		a.Update(tea.WindowSizeMsg{Width: 120, Height: 24})

		foreignCommit(t, a.deps.Engine.Vault().Root())

		_, cmd := a.Update(reloadTickMsg{})
		feedApp(t, a, cmd, 32)

		if got := a.View().Content; !strings.Contains(got, "5 pages") {
			t.Fatalf("header after an idle tick = %q, want the foreign commit's 5 pages", got)
		}
		if n := countReloaded(rec.got); n != 1 {
			t.Fatalf("an all-idle tick delivered %d VaultReloadedMsg, want exactly 1; got %#v", n, rec.got)
		}
	})

	t.Run("offscreen_busy_pane_still_blocks", func(t *testing.T) {
		a, rec := newTickApp(t, time.Millisecond) // current screen: Browse, idle
		bp := &busyPane{recordingPane: recordingPane{name: "offscreen"}, busy: true}
		a.panes[ScreenAsk] = bp // registered, but not the screen on top
		a.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
		rec.got, bp.got = nil, nil

		foreignCommit(t, a.deps.Engine.Vault().Root())

		_, cmd := a.Update(reloadTickMsg{})
		if cmd == nil {
			t.Fatal("the tick over an offscreen busy pane returned a nil cmd, want the re-armed tick")
		}
		for _, msg := range collectInitMsgs(t, cmd, 32) {
			if _, ok := msg.(VaultReloadedMsg); ok {
				t.Fatal("an offscreen busy pane did not block the reload")
			}
		}
		if len(rec.got) != 0 || len(bp.got) != 0 {
			t.Fatalf("the skipped tick delivered %#v / %#v, want nothing anywhere", rec.got, bp.got)
		}
		if got := a.View().Content; !strings.Contains(got, "4 pages") || strings.Contains(got, "5 pages") {
			t.Fatalf("header after the blocked tick = %q, want it still showing 4 pages", got)
		}

		bp.busy = false
		_, cmd = a.Update(reloadTickMsg{})
		feedApp(t, a, cmd, 32)

		if got := a.View().Content; !strings.Contains(got, "5 pages") {
			t.Fatalf("header after the offscreen pane went idle = %q, want 5 pages", got)
		}
	})
}
