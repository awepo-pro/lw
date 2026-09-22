// file.go is the ask screen's file key (009 contract §3.2–§3.5): the
// question/answer pair a cleanly answered turn records, the fileability gate
// over the answer's provenance markers, the ctrl+s filing turn, and
// fileMessage, the message that turn runs on. It also holds beginTurn, the
// start path submitInput and ctrl+s share, and the pane's OverlayHelp —
// whose ctrl+s entry is §3.5's, and which moved here with the feature
// because ask.go sits at its line cap.
package ask

import (
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/ui"
)

// fileHint is the kindStatus line a fileable turn's scrollback ends with
// (009 contract §3.2).
const fileHint = "ctrl+s save this answer into the wiki"

// fileEcho is the user entry a filing turn echoes before it starts (009
// contract §3.3).
const fileEcho = "save the last answer into the wiki"

// fileableRe is the fileability gate (009 contract §3.2): the answer must
// cite at least one vault source — a raw source or a wiki page — inline in
// its text. A multi-line answer cites if any line does; the character class
// keeps one marker from spanning lines.
var fileableRe = regexp.MustCompile(`\^\[(raw|wiki)/[^\]\n]+\]`)

// answerCapture is the recorded question and answer of the last turn that
// ended cleanly (DoneEv, not max_rounds) — the pair ctrl+s files. filing
// marks the pair as a filing turn's own answer, which is never fileable
// (009 contract §3.4): it is what makes a second ctrl+s answer with the
// unsourced refusal instead of filing the same answer twice.
type answerCapture struct {
	set      bool
	question string
	answer   string
	filing   bool
}

// recordLastAnswer captures the scrollback's last question/answer pair (009
// contract §3.2): the question is the last kindUser entry's text, the
// answer the kindAssistant entries after it joined with a blank line. A
// turn with no user entry at all — a stream a caller drove through
// StreamMsg, never a pane submit — still records: the question is "" and
// the answer is every assistant entry, because the gate (§3.2) runs on the
// answer alone. Consumes the running turn's filing marker, so the flag can
// only ever describe the turn whose answer is being recorded.
func (m *Model) recordLastAnswer() {
	wasFiling := m.filingTurn
	m.filingTurn = false

	q := -1
	for i := len(m.entries) - 1; i >= 0; i-- {
		if m.entries[i].kind == kindUser {
			q = i
			break
		}
	}
	if q < 0 {
		var parts []string
		for _, e := range m.entries {
			if e.kind == kindAssistant {
				parts = append(parts, e.text)
			}
		}
		m.last = answerCapture{set: true, answer: strings.Join(parts, "\n\n"), filing: wasFiling}
		return
	}
	var parts []string
	for _, e := range m.entries[q+1:] {
		if e.kind == kindAssistant {
			parts = append(parts, e.text)
		}
	}
	m.last = answerCapture{
		set:      true,
		question: m.entries[q].text,
		answer:   strings.Join(parts, "\n\n"),
		filing:   wasFiling,
	}
}

// forgetLastAnswer clears the recorded pair: an ErrorEv or a max_rounds
// turn leaves nothing fileable behind (009 contract §3.2) — except when the
// dying turn is a filing turn (C-908): then the pair it was filing is kept,
// so ctrl+s retries it, and only the filing marker clears. A turn cancelled
// before its terminal line never reaches this at all: it clears only its
// filing marker (ask.go's StreamClosedMsg), and the previous pair stays
// fileable.
func (m *Model) forgetLastAnswer() {
	if m.filingTurn {
		m.filingTurn = false
		return
	}
	m.last = answerCapture{}
	m.filingTurn = false
}

// lastAnswerFileable reports whether ctrl+s may file what is recorded: an
// answer is recorded, it is not a filing turn's own, and it cites a vault
// source (009 contract §3.2, §3.4).
func (m *Model) lastAnswerFileable() bool {
	return m.last.set && !m.last.filing && fileableRe.MatchString(m.last.answer)
}

// appendFileHint adds the hint after a fileable turn's terminal line — and
// after the kept hint, when one is owed (009 contract §3.2's order). A
// no-op whenever the recorded pair is not fileable.
func (m *Model) appendFileHint() {
	if !m.lastAnswerFileable() {
		return
	}
	m.appendStatus(fileHint)
}

