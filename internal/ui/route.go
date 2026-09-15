// route.go holds App's message-routing methods — split out of app.go
// (Tier-1 review, MASTER §8 ORCH-4/repair-1) so app.go stays under
// conventions §2's ~400-line guideline. This is a pure file split: every
// method here still binds to *App and is still called from Update's switch
// in app.go, unchanged, because Go methods bind to the type, not the file
// they're declared in.
package ui

import tea "charm.land/bubbletea/v2"

// switchTo moves focus to s, a no-op if s is not one of the five screens in
// a.order.
func (a *App) switchTo(s Screen) {
	for i, sc := range a.order {
		if sc == s {
			a.cur = i
			return
		}
	}
}

// activePane returns the pane at the currently active screen, or nil if
// none is injected.
func (a *App) activePane() Pane {
	return a.panes[a.order[a.cur]]
}

// paneCapturesKey reports whether msg is a printable keystroke the active
// pane should type (contract §5's TextCapturer, C27/D-3Q): the pane
// implements TextCapturer, CapturesText() is true, and the key carries text
// under no modifier but shift. While it does, handleKey delivers the key
// straight to the pane without matching the global bindings, so `q` and `?`
// type into a text-taking pane instead of quitting or opening the overlay.
func (a *App) paneCapturesKey(msg tea.KeyPressMsg) bool {
	tc, ok := a.activePane().(TextCapturer)
	return ok && tc.CapturesText() && printableKey(msg)
}

// printableKey reports whether msg is a plain printable keystroke: it
// carries text and no modifier but shift. The same rule the text panes type
// by (ask.go's input box, browse.go's finder) — one shared rule, so the
// shell's idea of "typing" cannot drift from a pane's.
func printableKey(msg tea.KeyPressMsg) bool {
	return msg.Text != "" && msg.Mod&^tea.ModShift == 0
}

// handleWheel routes one mouse-wheel notch to the active pane (contract §5
// frame note 7, W5 F2/D-3W). Only wheel-up and wheel-down are wheel input;
// the notch is dropped when the terminal is too small, the ? overlay is
// open, or it landed on the shell's own header or footer row. Otherwise the
// pane under focus receives one WheelMsg with pane-local coordinates: Y
// loses the header row, H loses header and footer, and Delta is -1 for up
// or +1 for down.
func (a *App) handleWheel(msg tea.MouseWheelMsg) tea.Cmd {
	if msg.Button != tea.MouseWheelUp && msg.Button != tea.MouseWheelDown {
		return nil
	}
	if a.tooSmall() || a.overlayOpen {
		return nil
	}
	if msg.Y <= 0 || msg.Y >= a.height-1 {
		return nil
	}
	delta := 1
	if msg.Button == tea.MouseWheelUp {
		delta = -1
	}
	return a.propagate(WheelMsg{
		X:     msg.X,
		Y:     msg.Y - 1,
		W:     a.width,
		H:     a.height - 2,
		Delta: delta,
	})
}

// propagate forwards msg to the active pane's Update, if one is injected
// for the current screen, and stores the pane it returns back into the map
// — Pane.Update returns a (possibly new) Pane the same way tea.Model.Update
// returns a (possibly new) Model. Whatever command the pane returns comes
// back tagged with its screen (deliverTo's producedBy), so its answer
// reaches it however the active screen has moved on in the meantime.
func (a *App) propagate(msg tea.Msg) tea.Cmd {
	return a.deliverTo(a.order[a.cur], msg)
}

