package hub_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/hub"
)

var fixedNow = time.Date(2026, 11, 3, 10, 20, 30, 0, time.UTC)
var testDevice = hub.Device{ID: "dev-0001", Name: "pixel"}

func setupEngine(t *testing.T, optsMod func(o *hub.Options)) (*hub.Engine, string, *[]string) {
	t.Helper()
	dir := initGitRepo(t)
	var commits []string
	opts := hub.Options{
		Dir: dir,
		Commit: func(msg string) error {
			commits = append(commits, msg)
			return nil
		},
		Now:         func() time.Time { return fixedNow },
		LWVersion:   "0.79.0",
		VaultFormat: 1,
	}
	if optsMod != nil {
		optsMod(&opts)
	}
	eng, err := hub.New(opts)
	if err != nil {
		t.Fatalf("hub.New failed: %v", err)
	}
	return eng, dir, &commits
}

func TestHello(t *testing.T) {
	ctx := context.Background()

	t.Run("with commit", func(t *testing.T) {
		eng, dir, _ := setupEngine(t, nil)
		head := runGit(t, dir, "rev-parse", "HEAD")

		hello, err := eng.Hello(ctx)
		if err != nil {
			t.Fatalf("Hello failed: %v", err)
		}
		if hello.Protocol != hub.ProtocolVersion {
			t.Errorf("Protocol want %d, got %d", hub.ProtocolVersion, hello.Protocol)
		}
		if hello.LWVersion != "0.79.0" {
			t.Errorf("LWVersion want 0.79.0, got %s", hello.LWVersion)
		}
		if hello.VaultFormat != 1 {
			t.Errorf("VaultFormat want 1, got %d", hello.VaultFormat)
		}
		if hello.Device != "approved" {
			t.Errorf("Device want approved, got %s", hello.Device)
		}
		if hello.Head != head {
			t.Errorf("Head want %s, got %s", head, hello.Head)
		}
	})

	t.Run("empty branch no commit", func(t *testing.T) {
		dir := initEmptyGitRepo(t)
		eng, err := hub.New(hub.Options{
			Dir:         dir,
			LWVersion:   "0.79.0",
			VaultFormat: 1,
		})
		if err != nil {
			t.Fatalf("hub.New on empty repo failed: %v", err)
		}
		hello, err := eng.Hello(ctx)
		if err != nil {
			t.Fatalf("Hello failed: %v", err)
		}
		if hello.Head != "" {
			t.Errorf("Head want empty string on branch with no commit, got %q", hello.Head)
		}
	})
}

func TestRead(t *testing.T) {
	ctx := context.Background()
	eng, dir, _ := setupEngine(t, nil)

	content := "some note content\n"
	writeTestFile(t, dir, "notes/a.md", content)

	// Existing file
	data, sha, err := eng.Read(ctx, "notes/a.md")
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if string(data) != content {
		t.Errorf("content want %q, got %q", content, string(data))
	}
	if sha != hexSHA256([]byte(content)) {
		t.Errorf("sha mismatch")
	}

	// Absent file -> not_found
	_, _, err = eng.Read(ctx, "notes/absent.md")
	assertHubError(t, err, hub.CodeNotFound, "Read absent file")
}

func TestM1PutNew(t *testing.T) {
	ctx := context.Background()
	eng, dir, commits := setupEngine(t, nil)

	body := []byte("hello from phone\n")
	res, err := eng.Put(ctx, testDevice, "notes/a.md", hub.BaseNew, body)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if res.Result != hub.ResultApplied {
		t.Errorf("Result want applied, got %s", res.Result)
	}
	if res.Path != "notes/a.md" {
		t.Errorf("Path want notes/a.md, got %s", res.Path)
	}
	expectedSHA := hexSHA256(body)
	if res.SHA256 != expectedSHA {
		t.Errorf("SHA256 want %s, got %s", expectedSHA, res.SHA256)
	}
	diskBytes, err := os.ReadFile(filepath.Join(dir, "notes/a.md"))
	if err != nil {
		t.Fatalf("read disk file: %v", err)
	}
	if string(diskBytes) != string(body) {
		t.Errorf("disk content want %q, got %q", string(body), string(diskBytes))
	}
	if len(*commits) != 1 || (*commits)[0] != "hub: pixel put notes/a.md" {
		t.Errorf("commits want [\"hub: pixel put notes/a.md\"], got %v", *commits)
	}
}

