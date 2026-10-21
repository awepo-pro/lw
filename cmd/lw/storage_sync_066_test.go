package main

// storage_sync_066_test.go holds the regression tests for the six defects 062
// (`lw storage`, v2.49.0) shipped with, B1-B6 of plans/066-storage-sync-integrity.md.
// They were written BEFORE the fix and are RED on v2.50.0 (c74b86d); each
// carries the bug id and the requirement id (R1-R7) it pins.
//
// The common cause is that 062 mirrors storage/ between PCs by file NAME and
// never reads the git catalog (.llmwiki/storage.json), so a delete comes back
// (B1), a same-size replace is lost (B2), a catalog can reach a PC without its
// bytes (B6), and a storage error stops the git push (B5). The other two are the
// 047 and 059 mistakes made again: storage/ is not registered as outside the
// vault's history (B3) and the storage verbs trust a mistyped --vault (B4).
//
// Where a test is about what the USER sees (a file is gone, a note is pushed, a
// doctor line) it asserts only that, so it stays valid whatever the transport
// does inside. Only the object-store layout - <remote>.storage/objects/<sha[0:2]>/<sha[2:]>,
// 066 D1 - is part of the contract and is asserted where a test needs to know
// whether a payload reached the remote at all (R6a).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/testutil"
)

// s066Rig is the two PCs and the remote one storage-sync test runs against. The
// remote is either a local bare repository or a host:path reached through the
// fake ssh of the 042 harness (installFakeSSH), which is how the user's real
// remote is reached; B1 and B2 were confirmed over ssh only by reading the
// code, so the ssh subtests are the first thing that ever runs that path.
type s066Rig struct {
	a, b     *syncPC
	spec     string // the remote as lw is told it: a local path, or box:lw-vault
	gitDir   string // the bare repository, on this machine for both kinds
	storeDir string // <remote>.storage: where the payloads live (062 and 066 D1)
}

// newS066Rig is syncPair for a local remote, or, with ssh, the same pair over a
// fake-ssh remote. The fake ssh runs the remote command in $HOME, so the remote
// path "lw-vault" is $HOME/lw-vault and its storage root $HOME/lw-vault.storage.
func newS066Rig(t *testing.T, ssh bool) *s066Rig {
	t.Helper()
	if !ssh {
		a, b, remote := syncPair(t)
		return &s066Rig{a: a, b: b, spec: remote, gitDir: remote, storeDir: remote + ".storage"}
	}
	home := syncHermetic(t)
	installFakeSSH(t)
	spec := "box:lw-vault"
	a, b := newSyncPC(t, "a"), newSyncPC(t, "b")
	a.useFixture()
	if out, errs, code := a.lw("sync", "init", spec, "--vault", a.root); code != 0 {
		t.Fatalf("sync init over the fake ssh: exit %d\nstdout %q\nstderr %q", code, out, errs)
	}
	if out, errs, code := b.lw("sync", "clone", spec, b.root); code != 0 {
		t.Fatalf("sync clone over the fake ssh: exit %d\nstdout %q\nstderr %q", code, out, errs)
	}
	gitDir := filepath.Join(home, "lw-vault")
	return &s066Rig{a: a, b: b, spec: spec, gitDir: gitDir, storeDir: gitDir + ".storage"}
}

// s066Variants runs body once over a local remote and once, as the subtest
// "ssh", over the fake-ssh remote. body must report with Errorf where it can: a
// Fatalf in the local run would stop the test before the ssh subtest ever ran.
func s066Variants(t *testing.T, body func(t *testing.T, r *s066Rig)) {
	t.Helper()
	body(t, newS066Rig(t, false))
	t.Run("ssh", func(t *testing.T) { body(t, newS066Rig(t, true)) })
}

// s066SHA is the hex sha256 of body, as the catalog records it.
func s066SHA(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// s066ObjectPath is where a payload lives in the remote's object store (D1).
func s066ObjectPath(storeDir, body string) string {
	sha := s066SHA(body)
	return filepath.Join(storeDir, "objects", sha[:2], sha[2:])
}

// s066Source writes body to a fresh file called base and returns its path: the
// <file> argument of lw storage add.
func s066Source(t *testing.T, base, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), base)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// store runs `lw storage add` as pc and fails the test unless it stored.
func (r *s066Rig) store(t *testing.T, pc *syncPC, name, body string, extra ...string) {
	t.Helper()
	args := append([]string{"storage", "add", s066Source(t, "src.bin", body), "--name", name}, extra...)
	args = append(args, "--vault", pc.root)
	if out, errs, code := pc.lw(args...); code != 0 {
		t.Fatalf("%s: lw %s: exit %d\nstdout %q\nstderr %q", pc.name, strings.Join(args, " "), code, out, errs)
	}
}

