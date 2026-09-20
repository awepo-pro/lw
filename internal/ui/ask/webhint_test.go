// webhint_test.go pins 012's ask-pane hint (012 contract §3, cs-79f2d7): on
// a vault whose web lookup is not configured while an agent is wired, the
// empty transcript carries the frozen hint line exactly once — wrapped, each
// line faint, between the intro and the Try block — and never as a
// transcript entry. A configured vault's empty state and every agentless
// pane stay exactly the mockgen.ask_empty shape, so the mockup-pinned grids
// cannot move. The only WebSearch-false fixtures in this package live here,
// deliberately (012 contract §5).
package ask

import (
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// webHintFrozen restates contract §3's hint bytes so an edit to view.go's
// webHintUnavailable const fails here — this file, not the const, pins the
// wording (em-dash included) forever.
const webHintFrozen = "web lookup unavailable — lw config set web.api_key env:TAVILY_API_KEY"

// hintW, hintH is the size every subtest renders at — the smallest checkpoint
// golden size, where the hint is one row and the empty state fits the panel.
const (
	hintW = 80
	hintH = 22
)

// newWebHintModel builds an engine-less ask pane at the given WebSearch
// state, with a non-nil agent when withAgent is true. No engine: the
// suggested prompts are the fallback set, so the assertions never depend on
// a vault fixture. newTestDeps pins WebSearch true; flipping it here is the
// one deliberate false in the package.
func newWebHintModel(t *testing.T, webSearch, withAgent bool) *Model {
	t.Helper()
	d := newTestDeps(t)
	d.WebSearch = webSearch
	if withAgent {
		d.Agent = &uitest.FakeAgent{}
	}
	return New(d).(*Model)
}

// screenLines renders m at hintW×hintH and returns its rows: styled, then
// ANSI-stripped — one render, so indexes line up across the two slices.
func screenLines(t *testing.T, m *Model) (styled, plain []string) {
	t.Helper()
	styledStr, plainStr := uitest.PaneScreen(m, hintW, hintH)
	return strings.Split(styledStr, "\n"), strings.Split(plainStr, "\n")
}

// lineContaining returns the index of the first row containing want, or -1.
// Rows are the pane's already-stripped screen rows, so they carry the
// Transcript panel's `│ ` borders — needles are unique substrings, never
// whole-row texts.
func lineContaining(lines []string, want string) int {
	for i, l := range lines {
		if strings.Contains(l, want) {
			return i
		}
	}
	return -1
}

func TestAskEmptyWebHint(t *testing.T) {
	t.Run("unconfigured", func(t *testing.T) {
		m := newWebHintModel(t, false, true)
		styled, plain := screenLines(t, m)
		joined := strings.Join(plain, "\n")

		// Exactly once...
		if n := strings.Count(joined, webHintFrozen); n != 1 {
			t.Fatalf("the empty state shows the hint %d time(s), want exactly once:\n%s", n, joined)
		}
		// ...between the intro and the Try block, contract §3's order...
		intro := lineContaining(plain, "Ask the wiki a question.")
		hint := lineContaining(plain, webHintFrozen)
		try := lineContaining(plain, "Try")
		if intro < 0 || hint < 0 || try < 0 || !(intro < hint && hint < try) {
			t.Fatalf("the hint is not between the intro and Try (intro %d, hint %d, try %d):\n%s", intro, hint, try, joined)
		}
		// ...faint — the row carrying the hint carries the Faint token's SGR...
		if !hasSGR(styled[hint], sgrRGBRun(m.theme.Faint.Render("x"))) {
			t.Fatalf("the hint line does not carry the Faint token: %q", styled[hint])
		}
		// ...and never a transcript entry: the pane is still in the empty
		// state the hint belongs to, with nothing appended to the scrollback.
		if len(m.entries) != 0 {
			t.Fatalf("the hint produced %d transcript entries", len(m.entries))
		}
	})

	t.Run("configured", func(t *testing.T) {
		m := newWebHintModel(t, true, true)
		_, plain := screenLines(t, m)
		joined := strings.Join(plain, "\n")

		if strings.Contains(joined, "web lookup unavailable") {
			t.Fatalf("a configured vault's empty state shows the hint:\n%s", joined)
		}

		// Byte-identical to today's: the unconfigured pane is this one plus
		// exactly a blank and the hint line between intro and Try. Deleting
		// those two rows from the unconfigured content list must reconstruct
		// the configured list byte for byte — the mockup-pinned ask_empty
		// shape with nothing else moved. Compared at emptyTranscriptLines,
		// where the hint is frozen to live, because the rendered View pads
		// the panel to size and the padding would blur the diff.
		th, _ := paneLayout(hintW, hintH)
		u := newWebHintModel(t, false, true)
		uLines := u.emptyTranscriptLines(hintW-4, th)
		hint := -1
		for i, l := range uLines {
			if strings.Contains(l, webHintFrozen) {
				hint = i
				break
			}
		}
		if hint <= 0 || strings.TrimSpace(uLines[hint-1]) != "" {
			t.Fatalf("the hint is not one line behind one blank:\n%s", strings.Join(uLines, "\n"))
		}
		uWithoutHint := append(append([]string{}, uLines[:hint-1]...), uLines[hint+1:]...)
		mLines := m.emptyTranscriptLines(hintW-4, th)
		if strings.Join(uWithoutHint, "\n") != strings.Join(mLines, "\n") {
			t.Fatalf("the configured empty state moved beyond the hint block:\n--- configured\n%s\n--- unconfigured minus hint\n%s",
				strings.Join(mLines, "\n"), strings.Join(uWithoutHint, "\n"))
		}
	})

	t.Run("no_agent", func(t *testing.T) {
		m := newWebHintModel(t, false, false)
		_, plain := screenLines(t, m)
		joined := strings.Join(plain, "\n")

		if strings.Contains(joined, "web lookup unavailable") {
			t.Fatalf("a pane without an agent shows the web hint:\n%s", joined)
		}
		// Still the empty state, not a blank pane: the hint's absence must
		// not be the whole shape disappearing.
		if lineContaining(plain, "Try") < 0 {
			t.Fatalf("the agentless empty state lost its shape:\n%s", joined)
		}
	})

	t.Run("prompts_survive", func(t *testing.T) {
		m := newWebHintModel(t, false, true)
		_, plain := screenLines(t, m)
		joined := strings.Join(plain, "\n")

		// The three suggested prompts still render, after the Try block, in
		// order — the hint squeezed in above must not push them out of the
		// state that invites a first question.
		try := lineContaining(plain, "Try")
		if try < 0 {
			t.Fatalf("the unconfigured empty state lost the Try block:\n%s", joined)
		}
		last := try
		for _, p := range fallbackPrompts() {
			idx := lineContaining(plain, "› "+p)
			if idx <= last {
				t.Fatalf("suggested prompt %q missing or out of order after Try:\n%s", p, joined)
			}
			last = idx
		}
	})
}