func TestM2PutIdentical(t *testing.T) {
	ctx := context.Background()
	eng, dir, commits := setupEngine(t, nil)

	body := []byte("identical content\n")
	filePath := filepath.Join(dir, "notes/a.md")
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := eng.Put(ctx, testDevice, "notes/a.md", hub.BaseNew, body)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if res.Result != hub.ResultApplied {
		t.Errorf("Result want applied, got %s", res.Result)
	}
	if len(*commits) != 0 {
		t.Errorf("expected no commit for identical put, got %v", *commits)
	}
}

func TestM3PutConflict(t *testing.T) {
	ctx := context.Background()
	eng, dir, commits := setupEngine(t, nil)

	origBody := []byte("original content\n")
	filePath := filepath.Join(dir, "notes/a.md")
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, origBody, 0o644); err != nil {
		t.Fatal(err)
	}

	phoneBody := []byte("phone content\n")
	res, err := eng.Put(ctx, testDevice, "notes/a.md", hub.BaseNew, phoneBody)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if res.Result != hub.ResultConflictCopy {
		t.Errorf("Result want conflict_copy, got %s", res.Result)
	}
	expectedConflict := "notes/a.conflict-pixel-20261103-102030.md"
	if res.ConflictPath != expectedConflict {
		t.Errorf("ConflictPath want %s, got %s", expectedConflict, res.ConflictPath)
	}
	origOnDisk, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(origOnDisk) != string(origBody) {
		t.Errorf("original file changed: want %q, got %q", string(origBody), string(origOnDisk))
	}
	conflictOnDisk, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(expectedConflict)))
	if err != nil {
		t.Fatal(err)
	}
	if string(conflictOnDisk) != string(phoneBody) {
		t.Errorf("conflict file content want %q, got %q", string(phoneBody), string(conflictOnDisk))
	}
	expectedCommit := "hub: pixel conflict notes/a.md -> notes/a.conflict-pixel-20261103-102030.md"
	if len(*commits) != 1 || (*commits)[0] != expectedCommit {
		t.Errorf("commits want [%q], got %v", expectedCommit, *commits)
	}
}

func TestM4PutOverwrites(t *testing.T) {
	ctx := context.Background()
	eng, dir, commits := setupEngine(t, nil)

	origBody := []byte("original content\n")
	filePath := filepath.Join(dir, "notes/a.md")
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, origBody, 0o644); err != nil {
		t.Fatal(err)
	}
	currentSHA := hexSHA256(origBody)

	newBody := []byte("updated content\n")
	res, err := eng.Put(ctx, testDevice, "notes/a.md", currentSHA, newBody)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if res.Result != hub.ResultApplied {
		t.Errorf("Result want applied, got %s", res.Result)
	}
	expectedSHA := hexSHA256(newBody)
	if res.SHA256 != expectedSHA {
		t.Errorf("SHA256 want %s, got %s", expectedSHA, res.SHA256)
	}
	diskBytes, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(diskBytes) != string(newBody) {
		t.Errorf("disk content want %q, got %q", string(newBody), string(diskBytes))
	}
	if len(*commits) != 1 || (*commits)[0] != "hub: pixel put notes/a.md" {
		t.Errorf("commits want [\"hub: pixel put notes/a.md\"], got %v", *commits)
	}
}

