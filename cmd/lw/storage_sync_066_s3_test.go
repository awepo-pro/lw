package main

// storage_sync_066_s3_test.go holds the tests 066 S3 added to S1's frozen set
// (storage_sync_066_test.go, whose helpers it reuses): the two the workflow's
// "Corrections after S1" ask for - TestSyncRetriesFailedUpload (C-066-1) and
// TestStorageSSHParity (C-066-5) - and TestStorageUploadPrecedesPush, which pins
// the ORDER of the upload and the push. No other test can: with the payload
// store working, "upload then push" and "push then upload" leave the same files
// in the same places. The only observer that can tell is the remote at the
// moment git arrives, so the remote is given a pre-receive hook that refuses a
// catalog naming an object the store lacks - which is what content before
// pointer (plan M5, B6) means to the PC that fetches from it.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestSyncRetriesFailedUpload is C-066-1. R5 makes a failed upload leave git
// pushed and exit 1 with "run lw sync again" - but the second run finds nothing
// ahead and, if the upload hung off the push, nothing to do: the payload would
// stay local forever while the message promised a retry. `lw sync` therefore
// uploads on every run, push or not.
func TestSyncRetriesFailedUpload(t *testing.T) {
	s066Variants(t, func(t *testing.T, r *s066Rig) {
		a, b := r.a, r.b
		const body = "bytes whose first upload fails\n"
		s066AutoSync(t, a, false) // the explicit lw sync below must be the first push
		r.store(t, a, "h", body)
		s066Unwritable(t, r.storeDir)

		stdout, stderr, code := s066Sync(a)
		head := a.git("rev-parse", "HEAD")
		if got := remoteHead(t, r.gitDir); got != head {
			t.Errorf("first sync: remote main = %s, want a's HEAD %s (the failed upload must not stop the push)", got, head)
		}
		if code != 1 || !strings.Contains(stderr, "(wiki synced; run lw sync again)") {
			t.Errorf("first sync: exit %d stderr %q, want exit 1 and the storage line\nstdout %q", code, stderr, stdout)
		}
		obj := s066ObjectPath(r.storeDir, body)
		if s066Exists(obj) {
			t.Fatalf("setup: the object %s exists although the store was unwritable", obj)
		}

		if err := os.Chmod(r.storeDir, 0o755); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, code = s066Sync(a)
		if code != 0 || stderr != "" {
			t.Errorf("second sync: exit %d stderr %q, want exit 0 and nothing on stderr\nstdout %q", code, stderr, stdout)
		}
		if !strings.Contains(stdout, "up to date with") {
			t.Errorf("second sync: stdout %q; the premise is that there was nothing to push (C-066-1)", stdout)
		}
		if got, err := os.ReadFile(obj); err != nil || string(got) != body {
			t.Errorf("second sync did not upload the object %s: %q, err %v (C-066-1: lw sync uploads on every run, not only when it pushes)", obj, got, err)
		}

		if out, errs, code := s066Sync(b); code != 0 {
			t.Errorf("b: lw sync exit %d\nstdout %q\nstderr %q", code, out, errs)
		}
		if got := b.read("storage/h"); got != body {
			t.Errorf("b: storage/h = %q after the retried upload, want %q", got, body)
		}
	})
}

