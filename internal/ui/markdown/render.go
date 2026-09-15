package markdown

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Options configures one render.
type Options struct {
	Width   int // total cells available; lines are padded to exactly min(Width, Measure+2)
	Measure int // prose measure; 0 means 100 (README size rules)
	Style   Style
	Plain   bool     // no ANSI at all (non-TTY CLI output); layout is identical
	Changed []string // source lines added by the change; blocks containing one get the ▎ gutter
}

// Renderer renders vault markdown for the terminal, memoized. The zero
// value isn't usable; construct with NewRenderer.
type Renderer struct {
	cache *renderCache
}

// NewRenderer returns a ready-to-use Renderer with its own bounded cache.
func NewRenderer() *Renderer {
	return &Renderer{cache: newRenderCache()}
}

// Render returns src rendered as lines (no trailing newline), each exactly
// min(o.Width, o.Measure+2) cells wide.
func (r *Renderer) Render(src []byte, o Options) ([]string, error) {
	key := newMemoKey(src, o)
	if lines, ok := r.cache.get(key); ok {
		return lines, nil
	}

	lines, err := renderPage(src, o)
	if err != nil {
		return nil, err
	}
	r.cache.put(key, lines)
	return cloneLines(lines), nil
}

// bodyLine is one rendered line before its gutter and final padding are
// applied: styled content (or plain text for the frontmatter header) plus
// whether the block it came from counts as "changed" for the gutter.
type bodyLine struct {
	text   string
	marked bool
}

// lineWidth is min(o.Width, o.Measure+2), with "Measure 0 means 100"
// (contract §2 Options.Measure) read as: when Measure is 0, the cap is 100
// outright, not 0+2.
func lineWidth(o Options) int {
	capW := o.Measure + 2
	if o.Measure == 0 {
		capW = 100
	}
	w := o.Width
	if capW < w {
		w = capW
	}
	if w < 0 {
		w = 0
	}
	return w
}

// renderPage does the actual, uncached work behind Render.
func renderPage(src []byte, o Options) ([]string, error) {
	totalW := lineWidth(o)
	contentW := totalW - 2
	if contentW < 1 {
		contentW = 1
	}

	fm, body, found := splitFrontmatter(src)
	body = strings.Trim(body, "\n") // mockgen.page_lines: body.strip('\n')

	var all []bodyLine
	if found {
		for _, sl := range pageHeader(fm, contentW) {
			all = append(all, bodyLine{text: renderStyled(sl, o.Style)})
		}
	}

	bodyLines, err := renderBody(body, o.Style, contentW, changedSet(o.Changed))
	if err != nil {
		return nil, fmt.Errorf("markdown: render: %w", err)
	}
	all = append(all, bodyLines...)

	lines := make([]string, len(all))
	for i, l := range all {
		lines[i] = padCells(gutterPrefix(o.Style, l.marked)+l.text, totalW)
		if o.Plain {
			lines[i] = ansi.Strip(lines[i])
		}
	}
	return lines, nil
}

// changedSet turns Options.Changed into a lookup set for block-marking
// (mockgen.render_md: "any(l in changed for l in blk)").
func changedSet(changed []string) map[string]bool {
	if len(changed) == 0 {
		return nil
	}
	set := make(map[string]bool, len(changed))
	for _, l := range changed {
		set[l] = true
	}
	return set
}

// blockMarked reports whether blk contains at least one line present in
// changed, matched against the block's raw source lines (before any
// provenance/wikilink preprocessing).
func blockMarked(blk []string, changed map[string]bool) bool {
	if len(changed) == 0 {
		return false
	}
	for _, l := range blk {
		if changed[l] {
			return true
		}
	}
	return false
}

// renderBody renders body per top-level block (contract §2 note 3;
// mockgen.render_md/gut): each block is rendered separately at contentW,
// blocks join with one blank line, and a block containing a Changed line
// (and the blank line between two such blocks) is marked for the gutter.
func renderBody(body string, style Style, contentW int, changed map[string]bool) ([]bodyLine, error) {
	cfg := buildGlamourStyle(style)

	var out []bodyLine
	prevMarked := false
	for i, blk := range splitBlocks(body) {
		marked := blockMarked(blk, changed)
		if i > 0 {
			out = append(out, bodyLine{text: "", marked: marked && prevMarked})
		}

		rendered, err := renderBlock(blk, cfg, contentW, style)
		if err != nil {
			return nil, err
		}
		for _, l := range rendered {
			out = append(out, bodyLine{text: l, marked: marked})
		}
		prevMarked = marked
	}
	return out, nil
}
