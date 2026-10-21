package main

// storage_sync_066_fix1_test.go holds the tests of 066 FIX1 (the branch review,
// runs/review/report.md). They pin rule P - lw never deletes or overwrites bytes
// it put in storage/ unless those bytes are confirmed on the remote - and the
// X-rules around it. Each was written BEFORE the fix and recorded RED on 4e52955;
// the three that reproduce data loss (B7, F-1 a and b) are red because the bytes
// exist nowhere afterwards, not because a message differs.
//
// The harness is S1's: two PCs and a remote, local or over the fake ssh
// (newS066Rig). What these tests add is a way to make the remote's object store
// unable to take a new object (s066Freeze) and then able again (s066Thaw), which
// is the only thing that separates "the upload failed" from "the upload never
// happened" - the state every one of the data-loss paths starts from.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/storage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vaultsync"
)

// s066Freeze makes dir and every directory under it read-only, so the remote's
// object store can be read but takes no new object (a mkdir or a temp file
// fails), and puts the permissions back at cleanup. Permission bits do not bind
// root, so the test is skipped there. Freezing the store root alone is not
// enough once an object exists: new objects go into subdirectories that are
// already writable.
func s066Freeze(t *testing.T, dir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 0555 does not make a directory unwritable")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s066ChmodDirs(t, dir, 0o555)
	t.Cleanup(func() { s066ChmodDirs(t, dir, 0o755) })
}

// s066Thaw undoes s066Freeze.
func s066Thaw(t *testing.T, dir string) {
	t.Helper()
	s066ChmodDirs(t, dir, 0o755)
}

func s066ChmodDirs(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	var dirs []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	for _, d := range dirs {
		if err := os.Chmod(d, mode); err != nil {
			t.Fatal(err)
		}
	}
}

// s066ObjectIs reports whether the remote's object store holds exactly body.
func s066ObjectIs(storeDir, body string) bool {
	got, err := os.ReadFile(s066ObjectPath(storeDir, body))
	return err == nil && string(got) == body
}

// s066DiskHas reports whether any file under root outside .git contains needle:
// "these bytes exist somewhere on this PC".
func s066DiskHas(root, needle string) bool {
	found := false
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && d.Name() == ".git" {
			return fs.SkipDir
		}
		if !d.IsDir() {
			if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), needle) {
				found = true
			}
		}
		return nil
	})
	return found
}

// s066Run is pc.lw that turns a panic into a result, so a test that exposes one
// reports it and the rest of the suite still runs.
func s066Run(pc *syncPC, args ...string) (stdout, stderr string, code int, panicked any) {
	pc.t.Helper()
	pc.act()
	stdout, stderr, code = captureRun(pc.t, func() (c int) {
		defer func() {
			if r := recover(); r != nil {
				panicked = r
				c = -1
			}
		}()
		return run(args)
	})
	return
}

// s066Lines counts the lines of s that contain sub.
func s066Lines(s, sub string) int {
	n := 0
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

// TestRemoteRmAfterFailedUploadKeepsBytes is B7 (plan R13, workflow C-066-8; the
// P4 scenario of probes-v2.49.0.log). a stores g while the remote's object store
// cannot take it: the catalog entry is pushed, the payload is not. b removes the
// entry. When a pulls the removal, the ledger says "lw put these bytes here" and
// the bytes still match - which proves lw wrote them, not that a copy survives
// anywhere else. They are the only copy. Deleting them (what v2.51.0-pre did)
// loses the payload for good while the catalog's history still names it.
func TestRemoteRmAfterFailedUploadKeepsBytes(t *testing.T) {
	const body = "payload that never reached the remote\n"
	// scenario ends with b's removal pushed and a not yet aware of it. a's
	// auto-sync is on, so the next verb on a (a note) does the start-of-verb pull.
	scenario := func(t *testing.T, r *s066Rig) {
		t.Helper()
		a, b := r.a, r.b
		s066AutoSync(t, a, false)
		s066AutoSync(t, b, false)
		s066Freeze(t, r.storeDir)
		r.store(t, a, "g.bin", body)
		if out, errs, code := s066Sync(a); code != 1 || !strings.Contains(errs, "(wiki synced; run lw sync again)") {
			t.Fatalf("setup: sync with a frozen store: exit %d\nstdout %q\nstderr %q", code, out, errs)
		}
		if s066ObjectIs(r.storeDir, body) {
			t.Fatal("setup: the object reached a frozen store")
		}
		s066Thaw(t, r.storeDir)
		s066MustSync(t, b)
		if s066Exists(filepath.Join(b.root, "storage", "g.bin")) {
			t.Fatal("setup: b has g.bin although nobody uploaded it")
		}
		if out, errs, code := b.lw("storage", "rm", "g.bin", "--vault", b.root); code != 0 {
			t.Fatalf("setup: storage rm on b: exit %d stdout %q stderr %q", code, out, errs)
		}
		s066MustSync(t, b)
		s066AutoSync(t, a, true)
	}

	t.Run("the reconcile uploads the only copy before it deletes", func(t *testing.T) {
		s066Variants(t, func(t *testing.T, r *s066Rig) {
			scenario(t, r)
			a := r.a
			noteAt(t, 0)
			_, stderr, code := a.lw("note", "-m", "x", "--vault", a.root)
			if code != 0 {
				t.Errorf("lw note exit %d, want 0 (an auto-sync problem never changes the verb's exit code); stderr %q", code, stderr)
			}
			if !s066ObjectIs(r.storeDir, body) {
				t.Errorf("the payload is in no object store after the removal reached a (B7: the only copy was deleted); remote objects: %v", s066ListObjects(r.storeDir))
			}
			if want := "storage: removed g.bin (removed on another PC; a copy was kept on the remote store)"; !strings.Contains(stderr, want) {
				t.Errorf("stderr = %q, want it to say %q", stderr, want)
			}
			if s066Exists(filepath.Join(a.root, "storage", "g.bin")) {
				t.Error("a: storage/g.bin is still on disk although its copy is safe and the entry was removed")
			}
		})
	})

	t.Run("kept, once, while the remote cannot take it", func(t *testing.T) {
		r := newS066Rig(t, false)
		scenario(t, r)
		a := r.a
		s066Freeze(t, r.storeDir)

		noteAt(t, 0)
		_, stderr, code := a.lw("note", "-m", "x", "--vault", a.root)
		if code != 0 {
			t.Errorf("lw note exit %d, want 0; stderr %q", code, stderr)
		}
		if got := a.read("storage/g.bin"); got != body {
			t.Fatalf("a: storage/g.bin = %q after the removal reached it with no copy on the remote, want it kept (B7)", got)
		}
		const kept = "storage: kept g.bin: no copy on the remote yet ("
		if !strings.Contains(stderr, kept) {
			t.Errorf("stderr = %q, want a line starting %q", stderr, kept)
		}

		// Said once per payload, not on every verb.
		noteAt(t, 1)
		_, stderr, _ = a.lw("note", "-m", "y", "--vault", a.root)
		if strings.Contains(stderr, "kept g.bin") {
			t.Errorf("the second verb repeated the kept line: %q", stderr)
		}
		if got := a.read("storage/g.bin"); got != body {
			t.Errorf("a: storage/g.bin = %q after the second verb", got)
		}
		if c := s066Doctor(t, a); !c.OK || !c.Warn || !strings.Contains(c.Detail, "1 untracked file(s) in storage/") {
			t.Errorf("doctor storage check = %+v, want the kept file shown as untracked", c)
		}

		// The remote comes back: the next verb retries, saves the payload, then removes the file.
		s066Thaw(t, r.storeDir)
		noteAt(t, 2)
		_, stderr, _ = a.lw("note", "-m", "z", "--vault", a.root)
		if !s066ObjectIs(r.storeDir, body) {
			t.Errorf("the payload is still in no object store after the remote came back; stderr %q", stderr)
		}
		if want := "storage: removed g.bin (removed on another PC; a copy was kept on the remote store)"; !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
		if s066Exists(filepath.Join(a.root, "storage", "g.bin")) {
			t.Error("a: storage/g.bin survived the retry")
		}
	})
}

// s066ListObjects names the object files in a store, for failure messages.
func s066ListObjects(storeDir string) []string {
	var out []string
	filepath.WalkDir(filepath.Join(storeDir, "objects"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, filepath.Base(filepath.Dir(p))+"/"+d.Name())
		}
		return nil
	})
	return out
}

