// Package markdown renders vault markdown into exact-width terminal lines,
// in the one adaptive token palette, memoized. It never imports internal/ui
// (workflow 003 contract §2, §3): it builds its own glamour style and its
// own local greedy word-wrap, so internal/ui and internal/ui/markdown can be
// built concurrently.
package markdown

import (
	glamour "charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
)

// Style is the adaptive palette the renderer builds its glamour style
// from, as resolved hex strings for one polarity (D7).
type Style struct {
	Dark                                              bool
	Fg, Muted, Faint, Border, Accent, Good, Warn, Bad string // "#RRGGBB"
}

// strPtr and uintPtr build the *string / *uint fields ansi.StyleConfig
// wants. Each call owns its own backing variable, so aliasing multiple
// fields to the same pointer is safe: nothing downstream ever writes
// through it.
func strPtr(s string) *string { return &s }
func uintPtr(u uint) *uint    { return &u }
func boolPtr(b bool) *bool    { return &b }

// buildGlamourStyle turns Style into the glamour ansi.StyleConfig the
// per-block renderer uses (contract §2 note 3; s0-foundation.md T02 item 1).
// Every colour in the result is one of Style's eight tokens: no stock
// glamour style (dark/light/notty/...) is used, so no chroma-theme colour
// or hard-coded ANSI-256 value ever leaks through (F5).
func buildGlamourStyle(s Style) ansi.StyleConfig {
	fg := strPtr(s.Fg)
	muted := strPtr(s.Muted)

	return ansi.StyleConfig{
		// No document margin: the caller (Render) already accounts for the
		// 2-cell gutter, and per-block rendering supplies its own spacing
		// between blocks.
		Document: ansi.StyleBlock{Margin: uintPtr(0)},

		BlockQuote: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{Color: muted},
			Indent:         uintPtr(1),
			IndentToken:    strPtr("│ "), // "│ "
		},
		Paragraph: ansi.StyleBlock{},
		List: ansi.StyleList{
			StyleBlock:  ansi.StyleBlock{Indent: uintPtr(0)},
			LevelIndent: 2,
		},

		// Headings: bold Fg, no "#" prefix (contract §2 note 2). Leaving
		// H1..H6 at their zero value means every level cascades straight
		// from Heading with no level-specific prefix or colour.
		Heading: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: fg, Bold: boolPtr(true)}},

		Text:        ansi.StylePrimitive{Color: fg},
		Strong:      ansi.StylePrimitive{Color: fg, Bold: boolPtr(true)},
		Emph:        ansi.StylePrimitive{Color: fg, Italic: boolPtr(true)},
		Item:        ansi.StylePrimitive{Color: fg, BlockPrefix: "• "}, // "• "
		Enumeration: ansi.StylePrimitive{Color: fg, BlockPrefix: ". "},

		// Genuine markdown links: underlined Fg. LinkText is the anchor
		// text; Link is the trailing " <url>" glamour appends for a real
		// destination — Muted, so a real link stays legible without
		// competing with LinkText.
		Link:     ansi.StylePrimitive{Color: muted},
		LinkText: ansi.StylePrimitive{Color: fg, Underline: boolPtr(true)},

		// Provenance and wikilinks are never routed through Image or
		// Strikethrough any more (repair-1, Critical 1: that collided with
		// real "![alt](url)" and "~~text~~" in vault content, since glamour
		// dispatches those on AST node kind alone). insertMarkers/inline.go
		// restyle provenance/wikilink text directly instead, so these two
		// tokens are free to mean what real markdown expects: a real image's
		// alt text renders like ordinary prose (Fg), and real strikethrough
		// keeps its SGR 9 strike-through line, Fg like any other text.
		Image:         ansi.StylePrimitive{Color: muted},
		ImageText:     ansi.StylePrimitive{Color: fg},
		Strikethrough: ansi.StylePrimitive{Color: fg, CrossedOut: boolPtr(true)},

		Code:      ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: muted}},
		CodeBlock: ansi.StyleCodeBlock{StyleBlock: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: muted}}},

		// Table separators use the Border token; cell text stays Fg via
		// StylePrimitive.Color on the table's own style (cells cascade
		// their content style from Text, so it is not set again here).
		Table: ansi.StyleTable{
			CenterSeparator: strPtr("┼"), // "┼"
			ColumnSeparator: strPtr("│"), // "│"
			RowSeparator:    strPtr("─"), // "─"
		},
	}
}

// newBlockRenderer builds a fresh glamour renderer for one block at wrapW
// cells. A fresh *glamour.TermRenderer per block keeps every block's
// internal AST/blockStack state independent (renderer.go relies on this).
func newBlockRenderer(cfg ansi.StyleConfig, wrapW int) (*glamour.TermRenderer, error) {
	if wrapW < 1 {
		wrapW = 1
	}
	return glamour.NewTermRenderer(glamour.WithStyles(cfg), glamour.WithWordWrap(wrapW))
}
