// reload.go implements 008 contract §5's periodic vault reload (U4): with
// Options.ReloadEvery set, the App re-arms a tea.Tick whose message, handled
// here, calls Engine.ReloadIfChanged — and when another process's commit
// landed, produces the VaultReloadedMsg whose existing Update case refreshes
// the header counts and broadcasts to every pane. Review's own successful
// commit feeds the same message from its side (the producer half), so both
// the in-TUI commit and the foreign one land on the same path.
package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// reloadTickMsg is the App's periodic-reload tick (008 contract §5).
// Unexported and constructed only by reloadTickCmd, so a test can feed one
// to Update without sleeping through a real tick.
type reloadTickMsg struct{}

// reloadTickCmd returns the tick's tea.Tick command. It only sleeps and
// delivers the message: the threading rule from T-D's review puts every
// engine read in Update's handler, because tea.Cmd bodies run on goroutines
// of their own.
func reloadTickCmd(every time.Duration) tea.Cmd {
	return tea.Tick(every, func(time.Time) tea.Msg { return reloadTickMsg{} })
}

// handleReloadTick is Update's reloadTickMsg case. It runs on the Update
// goroutine — the one place Engine state may be read while the shell is
// live, and the one place Review's synchronous in-Update commit can never
// overlap with. A reload produces ui.VaultReloadedMsg and lets the existing
// case do the refresh and the fan-out; whatever happened, the tick re-arms
// itself, and a failed stat is not fatal — the next tick retries.
//
// 008 A-801 (G5 review I-1): ReloadIfChanged mutates the vault and the
// index, and a pane reporting EngineBusy is using this Engine from another
// goroutine right now — an ask turn whose tool handlers read the vault's
// maps, a review loadCmd calling Current on its own goroutine. The reload
// is therefore skipped for that tick and only the re-arm is returned; the
// foreign commit is not lost, because the journal stamp still differs and
// the first tick after every pane goes idle reloads.
func (a *App) handleReloadTick() tea.Cmd {
	if a.deps.Engine != nil && !a.anyPaneBusy() {
		if reloaded, err := a.deps.Engine.ReloadIfChanged(); err == nil && reloaded {
			return tea.Batch(
				func() tea.Msg { return VaultReloadedMsg{} },
				reloadTickCmd(a.reloadEvery),
			)
		}
	}
	return reloadTickCmd(a.reloadEvery)
}

// anyPaneBusy reports whether any injected pane implements EngineUser and
// reports itself busy (008 A-801). Every pane is consulted, not just the
// one on screen: the pane using the engine from another goroutine is
// usually not the one the curator is looking at — an ask turn left running
// while they read Review is exactly the case I-1 probed. It iterates
// a.order, the fixed screen slice, the way propagateAll does, so the visit
// order stays deterministic.
func (a *App) anyPaneBusy() bool {
	for _, s := range a.order {
		if p := a.panes[s]; p != nil {
			if eu, ok := p.(EngineUser); ok && eu.EngineBusy() {
				return true
			}
		}
	}
	return false
}
