// ask.go implements the ask screen itself (backbone §12 ui.Pane;
// s4-tui.md S4-T6): an input box plus a scrollback, driven by whatever
// <-chan agent.Event a StreamMsg (stream.go) hands the pane.
//
// Since S5-T5 the pane also starts those turns: when ui.Deps.Agent is
// non-nil, submitting a question launches one real agent turn (backbone
// §9, C-105) whose events reach this pane through the same pump as any
// scripted stream. When Deps.Agent is nil — the config did not load, say —
// submitting refuses with a visible status line instead of dying, and
// browse/review/lint/log keep working without a provider (s5-agent-loop.md
// S5-T5).
//
// Since C-124/D-DH (S6) a turn no longer refuses for want of an open
// changeset: when none is open at submit, runTurn (stream.go) opens one
// itself, the same seam `lw ingest` already uses, and rejects it afterwards
// if the turn staged nothing — see stream.go's runTurn/resolveTurnChangeset.
package ask

import (
	"context"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/ui"
)

// toolArgsPreviewRunes and toolResultPreviewRunes bound a collapsed tool
// line's inline previews of its args and result — the full text is only
// ever shown on expand (s4-tui.md S4-T6).
const (
	toolArgsPreviewRunes   = 48
	toolResultPreviewRunes = 60
)

// Model is the ask screen (backbone §12 ui.Pane).
type Model struct {
	deps  ui.Deps
	theme ui.Theme // C-81: a copy, rebuilt locally on tea.BackgroundColorMsg

	entries    []entry
	toolIndex  map[string]int // agent event ID -> index into entries
	selected   int            // index into entries the next enter expands/collapses; -1 = none
	turnActive bool

	input string // the box's current, unsent text

	// sessionID is the changeset id the running (or most recent) turn ran
	// under — a session is keyed by its changeset (backbone §9, C-102) —
	// and "" when no turn of this pane's has a session to close. It is what
	// changesetGone archives when the changeset is committed or rejected.
	sessionID string
	// cancel aborts the running turn's context; nil until startTurn runs.
	cancel context.CancelFunc

	ch <-chan agent.Event // installed by StreamMsg; nil = no stream to re-arm
}

var _ ui.Pane = (*Model)(nil)

// New constructs the ask screen (backbone §12). It captures a copy of
// d.Theme and nothing else — there is no vault or engine state to load at
// construction, and no channel to listen on until a turn starts
// (s4-tui.md S4-T6).
func New(d ui.Deps) ui.Pane {
	return &Model{deps: d, theme: d.Theme, selected: -1}
}

// Title returns the pane's name for the shell's tab bar (backbone §12).
func (m *Model) Title() string { return "Ask" }

// Help returns the ask screen's key bindings (backbone §12). None of them
// have a ui.KeyMap field of their own — s4-tui.md S4-T6 adds nothing to
// the shell's KeyMap — so they are described with ad-hoc bindings for
// display purposes only, the same pattern browse.go uses for its own
// screen-local keys.
func (m *Model) Help() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send / expand tool call")),
		key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑/↓", "select tool call")),
		key.NewBinding(key.WithKeys("backspace"), key.WithHelp("backspace", "delete")),
		key.NewBinding(key.WithKeys("ctrl+r"), key.WithHelp("ctrl+r", "review")),
	}
}

// Init has nothing to load: the theme is already a copy of d.Theme, and
// there is no channel to Listen on until a turn starts (s4-tui.md S4-T6).
func (m *Model) Init() tea.Cmd { return nil }

