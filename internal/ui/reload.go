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
func (a *App) handleReloadTick() tea.Cmd {
	if a.deps.Engine != nil {
		if reloaded, err := a.deps.Engine.ReloadIfChanged(); err == nil && reloaded {
			return tea.Batch(
				func() tea.Msg { return VaultReloadedMsg{} },
				reloadTickCmd(a.reloadEvery),
			)
		}
	}
	return reloadTickCmd(a.reloadEvery)
}
