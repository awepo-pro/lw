package markdown

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// blackSGR is what an empty colour string resolves to once it reaches
// glamour or chroma: RGB 0,0,0 — invisible on a dark terminal.
const blackSGR = "38;2;0;0;0"

// emptyTokenSrc exercises every role a caller can leave unset: a heading,
// a code span, a fenced block (chroma), a wikilink, a list and a quote.
const emptyTokenSrc = "# Heading text\n\n" +
	"Body with `code span` and [[wikilink]].\n\n" +
	"- item one\n- item two\n\n" +
	"> quoted line\n\n" +
	"```go\nfunc f() int { return 2 } // c\n```\n"

// TestEmptyTokenFallsBackToFg pins ORCH-16: a Style whose role tokens are
// empty — a caller that has not been updated for a new token — renders
// that role in Fg, never black. Browse shipped its preview headings black
// for one commit because markdown.Style.Heading was "" and the empty
// string reached glamour as a colour.
func TestEmptyTokenFallsBackToFg(t *testing.T) {
	fg := "#D8DDE4"
	fgSGR := "38;2;216;221;228"

	t.Run("missing_role_tokens_render_fg", func(t *testing.T) {
		// Exactly the shape browse.mdStyle() had: the pre-W5 tokens set,
		// Heading and Code still unset.
		st := Style{
			Dark: true, Fg: fg, Muted: "#8C95A2", Faint: "#5E6672", Border: "#353C47",
			Accent: "#7AB2F2", Good: "#6BC28E", Warn: "#E2B45A", Bad: "#EF7F76",
		}
		joined := renderJoined(t, st)

		if strings.Contains(joined, blackSGR) {
			t.Errorf("an unset token rendered black (%s):\n%q", blackSGR, joined)
		}
		heading := lineContaining(t, joined, "Heading text")
		if !strings.Contains(heading, fgSGR) {
			t.Errorf("heading with an unset Heading token does not fall back to Fg %s: %q", fgSGR, heading)
		}
	})

	t.Run("every_token_empty_has_no_colour", func(t *testing.T) {
		// A zero Style asks for no colour at all: nothing should be
		// coloured, and in particular nothing should be black.
		joined := renderJoined(t, Style{})
		if strings.Contains(joined, blackSGR) {
			t.Errorf("zero Style rendered black (%s):\n%q", blackSGR, joined)
		}
		if strings.Contains(joined, "\x1b[38;2;") {
			t.Errorf("zero Style emitted a foreground colour:\n%q", joined)
		}
	})

	t.Run("set_tokens_still_win", func(t *testing.T) {
		// The fallback must not override a token the caller did set.
		st := Style{Dark: true, Fg: fg, Heading: "#C3A0F0", Code: "#6CC7C9"}
		joined := renderJoined(t, st)
		heading := lineContaining(t, joined, "Heading text")
		if !strings.Contains(heading, "38;2;195;160;240") {
			t.Errorf("set Heading token not used: %q", heading)
		}
	})
}

// renderJoined renders emptyTokenSrc with st and returns the joined lines.
func renderJoined(t *testing.T, st Style) string {
	t.Helper()
	lines, err := NewRenderer().Render([]byte(emptyTokenSrc), Options{Width: 44, Style: st})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return strings.Join(lines, "\n")
}

// lineContaining returns the first line of joined whose VISIBLE text holds
// want, failing the test when no line does. The comparison strips SGR
// first: glamour splits a styled run at each word, so "Heading text"
// appears in the raw bytes as "…mHeading\x1b[m…m text\x1b[m".
func lineContaining(t *testing.T, joined, want string) string {
	t.Helper()
	for _, l := range strings.Split(joined, "\n") {
		if strings.Contains(ansi.Strip(l), want) {
			return l
		}
	}
	t.Fatalf("no rendered line contains %q:\n%q", want, joined)
	return ""
}