func TestM5PutStaleBase(t *testing.T) {
	ctx := context.Background()
	eng, dir, commits := setupEngine(t, nil)

	origBody := []byte("hub current content\n")
	filePath := filepath.Join(dir, "notes/a.md")
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, origBody, 0o644); err != nil {
		t.Fatal(err)
	}

	staleBase := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	phoneBody := []byte("phone stale content\n")
	res, err := eng.Put(ctx, testDevice, "notes/a.md", staleBase, phoneBody)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if res.Result != hub.ResultConflictCopy {
		t.Errorf("Result want conflict_copy, got %s", res.Result)
	}
	expectedConflict := "notes/a.conflict-pixel-20261103-102030.md"
	if res.ConflictPath != expectedConflict {
		t.Errorf("ConflictPath want %s, got %s", expectedConflict, res.ConflictPath)
	}
	expectedCommit := "hub: pixel conflict notes/a.md -> notes/a.conflict-pixel-20261103-102030.md"
	if len(*commits) != 1 || (*commits)[0] != expectedCommit {
		t.Errorf("commits want [%q], got %v", expectedCommit, *commits)
	}
}

func TestM6PutResurrect(t *testing.T) {
	ctx := context.Background()
	eng, dir, commits := setupEngine(t, nil)

	staleBase := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	phoneBody := []byte("resurrected content\n")
	res, err := eng.Put(ctx, testDevice, "notes/a.md", staleBase, phoneBody)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if res.Result != hub.ResultResurrected {
		t.Errorf("Result want resurrected, got %s", res.Result)
	}
	diskBytes, err := os.ReadFile(filepath.Join(dir, "notes/a.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(diskBytes) != string(phoneBody) {
		t.Errorf("disk content want %q, got %q", string(phoneBody), string(diskBytes))
	}
	if len(*commits) != 1 || (*commits)[0] != "hub: pixel resurrect notes/a.md" {
		t.Errorf("commits want [\"hub: pixel resurrect notes/a.md\"], got %v", *commits)
	}
}

func TestM8DeleteBaseCurrent(t *testing.T) {
	ctx := context.Background()
	eng, dir, commits := setupEngine(t, nil)

	body := []byte("delete me\n")
	filePath := filepath.Join(dir, "notes/a.md")
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	currentSHA := hexSHA256(body)

	res, err := eng.Delete(ctx, testDevice, "notes/a.md", currentSHA)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if res.Result != hub.ResultDeleted {
		t.Errorf("Result want deleted, got %s", res.Result)
	}
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Errorf("file still exists after delete")
	}
	if len(*commits) != 1 || (*commits)[0] != "hub: pixel delete notes/a.md" {
		t.Errorf("commits want [\"hub: pixel delete notes/a.md\"], got %v", *commits)
	}
}

func TestM9DeleteBaseChanged(t *testing.T) {
	ctx := context.Background()
	eng, dir, commits := setupEngine(t, nil)

	body := []byte("do not delete me\n")
	filePath := filepath.Join(dir, "notes/a.md")
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	staleBase := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	res, err := eng.Delete(ctx, testDevice, "notes/a.md", staleBase)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if res.Result != hub.ResultRefusedChanged {
		t.Errorf("Result want refused_changed, got %s", res.Result)
	}
	if _, err := os.Stat(filePath); err != nil {
		t.Errorf("file was removed or errored: %v", err)
	}
	if len(*commits) != 0 {
		t.Errorf("expected no commit, got %v", *commits)
	}
}

func TestM10DeleteAbsent(t *testing.T) {
	ctx := context.Background()
	eng, _, commits := setupEngine(t, nil)

	base := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	res, err := eng.Delete(ctx, testDevice, "notes/a.md", base)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if res.Result != hub.ResultAlreadyGone {
		t.Errorf("Result want already_gone, got %s", res.Result)
	}
	if len(*commits) != 0 {
		t.Errorf("expected no commit, got %v", *commits)
	}
}