// Update handles the shell's background-colour broadcast, this screen's
// keymap, the event pump's own messages, the session-closed outcome and the
// shell's ui.StageChangedMsg broadcast (backbone §12 C-80: matches
// tea.KeyPressMsg, never tea.KeyMsg).
func (m *Model) Update(msg tea.Msg) (ui.Pane, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.theme = m.theme.WithDark(msg.IsDark())
		return m, nil

	case StreamMsg:
		// repair-1: the exported injection point for a caller that holds
		// only a ui.Pane. Installing the channel here, rather than
		// requiring it at construction, is what lets Listen's own re-arm
		// (below) keep working after every event with no concrete method
		// ever exposed.
		m.ch = msg.Ch
		return m, m.rearm()

	case turnStartedMsg:
		// C-124/D-DH: runTurn's own report of the changeset it resolved to
		// run this turn under (or a failure that ends the turn before
		// Agent.Send ever ran) — delivered before any agent.Event so
		// sessionID and m.ch are never behind the turn they track.
		if msg.err != nil {
			if m.cancel != nil {
				m.cancel()
				m.cancel = nil
			}
			m.endTurnError(msg.err.Error())
			return m, nil
		}
		m.sessionID = msg.sessionID
		m.ch = msg.ch
		return m, m.rearm()

	case EventMsg:
		extra := m.applyEvent(msg.Ev)
		return m, tea.Batch(m.rearm(), extra)

	case StreamClosedMsg:
		m.ch = nil
		m.turnActive = false
		// The turn is over either way, so its context has nothing left to
		// cancel; releasing it here (rather than waiting for a later
		// changesetGone) is what keeps one turn's cancel from being mistaken
		// for the next turn's.
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
		return m, nil

	case sessionClosedMsg:
		if msg.err != nil {
			m.appendStatus("closing the session failed: " + msg.err.Error())
		}
		return m, nil

	case ui.StageChangedMsg:
		// Broadcast by the shell to every pane. The populated form is the
		// pane's own StageEv coming back (or review reporting a change); the
		// empty form is how review says "no changeset is open any more" —
		// Commit and Reject both emit it — and that is when this pane's
		// session has to close (backbone §9; s5-agent-loop.md S5-T5).
		return m, m.changesetGone(msg.ChangesetID)

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// handleKey dispatches one tea.KeyPressMsg: Ctrl-R switches to Review
// (s4-tui.md S4-T6 pinned item 3), enter sends the typed message or
// expands the selected tool call, and everything else edits the input
// box.
func (m *Model) handleKey(msg tea.KeyPressMsg) (ui.Pane, tea.Cmd) {
	switch msg.String() {
	case "ctrl+r":
		return m, func() tea.Msg { return ui.SwitchScreenMsg{To: ui.ScreenReview} }
	case "up":
		m.moveSelection(-1)
		return m, nil
	case "down":
		m.moveSelection(1)
		return m, nil
	case "enter":
		if m.input != "" {
			return m, m.submitInput()
		}
		m.toggleSelectedExpand()
		return m, nil
	case "backspace":
		m.deleteInputRune()
		return m, nil
	}

	// A plain printable key (no ctrl/alt) types into the input box — the
	// same rule browse.go's finder uses for its own text field.
	if msg.Text != "" && msg.Mod&^tea.ModShift == 0 {
		m.input += msg.Text
	}
	return m, nil
}

// submitInput handles enter on a non-empty input box: it echoes the typed
// text into the scrollback, clears the box, and — when there is an agent to
// answer — starts one real agent turn. The turn itself runs in startTurn's
// goroutine and reaches this pane only as tea.Cmds, so Update returns
// immediately no matter how long the model takes (backbone §9, C-105;
// s5-agent-loop.md S5-T5 "never block Update").
//
// Two conditions refuse the submit instead, each as a visible status line
// rather than a dead input or a silent drop:
//
//   - a turn is already running. Refused, NOT queued — the documented
//     choice: a queued question would fire at the agent mid-turn, and the
//     answer that comes back could not be attributed to either question.
//     The typed text stays in the box so nothing the curator wrote is lost.
//   - Deps.Agent is nil. The config did not load, so there is no provider;
//     ask says so and every other screen carries on (S5-T5's degrade
//     requirement).
//
// A third refusal used to fire here — "no open changeset" — because a
// session is keyed by its changeset (backbone §9, C-102) and a fresh
// vault's first question had nowhere to run. C-124/D-DH removed it: when
// this synchronous, bounded read finds nothing open, the turn opens its
// own changeset itself, inside runTurn's goroutine where the filesystem and
// lint work that entails belongs (backbone §9, C-105) — see stream.go's
// runTurn and resolveTurnChangeset.
//
// The engine read this performs when a changeset IS already open —
// Engine.Current, one small directory scan plus one small JSON file — is
// bounded and local, the same order of work review's own commit path
// already does in Update; finding one this way (rather than waiting on the
// turn to report back) is what lets this pane's bookkeeping (m.sessionID)
// stay synchronous for that case, exactly as before D-DH.
func (m *Model) submitInput() tea.Cmd {
	msg := m.input

	if m.turnActive {
		m.appendStatus("a turn is already running — submit refused, not queued")
		return nil
	}

	if m.deps.Agent == nil {
		m.echoUser(msg)
		m.appendStatus("no agent is configured (config did not load) — ask is off; browse, review, lint and log still work")
		return nil
	}

	sessionID := ""
	if m.deps.Engine != nil {
		if cs, err := m.deps.Engine.Current(); err == nil {
			sessionID = cs.ID
		}
	}

	m.echoUser(msg)
	m.turnActive = true
	// "" until runTurn resolves it and reports back via turnStartedMsg
	// (C-124/D-DH) — a changeset already open above is known synchronously,
	// same as before.
	m.sessionID = sessionID
	return m.startTurn(sessionID, msg)
}

// echoUser appends text as a kindUser scrollback entry and clears the input
// box — the submit path's one shared "the curator said this" step.
func (m *Model) echoUser(text string) {
	m.entries = append(m.entries, entry{kind: kindUser, text: text})
	m.input = ""
}

// appendStatus appends one kindStatus line: a pane-local notice (a refused
// submit, a failed Close) rather than a turn boundary, which endTurn and
// endTurnError own.
func (m *Model) appendStatus(text string) {
	m.entries = append(m.entries, entry{kind: kindStatus, text: text})
}

// deleteInputRune removes the last rune of the input box, if any.
func (m *Model) deleteInputRune() {
	r := []rune(m.input)
	if len(r) == 0 {
		return
	}
	m.input = string(r[:len(r)-1])
}

// View renders the ask screen at exactly w by h (backbone §12): the
// scrollback, tail-scrolled so the most recent lines are visible, above a
// one-line input box.
func (m *Model) View(w, h int) string {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}

	scrollH := h - 1
	if scrollH < 0 {
		scrollH = 0
	}

	lines := m.renderScrollback(w)
	visible := fitLines(strings.Join(tailLines(lines, scrollH), "\n"), w, scrollH)

	rows := append([]string{}, visible...)
	rows = append(rows, fitLine(m.theme.Accent.Render("> ")+m.input, w))
	return strings.Join(rows, "\n")
}

