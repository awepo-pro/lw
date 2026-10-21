package main

// storage_sync_066_fix2_test.go holds the tests of 066 FIX2 (review2,
// runs/review2/report.md): Add over an untracked file (Y-1, G-1), the proof rule P
// asks for before anything is destroyed (Y-3, G-4), `lw sync status` that does not
// read every stored file (Y-5, G-5), and the guards review2's mutations found
// unpinned (Y-4, G-3). The Y-1/Y-3/Y-5 tests were written BEFORE the fix and are
// RED on 40cddf7; the Y-4 pins pass there by design and go RED under the
// mutation each names.
//
// The harness is S1's and FIX1's (newS066Rig, s066Freeze). What these add is a way
// to make only the remote's OBJECT store refuse (s066ObjectsDown: a wrapper in
// front of the fake ssh, so git still works and the pull that triggers a
// reconcile can happen), and to count the object-store calls a command made.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/storage"
)

// s066ObjectsDown puts a wrapper before ssh that refuses every object-store call
// the way an unreachable host does (ssh's own failure, exit 255) and logs each
// attempt, while git calls pass through. attempts is how many were made; up
// puts the real ssh back. Over the fake-ssh rig only.
func s066ObjectsDown(t *testing.T) (attempts func() int, up func()) {
	t.Helper()
	realSSH, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "attempts.log")
	script := "#!/bin/sh\ncase \"$*\" in *objects*) echo x >> '" + log + "'; echo 'ssh: connect to host box port 22: Connection refused' >&2; exit 255 ;; esac\nexec " + realSSH + " \"$@\"\n"
	wrapper := filepath.Join(dir, "bin")
	if err := os.MkdirAll(wrapper, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wrapper, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := os.Getenv("PATH")
	t.Setenv("PATH", wrapper+string(os.PathListSeparator)+orig)
	return func() int {
		b, _ := os.ReadFile(log)
		return strings.Count(string(b), "x\n")
	}, func() { t.Setenv("PATH", orig) }
}

// s066ObjectCalls counts the ssh calls in the fake ssh's log that touched the
// object store (every script of the store names objects/).
func s066ObjectCalls(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(os.Getenv("FAKE_SSH_LOG"))
	if err != nil {
		return 0
	}
	n := 0
	for _, call := range strings.Split(string(data), "argv:") {
		if strings.Contains(call, "/objects/") {
			n++
		}
	}
	return n
}

