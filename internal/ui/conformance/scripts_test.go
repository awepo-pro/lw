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

// mockupAnswer is askgen.py's ANSWER_MD, verbatim: the answer the user
// approved in mockup A-5-1, the scripted turn's single TextDelta. Unlike
// 003's single paragraph it carries markdown structure — a provenance
// marker, a heading, a list, a fenced block — so it exercises the
// renderer's markdown line counts.
const mockupAnswer = `Both paths call the same Claude weights, so the difference is **governance, not model quality**. ` +
	"^[wiki/comparisons/anthropic-api-vs-vertex-ai.md]\n" +
	`
## Where they differ

- **Auth** — static ` + "`x-api-key`" + ` keys, versus GCP IAM and service accounts.
- **Network** — the public internet to ` + "`api.anthropic.com`" + `, versus the private GCP backbone (VPC-SC / PSC).
- **Billing** — an Anthropic invoice, versus GCP commitments.
- **Features** — the direct API gets betas first; Vertex trails by a stated 2-4 weeks.

` + "```python" + `
client = anthropic.AnthropicVertex(region="us-east5", project_id=PROJECT)
` + "```" + `

See [[anthropic-api-vs-vertex-ai]] and [[claude]].`

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
// contents are part of the frozen setup — the same reason the contract
// pins DropHunk("op4", "h1").
const (
	reviewTargetPanel = "Ops"
	reviewTargetLabel = "private-network-access.md"
)

// The browse script's target: the selected comparison page.
const browseTargetLabel = "anthropic-api-vs-vertex-ai"

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
		m = driveCursorTo(t, m, reviewTargetPanel, reviewTargetLabel)
		return pressKeys(t, m, "p")

	case strings.HasPrefix(name, "keys-"):
		m = driveCursorTo(t, m, reviewTargetPanel, reviewTargetLabel)
		return pressKeys(t, m, "?")

	case strings.HasPrefix(name, "review-"):
		return driveCursorTo(t, m, reviewTargetPanel, reviewTargetLabel)

	case strings.HasPrefix(name, "browse-"):
		m = pressKeys(t, m, "tab", "tab", "tab", "tab")
		return driveCursorTo(t, m, "Pages", browseTargetLabel)
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

// minTargetRun is how many cells of a target's name must survive the
// panels' `…` clip for a name cell to identify it. Identification is
// clip-tolerant by design: the shortest clip any grid size produces —
// `private-network-acce…` at 100 columns — is far longer than this floor,
// and no other op or page shares a run this long with either target's name.
const minTargetRun = 12

// opsNameCol is the Ops row's name cell, in content columns: the glyph sits
// at column 0 and the kind (`patch`, padded to 6) at column 2, so the
// basename starts at column 8 (s2-screens.md T06 "Ops rows").
const opsNameCol = 8

// driveCursorTo presses j until panel's cursor row identifies the target,
// at most maxJPresses times, else the harness failed: the mockup state was
// not reached, and the subtest must not present that as a layout diff.
func driveCursorTo(t *testing.T, m tea.Model, panel, label string) tea.Model {
	t.Helper()

	for presses := 0; ; presses++ {
		_, plain := uitest.Screen(m)
		if row, x, ok := cursorRow(plain, panel); ok && rowIdentifies(row, x, panel, label) {
			return m
		}
		if presses == maxJPresses {
			t.Fatalf("setup: %s cursor never reached %s within %d j presses", panel, label, maxJPresses)
		}
		m = uitest.Drive(t, m, uitest.Key("j"))
	}
}

// cursorRow returns the plain text of panel's cursor row — the `▌` row
// inside the `╭ <panel> ` bordered panel — with the cell column of the
// panel's left border, where rowIdentifies's column arithmetic starts.
func cursorRow(plain, panel string) (row string, x int, ok bool) {
	rows := strings.Split(plain, "\n")
	p, found := findPanelWithTitle(rows, panel)
	if !found {
		return "", 0, false
	}
	for i := p.top + 1; i < p.bottom && i < len(rows); i++ {
		if cellAt(rows[i], p.x+1) == '▌' {
			return rows[i], p.x, true
		}
	}
	return "", 0, false
}

// rowIdentifies reports whether row — a content row of the panel titled
// panel, whose left border sits at cell x — names target in its name cell.
// An Ops row carries the basename at content column opsNameCol; a Pages
// row's name follows the row's indentation and any `▾ `/`▸ ` directory
// marker (s2-screens.md T07). The cell runs to the row's first run of two
// spaces, its first border cell, or its end. The row identifies the target
// when the cell is a prefix of the target's name and either equals it or —
// one `…` clip stripped — keeps at least minTargetRun cells. Nothing else
// in the row counts: op2's dir hint `wiki/concepts/` shares a long prefix
// with the target's path and must not identify it (C34).
func rowIdentifies(row string, x int, panel, target string) bool {
	cell, clipped := nameCell(row, nameCellStart(row, x, panel))
	if cell == "" || !strings.HasPrefix(target, cell) {
		return false
	}
	if clipped {
		return len([]rune(cell)) >= minTargetRun
	}
	return cell == target
}

// nameCellStart returns the cell column row's name cell starts at. A Pages
// row puts its name after the row's indentation and its `▾ `/`▸ ` marker;
// Ops is the only other driven panel, and puts the basename at content
// column opsNameCol. Columns are cells, from the panel's left border x:
// content starts two cells in, past the border and the cursor-gutter
// column.
func nameCellStart(row string, x int, panel string) int {
	if panel == "Pages" {
		rs := []rune(row)
		c := x + 2
		for c < len(rs) && rs[c] == ' ' {
			c++
		}
		if c < len(rs) && (rs[c] == '▾' || rs[c] == '▸') {
			c += 2 // the marker and the space between marker and name
		}
		return c
	}
	return x + 2 + opsNameCol
}

// nameCell returns the name cell row carries from cell start: the run of
// cells up to the row's first run of two spaces, its first `│` border cell
// or its end, trailing spaces dropped. clipped reports whether a trailing
// `…` was stripped — the panels' clip mark, which a cell the panel cut to
// fit ends in.
func nameCell(row string, start int) (cell string, clipped bool) {
	rs := []rune(row)
	if start >= len(rs) {
		return "", false
	}
	end := len(rs)
	for c := start; c < len(rs); c++ {
		if rs[c] == '│' || (rs[c] == ' ' && c+1 < len(rs) && rs[c+1] == ' ') {
			end = c
			break
		}
	}
	for end > start && rs[end-1] == ' ' {
		end--
	}
	if cell = string(rs[start:end]); strings.HasSuffix(cell, "…") {
		return strings.TrimSuffix(cell, "…"), true
	}
	return cell, false
}
