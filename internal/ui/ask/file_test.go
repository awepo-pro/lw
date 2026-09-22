// file_test.go pins fileMessage's byte format (009 contract §3.4) against
// literal expected strings, and the `?` overlay's fourth entry (§3.5).
package ask

import (
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/ui"
)

func TestFileMessage(t *testing.T) {
	t.Run("no_candidates", func(t *testing.T) {
		const want = "File this answer as a query page.\n" +
			"\n" +
			"Question:\n" +
			"What is a kv cache?\n" +
			"\n" +
			"Answer:\n" +
			"It is the attention cache.^[raw/articles/kv-cache-explained.md]\n" +
			"\n" +
			"Existing query pages that may already answer it: none found."
		got := fileMessage("What is a kv cache?",
			"It is the attention cache.^[raw/articles/kv-cache-explained.md]", nil)
		if got != want {
			t.Fatalf("fileMessage:\n got  %q\n want %q", got, want)
		}
		if strings.HasSuffix(got, "\n") {
			t.Fatalf("fileMessage ends in a newline; the contract wants none")
		}
	})

	t.Run("two_candidates", func(t *testing.T) {
		const want = "File this answer as a query page.\n" +
			"\n" +
			"Question:\n" +
			"What is a kv cache?\n" +
			"\n" +
			"Answer:\n" +
			"First paragraph.^[wiki/concepts/kv-cache.md]\n\n" +
			"Second paragraph.^[raw/articles/kv-cache-explained.md]\n" +
			"\n" +
			"Existing query pages that may already answer it:\n" +
			"- wiki/queries/kv-cache.md — KV Cache\n" +
			"- wiki/queries/attention.md — Attention"
		hits := []index.Hit{
			{Path: "wiki/queries/kv-cache.md", Title: "KV Cache"},
			{Path: "wiki/queries/attention.md", Title: "Attention"},
		}
		got := fileMessage("What is a kv cache?",
			"First paragraph.^[wiki/concepts/kv-cache.md]\n\nSecond paragraph.^[raw/articles/kv-cache-explained.md]", hits)
		if got != want {
			t.Fatalf("fileMessage:\n got  %q\n want %q", got, want)
		}
	})
}

func TestOverlayHelpFileEntry(t *testing.T) {
	m := New(newTestDeps(t)).(*Model)
	title, entries := m.OverlayHelp()
	if title != "Ask" {
		t.Fatalf("OverlayHelp title = %q, want \"Ask\"", title)
	}
	want := []ui.HelpEntry{
		{Key: "enter", Desc: "send"},
		{Key: "↑/↓", Desc: "select tool call"},
		{Key: "ctrl+r", Desc: "open review"},
		{Key: "ctrl+s", Desc: "save last answer into the wiki"},
		// 027 T2, after the ctrl+s entry as the contract fixes it. This pin
		// held the four pre-027 entries byte for byte; the fifth entry is
		// the contract's own addition, so the pin grows with it — position
		// and wording included.
		{Key: "ctrl+p", Desc: "show/hide sources"},
	}
	if len(entries) != len(want) {
		t.Fatalf("OverlayHelp has %d entries, want %d:\n%+v", len(entries), len(want), entries)
	}
	for i, w := range want {
		if entries[i] != w {
			t.Fatalf("OverlayHelp entry %d = %+v, want %+v", i, entries[i], w)
		}
	}
}