// s066Sync runs an explicit `lw sync` as pc.
func s066Sync(pc *syncPC) (stdout, stderr string, code int) {
	pc.t.Helper()
	return pc.lw("sync", "--vault", pc.root)
}

// s066MustSync is s066Sync for a step the scenario needs to have worked.
func s066MustSync(t *testing.T, pc *syncPC) {
	t.Helper()
	if out, errs, code := s066Sync(pc); code != 0 {
		t.Fatalf("%s: lw sync: exit %d\nstdout %q\nstderr %q", pc.name, code, out, errs)
	}
}

// s066Doctor is pc's `storage` doctor check. runDoctor is called with the
// provider probe off, so no test touches the network.
func s066Doctor(t *testing.T, pc *syncPC) doctorCheck {
	t.Helper()
	pc.act()
	return checkByName(t, runDoctor(context.Background(), pc.root, doctorOptions{}), "storage")
}

// s066Exists reports whether anything is at path.
func s066Exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// s066Unwritable makes dir (creating it) unwritable for the test and puts the
// permission back at cleanup, so t.TempDir can still remove it. Permission bits
// do not bind root, so the test is skipped there.
func s066Unwritable(t *testing.T, dir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 0555 does not make a directory unwritable")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
}

// s066AutoSync switches pc's auto-sync off or on (its remotes stay). 066 D4 makes
// `lw storage add` request an auto push like lw note does, so a test about the
// push of ANOTHER verb, or of an explicit `lw sync`, stores its file with
// auto-sync off: otherwise the store itself would be the push under test.
func s066AutoSync(t *testing.T, pc *syncPC, on bool) {
	t.Helper()
	pc.act()
	cfg := mustLoadConfig(t)
	if cfg.Sync == nil {
		t.Fatalf("%s: no [sync] table in the config", pc.name)
	}
	cfg.Sync.Auto = nil
	if !on {
		off := false
		cfg.Sync.Auto = &off
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("%s: save config: %v", pc.name, err)
	}
}

// s066CatalogEmpty reports whether `lw storage list` shows no entry on pc.
func s066CatalogEmpty(t *testing.T, pc *syncPC) bool {
	t.Helper()
	out, errs, code := pc.lw("storage", "list", "--json", "--vault", pc.root)
	if code != 0 {
		t.Fatalf("%s: storage list --json: exit %d stderr %q", pc.name, code, errs)
	}
	return strings.TrimSpace(out) == "[]"
}

// TestStorageRmPropagates is B1 / R1. 062 deletes the file and its catalog entry
// on the PC that ran `lw storage rm` and nothing else: the payload stays on the
// remote, and the next sync (a "copy what is missing" mirror with no notion of a
// delete) copies it straight back, to that PC and, never having been deleted
// there, to every other one. Both catalogs end up empty while the bytes live on
// everywhere, and `lw sync clone` hands them to a brand-new PC (repro.sh B1).
//
// After the fix a delete travels through the catalog: the PC that pulls a
// catalog without f removes the f it holds, and a PC that clones later never
// sees it.
func TestStorageRmPropagates(t *testing.T) {
	s066Variants(t, func(t *testing.T, r *s066Rig) {
		a, b := r.a, r.b
		const body = "payload that is removed again\n"
		r.store(t, a, "f", body)
		s066MustSync(t, a)
		s066MustSync(t, b)
		if got := b.read("storage/f"); got != body {
			t.Fatalf("setup: b did not receive f (got %q)", got)
		}

		if out, errs, code := b.lw("storage", "rm", "f", "--vault", b.root); code != 0 {
			t.Fatalf("storage rm on b: exit %d stdout %q stderr %q", code, out, errs)
		}
		s066MustSync(t, b)
		s066MustSync(t, a)
		s066MustSync(t, b)

		for _, pc := range []*syncPC{a, b} {
			if s066Exists(filepath.Join(pc.root, "storage", "f")) {
				t.Errorf("%s: storage/f is still on disk after `lw storage rm f` on b and sync b, a, b (B1: the delete never propagated, or the payload came back)", pc.name)
			}
			if !s066CatalogEmpty(t, pc) {
				t.Errorf("%s: the catalog still lists an entry; want it empty", pc.name)
			}
		}

		c := newSyncPC(t, "c")
		if out, errs, code := c.lw("sync", "clone", r.spec, c.root); code != 0 {
			t.Fatalf("sync clone into c: exit %d stdout %q stderr %q", code, out, errs)
		}
		if s066Exists(filepath.Join(c.root, "storage", "f")) {
			t.Errorf("a PC cloned after the rm received storage/f (B1: the remote still serves the removed payload)")
		}
	})
}

