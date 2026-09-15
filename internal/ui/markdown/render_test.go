package markdown

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// testStyle is a small, fixed Style used by every TestRender subtest: real
// hex values (not the zero string), so a missing Color assignment anywhere
// in style.go would show up as an unstyled span instead of silently
// matching an empty default.
var testStyle = Style{
	Dark:    true,
	Fg:      "#D8DDE4",
	Muted:   "#8C95A2",
	Faint:   "#5E6672",
	Border:  "#353C47",
	Accent:  "#7AB2F2",
	Good:    "#6BC28E",
	Warn:    "#E2B45A",
	Bad:     "#EF7F76",
	Heading: "#C3A0F0",
	Code:    "#6CC7C9",
}

// stripGutterAndTrim removes a rendered line's 2-cell gutter and trims
// surrounding whitespace, for tests that check body text content rather
// than exact layout.
func stripGutterAndTrim(line string) string {
	plain := ansi.Strip(line)
	if len(plain) >= 2 {
		plain = plain[2:]
	}
	return strings.TrimSpace(plain)
}

// normalizeBody drops the gutter from every line, trims it, discards blank
// lines and joins what remains with single spaces — the frontmatter test's
// "rest of the output" comparison, factored out for reuse.
func normalizeBody(lines []string) string {
	var words []string
	for _, l := range lines {
		t := stripGutterAndTrim(l)
		if t == "" {
			continue
		}
		words = append(words, t)
	}
	return strings.Join(words, " ")
}

func rightTrim(s string) string {
	return strings.TrimRight(s, " ")
}

