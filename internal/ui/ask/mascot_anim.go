// mascot_anim.go holds 023's mascot motion (amendment A-023-1, which
// reverses 016 F.M5's fully-static rule through the logged amendment
// path): the thinking eye-scan, the idle blink, and the tea.Tick chains
// that drive them — plus 025 F.W2/F.W3's wait blink, the waiting pose's
// idle↔blink alternation on its own beat and its own flags, and 026
// F.C2's count chain, the counted sending row's one-second beat. This
// file is the only place in the package a clock may live, and the anim
// runs on Update's thread — the ticks only sleep and deliver, the
// handlers below decide by the pane's own state whether to act and
// re-arm, and a chain stops by not re-issuing. Nothing here re-derives
// pane state: the same mascotState/round flags the art reads (mascot.go,
// state.go) decide every beat.
package ask

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// The anim's clock (F.A2): the eyes scan while the model thinks, and an
// idle pane blinks every blinkEvery, holding the shut frame one blinkHold.
// 025 F.W3 adds the waiting pane's beat: waitBlinkEvery, quicker than the
// idle blink — a pane whose provider has not answered is the one state
// that must not sit frozen — with the SAME shut-hold as the idle blink
// (blinkHold, reused, not duplicated). 026 F.C2 adds the count chain's
// beat: sendCountEvery, the counted sending row's one-second step (F.C1's
// suffix on 025 F.W4's line).
const (
	scanEvery      = 200 * time.Millisecond
	blinkEvery     = 4 * time.Second
	blinkHold      = 120 * time.Millisecond
	waitBlinkEvery = 1200 * time.Millisecond
	sendCountEvery = 1 * time.Second // 026 F.C2
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
// 120ms hold that opens the eyes again. 025 F.W3 adds the wait chain's
// two: the every-1.2s open→shut edge and its own 120ms hold — a separate
// pair, not the blink's, so a hold in flight across the idle↔waiting
// boundary can never be mistaken for the other chain's (each handler
// clears only its own flag). 026 F.C2 adds the count chain's beat: the
// one sendCountMsg per second, stamped with the generation of the window
// it was minted for. Each is constructed only by the tea.Tick command
// beside it, so a test can feed one to Update without sleeping through a
// real beat (the reloadTickMsg shape, internal/ui/reload.go).
type (
	scanTickMsg  struct{}
	blinkTickMsg struct{}
	blinkOpenMsg struct{}
	waitTickMsg  struct{} // 025 F.W3
	waitOpenMsg  struct{} // 025 F.W3
	sendCountMsg struct { // 026 F.C2
		gen int
	}
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

func waitTickCmd() tea.Cmd {
	return tea.Tick(waitBlinkEvery, func(time.Time) tea.Msg { return waitTickMsg{} })
}

// waitHoldCmd is the wait blink's shut-hold: blinkHold's duration reused
// (F.W3), delivering waitOpenMsg so the wait chain's own handler reopens
// the eyes.
func waitHoldCmd() tea.Cmd {
	return tea.Tick(blinkHold, func(time.Time) tea.Msg { return waitOpenMsg{} })
}

// sendCountCmd mints the count chain's beat (026 F.C2): one
// sendCountEvery, carrying the generation of the window it was minted
// for — a beat still in flight when that window closes (or the next one
// opens) is stale by its stamp, and the handler drops it (F.C3).
func sendCountCmd(gen int) tea.Cmd {
	return tea.Tick(sendCountEvery, func(time.Time) tea.Msg { return sendCountMsg{gen} })
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

// handleWaitTick is Update's waitTickMsg case (025 F.W3/F.W5): while the
// pane waits on the provider it shuts the wait pose's eyes for one
// blinkHold and re-arms; anything else — the first reasoning delta, the
// first answer token, a tool call, the turn's end, a cut stream — stops
// the chain by not re-issuing.
func (m *Model) handleWaitTick() tea.Cmd {
	m.waitArmed = false
	if !m.anim || !m.waitingVisible() {
		return nil
	}
	m.waitShut = true
	return waitHoldCmd()
}

// handleWaitOpen is Update's waitOpenMsg case (025 F.W3): the hold is
// over, the wait pose's eyes open, and the every-1.2s tick re-arms while
// the pane still waits.
func (m *Model) handleWaitOpen() tea.Cmd {
	m.waitShut = false
	if !m.anim || !m.waitingVisible() {
		return nil
	}
	m.waitArmed = true
	return waitTickCmd()
}

// handleSendCount is Update's sendCountMsg case (026 F.C2): one beat, one
// sendCountEvery onto the waiting window's counter, re-arming only while
// the anim is on, the pane still waits AND the beat carries the current
// window's generation. Anything else — the first reasoning byte, a tool
// call, the turn's end, a cut stream, or a beat minted for a window that
// has since closed — stops the chain by not re-issuing, without touching
// the count. The generation is F.C3's by-construction closure of the 023
// surviving-chain defect class: the edge that opens a window bumps
// sendGen (animArm), so every beat minted before it is dead on arrival,
// even mid-window where the visibility guards alone would still pass it.
func (m *Model) handleSendCount(msg sendCountMsg) tea.Cmd {
	if !m.anim || !m.waitingVisible() || msg.gen != m.sendGen {
		return nil
	}
	m.sendSecs++
	return sendCountCmd(m.sendGen)
}

// mascotPose selects the frame the pane draws for state s — the state
// table's frame (mascotFrameFor) with 023's motion overlaid, and only when
// the anim switch is on: with anim false every render is today's bytes,
// by construction. The scan moves the FULL form only — the rise above the
// bare thinking line (F.A4) — so the compact form keeps the frozen
// thinking pose wherever it shows while thinking (the ctrl+t row,
// byte-stable per F.A4). The blink moves both forms together at idle: the
// welcome art and the footer morsel are one frame source (F.A5). The wait
// pose (025 F.W2) alternates the SAME two frames on its own flag — and on
// the COMPACT form only, that being the form the sending row renders; the
// idle chain's eyesShut never leaks into it, and msWaiting's full form
// keeps the frozen idle rows outright (F.W2: waiting renders the compact
// form only, the rise keeps meaning thinking).
func (m *Model) mascotPose(s mascotState, full bool) mascotFrame {
	f, _ := mascotFrameFor(s)
	if !m.anim {
		return f
	}
	switch {
	case s == msThinking && full:
		return mascotScanCycle[m.scanStep%len(mascotScanCycle)]
	case s == msWaiting && !full && m.waitShut:
		return frameBlink
	case s == msIdle && m.blinkEligible() && m.eyesShut:
		return frameBlink
	}
	return f
}

// animArm returns the tick the pane's current state calls for, arming each
// chain at the transition that starts it — a round's first reasoning (the
// scan), the turn's submit (the wait blink, 025 F.W3: handleKey's enter
// arm and turnStartedMsg's, covering the ctrl+s filing turn that begins
// through beginTurn alone) and a turn's end (the blink) — and never
// doubling one already in flight; it also tracks the thinking phase's
// visible edge, the one place the scan cycle resets to its head, and —
// 026 F.C3, the same idiom — the waiting window's, the one place the
// count restarts under a fresh generation. Init arms the first blink for
// a freshly opened pane; Update calls this beside its existing re-arm on
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

	// 026 F.C3: the waiting window's own edge, the scan edge's idiom one
	// state over — on waitingVisible's invisible→visible edge the count
	// restarts from zero under a fresh generation, so every round's wait
	// counts its own seconds (resetReasoningRound re-opens the window for
	// each tool round) and a beat still in flight across the boundary is
	// stale on arrival. The same messages flip this predicate as flip
	// thinkingVisible — every EventMsg, StreamClosedMsg, turnStartedMsg
	// and the key arms — so no edge is missed here either; the tracking
	// sits before the anim guard like the scan's (unconditional and cheap
	// either way).
	waitLive := m.waitingVisible()
	waitEdge := waitLive && !m.waitPhaseLive
	if waitEdge {
		m.sendSecs = 0 // the window's count opens at zero, every round (F.C3)
		m.sendGen++    // beats minted for the old window are now stale (F.C3)
	}
	m.waitPhaseLive = waitLive

	if !m.anim {
		return nil
	}
	// 026 F.C3: the edge arms the count's first beat beside whatever
	// motion chain the state calls for. A waiting edge can only coincide
	// with the waiting case below — thinkingVisible and waitingVisible are
	// mutually exclusive and blinkEligible needs the turn over — and Batch
	// with a nil count is that case's own command, unchanged.
	var count tea.Cmd
	if waitEdge {
		count = sendCountCmd(m.sendGen)
	}
	switch {
	case m.thinkingVisible():
		if m.scanArmed {
			return count
		}
		// The phase opened with no chain in flight: start the scan. The
		// cycle's reset lives on the edge above, not here — a surviving
		// chain defers this arm and must still open its phase at UP.
		m.scanArmed = true
		return tea.Batch(count, scanTickCmd())
	case m.waitingVisible():
		// 025 F.W3/F.W5: the wait chain arms at the submit and re-arms only
		// while the pane waits; a hold in flight (waitShut) defers to its
		// own waitOpenMsg, like the blink's. The count chain needs no
		// deferral beside it — its single-file rule is the generation
		// itself, and its edge can only fire once per window.
		if m.waitArmed || m.waitShut {
			return count
		}
		m.waitArmed = true
		return tea.Batch(count, waitTickCmd())
	case m.blinkEligible():
		if m.blinkArmed || m.eyesShut {
			return count
		}
		m.blinkArmed = true
		return tea.Batch(count, blinkTickCmd())
	}
	return count
}