// TestStorageRmKeepsLocallyChangedFile is R1a, the safety half of B1's fix. A
// delete that arrives with a pull must remove only what lw itself put there: if
// the user changed storage/f on this PC before the removal reached it, the bytes
// are theirs and are kept. The pull says so (the catalog no longer lists the
// file, so the user must be able to find out why it is still there), and doctor
// counts it as an untracked file instead of calling the vault clean.
func TestStorageRmKeepsLocallyChangedFile(t *testing.T) {
	r := newS066Rig(t, false)
	a, b := r.a, r.b
	r.store(t, a, "f", "the original bytes\n")
	s066MustSync(t, a)
	s066MustSync(t, b)
	if out, errs, code := b.lw("storage", "rm", "f", "--vault", b.root); code != 0 {
		t.Fatalf("storage rm on b: exit %d stdout %q stderr %q", code, out, errs)
	}
	s066MustSync(t, b)

	// a edits its copy BEFORE it learns of the removal.
	const edited = "edited on a, by hand\n"
	if err := os.WriteFile(filepath.Join(a.root, "storage", "f"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := s066Sync(a)
	if code != 0 {
		t.Errorf("a: lw sync exit %d, want 0 (keeping a file is not an error); stderr %q", code, stderr)
	}

	if want := "storage: kept f: changed locally since it was removed elsewhere"; !strings.Contains(stderr, want) {
		t.Errorf("a: stderr = %q, want a line %q (R1a)", stderr, want)
	}
	if got := a.read("storage/f"); got != edited {
		t.Errorf("a: storage/f = %q, want the locally edited bytes %q kept", got, edited)
	}
	c := s066Doctor(t, a)
	if !c.OK || !c.Warn || !strings.Contains(c.Detail, "1 untracked file(s) in storage/") {
		t.Errorf("a: doctor storage check = %+v, want OK with Warn and %q in the detail", c, "1 untracked file(s) in storage/")
	}
	if want := "move untracked files out of storage/, or lw storage add them"; c.Remedy != want {
		t.Errorf("a: doctor remedy = %q, want %q", c.Remedy, want)
	}
}

// TestStorageForceReplaceSameSize is B2 / R2. 062 decides whether a file needs
// copying by its SIZE (equal size means "already there") and only for a
// differing size by modification time; the sha256 the catalog records is never
// consulted. `lw storage add --force` of different bytes of the same length
// therefore reaches the remote and the other PC as a no-op, and b's doctor is
// left saying the file is "corrupt" with no way out (repro.sh B2).
//
// The two files get the SAME mtime explicitly, so the test needs no clock
// resolution and covers the case the old "newer wins" rule could not have
// resolved either; after the fix mtime is not consulted at all.
func TestStorageForceReplaceSameSize(t *testing.T) {
	s066Variants(t, func(t *testing.T, r *s066Rig) {
		a, b := r.a, r.b
		const oldBody, newBody = "hellX\n", "hellY\n" // 6 bytes each
		r.store(t, a, "g", oldBody)
		s066MustSync(t, a)
		s066MustSync(t, b)
		if got := b.read("storage/g"); got != oldBody {
			t.Fatalf("setup: b did not receive g (got %q)", got)
		}

		r.store(t, a, "g", newBody, "--force")
		same := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
		for _, pc := range []*syncPC{a, b} {
			if err := os.Chtimes(filepath.Join(pc.root, "storage", "g"), same, same); err != nil {
				t.Fatal(err)
			}
		}
		s066MustSync(t, a)
		s066MustSync(t, b)

		if got := b.read("storage/g"); got != newBody {
			t.Errorf("b: storage/g = %q, want %q (B2: a same-size replace never reached b)", got, newBody)
		}
		for _, pc := range []*syncPC{a, b} {
			c := s066Doctor(t, pc)
			if !c.OK || c.Warn || c.Detail != "all 1 storage file(s) present and verified" {
				t.Errorf("%s: doctor storage check = %+v, want OK, no warn, %q", pc.name, c, "all 1 storage file(s) present and verified")
			}
		}
	})
}

// s066HistoryVault is a copy of the minimal fixture with the shape B3 needs: a
// first commit, then a stored markdown file, then a second commit - so the
// stored file is the one path the second snapshot has that the first does not,
// which is the ADDED entry `lw revert` prints as "skipped: storage/x.md". It
// returns the vault root and the stored file's bytes. The stored file is
// markdown on purpose: walkWholeTree only ever records *.md.
func s066HistoryVault(t *testing.T) (root, stored string) {
	t.Helper()
	root = testutil.CopyFixture(t, "minimal")
	commit := func(id, msg, path, title string) string {
		t.Helper()
		openCreatePageChangeset(t, root, path, title)
		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"commit", "--vault", root, "-m", msg})
		})
		if code != 0 || !strings.Contains(stdout, "committed "+id) {
			t.Fatalf("commit %s: exit %d stdout %q stderr %q", id, code, stdout, stderr)
		}
		return stdout + stderr
	}
	commit("000001", "first page", "wiki/concepts/first-page.md", "First Page")

	stored = "# Stored, not wiki\n\nThis file is a payload, not a page.\n"
	if out, errs, code := captureRun(t, func() int {
		return run([]string{"storage", "add", s066Source(t, "x.md", stored), "--vault", root})
	}); code != 0 {
		t.Fatalf("storage add x.md: exit %d stdout %q stderr %q", code, out, errs)
	}
	commit("000002", "second page", "wiki/concepts/second-page.md", "Second Page")
	return root, stored
}

