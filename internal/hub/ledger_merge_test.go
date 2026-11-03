package hub_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/hub"
)

// TestM7LedgerMerge verifies M7 ledger day file merge semantics and Commit behavior.
func TestM7LedgerMerge(t *testing.T) {
	ctx := context.Background()
	dev := hub.Device{ID: "dev-0001", Name: "pixel"}
	fixedTime := time.Date(2026, 11, 3, 10, 20, 30, 0, time.UTC)
	path := "ledger/2026/11/2026-11-03.md"

	t.Run("verbatim union and append with commit", func(t *testing.T) {
		dir := initGitRepo(t)
		initialContent := "# 2026-11-03\n2026-11-03 coffee 450\n"
		fullPath := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(initialContent), 0o644); err != nil {
			t.Fatal(err)
		}

		var commits []string
		eng, err := hub.New(hub.Options{
			Dir: dir,
			Commit: func(msg string) error {
				commits = append(commits, msg)
				return nil
			},
			Now: func() time.Time { return fixedTime },
		})
		if err != nil {
			t.Fatalf("hub.New failed: %v", err)
		}

		// Phone has coffee 450, plus duplicate lunch lines, and different txn with same ID
		phoneBody := []byte("# 2026-11-03\n2026-11-03 coffee 450\n2026-11-03 lunch 1200\n\n2026-11-03 lunch 1200\n")
		staleBase := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

		res, err := eng.Put(ctx, dev, path, staleBase, phoneBody)
		if err != nil {
			t.Fatalf("Put failed: %v", err)
		}

		if res.Result != hub.ResultMerged {
			t.Fatalf("expected result %q, got %q", hub.ResultMerged, res.Result)
		}
		if res.Path != path {
			t.Fatalf("expected path %q, got %q", path, res.Path)
		}

		expectedContent := "# 2026-11-03\n2026-11-03 coffee 450\n2026-11-03 lunch 1200\n"
		diskBytes, err := os.ReadFile(fullPath)
		if err != nil {
			t.Fatalf("failed to read disk file: %v", err)
		}
		if string(diskBytes) != expectedContent {
			t.Fatalf("unexpected content on disk:\nwant: %q\ngot:  %q", expectedContent, string(diskBytes))
		}

		expectedSHA := hexSHA256([]byte(expectedContent))
		if res.SHA256 != expectedSHA {
			t.Fatalf("expected SHA %s, got %s", expectedSHA, res.SHA256)
		}

		expectedCommit := "hub: pixel merge ledger/2026/11/2026-11-03.md"
		if len(commits) != 1 || commits[0] != expectedCommit {
			t.Fatalf("expected commit %q, got %v", expectedCommit, commits)
		}
	})

	t.Run("Y lacks final newline", func(t *testing.T) {
		dir := initGitRepo(t)
		initialContent := "# 2026-11-03\n2026-11-03 coffee 450" // no trailing \n
		fullPath := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(initialContent), 0o644); err != nil {
			t.Fatal(err)
		}

		var commits []string
		eng, err := hub.New(hub.Options{
			Dir: dir,
			Commit: func(msg string) error {
				commits = append(commits, msg)
				return nil
			},
			Now: func() time.Time { return fixedTime },
		})
		if err != nil {
			t.Fatalf("hub.New failed: %v", err)
		}

		phoneBody := []byte("2026-11-03 lunch 1200\n")
		staleBase := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

		res, err := eng.Put(ctx, dev, path, staleBase, phoneBody)
		if err != nil {
			t.Fatalf("Put failed: %v", err)
		}
		if res.Result != hub.ResultMerged {
			t.Fatalf("expected result %q, got %q", hub.ResultMerged, res.Result)
		}

		expectedContent := "# 2026-11-03\n2026-11-03 coffee 450\n2026-11-03 lunch 1200\n"
		diskBytes, err := os.ReadFile(fullPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(diskBytes) != expectedContent {
			t.Fatalf("unexpected content:\nwant: %q\ngot:  %q", expectedContent, string(diskBytes))
		}
		if len(commits) != 1 {
			t.Fatalf("expected 1 commit, got %d", len(commits))
		}
	})

	t.Run("nothing appended is no-op with no commit", func(t *testing.T) {
		dir := initGitRepo(t)
		initialContent := "# 2026-11-03\n2026-11-03 coffee 450\n"
		fullPath := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(initialContent), 0o644); err != nil {
			t.Fatal(err)
		}

		var commits []string
		eng, err := hub.New(hub.Options{
			Dir: dir,
			Commit: func(msg string) error {
				commits = append(commits, msg)
				return nil
			},
			Now: func() time.Time { return fixedTime },
		})
		if err != nil {
			t.Fatalf("hub.New failed: %v", err)
		}

		phoneBody := []byte("2026-11-03 coffee 450\n\n   \n")
		staleBase := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

		res, err := eng.Put(ctx, dev, path, staleBase, phoneBody)
		if err != nil {
			t.Fatalf("Put failed: %v", err)
		}

		if res.Result != hub.ResultMerged {
			t.Fatalf("expected result %q, got %q", hub.ResultMerged, res.Result)
		}

		diskBytes, err := os.ReadFile(fullPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(diskBytes) != initialContent {
			t.Fatalf("content should be unchanged, got: %q", string(diskBytes))
		}

		if len(commits) != 0 {
			t.Fatalf("expected 0 commits when nothing appended, got %d: %v", len(commits), commits)
		}
	})

	t.Run("non day file under ledger falls back to M5 conflict copy", func(t *testing.T) {
		dir := initGitRepo(t)
		nonDayPath := "ledger/notes.md"
		initialContent := "ledger notes v1\n"
		fullPath := filepath.Join(dir, filepath.FromSlash(nonDayPath))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(initialContent), 0o644); err != nil {
			t.Fatal(err)
		}

		var commits []string
		eng, err := hub.New(hub.Options{
			Dir: dir,
			Commit: func(msg string) error {
				commits = append(commits, msg)
				return nil
			},
			Now: func() time.Time { return fixedTime },
		})
		if err != nil {
			t.Fatalf("hub.New failed: %v", err)
		}

		phoneBody := []byte("phone ledger notes\n")
		staleBase := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

		res, err := eng.Put(ctx, dev, nonDayPath, staleBase, phoneBody)
		if err != nil {
			t.Fatalf("Put failed: %v", err)
		}

		if res.Result != hub.ResultConflictCopy {
			t.Fatalf("expected result %q, got %q", hub.ResultConflictCopy, res.Result)
		}

		expectedConflict := "ledger/notes.conflict-pixel-20261103-102030.md"
		if res.ConflictPath != expectedConflict {
			t.Fatalf("expected conflict path %q, got %q", expectedConflict, res.ConflictPath)
		}

		expectedCommit := "hub: pixel conflict ledger/notes.md -> ledger/notes.conflict-pixel-20261103-102030.md"
		if len(commits) != 1 || commits[0] != expectedCommit {
			t.Fatalf("expected commit %q, got %v", expectedCommit, commits)
		}
	})
}

func hexSHA256(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
