package markdown

import (
	"strings"
	"testing"
)

// TestSettledPrefix pins the streaming rule (workflow 005 contract §2):
// every top-level block except the last is settled, the split is a pure
// line scan (never a parse, never splitBlocks), and settled+tail always
// reconstructs buf byte for byte — blank lines between blocks included.
func TestSettledPrefix(t *testing.T) {
	t.Run("empty_buffer_is_all_tail", func(t *testing.T) {
		settled, tail := SettledPrefix("")
		if settled != "" || tail != "" {
			t.Errorf("SettledPrefix(\"\") = (%q, %q), want (\"\", \"\")", settled, tail)
		}
	})

	t.Run("one_block_is_all_tail", func(t *testing.T) {
		buf := "Just one paragraph, still streaming.\n"
		settled, tail := SettledPrefix(buf)
		if settled != "" {
			t.Errorf("settled = %q, want \"\"", settled)
		}
		if tail != buf {
			t.Errorf("tail = %q, want the whole buffer %q", tail, buf)
		}
	})

	t.Run("two_blocks_first_is_settled", func(t *testing.T) {
		settled, tail := SettledPrefix("First block.\n\nSecond block.\n")
		if settled != "First block.\n\n" {
			t.Errorf("settled = %q, want %q", settled, "First block.\n\n")
		}
		if tail != "Second block.\n" {
			t.Errorf("tail = %q, want %q", tail, "Second block.\n")
		}
	})

	t.Run("blank_lines_are_preserved", func(t *testing.T) {
		// The blank lines that precede the last block belong to settled;
		// dropping or duplicating one would corrupt a buffer redrawn every
		// stream frame.
		settled, tail := SettledPrefix("a\n\n\n\nb")
		if settled != "a\n\n\n\n" {
			t.Errorf("settled = %q, want %q", settled, "a\n\n\n\n")
		}
		if tail != "b" {
			t.Errorf("tail = %q, want %q", tail, "b")
		}
	})

	t.Run("open_fence_is_all_in_tail", func(t *testing.T) {
		buf := "para\n\n```go\nx := 1\n"
		settled, tail := SettledPrefix(buf)
		if settled != "para\n\n" {
			t.Errorf("settled = %q, want %q", settled, "para\n\n")
		}
		if strings.Contains(settled, "```") {
			t.Errorf("settled contains fence bytes: %q", settled)
		}
		if tail != "```go\nx := 1\n" {
			t.Errorf("tail = %q, want the whole open fence %q", tail, "```go\nx := 1\n")
		}
	})

	t.Run("closed_fence_then_text_is_settled", func(t *testing.T) {
		settled, tail := SettledPrefix("```go\nx\n```\n\nmore")
		if settled != "```go\nx\n```\n\n" {
			t.Errorf("settled = %q, want the closed fence plus its separating blank line", settled)
		}
		if tail != "more" {
			t.Errorf("tail = %q, want %q", tail, "more")
		}
	})

	t.Run("growing_list_stays_in_tail", func(t *testing.T) {
		// A list still being written has no blank line after it: it is the
		// last block, so it stays in tail and reflows once it closes
		// (contract §2 note 3 — correct and intended, not a bug).
		buf := "- one\n- two\n- three"
		settled, tail := SettledPrefix(buf)
		if settled != "" || tail != buf {
			t.Errorf("growing list = (%q, %q), want (\"\", %q)", settled, tail, buf)
		}

		// Once a block follows it, the list has closed and is settled.
		settled, tail = SettledPrefix("- one\n- two\n\nAfter.")
		if settled != "- one\n- two\n\n" {
			t.Errorf("closed list settled = %q, want %q", settled, "- one\n- two\n\n")
		}
		if tail != "After." {
			t.Errorf("closed list tail = %q, want %q", tail, "After.")
		}
	})

	// roundtrip_corpus is the strongest test in the subtask: for every
	// buffer shape Ask can be mid-way through, settled + tail must equal
	// buf byte for byte. Deliberately flat (no nested t.Run) so the gate's
	// frozen `--- PASS` list stays exact.
	t.Run("roundtrip_corpus", func(t *testing.T) {
		cases := []struct {
			name string
			buf  string
		}{
			{"empty", ""},
			{"lone_newline", "\n"},
			{"leading_blank_lines", "\n\n\nfirst real block\n"},
			{"trailing_blank_lines", "only block\n\n\n"},
			{"open_fence", "intro\n\n```python\ndef f():\n    return 1\n"},
			{"closed_fence", "```go\nx := 1\ny := 2\n```\n"},
			{"fence_with_blank_lines", "```text\nline one\n\nline three\n```\n\ntail paragraph"},
			{"markdown_table", "| a | b |\n|---|---|\n| 1 | 2 |\n"},
			{"ordered_list", "1. first\n2. second\n3. third\n"},
			{"unordered_list", "- one\n- two\n"},
			{"crlf_endings", "first block\r\n\r\nsecond block\r\n"},
			{"crlf_open_fence", "para\r\n\r\n```go\r\nx := 1\r\n"},
			{"no_trailing_newline", "a block that never got its newline"},
			{"fence_between_paragraphs", "before\n\n```\ncode\n```\n\nafter"},
			{"only_blank_lines", "\n\n\n"},
			{"tab_indented_fence_line", "\t```\ncode\n\t```\n"},
		}
		for _, tc := range cases {
			settled, tail := SettledPrefix(tc.buf)
			if settled+tail != tc.buf {
				t.Errorf("case %s: settled+tail = %q, want buf %q", tc.name, settled+tail, tc.buf)
			}
		}
	})
}
