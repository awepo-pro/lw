package vault_test

import (
	"testing"

	"github.com/awepo-pro/lw/internal/vault"
)

// TestFindFrontmatterBlock pins FC.1 from workflow 081:
// vault.FindFrontmatterBlock(text string) (key string, line int, ok bool)
// detects a line that is exactly "---" (after trimming trailing spaces, tabs, and CR)
// immediately followed by a line starting with one of the 9 recognized keys
// (title, created, updated, type, tags, sources, confidence, contested, aliases) + ":".
// line is the 1-based line number of that "---" line in text.
func TestFindFrontmatterBlock(t *testing.T) {
	t.Run("empty text", func(t *testing.T) {
		key, line, ok := vault.FindFrontmatterBlock("")
		if ok || key != "" || line != 0 {
			t.Errorf("got (%q, %d, %v), want (\"\", 0, false)", key, line, ok)
		}
	})

	t.Run("no dashes", func(t *testing.T) {
		key, line, ok := vault.FindFrontmatterBlock("hello\nworld\n")
		if ok || key != "" || line != 0 {
			t.Errorf("got (%q, %d, %v), want (\"\", 0, false)", key, line, ok)
		}
	})

	t.Run("dashes between prose not a frontmatter block", func(t *testing.T) {
		key, line, ok := vault.FindFrontmatterBlock("a\n\n---\n\nb\n")
		if ok || key != "" || line != 0 {
			t.Errorf("got (%q, %d, %v), want (\"\", 0, false)", key, line, ok)
		}
	})

	t.Run("dashes followed by non key line", func(t *testing.T) {
		key, line, ok := vault.FindFrontmatterBlock("---\nnot a key line\n")
		if ok || key != "" || line != 0 {
			t.Errorf("got (%q, %d, %v), want (\"\", 0, false)", key, line, ok)
		}
	})

	t.Run("unrecognized key line", func(t *testing.T) {
		key, line, ok := vault.FindFrontmatterBlock("---\nauthor: Alice\n")
		if ok || key != "" || line != 0 {
			t.Errorf("got (%q, %d, %v), want (\"\", 0, false)", key, line, ok)
		}
	})

	t.Run("all 9 recognized keys", func(t *testing.T) {
		keys := []string{
			"title",
			"created",
			"updated",
			"type",
			"tags",
			"sources",
			"confidence",
			"contested",
			"aliases",
		}
		for _, k := range keys {
			text := "---\n" + k + ": value\n---\nbody\n"
			gotKey, line, ok := vault.FindFrontmatterBlock(text)
			if !ok {
				t.Errorf("key %q: ok = false, want true", k)
			}
			if gotKey != k {
				t.Errorf("key %q: gotKey = %q, want %q", k, gotKey, k)
			}
			if line != 1 {
				t.Errorf("key %q: line = %d, want 1", k, line)
			}
		}
	})

	t.Run("1-based line number for offset block", func(t *testing.T) {
		text := "Lead line 1.\nLead line 2.\n---\ntitle: Sub\n---\nBody.\n"
		gotKey, line, ok := vault.FindFrontmatterBlock(text)
		if !ok {
			t.Fatal("ok = false, want true")
		}
		if gotKey != "title" {
			t.Errorf("gotKey = %q, want \"title\"", gotKey)
		}
		if line != 3 {
			t.Errorf("line = %d, want 3", line)
		}
	})

	t.Run("trailing spaces tabs and carriage return on dashes", func(t *testing.T) {
		text := "---\t  \r\ncreated: 2026-09-01\r\n---\r\nBody\r\n"
		gotKey, line, ok := vault.FindFrontmatterBlock(text)
		if !ok {
			t.Fatal("ok = false, want true")
		}
		if gotKey != "created" {
			t.Errorf("gotKey = %q, want \"created\"", gotKey)
		}
		if line != 1 {
			t.Errorf("line = %d, want 1", line)
		}
	})

	t.Run("pre-fix infiniband shape", func(t *testing.T) {
		body := "---\ntitle: X\ncreated: 2026-09-01\n---\n\nLead.\n\n## Abstract\n\nContent.\n"
		gotKey, line, ok := vault.FindFrontmatterBlock(body)
		if !ok {
			t.Fatal("ok = false, want true")
		}
		if gotKey != "title" {
			t.Errorf("gotKey = %q, want \"title\"", gotKey)
		}
		if line != 1 {
			t.Errorf("line = %d, want 1", line)
		}
	})
}