// renderScrollback renders every entry, in order, to a flat list of
// display lines at width w.
func (m *Model) renderScrollback(w int) []string {
	var lines []string
	for i, e := range m.entries {
		lines = append(lines, m.renderEntry(e, i == m.selected, w)...)
	}
	return lines
}

// renderEntry renders one scrollback entry to one or more display lines.
func (m *Model) renderEntry(e entry, selected bool, w int) []string {
	switch e.kind {
	case kindUser:
		return renderPrefixed("you: ", e.text, w, m.theme.Base)
	case kindAssistant:
		return renderPrefixed("assistant: ", e.text, w, m.theme.Base)
	case kindStatus:
		return []string{m.theme.Muted.Render(fitLine("— "+e.text+" —", w))}
	case kindError:
		return renderPrefixed("error: ", e.text, w, m.theme.Bad)
	case kindTool:
		return renderToolEntry(m.theme, e.tool, selected, w)
	}
	return nil
}

// renderPrefixed word-wraps text to fit alongside prefix at width w,
// indenting every continuation line to align under the first.
func renderPrefixed(prefix, text string, w int, style lipgloss.Style) []string {
	avail := w - lipgloss.Width(prefix)
	if avail < 1 {
		avail = 1
	}
	indent := strings.Repeat(" ", lipgloss.Width(prefix))
	wrapped := wrapText(text, avail)
	lines := make([]string, len(wrapped))
	for i, l := range wrapped {
		p := prefix
		if i > 0 {
			p = indent
		}
		lines[i] = style.Render(fitLine(p+l, w))
	}
	return lines
}

