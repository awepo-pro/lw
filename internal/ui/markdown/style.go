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
	Heading, Code                                     string // "#RRGGBB"; added 2026-09-16 (W5 F3/D-3W)
}

// resolved returns s with every empty role token filled from Fg, and is
// applied once per render before any token is read (render.go).
//
// An empty token must never reach glamour or chroma: they take a colour as
// a string, and an empty string resolves to BLACK, which is invisible on a
// dark terminal. That is not hypothetical — W5's first wave rendered
// Browse's preview headings black for exactly one commit, because the
// caller had not set the two new tokens yet (ORCH-16). Falling back to Fg
// degrades a missing token to ordinary prose colour instead. An empty Fg
// means the caller asked for no colour at all, and stays empty: the
// per-field builders then set no colour, which is what `Plain` output and
// a zero Style expect.
func (s Style) resolved() Style {
	fill := func(tok *string) {
		if *tok == "" {
			*tok = s.Fg
		}
	}
	fill(&s.Muted)
	fill(&s.Faint)
	fill(&s.Border)
	fill(&s.Accent)
	fill(&s.Good)
	fill(&s.Warn)
	fill(&s.Bad)
	fill(&s.Heading)
	fill(&s.Code)
	return s
}

// strPtr and uintPtr build the *string / *uint fields ansi.StyleConfig
// wants. Each call owns its own backing variable, so aliasing multiple
// fields to the same pointer is safe: nothing downstream ever writes
// through it.
func strPtr(s string) *string { return &s }
func uintPtr(u uint) *uint    { return &u }
func boolPtr(b bool) *bool    { return &b }

// colorPtr is strPtr for a colour token: an empty hex becomes a nil
// pointer, which glamour reads as "this style sets no colour". A non-nil
// pointer to "" is not the same thing — glamour passes the empty string on
// as a colour, and it resolves to RGB 0,0,0 (ORCH-16). Every token is
// non-empty after Style.resolved() unless the caller set no Fg either,
// which means "no colour at all".
func colorPtr(hex string) *string {
	if hex == "" {
		return nil
	}
	return &hex
}

// buildGlamourStyle turns Style into the glamour ansi.StyleConfig the
// per-block renderer uses (contract §2 note 3; s0-foundation.md T02 item 1).
// Every colour in the result is one of Style's ten colour tokens: no stock
// glamour style (dark/light/notty/...) is used, so no stock chroma theme
// or hard-coded ANSI-256 value ever leaks through (F5). The role mapping is
// the one W5 F3/D-3W chose (contract §2 note 2), foreground only.
func buildGlamourStyle(s Style) ansi.StyleConfig {
	fg := colorPtr(s.Fg)
	muted := colorPtr(s.Muted)
	accent := colorPtr(s.Accent)
	heading := colorPtr(s.Heading)
	code := colorPtr(s.Code)

	return ansi.StyleConfig{
		// No document margin: the caller (Render) already accounts for the
		// 2-cell gutter, and per-block rendering supplies its own spacing
		// between blocks. The Document primitive carries Fg: it is the base
		// of every block cascade, so plain paragraph text renders Fg.
		Document: ansi.StyleBlock{Margin: uintPtr(0), StylePrimitive: ansi.StylePrimitive{Color: fg}},

		// Quote text is Muted + italic (the inner paragraph inherits the
		// BlockQuote primitive, since Text carries no colour); the bar
		// ("│ ") glamour draws from the parent (document) style, so
		// block.go recolours it Border after the render (colorizeQuoteBar).
		BlockQuote: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{Color: muted, Italic: boolPtr(true)},
			Indent:         uintPtr(1),
			IndentToken:    strPtr("│ "), // "│ "
		},
		Paragraph: ansi.StyleBlock{},

		// Fg, not Accent: item text renders against the List primitive (a
		// tight list's TextBlock pushes no block of its own), so the marker
		// and the item text can't take different colours from the config.
		// reflowListLines recolours every "•" / "N." marker Accent after
		// the render, the same post-render pass tables and blockquotes get.
		List: ansi.StyleList{
			StyleBlock:  ansi.StyleBlock{Indent: uintPtr(0), StylePrimitive: ansi.StylePrimitive{Color: fg}},
			LevelIndent: 2,
		},

		// Headings (contract §2 note 2): H1 Heading+bold, H2 Accent+bold,
		// H3–H6 Heading, not bold. H3..H6 are left at their zero value, so
		// every one of those levels cascades straight from Heading with no
		// level-specific prefix, colour or weight.
		Heading: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: heading}},
		H1:      ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: heading, Bold: boolPtr(true)}},
		H2:      ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: accent, Bold: boolPtr(true)}},

		// Text deliberately carries NO colour: a text node's primitive
		// overrides its block's in glamour's cascade (blockstack.go With),
		// so a coloured Text would flatten every heading/quote/role colour
		// back to Fg. Prose takes its colour from its block — the stock
		// glamour styles are built the same way ("text": {}). Strong, Emph
		// and Strikethrough keep Fg + their attribute.
		Text:           ansi.StylePrimitive{},
		Strong:         ansi.StylePrimitive{Color: fg, Bold: boolPtr(true)},
		Emph:           ansi.StylePrimitive{Color: fg, Italic: boolPtr(true)},
		Item:           ansi.StylePrimitive{Color: fg, BlockPrefix: "• "}, // "• "
		Enumeration:    ansi.StylePrimitive{Color: fg, BlockPrefix: ". "},
		HorizontalRule: ansi.StylePrimitive{Color: fg},

		// Genuine markdown links: Accent + underlined text, Muted URL.
		// LinkText is the anchor text; Link is the trailing " <url>" glamour
		// appends for a real destination — Muted, so a real link stays
		// legible without competing with LinkText.
		Link:     ansi.StylePrimitive{Color: muted},
		LinkText: ansi.StylePrimitive{Color: accent, Underline: boolPtr(true)},

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

		// Code spans are Code (W5 F3). A code BLOCK's own fallback style —
		// what a fence with an unknown or empty language renders through —
		// is plain Fg: its highlighting, when the language is known, comes
		// from the per-Style chroma style selected via Theme (chroma.go),
		// never from StyleCodeBlock.Chroma (C37).
		Code:      ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: code}},
		CodeBlock: ansi.StyleCodeBlock{StyleBlock: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: fg}}},

		// Table separators use the Border token (block.go recolours them);
		// cell text is styled with the table's own primitive, Fg — the
		// exact SGR colorizeTableHeader matches to lift the header row to
		// Accent + bold afterwards.
		Table: ansi.StyleTable{
			StyleBlock:      ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: fg}},
			CenterSeparator: strPtr("┼"), // "┼"
			ColumnSeparator: strPtr("│"), // "│"
			RowSeparator:    strPtr("─"), // "─"
		},
	}
}

// newBlockRenderer builds a fresh glamour renderer for one block at wrapW
// cells. A fresh *glamour.TermRenderer per block keeps every block's
// internal AST/blockStack state independent (renderer.go relies on this).
// The chroma formatter is terminal16m, never glamour's default terminal256:
// 256-colour quantisation would dull the token colours even on a truecolor
// terminal (contract §2 note 3; downsampling belongs to Bubble Tea's
// renderer).
func newBlockRenderer(cfg ansi.StyleConfig, wrapW int) (*glamour.TermRenderer, error) {
	if wrapW < 1 {
		wrapW = 1
	}
	return glamour.NewTermRenderer(
		glamour.WithStyles(cfg),
		glamour.WithWordWrap(wrapW),
		glamour.WithChromaFormatter("terminal16m"),
	)
}