func TestM11OutOfScopeBadRequestTooLarge(t *testing.T) {
	ctx := context.Background()
	eng, _, commits := setupEngine(t, nil)

	// Out of scope PUT, DELETE, Read
	outOfScopePaths := []string{"wiki/x.md", "raw/y.md", "index.md", ".obsidian/a.json"}
	for _, p := range outOfScopePaths {
		_, err := eng.Put(ctx, testDevice, p, hub.BaseNew, []byte("body"))
		assertHubError(t, err, hub.CodeOutOfScope, p+" (PUT)")

		_, err = eng.Delete(ctx, testDevice, p, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
		assertHubError(t, err, hub.CodeOutOfScope, p+" (DELETE)")

		_, _, err = eng.Read(ctx, p)
		assertHubError(t, err, hub.CodeOutOfScope, p+" (Read)")
	}

	// Dot-dot path is bad_request per scope-cases.json fixture
	dotDotPaths := []string{"../x.md", "notes/../wiki/a.md"}
	for _, p := range dotDotPaths {
		_, err := eng.Put(ctx, testDevice, p, hub.BaseNew, []byte("body"))
		assertHubError(t, err, hub.CodeBadRequest, p+" (PUT)")

		_, err = eng.Delete(ctx, testDevice, p, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
		assertHubError(t, err, hub.CodeBadRequest, p+" (DELETE)")

		_, _, err = eng.Read(ctx, p)
		assertHubError(t, err, hub.CodeBadRequest, p+" (Read)")
	}

	// Invalid base
	invalidBases := []string{"bad-base", "123", "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF", "0123456789abcdef"}
	for _, b := range invalidBases {
		_, err := eng.Put(ctx, testDevice, "notes/a.md", b, []byte("body"))
		assertHubError(t, err, hub.CodeBadRequest, b+" (PUT base)")

		_, err = eng.Delete(ctx, testDevice, "notes/a.md", b)
		assertHubError(t, err, hub.CodeBadRequest, b+" (DELETE base)")
	}

	// Too large
	tooLargeBody := make([]byte, hub.MaxBodyBytes+1)
	_, err := eng.Put(ctx, testDevice, "notes/a.md", hub.BaseNew, tooLargeBody)
	assertHubError(t, err, hub.CodeTooLarge, "too large body")

	if len(*commits) != 0 {
		t.Errorf("expected no commits for refused ops, got %v", *commits)
	}
}

func TestM12Receipts(t *testing.T) {
	ctx := context.Background()
	var imported []string
	eng, dir, commits := setupEngine(t, func(o *hub.Options) {
		o.ImportReceipts = func(paths []string) error {
			imported = append(imported, paths...)
			return nil
		}
	})

	receiptPath := "ledger/receipts/2026/11/r-1.jpg"
	body := []byte("fake image data")
	res, err := eng.Put(ctx, testDevice, receiptPath, hub.BaseNew, body)
	if err != nil {
		t.Fatalf("Put receipt failed: %v", err)
	}
	if res.Result != hub.ResultApplied {
		t.Errorf("Result want applied, got %s", res.Result)
	}
	diskBytes, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(receiptPath)))
	if err != nil {
		t.Fatal(err)
	}
	if string(diskBytes) != string(body) {
		t.Errorf("receipt disk content mismatch")
	}
	if len(imported) != 1 || imported[0] != receiptPath {
		t.Errorf("ImportReceipts want [%q], got %v", receiptPath, imported)
	}
	expectedCommit := "hub: pixel receipt ledger/receipts/2026/11/r-1.jpg"
	if len(*commits) != 1 || (*commits)[0] != expectedCommit {
		t.Errorf("commits want [%q], got %v", expectedCommit, *commits)
	}

	// DELETE under ledger/receipts/ -> out_of_scope
	base := hexSHA256(body)
	_, err = eng.Delete(ctx, testDevice, receiptPath, base)
	assertHubError(t, err, hub.CodeOutOfScope, "delete receipt")
}