// s066TreesListing returns every snapshot manifest under root whose entries
// include a storage/ path, as "<file>: <line>".
func s066TreesListing(t *testing.T, root string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(root, ".llmwiki", "snapshots", "*.tree"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	var leaks []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			if len(line) > 66 && strings.HasPrefix(line[66:], "storage/") {
				leaks = append(leaks, filepath.Base(f)+": "+line)
			}
		}
	}
	return leaks
}

// TestStorageInvisibleToHistory is B3 / R3, the 047 mistake made again
// (TestNotesInvisibleToHistory is the template): every commit's snapshot
// manifest is built by walking the vault for *.md, and the walk does not skip
// the vault-root storage/. A stored markdown file is then "vault content": it is
// an ADDED path of whichever commit follows `lw storage add`, `lw revert` reports
// it as "skipped: storage/x.md" in a changeset about wiki pages, and the
// manifest - which is tracked by git and synced - leaks the stored file's name
// and sha to every PC (probes-v2.49.0.log P2).
//
// Snapshots written by v2.49.0 are not rewritten, so the second half pins that
// an OLD manifest that does list storage/x.md changes nothing: the commit after
// it, and the revert of that commit, must not mention the path or touch the file
// (D5: the diff ignores root storage/ on both sides).
func TestStorageInvisibleToHistory(t *testing.T) {
	syncHermetic(t)

	t.Run("new snapshots", func(t *testing.T) {
		root, _ := s066HistoryVault(t)
		for _, leak := range s066TreesListing(t, root) {
			t.Errorf("a snapshot lists a stored file (B3: storage/ is outside the vault's history): %s", leak)
		}
		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"revert", "--vault", root, "000002"})
		})
		if code != 0 {
			t.Fatalf("revert 000002: exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		if strings.Contains(stdout+stderr, "storage/") {
			t.Errorf("revert output mentions a stored file:\n%s%s", stdout, stderr)
		}
	})

	t.Run("a pre-fix snapshot", func(t *testing.T) {
		root, stored := s066HistoryVault(t)
		// The newest manifest as v2.49.0 would have written it: the same tree plus
		// the stored file. Any line already there is replaced, so the fixture is
		// the same whether or not the walk under test skips storage/.
		treePath := filepath.Join(root, ".llmwiki", "snapshots", "000002.tree")
		b, err := os.ReadFile(treePath)
		if err != nil {
			t.Fatal(err)
		}
		type entry struct{ path, line string }
		var entries []entry
		for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
			if len(line) > 66 && !strings.HasPrefix(line[66:], "storage/") {
				entries = append(entries, entry{line[66:], line})
			}
		}
		entries = append(entries, entry{"storage/x.md", s066SHA(stored) + "  storage/x.md"})
		sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
		var tree strings.Builder
		for _, e := range entries {
			tree.WriteString(e.line + "\n")
		}
		if err := os.WriteFile(treePath, []byte(tree.String()), 0o644); err != nil {
			t.Fatal(err)
		}

		openCreatePageChangeset(t, root, "wiki/concepts/third-page.md", "Third Page")
		cstdout, cstderr, ccode := captureRun(t, func() int {
			return run([]string{"commit", "--vault", root, "-m", "third page"})
		})
		if ccode != 0 || !strings.Contains(cstdout, "committed 000003") {
			t.Fatalf("commit 000003: exit %d stdout %q stderr %q", ccode, cstdout, cstderr)
		}
		if strings.Contains(cstdout+cstderr, "storage/") {
			t.Errorf("the commit after a pre-fix snapshot mentions a stored file:\n%s%s", cstdout, cstderr)
		}
		for _, leak := range s066TreesListing(t, root) {
			if !strings.HasPrefix(leak, "000002.tree: ") {
				t.Errorf("a snapshot written after the pre-fix one lists a stored file (B3): %s", leak)
			}
		}

		rstdout, rstderr, rcode := captureRun(t, func() int {
			return run([]string{"revert", "--vault", root, "000003"})
		})
		if rcode != 0 {
			t.Fatalf("revert 000003: exit %d stdout %q stderr %q", rcode, rstdout, rstderr)
		}
		if strings.Contains(rstdout+rstderr, "storage/") {
			t.Errorf("revert against a pre-fix snapshot mentions a stored file (D5: both sides of the diff ignore storage/):\n%s%s", rstdout, rstderr)
		}
		got, err := os.ReadFile(filepath.Join(root, "storage", "x.md"))
		if err != nil || string(got) != stored {
			t.Errorf("storage/x.md = %q, err %v; want it untouched", got, err)
		}
	})
}

