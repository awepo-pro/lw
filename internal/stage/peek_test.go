package stage_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

func TestPeekOpenChangeset(t *testing.T) {
	t.Run("none_open", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		e, err := stage.OpenEngine(dir)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		defer e.Close()

		cs, err := stage.PeekOpenChangeset(dir)
		if err != nil {
			t.Fatalf("PeekOpenChangeset: %v", err)
		}
		if cs != nil {
			t.Fatalf("expected nil changeset, got %+v", cs)
		}
	})

	t.Run("one_open", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		e, err := stage.OpenEngine(dir)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		defer e.Close()

		_, err = e.OpenChangeset("test peek", stage.Author{Kind: "human"})
		if err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		_, err = e.Append(stage.Op{
			Kind:       stage.OpCreatePage,
			Path:       "wiki/concepts/peek.md",
			Content:    []byte("---\ntitle: Peek\ntype: concept\ncreated: 2026-10-22\nupdated: 2026-10-22\n---\n\n# Peek\n\nContent for Peek referencing [[wiki/concepts/kv-cache.md]] and [[wiki/concepts/transformer.md]].\n"),
			Rationale:  "test peek",
			Provenance: []string{"raw/papers/leviathan-2023.md"},
		})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}

		want, err := e.Current()
		if err != nil {
			t.Fatalf("Engine.Current: %v", err)
		}

		got, err := stage.PeekOpenChangeset(dir)
		if err != nil {
			t.Fatalf("PeekOpenChangeset: %v", err)
		}
		if got == nil {
			t.Fatal("PeekOpenChangeset returned nil, want open changeset")
		}

		if got.ID != want.ID {
			t.Errorf("ID = %q, want %q", got.ID, want.ID)
		}
		if !got.OpenedAt.Equal(want.OpenedAt) {
			t.Errorf("OpenedAt = %v, want %v", got.OpenedAt, want.OpenedAt)
		}
		if len(got.Live()) != len(want.Live()) {
			t.Fatalf("len(Live()) = %d, want %d", len(got.Live()), len(want.Live()))
		}
		for i := range got.Live() {
			if got.Live()[i].ID != want.Live()[i].ID || got.Live()[i].Path != want.Live()[i].Path {
				t.Errorf("Live()[%d] mismatch: got %+v, want %+v", i, got.Live()[i], want.Live()[i])
			}
		}
	})

	t.Run("vault_with_no_llmwiki", func(t *testing.T) {
		dir := t.TempDir()

		cs, err := stage.PeekOpenChangeset(dir)
		if err != nil {
			t.Fatalf("PeekOpenChangeset: %v", err)
		}
		if cs != nil {
			t.Fatalf("expected nil changeset, got %+v", cs)
		}

		// Verify NOTHING created on disk
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("ReadDir: %v", err)
		}
		if len(entries) != 0 {
			t.Fatalf("directory was modified: entries = %v", entries)
		}
		if _, err := os.Stat(filepath.Join(dir, ".llmwiki")); !os.IsNotExist(err) {
			t.Fatalf(".llmwiki directory was created: %v", err)
		}
	})

	t.Run("empty_root", func(t *testing.T) {
		cs, err := stage.PeekOpenChangeset("")
		if err != nil {
			t.Fatalf("PeekOpenChangeset(\"\"): %v", err)
		}
		if cs != nil {
			t.Fatalf("expected nil changeset, got %+v", cs)
		}
	})
}