func TestM13ConflictNameTaken(t *testing.T) {
	ctx := context.Background()
	eng, dir, commits := setupEngine(t, nil)

	target := filepath.Join(dir, "notes/a.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("orig"), 0o644); err != nil {
		t.Fatal(err)
	}

	// First conflict name
	firstConflict := filepath.Join(dir, "notes/a.conflict-pixel-20261103-102030.md")
	if err := os.WriteFile(firstConflict, []byte("conflict 1"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Second conflict name
	secondConflict := filepath.Join(dir, "notes/a.conflict-pixel-20261103-102030-2.md")
	if err := os.WriteFile(secondConflict, []byte("conflict 2"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := eng.Put(ctx, testDevice, "notes/a.md", hub.BaseNew, []byte("conflict 3"))
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if res.Result != hub.ResultConflictCopy {
		t.Errorf("Result want conflict_copy, got %s", res.Result)
	}
	expectedConflict := "notes/a.conflict-pixel-20261103-102030-3.md"
	if res.ConflictPath != expectedConflict {
		t.Errorf("ConflictPath want %s, got %s", expectedConflict, res.ConflictPath)
	}
	cBytes, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(expectedConflict)))
	if err != nil {
		t.Fatal(err)
	}
	if string(cBytes) != "conflict 3" {
		t.Errorf("conflict content want 'conflict 3', got %q", string(cBytes))
	}
	expectedCommit := "hub: pixel conflict notes/a.md -> notes/a.conflict-pixel-20261103-102030-3.md"
	if len(*commits) != 1 || (*commits)[0] != expectedCommit {
		t.Errorf("commits want [%q], got %v", expectedCommit, *commits)
	}
}

func TestM14NeverDestroy(t *testing.T) {
	ctx := context.Background()

	// First check symlinked target or parent -> out_of_scope
	t.Run("symlinks refused", func(t *testing.T) {
		eng, dir, _ := setupEngine(t, nil)
		realDir := filepath.Join(dir, "realnotes")
		if err := os.MkdirAll(realDir, 0o755); err != nil {
			t.Fatal(err)
		}
		symDir := filepath.Join(dir, "notes")
		if err := os.Symlink(realDir, symDir); err != nil {
			t.Fatal(err)
		}

		_, err := eng.Put(ctx, testDevice, "notes/a.md", hub.BaseNew, []byte("data"))
		assertHubError(t, err, hub.CodeOutOfScope, "symlink parent")

		// Remove symDir and test symlinked file target
		os.Remove(symDir)
		if err := os.MkdirAll(symDir, 0o755); err != nil {
			t.Fatal(err)
		}
		realFile := filepath.Join(realDir, "target.md")
		if err := os.WriteFile(realFile, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
		symTarget := filepath.Join(symDir, "sym.md")
		if err := os.Symlink(realFile, symTarget); err != nil {
			t.Fatal(err)
		}
		_, err = eng.Put(ctx, testDevice, "notes/sym.md", hub.BaseNew, []byte("new data"))
		assertHubError(t, err, hub.CodeOutOfScope, "symlink target")
	})

	// 200 random ops property test
	t.Run("200 random ops never destroy", func(t *testing.T) {
		type snapshot map[string]string
		var snapshots []snapshot

		dir := initGitRepo(t)
		allEverPut := make(map[string]bool)

		paths := []string{"notes/p1.md", "notes/p2.md", "ledger/2026/11/2026-11-03.md"}

		// Seed initial files
		for i, p := range paths {
			initContent := fmt.Sprintf("initial content %d\n", i)
			allEverPut[initContent] = true
			full := filepath.Join(dir, filepath.FromSlash(p))
			os.MkdirAll(filepath.Dir(full), 0o755)
			os.WriteFile(full, []byte(initContent), 0o644)
		}

		snapTree := func() {
			snap := make(snapshot)
			filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				rel, _ := filepath.Rel(dir, p)
				relSlash := filepath.ToSlash(rel)
				if hub.CheckPath(relSlash) == nil {
					b, _ := os.ReadFile(p)
					snap[relSlash] = string(b)
				}
				return nil
			})
			snapshots = append(snapshots, snap)
		}

		// Initial commit snapshot
		snapTree()

		eng, err := hub.New(hub.Options{
			Dir: dir,
			Commit: func(msg string) error {
				snapTree()
				return nil
			},
			Now: func() time.Time { return fixedNow },
		})
		if err != nil {
			t.Fatal(err)
		}

		rng := rand.New(rand.NewSource(42))
		for op := 0; op < 200; op++ {
			p := paths[rng.Intn(len(paths))]
			isPut := rng.Intn(2) == 0

			// Get current sha on disk or dummy
			full := filepath.Join(dir, filepath.FromSlash(p))
			curBytes, curErr := os.ReadFile(full)
			curSHA := ""
			if curErr == nil {
				curSHA = hexSHA256(curBytes)
			}

			// Base choice: "new", curSHA, stale SHA
			var base string
			switch rng.Intn(3) {
			case 0:
				base = hub.BaseNew
			case 1:
				if curSHA != "" {
					base = curSHA
				} else {
					base = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
				}
			case 2:
				base = "9999999999abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
			}

			if isPut {
				var content string
				if strings.HasPrefix(p, "ledger/") {
					content = fmt.Sprintf("2026-11-03 item-%d %d\n", op, rng.Intn(1000))
				} else {
					content = fmt.Sprintf("content from op %d\n", op)
				}
				allEverPut[content] = true
				eng.Put(ctx, testDevice, p, base, []byte(content))
			} else {
				eng.Delete(ctx, testDevice, p, base)
			}
		}

		// At the end: every byte string ever PUT or present at start must be present
		// either in some in-scope file on disk or in one of the snapshots.
		// Note for ledger: individual lines in merged files count as preserved.
		diskContents := make(map[string]bool)
		filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(dir, p)
			relSlash := filepath.ToSlash(rel)
			if hub.CheckPath(relSlash) == nil {
				b, _ := os.ReadFile(p)
				diskContents[string(b)] = true
			}
			return nil
		})

		for str := range allEverPut {
			found := false
			// Check current disk
			for dc := range diskContents {
				if dc == str || strings.Contains(dc, strings.TrimSpace(str)) {
					found = true
					break
				}
			}
			if !found {
				// Check snapshots
				for _, snap := range snapshots {
					for _, sc := range snap {
						if sc == str || strings.Contains(sc, strings.TrimSpace(str)) {
							found = true
							break
						}
					}
					if found {
						break
					}
				}
			}
			if !found {
				t.Fatalf("NeverDestroy violated: string %q was lost!", str)
			}
		}
	})
}