// TestStorageRefusesNonVault is B4 / R4, the 059 mistake made again: findVaultRoot
// trusts an explicit --vault as given, which is fine for a verb that goes on to
// open the vault and fail there. Storage's verbs do not: add makes <dir>/storage
// and <dir>/.llmwiki and exits 0, list succeeds with nothing to say, and
// show/get/rm fail only later, with "entry not found" instead of the cause. A typo
// in --vault must be refused with lw note's exact sentence and exit code, and
// leave the directory as it was. lw note is run on the same input inside the
// test, so the exit code is the one observed, not one this file assumes.
func TestStorageRefusesNonVault(t *testing.T) {
	syncHermetic(t)
	src := s066Source(t, "f.bin", "payload\n")
	verbs := []struct {
		name string
		args []string
	}{
		{"add", []string{"add", src}},
		{"list", []string{"list"}},
		{"show", []string{"show", "f.bin"}},
		{"get", []string{"get", "f.bin"}},
		{"rm", []string{"rm", "f.bin"}},
	}
	dirs := []struct {
		name string
		make func(t *testing.T) string
	}{
		{"missing dir", func(t *testing.T) string { return filepath.Join(t.TempDir(), "nope") }},
		{"empty dir", func(t *testing.T) string { return t.TempDir() }},
	}
	for _, d := range dirs {
		for _, v := range verbs {
			t.Run(d.name+"/"+v.name, func(t *testing.T) {
				dir := d.make(t)

				_, noteErr, noteCode := captureRun(t, func() int {
					return run([]string{"note", "-m", "x", "--vault", dir})
				})
				const refusal = " is not a vault (no SCHEMA.md); pass --vault\n"
				if noteCode == 0 || !strings.HasSuffix(noteErr, refusal) {
					t.Fatalf("premise: lw note --vault %s: exit %d stderr %q; want a refusal ending %q", dir, noteCode, noteErr, refusal)
				}
				want := strings.Replace(noteErr, "lw: note: ", "lw: storage: ", 1)

				stdout, stderr, code := captureRun(t, func() int {
					return run(append(append([]string{"storage"}, v.args...), "--vault", dir))
				})
				if code != noteCode || stdout != "" || stderr != want {
					t.Errorf("lw storage %s --vault %s: exit %d stdout %q stderr %q; want exit %d (lw note's), nothing on stdout, stderr %q",
						v.name, dir, code, stdout, stderr, noteCode, want)
				}

				switch d.name {
				case "missing dir":
					if s066Exists(dir) {
						t.Errorf("%s was created by lw storage %s (B4: a typo'd --vault must not fabricate a vault)", dir, v.name)
					}
				default:
					entries, err := os.ReadDir(dir)
					if err != nil || len(entries) != 0 {
						t.Errorf("%s is no longer empty after lw storage %s: %v (err %v)", dir, v.name, entries, err)
					}
				}
			})
		}
	}
}

