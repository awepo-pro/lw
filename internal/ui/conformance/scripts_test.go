package conformance

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// mockupQuestion is mockgen.py's QUESTION (line 592), verbatim: what the
// ask-conversation script types.
const mockupQuestion = "How does calling Claude through Vertex AI differ from the Anthropic API?"

// mockupAnswer is mockgen.py's ANSWER (line 593), verbatim: the scripted
// turn's single TextDelta.
const mockupAnswer = "Both paths call the same Claude weights, so the difference is governance, not model quality. " +
	"The direct API authenticates with static `x-api-key` keys, goes over the public internet to " +
	"`api.anthropic.com`, bills on an Anthropic invoice and gets beta features first. Vertex AI uses " +
	"GCP IAM and service accounts, can keep traffic on the private GCP backbone (VPC-SC / PSC), bills " +
	"against GCP commitments and trails new features by a stated 2–4 weeks. " +
	"See [[anthropic-api-vs-vertex-ai]] and [[claude]]."

// fakeAgentEvents is s1-harness-gate.md T13's six scripted events, in
// order: two tool calls with their results, the answer as one TextDelta,
// and the done event.
func fakeAgentEvents() []agent.Event {
	return []agent.Event{
		agent.ToolCallEv{ID: "c1", Name: "wiki_search", Args: `{"query":"claude vertex ai anthropic api"}`},
		agent.ToolResEv{ID: "c1", Name: "wiki_search",
			Content: "4 pages: anthropic-api-vs-vertex-ai, claude, vertex-ai, model-as-a-service"},
		agent.ToolCallEv{ID: "c2", Name: "wiki_get",
			Args: `{"path":"wiki/comparisons/anthropic-api-vs-vertex-ai.md"}`},
		agent.ToolResEv{ID: "c2", Name: "wiki_get",
			Content: "Anthropic Official API vs Vertex AI · 41 lines"},
		agent.TextDelta{Text: mockupAnswer},
		agent.DoneEv{Reason: "stop", Rounds: 2},
	}
}

// wantsAgent reports whether a grid's view script installs the FakeAgent:
// only the ask-conversation script drives a turn.
func wantsAgent(name string) bool {
	return strings.HasPrefix(name, "ask-conversation-")
}

// maxJPresses is the scripted j loops' press cap.
const maxJPresses = 10

// The review script's target: op3, the private-network-access patch the
// mockup vault stages and the Ops panel's cursor starts on. The vault's
// op ids are part of the frozen setup — the same reason the contract pins
// DropHunk("op4", "h1").
const (
	reviewTargetPanel = "Ops"
	reviewTargetID    = "op3"
	reviewTargetLabel = "private-network-access.md"
	reviewTargetPath  = "wiki/concepts/private-network-access.md"
)

// The browse script's target: the selected comparison page.
const (
	browseTargetLabel = "anthropic-api-vs-vertex-ai"
	browseTargetPath  = "wiki/comparisons/anthropic-api-vs-vertex-ai.md"
)

// runViewScript drives m through its grid's script (s1-harness-gate.md
// T13), after the size and background messages have been delivered.
func runViewScript(t *testing.T, name string, m tea.Model) tea.Model {
	t.Helper()

	switch {
	case strings.HasPrefix(name, "too-small-"):
		return m // no keys: the shell refuses every key but quit below the minimum

	case strings.HasPrefix(name, "ask-conversation-"):
		m = pressKeys(t, m, "tab")
		for _, r := range mockupQuestion {
			m = uitest.Drive(t, m, uitest.Key(string(r)))
		}
		return uitest.Drive(t, m, uitest.Key("enter"))

	case strings.HasPrefix(name, "ask-"):
		return pressKeys(t, m, "tab")

	case strings.HasPrefix(name, "review-preview-"):
		m = driveCursorTo(t, m, reviewTargetPanel, reviewTargetID, reviewTargetLabel, reviewTargetPath)
		return pressKeys(t, m, "p")

	case strings.HasPrefix(name, "keys-"):
		m = driveCursorTo(t, m, reviewTargetPanel, reviewTargetID, reviewTargetLabel, reviewTargetPath)
		return pressKeys(t, m, "?")

	case strings.HasPrefix(name, "review-"):
		return driveCursorTo(t, m, reviewTargetPanel, reviewTargetID, reviewTargetLabel, reviewTargetPath)

	case strings.HasPrefix(name, "browse-"):
		m = pressKeys(t, m, "tab", "tab", "tab", "tab")
		return driveCursorTo(t, m, "Pages", "", browseTargetLabel, browseTargetPath)
	}
	t.Fatalf("setup: no view script for grid %s", name)
	return m
}