// TestReplaceAfterFailedUploadKeepsBytes is F-1 (b), the overwrite twin of B7. a
// stores f = A and the upload fails. b, which never got A, force-replaces f with
// B and uploads it. When a pulls, its file is "stale": the bytes lw itself put
// there before the catalog moved on. The fetch used to overwrite them with B, and
// A - never uploaded - existed nowhere (probe, runs/review/probes).
func TestReplaceAfterFailedUploadKeepsBytes(t *testing.T) {
	const a0, b0 = "A: the only copy, on PC a, whose upload failed\n", "B: the replacement b stored and uploaded\n"
	scenario := func(t *testing.T) *s066Rig {
		t.Helper()
		r := newS066Rig(t, false)
		a, b := r.a, r.b
		s066AutoSync(t, a, false)
		s066AutoSync(t, b, false)
		s066Freeze(t, r.storeDir)
		r.store(t, a, "f", a0)
		if _, errs, code := s066Sync(a); code != 1 {
			t.Fatalf("setup: sync with a frozen store: exit %d stderr %q", code, errs)
		}
		s066Thaw(t, r.storeDir)
		s066MustSync(t, b)
		r.store(t, b, "f", b0, "--force")
		s066MustSync(t, b)
		if !s066ObjectIs(r.storeDir, b0) {
			t.Fatal("setup: B did not reach the remote")
		}
		return r
	}

	t.Run("the upload runs first, then the file is replaced", func(t *testing.T) {
		r := scenario(t)
		a := r.a
		stdout, stderr, code := s066Sync(a)
		if code != 0 {
			t.Errorf("a: lw sync exit %d\nstdout %q\nstderr %q", code, stdout, stderr)
		}
		if got := a.read("storage/f"); got != b0 {
			t.Errorf("a: storage/f = %q, want the replacement %q", got, b0)
		}
		if !s066ObjectIs(r.storeDir, a0) {
			t.Errorf("A is not in the object store (%v) and %v on a's disk: the replacement overwrote the only copy without saving it first (F-1)", s066ListObjects(r.storeDir), map[bool]string{true: "is still", false: "is no longer"}[s066DiskHas(a.root, a0)])
		}
	})

	t.Run("kept while the old version cannot be saved", func(t *testing.T) {
		r := scenario(t)
		a := r.a
		s066Freeze(t, r.storeDir) // B can be read; A cannot be written

		stdout, stderr, code := s066Sync(a)
		if got := a.read("storage/f"); got != a0 {
			t.Fatalf("a: storage/f = %q, want the old version kept (F-1: it was overwritten while its only copy was local)", got)
		}
		if want := "storage: kept f: the replaced version has no copy on the remote yet ("; !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want a line containing %q", stderr, want)
		}
		if code != 1 {
			t.Errorf("exit %d, want 1: a fetch that could not be done is a storage problem\nstdout %q", code, stdout)
		}
		if c := s066Doctor(t, a); !c.OK || !c.Warn || c.Detail != "0 of 1 verified; 1 pending (replaced on another PC)" {
			t.Errorf("doctor storage check = %+v, want OK with Warn and the stale entry described", c)
		}

		s066Thaw(t, r.storeDir)
		stdout, stderr, code = s066Sync(a)
		if code != 0 {
			t.Errorf("sync after the remote came back: exit %d\nstdout %q\nstderr %q", code, stdout, stderr)
		}
		if got := a.read("storage/f"); got != b0 {
			t.Errorf("a: storage/f = %q after the retry, want %q", got, b0)
		}
		if !s066ObjectIs(r.storeDir, a0) {
			t.Error("A is not in the object store after the retry")
		}
	})
}

// s066Diverge makes the situation F-1 (a) starts from: a and b each stored the
// SAME name with different bytes while apart, b synced first, so a's catalog
// edit conflicts with b's and a's own sync is refused as diverged. a holds the
// only copy of A, and the catalog entry that named it is about to be discarded.
// A seed file is stored and synced first so the catalog is a tracked file on both
// PCs; an untracked one would make the pull fail on "untracked files would be
// overwritten" instead of diverging.
func s066Diverge(t *testing.T, a0, b0 string) *s066Rig {
	t.Helper()
	r := newS066Rig(t, false)
	s066AutoSync(t, r.a, false)
	s066AutoSync(t, r.b, false)
	r.store(t, r.a, "seed.bin", "a file both PCs have\n")
	s066MustSync(t, r.a)
	s066MustSync(t, r.b)
	r.store(t, r.a, "report.pdf", a0)
	r.store(t, r.b, "report.pdf", b0)
	s066MustSync(t, r.b) // b wins the race
	return r
}

