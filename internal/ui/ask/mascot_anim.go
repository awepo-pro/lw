// mascot_anim.go holds 023's mascot motion (amendment A-023-1, which
// reverses 016 F.M5's fully-static rule through the logged amendment
// path): the thinking eye-scan, the idle blink, and the tea.Tick chains
// that drive them. This file is the only place in the package a clock may
// live, and the anim runs on Update's thread — the ticks only sleep and
// deliver, the handlers below decide by the pane's own state whether to
// act and re-arm, and a chain stops by not re-issuing. Nothing here
// re-derives pane state: the same mascotState/round flags the art reads
// (mascot.go, state.go) decide every beat.
package ask

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// The anim's clock (F.A2): the eyes scan while the model thinks, and an
// idle pane blinks every blinkEvery, holding the shut frame one blinkHold.
const (
	scanEvery  = 200 * time.Millisecond
	blinkEvery = 4 * time.Second
	blinkHold  = 120 * time.Millisecond
)

// mascotScanCycle is the thinking eye-scan's pose order (F.A2): up, left,
// up, right, repeat — the reel's array, the look between darts included.
var mascotScanCycle = [4]mascotFrame{
	frameThinking, // up
	frameScanLeft,
	frameThinking, // up
	frameScanRight,
}

// scanTickMsg, blinkTickMsg and blinkOpenMsg are the anim's three beats:
// the scan's 200ms step, the blink's every-4s open→shut edge, and the
// 120ms hold that opens the eyes again. Each is constructed only by the
// tea.Tick command beside it, so a test can feed one to Update without
// sleeping through a real beat (the reloadTickMsg shape, internal/ui/
// reload.go).
type (
	scanTickMsg  struct{}
	blinkTickMsg struct{}
	blinkOpenMsg struct{}
)

func scanTickCmd() tea.Cmd {
	return tea.Tick(scanEvery, func(time.Time) tea.Msg { return scanTickMsg{} })
}

func blinkTickCmd() tea.Cmd {
	return tea.Tick(blinkEvery, func(time.Time) tea.Msg { return blinkTickMsg{} })
}

func blinkHoldCmd() tea.Cmd {
	return tea.Tick(blinkHold, func(time.Time) tea.Msg { return blinkOpenMsg{} })
}

// blinkEligible reports whether an idle pane may blink (F.A5): answering
// and a tool round are deliberately still — the streamed text is the
// performance — and the error state is frozen, stillness being the point
// there.
func (m *Model) blinkEligible() bool {
	return !m.turnActive && m.mascotState() != msError
}

// handleScanTick is Update's scanTickMsg case: while the round is thinking
// it advances the scan cycle one beat and re-arms; anything else — the
// first answer token, a tool round, the turn's end — stops the chain by
// not re-issuing (F.A3).
func (m *Model) handleScanTick() tea.Cmd {
	m.scanArmed = false
	if !m.anim || !m.thinkingVisible() {
		return nil
	}
	m.scanStep++
	m.scanArmed = true
	return scanTickCmd()
}

// handleBlinkTick is Update's blinkTickMsg case: on an eligible pane it
// starts one blink — the eyes shut for one blinkHold — and arms the
// blinkOpenMsg that reopens them; anywhere else the chain dies here (F.A3,
// F.A5).
func (m *Model) handleBlinkTick() tea.Cmd {
	m.blinkArmed = false
	if !m.anim || !m.blinkEligible() {
		return nil
	}
	m.eyesShut = true
	return blinkHoldCmd()
}

// handleBlinkOpen is Update's blinkOpenMsg case: the hold is over, the
// eyes open, and the every-4s tick re-arms while the pane may still blink
// (F.A2's open → shut → open cycle).
func (m *Model) handleBlinkOpen() tea.Cmd {
	m.eyesShut = false
	if !m.anim || !m.blinkEligible() {
		return nil
	}
	m.blinkArmed = true
	return blinkTickCmd()
}

// mascotPose selects the frame the pane draws for state s — the state
// table's frame (mascotFrameFor) with 023's motion overlaid, and only when
// the anim switch is on: with anim false every render is today's bytes,
// by construction. The scan moves the FULL form only — the rise above the
// bare thinking line (F.A4) — so the compact form keeps the frozen
// thinking pose wherever it shows while thinking (the ctrl+t row,
// byte-stable per F.A4). The blink moves both forms together at idle: the
// welcome art and the footer morsel are one frame source (F.A5).
func (m *Model) mascotPose(s mascotState, full bool) mascotFrame {
	f, _ := mascotFrameFor(s)
	if !m.anim {
		return f
	}
	switch {
	case s == msThinking && full:
		return mascotScanCycle[m.scanStep%len(mascotScanCycle)]
	case s == msIdle && m.blinkEligible() && m.eyesShut:
		return frameBlink
	}
	return f
}

// animArm returns the tick the pane's current state calls for, arming each
// chain at the transition that starts it — a round's first reasoning (the
// scan) and a turn's end (the blink) — and never doubling one already in
// flight; it also tracks the thinking phase's visible edge, the one place
// the scan cycle resets to its head. Init arms the first blink for a
// freshly opened pane; Update calls this beside its existing re-arm on
// every agent event and when a stream is cut. A chain in flight needs no
// arming: its own handlers re-arm it while the state holds and let it die
// when the state turns. nil with anim off.
func (m *Model) animArm() tea.Cmd {
	// The scan cycle resets on the thinking phase's invisible→visible edge
	// (F.A2: the cycle opens at UP; F.M4: entering msThinking renders the
	// thinking frame) — not at the re-arm below. animArm runs on every
	// EventMsg and on StreamClosedMsg, the only messages that can flip
	// thinkingVisible (round flags change in applyEvent, the turn ends in
	// endTurn/endTurnError, cuts reset in StreamClosedMsg), so no edge is
	// missed; beats while the phase stays live never reset, which is what
	// keeps the cycle advancing instead of pinned at UP. The edge is also
	// what covers the surviving chain: a scan tick still in flight across
	// a fast round boundary leaves scanArmed true and the arm below
	// defers, and only this edge still puts the next phase at UP.
	// turnStartedMsg cannot produce it — thinkingVisible is false on both
	// sides of it. The tracking sits before the anim guard (EnableAnim
	// precedes Init, so anim off never sees an edge; unconditional and
	// cheap either way).
	live := m.thinkingVisible()
	if live && !m.scanPhaseLive {
		m.scanStep = 0 // invisible→visible edge: the phase opens at the cycle head (F.A2, F.M4)
	}
	m.scanPhaseLive = live

	if !m.anim {
		return nil
	}
	switch {
	case m.thinkingVisible():
		if m.scanArmed {
			return nil
		}
		// The phase opened with no chain in flight: start the scan. The
		// cycle's reset lives on the edge above, not here — a surviving
		// chain defers this arm and must still open its phase at UP.
		m.scanArmed = true
		return scanTickCmd()
	case m.blinkEligible():
		if m.blinkArmed || m.eyesShut {
			return nil
		}
		m.blinkArmed = true
		return blinkTickCmd()
	}
	return nil
}
