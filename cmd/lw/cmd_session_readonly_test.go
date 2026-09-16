package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// vaultSnapshot hashes everything under root — every path's size, mode and
// mtime, and every regular file's content — into one deterministic string,
// so a test can prove a command changed nothing at all.
func vaultSnapshot(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s|%d|%s|%d", rel, info.Size(), info.Mode(), info.ModTime().UnixNano())
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			fmt.Fprintf(&b, "|%x", sha256.Sum256(data))
		}
		b.WriteByte('\n')
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return b.String()
}

// TestSessionReadOnly pins the subtask's hard promise (005 contract §4):
// lw session reads the vault and touches nothing — it never creates
// .llmwiki, never opens a changeset, and never rewrites the index the way
// opening an engine does.
func TestSessionReadOnly(t *testing.T) {
	t.Run("vault_bytes_unchanged", func(t *testing.T) {
		root := newSessionVault(t)
		writeSession(t, root, "open", "cs-aaaa111111111111",
			changesetJSON("cs-aaaa111111111111", "2026-09-16T12:00:00Z"),
			srec("2026-09-16T12:00:00Z", "user", "what changed in this changeset?"),
			srec("2026-09-16T12:00:05Z", "assistant", "Two pages and a patch, all proposed."),
		)
		writeSession(t, root, "committed", "cs-bbbb222222222222",
			changesetJSON("cs-bbbb222222222222", "2026-09-16T11:00:00Z"),
			srec("2026-09-16T11:00:00Z", "user", "earlier question"),
			srec("2026-09-16T11:00:04Z", "assistant", "earlier answer"),
			stool("2026-09-16T11:00:09Z", "stage.status", "{}", "clean"),
		)
		before := vaultSnapshot(t, root)

		for _, args := range [][]string{
			{"session", "list", "--vault", root},
			{"session", "list", "--vault", root, "--json"},
			{"session", "show", "--vault", root},
			{"session", "show", "--vault", root, "--plain"},
			{"session", "show", "--vault", root, "--json"},
			{"session", "show", "--vault", root, "--thinking"},
			{"session", "show", "--vault", root, "cs-nope"},
			{"session", "show", "--vault", root, "cs-"},
		} {
			captureRun(t, func() int { return run(args) })
		}

		if after := vaultSnapshot(t, root); after != before {
			t.Fatalf("lw session wrote to the vault.\nbefore:\n%s\nafter:\n%s", before, after)
		}
	})

	t.Run("missing_llmwiki_is_not_created", func(t *testing.T) {
		root := newSessionVault(t)

		captureRun(t, func() int { return run([]string{"session", "list", "--vault", root}) })
		captureRun(t, func() int { return run([]string{"session", "show", "--vault", root}) })

		if _, err := os.Stat(filepath.Join(root, ".llmwiki")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf(".llmwiki exists after a read-only session run (stat err=%v): lw session created it", err)
		}
	})
}