// fileKey handles ctrl+s (009 contract §3.3), in rule order: a running turn
// refuses with submit's own string; nothing recorded, an already-filed
// answer (rule 3a) and an unsourced answer each say so; otherwise one
// filing turn starts on fileMessage's text, over the candidates the vault's
// index returns for the question — read here, on the Update thread, while
// the pane owns the model.
func (m *Model) fileKey() tea.Cmd {
	if m.turnActive {
		m.appendStatus("a turn is already running — submit refused, not queued")
		return nil
	}
	if !m.last.set {
		m.appendStatus("nothing to save yet: ask a question first")
		return nil
	}
	if m.last.filing {
		// Rule 3a (C-909): a filing turn's own answer is never fileable
		// (009 contract §3.4) — it has already been filed, and the honest
		// answer names that instead of claiming it cites no source.
		m.appendStatus("this answer was already saved — review it with ctrl+r")
		return nil
	}
	if !m.lastAnswerFileable() {
		m.appendStatus("this answer cites no vault source; nothing to save")
		return nil
	}
	msg := fileMessage(m.last.question, m.last.answer, m.queryCandidates(m.last.question))
	cmd := m.beginTurn(fileEcho, msg)
	if cmd != nil {
		// The filing turn really started — beginTurn returns nil only for
		// the nil-agent degrade — so its own answer will record as a filing
		// turn's, never fileable (009 contract §3.4).
		m.filingTurn = true
	}
	return cmd
}

// queryCandidates returns the existing query pages that may already answer
// question — at most three, in Search order (009 contract §3.3). The index
// is copy-on-write (008 A-802), so the read is safe beside a turn's tool
// calls; a nil engine or index means no candidates.
func (m *Model) queryCandidates(question string) []index.Hit {
	if m.deps.Engine == nil {
		return nil
	}
	ix := m.deps.Engine.Index()
	if ix == nil {
		return nil
	}
	return ix.Search(question, index.Options{Type: "query", Limit: 3})
}

// beginTurn is the shared tail of submitInput and the ctrl+s filing turn
// (009 contract §3.3): the nil-agent degrade, the synchronous changeset
// read, the tail re-attach, the echo and the turn bookkeeping, so both
// entry points set m.back, turnActive and sessionID identically. echo is
// the user entry the transcript shows; msg is what the agent receives —
// the submitted text for submitInput, fileMessage's for ctrl+s.
func (m *Model) beginTurn(echo, msg string) tea.Cmd {
	if m.deps.Agent == nil {
		m.echoUser(echo)
		m.appendStatus(noAgentStatus)
		return nil
	}

	sessionID := ""
	if m.deps.Engine != nil {
		if cs, err := m.deps.Engine.Current(); err == nil {
			sessionID = cs.ID
		}
	}

	m.back = 0
	m.echoUser(echo)
	m.turnActive = true
	// 022 T2: a new turn counts its thinking from zero — the previous
	// turn's reasoning count and tail never bleed into this one's.
	m.resetReasoningTurn()
	// "" until runTurn resolves it and reports back via turnStartedMsg
	// (C-124/D-DH) — a changeset already open above is known synchronously,
	// same as before.
	m.sessionID = sessionID
	return m.startTurn(sessionID, m.convID, msg)
}

// fileMessage renders the message a filing turn runs on, byte for byte as
// 009 contract §3.4: the instruction, the recorded question, the recorded
// answer, then the existing query pages that may already answer it — one
// `- <path> — <title>` line per candidate, in Search order, or the single
// none-found line when the search came back empty. No trailing newline.
func fileMessage(question, answer string, hits []index.Hit) string {
	var b strings.Builder
	b.WriteString("File this answer as a query page.\n\nQuestion:\n")
	b.WriteString(question)
	b.WriteString("\n\nAnswer:\n")
	b.WriteString(answer)
	b.WriteString("\n\nExisting query pages that may already answer it:")
	if len(hits) == 0 {
		b.WriteString(" none found.")
		return b.String()
	}
	for _, h := range hits {
		b.WriteString("\n- " + h.Path + " — " + h.Title)
	}
	return b.String()
}

// OverlayHelp implements ui.OverlayHelper (contract §5): Ask's section of
// the `?` overlay. There is no `esc cancel turn` entry — Ask binds no esc
// key today (s2-screens.md T08: "only if bound today"). The ctrl+s entry is
// 009 contract §3.5's; the footer list is unchanged.
func (m *Model) OverlayHelp() (string, []ui.HelpEntry) {
	return "Ask", []ui.HelpEntry{
		{Key: "enter", Desc: "send"},
		{Key: "↑/↓", Desc: "select tool call"},
		{Key: "ctrl+r", Desc: "open review"},
		{Key: "ctrl+s", Desc: "save last answer into the wiki"},
		{Key: "ctrl+p", Desc: "show/hide sources"}, // 027 T2
	}
}