// TestSyncStorageFailureDoesNotBlockPush is B5 / R5. 062 runs the storage phase
// between the pull and the push of `lw sync` and returns its error, so a remote
// whose storage area cannot be written (a full disk, a permission, a dead NFS
// mount) stops the wiki's own commits from reaching the remote: the remote stays
// at the old commit and this PC is left ahead (repro.sh B5). Wiki work always
// syncs; the storage failure is reported after the push and fails the command.
func TestSyncStorageFailureDoesNotBlockPush(t *testing.T) {
	r := newS066Rig(t, false)
	a := r.a
	s066Unwritable(t, r.storeDir)
	s066AutoSync(t, a, false) // the explicit lw sync below is the first push, as in repro.sh B5
	r.store(t, a, "h", "bytes that cannot be uploaded\n")
	before := remoteHead(t, r.gitDir)

	stdout, stderr, code := s066Sync(a)

	head := a.git("rev-parse", "HEAD")
	if got := remoteHead(t, r.gitDir); got != head {
		t.Errorf("remote main = %s, want a's HEAD %s (B5: the storage error stopped the git push; it was %s before)", got, head, before)
	}
	if code != 1 {
		t.Errorf("exit %d, want 1 (the storage error fails the command, after the push)\nstdout %q\nstderr %q", code, stdout, stderr)
	}
	if want := "(wiki synced; run lw sync again)"; !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr, want)
	}
}

