package hub_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/hub"
)

func TestFix1_LockOrderAndAtomicSymlink(t *testing.T) {
	ctx := context.Background()

	t.Run("symlink created before lock acquired in Put", func(t *testing.T) {
		outsideDir := t.TempDir()
		var eng *hub.Engine
		var dir string

		eng, dir, _ = setupEngine(t, func(o *hub.Options) {
			o.Lock = func() (func(), error) {
				subDir := filepath.Join(dir, "notes/sub")
				if err := os.Symlink(outsideDir, subDir); err != nil {
					t.Fatalf("failed to create symlink: %v", err)
				}
				return func() {}, nil
			}
		})
		if err := os.MkdirAll(filepath.Join(dir, "notes"), 0o755); err != nil {
			t.Fatal(err)
		}

		_, err := eng.Put(ctx, testDevice, "notes/sub/doc.md", hub.BaseNew, []byte("escaped\n"))
		if err == nil {
			t.Fatal("expected Put to fail when parent is a symlink, but it succeeded")
		}
		var hubErr *hub.Error
		if !errors.As(err, &hubErr) || hubErr.Code != hub.CodeOutOfScope {
			t.Fatalf("expected CodeOutOfScope error, got %v", err)
		}

		outsideFile := filepath.Join(outsideDir, "doc.md")
		if _, err := os.Stat(outsideFile); err == nil {
			t.Fatal("arbitrary file write escape: file was written outside vault!")
		}
	})

	t.Run("case collision created before lock acquired in Put", func(t *testing.T) {
		var eng *hub.Engine
		var dir string

		eng, dir, _ = setupEngine(t, func(o *hub.Options) {
			o.Lock = func() (func(), error) {
				if err := os.WriteFile(filepath.Join(dir, "notes/File.md"), []byte("existing\n"), 0o644); err != nil {
					t.Fatalf("failed to write file: %v", err)
				}
				return func() {}, nil
			}
		})
		if err := os.MkdirAll(filepath.Join(dir, "notes"), 0o755); err != nil {
			t.Fatal(err)
		}

		_, err := eng.Put(ctx, testDevice, "notes/file.md", hub.BaseNew, []byte("new\n"))
		if err == nil {
			t.Fatal("expected Put to fail with case collision, but it succeeded")
		}
		var hubErr *hub.Error
		if !errors.As(err, &hubErr) || hubErr.Code != hub.CodeCaseCollision {
			t.Fatalf("expected CodeCaseCollision error, got %v", err)
		}
	})

	t.Run("writeAtomic refuses when parent component is symlink", func(t *testing.T) {
		outsideDir := t.TempDir()
		eng, dir, _ := setupEngine(t, nil)
		if err := os.MkdirAll(filepath.Join(dir, "notes"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outsideDir, filepath.Join(dir, "notes/symdir")); err != nil {
			t.Fatal(err)
		}

		_, err := eng.Put(ctx, testDevice, "notes/symdir/doc.md", hub.BaseNew, []byte("payload\n"))
		if err == nil {
			t.Fatal("expected Put to fail when writing into symlinked directory")
		}
		var hubErr *hub.Error
		if !errors.As(err, &hubErr) || hubErr.Code != hub.CodeOutOfScope {
			t.Fatalf("expected CodeOutOfScope error, got %v", err)
		}
	})
}

func TestFix2_ChangesAndManifestSymlink(t *testing.T) {
	ctx := context.Background()
	eng, dir, _ := setupEngine(t, nil)

	secretDir := filepath.Join(dir, "wiki")
	if err := os.MkdirAll(secretDir, 0o755); err != nil {
		t.Fatal(err)
	}
	secretFile := filepath.Join(secretDir, "x.md")
	if err := os.WriteFile(secretFile, []byte("secret content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "initial commit")
	since := runGit(t, dir, "rev-parse", "HEAD")

	notesDir := filepath.Join(dir, "notes")
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	leakPath := filepath.Join(notesDir, "leak.md")
	if err := os.Symlink("../wiki/x.md", leakPath); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "notes/leak.md")
	runGit(t, dir, "commit", "-m", "add symlink leak.md")

	changes, err := eng.Changes(ctx, since)
	if err != nil {
		t.Fatalf("Changes failed: %v", err)
	}
	for _, ch := range changes.Changed {
		if ch.Path == "notes/leak.md" {
			t.Fatalf("Changes leaked symlink %q with sha %v", ch.Path, ch.SHA256)
		}
	}

	manifest, err := eng.Manifest(ctx)
	if err != nil {
		t.Fatalf("Manifest failed: %v", err)
	}
	for _, entry := range manifest.Files {
		if entry.Path == "notes/leak.md" {
			t.Fatalf("Manifest leaked symlink %q", entry.Path)
		}
	}
}

func TestFix3_ConflictCopyPathLimit(t *testing.T) {
	ctx := context.Background()
	eng, dir, _ := setupEngine(t, nil)

	// Target path is 491 bytes long with components <= 255 bytes:
	// "notes/" (6) + 150 'a's + "/" (1) + 150 'b's + "/" (1) + 180 'c's + ".md" (3) = 491 bytes.
	dir1 := strings.Repeat("a", 150)
	dir2 := strings.Repeat("b", 150)
	stem := strings.Repeat("c", 180)
	longRel := "notes/" + dir1 + "/" + dir2 + "/" + stem + ".md"
	if len(longRel) != 491 {
		t.Fatalf("expected len 491, got %d", len(longRel))
	}
	if err := hub.CheckPath(longRel); err != nil {
		t.Fatalf("longRel must pass CheckPath: %v", err)
	}

	origBody := []byte("original content\n")
	writeTestFile(t, dir, longRel, string(origBody))

	newBody := []byte("conflicting content\n")
	res, err := eng.Put(ctx, testDevice, longRel, hub.BaseNew, newBody)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if res.Result != hub.ResultConflictCopy {
		t.Fatalf("expected conflict_copy, got %q", res.Result)
	}

	if len(res.ConflictPath) > 512 {
		t.Fatalf("conflict path exceeds 512 bytes: len=%d (%q)", len(res.ConflictPath), res.ConflictPath)
	}
	if err := hub.CheckPath(res.ConflictPath); err != nil {
		t.Fatalf("conflict path %q must pass CheckPath: %v", res.ConflictPath, err)
	}

	readData, _, err := eng.Read(ctx, res.ConflictPath)
	if err != nil {
		t.Fatalf("Read(ConflictPath) failed: %v", err)
	}
	if string(readData) != string(newBody) {
		t.Fatalf("conflict file content mismatch: want %q, got %q", string(newBody), string(readData))
	}
}

func TestFix4_MergeLedgerDayBOM(t *testing.T) {
	ctx := context.Background()
	eng, dir, _ := setupEngine(t, nil)

	path := "ledger/2026/11/2026-11-03.md"
	bom := "\xef\xbb\xbf"
	currentContent := bom + "# 2026-11-03\n2026-11-03 coffee 450\n"
	writeTestFile(t, dir, path, currentContent)

	phoneContent := "# 2026-11-03\n2026-11-03 coffee 450\n2026-11-03 lunch 1200\n"
	res, err := eng.Put(ctx, testDevice, path, hub.BaseNew, []byte(phoneContent))
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if res.Result != hub.ResultMerged {
		t.Fatalf("expected ResultMerged, got %q", res.Result)
	}

	diskBytes, err := os.ReadFile(filepath.Join(dir, path))
	if err != nil {
		t.Fatal(err)
	}
	diskStr := string(diskBytes)

	headerCount := strings.Count(diskStr, "# 2026-11-03")
	if headerCount != 1 {
		t.Fatalf("expected header # 2026-11-03 to appear 1 time, appeared %d times in:\n%s", headerCount, diskStr)
	}

	if !strings.HasPrefix(diskStr, bom) {
		t.Fatal("hub file BOM was lost")
	}

	if strings.Contains(diskStr, bom+"2026-11-03 lunch") {
		t.Fatal("appended line contains unexpected BOM")
	}
}

func TestFix6_PutImportReceiptsCommit(t *testing.T) {
	ctx := context.Background()
	var commits []string
	importErrExpected := errors.New("simulated import failure")

	eng, dir, _ := setupEngine(t, func(o *hub.Options) {
		o.Commit = func(msg string) error {
			commits = append(commits, msg)
			return nil
		}
		o.ImportReceipts = func(paths []string) error {
			return importErrExpected
		}
	})

	path := "ledger/receipts/2026/11/r-1.jpg"
	_, err := eng.Put(ctx, testDevice, path, hub.BaseNew, []byte("fake image data"))
	if err == nil {
		t.Fatal("expected error from Put, got nil")
	}
	if !errors.Is(err, importErrExpected) {
		t.Fatalf("expected import error %v, got %v", importErrExpected, err)
	}

	if len(commits) == 0 {
		t.Fatal("Put aborted without calling Commit after writing file to disk")
	}

	if _, err := os.Stat(filepath.Join(dir, path)); err != nil {
		t.Fatalf("file not found on disk: %v", err)
	}
}

func TestFix7_NewWorkTreeRoot(t *testing.T) {
	dir := initGitRepo(t)
	subdir := filepath.Join(dir, "notes")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := hub.New(hub.Options{
		Dir: subdir,
	})
	if err == nil {
		t.Fatalf("hub.New accepted subdirectory %q, expected refusal", subdir)
	}

	eng, err := hub.New(hub.Options{
		Dir: dir,
	})
	if err != nil {
		t.Fatalf("hub.New refused valid work tree root %q: %v", dir, err)
	}
	if eng == nil {
		t.Fatal("expected non-nil Engine")
	}
}

func TestFix8_ConflictCopyCaseCollision(t *testing.T) {
	ctx := context.Background()
	eng, dir, _ := setupEngine(t, nil)

	writeTestFile(t, dir, "notes/a.md", "v1 content\n")

	c1Upper := "notes/a.conflict-pixel-20261103-102030.MD"
	writeTestFile(t, dir, c1Upper, "prior conflict\n")

	res, err := eng.Put(ctx, testDevice, "notes/a.md", hub.BaseNew, []byte("v2 content\n"))
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if res.Result != hub.ResultConflictCopy {
		t.Fatalf("expected ResultConflictCopy, got %q", res.Result)
	}

	expectedConflict := "notes/a.conflict-pixel-20261103-102030-2.md"
	if res.ConflictPath != expectedConflict {
		t.Fatalf("expected conflict path %q to avoid case collision, got %q", expectedConflict, res.ConflictPath)
	}
}

func TestFix9_DeleteReceiptsOutOfScope(t *testing.T) {
	ctx := context.Background()
	eng, _, _ := setupEngine(t, nil)
	validBase := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	t.Run("delete exact ledger/receipts", func(t *testing.T) {
		_, err := eng.Delete(ctx, testDevice, "ledger/receipts", validBase)
		if err == nil {
			t.Fatal("expected Delete(\"ledger/receipts\") to fail, got nil error")
		}
		var hubErr *hub.Error
		if !errors.As(err, &hubErr) || hubErr.Code != hub.CodeOutOfScope {
			t.Fatalf("expected CodeOutOfScope, got %v", err)
		}
	})

	t.Run("delete ledger/receipts/path", func(t *testing.T) {
		_, err := eng.Delete(ctx, testDevice, "ledger/receipts/2026/11/r-1.jpg", validBase)
		if err == nil {
			t.Fatal("expected Delete under ledger/receipts/ to fail, got nil error")
		}
		var hubErr *hub.Error
		if !errors.As(err, &hubErr) || hubErr.Code != hub.CodeOutOfScope {
			t.Fatalf("expected CodeOutOfScope, got %v", err)
		}
	})
}

func TestFixA1_PinA079_1(t *testing.T) {
	ctx := context.Background()
	path := "ledger/2026/11/2026-11-03.md"
	initialContent := "# 2026-11-03\n2026-11-03 coffee 450\n"

	dir := initGitRepo(t)
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
		Now: func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatalf("hub.New failed: %v", err)
	}

	phoneBody := []byte("# 2026-11-03\n2026-11-03 lunch 1200\n")
	res, err := eng.Put(ctx, testDevice, path, hub.BaseNew, phoneBody)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	if res.Result != hub.ResultMerged {
		t.Fatalf("A-079-1: PUT base=new onto existing ledger day file want ResultMerged, got %q", res.Result)
	}
	if res.Path != path {
		t.Fatalf("expected path %q, got %q", path, res.Path)
	}
	if res.ConflictPath != "" {
		t.Fatalf("expected no ConflictPath on merge, got %q", res.ConflictPath)
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
}
