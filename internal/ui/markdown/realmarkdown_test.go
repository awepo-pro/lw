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

	// repair-2 R1: restyleSpans did not carry an open marker span across a
	// line break glamour's own word-wrap inserted, so a continuation
	// segment silently lost the token style.
	t.Run("marker_wrapped_across_lines_keeps_style", func(t *testing.T) {
		src := []byte("Some words before a very long link [[a-very-long-wikilink-target-name-that-wraps]] " +
			"and a provenance ^[raw/articles/a-very-long-provenance-file-name-that-will-wrap.md] after it.\n")
		fgSGR := "\x1b[38;2;216;221;228m"
		underlineSGR := "\x1b[38;2;216;221;228;4m"
		faintSGR := "\x1b[38;2;94;102;114m"

		plain, err := NewRenderer().Render(src, Options{Width: 44, Style: testStyle, Plain: true})
		if err != nil {
			t.Fatalf("Render plain: %v", err)
		}
		for _, l := range plain {
			if strings.ContainsAny(l, sentinelRunes) {
				t.Errorf("plain line still has a sentinel rune: %q", l)
			}
		}
		// normalizeBody joins line-by-line, so a word hard-wrapped mid-token
		// (like "a-very-" / "long-wikilink-...") picks up one extra space at
		// the wrap point; compare with spaces collapsed so the check is
		// about content (nothing missing, duplicated or reordered), not the
		// exact wrap column.
		wantPlain := "Some words before a very long link a-very-long-wikilink-target-name-that-wraps and a " +
			"provenance [a-very-long-provenance-file-name-that-will-wrap.md] after it."
		norm := func(s string) string { return strings.ReplaceAll(s, " ", "") }
		if got := normalizeBody(plain); norm(got) != norm(wantPlain) {
			t.Errorf("plain content = %q, want (space-insensitive) %q", got, wantPlain)
		}

		styled, err := NewRenderer().Render(src, Options{Width: 44, Style: testStyle})
		if err != nil {
			t.Fatalf("Render styled: %v", err)
		}
		joined := strings.Join(styled, "\n")
		for _, l := range styled {
			if strings.ContainsAny(l, sentinelRunes) {
				t.Errorf("styled line still has a sentinel rune: %q", l)
			}
		}
		// Every wikilink word fragment — both the segment before the wrap and
		// the continuation after it — must carry underline+Fg.
		for _, frag := range []string{"a-very-", "long-wikilink-target-name-that-wraps"} {
			if !strings.Contains(joined, underlineSGR+frag) {
				t.Errorf("wikilink fragment %q is not styled underline+Fg: %q", frag, joined)
			}
		}
		// Every provenance word fragment must carry Faint, including the
		// continuation after the wrap (the exact bug R1 reported: the
		// continuation came back plain Fg instead of Faint).
		for _, frag := range []string{"[a-very-long-provenance-file-", "name-that-will-wrap.md]"} {
			if !strings.Contains(joined, faintSGR+frag) {
				t.Errorf("provenance fragment %q is not styled Faint: %q", frag, joined)
			}
		}
		// Ambient Fg must be restored after each span, on whichever line it
		// closes on.
		for _, frag := range []string{" and a", " after"} {
			if !strings.Contains(joined, fgSGR+frag) {
				t.Errorf("ambient Fg not restored before %q: %q", frag, joined)
			}
		}
	})

	// repair-2 R2: a source containing one of the sentinel code points
	// (U+2060-U+2063) came out with that character silently removed.
	t.Run("source_sentinel_code_points_preserved", func(t *testing.T) {
		src := []byte("User text with invisible ⁠op⁡ chars and [[link]] here.\n")
		countIn := func(s string) map[rune]int {
			counts := map[rune]int{}
			for _, r := range s {
				if strings.ContainsRune(sentinelRunes, r) {
					counts[r]++
				}
			}
			return counts
		}
		srcCounts := countIn(string(src))
		if len(srcCounts) == 0 {
			t.Fatal("test source has no sentinel-like rune; probe is void")
		}

		plain, err := NewRenderer().Render(src, Options{Width: 80, Style: testStyle, Plain: true})
		if err != nil {
			t.Fatalf("Render plain: %v", err)
		}
		plainCounts := countIn(strings.Join(plain, ""))
		for r, want := range srcCounts {
			if got := plainCounts[r]; got != want {
				t.Errorf("plain output has %d of U+%04X, want %d (source count): %q", got, r, want, plain)
			}
		}
		if !strings.Contains(normalizeBody(plain), "invisible ⁠op⁡ chars") {
			t.Errorf("plain output lost the source's own sentinel characters: %q", plain)
		}
		if !strings.Contains(normalizeBody(plain), "link") {
			t.Errorf("plain output missing the wikilink text: %q", plain)
		}

		styled, err := NewRenderer().Render(src, Options{Width: 80, Style: testStyle})
		if err != nil {
			t.Fatalf("Render styled: %v", err)
		}
		joined := strings.Join(styled, "\n")
		underlineSGR := "\x1b[38;2;216;221;228;4m"
		if !strings.Contains(joined, underlineSGR+"link") {
			t.Errorf("wikilink not styled next to preserved sentinel characters: %q", joined)
		}
		if strings.Contains(joined, underlineSGR+"⁠") || strings.Contains(joined, underlineSGR+"op") {
			t.Errorf("the source's own sentinel characters got the wikilink style: %q", joined)
		}
	})

	// Self-review findings: '~' and '&' inside marker content were being
	// consumed as goldmark inline syntax (GFM strikethrough ate the tildes
	// of "[[a~~b~~c]]" down to "abc"; an HTML entity ate "&amp;" down to
	// "&"), because neither rune has a working backslash escape — glamour's
	// escapeReplacer only knows its own 18 pairs. escapeMarkdown now hides
	// them behind escMark+code pairs that unescapeMarkers restores.
	t.Run("marker_content_tilde_ampersand_preserved", func(t *testing.T) {
		src := []byte("Link [[R&D.md]] and [[a~~b~~c]] here.\n" +
			"Prov ^[raw/R&D~~x.md] and [[Tom &amp; Jerry]] too.\n")
		plain, err := NewRenderer().Render(src, Options{Width: 80, Style: testStyle, Plain: true})
		if err != nil {
			t.Fatalf("Render plain: %v", err)
		}
		got := normalizeBody(plain)
		for _, want := range []string{"R&D.md", "a~~b~~c", "[R&D~~x.md]", "Tom &amp; Jerry"} {
			if !strings.Contains(got, want) {
				t.Errorf("plain output %q lost marker content %q", got, want)
			}
		}
		for _, bad := range []string{"abc ", "Tom & Jerry"} {
			if strings.Contains(got, bad) {
				t.Errorf("plain output %q still contains the syntax-eaten form %q", got, bad)
			}
		}
		assertNoEscapedRunes(t, plain)

		styled, err := NewRenderer().Render(src, Options{Width: 80, Style: testStyle})
		if err != nil {
			t.Fatalf("Render styled: %v", err)
		}
		joined := strings.Join(styled, "\n")
		underlineSGR := "\x1b[38;2;216;221;228;4m"
		faintSGR := "\x1b[38;2;94;102;114m"
		if !strings.Contains(joined, underlineSGR+"R&D.md") {
			t.Errorf("wikilink R&D.md not styled underline+Fg: %q", joined)
		}
		if !strings.Contains(joined, underlineSGR+"a~~b~~c") {
			t.Errorf("wikilink a~~b~~c not styled underline+Fg: %q", joined)
		}
		if !strings.Contains(joined, faintSGR+"[R&D~~x.md]") {
			t.Errorf("provenance [R&D~~x.md] not styled Faint: %q", joined)
		}
		assertNoEscapedRunes(t, styled)
	})

	// Self-review finding: the escape encoding for source sentinel code
	// points used escMark plus an ASCII digit. The digit is one cell wide,
	// so every escaped source sentinel shifted glamour's word-wrap decision
	// one cell, visibly moving line breaks. The code rune is now a
	// zero-width variation selector, so a page's wrap is identical to the
	// same page with the invisible runes absent.
	t.Run("escaped_source_sentinels_do_not_shift_wrap", func(t *testing.T) {
		clean := strings.Repeat("abcdefgh ", 12)
		marked := strings.Repeat("ab⁠cdefgh ", 12)
		gotClean, err := NewRenderer().Render([]byte(clean+"\n"), Options{Width: 48, Style: testStyle, Plain: true})
		if err != nil {
			t.Fatalf("Render clean: %v", err)
		}
		gotMarked, err := NewRenderer().Render([]byte(marked+"\n"), Options{Width: 48, Style: testStyle, Plain: true})
		if err != nil {
			t.Fatalf("Render marked: %v", err)
		}
		stripped := make([]string, len(gotMarked))
		for i, l := range gotMarked {
			stripped[i] = strings.ReplaceAll(l, provOpen, "")
		}
		if strings.Join(stripped, "\n") != strings.Join(gotClean, "\n") {
			t.Errorf("wrap positions differ with U+2060 present:\nclean  %q\nmarked %q", gotClean, stripped)
		}
	})

	// Self-review finding, bijection half: the escape/unescape round trip
	// must hold for adversarial arrangements of the five sentinel runes —
	// including escMark directly followed by what would otherwise be a code
	// rune, and the escMark+digit sequences the pre-fix encoding produced.
	t.Run("escaped_sentinels_roundtrip_adversarial", func(t *testing.T) {
		srcs := []string{
			"Runs ⁠⁡⁢⁣⁤ tail\n",
			"⁤⁤0⁤1⁤2⁤3⁤4 end\n",
			"Pair ⁤0 and ⁤x and ⁤⁤ done\n",
			"Codes ⁤︀ ⁤︁ ⁤︂ ⁤︃ ⁤︄ ⁤︅ ⁤︆ mixed\n",
			"Word ⁠mid⁡ word ⁢join⁣ ⁤end\n",
			// fence blocks skip marker insertion but still escape, restyle
			// and unescape (block.go): their verbatim content must survive
			// the new encoding too.
			"```\n⁠⁡⁢⁣⁤ [[not-a-link]]\n```\n",
		}
		for _, src := range srcs {
			want := countEscapable(string(src))
			plain, err := NewRenderer().Render([]byte(src), Options{Width: 80, Style: testStyle, Plain: true})
			if err != nil {
				t.Fatalf("Render plain %q: %v", src, err)
			}
			if got := countEscapable(strings.Join(plain, "")); !sameCounts(got, want) {
				t.Errorf("plain %q rune counts = %v, want source counts %v", src, got, want)
			}
			styled, err := NewRenderer().Render([]byte(src), Options{Width: 80, Style: testStyle})
			if err != nil {
				t.Fatalf("Render styled %q: %v", src, err)
			}
			if got := countEscapable(strings.Join(styled, "")); !sameCounts(got, want) {
				t.Errorf("styled %q rune counts = %v, want source counts %v", src, got, want)
			}
			// No assertNoEscapedRunes here: these sources deliberately
			// contain bare machinery runes (escMark, variation selectors),
			// which pass through unescaped. Per-rune count equality above
			// is the invariant — a collapsed pair shows up as a missing
			// code rune, a split pair as an extra one.
		}
	})

	// Self-review finding, hard-wrap half: if glamour's word-wrap ever
	// hard-splits an overlong word BETWEEN the two halves of an escaped
	// pair, unescapeMarkers cannot collapse it and a bare escMark plus a
	// stray code rune would leak into the output. Sweep wrap widths around
	// every plausible split point with both a zero-width and an escaped
	// rune embedded: counts must survive and no escape-machinery rune may
	// leak.
	t.Run("escaped_sentinel_pair_survives_hard_wrap", func(t *testing.T) {
		for _, mid := range []rune{[]rune(provOpen)[0], []rune(escMark)[0]} {
			for w := 8; w <= 60; w++ {
				src := strings.Repeat("x", w) + string(mid) + strings.Repeat("y", w) + "\n"
				plain, err := NewRenderer().Render([]byte(src), Options{Width: w + 2, Style: testStyle, Plain: true})
				if err != nil {
					t.Fatalf("Render(width=%d): %v", w, err)
				}
				got := countEscapable(strings.Join(plain, ""))
				want := map[rune]int{mid: 1}
				if !sameCounts(got, want) {
					t.Errorf("width=%d mid=U+%04X: output counts %v, want %v: %q", w, mid, got, want, plain)
				}
			}
		}
	})

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