// propagateAll forwards msg to every injected pane's Update, active or not
// (backbone §12, s4-tui.md S4-T8, C-106/TD-4, C-117/D-DA) — used for the
// named set of messages a pane must never miss regardless of which screen
// is on top: the shell's own StageChangedMsg, VaultReloadedMsg,
// tea.WindowSizeMsg and tea.BackgroundColorMsg, and the ask screen's stream
// pump (StreamMsg, EventMsg, StreamClosedMsg), which pane.go declares so
// Update can route them by name. Iterates a.order, a fixed slice, rather
// than ranging a.panes directly, so which pane's Update runs first stays
// deterministic even though no pane's returned Cmd depends on that order
// (00-conventions.md §3).
func (a *App) propagateAll(msg tea.Msg) tea.Cmd {
	var cmds []tea.Cmd
	for _, s := range a.order {
		if cmd := a.deliverTo(s, msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}

// deliverTo forwards msg to the pane at screen s specifically — regardless
// of which screen is currently active — and stores the (possibly new) pane
// it returns back into the map. propagate and propagateAll are both built
// on this; OpenPathMsg's handler in Update also calls it directly so the
// message reaches the Browse pane even on the frame Browse becomes active
// (C-108/D-CU).
//
// The command the pane returns is enveloped with s (producedBy) before it
// goes back: a tea.Cmd's result is delivered to App.Update, not to the pane,
// so without the tag the answer to an off-screen pane's own command would be
// routed to whichever pane is active — the gap this closes.
func (a *App) deliverTo(s Screen, msg tea.Msg) tea.Cmd {
	p, ok := a.panes[s]
	if !ok || p == nil {
		return nil
	}
	updated, cmd := p.Update(msg)
	a.panes[s] = updated
	if cmd == nil {
		return nil
	}
	return producedBy(s, cmd)
}

// producedBy wraps cmd so the message it produces reaches Update tagged with
// the screen whose pane produced it (paneMsg). A nil cmd stays nil, and a
// cmd that produces no message stays a cmd that produces no message — the
// runtime treats a nil tea.Msg as nothing to deliver, and so does the
// envelope.
//
// A message the shell or the runtime itself consumes (shellOwned) is passed
// through untouched: it is a command to the shell, not the producing pane's
// answer, and Update would route it by the same named case either way.
// Leaving it bare keeps the shell's message stream exactly what it was
// before the envelope existed — which is what logview's revert, ask's ctrl+r
// and lintview's enter all depend on, and what anything watching that stream
// from outside ui is entitled to.
//
// A tea.BatchMsg is the one message the envelope must not carry: the runtime
// expands a batch itself and never hands one to Update, so an enveloped
// batch would arrive at Update's default branch as an ordinary message and
// be delivered, unexpanded, to a single pane — three quarters of logview's
// revert (its query, the StageChangedMsg and the jump to Review) would
// vanish into the log pane. Each constituent is wrapped with the same
// producer instead, which is what the runtime would have done with them.
func producedBy(from Screen, cmd tea.Cmd) tea.Cmd {
	return func() tea.Msg {
		msg := cmd()
		if msg == nil {
			return nil
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			var wrapped tea.BatchMsg
			for _, c := range batch {
				if c == nil {
					continue
				}
				wrapped = append(wrapped, producedBy(from, c))
			}
			if len(wrapped) == 0 {
				return nil
			}
			return wrapped
		}
		if shellOwned(msg) {
			return msg
		}
		return paneMsg{from: from, msg: msg}
	}
}

// shellOwned reports whether msg is addressed to the shell or the runtime
// rather than to a pane: the shell's own message vocabulary (pane.go) plus
// the runtime's key and quit traffic. See producedBy for why those are left
// bare. A message missing from this set is still routed correctly when it is
// enveloped — Update unwraps before any of its cases — so this list shapes
// who may watch the shell's message stream, never where a message goes.
func shellOwned(msg tea.Msg) bool {
	switch msg.(type) {
	case tea.KeyPressMsg, tea.KeyReleaseMsg, tea.WindowSizeMsg,
		tea.BackgroundColorMsg, tea.QuitMsg,
		StageChangedMsg, VaultReloadedMsg, StreamMsg, EventMsg,
		StreamClosedMsg, SwitchScreenMsg, OpenPathMsg:
		return true
	default:
		return false
	}
}