// pressKeys delivers each key to m in order, each driven to quiescence.
func pressKeys(t *testing.T, m tea.Model, keys ...string) tea.Model {
	t.Helper()
	for _, k := range keys {
		m = uitest.Drive(t, m, uitest.Key(k))
	}
	return m
}

// minTargetRun is how many leading characters of a target's label must be
// visible on the cursor row to identify it. Panels clip what does not fit
// — `…`-clipped in the redesigned ones — so identification is
// clip-tolerant by design; the shortest clip any grid size produces
// ("private-network-acce…" at 100 columns) is far longer than this floor,
// and no other op or page shares a run this long with either target.
const minTargetRun = 12

// driveCursorTo presses j until panel's cursor row identifies the target,
// at most maxJPresses times, else the harness failed: the mockup state was
// not reached, and the subtest must not present that as a layout diff.
// opID is the target's op id in the pre-redesign op list, which leads the
// cursor row with it; empty for screens that label rows by name alone.
func driveCursorTo(t *testing.T, m tea.Model, panel, opID, label, path string) tea.Model {
	t.Helper()

	for presses := 0; ; presses++ {
		styled, plain := uitest.Screen(m)
		if row, ok := cursorRow(styled, plain, panel); ok && rowIdentifies(row, opID, label, path) {
			return m
		}
		if presses == maxJPresses {
			t.Fatalf("setup: %s cursor never reached %s within %d j presses", panel, label, maxJPresses)
		}
		m = uitest.Drive(t, m, uitest.Key("j"))
	}
}

// cursorRow returns the plain text of the screen's cursor row in panel:
// the `▌` row inside the `╭ <panel> ` bordered panel, how the redesigned
// screens mark it, or — while the screens are still the pre-redesign ones,
// which mark the cursor by painting the whole row from column 0 with the
// cursor background — that row.
func cursorRow(styled, plain, panel string) (string, bool) {
	if row, ok := panelCursorRow(plain, panel); ok {
		return row, true
	}
	return legacyCursorRow(styled, plain)
}

// panelCursorRow returns the `▌` row inside the named panel.
func panelCursorRow(plain, panel string) (string, bool) {
	rows := strings.Split(plain, "\n")
	p, ok := findPanelWithTitle(rows, panel)
	if !ok {
		return "", false
	}
	for i := p.top + 1; i < p.bottom && i < len(rows); i++ {
		if cellAt(rows[i], p.x+1) == '▌' {
			return rows[i], true
		}
	}
	return "", false
}

// legacyCursorRow returns the plain text of the row the pre-redesign
// screens paint with the cursor-row background from column 0 (their
// pre-redesign cursor-row style, whose background was the cursor colour by
// contract §3 note 4), the way the old op list and browse tree marked the
// cursor.
func legacyCursorRow(styled, plain string) (string, bool) {
	styledRows := strings.Split(styled, "\n")
	plainRows := strings.Split(plain, "\n")
	if len(styledRows) != len(plainRows) {
		return "", false
	}
	for i, row := range styledRows {
		cells := scanCells(row)
		if len(cells) > 0 && strings.Contains(cells[0].sgr, darkCursorBg) {
			return plainRows[i], true
		}
	}
	return "", false
}

// rowIdentifies reports whether row shows enough of the target to be it.
// The redesigned panels show the label clipped to what fits, so a visible
// run of at least minTargetRun leading characters of the label — or the
// full path — identifies it. The pre-redesign op list leads the cursor row
// with the op's id, which identifies it at any width (a directory prefix
// alone cannot: two ops share `wiki/concepts/`).
func rowIdentifies(row, opID, label, path string) bool {
	if opID != "" && strings.HasPrefix(strings.TrimLeft(row, " ▎▌"), opID+" ") {
		return true
	}
	return leadingRun(row, label) >= minTargetRun || leadingRun(row, path) >= minTargetRun
}

// leadingRun returns the length of the longest prefix of target contained
// in row.
func leadingRun(row, target string) int {
	tr := []rune(target)
	for n := len(tr); n >= 1; n-- {
		if strings.Contains(row, string(tr[:n])) {
			return n
		}
	}
	return 0
}
