package vaultsync

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestConflictCopyCandidateNaming verifies candidate name shape, host sanitation,
// and sequential numbering (-2, -3...) when previous candidate names are taken.
func TestConflictCopyCandidateNaming(t *testing.T) {
	dir := t.TempDir()
	r, err := newRunner(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}

	fixedTime := time.Date(2026, 11, 10, 14, 30, 45, 0, time.Local)
	testNowFunc = func() time.Time { return fixedTime }
	defer func() { testNowFunc = nil }()

	// Candidate 1 when free
	c1, err := r.conflictCopyCandidate("notes/work.md", nil)
	if err != nil {
		t.Fatalf("candidate 1: %v", err)
	}
	expectedStem := "notes/work.conflict-" + conflictHost() + "-20261110-143045.md"
	if c1 != expectedStem {
		t.Errorf("candidate 1 = %q, want %q", c1, expectedStem)
	}

	// Create candidate 1 on disk
	abs1 := filepath.Join(dir, filepath.FromSlash(c1))
	if err := os.MkdirAll(filepath.Dir(abs1), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs1, []byte("taken"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Candidate 2 when candidate 1 is taken on disk
	c2, err := r.conflictCopyCandidate("notes/work.md", nil)
	if err != nil {
		t.Fatalf("candidate 2: %v", err)
	}
	expected2 := "notes/work.conflict-" + conflictHost() + "-20261110-143045-2.md"
	if c2 != expected2 {
		t.Errorf("candidate 2 = %q, want %q", c2, expected2)
	}

	// Candidate 3 when candidate 2 is in alreadyCreated list
	c3, err := r.conflictCopyCandidate("notes/work.md", []string{c2})
	if err != nil {
		t.Fatalf("candidate 3: %v", err)
	}
	expected3 := "notes/work.conflict-" + conflictHost() + "-20261110-143045-3.md"
	if c3 != expected3 {
		t.Errorf("candidate 3 = %q, want %q", c3, expected3)
	}
}

// TestConflictCopyBinaryPreservation verifies that binary blobs with null bytes
// and control chars are read from stages and written byte-for-byte.
func TestConflictCopyBinaryPreservation(t *testing.T) {
	dir := t.TempDir()
	r, err := newRunner(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}

	// Initialize git repo
	ctx := context.Background()
	gitCall := func(args ...string) {
		t.Helper()
		if _, err := r.out(ctx, args...); err != nil {
			t.Fatalf("git %s: %v", strings.Join(args, " "), err)
		}
	}

	gitCall("init", "-b", "main")
	gitCall("config", "user.name", "test")
	gitCall("config", "user.email", "test@example.com")

	binaryBase := []byte("bin\x00\xff\xfe\x01\x02")
	binaryRemote := []byte("bin\x00\x00\xaa\xbb\xcc")
	binaryLocal := []byte("bin\x00\xff\x11\x22\x33")

	putFile := func(rel string, b []byte) {
		abs := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	putFile("notes/data.bin", binaryBase)
	gitCall("add", "notes/data.bin")
	gitCall("commit", "-m", "base")

	gitCall("branch", "remote-branch")
	putFile("notes/data.bin", binaryLocal)
	gitCall("commit", "-am", "local edit")

	gitCall("checkout", "remote-branch")
	putFile("notes/data.bin", binaryRemote)
	gitCall("commit", "-am", "remote edit")

	gitCall("checkout", "main")
	_, _ = r.gitCall(ctx, call{
		args: []string{"-c", "rebase.autoStash=false", "-c", "rebase.updateRefs=false", "-c", "rerere.enabled=false",
			"rebase", "--merge", "remote-branch"},
		env: []string{"GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true"},
	})

	if !r.gitRebaseRunning(ctx) {
		t.Fatal("expected rebase to stop on conflict")
	}

	// Read stage 2 and 3 blobs
	b2, has2, err := r.stageBlob(ctx, 2, "notes/data.bin")
	if err != nil || !has2 {
		t.Fatalf("stageBlob 2: has=%v, err=%v", has2, err)
	}
	if !bytes.Equal(b2, binaryRemote) {
		t.Errorf("stageBlob 2 = %x, want %x", b2, binaryRemote)
	}

	b3, has3, err := r.stageBlob(ctx, 3, "notes/data.bin")
	if err != nil || !has3 {
		t.Fatalf("stageBlob 3: has=%v, err=%v", has3, err)
	}
	if !bytes.Equal(b3, binaryLocal) {
		t.Errorf("stageBlob 3 = %x, want %x", b3, binaryLocal)
	}

	// Clean up rebase
	_, _ = r.gitCall(ctx, call{args: []string{"rebase", "--abort"}})
}