func TestRender(t *testing.T) {
	t.Run("frontmatter_title_and_meta", func(t *testing.T) {
		src := []byte(`---
title: Kv Cache
type: concept
tags: [inference, memory]
confidence: high
updated: 2026-09-01
---
# KV cache

Stores keys and values. ^[raw/articles/notes.md] See [[attention]].
`)
		r := NewRenderer()
		lines, err := r.Render(src, Options{Width: 40, Plain: true, Style: testStyle})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if len(lines) < 4 {
			t.Fatalf("got %d lines, want at least 4: %q", len(lines), lines)
		}

		wantHeader := []string{
			"  Kv Cache",
			"  concept · inference, memory ·",
			"  confidence high · updated 2026-09-01",
			"",
		}
		for i, want := range wantHeader {
			if got := rightTrim(lines[i]); got != want {
				t.Errorf("line %d = %q, want %q", i+1, got, want)
			}
		}

		for i, l := range lines {
			if w := ansi.StringWidth(l); w != 40 {
				t.Errorf("line %d width = %d, want 40 (%q)", i+1, w, l)
			}
		}

		gotRest := normalizeBody(lines[4:])
		wantRest := "KV cache Stores keys and values. [notes.md] See attention."
		if gotRest != wantRest {
			t.Errorf("rest = %q, want %q", gotRest, wantRest)
		}
	})

	t.Run("provenance_to_basename", func(t *testing.T) {
		src := []byte("A source. ^[raw/articles/deep/report.md] Confirmed.\n")
		r := NewRenderer()
		lines, err := r.Render(src, Options{Width: 60, Plain: true, Style: testStyle})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		got := normalizeBody(lines)
		if !strings.Contains(got, "[report.md]") {
			t.Errorf("output %q does not contain [report.md]", got)
		}
		if strings.Contains(got, "raw/articles") {
			t.Errorf("output %q still contains the provenance path", got)
		}
		if strings.Contains(got, "^[") {
			t.Errorf("output %q still contains a raw provenance marker", got)
		}
	})

	t.Run("wikilink_to_plain_text", func(t *testing.T) {
		src := []byte("See [[attention|Attention Mechanism]] for detail.\n")
		r := NewRenderer()
		lines, err := r.Render(src, Options{Width: 60, Plain: true, Style: testStyle})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		got := normalizeBody(lines)
		if !strings.Contains(got, "attention") {
			t.Errorf("output %q does not contain the wikilink target", got)
		}
		if strings.Contains(got, "Attention Mechanism") {
			t.Errorf("output %q still contains the wikilink label", got)
		}
		if strings.Contains(got, "[[") || strings.Contains(got, "]]") {
			t.Errorf("output %q still contains wikilink brackets", got)
		}
	})

	t.Run("headings_without_marks", func(t *testing.T) {
		src := []byte("# A Heading\n\n## A subheading\n\nBody text.\n")
		r := NewRenderer()
		lines, err := r.Render(src, Options{Width: 60, Plain: true, Style: testStyle})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		found := false
		for _, l := range lines {
			trimmed := stripGutterAndTrim(l)
			if strings.HasPrefix(trimmed, "#") {
				t.Errorf("line %q still starts with a heading mark", l)
			}
			if strings.Contains(trimmed, "A Heading") {
				found = true
			}
		}
		if !found {
			t.Fatalf("heading text not found in %q", lines)
		}
	})

	t.Run("gutter_marks_changed_blocks", func(t *testing.T) {
		changedLine := "This block was added by the change."
		src := []byte("Unrelated first paragraph.\n\n" + changedLine + "\n\nUnrelated last paragraph.\n")
		r := NewRenderer()
		lines, err := r.Render(src, Options{
			Width:   60,
			Style:   testStyle,
			Changed: []string{changedLine},
		})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}

		sawMarked, sawUnmarkedContent := false, false
		for _, l := range lines {
			plain := ansi.Strip(l)
			marked := strings.HasPrefix(plain, "▎")
			content := strings.TrimSpace(plain[min(2, len(plain)):])
			if marked {
				sawMarked = true
				if !strings.Contains(content, "added by the change") && content != "" {
					t.Errorf("marked line has unexpected content: %q", plain)
				}
			} else if strings.Contains(content, "Unrelated") {
				sawUnmarkedContent = true
			}
		}
		if !sawMarked {
			t.Fatalf("no line carried the ▎ gutter in %q", lines)
		}
		if !sawUnmarkedContent {
			t.Fatalf("unrelated paragraphs should be unmarked in %q", lines)
		}
	})

	t.Run("plain_equals_stripped", func(t *testing.T) {
		src := []byte("---\ntitle: Demo\ntype: note\n---\n# Heading\n\nSome **bold** and *italic* and `code` and ^[raw/a.md] and [[b|B]].\n")
		opts := Options{Width: 72, Style: testStyle}
		r1 := NewRenderer()
		styled, err := r1.Render(src, opts)
		if err != nil {
			t.Fatalf("Render styled: %v", err)
		}
		opts.Plain = true
		r2 := NewRenderer()
		plain, err := r2.Render(src, opts)
		if err != nil {
			t.Fatalf("Render plain: %v", err)
		}
		if len(styled) != len(plain) {
			t.Fatalf("line count differs: styled=%d plain=%d", len(styled), len(plain))
		}
		for i := range styled {
			want := ansi.Strip(styled[i])
			if plain[i] != want {
				t.Errorf("line %d: plain=%q, want ansi.Strip(styled)=%q", i, plain[i], want)
			}
		}
	})

	t.Run("measure_caps_at_100", func(t *testing.T) {
		src := []byte(strings.Repeat("word ", 40) + "\n")
		r := NewRenderer()
		lines, err := r.Render(src, Options{Width: 200, Style: testStyle})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if len(lines) == 0 {
			t.Fatal("no lines returned")
		}
		for i, l := range lines {
			if w := ansi.StringWidth(l); w != 100 {
				t.Errorf("line %d width = %d, want 100 (Measure 0 caps at 100)", i, w)
			}
		}
	})

	t.Run("every_line_exact_width", func(t *testing.T) {
		src := []byte("---\ntitle: T\ntype: x\n---\n# H\n\nA paragraph with enough words to wrap onto more than one line at a narrow width, definitely.\n\n| a | b |\n|---|---|\n| 1 | 2 |\n")
		cases := []struct {
			width, measure int
			plain          bool
		}{
			{40, 0, false},
			{40, 0, true},
			{80, 30, false},
			{200, 0, false},
		}
		for _, c := range cases {
			r := NewRenderer()
			lines, err := r.Render(src, Options{Width: c.width, Measure: c.measure, Plain: c.plain, Style: testStyle})
			if err != nil {
				t.Fatalf("Render(%+v): %v", c, err)
			}
			want := lineWidth(Options{Width: c.width, Measure: c.measure})
			for i, l := range lines {
				if w := ansi.StringWidth(l); w != want {
					t.Errorf("case %+v line %d width = %d, want %d: %q", c, i, w, want, l)
				}
			}
		}
	})

	t.Run("memo_returns_copy", func(t *testing.T) {
		src := []byte("# Title\n\nSome body text.\n")
		r := NewRenderer()
		opts := Options{Width: 50, Style: testStyle}

		first, err := r.Render(src, opts)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if len(first) == 0 {
			t.Fatal("no lines returned")
		}
		original := first[0]
		first[0] = "MUTATED"

		second, err := r.Render(src, opts)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if second[0] != original {
			t.Errorf("second call saw the mutation: got %q, want %q", second[0], original)
		}
		if second[0] == "MUTATED" {
			t.Errorf("cache returned an alias, not a copy")
		}
	})

	t.Run("list_and_fence_continuity", func(t *testing.T) {
		changedItem := "Second item, changed by this edit."
		src := []byte("- First item, unchanged.\n" +
			"- " + changedItem + "\n" +
			"- Third item, unchanged.\n" +
			"\n" +
			"```\n" +
			"line one\n" +
			"\n" +
			"line three\n" +
			"```\n")
		r := NewRenderer()
		lines, err := r.Render(src, Options{
			Width:   72,
			Plain:   true,
			Style:   testStyle,
			Changed: []string{changedItem},
		})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		got := normalizeBody(lines)

		wantInOrder := []string{
			"First item, unchanged.",
			"Second item, changed by this edit.",
			"Third item, unchanged.",
			"line one",
			"line three",
		}
		pos := 0
		for _, want := range wantInOrder {
			idx := strings.Index(got[pos:], want)
			if idx < 0 {
				t.Fatalf("output missing %q in order (after position %d); got %q", want, pos, got)
			}
			if strings.Count(got, want) != 1 {
				t.Errorf("%q appears %d times, want exactly once", want, strings.Count(got, want))
			}
			pos += idx + len(want)
		}
	})
}