func TestM15CaseCollision(t *testing.T) {
	ctx := context.Background()
	eng, dir, commits := setupEngine(t, nil)

	fileA := filepath.Join(dir, "notes/A.md")
	if err := os.MkdirAll(filepath.Dir(fileA), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileA, []byte("existing A"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := eng.Put(ctx, testDevice, "notes/a.md", hub.BaseNew, []byte("new a"))
	assertHubError(t, err, hub.CodeCaseCollision, "PUT case collision")

	if len(*commits) != 0 {
		t.Errorf("expected no commit on case collision, got %v", *commits)
	}
}

func TestM16LockDuringReadCompareWrite(t *testing.T) {
	ctx := context.Background()
	var lockHeld atomic.Int32
	var lockCount atomic.Int32

	eng, _, _ := setupEngine(t, func(o *hub.Options) {
		o.Lock = func() (func(), error) {
			lockCount.Add(1)
			lockHeld.Store(1)
			return func() {
				lockHeld.Store(0)
			}, nil
		}
		o.Commit = func(msg string) error {
			if lockHeld.Load() != 0 {
				t.Fatalf("Commit was called while lock was still held!")
			}
			return nil
		}
	})

	_, err := eng.Put(ctx, testDevice, "notes/a.md", hub.BaseNew, []byte("data"))
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if lockCount.Load() == 0 {
		t.Fatalf("Lock was never acquired")
	}
	if lockHeld.Load() != 0 {
		t.Fatalf("Lock was not released after Put")
	}
}

func TestM17Manifest(t *testing.T) {
	ctx := context.Background()
	eng, dir, _ := setupEngine(t, nil)

	// Create various files: in-scope, out-of-scope, dot-dir, symlink
	writeTestFile(t, dir, "notes/a.md", "content a")
	writeTestFile(t, dir, "notes/img.png", "img data")
	writeTestFile(t, dir, "todo.md", "todo item")
	writeTestFile(t, dir, "ledger/receipts/2026/11/r-1.jpg", "receipt pic")
	writeTestFile(t, dir, "wiki/w.md", "wiki page")
	writeTestFile(t, dir, "raw/r.md", "raw data")
	writeTestFile(t, dir, ".obsidian/conf.json", "{}")

	// Symlink
	realFile := filepath.Join(dir, "notes/a.md")
	symFile := filepath.Join(dir, "notes/sym.md")
	os.Symlink(realFile, symFile)

	manifest, err := eng.Manifest(ctx)
	if err != nil {
		t.Fatalf("Manifest failed: %v", err)
	}
	head := runGit(t, dir, "rev-parse", "HEAD")
	if manifest.Head != head {
		t.Errorf("Head want %s, got %s", head, manifest.Head)
	}

	expectedPaths := []string{
		"ledger/receipts/2026/11/r-1.jpg",
		"notes/a.md",
		"notes/img.png",
		"todo.md",
	}
	if len(manifest.Files) != len(expectedPaths) {
		t.Fatalf("Files count want %d, got %d: %+v", len(expectedPaths), len(manifest.Files), manifest.Files)
	}
	for i, exp := range expectedPaths {
		if manifest.Files[i].Path != exp {
			t.Errorf("Files[%d].Path want %s, got %s", i, exp, manifest.Files[i].Path)
		}
		if manifest.Files[i].SHA256 == "" {
			t.Errorf("Files[%d].SHA256 is empty", i)
		}
		if manifest.Files[i].Size <= 0 {
			t.Errorf("Files[%d].Size <= 0", i)
		}
	}

	// Verify empty files slice serializes as [] and not null
	emptyEng, _, _ := setupEngine(t, nil)
	emptyMan, err := emptyEng.Manifest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rawJSON, err := json.Marshal(emptyMan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawJSON), `"files":[]`) {
		t.Errorf("expected empty Files to serialize as [], got %s", string(rawJSON))
	}
}

func TestM18Changes(t *testing.T) {
	ctx := context.Background()
	eng, dir, _ := setupEngine(t, nil)

	// Step 1: Initial commit with notes/b.md, notes/c.md
	writeTestFile(t, dir, "notes/b.md", "b v1\n")
	writeTestFile(t, dir, "notes/c.md", "c v1\n")
	runGit(t, dir, "add", "notes/b.md", "notes/c.md")
	runGit(t, dir, "commit", "-m", "add b and c")
	sinceCommit := runGit(t, dir, "rev-parse", "HEAD")

	// Changes with since=HEAD -> empty changed, full: false
	ch, err := eng.Changes(ctx, sinceCommit)
	if err != nil {
		t.Fatalf("Changes failed: %v", err)
	}
	if ch.Full {
		t.Errorf("Full want false, got true")
	}
	if len(ch.Changed) != 0 {
		t.Errorf("Changed want empty, got %v", ch.Changed)
	}
	rawJSON, err := json.Marshal(ch)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawJSON), `"changed":[]`) {
		t.Errorf("expected empty Changed to serialize as [], got %s", string(rawJSON))
	}

	// Step 2: Change notes/b.md, delete notes/c.md, touch wiki/w.md
	writeTestFile(t, dir, "notes/b.md", "b v2 edited\n")
	os.Remove(filepath.Join(dir, "notes/c.md"))
	writeTestFile(t, dir, "wiki/w.md", "wiki change\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "update b, delete c, touch wiki")

	ch2, err := eng.Changes(ctx, sinceCommit)
	if err != nil {
		t.Fatalf("Changes failed: %v", err)
	}
	if ch2.Full {
		t.Errorf("Full want false, got true")
	}
	if len(ch2.Changed) != 2 {
		t.Fatalf("Changed count want 2, got %d: %+v", len(ch2.Changed), ch2.Changed)
	}
	// Sorted: notes/b.md, notes/c.md
	if ch2.Changed[0].Path != "notes/b.md" {
		t.Errorf("Changed[0] want notes/b.md, got %s", ch2.Changed[0].Path)
	}
	if ch2.Changed[0].SHA256 == nil || *ch2.Changed[0].SHA256 != hexSHA256([]byte("b v2 edited\n")) {
		t.Errorf("Changed[0].SHA256 mismatch")
	}
	if ch2.Changed[1].Path != "notes/c.md" {
		t.Errorf("Changed[1] want notes/c.md, got %s", ch2.Changed[1].Path)
	}
	if ch2.Changed[1].SHA256 != nil {
		t.Errorf("Changed[1].SHA256 want nil, got %v", ch2.Changed[1].SHA256)
	}

	// Step 3: since empty or unknown -> full: true with files = Manifest
	chEmpty, err := eng.Changes(ctx, "")
	if err != nil {
		t.Fatalf("Changes empty failed: %v", err)
	}
	if !chEmpty.Full {
		t.Errorf("Full want true for empty since")
	}
	if len(chEmpty.Files) == 0 {
		t.Errorf("Files want non-empty manifest for empty since")
	}

	chUnknown, err := eng.Changes(ctx, "0123456789abcdef0123456789abcdef01234567")
	if err != nil {
		t.Fatalf("Changes unknown failed: %v", err)
	}
	if !chUnknown.Full {
		t.Errorf("Full want true for unknown since")
	}
}

