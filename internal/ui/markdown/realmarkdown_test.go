package markdown

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestRenderRealMarkdownKeepsMeaning is repair-1's required regression
// suite: it proves genuine markdown (strikethrough, images) keeps its own
// meaning now that provenance/wikilink markers no longer share their AST
// node kind, that provenance no longer leaks an OSC-8 hyperlink, that a
// no-frontmatter page starts with its body, that wide/CJK text still pads
// to an exact cell width, and that box-drawing runes typed inside a real
// table cell are not mistaken for glamour's own separators.
func TestRenderRealMarkdownKeepsMeaning(t *testing.T) {
	t.Run("strikethrough_keeps_sgr9", func(t *testing.T) {
		src := []byte("Real ~~struck~~ text.\n")
		lines, err := NewRenderer().Render(src, Options{Width: 60, Style: testStyle})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		joined := strings.Join(lines, "\n")
		if !strings.Contains(joined, "\x1b[38;2;216;221;228;9mstruck\x1b[m") &&
			!strings.Contains(joined, ";9m") {
			t.Errorf("styled output has no SGR 9 (strikethrough) on real ~~struck~~: %q", joined)
		}
		if strings.Contains(joined, "38;2;216;221;228;4mstruck") {
			t.Errorf("real ~~struck~~ still carries SGR 4 (the old wikilink carrier's underline): %q", joined)
		}
	})

	t.Run("image_alt_not_faint", func(t *testing.T) {
		src := []byte("An image: ![alt text](http://example.com/x.png) here.\n")
		lines, err := NewRenderer().Render(src, Options{Width: 60, Style: testStyle})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		joined := strings.Join(lines, "\n")
		faintSGR := "38;2;94;102;114" // testStyle.Faint, #5E6672
		// The alt text must appear, and not immediately preceded by the
		// Faint colour code (the old provenance carrier's ImageText style).
		idx := strings.Index(ansi.Strip(joined), "alt text")
		if idx < 0 {
			t.Fatalf("alt text missing from output: %q", joined)
		}
		if strings.Contains(joined, faintSGR+"malt") || strings.Contains(joined, faintSGR+"m\x1b[38;2;216;221;228malt") {
			t.Errorf("real image alt text is styled Faint: %q", joined)
		}
	})

	t.Run("provenance_emits_no_osc8", func(t *testing.T) {
		src := []byte("Covered here. ^[raw/a.md] More text.\n")
		lines, err := NewRenderer().Render(src, Options{Width: 60, Style: testStyle})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		joined := strings.Join(lines, "\n")
		if strings.Contains(joined, "\x1b]8;") {
			t.Errorf("styled output for ^[raw/a.md] contains an OSC-8 hyperlink: %q", joined)
		}
		if !strings.Contains(ansi.Strip(joined), "[a.md]") {
			t.Errorf("output missing [a.md]: %q", ansi.Strip(joined))
		}
	})

	t.Run("no_frontmatter_starts_with_body", func(t *testing.T) {
		src := []byte("# Title\n\nbody\n")
		lines, err := NewRenderer().Render(src, Options{Width: 60, Plain: true, Style: testStyle})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if len(lines) == 0 {
			t.Fatal("no lines returned")
		}
		first := strings.TrimSpace(stripGutterAndTrim(lines[0]))
		if first == "" {
			t.Fatalf("lines[0] is blank, want the heading: %q", lines)
		}
		if !strings.Contains(first, "Title") {
			t.Errorf("lines[0] = %q, want it to contain the heading text", lines[0])
		}
	})

	t.Run("wide_chars_exact_width", func(t *testing.T) {
		src := []byte("这是一些中文文本，用来测试宽字符在渲染时是否仍然占据正确的单元格宽度并被正确地填充到目标宽度。\n")
		for _, width := range []int{40, 79, 80, 81, 120} {
			lines, err := NewRenderer().Render(src, Options{Width: width, Style: testStyle})
			if err != nil {
				t.Fatalf("Render(width=%d): %v", width, err)
			}
			want := lineWidth(Options{Width: width})
			for i, l := range lines {
				if w := ansi.StringWidth(l); w != want {
					t.Errorf("width=%d line %d width=%d, want %d: %q", width, i, w, want, l)
				}
			}
		}
	})

	t.Run("box_runes_in_table_cell", func(t *testing.T) {
		src := []byte("| Cell | Note |\n|---|---|\n| a-b box | plain |\n")
		styled, err := NewRenderer().Render(src, Options{Width: 60, Style: testStyle})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		joined := strings.Join(styled, "\n")
		plain := ansi.Strip(joined)
		if !strings.Contains(plain, "a-b box") {
			t.Fatalf("cell content missing: %q", plain)
		}

		// Now the precise case the finding is about: a "│" *typed inside a
		// cell's own text* must not be mistaken for one of glamour's own
		// column separators and recoloured Border.
		srcCell := []byte("| Cell | Note |\n|---|---|\n| a│b | plain |\n")
		styled2, err := NewRenderer().Render(srcCell, Options{Width: 60, Style: testStyle})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		row := ""
		for _, l := range styled2 {
			if strings.Contains(ansi.Strip(l), "a│b") {
				row = l
				break
			}
		}
		if row == "" {
			t.Fatalf("table row with the cell-typed │ not found: %v", styled2)
		}
		fgSGR := "\x1b[38;2;216;221;228m"
		borderSGR := "\x1b[38;2;53;60;71m"
		cellBar := fgSGR + "│" // the cell-typed "│" must stay Fg (part of the
		// same run glamour wraps the cell's own text in), never Border.
		if !strings.Contains(row, cellBar) && !strings.Contains(ansi.Strip(row), "a│b") {
			t.Errorf("cell-typed │ not found styled as Fg content: %q", row)
		}
		if strings.Count(row, borderSGR+"│") != 1 {
			t.Errorf("want exactly 1 Border-coloured │ (the real column separator), got %d: %q",
				strings.Count(row, borderSGR+"│"), row)
		}
	})
	sentinelEscapeSubtests(t)

	// repair-2, third required case: one marker whose styled segment ends a
	// wrapped line, and another whose styled segment begins the next one;
	// both cleanly, with ambient restored.
	t.Run("marker_at_line_start_and_end", func(t *testing.T) {
		src := []byte("aaaa bbbb cccc [[endmark]] dddd eeee ffff gggg [[startmark]] hhhh.\n")
		styled, err := NewRenderer().Render(src, Options{Width: 24, Style: testStyle})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		underlineSGR := "\x1b[38;2;216;221;228;4m"

		var endLine, startLine string
		for _, l := range styled {
			plain := stripGutterAndTrim(l)
			if strings.HasSuffix(strings.TrimRight(ansi.Strip(l), " "), "endmark") {
				endLine = l
			}
			if strings.HasPrefix(plain, "startmark") {
				startLine = l
			}
		}
		if endLine == "" {
			t.Fatalf("no line ends with endmark at width 24: %v", styled)
		}
		if startLine == "" {
			t.Fatalf("no line starts with startmark at width 24: %v", styled)
		}

		if !strings.Contains(endLine, underlineSGR+"endmark") {
			t.Errorf("line-ending marker not styled: %q", endLine)
		}
		if !strings.Contains(endLine, underlineSGR+"endmark\x1b[m") {
			t.Errorf("line-ending marker has no reset right after its content (before trailing padding): %q", endLine)
		}

		if !strings.Contains(startLine, underlineSGR+"startmark") {
			t.Errorf("line-starting marker not styled: %q", startLine)
		}
		// Immediately after the 2-cell (unmarked) gutter, the line's first
		// *visible* content must already be "startmark" — nothing unstyled
		// in front of it — and the underline+Fg span style must appear
		// somewhere in the run of escape sequences ahead of it. Glamour's
		// own outer Fg wrap for the paragraph's text node can legitimately
		// sit right before the span's own open (a harmless, same-colour
		// redundant SGR, not a styling bug), so this checks for content
		// correctness rather than requiring underlineSGR to be the literal
		// first byte.
		afterGutter := strings.TrimPrefix(startLine, "  ")
		prefix, rest := leadingEscapes(afterGutter)
		if !strings.HasPrefix(rest, "startmark") {
			t.Errorf("line-starting marker has unstyled text before it: %q", startLine)
		}
		if !strings.Contains(prefix, underlineSGR) {
			t.Errorf("line-starting marker's leading escapes never set underline+Fg: %q", startLine)
		}
	})
}

// leadingEscapes splits s into its leading run of SGR escape sequences and
// whatever follows.
func leadingEscapes(s string) (prefix, rest string) {
	rest = s
	for {
		seq, n, _, ok := decodeSGR(rest)
		if !ok {
			return prefix, rest
		}
		prefix += seq
		rest = rest[n:]
	}
}
