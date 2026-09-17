package markdown

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// RenderFragment renders src as a standalone fragment rather than a page:
// no frontmatter split, no page header, and no padding to a fixed cell
// width. Lines carry exactly the role colours Render gives a page, so a
// heading in an Ask answer and the same heading in a preview are the same
// colour.
//
// o.Width still wraps. o.Measure still caps the prose measure. o.Plain
// still suppresses every escape. o.Changed is meaningless for a fragment
// and is IGNORED — a fragment has no diff to gutter.
//
// Returns lines with no trailing newline. An empty src returns an empty,
// non-nil slice, never an error.
func (r *Renderer) RenderFragment(src []byte, o Options) ([]string, error) {
	key := newFragmentMemoKey(src, o)
	if lines, ok := r.cache.get(key); ok {
		return lines, nil
	}

	lines, err := renderFragment(src, o)
	if err != nil {
		return nil, err
	}
	r.cache.put(key, lines)
	return cloneLines(lines), nil
}

// renderFragment does the actual, uncached work behind RenderFragment. It
// is renderPage minus the page shape: the same Style.resolved()
// normalization (ORCH-16), the same renderBody, chroma setup and glamour
// configuration — but splitFrontmatter and pageHeader never run (a leading
// "---" is rendered as the thematic break CommonMark says it is), and no
// line is gutter-prefixed or padded, because the panel hosting a fragment
// owns its own padding and cursor gutter.
func renderFragment(src []byte, o Options) ([]string, error) {
	// The same one normalization point as renderPage: every token a caller
	// left empty becomes Fg before anything reads it (ORCH-16).
	o.Style = o.Style.resolved()

	// Changed is ignored: pass no change set, so no block is ever marked —
	// and the ▎ gutter lives only in the page path's pad loop anyway.
	bodyLines, err := renderBody(strings.Trim(string(src), "\n"), o.Style, fragmentWidth(o), nil)
	if err != nil {
		return nil, fmt.Errorf("markdown: render fragment: %w", err)
	}

	lines := make([]string, len(bodyLines)) // non-nil even at len 0
	for i, l := range bodyLines {
		line := l.text // no gutter prefix, no padCells
		if o.Plain {
			line = ansi.Strip(line)
		}
		// glamour still pads a block's lines out to the wrap width
		// internally; that padding belongs to the hosting panel, so it is
		// cut back — through ansi.Truncate, because the pad cells can sit
		// inside a styled run where a plain TrimRight would miss them.
		lines[i] = ansi.Truncate(line, ansi.StringWidth(strings.TrimRight(ansi.Strip(line), " ")), "")
	}
	return lines, nil
}