// s066ResetSSHLog empties the fake ssh's log.
func s066ResetSSHLog(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(os.Getenv("FAKE_SSH_LOG"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// s066PutObject writes body into a remote store by hand, as another PC's upload would.
func s066PutObject(t *testing.T, storeDir, body string) {
	t.Helper()
	p := s066ObjectPath(storeDir, body)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestAddRefusesOverUntrackedFile is Y-1 (G-1, review2 probe p01). Reconcile keeps
// an edited file ("kept f.bin: changed locally since it was removed elsewhere")
// because it may be the only copy of the user's work. The next
// `lw storage add other --name f.bin` renamed straight over it and exited 0: the
// kept bytes then existed nowhere.
func TestAddRefusesOverUntrackedFile(t *testing.T) {
	const orig, edited = "ORIGINAL\n", "ORIGINAL\nUSER EDIT - precious\n"
	r := newS066Rig(t, false)
	a, b := r.a, r.b
	s066AutoSync(t, a, false)
	s066AutoSync(t, b, false)
	r.store(t, a, "f.bin", orig)
	s066MustSync(t, a)
	s066MustSync(t, b)
	if out, errs, code := b.lw("storage", "rm", "f.bin", "--vault", b.root); code != 0 {
		t.Fatalf("setup: storage rm on b: exit %d stdout %q stderr %q", code, out, errs)
	}
	s066MustSync(t, b)
	if err := os.WriteFile(filepath.Join(a.root, "storage", "f.bin"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, errs, code := s066Sync(a); code != 0 || !strings.Contains(errs, "storage: kept f.bin: changed locally since it was removed elsewhere") {
		t.Fatalf("setup: a's sync: exit %d stderr %q, want the kept line", code, errs)
	}

	other := s066Source(t, "other.bin", "UNRELATED\n")
	stdout, stderr, code := a.lw("storage", "add", other, "--name", "f.bin", "--vault", a.root)
	if code == 0 {
		t.Errorf("storage add over a kept file: exit 0\nstdout %q\nstderr %q", stdout, stderr)
	}
	if want := "storage/f.bin exists and is not in the catalog"; !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr, want)
	}
	if got := a.read("storage/f.bin"); got != edited {
		t.Errorf("a: storage/f.bin = %q after the refused add, want the user's edit %q (the kept bytes exist nowhere else)", got, edited)
	}
	if !s066CatalogEmpty(t, a) {
		t.Error("a: the catalog gained an entry although the add was refused")
	}
}

// TestAddAdoptsIdenticalUntrackedFile is Y-1's other half: the bytes that are
// already at storage/<name> - what 062's name mirror left behind, a file copied in
// by hand - are adopted, not copied over themselves and not refused.
func TestAddAdoptsIdenticalUntrackedFile(t *testing.T) {
	const body = "bytes already in storage/\n"
	a, _, _ := syncPair(t)
	if err := os.MkdirAll(filepath.Join(a.root, "storage"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(a.root, "storage", "f.bin")
	if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := a.lw("storage", "add", s066Source(t, "again.bin", body), "--name", "f.bin", "--vault", a.root)
	if code != 0 {
		t.Fatalf("storage add of the bytes already there: exit %d\nstdout %q\nstderr %q", code, stdout, stderr)
	}
	after, err := os.Stat(target)
	if err != nil || !os.SameFile(before, after) {
		t.Errorf("storage/f.bin was replaced by a copy (%v); identical bytes are adopted", err)
	}
	if c := s066Doctor(t, a); !c.OK || c.Warn || !strings.Contains(c.Detail, "all 1 storage file(s) present and verified") {
		t.Errorf("doctor storage check = %+v, want the adopted file verified", c)
	}
}

// TestDestroyReprovesConfirmedObject is Y-3 (G-4, review2 probe p03). The
// confirmed bit says "the remote held this object once". It cannot say it still
// does: a store restored from a git-only backup, a wiped directory, a PC that
// replaced the disk. Deleting or overwriting on that claim lost the payload for
// good. Before bytes are destroyed the remote is asked again, and given the object
// when it lacks it.
func TestDestroyReprovesConfirmedObject(t *testing.T) {
	t.Run("a removal", func(t *testing.T) {
		const body = "a payload the remote lost\n"
		s066Variants(t, func(t *testing.T, r *s066Rig) {
			a, b := r.a, r.b
			s066AutoSync(t, a, false)
			s066AutoSync(t, b, false)
			r.store(t, a, "f.bin", body)
			s066MustSync(t, a) // uploaded: confirmed on a
			s066MustSync(t, b)
			if !s066ObjectIs(r.storeDir, body) {
				t.Fatal("setup: the object never reached the store")
			}
			if err := os.RemoveAll(r.storeDir); err != nil { // the store is lost; the URL is the same
				t.Fatal(err)
			}
			if out, errs, code := b.lw("storage", "rm", "f.bin", "--vault", b.root); code != 0 {
				t.Fatalf("setup: storage rm on b: exit %d stdout %q stderr %q", code, out, errs)
			}
			s066MustSync(t, b)

			stdout, stderr, code := s066Sync(a)
			if code != 0 {
				t.Errorf("a: lw sync exit %d\nstdout %q\nstderr %q", code, stdout, stderr)
			}
			if !s066ObjectIs(r.storeDir, body) {
				t.Errorf("the payload is in no object store after the removal reached a (G-4: a confirmed copy was believed, not checked); store: %v, still on a's disk: %v", s066ListObjects(r.storeDir), s066DiskHas(a.root, body))
			}
			if want := "storage: removed f.bin (removed on another PC; a copy was kept on the remote store)"; !strings.Contains(stderr, want) {
				t.Errorf("stderr = %q, want it to say %q", stderr, want)
			}
			if s066Exists(filepath.Join(a.root, "storage", "f.bin")) {
				t.Error("a: storage/f.bin is still on disk although its copy is safe and the entry was removed")
			}
		})
	})

	t.Run("a replacement", func(t *testing.T) {
		const a0, b0 = "version A, put here by a\n", "version B, stored on b\n"
		s066Variants(t, func(t *testing.T, r *s066Rig) {
			a, b := r.a, r.b
			s066AutoSync(t, a, false)
			s066AutoSync(t, b, false)
			r.store(t, a, "f", a0)
			s066MustSync(t, a) // A is uploaded: confirmed on a
			s066MustSync(t, b)
			r.store(t, b, "f", b0, "--force")
			s066MustSync(t, b)
			if err := os.RemoveAll(r.storeDir); err != nil {
				t.Fatal(err)
			}
			s066PutObject(t, r.storeDir, b0) // B is back (b's disk, a restore); A is not

			stdout, stderr, code := s066Sync(a)
			if code != 0 {
				t.Errorf("a: lw sync exit %d\nstdout %q\nstderr %q", code, stdout, stderr)
			}
			if got := a.read("storage/f"); got != b0 {
				t.Errorf("a: storage/f = %q, want the replacement %q", got, b0)
			}
			if !s066ObjectIs(r.storeDir, a0) {
				t.Errorf("A is in no object store (%v) and %v on a's disk: the replacement overwrote it on a confirmation the remote no longer honoured (G-4)", s066ListObjects(r.storeDir), map[bool]string{true: "is still", false: "is no longer"}[s066DiskHas(a.root, a0)])
			}
		})
	})
}

// TestDestroyOfflineKeepsFile is Y-3 for a remote that cannot be asked. A file lw is
// about to delete stays; the user is told once; the ledger row stays so that the
// next sync that can reach the remote finishes the job; the verb's exit code is
// the git phase's. Only the object store is down - git is not - because that is
// the state a pull-triggered reconcile can be in.
func TestDestroyOfflineKeepsFile(t *testing.T) {
	const body = "a payload this PC believes the remote holds\n"
	r := newS066Rig(t, true)
	a, b := r.a, r.b
	s066AutoSync(t, a, false)
	s066AutoSync(t, b, false)
	r.store(t, a, "f.bin", body)
	s066MustSync(t, a)
	s066MustSync(t, b)
	if out, errs, code := b.lw("storage", "rm", "f.bin", "--vault", b.root); code != 0 {
		t.Fatalf("setup: storage rm on b: exit %d stdout %q stderr %q", code, out, errs)
	}
	s066MustSync(t, b)
	attempts, up := s066ObjectsDown(t)

	const kept = "storage: kept f.bin: remote unreachable, will remove on a later sync"
	stdout, stderr, code := s066Sync(a)
	if code != 0 {
		t.Errorf("a: lw sync exit %d, want 0 (the git phase worked; an unproven delete is not a failure)\nstdout %q\nstderr %q", code, stdout, stderr)
	}
	if got := a.read("storage/f.bin"); got != body {
		t.Fatalf("a: storage/f.bin = %q, want it kept while its copy cannot be proven (G-4)", got)
	}
	if n := s066Lines(stderr, kept); n != 1 {
		t.Errorf("stderr has the kept line %d times, want once: %q", n, stderr)
	}
	if attempts() == 0 {
		t.Error("the reconcile never asked the remote")
	}
	ledger, _ := os.ReadFile(filepath.Join(a.root, ".llmwiki", "tmp", "storage-local.json"))
	if !strings.Contains(string(ledger), s066SHA(body)) {
		t.Errorf("ledger = %s, want the row kept so a later sync retries", ledger)
	}

	_, stderr, code = s066Sync(a)
	if code != 0 || strings.Contains(stderr, "kept f.bin") {
		t.Errorf("second sync: exit %d stderr %q; want exit 0 and the line not repeated", code, stderr)
	}
	if got := a.read("storage/f.bin"); got != body {
		t.Fatalf("a: storage/f.bin = %q after the second sync", got)
	}

	up()
	stdout, stderr, code = s066Sync(a)
	if code != 0 {
		t.Errorf("sync after the remote came back: exit %d\nstdout %q\nstderr %q", code, stdout, stderr)
	}
	if want := "storage: removed f.bin (removed on another PC)\n"; !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
	if s066Exists(filepath.Join(a.root, "storage", "f.bin")) {
		t.Error("a: storage/f.bin survived the retry")
	}
}

// TestSteadySyncNoObjectCalls is Y-3's cost promise: the remote is asked again only
// when something is about to be destroyed. A sync with nothing to destroy makes no
// object-store call (F-3, X-6); one removal costs exactly one question.
func TestSteadySyncNoObjectCalls(t *testing.T) {
	r := newS066Rig(t, true)
	a, b := r.a, r.b
	r.store(t, a, "one.bin", "the first payload\n")
	r.store(t, a, "two.bin", "the second payload\n")
	s066MustSync(t, b)
	s066MustSync(t, a)
	s066MustSync(t, b)

	s066ResetSSHLog(t)
	s066MustSync(t, a)
	s066MustSync(t, b)
	noteAt(t, 0)
	if _, errs, code := a.lw("note", "-m", "an auto-synced verb", "--vault", a.root); code != 0 {
		t.Fatalf("lw note: exit %d stderr %q", code, errs)
	}
	s066MustSync(t, b)
	if n := s066ObjectCalls(t); n != 0 {
		t.Errorf("syncs with nothing to destroy made %d object-store call(s), want 0", n)
	}

	if out, errs, code := b.lw("storage", "rm", "one.bin", "--vault", b.root); code != 0 {
		t.Fatalf("storage rm on b: exit %d stdout %q stderr %q", code, out, errs)
	}
	s066MustSync(t, b)
	s066ResetSSHLog(t)
	s066MustSync(t, a)
	if n := s066ObjectCalls(t); n != 1 {
		t.Errorf("a removal made %d object-store call(s), want exactly 1 (one HaveObjects before the delete)", n)
	}
	if s066Exists(filepath.Join(a.root, "storage", "one.bin")) {
		t.Error("a: storage/one.bin survived the removal")
	}
}

// TestReconcileScopesToCurrentRemote is Y-4 (C3, review2 probe p10). A remote is
// known by the set of [sync] remotes. a's config now names a second path to the
// same git repository whose object store is empty: what this PC confirmed on the
// first store says nothing about the second. After Y-3 a reconcile re-proves in
// any case, so what the scope still decides is what the user is TOLD when the
// proof fails: "no copy on the remote yet" (the second store lacks it) rather than
// "remote unreachable, will remove later" (the first store had it).
func TestReconcileScopesToCurrentRemote(t *testing.T) {
	const body = "a payload only the first store holds\n"
	setup := func(t *testing.T) (*s066Rig, string) {
		t.Helper()
		r := newS066Rig(t, false)
		a, b := r.a, r.b
		s066AutoSync(t, a, false)
		s066AutoSync(t, b, false)
		r.store(t, a, "h", body)
		s066MustSync(t, a) // uploaded to the first store: confirmed there
		s066MustSync(t, b)
		remote2 := filepath.Join(syncTemp(t), "lw-vault2")
		if err := os.Symlink(r.gitDir, remote2); err != nil {
			t.Fatal(err)
		}
		a.setConfig(strings.ReplaceAll(a.config(), r.spec, remote2))
		if !strings.Contains(a.config(), remote2) {
			t.Fatalf("setup: a's config does not name %s:\n%s", remote2, a.config())
		}
		if out, errs, code := b.lw("storage", "rm", "h", "--vault", b.root); code != 0 {
			t.Fatalf("setup: storage rm on b: exit %d stdout %q stderr %q", code, out, errs)
		}
		s066MustSync(t, b)
		s066AutoSync(t, a, true) // the next verb on a pulls at its start
		return r, remote2 + ".storage"
	}

	// The start-of-verb pull of a `lw note` reconciles before anything is uploaded,
	// which is where p10 saw it; an explicit lw sync uploads before its pull and
	// would save the copy there.
	note := func(t *testing.T, a *syncPC, minute int) string {
		t.Helper()
		noteAt(t, minute)
		_, stderr, code := a.lw("note", "-m", "x", "--vault", a.root)
		if code != 0 {
			t.Errorf("a: lw note exit %d, want 0; stderr %q", code, stderr)
		}
		return stderr
	}

	t.Run("the new store takes the copy", func(t *testing.T) {
		r, store2 := setup(t)
		stderr := note(t, r.a, 0)
		if !s066ObjectIs(store2, body) {
			t.Errorf("the payload is not in the new store after the removal reached a (p10: the old store's confirmation was believed); stderr %q", stderr)
		}
		if want := "storage: removed h (removed on another PC; a copy was kept on the remote store)"; !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want it to contain %q", stderr, want)
		}
	})

	t.Run("the new store cannot take it", func(t *testing.T) {
		r, store2 := setup(t)
		a := r.a
		s066Freeze(t, store2)
		stderr := note(t, a, 0)
		if got := a.read("storage/h"); got != body {
			t.Fatalf("a: storage/h = %q, want it kept: neither store can show a copy now", got)
		}
		if want := "storage: kept h: no copy on the remote yet ("; !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want a line starting %q (the new store never held it)", stderr, want)
		}
		if strings.Contains(stderr, "will remove on a later sync") {
			t.Errorf("stderr = %q: it says the file will be removed later on the strength of a confirmation that belongs to the old store", stderr)
		}
		s066Thaw(t, store2)
		stderr = note(t, a, 1)
		if !s066ObjectIs(store2, body) || s066Exists(filepath.Join(a.root, "storage", "h")) {
			t.Errorf("after the new store became writable: object saved %v, file still there %v; stderr %q", s066ObjectIs(store2, body), s066Exists(filepath.Join(a.root, "storage", "h")), stderr)
		}
	})
}

// TestTakeRemoteExitsOneAfterFailedUpload is Y-4 (C14). `lw sync --take-remote`
// uploads this PC's payloads before it discards the commits that named them. When
// that upload fails the remote is taken all the same - the divergence is resolved -
// but the command exits 1 and says the payloads did not go: the backup branch
// names bytes that exist only here.
func TestTakeRemoteExitsOneAfterFailedUpload(t *testing.T) {
	const a0, b0 = "bytes of PC a, the only copy\n", "bytes of PC b\n"
	// diverged is FIX1's F-1 (a) setup: a's own sync is refused, and with frozen the
	// payload has not been uploaded by it either (a confirmed one would need no upload
	// at the take, and there would be nothing to fail).
	diverged := func(t *testing.T, frozen bool) *s066Rig {
		t.Helper()
		r := s066Diverge(t, a0, b0)
		if frozen {
			s066Freeze(t, r.storeDir)
		}
		if _, errs, code := s066Sync(r.a); code != 1 || !strings.Contains(errs, "diverged") {
			t.Fatalf("setup: a's sync: exit %d stderr %q, want a refusal as diverged", code, errs)
		}
		return r
	}

	t.Run("the upload fails", func(t *testing.T) {
		r := diverged(t, true)
		out, errs, code := r.a.lw("sync", "--take-remote", "--vault", r.a.root)
		if !strings.Contains(out, "took ") {
			t.Fatalf("--take-remote did not take the remote\nstdout %q\nstderr %q", out, errs)
		}
		if code != 1 {
			t.Errorf("exit %d, want 1: this PC's payloads were not uploaded\nstderr %q", code, errs)
		}
		if want := "(wiki synced; run lw sync again)"; !strings.Contains(errs, want) {
			t.Errorf("stderr = %q, want it to contain %q", errs, want)
		}
	})

	t.Run("the upload works", func(t *testing.T) {
		r := diverged(t, false)
		out, errs, code := r.a.lw("sync", "--take-remote", "--vault", r.a.root)
		if !strings.Contains(out, "took ") || code != 0 {
			t.Errorf("--take-remote: exit %d\nstdout %q\nstderr %q; want it taken and exit 0", code, out, errs)
		}
	})
}

// TestFetchStopsAtFirstTransientFailure is Y-4 (C16). When the remote cannot be
// reached for one payload it cannot for the next: a fetch of N pending files makes
// one attempt and reports it, not N attempts that each wait out the timeout.
func TestFetchStopsAtFirstTransientFailure(t *testing.T) {
	r := newS066Rig(t, true)
	a, b := r.a, r.b
	for _, name := range []string{"one.bin", "two.bin", "three.bin"} {
		r.store(t, a, name, "payload of "+name+"\n")
	}
	attempts, _ := s066ObjectsDown(t)

	stdout, stderr, code := s066Sync(b)
	if code != 1 || !strings.Contains(stderr, "(wiki synced; run lw sync again)") {
		t.Errorf("b: lw sync exit %d\nstdout %q\nstderr %q; want exit 1 with the storage line", code, stdout, stderr)
	}
	if n := attempts(); n != 1 {
		t.Errorf("the fetch made %d attempt(s) at the object store, want 1: the first failure was the remote's, so the rest would fail the same way", n)
	}
	for _, name := range []string{"one.bin", "two.bin", "three.bin"} {
		if s066Exists(filepath.Join(b.root, "storage", name)) {
			t.Errorf("b: storage/%s exists although nothing could be fetched", name)
		}
	}
}

// TestSyncStatusDoesNotHash is Y-5 (G-5). `lw sync status` is a read-only look; it
// hashed every stored file to count them (0.39 s for 600 MB). It now trusts what
// the sync paths trust - a file where the ledger put it, still the catalog's size -
// and reads only files it cannot vouch for.
func TestSyncStatusDoesNotHash(t *testing.T) {
	r := newS066Rig(t, false)
	a, b := r.a, r.b
	r.store(t, a, "one.bin", "the first payload\n")
	r.store(t, a, "two.bin", "the second payload\n")
	s066MustSync(t, b)
	s066MustSync(t, a)

	for _, pc := range []*syncPC{a, b} {
		h0 := storage.HashCount()
		out, errs, code := pc.lw("sync", "status", "--vault", pc.root)
		if d := storage.HashCount() - h0; d != 0 {
			t.Errorf("%s: sync status hashed %d stored file(s), want 0 (G-5)", pc.name, d)
		}
		if want := "storage: 2 verified, 0 pending, 0 corrupt\n"; code != 0 || !strings.Contains(out, want) {
			t.Errorf("%s: sync status: exit %d stdout %q stderr %q; want it to contain %q", pc.name, code, out, errs, want)
		}
	}
}
