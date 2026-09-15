package markdown

import (
	"strings"
	"testing"
)

// sentinelEscapeSubtests is TestRenderRealMarkdownKeepsMeaning's escape-machinery
// half, moved here whole (a pure move, file-size split): the marker-wrap span
// styling case and every sentinel-encoding round-trip case.
func sentinelEscapeSubtests(t *testing.T) {
	// repair-2 R1: restyleSpans did not carry an open marker span across a
	// line break glamour's own word-wrap inserted, so a continuation
	// segment silently lost the token style.
	t.Run("marker_wrapped_across_lines_keeps_style", func(t *testing.T) {
		src := []byte("Some words before a very long link [[a-very-long-wikilink-target-name-that-wraps]] " +
			"and a provenance ^[raw/articles/a-very-long-provenance-file-name-that-will-wrap.md] after it.\n")
		fgSGR := "\x1b[38;2;216;221;228m"
		underlineSGR := "\x1b[38;2;122;178;242;4m" // Accent + underline (W5 F3 role)
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
		// the continuation after it — must carry underline+Accent.
		for _, frag := range []string{"a-very-", "long-wikilink-target-name-that-wraps"} {
			if !strings.Contains(joined, underlineSGR+frag) {
				t.Errorf("wikilink fragment %q is not styled underline+Accent: %q", frag, joined)
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
		underlineSGR := "\x1b[38;2;122;178;242;4m" // Accent + underline (W5 F3 role)
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
		underlineSGR := "\x1b[38;2;122;178;242;4m" // Accent + underline (W5 F3 role)
		faintSGR := "\x1b[38;2;94;102;114m"
		if !strings.Contains(joined, underlineSGR+"R&D.md") {
			t.Errorf("wikilink R&D.md not styled underline+Accent: %q", joined)
		}
		if !strings.Contains(joined, underlineSGR+"a~~b~~c") {
			t.Errorf("wikilink a~~b~~c not styled underline+Accent: %q", joined)
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