// TestTakeRemoteKeepsDivergedStorage is F-1 (a): a loses the race, runs
// --take-remote (the catalog takes b's report.pdf, a's commits go to a backup
// branch), and the next sync fetched b's report.pdf over a's - the only copy
// of A, which no remote ever held.
func TestTakeRemoteKeepsDivergedStorage(t *testing.T) {
	const a0, b0 = "bytes of PC a, the only copy\n", "bytes of PC b\n"

	diverged := func(t *testing.T, a *syncPC) {
		t.Helper()
		if _, errs, code := s066Sync(a); code != 1 || !strings.Contains(errs, "diverged") {
			t.Fatalf("setup: a's sync: exit %d stderr %q, want a refusal as diverged", code, errs)
		}
	}
	take := func(t *testing.T, a *syncPC) {
		t.Helper()
		// With the store frozen the upload before the take fails, and the command
		// says so by exiting 1 after it has taken the remote.
		if out, errs, _ := a.lw("sync", "--take-remote", "--vault", a.root); !strings.Contains(out, "took ") {
			t.Fatalf("setup: sync --take-remote did not take the remote\nstdout %q\nstderr %q", out, errs)
		}
	}

	t.Run("the diverged sync already uploaded it", func(t *testing.T) {
		r := s066Diverge(t, a0, b0)
		a := r.a
		diverged(t, a)
		take(t, a)
		if out, errs, code := s066Sync(a); code != 0 {
			t.Errorf("sync after take: exit %d\nstdout %q\nstderr %q", code, out, errs)
		}
		if got := a.read("storage/report.pdf"); got != b0 {
			t.Errorf("a: storage/report.pdf = %q, want the remote's %q", got, b0)
		}
		if !s066ObjectIs(r.storeDir, a0) {
			t.Errorf("A is not in the object store: PC a's bytes exist nowhere (%v) although the backup branch names them", s066ListObjects(r.storeDir))
		}
	})

	t.Run("the upload failed until the final sync", func(t *testing.T) {
		r := s066Diverge(t, a0, b0)
		a := r.a
		s066Freeze(t, r.storeDir)
		diverged(t, a)
		take(t, a)
		if got := a.read("storage/report.pdf"); got != a0 {
			t.Fatalf("setup: take-remote changed storage/report.pdf to %q", got)
		}
		s066Thaw(t, r.storeDir)
		if out, errs, code := s066Sync(a); code != 0 {
			t.Errorf("sync after take: exit %d\nstdout %q\nstderr %q", code, out, errs)
		}
		if got := a.read("storage/report.pdf"); got != b0 {
			t.Errorf("a: storage/report.pdf = %q, want %q", got, b0)
		}
		if !s066ObjectIs(r.storeDir, a0) {
			t.Errorf("A is not in the object store: the stale file was replaced before its bytes were saved (%v)", s066ListObjects(r.storeDir))
		}
	})

	t.Run("still unwritable at the final sync", func(t *testing.T) {
		r := s066Diverge(t, a0, b0)
		a := r.a
		s066Freeze(t, r.storeDir)
		diverged(t, a)
		take(t, a)
		_, stderr, _ := s066Sync(a)
		if got := a.read("storage/report.pdf"); got != a0 {
			t.Fatalf("a: storage/report.pdf = %q, want a's own bytes kept while they have no copy (F-1 a)", got)
		}
		if want := "storage: kept report.pdf: the replaced version has no copy on the remote yet ("; !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
		s066Thaw(t, r.storeDir)
		s066MustSync(t, a)
		if got := a.read("storage/report.pdf"); got != b0 || !s066ObjectIs(r.storeDir, a0) {
			t.Errorf("after the remote came back: storage/report.pdf = %q, A in the store: %v; want %q and true", got, s066ObjectIs(r.storeDir, a0), b0)
		}
	})
}

// TestTakeRemoteKeepsUnuploadedStorage is C-066-8's second case, B7 through
// --take-remote: the catalog edit that named a never-uploaded file is discarded
// (it is on the backup branch), so the next reconcile sees "removed elsewhere".
func TestTakeRemoteKeepsUnuploadedStorage(t *testing.T) {
	const body = "payload stored on a, never uploaded\n"
	scenario := func(t *testing.T) *s066Rig {
		t.Helper()
		r := newS066Rig(t, false)
		a, b := r.a, r.b
		s066AutoSync(t, a, false)
		s066AutoSync(t, b, false)
		r.store(t, a, "seed.bin", "a file both PCs have\n") // makes the catalog a tracked file on both
		s066MustSync(t, a)
		s066MustSync(t, b)
		s066Freeze(t, r.storeDir)
		r.store(t, a, "h.bin", body)
		r.store(t, b, "h2.bin", "payload from b\n") // sorts beside h.bin: the two catalog edits conflict
		s066Sync(b)                                 // pushes the catalog; its own upload fails, which does not matter here
		if _, errs, code := s066Sync(a); code != 1 || !strings.Contains(errs, "diverged") {
			t.Fatalf("setup: a's sync: exit %d stderr %q, want a refusal as diverged", code, errs)
		}
		if out, errs, _ := a.lw("sync", "--take-remote", "--vault", a.root); !strings.Contains(out, "took ") {
			t.Fatalf("setup: sync --take-remote did not take the remote\nstdout %q\nstderr %q", out, errs)
		}
		return r
	}

	t.Run("saved on the remote, then removed", func(t *testing.T) {
		r := scenario(t)
		a := r.a
		s066Thaw(t, r.storeDir)
		_, stderr, _ := s066Sync(a)
		if !s066ObjectIs(r.storeDir, body) {
			t.Errorf("the payload is in no object store: the catalog edit that named it was taken away and the file deleted (B7); stderr %q", stderr)
		}
		if s066Exists(filepath.Join(a.root, "storage", "h.bin")) {
			t.Errorf("a: storage/h.bin was neither deleted nor kept consistently; stderr %q", stderr)
		}
	})

	t.Run("kept while the store is unwritable", func(t *testing.T) {
		r := scenario(t)
		a := r.a
		_, stderr, _ := s066Sync(a)
		if got := a.read("storage/h.bin"); got != body {
			t.Fatalf("a: storage/h.bin = %q, want it kept (B7 via --take-remote)", got)
		}
		if want := "storage: kept h.bin: no copy on the remote yet ("; !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
		s066Thaw(t, r.storeDir)
		s066Sync(a)
		if !s066ObjectIs(r.storeDir, body) {
			t.Error("the payload never reached the store after the remote came back")
		}
	})
}

// TestDivergedSyncStillUploads is X-2: the payloads go up before the pull, so a
// pull that is refused (diverged, dirty, a rebase conflict) still leaves them on
// the remote. The after-the-pull upload never ran on those paths.
func TestDivergedSyncStillUploads(t *testing.T) {
	const a0, b0 = "bytes of PC a\n", "bytes of PC b\n"

	t.Run("lw sync", func(t *testing.T) {
		r := s066Diverge(t, a0, b0)
		if _, errs, code := s066Sync(r.a); code != 1 || !strings.Contains(errs, "diverged") {
			t.Fatalf("a's sync: exit %d stderr %q, want a refusal as diverged", code, errs)
		}
		if !s066ObjectIs(r.storeDir, a0) {
			t.Errorf("A was not uploaded by a sync that was refused as diverged (%v)", s066ListObjects(r.storeDir))
		}
	})

	t.Run("the auto-push after a note", func(t *testing.T) {
		r := s066Diverge(t, a0, b0)
		a := r.a
		s066AutoSync(t, a, true)
		noteAt(t, 0)
		stdout, stderr, code := a.lw("note", "-m", "x", "--vault", a.root)
		if code != 0 {
			t.Errorf("lw note exit %d, want 0\nstdout %q\nstderr %q", code, stdout, stderr)
		}
		if !s066ObjectIs(r.storeDir, a0) {
			t.Errorf("A was not uploaded by an auto-push that the divergence stopped (%v)\nstderr %q", s066ListObjects(r.storeDir), stderr)
		}
	})
}

