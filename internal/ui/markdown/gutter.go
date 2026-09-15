package markdown

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// colorKind names which Style token a styledLine's text uses, resolved
// against the Style in effect for one Render call.
type colorKind int

const (
	colorNone colorKind = iota
	colorFg
	colorFaint
	colorAccent
)

// styledLine is one pre-gutter, pre-pad line the frontmatter header
// produces: plain text plus the one token colour (and optional bold) it
// should render in. render.go turns it into a real ANSI string once it
// knows the Style in effect, then feeds it through the same gutter/pad
// path as glamour's own block output.
type styledLine struct {
	text  string
	color colorKind
	bold  bool
}

// hexFor resolves a colorKind against Style; colorNone means "no colour at
// all" (used for the blank separator line).
func hexFor(s Style, c colorKind) string {
	switch c {
	case colorFg:
		return s.Fg
	case colorFaint:
		return s.Faint
	case colorAccent:
		return s.Accent
	default:
		return ""
	}
}

// renderStyled turns l into an ANSI string using x/ansi.Style directly
// (not lipgloss.Style: baseelement.go in glamour notes a lipgloss.Style
// join bug and avoids it the same way).
func renderStyled(l styledLine, s Style) string {
	if l.text == "" {
		return ""
	}
	st := ansi.Style{}
	if hex := hexFor(s, l.color); hex != "" {
		st = st.ForegroundColor(lipgloss.Color(hex))
	}
	if l.bold {
		st = st.Bold()
	}
	return st.Styled(l.text)
}

// gutterPrefix is the 2-cell gutter every output line starts with: "▎ " in
// Accent when marked, two spaces otherwise (contract §2 note 4;
// mockgen.gut).
func gutterPrefix(s Style, marked bool) string {
	if !marked {
		return "  "
	}
	st := ansi.Style{}.ForegroundColor(lipgloss.Color(s.Accent))
	return st.Styled("▎") + " "
}

// padCells pads s with spaces to exactly w cells, clipping first if wider
// (contract: "every returned line is exactly min(Width, Measure+2) cells").
func padCells(s string, w int) string {
	cw := ansi.StringWidth(s)
	switch {
	case cw > w:
		return ansi.Truncate(s, w, "…")
	case cw < w:
		return s + strings.Repeat(" ", w-cw)
	default:
		return s
	}
}