func TestM19Refresh(t *testing.T) {
	ctx := context.Background()

	t.Run("pass through hub.Error", func(t *testing.T) {
		eng, _, _ := setupEngine(t, func(o *hub.Options) {
			o.Refresh = func(ctx context.Context) error {
				return &hub.Error{Code: hub.CodeDiverged, Message: "diverged remote"}
			}
		})
		_, err := eng.Manifest(ctx)
		assertHubError(t, err, hub.CodeDiverged, "Manifest Refresh error")

		_, err = eng.Changes(ctx, "")
		assertHubError(t, err, hub.CodeDiverged, "Changes Refresh error")
	})

	t.Run("ignore non-hub error", func(t *testing.T) {
		eng, _, _ := setupEngine(t, func(o *hub.Options) {
			o.Refresh = func(ctx context.Context) error {
				return fmt.Errorf("network connection error")
			}
		})
		_, err := eng.Manifest(ctx)
		if err != nil {
			t.Errorf("Manifest should ignore non-hub error, got: %v", err)
		}
		_, err = eng.Changes(ctx, "")
		if err != nil {
			t.Errorf("Changes should ignore non-hub error, got: %v", err)
		}
	})
}

func TestM20CommitDiverged(t *testing.T) {
	ctx := context.Background()
	eng, dir, _ := setupEngine(t, func(o *hub.Options) {
		o.Commit = func(msg string) error {
			return &hub.Error{Code: hub.CodeDiverged, Message: "commit diverged"}
		}
	})

	body := []byte("content that stays on disk\n")
	res, err := eng.Put(ctx, testDevice, "notes/a.md", hub.BaseNew, body)
	assertHubError(t, err, hub.CodeDiverged, "Put diverged commit")
	if res.Result != hub.ResultApplied {
		t.Errorf("Result want applied, got %s", res.Result)
	}
	// Verify write stayed on disk
	diskBytes, err := os.ReadFile(filepath.Join(dir, "notes/a.md"))
	if err != nil {
		t.Fatalf("file should stay on disk: %v", err)
	}
	if string(diskBytes) != string(body) {
		t.Errorf("disk content want %q, got %q", string(body), string(diskBytes))
	}
}

func writeTestFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertHubError(t *testing.T, err error, wantCode string, contextMsg string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected *hub.Error with code %s, got nil", contextMsg, wantCode)
	}
	hubErr, ok := err.(*hub.Error)
	if !ok {
		t.Fatalf("%s: expected *hub.Error, got %T: %v", contextMsg, err, err)
	}
	if hubErr.Code != wantCode {
		t.Fatalf("%s: expected code %s, got %s (message: %s)", contextMsg, wantCode, hubErr.Code, hubErr.Message)
	}
}