// renderToolEntry renders one ToolCallEv/ToolResEv pair as a single
// collapsed line (s4-tui.md S4-T6: "▸ wiki.search {\"q\":\"…\"}"), or, when
// expanded, that line followed by the full args and result. An IsError
// result is visibly marked in both states.
func renderToolEntry(theme ui.Theme, tc *toolCall, selected bool, w int) []string {
	marker := "▸"
	if tc.expanded {
		marker = "▾"
	}
	head := marker + " " + tc.name + " " + truncateRunes(tc.args, toolArgsPreviewRunes)
	switch {
	case tc.resolved && tc.isError:
		head += "  ✗ " + truncateRunes(singleLine(tc.content), toolResultPreviewRunes)
	case tc.resolved:
		head += "  → " + truncateRunes(singleLine(tc.content), toolResultPreviewRunes)
	default:
		head += "  …"
	}

	style := theme.Base
	if tc.resolved && tc.isError {
		style = theme.Bad
	}
	if selected {
		style = theme.Selected
	}
	lines := []string{style.Render(fitLine(head, w))}

	if !tc.expanded {
		return lines
	}

	lines = append(lines, theme.Muted.Render(fitLine("    args: "+tc.args, w)))
	switch {
	case !tc.resolved:
		lines = append(lines, theme.Muted.Render(fitLine("    (waiting for result…)", w)))
	default:
		resultStyle := theme.Muted
		if tc.isError {
			resultStyle = theme.Bad
		}
		for _, l := range wrapText(tc.content, maxInt(1, w-4)) {
			lines = append(lines, resultStyle.Render(fitLine("    "+l, w)))
		}
	}
	return lines
}

// maxInt returns the larger of a and b.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// tailLines returns the last n elements of lines, or all of them if there
// are n or fewer — how the scrollback keeps the most recent activity in
// view rather than the oldest.
func tailLines(lines []string, n int) []string {
	if n <= 0 {
		return nil
	}
	if len(lines) <= n {
		return lines
	}
	return lines[len(lines)-n:]
}

// wrapText word-wraps s to width w, breaking a single word longer than w
// on rune boundaries. It never returns an empty slice, so an empty entry
// still occupies exactly one blank line.
func wrapText(s string, w int) []string {
	if w < 1 {
		w = 1
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		out = append(out, wrapParagraph(para, w)...)
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

// wrapParagraph word-wraps one line (no "\n") of text to width w.
func wrapParagraph(s string, w int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{""}
	}
	var lines []string
	cur := ""
	for _, word := range words {
		for len([]rune(word)) > w {
			if cur != "" {
				lines = append(lines, cur)
				cur = ""
			}
			r := []rune(word)
			lines = append(lines, string(r[:w]))
			word = string(r[w:])
		}
		switch {
		case cur == "":
			cur = word
		case len([]rune(cur))+1+len([]rune(word)) <= w:
			cur += " " + word
		default:
			lines = append(lines, cur)
			cur = word
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// fitLine returns s clipped or padded to exactly w display columns
// (backbone §12: "no line wider than w"). Duplicated from
// internal/ui/layout.go, which is unexported and not something a screen
// imports (same pattern as internal/ui/review/diffview.go).
func fitLine(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = lipgloss.NewStyle().MaxWidth(w).Render(s)
	if cur := lipgloss.Width(s); cur < w {
		s += strings.Repeat(" ", w-cur)
	}
	return s
}

// fitLines splits s on "\n" and returns exactly n lines, each fitLine'd to
// w: lines beyond n are dropped, missing ones come back blank.
func fitLines(s string, w, n int) []string {
	if n < 0 {
		n = 0
	}
	src := strings.Split(s, "\n")
	out := make([]string, n)
	for i := range out {
		var line string
		if i < len(src) {
			line = src[i]
		}
		out[i] = fitLine(line, w)
	}
	return out
}
