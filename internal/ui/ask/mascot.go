// mascot.go is the ask pane's mascot (workflow 016): a clawd-style pixel
// creature drawn from terminal half-block cells, one theme accent. 023
// (amendment A-023-1) put its motion behind the anim flag in
// mascot_anim.go; this file stays what it always was — the frozen art
// (plan 016 §2/§3, 023 F.A1's additions included), the state table, and
// the frame selection, with never a clock nor a tick of its own. It
// expresses the pane's state but is never a second source of it: every
// state reads fields the turn state machine and 022's thinking line
// already maintain (state.go), and the frames are byte-exact,
// single-width cells only.
package ask

import (
	lipgloss "charm.land/lipgloss/v2"
)

// mascotFrame selects one pose of the art: facing the curator, looking up
// while the model thinks, the 023 eye-scan darts, or mid-blink. Error is
// not a pose — it is the idle art rendered reversed (plan 016 §2: "no new
// art").
type mascotFrame int

const (
	frameIdle      mascotFrame = iota
	frameThinking              // the top row's ▀ eyes become ▄
	frameScanLeft              // 023 F.A1: the eyes dart left
	frameScanRight             // 023 F.A1: the eyes dart right
	frameBlink                 // 023 F.A1: the eyes shut
)

// mascotFull is the full three-row form (9 cells per row) that greets an
// empty transcript and rises above the thinking line (023 F.A4);
// mascotCompact is the one-row form beside the thinking status line and at
// the footer's left edge. Both are indexed by mascotFrame, and the compact
// form is the full form's top row — the eyes are the whole vocabulary, so
// the two sizes cannot drift apart.
var (
	mascotFull = [5][3]string{
		frameIdle:      {" ██▀██▀█ ", "▀███████▀", " ▀██▀▀██ "},
		frameThinking:  {" ██▄██▄█ ", "▀███████▀", " ▀██▀▀██ "},
		frameScanLeft:  {" █▀██▀██ ", "▀███████▀", " ▀██▀▀██ "},
		frameScanRight: {" ███▀██▀ ", "▀███████▀", " ▀██▀▀██ "},
		frameBlink:     {" ███████ ", "▀███████▀", " ▀██▀▀██ "},
	}
	mascotCompact = [5]string{
		frameIdle:      "██▀██▀█ ",
		frameThinking:  "██▄██▄█ ",
		frameScanLeft:  "█▀██▀██ ",
		frameScanRight: "███▀██▀ ",
		frameBlink:     "███████ ",
	}
)

// mascotState is the pane state the mascot expresses (plan 016 §4).
type mascotState int

const (
	msIdle     mascotState = iota // pane open, turn done, or between rounds
	msThinking                    // the current round is thinking (022's line shows)
	msError                       // the last finished turn errored
)

// mascotState reads the pane's existing state — 022's thinking rule and
// the turn flags — and never re-derives any of it. Answering and a tool
// round are deliberately still (the streamed text is the performance, plan
// 016 §7), so they fall out as the idle art without states of their own;
// a new turn's reset likewise needs no code here, because turnActive masks
// a stale turnErrored for the whole turn it belongs to.
func (m *Model) mascotState() mascotState {
	switch {
	case m.thinkingVisible():
		return msThinking
	case m.turnActive: // answering, or waiting on a tool round
		return msIdle
	case m.turnErrored:
		return msError
	}
	return msIdle
}

// mascotFrameFor maps a state to its art: the frame to draw and whether it
// renders reversed. Only the error state reverses, and it reuses the idle
// frame to do it.
func mascotFrameFor(s mascotState) (f mascotFrame, reversed bool) {
	switch s {
	case msThinking:
		return frameThinking, false
	case msError:
		return frameIdle, true
	}
	return frameIdle, false
}

// mascotStyle is the one colour the mascot draws in — the theme's Accent —
// reversed for the error state. Theme styles only, like every other row
// this pane renders.
func (m *Model) mascotStyle(reversed bool) lipgloss.Style {
	style := m.theme.Accent
	if reversed {
		style = style.Reverse(true)
	}
	return style
}

// renderMascotFull renders the current state's full form, one styled string
// per row. The frames are 9 cells and the panel contract keeps the inner
// width above that at every size the shell can run at; whatever a too-short
// or too-narrow panel cannot hold, Panel's own Pad clamps.
func (m *Model) renderMascotFull() []string {
	s := m.mascotState()
	_, rev := mascotFrameFor(s)
	style := m.mascotStyle(rev)
	rows := mascotFull[m.mascotPose(s, true)]
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = style.Render(r)
	}
	return out
}

// renderMascotCompact renders the current state's one-row form.
func (m *Model) renderMascotCompact() string {
	s := m.mascotState()
	_, rev := mascotFrameFor(s)
	return m.mascotStyle(rev).Render(mascotCompact[m.mascotPose(s, false)])
}

// mascotStatusRow is the thinking status line with the compact form at its
// left (F.M3). The 022 substring `· thinking… (` stays byte-intact — the
// mascot renders beside it, never instead of it (plan 016 §8).
func (m *Model) mascotStatusRow() string {
	return m.renderMascotCompact() + m.theme.Faint.Render(m.thinkingStatusLine())
}

// FooterPrefix implements ui.FooterPrefix (F.M3): the compact form at the
// shell footer's left edge, carried as plain cells plus the pane's own
// style so the shell's row primitive can place it like any binding run.
// While the round is thinking the compact form has moved to the status
// line's own row inside the transcript (mascotStatusRow), so the footer
// keeps its exact shape: the compact form shows in exactly one of the two
// slots at a time. (The empty pane's full-form greeting is plan 016 §5
// candidate A's separate mount, above the intro.)
func (m *Model) FooterPrefix() (string, lipgloss.Style) {
	if m.thinkingVisible() {
		return "", lipgloss.Style{}
	}
	s := m.mascotState()
	_, rev := mascotFrameFor(s)
	return mascotCompact[m.mascotPose(s, false)], m.mascotStyle(rev)
}