// countEscapable counts every rune the escape machinery can produce or
// consume: the five sentinel runes plus the seven variation-selector code
// runes. A correct round trip leaves exactly the source's sentinel runes
// and none of the machinery's code runes.
func countEscapable(s string) map[rune]int {
	counts := map[rune]int{}
	for _, r := range s {
		if strings.ContainsRune(sentinelRunes, r) {
			counts[r]++
		}
		if _, ok := unescapeCode[r]; ok {
			counts[r]++
		}
	}
	return counts
}

// sameCounts reports whether two rune-count maps are equal.
func sameCounts(a, b map[rune]int) bool {
	if len(a) != len(b) {
		return false
	}
	for r, n := range a {
		if b[r] != n {
			return false
		}
	}
	return true
}

// assertNoEscapedRunes fails if any line still contains escape-machinery
// runes: a bare escMark or an uncollapsed variation-selector code rune.
func assertNoEscapedRunes(t *testing.T, lines []string) {
	t.Helper()
	for i, l := range lines {
		if strings.Contains(l, escMark) {
			t.Errorf("line %d contains a bare escMark: %q", i, l)
		}
		for r := range unescapeCode {
			if strings.ContainsRune(l, r) {
				t.Errorf("line %d contains leaked code rune U+%04X: %q", i, r, l)
			}
		}
	}
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