// TestSyncDoesNotRehashUnchanged is X-6 (F-3). A sync of files that are where
// the ledger put them, still the catalog's size, and already on the remote reads
// none of them and asks the remote nothing: 062's per-sync cost grew with the
// data (a 1 GiB payload cost 1.9 s of hashing on every lw sync, and a round
// trip on every auto-push). Over ssh a round trip is the visible cost, so that
// is the rig.
func TestSyncDoesNotRehashUnchanged(t *testing.T) {
	r := newS066Rig(t, true)
	a, b := r.a, r.b
	r.store(t, a, "one.bin", "the first payload, uploaded by the add\n")
	r.store(t, a, "two.bin", "the second payload, uploaded by the add\n")
	s066MustSync(t, b)
	s066MustSync(t, a)
	s066MustSync(t, b)
	if b.read("storage/two.bin") == "" {
		t.Fatal("setup: b did not receive the files")
	}

	log := os.Getenv("FAKE_SSH_LOG")
	if err := os.WriteFile(log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h0 := storage.HashCount()
	s066MustSync(t, a)
	s066MustSync(t, b)
	s066MustSync(t, a)

	if d := storage.HashCount() - h0; d != 0 {
		t.Errorf("three syncs of unchanged, confirmed files hashed %d file(s), want 0 (F-3)", d)
	}
	data, _ := os.ReadFile(log)
	if n := s066Lines(string(data), "objects"); n != 0 {
		t.Errorf("three syncs of confirmed files made %d object-store ssh call(s), want 0 (the confirmed bit skips HaveObjects)", n)
	}
}

// TestUploadAsksBeforeSending: the remote is asked once for the whole batch and
// only what it lacks is sent. A PC that has lost its record of what the remote
// holds (a restored backup, a deleted ledger) is in the position of a first
// upload; sending every payload again would cost bandwidth for nothing, and the
// answer is recorded so the next sync asks nothing.
func TestUploadAsksBeforeSending(t *testing.T) {
	r := newS066Rig(t, true)
	a := r.a
	const body = "a payload the remote already holds\n"
	r.store(t, a, "k", body) // auto-sync is on: uploaded and pushed
	if !s066ObjectIs(r.storeDir, body) {
		t.Fatal("setup: the add did not upload")
	}
	if err := os.Remove(filepath.Join(a.root, ".llmwiki", "tmp", "storage-local.json")); err != nil {
		t.Fatal(err)
	}
	log := os.Getenv("FAKE_SSH_LOG")
	if err := os.WriteFile(log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s066MustSync(t, a)
	data, _ := os.ReadFile(log)
	if n := s066Lines(string(data), "read -r p n"); n != 1 {
		t.Errorf("the sync asked the remote %d time(s) which objects it holds, want 1", n)
	}
	if n := s066Lines(string(data), "cat > "); n != 0 {
		t.Errorf("the sync sent a payload the remote already holds (%d upload call(s))", n)
	}

	if err := os.WriteFile(log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s066MustSync(t, a)
	data, _ = os.ReadFile(log)
	if n := s066Lines(string(data), "objects"); n != 0 {
		t.Errorf("the next sync made %d object-store call(s); the answer should have been recorded", n)
	}
}

// TestUnuploadableEntryIsTold is X-9's second half: a catalog entry whose file is
// on this PC but is not the catalog's cannot be uploaded (the object store would
// refuse the bytes), and that used to be silent. It is one line per run - not one
// per upload attempt, and an explicit sync attempts twice.
func TestUnuploadableEntryIsTold(t *testing.T) {
	r := newS066Rig(t, false)
	a := r.a
	s066AutoSync(t, a, false)
	r.store(t, a, "f.bin", "the bytes the catalog describes\n")
	if err := os.WriteFile(filepath.Join(a.root, "storage", "f.bin"), []byte("bytes somebody else wrote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const want = "storage: f.bin cannot be uploaded: this PC's copy is not the one the catalog describes"

	stdout, stderr, code := s066Sync(a)
	if n := s066Lines(stderr, want); n != 1 {
		t.Errorf("lw sync told the unuploadable entry %d times, want once\nstdout %q\nstderr %q", n, stdout, stderr)
	}
	if code != 0 {
		t.Errorf("exit %d, want 0: the line is information, doctor is where the damage fails\nstderr %q", code, stderr)
	}
	if s066ObjectIs(r.storeDir, "the bytes the catalog describes\n") || s066ObjectIs(r.storeDir, "bytes somebody else wrote\n") {
		t.Error("an object was published for an entry whose file is not the catalog's")
	}

	s066AutoSync(t, a, true)
	noteAt(t, 0)
	_, stderr, _ = a.lw("note", "-m", "x", "--vault", a.root)
	if n := s066Lines(stderr, want); n != 1 {
		t.Errorf("the auto-push told it %d times, want once\nstderr %q", n, stderr)
	}
}

// TestAutoPushDefersLargeUpload is X-6's other half: the auto-push has a 15 s
// deadline for the whole step, and a 200 MB payload re-sent and cut at every push
// would never arrive. Over a size threshold the auto path leaves the upload to
// the next explicit lw sync and says so in one line.
func TestAutoPushDefersLargeUpload(t *testing.T) {
	if storageAutoMaxObject != 32<<20 {
		t.Errorf("storageAutoMaxObject = %d, want 32 MiB", storageAutoMaxObject)
	}
	orig := storageAutoMaxObject
	storageAutoMaxObject = 20
	t.Cleanup(func() { storageAutoMaxObject = orig })

	r := newS066Rig(t, false)
	a := r.a
	const small, big = "small payload\n", "a payload bigger than the twenty byte threshold\n"
	s066AutoSync(t, a, false)
	r.store(t, a, "small.bin", small)
	r.store(t, a, "big.bin", big)
	s066AutoSync(t, a, true)

	noteAt(t, 0)
	_, stderr, code := a.lw("note", "-m", "x", "--vault", a.root)
	if code != 0 {
		t.Errorf("lw note exit %d, stderr %q", code, stderr)
	}
	if !s066ObjectIs(r.storeDir, small) {
		t.Error("the small payload was not uploaded by the auto-push")
	}
	if s066ObjectIs(r.storeDir, big) {
		t.Error("the auto-push uploaded an object over the threshold (F-3)")
	}
	if want := "storage: big.bin (48 B) uploads on the next lw sync"; !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want a line %q", stderr, want)
	}
	if strings.Contains(stderr, "small.bin") {
		t.Errorf("stderr mentions the small payload: %q", stderr)
	}

	// The explicit sync has no threshold.
	if out, errs, code := s066Sync(a); code != 0 {
		t.Errorf("lw sync exit %d\nstdout %q\nstderr %q", code, out, errs)
	}
	if !s066ObjectIs(r.storeDir, big) {
		t.Error("lw sync did not upload the large payload")
	}
}

// TestStorageVerbsPullFirst pins the 047/059 pattern the S3 review found
// unpinned (survivors M12, M38): `lw storage add` and `rm` pull before they edit
// the catalog, so two PCs rarely edit storage.json from the same base. Without
// it, b's entry and a's land in the same place of the file, the push's rebase
// conflicts, and a's store is refused as diverged instead of simply landing.
func TestStorageVerbsPullFirst(t *testing.T) {
	for _, verb := range []string{"add", "rm"} {
		t.Run(verb, func(t *testing.T) {
			r := newS066Rig(t, false)
			a, b := r.a, r.b
			r.store(t, a, "m.bin", "the entry a will rm\n")
			s066MustSync(t, b)
			r.store(t, b, "n.bin", "stored on b while a is behind\n") // auto-sync: pushed
			switch verb {
			case "add":
				r.store(t, a, "o.bin", "stored on a, which has not pulled n.bin\n")
			case "rm":
				if out, errs, code := a.lw("storage", "rm", "m.bin", "--vault", a.root); code != 0 {
					t.Fatalf("storage rm on a: exit %d stdout %q stderr %q", code, out, errs)
				}
			}
			head := a.git("rev-parse", "HEAD")
			if got := remoteHead(t, r.gitDir); got != head {
				t.Fatalf("remote main = %s, want a's HEAD %s: a's catalog edit was not pushed (the pull before the edit is what keeps it a fast-forward)", got, head)
			}
			cat := remoteFile(t, r.gitDir, ".llmwiki/storage.json")
			if !strings.Contains(cat, `"n.bin"`) {
				t.Errorf("the remote's catalog lost b's entry:\n%s", cat)
			}
			if verb == "add" && !strings.Contains(cat, `"o.bin"`) || verb == "rm" && strings.Contains(cat, `"m.bin"`) {
				t.Errorf("the remote's catalog does not show a's %s:\n%s", verb, cat)
			}
		})
	}
}

// TestStorageUploadFollowsTheRemote: a confirmation is a statement about one
// remote. A vault moved to a new remote (sync init, a changed [sync] remotes)
// must upload to it again; believing the old remote's confirmations would leave
// the new one without objects and the old one's bit deleting the only copy.
func TestStorageUploadFollowsTheRemote(t *testing.T) {
	syncHermetic(t)
	root := testutil.CopyFixture(t, "minimal")
	const body = "stored once, uploaded to two remotes\n"
	if out, errs, code := captureRun(t, func() int {
		return run([]string{"storage", "add", s066Source(t, "f.bin", body), "--vault", root})
	}); code != 0 {
		t.Fatalf("storage add: exit %d stdout %q stderr %q", code, out, errs)
	}
	remoteA := filepath.Join(t.TempDir(), "a.git")
	remoteB := filepath.Join(t.TempDir(), "b.git")
	up := func(remote string) {
		t.Helper()
		o := vaultsync.Options{Dir: root, Remotes: []string{remote}}
		if err := storageUpload(context.Background(), root, o); err != nil {
			t.Fatalf("storageUpload to %s: %v", remote, err)
		}
	}
	up(remoteA)
	if !s066ObjectIs(remoteA+".storage", body) {
		t.Fatal("the first upload did not happen")
	}
	up(remoteB)
	if !s066ObjectIs(remoteB+".storage", body) {
		t.Errorf("the object is not in the second remote's store: a confirmation for remote A was believed for remote B")
	}
}

// TestStorageSecureAnswers pins what the proof rule P asks for says about each
// kind of failure: a remote that cannot be reached is transient (the same call
// can succeed later, so the ledger row is kept and the file waits); a remote
// that cannot hold objects at all is not.
func TestStorageSecureAnswers(t *testing.T) {
	syncHermetic(t)
	installFakeSSH(t)
	body := "bytes whose copy is being asked for\n"
	path := s066Source(t, "f.bin", body)
	blob := storage.Blob{Name: "f.bin", SHA256: s066SHA(body), Size: int64(len(body)), Path: path}
	ask := func(remote string) (storage.EnsureResult, error) {
		answers, batch := storageSecure(context.Background(), vaultsync.Options{Dir: t.TempDir(), Remotes: []string{remote}}, []storage.Blob{blob})
		return answers[blob.SHA256], batch
	}

	r, batch := ask("deadhost:lw-vault")
	if r.Held || !r.Transient || batch == nil || !strings.Contains(r.Err.Error(), "remote unreachable") {
		t.Errorf("an unreachable remote: %+v, batch %v; want not held, transient, 'remote unreachable'", r, batch)
	}
	r, batch = ask("https://example.test/lw-vault.git")
	if r.Held || r.Transient || r.Err == nil || batch == nil {
		t.Errorf("a remote that cannot hold objects: %+v, batch %v; want not held and not transient", r, batch)
	}
	store := filepath.Join(t.TempDir(), "remote.git")
	r, batch = ask(store)
	if !r.Held || !r.Uploaded || batch != nil || !s066ObjectIs(store+".storage", body) {
		t.Errorf("a local remote: %+v, batch %v; want the payload uploaded", r, batch)
	}
	r, batch = ask(store)
	if !r.Held || r.Uploaded || batch != nil {
		t.Errorf("the same remote again: %+v, batch %v; want held and not uploaded again", r, batch)
	}
}

// TestUnsupportedRemoteWarnsNotFails is X-7 (F-4): an https:// or git:// remote
// carries git and cannot hold storage files. 062's first version made every
// `lw sync` exit 1 forever once the catalog had an entry, with the label "remote
// unreachable". It is a configuration fact, told once per run as a warning, and
// doctor flags it.
func TestUnsupportedRemoteWarnsNotFails(t *testing.T) {
	a, _, remote := syncPair(t)
	const spec = "https://example.test/lw-vault.git"
	gitcfg := os.Getenv("GIT_CONFIG_GLOBAL")
	if err := os.WriteFile(gitcfg, []byte("[url \""+remote+"\"]\n\tinsteadOf = "+spec+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.act()
	cfg := mustLoadConfig(t)
	cfg.Sync.Remotes = []string{spec}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if out, errs, code := s066Sync(a); code != 0 {
		t.Fatalf("setup: a plain sync over the rewritten https remote: exit %d\nstdout %q\nstderr %q", code, out, errs)
	}

	src := s066Source(t, "f.bin", "a payload with nowhere to go\n")
	stdout, stderr, code := a.lw("storage", "add", src, "--vault", a.root)
	if code != 0 {
		t.Fatalf("storage add: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	want := "storage: " + spec + " cannot hold storage files (needs an ssh or local path remote)"
	if n := s066Lines(stderr, want); n != 1 {
		t.Errorf("storage add (an auto-sync verb): the warning appeared %d times, want once per run\nstderr %q", n, stderr)
	}

	stdout, stderr, code = s066Sync(a)
	if code != 0 {
		t.Errorf("lw sync: exit %d, want 0 (a remote that cannot hold files is not a failure)\nstdout %q\nstderr %q", code, stdout, stderr)
	}
	if n := s066Lines(stderr, want); n != 1 {
		t.Errorf("lw sync: the warning appeared %d times, want once\nstderr %q", n, stderr)
	}
	for _, bad := range []string{"unreachable", "run lw sync again"} {
		if strings.Contains(stderr, bad) {
			t.Errorf("lw sync stderr mentions %q for a remote that is merely the wrong kind: %q", bad, stderr)
		}
	}

	rep := runDoctor(context.Background(), a.root, doctorOptions{})
	c := checkByName(t, rep, "storage remote")
	if !c.OK || !c.Warn || !strings.Contains(c.Detail, "cannot hold storage files") {
		t.Errorf("doctor 'storage remote' check = %+v, want OK with Warn naming the problem", c)
	}
}

// TestBadCatalogShaIsCorruptNotPanic is X-8 (F-6). The catalog is a git file that
// other PCs write. One entry whose sha256 is not 64 lower-case hex digits used to
// panic `lw storage get` (a slice of the first 12 characters), and made the
// batch question to the remote fail as a whole, so no other payload moved.
func TestBadCatalogShaIsCorruptNotPanic(t *testing.T) {
	r := newS066Rig(t, false)
	a, b := r.a, r.b
	const g1, g2 = "the first good payload\n", "the second good payload, stored after the bad record\n"
	s066AutoSync(t, a, false)
	r.store(t, a, "good1.bin", g1)

	cat := filepath.Join(a.root, ".llmwiki", "storage.json")
	data, err := os.ReadFile(cat)
	if err != nil {
		t.Fatal(err)
	}
	var m storage.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	m.Entries = append(m.Entries, storage.Entry{Name: "bad.bin", SHA256: "abc", Size: 3, Added: time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)})
	data, _ = json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(cat, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	r.store(t, a, "good2.bin", g2)

	// With no file, and with a file there (the shape that sliced the first 12
	// characters of "abc" for the corrupt message).
	for _, withFile := range []bool{false, true} {
		if withFile {
			a.write("storage/bad.bin", "xyz")
		}
		_, stderr, code, panicked := s066Run(a, "storage", "get", "bad.bin", "--vault", a.root)
		if panicked != nil {
			t.Errorf("lw storage get bad.bin (file there: %v) panicked: %v", withFile, panicked)
		} else if code != 1 || !strings.Contains(stderr, "bad.bin") || !strings.Contains(stderr, "not 64") {
			t.Errorf("storage get bad.bin (file there: %v): exit %d stderr %q, want exit 1 naming the entry and what is wrong with it", withFile, code, stderr)
		}
	}
	if err := os.Remove(filepath.Join(a.root, "storage", "bad.bin")); err != nil {
		t.Fatal(err)
	}
	out, _, _, panicked := s066Run(a, "storage", "list", "-v", "--vault", a.root)
	if panicked != nil {
		t.Errorf("lw storage list -v panicked: %v", panicked)
	}
	if row := s066RowOf(out, "bad.bin"); !s066Contains(row, "corrupt") {
		t.Errorf("storage list -v row for bad.bin = %q, want corrupt\n%s", row, out)
	}

	stdout, stderr, code, panicked := s066Run(a, "sync", "--vault", a.root)
	if panicked != nil {
		t.Errorf("lw sync panicked: %v", panicked)
	}
	if code != 0 {
		t.Errorf("lw sync: exit %d, want 0: one bad record must not fail the batch\nstdout %q\nstderr %q", code, stdout, stderr)
	}
	for _, body := range []string{g1, g2} {
		if !s066ObjectIs(r.storeDir, body) {
			t.Errorf("a good payload was not uploaded because of the bad record (%v)", s066ListObjects(r.storeDir))
		}
	}
	c := s066Doctor(t, a)
	if c.OK || !strings.Contains(c.Detail, "1 corrupt (changed on disk)") || !strings.Contains(c.Remedy, "bad.bin") {
		t.Errorf("doctor storage check = %+v, want it failed, naming 1 corrupt and bad.bin in the remedy", c)
	}

	if out, errs, code, panicked := s066Run(b, "sync", "--vault", b.root); panicked != nil || code != 0 {
		t.Errorf("b: lw sync with the bad record pulled in: exit %d panic %v\nstdout %q\nstderr %q", code, panicked, out, errs)
	}
	if got := b.read("storage/good2.bin"); got != g2 {
		t.Errorf("b: storage/good2.bin = %q, want %q: the bad record stopped the fetch", got, g2)
	}
}

// s066RowOf returns the whitespace-separated fields of the line that starts with name.
func s066RowOf(listing, name string) []string {
	for _, line := range strings.Split(listing, "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] == name {
			return f
		}
	}
	return nil
}

// TestDamagedRemoteObjectLabelled is X-4 (F-7): an object on the remote whose
// length is wrong is "damaged", not "not arrived yet", and it can be repaired
// from any PC that holds the right bytes.
func TestDamagedRemoteObjectLabelled(t *testing.T) {
	r := newS066Rig(t, false)
	a, b := r.a, r.b
	const body = "the bytes the catalog names, cut short on the remote\n"
	r.store(t, a, "k", body) // auto-sync on: uploaded and pushed
	obj := s066ObjectPath(r.storeDir, body)
	if err := os.WriteFile(obj, []byte(body[:10]), 0o644); err != nil {
		t.Fatal(err)
	}
	s066AutoSync(t, b, false)

	stdout, stderr, code := s066Sync(b)
	if code != 1 || !strings.Contains(stderr, "remote object for k is damaged") {
		t.Errorf("b: lw sync exit %d stderr %q, want exit 1 and the line 'remote object for k is damaged'\nstdout %q", code, stderr, stdout)
	}
	if strings.Contains(stderr, "has not arrived") {
		t.Errorf("a damaged object was reported as not arrived: %q", stderr)
	}
	c := s066Doctor(t, b)
	if !c.OK || !c.Warn || c.Detail != "0 of 1 verified; 1 pending (remote object is damaged)" {
		t.Errorf("b: doctor storage check = %+v, want OK with Warn and 'remote object is damaged'", c)
	}
	sha := s066SHA(body)
	if want := "lw storage add <file> --name k"; !strings.Contains(c.Remedy, want) || !strings.Contains(c.Remedy, "objects/"+sha[:2]+"/"+sha[2:]) {
		t.Errorf("b: doctor remedy = %q, want the repair (%q) and the path of the object to delete when only its bytes are wrong", c.Remedy, want)
	}
	if out, _, _ := b.lw("storage", "list", "-v", "--vault", b.root); !strings.Contains(out, "pending (remote object is damaged)") {
		t.Errorf("b: storage list -v does not say the object is damaged:\n%s", out)
	}

	// Repair: b is given the right bytes. Add materializes the absent file, and the
	// next sync sees that the remote's object has the wrong length and replaces it.
	if out, errs, code := b.lw("storage", "add", s066Source(t, "orig.bin", body), "--name", "k", "--vault", b.root); code != 0 {
		t.Fatalf("b: storage add of the catalog's own bytes: exit %d stdout %q stderr %q", code, out, errs)
	}
	if got := b.read("storage/k"); got != body {
		t.Fatalf("b: storage/k = %q after Add, want the file healed (F-7)", got)
	}
	if out, errs, code := s066Sync(b); code != 0 {
		t.Fatalf("b: lw sync after the heal: exit %d\nstdout %q\nstderr %q", code, out, errs)
	}
	if !s066ObjectIs(r.storeDir, body) {
		t.Error("the damaged object was not replaced by the PC that holds the right bytes")
	}
	c3 := newSyncPC(t, "c")
	if out, errs, code := c3.lw("sync", "clone", r.spec, c3.root); code != 0 {
		t.Fatalf("clone into c: exit %d stdout %q stderr %q", code, out, errs)
	}
	if got := c3.read("storage/k"); got != body {
		t.Errorf("c: storage/k = %q after the repair, want %q", got, body)
	}
}

// TestSyncStatusShowsStorage is X-11 (F-10, plan R5): `lw sync status` tells the
// counts when the catalog has entries, and nothing when it has none (042's
// output stays byte-identical for a vault that stores nothing).
func TestSyncStatusShowsStorage(t *testing.T) {
	r := newS066Rig(t, false)
	a, b := r.a, r.b

	if out, errs, code := a.lw("sync", "status", "--vault", a.root); code != 0 || strings.Contains(out, "storage:") {
		t.Fatalf("sync status with an empty catalog: exit %d stdout %q stderr %q; want no storage line", code, out, errs)
	}

	r.store(t, a, "f.bin", "a payload\n")
	out, errs, code := a.lw("sync", "status", "--vault", a.root)
	if code != 0 || !strings.Contains(out, "storage: 1 verified, 0 pending, 0 corrupt\n") {
		t.Errorf("a: sync status: exit %d stdout %q stderr %q; want 'storage: 1 verified, 0 pending, 0 corrupt'", code, out, errs)
	}

	noteAt(t, 0)
	if _, errs, code := b.lw("note", "-m", "pulls the catalog", "--vault", b.root); code != 0 {
		t.Fatalf("lw note on b: exit %d stderr %q", code, errs)
	}
	out, errs, code = b.lw("sync", "status", "--vault", b.root)
	if code != 0 || !strings.Contains(out, "storage: 0 verified, 1 pending, 0 corrupt\n") {
		t.Errorf("b: sync status: exit %d stdout %q stderr %q; want 'storage: 0 verified, 1 pending, 0 corrupt'", code, out, errs)
	}
	if err := os.MkdirAll(filepath.Join(b.root, "storage"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.root, "storage", "f.bin"), []byte("somebody's own bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, _ = b.lw("sync", "status", "--vault", b.root)
	if !strings.Contains(out, "storage: 0 verified, 0 pending, 1 corrupt\n") {
		t.Errorf("b: sync status after a local edit: stdout %q; want 1 corrupt", out)
	}

	// rm's help says what rm leaves behind.
	help, _, _ := a.lw("storage", "help")
	if !strings.Contains(help, "until a future lw storage gc") {
		t.Errorf("lw storage help does not say remote objects remain after rm:\n%s", help)
	}
}

// TestStaleShownAsReplaced is X-12 (F-11): an entry whose file is an older
// version lw put there reads "pending (replaced on another PC)", not "not
// arrived yet", in list -v, show and doctor.
func TestStaleShownAsReplaced(t *testing.T) {
	r := newS066Rig(t, false)
	a, b := r.a, r.b
	r.store(t, a, "g", "the first version\n")
	s066MustSync(t, b)
	r.store(t, a, "g", "the second version, longer\n", "--force") // auto-sync on: uploaded and pushed
	noteAt(t, 0)
	if _, errs, code := b.lw("note", "-m", "pulls the catalog", "--vault", b.root); code != 0 {
		t.Fatalf("lw note on b: exit %d stderr %q", code, errs)
	}
	if got := b.read("storage/g"); got != "the first version\n" {
		t.Fatalf("setup: b's storage/g = %q, want the stale first version", got)
	}

	out, _, _ := b.lw("storage", "list", "-v", "--vault", b.root)
	if !strings.Contains(out, "pending (replaced on another PC)") {
		t.Errorf("storage list -v:\n%s\nwant 'pending (replaced on another PC)'", out)
	}
	out, _, _ = b.lw("storage", "show", "g", "--vault", b.root)
	if !strings.Contains(out, "Status:  pending (replaced on another PC)") {
		t.Errorf("storage show:\n%s\nwant 'Status:  pending (replaced on another PC)'", out)
	}
	if c := s066Doctor(t, b); !c.OK || !c.Warn || c.Detail != "0 of 1 verified; 1 pending (replaced on another PC)" {
		t.Errorf("doctor storage check = %+v, want OK with Warn and 'pending (replaced on another PC)'", c)
	}
	// And a plain missing file still says it has not arrived.
	if err := os.Remove(filepath.Join(b.root, "storage", "g")); err != nil {
		t.Fatal(err)
	}
	if c := s066Doctor(t, b); c.Detail != "0 of 1 verified; 1 pending (not arrived yet)" {
		t.Errorf("doctor storage check for an absent file = %q", c.Detail)
	}
}

// TestStorageLocalErrorHasNoRerunAdvice is X-12's first half: "(wiki synced; run lw
// sync again)" is advice for the remote; an unreadable ledger or catalog is local,
// a rerun reads the same bytes, and the message must name the file instead.
func TestStorageLocalErrorHasNoRerunAdvice(t *testing.T) {
	cases := []struct {
		name string
		file string // relative to the vault
		bad  string
	}{
		{"ledger", ".llmwiki/tmp/storage-local.json", "{not json"},
		{"catalog", ".llmwiki/storage.json", "{not json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newS066Rig(t, false)
			a := r.a
			r.store(t, a, "f.bin", "a payload\n")
			if err := os.WriteFile(filepath.Join(a.root, filepath.FromSlash(tc.file)), []byte(tc.bad), 0o644); err != nil {
				t.Fatal(err)
			}
			stdout, stderr, code := s066Sync(a)
			if code != 1 {
				t.Errorf("exit %d, want 1\nstdout %q\nstderr %q", code, stderr, stdout)
			}
			if !strings.Contains(stderr, filepath.Base(tc.file)) {
				t.Errorf("stderr %q does not name %s", stderr, tc.file)
			}
			if strings.Contains(stderr, "(wiki synced; run lw sync again)") {
				t.Errorf("stderr %q gives rerun advice for an error a rerun cannot fix", stderr)
			}
			if !strings.Contains(stderr, "(wiki synced)") {
				t.Errorf("stderr %q does not say the wiki synced", stderr)
			}
			if n := s066Lines(stderr, "storage:"); n != 1 {
				t.Errorf("stderr has %d storage lines, want the one reason once\n%s", n, stderr)
			}
		})
	}
}

// TestPendingOnlySyncExitsZero pins C-066-7 (survivor M3): a payload that has not
// arrived is the normal state of a PC whose neighbour has not uploaded, so the
// sync that finds nothing else wrong exits 0 and says so.
func TestPendingOnlySyncExitsZero(t *testing.T) {
	r := newS066Rig(t, false)
	a, b := r.a, r.b
	r.store(t, a, "k", "payload nobody uploaded\n")
	s066MustSync(t, a)
	if err := os.RemoveAll(r.storeDir); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := s066Sync(b)
	if code != 0 {
		t.Errorf("lw sync with only a pending payload: exit %d, want 0\nstdout %q\nstderr %q", code, stdout, stderr)
	}
	if want := "storage: k has not arrived yet (stored on another PC; run lw sync there)"; !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
	if strings.Contains(stderr, "wiki synced") {
		t.Errorf("stderr = %q: a pending payload is not an error", stderr)
	}
}

// TestGetCorruptMessage pins D4's text (survivor M9): `lw storage get` refuses
// bytes that are not the catalog's, by name and by both shas, and serves nothing.
func TestGetCorruptMessage(t *testing.T) {
	r := newS066Rig(t, false)
	a, b := r.a, r.b
	const body, edited = "the bytes the catalog names\n", "bytes somebody else wrote here\n"
	r.store(t, a, "k", body)
	s066MustSync(t, b)
	if err := os.WriteFile(filepath.Join(b.root, "storage", "k"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := b.lw("storage", "get", "k", "--vault", b.root)
	want := "lw: storage: k is corrupt on disk (sha256 " + s066SHA(edited)[:12] + " ≠ catalog " + s066SHA(body)[:12] + ")\n"
	if code != 1 || stdout != "" || stderr != want {
		t.Errorf("storage get of a corrupt file: exit %d stdout %q stderr %q; want exit 1, nothing on stdout, stderr %q", code, stdout, stderr, want)
	}
}

// TestSlowUploadStillPushes pins R5's time bound (survivor M6): an upload that
// hangs gets half of what is left of the auto-sync step's deadline, so the git
// push that follows still has time to run. Over ssh because that is where a slow
// upload happens; the object-store calls are the ones that hang.
func TestSlowUploadStillPushes(t *testing.T) {
	r := newS066Rig(t, true)
	a := r.a
	realSSH, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in *objects*) exec sleep 120 ;; esac\nexec " + realSSH + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(wrapper, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s066AutoSync(t, a, false)
	r.store(t, a, "k", "a payload whose upload hangs\n")
	s066AutoSync(t, a, true)
	t.Setenv("PATH", wrapper+string(os.PathListSeparator)+os.Getenv("PATH"))

	orig := syncAutoTimeout
	syncAutoTimeout = 6 * time.Second
	t.Cleanup(func() { syncAutoTimeout = orig })
	before := remoteHead(t, r.gitDir)

	noteAt(t, 0)
	start := time.Now()
	stdout, stderr, code := a.lw("note", "-m", "x", "--vault", a.root)
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Errorf("lw note took %v with a hung upload", elapsed)
	}
	if code != 0 {
		t.Errorf("lw note exit %d, want 0\nstdout %q\nstderr %q", code, stdout, stderr)
	}
	head := a.git("rev-parse", "HEAD")
	if got := remoteHead(t, r.gitDir); got == before || got != head {
		t.Errorf("remote main = %s, want a's HEAD %s, advanced from %s: the hung upload took the step's whole deadline and the push never ran", got, head, before)
	}
	if !strings.Contains(stderr, "(wiki synced; run lw sync again)") {
		t.Errorf("stderr = %q, want the storage line", stderr)
	}
}

// TestPusherReportsStorageErr pins the TUI pusher's hand-off (survivor M19): the
// upload failure of the last push reaches Flush's report, once.
func TestPusherReportsStorageErr(t *testing.T) {
	boom := errors.New("storage: upload of h failed: boom")
	p := newSyncPusher(func(context.Context) syncPushResult {
		return syncPushResult{Remote: "r", Pushed: 1, StorageErr: boom}
	}, time.Millisecond)
	p.Trigger()
	res := p.Flush(5*time.Second, nil)
	if !errors.Is(res.StorageErr, boom) || res.Pushed != 1 {
		t.Errorf("Flush = %+v, want Pushed 1 and the storage error", res)
	}
	var buf bytes.Buffer
	(&autoSync{}).report(&buf, res)
	if want := "storage: upload of h failed: boom (wiki synced; run lw sync again)\n"; !strings.Contains(buf.String(), want) {
		t.Errorf("report = %q, want it to contain %q", buf.String(), want)
	}
	if again := p.Flush(time.Second, nil); again.StorageErr != nil {
		t.Errorf("a second Flush reported %v; the session is told once", again.StorageErr)
	}
}

// TestStorageCollisionRefusal is X-15 (C-066-9): storage add and rm are writing
// verbs of a synced vault like lw note, so a checkout collision refuses them, with
// the same text, before anything changes.
func TestStorageCollisionRefusal(t *testing.T) {
	a, _, _ := syncPair(t)
	if out, errs, code := a.lw("storage", "add", s066Source(t, "keep.bin", "already stored\n"), "--vault", a.root); code != 0 {
		t.Fatalf("setup: storage add: exit %d stdout %q stderr %q", code, out, errs)
	}
	markCollision(t, a.root)
	want := "lw: storage: " + collisionText + "\n"

	stdout, stderr, code := a.lw("storage", "add", s066Source(t, "new.bin", "refused\n"), "--vault", a.root)
	if code != 1 || stdout != "" || stderr != want {
		t.Errorf("storage add during a collision: exit %d stdout %q stderr %q; want exit 1 and %q", code, stdout, stderr, want)
	}
	if s066Exists(filepath.Join(a.root, "storage", "new.bin")) {
		t.Error("the refused add stored the file")
	}
	stdout, stderr, code = a.lw("storage", "rm", "keep.bin", "--vault", a.root)
	if code != 1 || stdout != "" || stderr != want {
		t.Errorf("storage rm during a collision: exit %d stdout %q stderr %q; want exit 1 and %q", code, stdout, stderr, want)
	}
	if a.read("storage/keep.bin") != "already stored\n" {
		t.Error("the refused rm removed the file")
	}
	// Reading is not writing.
	if out, errs, code := a.lw("storage", "list", "--vault", a.root); code != 0 || !strings.Contains(out, "keep.bin") {
		t.Errorf("storage list during a collision: exit %d stdout %q stderr %q; want it to work", code, out, errs)
	}
}

// TestStorageErrorPrefix is X-15's other half: dispatch already prefixes
// "lw: storage: ", and the errors from internal/storage carried "storage: " too.
func TestStorageErrorPrefix(t *testing.T) {
	syncHermetic(t)
	root := testutil.CopyFixture(t, "minimal")
	for _, verb := range []string{"show", "get", "rm"} {
		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"storage", verb, "nope", "--vault", root})
		})
		if want := "lw: storage: entry \"nope\" not found\n"; code != 1 || stdout != "" || stderr != want {
			t.Errorf("lw storage %s nope: exit %d stdout %q stderr %q; want exit 1 and %q", verb, code, stdout, stderr, want)
		}
	}
}
