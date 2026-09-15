// ask.go holds the ask screen's ui.Pane itself: the Model, its construction
// and key handling, the shell-interface surface (footer, overlay, text
// capture), and the D10 suggested prompts read off index.md. The rendering
// lives in view.go, the scrollback state machine in state.go, and the
// agent.Event pump and turn lifecycle in stream.go.
package ask

import (
	"regexp"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/ui"
)

// Model is the ask screen (backbone §12 ui.Pane).
type Model struct {
	deps  ui.Deps
	theme ui.Theme // C-81: a copy, rebuilt locally on tea.BackgroundColorMsg

	// prompts are the D10 suggested first questions, read off index.md at
	// New and refreshed on ui.VaultReloadedMsg. They render only while the
	// transcript is empty (view.go's empty state).
	prompts []string

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
	cancel func()

	ch <-chan agent.Event // installed by StreamMsg; nil = no stream to re-arm
}

var _ ui.Pane = (*Model)(nil)
var _ ui.TextCapturer = (*Model)(nil)

// New constructs the ask screen (backbone §12). It captures a copy of
// d.Theme and reads the D10 prompts off index.md — a bounded, local vault
// read, the same order of work review's construction does — falling back to
// the generic prompt set when there is no engine or no readable index.
func New(d ui.Deps) ui.Pane {
	m := &Model{deps: d, theme: d.Theme, selected: -1}
	m.prompts = m.loadPrompts()
	return m
}

// Title returns the pane's name for the shell's tab bar (backbone §12).
func (m *Model) Title() string { return "Ask" }

// footerBindings is the ask footer's own bindings (s2-screens.md T08). No
// `tab screen` and no `q quit`: the shell appends the suffix after this
// list (ORCH-13/D-3T) — tab always, and q only for panes that do not
// capture text, which Ask does (CapturesText), so quitting from here would
// eat questions.
func footerBindings() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
		key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑/↓", "select tool call")),
		key.NewBinding(key.WithKeys("ctrl+r"), key.WithHelp("ctrl+r", "review")),
	}
}

// Help returns the ask screen's key bindings (backbone §12). The shell
// renders the footer from FooterHelp, which returns the same list; Help
// remains for the Pane interface and any caller that wants the raw map.
func (m *Model) Help() []key.Binding { return footerBindings() }

// FooterHelp implements ui.FooterHelper (contract §5): the footer list the
// shell renders for this pane, in display order. The shell appends
// `tab screen` and `? help` after it — and, because Ask captures text, no
// `q quit`.
func (m *Model) FooterHelp() []key.Binding { return footerBindings() }

// OverlayHelp implements ui.OverlayHelper (contract §5): Ask's section of
// the `?` overlay. There is no `esc cancel turn` entry — Ask binds no esc
// key today (s2-screens.md T08: "only if bound today").
func (m *Model) OverlayHelp() (string, []ui.HelpEntry) {
	return "Ask", []ui.HelpEntry{
		{Key: "enter", Desc: "send"},
		{Key: "↑/↓", Desc: "select tool call"},
		{Key: "ctrl+r", Desc: "open review"},
	}
}

// CapturesText implements ui.TextCapturer (contract §5, C27/D-3Q): the
// input box always takes typing, so while Ask is the active screen the
// shell hands every printable key straight here — `q` and `?` type into the
// question instead of quitting the program or opening the keys overlay.
func (m *Model) CapturesText() bool { return true }

// Init has nothing to load: the theme is already a copy of d.Theme and the
// prompts were read at New; there is no channel to Listen on until a turn
// starts (s4-tui.md S4-T6).
func (m *Model) Init() tea.Cmd { return nil }

// Update handles the shell's background-colour broadcast, this screen's
// keymap, the event pump's own messages, the session-closed outcome and the
// shell's broadcasts (backbone §12 C-80: matches tea.KeyPressMsg, never
// tea.KeyMsg).
func (m *Model) Update(msg tea.Msg) (ui.Pane, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.theme = m.theme.WithDark(msg.IsDark())
		return m, nil

	case ui.VaultReloadedMsg:
		// D10: the suggested prompts track the vault, so a re-read (the
		// shell broadcasts this after a commit) refreshes them.
		m.prompts = m.loadPrompts()
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
// Two conditions refuse the submit instead, each as a visible transcript
// entry rather than a dead input or a silent drop:
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

// promptEntryRe matches one index.md bullet D10 turns into a suggested
// prompt: `- [[target]] — description`, with the `|label` form of a
// wikilink allowed (s2-screens.md T08).
var promptEntryRe = regexp.MustCompile(`(?m)^- \[\[([^\]|]+)(?:\|[^\]]*)?\]\] — (.+)$`)

// fallbackPrompts is the D10 prompt set for a vault whose index has fewer
// than two entries (or none at all).
func fallbackPrompts() []string {
	return []string{
		"What does the wiki cover so far?",
		"Which pages were updated most recently?",
		"Which pages rest on a single source?",
	}
}

// promptsFromIndex computes the D10 prompt set from index.md's content.
// The first two entries' descriptions become the personalized prompts (T1,
// T2); anything from the third on is not needed. The frozen grids pin the
// descriptions — not the wikilink targets — as T1/T2: on the mockup vault
// the first two entries are `[[vertex-ai]] — Vertex AI` and
// `[[claude]] — Claude`, and ask-80x24 reads "What does the wiki say about
// Vertex AI?" / "How is Claude related to Vertex AI?".
func promptsFromIndex(src []byte) []string {
	var descs []string
	for _, match := range promptEntryRe.FindAllStringSubmatch(string(src), -1) {
		descs = append(descs, match[2])
		if len(descs) == 2 {
			break
		}
	}
	if len(descs) < 2 {
		return fallbackPrompts()
	}
	return []string{
		"What does the wiki say about " + descs[0] + "?",
		"How is " + descs[1] + " related to " + descs[0] + "?",
		"Which pages rest on a single source?",
	}
}

// loadPrompts reads index.md through the engine's vault (D10) and returns
// the prompt set for it. Every failure — no engine, unreadable vault,
// missing index — degrades to fallbackPrompts; suggested questions are
// never worth an error surface.
func (m *Model) loadPrompts() []string {
	if m.deps.Engine == nil {
		return fallbackPrompts()
	}
	src, err := m.deps.Engine.Vault().Read("index.md")
	if err != nil {
		return fallbackPrompts()
	}
	return promptsFromIndex(src)
}