// TestAutoPushUploadsStorageFirst is B6 / R6a. The catalog is a git file and
// travels with every push - the auto-push after lw commit, lw reject, lw note and
// the TUI pusher - but 062 copies payloads only inside an explicit `lw sync`. So
// `lw storage add` followed by any other verb publishes a catalog entry whose
// bytes are nowhere but on the PC that stored them: the other PC lists the file,
// cannot read it, and doctor calls that "corrupt" (repro.sh B6). Every push of
// git now uploads the objects it is about to point at first.
//
// upload-fails: content-before-pointer must never become pointer-blocked-by-
// content (R5 again): if the upload cannot be done the git push still lands, and
// the verb says so in one line and keeps its own exit code.
func TestAutoPushUploadsStorageFirst(t *testing.T) {
	r := newS066Rig(t, false)
	a, b := r.a, r.b
	const body = "payload stored, then an unrelated verb\n"
	s066AutoSync(t, a, false) // the store must not push: lw note's push is the one under test
	r.store(t, a, "k", body)
	s066AutoSync(t, a, true)
	noteAt(t, 0)

	stdout, stderr, code := a.lw("note", "-m", "x", "--vault", a.root)
	if code != 0 {
		t.Errorf("lw note on a: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	obj := s066ObjectPath(r.storeDir, body)
	if got, err := os.ReadFile(obj); err != nil || string(got) != body {
		t.Errorf("the remote object store has no object for k at %s after the auto-push of lw note (B6: content must reach the remote before the catalog entry that points to it): %q, err %v", obj, got, err)
	}

	if out, errs, code := s066Sync(b); code != 0 {
		t.Errorf("b: lw sync exit %d\nstdout %q\nstderr %q", code, out, errs)
	}
	if got := b.read("storage/k"); got != body {
		t.Errorf("b: storage/k = %q after lw sync, want %q", got, body)
	}

	t.Run("upload-fails", func(t *testing.T) {
		r := newS066Rig(t, false)
		a := r.a
		s066Unwritable(t, r.storeDir)
		s066AutoSync(t, a, false)
		r.store(t, a, "k", "payload that cannot be uploaded\n")
		s066AutoSync(t, a, true)
		noteAt(t, 0)
		before := remoteHead(t, r.gitDir)

		stdout, stderr, code := a.lw("note", "-m", "x", "--vault", a.root)

		if code != 0 {
			t.Errorf("lw note exit %d, want 0 (an auto-sync problem never changes the verb's exit code)\nstdout %q\nstderr %q", code, stdout, stderr)
		}
		head := a.git("rev-parse", "HEAD")
		if got := remoteHead(t, r.gitDir); got == before || got != head {
			t.Errorf("remote main = %s, want a's HEAD %s, advanced from %s (the failed upload must not block the push)", got, head, before)
		}
		if want := "(wiki synced; run lw sync again)"; !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want one warning line containing %q", stderr, want)
		}
	})
}

// TestStoragePendingIsNotCorrupt is B6 / R6b, the reader's half of the same
// defect. A catalog entry whose payload simply has not arrived yet is the normal
// state of a PC between a pull and the fetch, not damage: 062 gives it the one
// word "missing" in `storage list -v`, an ENOENT from `storage get`, and a doctor
// failure that advises "restore from a backup or rm" - the one remedy that makes
// it worse. It is `pending`: the list says so, get says where the bytes are, and
// doctor warns without failing and says to sync.
//
// The payload is made not to arrive by emptying the remote's storage area after
// a's sync, which is the state any PC is in whose neighbour has not uploaded yet.
func TestStoragePendingIsNotCorrupt(t *testing.T) {
	r := newS066Rig(t, false)
	a, b := r.a, r.b
	r.store(t, a, "k", "payload nobody uploaded\n")
	s066MustSync(t, a)
	if err := os.RemoveAll(r.storeDir); err != nil {
		t.Fatal(err)
	}
	// b learns the catalog entry; the fetch has nothing to fetch, which `lw sync`
	// is free to report. What this test pins is how the PC then describes it.
	b.lw("sync", "--vault", b.root)
	if out, _, _ := b.lw("storage", "list", "--vault", b.root); !strings.Contains(out, "k") {
		t.Fatalf("setup: b's catalog does not list k after lw sync: %q", out)
	}
	if s066Exists(filepath.Join(b.root, "storage", "k")) {
		t.Fatal("setup: b has storage/k although nobody uploaded it")
	}

	out, errs, code := b.lw("storage", "list", "-v", "--vault", b.root)
	if code != 0 {
		t.Fatalf("storage list -v on b: exit %d stderr %q", code, errs)
	}
	var row []string
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] == "k" {
			row = f
		}
	}
	if row == nil {
		t.Fatalf("storage list -v on b has no row for k:\n%s", out)
	}
	if !s066Contains(row, "pending") {
		t.Errorf("storage list -v row for k = %q, want the state word %q (B6: it is not damage, it has not arrived)", strings.Join(row, " "), "pending")
	}

	gout, gerrs, gcode := b.lw("storage", "get", "k", "-o", filepath.Join(t.TempDir(), "out"), "--vault", b.root)
	if want := "k has not arrived yet (stored on another PC; run lw sync there)"; gcode != 1 || gout != "" || !strings.Contains(gerrs, want) {
		t.Errorf("storage get k on b: exit %d stdout %q stderr %q; want exit 1 and %q", gcode, gout, gerrs, want)
	}

	c := s066Doctor(t, b)
	if !c.OK || !c.Warn || c.Detail != "0 of 1 verified; 1 pending (not arrived yet)" {
		t.Errorf("b: doctor storage check = %+v, want OK with Warn, detail %q", c, "0 of 1 verified; 1 pending (not arrived yet)")
	}
	if want := "run lw sync; if it stays pending, run lw sync on the PC that stored it"; c.Remedy != want {
		t.Errorf("b: doctor remedy = %q, want %q", c.Remedy, want)
	}
}