// TestStorageSSHParity is C-066-5, R8: the behaviours R5 and R6 add are pinned
// over the fake-ssh remote too, because a host:path remote is how the user
// reaches theirs and the object store's ssh path is a different code path from
// the local one (three sh scripts run as `sh -c '...' sh <root> <args>` through
// the runner's ssh invocation).
func TestStorageSSHParity(t *testing.T) {
	t.Run("auto-push uploads before git", func(t *testing.T) {
		r := newS066Rig(t, true)
		a, b := r.a, r.b
		const body = "payload stored, then an unrelated verb, over ssh\n"
		s066AutoSync(t, a, false)
		r.store(t, a, "k", body)
		s066AutoSync(t, a, true)
		noteAt(t, 0)

		stdout, stderr, code := a.lw("note", "-m", "x", "--vault", a.root)
		if code != 0 {
			t.Errorf("lw note on a: exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		obj := s066ObjectPath(r.storeDir, body)
		if got, err := os.ReadFile(obj); err != nil || string(got) != body {
			t.Errorf("no object for k at %s after the auto-push of lw note (R6a over ssh): %q, err %v", obj, got, err)
		}
		if head := a.git("rev-parse", "HEAD"); remoteHead(t, r.gitDir) != head {
			t.Errorf("remote main = %s, want a's HEAD %s", remoteHead(t, r.gitDir), head)
		}
		if out, errs, code := s066Sync(b); code != 0 {
			t.Errorf("b: lw sync exit %d\nstdout %q\nstderr %q", code, out, errs)
		}
		if got := b.read("storage/k"); got != body {
			t.Errorf("b: storage/k = %q after lw sync over ssh, want %q", got, body)
		}
	})

	t.Run("pending is fetched on demand", func(t *testing.T) {
		r := newS066Rig(t, true)
		a, b := r.a, r.b
		const body = "payload fetched when asked for, over ssh\n"
		r.store(t, a, "k", body) // auto-sync is on: the add uploads and pushes
		noteAt(t, 0)
		if out, errs, code := b.lw("note", "-m", "pulls the catalog", "--vault", b.root); code != 0 {
			t.Fatalf("lw note on b: exit %d stdout %q stderr %q", code, out, errs)
		}
		if out, _, _ := b.lw("storage", "list", "--vault", b.root); !strings.Contains(out, "k") {
			t.Fatalf("setup: b's catalog does not list k: %q", out)
		}
		if s066Exists(filepath.Join(b.root, "storage", "k")) {
			t.Fatal("setup: b already has storage/k; the scenario needs it pending")
		}

		dest := filepath.Join(t.TempDir(), "out")
		stdout, stderr, code := b.lw("storage", "get", "k", "-o", dest, "--vault", b.root)
		if code != 0 {
			t.Errorf("storage get k on b over ssh: exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		if got, err := os.ReadFile(dest); err != nil || string(got) != body {
			t.Errorf("storage get k -o: wrote %q, err %v; want %q byte-exact", got, err, body)
		}
		if got := b.read("storage/k"); got != body {
			t.Errorf("b: storage/k = %q, want the fetched file kept on disk, verified", got)
		}
		if c := s066Doctor(t, b); !c.OK || c.Warn || c.Detail != "all 1 storage file(s) present and verified" {
			t.Errorf("b: doctor storage check = %+v after the on-demand fetch, want OK, no warn", c)
		}
	})

	t.Run("a storage failure does not block the push", func(t *testing.T) {
		r := newS066Rig(t, true)
		a := r.a
		s066Unwritable(t, r.storeDir)
		s066AutoSync(t, a, false)
		r.store(t, a, "h", "bytes that cannot be uploaded over ssh\n")
		before := remoteHead(t, r.gitDir)

		stdout, stderr, code := s066Sync(a)

		head := a.git("rev-parse", "HEAD")
		if got := remoteHead(t, r.gitDir); got != head {
			t.Errorf("remote main = %s, want a's HEAD %s (R5 over ssh; it was %s)", got, head, before)
		}
		if code != 1 || !strings.Contains(stderr, "(wiki synced; run lw sync again)") {
			t.Errorf("exit %d stderr %q, want exit 1 and the storage line\nstdout %q", code, stderr, stdout)
		}
	})
}

// s066RefuseUnstoredPointer gives the remote a pre-receive hook that refuses any
// push whose .llmwiki/storage.json names a sha256 the remote's object store does
// not hold. It is the remote's own view of "content before pointer".
func s066RefuseUnstoredPointer(t *testing.T, gitDir, storeDir string) {
	t.Helper()
	hooks := filepath.Join(gitDir, "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"store='" + storeDir + "'\n" +
		"zero=0000000000000000000000000000000000000000\n" +
		"while read old new ref; do\n" +
		"  [ \"$new\" = \"$zero\" ] && continue\n" +
		"  git show \"$new:.llmwiki/storage.json\" 2>/dev/null | grep -o '\"sha256\": *\"[0-9a-f]*\"' | grep -o '[0-9a-f]\\{64\\}' | while read sha; do\n" +
		"    a=$(printf %s \"$sha\" | cut -c1-2); b=$(printf %s \"$sha\" | cut -c3-)\n" +
		"    if [ ! -f \"$store/objects/$a/$b\" ]; then echo \"pre-receive: object $sha is not in the store yet\" >&2; exit 1; fi\n" +
		"  done || exit 1\n" +
		"done\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(hooks, "pre-receive"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestStorageUploadPrecedesPush is B6 / R6a seen from the remote: every push of
// git is preceded by the upload of the payloads its catalog names (plan M5). The
// remote refuses a catalog whose object it lacks (s066RefuseUnstoredPointer), so
// an upload placed after the push - the shape that looks right and leaves a
// window in which another PC pulls a pointer to nothing - fails the push itself.
// Both pushers are covered: the explicit lw sync, and the auto-push a
// `lw storage add` requests (which is also the TUI pusher's path).
func TestStorageUploadPrecedesPush(t *testing.T) {
	s066Variants(t, func(t *testing.T, r *s066Rig) {
		a := r.a
		s066RefuseUnstoredPointer(t, r.gitDir, r.storeDir)

		s066AutoSync(t, a, false)
		const explicit = "stored, then pushed by an explicit lw sync\n"
		r.store(t, a, "e", explicit)
		stdout, stderr, code := s066Sync(a)
		head := a.git("rev-parse", "HEAD")
		if got := remoteHead(t, r.gitDir); got != head || code != 0 {
			t.Errorf("explicit lw sync: exit %d, remote main %s, want exit 0 and a's HEAD %s (the push was refused: the pointer arrived before its content)\nstdout %q\nstderr %q", code, got, head, stdout, stderr)
		}

		s066AutoSync(t, a, true)
		const auto = "stored, then pushed by the auto-push of the add itself\n"
		r.store(t, a, "u", auto) // the add's own auto-push is the push under test
		head = a.git("rev-parse", "HEAD")
		if got := remoteHead(t, r.gitDir); got != head {
			t.Errorf("auto-push after storage add: remote main = %s, want a's HEAD %s (the push was refused: the pointer arrived before its content)", got, head)
		}
		for _, body := range []string{explicit, auto} {
			if obj := s066ObjectPath(r.storeDir, body); !s066Exists(obj) {
				t.Errorf("no object at %s", obj)
			}
		}
	})
}

// TestSyncInitUploadsStorageFirst is the same promise for the first push of all,
// `lw sync init`: whatever the vault stored before it was put under sync reaches
// the object store before the catalog reaches the remote.
func TestSyncInitUploadsStorageFirst(t *testing.T) {
	syncHermetic(t)
	remote := filepath.Join(syncTemp(t), "lw-vault")
	gitIn(t, filepath.Dir(remote), "init", "--bare", "--quiet", remote)
	s066RefuseUnstoredPointer(t, remote, remote+".storage")
	a := newSyncPC(t, "a")
	a.useFixture()
	const body = "stored before the vault was put under sync\n"
	if out, errs, code := a.lw("storage", "add", s066Source(t, "pre.bin", body), "--vault", a.root); code != 0 {
		t.Fatalf("storage add before sync init: exit %d stdout %q stderr %q", code, out, errs)
	}

	stdout, stderr, code := a.lw("sync", "init", remote, "--vault", a.root)

	if code != 0 {
		t.Errorf("sync init: exit %d (the push was refused if the pointer arrived before its content)\nstdout %q\nstderr %q", code, stdout, stderr)
	}
	if obj := s066ObjectPath(remote+".storage", body); !s066Exists(obj) {
		t.Errorf("no object at %s after sync init", obj)
	}
	c := newSyncPC(t, "c")
	if out, errs, code := c.lw("sync", "clone", remote, c.root); code != 0 {
		t.Fatalf("sync clone: exit %d stdout %q stderr %q", code, out, errs)
	}
	if got := c.read("storage/pre.bin"); got != body {
		t.Errorf("clone: storage/pre.bin = %q, want %q", got, body)
	}
}

// TestAutoSyncReconcilesStorage pins the two auto-sync call sites of
// storageReconcile (066 D4): the pull at the start of a writing verb, and the
// pull inside an auto-push. TestStorageRmPropagates drives only the explicit
// `lw sync`; without these a removal that reaches a PC through `lw note` or
// `lw commit` would sit in storage/ until somebody ran `lw sync`.
func TestAutoSyncReconcilesStorage(t *testing.T) {
	const removedLine = "storage: removed f (removed on another PC)"
	setup := func(t *testing.T) (a, b *syncPC) {
		t.Helper()
		r := newS066Rig(t, false)
		a, b = r.a, r.b
		r.store(t, a, "f", "stored, then removed on the other PC\n") // auto-sync is on: uploaded and pushed
		s066MustSync(t, b)
		if b.read("storage/f") == "" {
			t.Fatal("setup: b did not receive f")
		}
		if out, errs, code := a.lw("storage", "rm", "f", "--vault", a.root); code != 0 { // pushes the removal
			t.Fatalf("storage rm on a: exit %d stdout %q stderr %q", code, out, errs)
		}
		return a, b
	}

	// The pull alone, not a whole verb: lw note would push at its end, and that
	// push's own pull would reconcile if the start-of-verb pull did not.
	t.Run("the pull at the start of a verb", func(t *testing.T) {
		_, b := setup(t)
		b.act()
		auto := loadAutoSync(b.root)
		_, stderr, _ := captureRun(t, func() int {
			auto.pull()
			return 0
		})
		if !strings.Contains(stderr, removedLine) {
			t.Errorf("stderr = %q, want %q", stderr, removedLine)
		}
		if s066Exists(filepath.Join(b.root, "storage", "f")) {
			t.Error("b: storage/f is still on disk after the start-of-verb pull (B1)")
		}
	})

	t.Run("the pull inside an auto-push", func(t *testing.T) {
		_, b := setup(t)
		b.act()
		auto := loadAutoSync(b.root)
		var res syncPushResult
		_, stderr, _ := captureRun(t, func() int {
			res = auto.pushOnce(context.Background())
			return 0
		})
		if res.Err != nil {
			t.Fatalf("pushOnce = %+v", res)
		}
		if !strings.Contains(stderr, removedLine) {
			t.Errorf("stderr = %q, want %q", stderr, removedLine)
		}
		if s066Exists(filepath.Join(b.root, "storage", "f")) {
			t.Error("b: storage/f is still on disk after the pull inside the auto-push (B1)")
		}
	})
}

// TestAutoSyncHoldsStorageLinesForTheTUI pins how the auto-sync reports storage
// in the TUI: the pusher runs while the screen is up and must not write to it,
// so a line is held until the report that runs after the terminal is restored;
// and "wiki synced" is only claimed when the push worked.
func TestAutoSyncHoldsStorageLinesForTheTUI(t *testing.T) {
	auto := &autoSync{pusher: newSyncPusher(func(context.Context) syncPushResult { return syncPushResult{} }, time.Hour)}
	_, stderr, _ := captureRun(t, func() int {
		auto.storageSay("storage: removed f (removed on another PC)")
		return 0
	})
	if stderr != "" {
		t.Errorf("the TUI's pusher wrote %q to stderr while the screen was up", stderr)
	}

	var buf bytes.Buffer
	auto.report(&buf, syncPushResult{StorageErr: errors.New("storage: upload of h failed: boom")})
	want := "storage: removed f (removed on another PC)\n" +
		"storage: upload of h failed: boom (wiki synced; run lw sync again)\n"
	if buf.String() != want {
		t.Errorf("report = %q, want %q", buf.String(), want)
	}

	buf.Reset()
	auto.report(&buf, syncPushResult{Err: errors.New("no route"), StorageErr: errors.New("storage: upload of h failed: boom")})
	if strings.Contains(buf.String(), "wiki synced") {
		t.Errorf("report = %q: a push that failed must not claim the wiki synced", buf.String())
	}
}

// TestRevertIgnoresStorageInThePreFixSnapshotItself is D5's other side. S1's
// TestStorageInvisibleToHistory reverts the commit AFTER a v2.49.0 snapshot,
// where the stored file is on the previous side of the diff (a REMOVED path).
// Reverting the commit whose own snapshot lists it puts it on the current side
// (an ADDED path - "skipped: storage/x.md"), and that side needs the ignore too.
func TestRevertIgnoresStorageInThePreFixSnapshotItself(t *testing.T) {
	syncHermetic(t)
	root, stored := s066HistoryVault(t)
	treePath := filepath.Join(root, ".llmwiki", "snapshots", "000002.tree")
	b, err := os.ReadFile(treePath)
	if err != nil {
		t.Fatal(err)
	}
	// Any storage/ line is dropped and the one v2.49.0 would have written is
	// added, so the manifest is the same whether or not the walk skips storage/;
	// lines sort by path and "storage/" follows "raw/" and "index.md".
	var lines []string
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if len(line) > 66 && !strings.HasPrefix(line[66:], "storage/") {
			lines = append(lines, line)
		}
	}
	lines = append(lines, s066SHA(stored)+"  storage/x.md")
	sort.SliceStable(lines, func(i, j int) bool { return lines[i][66:] < lines[j][66:] })
	if err := os.WriteFile(treePath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"revert", "--vault", root, "000002"})
	})
	if code != 0 {
		t.Fatalf("revert 000002: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if strings.Contains(stdout+stderr, "storage/") {
		t.Errorf("revert of a commit whose snapshot lists a stored file mentions it (D5: both sides of the diff ignore storage/):\n%s%s", stdout, stderr)
	}
	if got, err := os.ReadFile(filepath.Join(root, "storage", "x.md")); err != nil || string(got) != stored {
		t.Errorf("storage/x.md = %q, err %v; want it untouched", got, err)
	}
}

// TestStorageFetchRefusesAWrongObject pins what the fetch does with an object
// that is not the catalog's (R9): a remote object of the right size but other
// bytes, and one cut short. Both leave nothing on this PC - no storage/<name>,
// no temp file - and are named as a failure, never served; `lw sync` still
// finishes its git phase and says so (exit 1, the storage line), and `lw
// storage get` exits 1 with the sha it expected. The wrong object is not
// repaired: objects are write-once and the remote is not this PC's to edit.
func TestStorageFetchRefusesAWrongObject(t *testing.T) {
	const body = "the bytes the catalog names\n"
	cases := []struct {
		name  string
		wrong string
		want  string // in the error
	}{
		{"same size, other bytes", "the BYTES the catalog names\n", "sha256 mismatch"},
		{"cut short", body[:8], "has 8 bytes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newS066Rig(t, false)
			a, b := r.a, r.b
			r.store(t, a, "k", body) // auto-sync is on: uploaded and pushed
			obj := s066ObjectPath(r.storeDir, body)
			if err := os.WriteFile(obj, []byte(tc.wrong), 0o644); err != nil {
				t.Fatal(err)
			}

			stdout, stderr, code := s066Sync(b)
			if code != 1 || !strings.Contains(stderr, tc.want) || !strings.Contains(stderr, "(wiki synced; run lw sync again)") {
				t.Errorf("lw sync on b: exit %d stderr %q, want exit 1, %q and the storage line\nstdout %q", code, stderr, tc.want, stdout)
			}
			if b.git("rev-parse", "HEAD") != remoteHead(t, r.gitDir) {
				t.Error("b did not take the remote's commits: a bad object must not stop the git phase")
			}

			gout, gerr, gcode := b.lw("storage", "get", "k", "--vault", b.root)
			if gcode != 1 || gout != "" || !strings.Contains(gerr, tc.want) {
				t.Errorf("storage get k on b: exit %d stdout %q stderr %q, want exit 1, nothing on stdout and %q", gcode, gout, gerr, tc.want)
			}

			if s066Exists(filepath.Join(b.root, "storage", "k")) {
				t.Error("b: storage/k exists although the object was wrong")
			}
			if left, _ := filepath.Glob(filepath.Join(b.root, "storage", ".*")); len(left) > 0 {
				t.Errorf("b left temp files behind: %v", left)
			}
			// 066 FIX1 (X-4) amended the wording: the entry is still pending and still
			// only a warning, but a fetch that failed its check marks the remote's
			// object as damaged, and doctor says so instead of "not arrived yet".
			if c := s066Doctor(t, b); !c.OK || !c.Warn || c.Detail != "0 of 1 verified; 1 pending (remote object is damaged)" {
				t.Errorf("b: doctor storage check = %+v, want it still pending, with the remote object called damaged", c)
			}
		})
	}
}