// s066Contains reports whether list has s.
func s066Contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// TestStorageGetFetchesOnDemand is R6: a pending entry is not a dead end. b has
// the catalog entry for k (the start-of-verb pull of an unrelated lw note
// brought it, with no `lw sync` on b) and the payload is on the remote, so
// `lw storage get k` fetches that one object itself, verifies it and serves it,
// instead of failing with ENOENT until the user thinks of running a sync.
func TestStorageGetFetchesOnDemand(t *testing.T) {
	r := newS066Rig(t, false)
	a, b := r.a, r.b
	const body = "payload fetched when asked for\n"
	r.store(t, a, "k", body)
	s066MustSync(t, a)

	noteAt(t, 0)
	if out, errs, code := b.lw("note", "-m", "pulls the catalog", "--vault", b.root); code != 0 {
		t.Fatalf("lw note on b: exit %d stdout %q stderr %q", code, out, errs)
	}
	if out, _, _ := b.lw("storage", "list", "--vault", b.root); !strings.Contains(out, "k") {
		t.Fatalf("setup: the auto-pull of lw note did not bring k's catalog entry to b: %q", out)
	}
	if s066Exists(filepath.Join(b.root, "storage", "k")) {
		t.Fatal("setup: b already has storage/k; the scenario needs it pending")
	}

	dest := filepath.Join(t.TempDir(), "out")
	stdout, stderr, code := b.lw("storage", "get", "k", "-o", dest, "--vault", b.root)
	if code != 0 {
		t.Errorf("storage get k on b: exit %d stdout %q stderr %q; want 0 (R6: fetch on demand)", code, stdout, stderr)
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != body {
		t.Errorf("storage get k -o: wrote %q, err %v; want %q byte-exact", got, err, body)
	}
}

// TestDoctorStorageStates is R7: doctor tells the four states apart, because each
// has a different cure and 062's single "missing or corrupt" had none that was
// right for all (A-066-1 amends 062's A-062-2 wording; TestDoctorStorageCheck
// keeps its scenarios). One vault holds one of each: v verified, p pending (no
// file: not arrived), c corrupt (bytes lw did not write and the catalog does not
// name), o an orphan (a file in storage/ the catalog has never heard of).
//
// The check fails only for damage. Pending and orphan are warnings, and when a
// corrupt entry is present its remedy comes first.
func TestDoctorStorageStates(t *testing.T) {
	syncHermetic(t)
	root := testutil.CopyFixture(t, "minimal")
	for _, f := range []struct{ name, body string }{{"v.bin", "verified\n"}, {"p.bin", "pending\n"}, {"c.bin", "corrupt\n"}} {
		if out, errs, code := captureRun(t, func() int {
			return run([]string{"storage", "add", s066Source(t, f.name, f.body), "--vault", root})
		}); code != 0 {
			t.Fatalf("storage add %s: exit %d stdout %q stderr %q", f.name, code, out, errs)
		}
	}
	if err := os.Remove(filepath.Join(root, "storage", "p.bin")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "storage", "c.bin"), []byte("changed by something other than lw\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "storage", "o.bin"), []byte("nobody catalogued this\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := checkByName(t, runDoctor(context.Background(), root, doctorOptions{}), "storage")

	const wantDetail = "1 of 3 verified; 1 pending (not arrived yet); 1 corrupt (changed on disk); 1 untracked file(s) in storage/"
	if c.Detail != wantDetail {
		t.Errorf("detail = %q, want %q", c.Detail, wantDetail)
	}
	if c.OK {
		t.Errorf("check = %+v, want OK false (a corrupt file is damage)", c)
	}
	// The remedy of the first applicable state, corrupt. "<name>" is a
	// placeholder in the spec; either it or the entry's name satisfies it.
	remedy := regexp.MustCompile(`^lw storage show (<name>|c\.bin) names the expected sha256; restore the file or lw storage rm it$`)
	if !remedy.MatchString(c.Remedy) {
		t.Errorf("remedy = %q, want the corrupt remedy %q", c.Remedy, "lw storage show <name> names the expected sha256; restore the file or lw storage rm it")
	}
}
