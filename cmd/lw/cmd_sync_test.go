package main

// cmd_sync_test.go pins the `lw sync` verbs (042 D5): their exact stdout and
// stderr, the config lines init and clone write, and what status shows. Every
// remote is a bare repository in a temp directory; nothing touches a network.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/vault"
	"github.com/awepo-pro/lw/internal/vaultsync"
)

// appendTo adds a line to a vault file, so its bytes (and its page SHA)
// change.
func (pc *syncPC) appendTo(rel, line string) {
	pc.t.Helper()
	pc.write(rel, pc.read(rel)+line)
}

const kvPage = "wiki/concepts/kv-cache.md"

// stderrEndsWith reports whether the dispatch error line is the last thing
// stderr holds. An interactive sync passes git's own progress through, so
// stderr may carry lines before it — never after.
func stderrEndsWith(stderr, line string) bool { return strings.HasSuffix(stderr, line) }

// TestSyncVerbOutputs: up to date / pulled / pushed / diverged / take-remote /
// no remotes / format too new, each with exact stdout or error line.
func TestSyncVerbOutputs(t *testing.T) {
	t.Run("up to date", func(t *testing.T) {
		a, _, remote := syncPair(t)
		stdout, stderr, code := a.lw("sync")
		if code != 0 || stdout != "up to date with "+remote+"\n" {
			t.Fatalf("exit %d stdout %q stderr %q; want 0 and the up-to-date line", code, stdout, stderr)
		}
	})

	t.Run("pushed", func(t *testing.T) {
		a, _, remote := syncPair(t)
		a.appendTo(kvPage, "\nEdited on A.\n")
		stdout, stderr, code := a.lw("sync")
		if code != 0 || stdout != "pushed 1 commit(s) to "+remote+"\n" {
			t.Fatalf("exit %d stdout %q stderr %q; want 0 and the pushed line", code, stdout, stderr)
		}
		if subjects := remoteSubjects(t, remote); subjects[0] != "lw sync" {
			t.Errorf("remote tip subject = %q, want %q (no lw commit, not notes only)", subjects[0], "lw sync")
		}
		if got := remoteFile(t, remote, kvPage); !strings.Contains(got, "Edited on A.") {
			t.Errorf("the remote does not hold A's edit")
		}
	})

	t.Run("pulled, and the index rebuilt when wiki changed", func(t *testing.T) {
		a, b, remote := syncPair(t)
		a.appendTo(kvPage, "\nEdited on A.\n")
		if _, _, code := a.lw("sync"); code != 0 {
			t.Fatal("A could not push")
		}
		stdout, stderr, code := b.lw("sync")
		if want := "pulled 1 commit(s) from " + remote + "\nrebuilt the search index\n"; code != 0 || stdout != want {
			t.Fatalf("exit %d stdout %q stderr %q; want 0 and %q", code, stdout, stderr, want)
		}
		if !strings.Contains(b.read(kvPage), "Edited on A.") {
			t.Error("B's work tree did not take A's edit")
		}
		// The rebuilt index matches the pulled vault.
		v, err := vault.Open(b.root)
		if err != nil {
			t.Fatal(err)
		}
		ix, err := index.Load(filepath.Join(b.root, ".llmwiki", "index.gob"))
		if err != nil || ix.StaleAgainst(v) {
			t.Errorf("index after the pull: load err %v, stale %v", err, err == nil && ix.StaleAgainst(v))
		}
	})

	t.Run("pulled, no wiki change, no rebuild line", func(t *testing.T) {
		a, b, remote := syncPair(t)
		a.write("notes/20261009-120000-idea.md", "---\ncreated: 2026-10-09T12:00:00+08:00\nsummarized: null\n---\n\nan idea\n")
		if _, _, code := a.lw("sync"); code != 0 {
			t.Fatal("A could not push")
		}
		stdout, stderr, code := b.lw("sync")
		if want := "pulled 1 commit(s) from " + remote + "\n"; code != 0 || stdout != want {
			t.Fatalf("exit %d stdout %q stderr %q; want 0 and %q", code, stdout, stderr, want)
		}
	})

	t.Run("diverged is refused and nothing changes", func(t *testing.T) {
		a, b, remote := syncPair(t)
		a.appendTo(kvPage, "\nEdited on A.\n")
		if _, _, code := a.lw("sync"); code != 0 {
			t.Fatal("A could not push")
		}
		// B edited a tracked page (A-042-7: an untracked or journal-only change
		// no longer stops a pull, so only a committed edit can diverge).
		b.appendTo("index.md", "\nedited on B\n")
		// ... and the page A edited (A-042-8: edits that touch different pages
		// are rebased now, so this divergence has to conflict).
		b.appendTo(kvPage, "\nEdited on B.\n")
		stdout, stderr, code := b.lw("sync")
		want := "lw: sync: diverged from " + remote + ": this PC has 1 commit(s) the remote lacks, the remote has 1 this PC lacks; " +
			"nothing was changed — lw sync --take-remote keeps the remote and saves this PC's commits on a backup branch\n"
		if code != 1 || stdout != "" || !stderrEndsWith(stderr, want) {
			t.Fatalf("exit %d stdout %q stderr %q; want 1, no stdout and the diverged line", code, stdout, stderr)
		}
		if strings.Contains(b.read(kvPage), "Edited on A.") {
			t.Error("a refused sync merged A's edit")
		}
		if !strings.Contains(b.read("index.md"), "edited on B") {
			t.Error("a refused sync lost B's own work")
		}

		// --take-remote keeps the remote and saves B's commits on a branch.
		stdout, stderr, code = b.lw("sync", "--take-remote")
		re := regexp.MustCompile(`^took ` + regexp.QuoteMeta(remote) + `; this PC's commits are saved on branch (lw-diverged-\d{8}-\d{6}) \(git -C ` +
			regexp.QuoteMeta(b.root) + ` log (lw-diverged-\d{8}-\d{6})\)\nrebuilt the search index\n$`)
		m := re.FindStringSubmatch(stdout)
		if code != 0 || m == nil || m[1] != m[2] {
			t.Fatalf("exit %d stdout %q stderr %q; want the took line, naming one branch twice, then the rebuild line", code, stdout, stderr)
		}
		if !strings.Contains(b.read(kvPage), "Edited on A.") {
			t.Error("--take-remote did not make B's tree the remote's")
		}
		if got := b.git("show", m[1]+":index.md"); !strings.Contains(got, "edited on B") {
			t.Errorf("the backup branch %s does not hold B's work", m[1])
		}
		if stdout, _, code = b.lw("sync"); code != 0 || stdout != "up to date with "+remote+"\n" {
			t.Errorf("after take-remote: exit %d stdout %q, want up to date", code, stdout)
		}
	})

	t.Run("take-remote without a wiki change has no rebuild line", func(t *testing.T) {
		a, b, remote := syncPair(t)
		a.write("notes/20261009-120000-a.md", "a\n")
		if _, _, code := a.lw("sync"); code != 0 {
			t.Fatal("A could not push")
		}
		b.write("notes/20261009-130000-b.md", "b\n")
		stdout, stderr, code := b.lw("sync", "--take-remote")
		re := regexp.MustCompile(`^took ` + regexp.QuoteMeta(remote) + `; this PC's commits are saved on branch lw-diverged-\d{8}-\d{6} \(git -C ` +
			regexp.QuoteMeta(b.root) + ` log lw-diverged-\d{8}-\d{6}\)\n$`)
		if code != 0 || !re.MatchString(stdout) {
			t.Fatalf("exit %d stdout %q stderr %q; want only the took line", code, stdout, stderr)
		}
	})

	t.Run("no remotes", func(t *testing.T) {
		syncHermetic(t)
		pc := newSyncPC(t, "c")
		pc.useFixture()
		for _, args := range [][]string{{"sync"}, {"sync", "--take-remote"}, {"sync", "status"}} {
			full := append(append([]string{}, args...), "--vault", pc.root)
			stdout, stderr, code := pc.lw(full...)
			want := "lw: sync: no [sync] remotes in config — run lw sync init <host:path> first\n"
			if code != 1 || stdout != "" || stderr != want {
				t.Errorf("%v: exit %d stdout %q stderr %q; want 1 and %q", args, code, stdout, stderr, want)
			}
		}
	})

	t.Run("the vault is not under lw sync", func(t *testing.T) {
		syncHermetic(t)
		pc := newSyncPC(t, "c")
		pc.useFixture()
		pc.setConfig("[sync]\nremotes = [\"nowhere:vault\"]\n")
		stdout, stderr, code := pc.lw("sync", "--vault", pc.root)
		want := "lw: sync: the vault is not under lw sync yet — run lw sync init <remote> or lw sync clone\n"
		if code != 1 || stdout != "" || stderr != want {
			t.Fatalf("exit %d stdout %q stderr %q; want 1 and %q", code, stdout, stderr, want)
		}
		// Nothing was committed, no .gitignore written: not under sync means untouched.
		if _, err := os.Stat(filepath.Join(pc.root, ".git")); err == nil {
			t.Error("a refused sync created .git")
		}
		if _, err := os.Stat(filepath.Join(pc.root, ".gitignore")); err == nil {
			t.Error("a refused sync wrote .gitignore")
		}
	})

	t.Run("format too new on the remote", func(t *testing.T) {
		_, b, remote := syncPair(t)
		pushFromScratch(t, remote, ".llmwiki/format", "{\"version\": 2}\n", "bump format")
		want := "lw: sync: the remote vault is format 2; this lw supports 1 — upgrade lw on this PC, then lw sync\n"
		for _, args := range [][]string{{"sync"}, {"sync", "--take-remote"}} {
			stdout, stderr, code := b.lw(args...)
			if code != 1 || stdout != "" || !stderrEndsWith(stderr, want) {
				t.Errorf("%v: exit %d stdout %q stderr %q; want 1 and %q", args, code, stdout, stderr, want)
			}
		}
		if b.read(".llmwiki/format") != "" {
			t.Error("a refused sync checked out the newer format")
		}
	})

	t.Run("every remote failed", func(t *testing.T) {
		a, _, _ := syncPair(t)
		dead1 := filepath.Join(syncTemp(t), "gone-1")
		dead2 := filepath.Join(syncTemp(t), "gone-2")
		a.setConfig("[vault]\npath = \"" + a.root + "\"\n[sync]\nremotes = [\"" + dead1 + "\", \"" + dead2 + "\"]\n")
		stdout, stderr, code := a.lw("sync")
		if code != 1 || stdout != "" || !strings.Contains(stderr, "lw: sync: "+dead1+": ") || !strings.Contains(stderr, "; "+dead2+": ") {
			t.Fatalf("exit %d stdout %q stderr %q; want 1 and a RemoteError naming both remotes in order", code, stdout, stderr)
		}
	})

	t.Run("the first remote that answers wins", func(t *testing.T) {
		a, _, remote := syncPair(t)
		dead := filepath.Join(syncTemp(t), "gone")
		a.setConfig("[vault]\npath = \"" + a.root + "\"\n[sync]\nremotes = [\"" + dead + "\", \"" + remote + "\"]\n")
		stdout, stderr, code := a.lw("sync")
		if code != 0 || stdout != "up to date with "+remote+"\n" {
			t.Fatalf("exit %d stdout %q stderr %q; want up to date with the second remote", code, stdout, stderr)
		}
	})

	t.Run("bad usage", func(t *testing.T) {
		syncHermetic(t)
		pc := newSyncPC(t, "c")
		for _, args := range [][]string{
			{"sync", "init"},
			{"sync", "init", "a", "b"},
			{"sync", "clone", "only-one"},
			{"sync", "status", "extra"},
			{"sync", "--take-remote", "status"},
			{"sync", "bogus"},
			{"sync", "--nope"},
		} {
			stdout, _, code := pc.lw(args...)
			if code != 2 || stdout != "" {
				t.Errorf("%v: exit %d stdout %q, want 2 and no stdout", args, code, stdout)
			}
		}
	})
}

// TestSyncHelpOwnsTheGitignore: the usage lists the four sync verbs after
// doctor, word for word, and says lw sync owns (rewrites) the vault's
// .gitignore.
func TestSyncHelpOwnsTheGitignore(t *testing.T) {
	stdout, _, code := captureRun(t, func() int { return run([]string{"--help"}) })
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	rows := []string{
		"  sync [--take-remote]         pull then push the vault through its [sync] remotes\n",
		"  sync status                  show ahead/behind against the remote, without changing anything\n",
		"  sync init <remote>           put this vault under lw sync and push it to an empty remote\n",
		"  sync clone <remote> <dir>    copy a synced vault from its remote to this PC\n",
	}
	doctor := strings.Index(stdout, "  doctor [--unlock]")
	tui := strings.Index(stdout, "  tui  ")
	for _, row := range rows {
		i := strings.Index(stdout, row)
		if i < 0 {
			t.Fatalf("--help lacks the row %q:\n%s", row, stdout)
		}
		if i < doctor || i > tui {
			t.Errorf("row %q is not between doctor and tui", row)
		}
	}
	if strings.Contains(stdout, "stage") {
		t.Error("--help mentions the hidden stage verb")
	}

	for _, args := range [][]string{{"sync", "--help"}, {"sync", "-h"}} {
		out, errs, code := captureRun(t, func() int { return run(args) })
		if code != 0 || errs != "" {
			t.Errorf("%v: exit %d stderr %q, want 0 and nothing on stderr", args, code, errs)
		}
		if !strings.Contains(out, ".gitignore") || !strings.Contains(out, "rewrite") {
			t.Errorf("%v does not say lw sync owns and rewrites the vault's .gitignore:\n%s", args, out)
		}
		for _, verb := range []string{"sync [--take-remote]", "sync status", "sync init <remote>", "sync clone <remote> <dir>"} {
			if !strings.Contains(out, verb) {
				t.Errorf("%v does not describe %q:\n%s", args, verb, out)
			}
		}
	}
}

// TestSyncInitWritesMissingConfigOnly: an empty config gets both lines; a
// config that already holds a key keeps its own value and prints nothing for
// it; one that holds both is left byte for byte alone.
func TestSyncInitWritesMissingConfigOnly(t *testing.T) {
	type tc struct {
		name    string
		config  string // "" = no file
		lines   func(root, remote string) string
		wantCfg func(t *testing.T, c *config.Config, root, remote string)
	}
	vaultLine := func(root string) string { return "config: [vault] path = " + root + "\n" }
	remotesLine := func(remote string) string { return "config: [sync] remotes = [\"" + remote + "\"]\n" }

	cases := []tc{
		{
			name: "an empty config gets both",
			lines: func(root, remote string) string {
				return vaultLine(root) + remotesLine(remote)
			},
			wantCfg: func(t *testing.T, c *config.Config, root, remote string) {
				if c.Vault.Path != root {
					t.Errorf("Vault.Path = %q, want %q", c.Vault.Path, root)
				}
				if got := c.Sync.RemoteList(); len(got) != 1 || got[0] != remote {
					t.Errorf("Sync.Remotes = %q, want [%q]", got, remote)
				}
			},
		},
		{
			name:   "a config with a vault path gets only the remotes",
			config: "# my config\n[vault]\npath = \"/somewhere/else\"\n",
			lines:  func(root, remote string) string { return remotesLine(remote) },
			wantCfg: func(t *testing.T, c *config.Config, root, remote string) {
				if c.Vault.Path != "/somewhere/else" {
					t.Errorf("Vault.Path = %q, want the user's own", c.Vault.Path)
				}
				if got := c.Sync.RemoteList(); len(got) != 1 || got[0] != remote {
					t.Errorf("Sync.Remotes = %q, want [%q]", got, remote)
				}
			},
		},
		{
			name:   "a config with remotes gets only the vault path",
			config: "[sync]\nremotes = [\"home:lw-vault\", \"home-remote:lw-vault\"]\n",
			lines:  func(root, remote string) string { return vaultLine(root) },
			wantCfg: func(t *testing.T, c *config.Config, root, remote string) {
				if c.Vault.Path != root {
					t.Errorf("Vault.Path = %q, want %q", c.Vault.Path, root)
				}
				if got := c.Sync.RemoteList(); len(got) != 2 || got[0] != "home:lw-vault" {
					t.Errorf("Sync.Remotes = %q, want the user's two", got)
				}
			},
		},
		{
			name:   "a [sync] table with only auto gets the remotes and keeps auto",
			config: "[sync]\nauto = false\n",
			lines:  func(root, remote string) string { return vaultLine(root) + remotesLine(remote) },
			wantCfg: func(t *testing.T, c *config.Config, root, remote string) {
				if c.Sync == nil || c.Sync.Auto == nil || *c.Sync.Auto {
					t.Errorf("Sync.Auto = %v, want false kept", c.Sync)
				}
				if got := c.Sync.RemoteList(); len(got) != 1 || got[0] != remote {
					t.Errorf("Sync.Remotes = %q", got)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			syncHermetic(t)
			pc := newSyncPC(t, "a")
			pc.useFixture()
			remote := filepath.Join(syncTemp(t), "lw-vault")
			pc.setConfig(c.config)
			stdout, stderr, code := pc.lw("sync", "init", remote, "--vault", pc.root)
			want := "pushed 1 commit(s) to " + remote + "\n" + c.lines(pc.root, remote)
			if code != 0 || stdout != want {
				t.Fatalf("exit %d\nstdout %q\nwant   %q\nstderr %q", code, stdout, want, stderr)
			}
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			c.wantCfg(t, cfg, pc.root, remote)
		})
	}

	t.Run("a config holding both is left byte for byte alone", func(t *testing.T) {
		syncHermetic(t)
		pc := newSyncPC(t, "a")
		pc.useFixture()
		remote := filepath.Join(syncTemp(t), "lw-vault")
		body := "# hand written, comments and all\n[vault]\npath   = \"/elsewhere\"\n\n[sync]\nremotes = [ \"home:lw-vault\" ]   # ordered\n"
		pc.setConfig(body)
		stdout, stderr, code := pc.lw("sync", "init", remote, "--vault", pc.root)
		if want := "pushed 1 commit(s) to " + remote + "\n"; code != 0 || stdout != want {
			t.Fatalf("exit %d stdout %q stderr %q; want only the pushed line", code, stdout, stderr)
		}
		if got := pc.config(); got != body {
			t.Errorf("config bytes changed:\n got  %q\n want %q", got, body)
		}
	})

	t.Run("the vault is committed and pushed whole", func(t *testing.T) {
		syncHermetic(t)
		pc := newSyncPC(t, "a")
		pc.useFixture()
		remote := filepath.Join(syncTemp(t), "lw-vault")
		if _, stderr, code := pc.lw("sync", "init", remote, "--vault", pc.root); code != 0 {
			t.Fatalf("exit %d stderr %q", code, stderr)
		}
		if subjects := remoteSubjects(t, remote); len(subjects) != 1 || subjects[0] != "lw sync init" {
			t.Errorf("remote history = %q, want the one init commit", subjects)
		}
		if got := remoteFile(t, remote, ".gitignore"); got != strings.TrimSuffix(vaultsync.Ignore, "\n") {
			t.Errorf("the remote's .gitignore is not the managed one:\n%s", got)
		}
		if remoteFile(t, remote, "SCHEMA.md") == "" || remoteFile(t, remote, kvPage) == "" {
			t.Error("the remote lacks the vault's files")
		}
	})

	t.Run("a second init says it is already under lw sync", func(t *testing.T) {
		a, _, _ := syncPair(t)
		other := filepath.Join(syncTemp(t), "other")
		stdout, stderr, code := a.lw("sync", "init", other, "--vault", a.root)
		if code != 1 || stdout != "" || !stderrEndsWith(stderr, "lw: sync: already under lw sync\n") {
			t.Errorf("exit %d stdout %q stderr %q; want 1 and the already-under-lw-sync error", code, stdout, stderr)
		}
	})

	t.Run("a directory that is not a vault is refused before git runs", func(t *testing.T) {
		syncHermetic(t)
		pc := newSyncPC(t, "a")
		notVault := t.TempDir()
		putFile(t, notVault, "precious.txt", "keep\n")
		remote := filepath.Join(syncTemp(t), "lw-vault")
		stdout, stderr, code := pc.lw("sync", "init", remote, "--vault", notVault)
		if want := "lw: sync: " + notVault + " is not a vault (no SCHEMA.md); pass --vault\n"; code != 1 || stdout != "" || stderr != want {
			t.Errorf("exit %d stdout %q stderr %q; want 1 and %q", code, stdout, stderr, want)
		}
		if _, err := os.Stat(filepath.Join(notVault, ".git")); err == nil {
			t.Error("init created a repository in a directory that is not a vault")
		}
	})
}

// TestSyncClone: the cloned line, the index present and current, the config
// lines, the bookkeeping file, and a vault the other verbs can read.
func TestSyncClone(t *testing.T) {
	t.Run("a clone is a working vault", func(t *testing.T) {
		syncHermetic(t)
		pinSyncClock(t, time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC))
		a := newSyncPC(t, "a")
		a.useFixture()
		remote := filepath.Join(syncTemp(t), "lw-vault")
		if _, _, code := a.lw("sync", "init", remote, "--vault", a.root); code != 0 {
			t.Fatal("init failed")
		}
		c := newSyncPC(t, "c")
		stdout, stderr, code := c.lw("sync", "clone", remote, c.root)
		want := "cloned " + remote + " into " + c.root + "\n" +
			"config: [vault] path = " + c.root + "\n" +
			"config: [sync] remotes = [\"" + remote + "\"]\n"
		if code != 0 || stdout != want {
			t.Fatalf("exit %d\nstdout %q\nwant   %q\nstderr %q", code, stdout, want, stderr)
		}

		// The index exists and matches the cloned pages.
		v, err := vault.Open(c.root)
		if err != nil {
			t.Fatal(err)
		}
		ix, err := index.Load(filepath.Join(c.root, ".llmwiki", "index.gob"))
		if err != nil {
			t.Fatalf("no index after a clone: %v", err)
		}
		if ix.StaleAgainst(v) || ix.Len() != len(v.Pages()) {
			t.Errorf("the clone's index is stale or incomplete (%d docs, %d pages)", ix.Len(), len(v.Pages()))
		}

		// The files are A's, the managed .gitignore is in place, the
		// bookkeeping file says when and where.
		if c.read(kvPage) != a.read(kvPage) {
			t.Error("the clone's kv-cache page differs from A's")
		}
		if c.read(".gitignore") != vaultsync.Ignore {
			t.Error("the clone's .gitignore is not the managed one")
		}
		if got := c.read(".llmwiki/sync.json"); !strings.Contains(got, `"last_ok": "2026-10-09T12:00:00Z"`) || !strings.Contains(got, `"remote": "`+remote+`"`) {
			t.Errorf("sync.json after a clone = %q", got)
		}

		// git dropped any empty directory; the verbs must not mind.
		for _, args := range [][]string{{"status"}, {"log"}, {"lint"}, {"note", "list"}, {"session", "list"}, {"sync", "status"}} {
			full := append(append([]string{}, args...), "--vault", c.root)
			if _, stderr, code := c.lw(full...); code != 0 {
				t.Errorf("lw %v on the clone: exit %d stderr %q", args, code, stderr)
			}
		}
		rep := runDoctor(t.Context(), c.root, doctorOptions{})
		for _, ck := range rep.Checks {
			switch ck.Name {
			case "index", "objects", "journal", "recovery", "lock", "git":
				if !ck.OK {
					t.Errorf("doctor check %s fails on a fresh clone: %s", ck.Name, ck.Detail)
				}
			}
		}
	})

	t.Run("an existing config keeps its keys", func(t *testing.T) {
		a, _, remote := syncPair(t)
		c := newSyncPC(t, "c")
		body := "[vault]\npath = \"/mine\"\n[sync]\nremotes = [\"home:lw-vault\"]\n"
		c.setConfig(body)
		stdout, _, code := c.lw("sync", "clone", remote, c.root)
		if want := "cloned " + remote + " into " + c.root + "\n"; code != 0 || stdout != want {
			t.Fatalf("exit %d stdout %q; want only the cloned line", code, stdout)
		}
		if c.config() != body {
			t.Error("clone rewrote a config that already held both keys")
		}
		_ = a
	})

	t.Run("a relative directory is reported absolute", func(t *testing.T) {
		_, _, remote := syncPair(t)
		parent := t.TempDir()
		chdir(t, parent)
		c := newSyncPC(t, "c")
		stdout, stderr, code := c.lw("sync", "clone", remote, "mine")
		abs := filepath.Join(parent, "mine")
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			abs = filepath.Join(resolved, "mine")
		}
		if code != 0 || !strings.HasPrefix(stdout, "cloned "+remote+" into ") || !strings.Contains(stdout, abs) {
			t.Errorf("exit %d stdout %q stderr %q; want the absolute directory %s", code, stdout, stderr, abs)
		}
	})

	t.Run("a directory that is not empty is refused and left alone", func(t *testing.T) {
		_, _, remote := syncPair(t)
		c := newSyncPC(t, "c")
		putFile(t, c.root, "mine.txt", "keep\n")
		stdout, stderr, code := c.lw("sync", "clone", remote, c.root)
		if code != 1 || stdout != "" || !strings.HasPrefix(stderr, "lw: sync: ") {
			t.Fatalf("exit %d stdout %q stderr %q; want 1 and a sync error", code, stdout, stderr)
		}
		if c.read("mine.txt") != "keep\n" {
			t.Error("the refused clone touched the directory")
		}
		if c.config() != "" {
			t.Error("a refused clone wrote config")
		}
	})

	t.Run("a remote in a newer format is refused before anything is created", func(t *testing.T) {
		_, _, remote := syncPair(t)
		pushFromScratch(t, remote, ".llmwiki/format", "{\"version\": 2}\n", "bump format")
		c := newSyncPC(t, "c")
		stdout, stderr, code := c.lw("sync", "clone", remote, c.root)
		want := "lw: sync: the remote vault is format 2; this lw supports 1 — upgrade lw on this PC, then lw sync\n"
		if code != 1 || stdout != "" || !stderrEndsWith(stderr, want) {
			t.Fatalf("exit %d stdout %q stderr %q; want 1 and %q", code, stdout, stderr, want)
		}
		if _, err := os.Stat(c.root); err == nil {
			t.Error("a refused clone left a directory behind")
		}
		if c.config() != "" {
			t.Error("a refused clone wrote config")
		}
	})
}

// TestSyncStatusLines: the exact lines, including never, and that status
// changes nothing.
func TestSyncStatusLines(t *testing.T) {
	stamp := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

	t.Run("in step", func(t *testing.T) {
		pinSyncClock(t, stamp)
		a, _, remote := syncPair(t)
		stdout, stderr, code := a.lw("sync", "status")
		want := "remote   " + remote + "\nahead    0\nbehind   0\nformat   1 (this lw: 1)\nlast     2026-10-09T12:00:00Z\n"
		if code != 0 || stdout != want {
			t.Fatalf("exit %d\nstdout %q\nwant   %q\nstderr %q", code, stdout, want, stderr)
		}
	})

	t.Run("never", func(t *testing.T) {
		a, _, remote := syncPair(t)
		if err := os.Remove(filepath.Join(a.root, ".llmwiki", "sync.json")); err != nil {
			t.Fatal(err)
		}
		stdout, _, code := a.lw("sync", "status")
		want := "remote   " + remote + "\nahead    0\nbehind   0\nformat   1 (this lw: 1)\nlast     never\n"
		if code != 0 || stdout != want {
			t.Fatalf("exit %d stdout %q; want %q", code, stdout, want)
		}
	})

	t.Run("ahead", func(t *testing.T) {
		pinSyncClock(t, stamp)
		a, _, remote := syncPair(t)
		a.appendTo(kvPage, "\nmore\n")
		a.git("add", "-A")
		a.git("commit", "--quiet", "-m", "local work")
		stdout, _, code := a.lw("sync", "status")
		want := "remote   " + remote + "\nahead    1\nbehind   0\nformat   1 (this lw: 1)\nlast     2026-10-09T12:00:00Z\n"
		if code != 0 || stdout != want {
			t.Fatalf("exit %d stdout %q; want %q", code, stdout, want)
		}
	})

	t.Run("behind", func(t *testing.T) {
		pinSyncClock(t, stamp)
		a, b, remote := syncPair(t)
		a.write("notes/20261009-120000-a.md", "a\n")
		if _, _, code := a.lw("sync"); code != 0 {
			t.Fatal("A could not push")
		}
		stdout, _, code := b.lw("sync", "status")
		want := "remote   " + remote + "\nahead    0\nbehind   1\nformat   1 (this lw: 1)\nlast     2026-10-09T12:00:00Z\n"
		if code != 0 || stdout != want {
			t.Fatalf("exit %d stdout %q; want %q", code, stdout, want)
		}
	})

	t.Run("diverged says so", func(t *testing.T) {
		pinSyncClock(t, stamp)
		a, b, remote := syncPair(t)
		a.write("notes/20261009-120000-a.md", "a\n")
		if _, _, code := a.lw("sync"); code != 0 {
			t.Fatal("A could not push")
		}
		b.write("notes/20261009-130000-b.md", "b\n")
		b.git("add", "-A")
		b.git("commit", "--quiet", "-m", "local work")
		stdout, _, code := b.lw("sync", "status")
		want := "remote   " + remote + "\nahead    1\nbehind   1\nformat   1 (this lw: 1)\nlast     2026-10-09T12:00:00Z\ndiverged — run lw sync for details\n"
		if code != 0 || stdout != want {
			t.Fatalf("exit %d stdout %q; want %q", code, stdout, want)
		}
	})

	t.Run("the remote's format is shown", func(t *testing.T) {
		pinSyncClock(t, stamp)
		_, b, remote := syncPair(t)
		pushFromScratch(t, remote, ".llmwiki/format", "{\"version\": 3}\n", "bump format")
		stdout, _, code := b.lw("sync", "status")
		want := "remote   " + remote + "\nahead    0\nbehind   1\nformat   3 (this lw: 1)\nlast     2026-10-09T12:00:00Z\n"
		if code != 0 || stdout != want {
			t.Fatalf("exit %d stdout %q; want %q", code, stdout, want)
		}
	})

	t.Run("status changes nothing", func(t *testing.T) {
		_, b, _ := syncPair(t)
		b.write("notes/20261009-130000-b.md", "uncommitted\n")
		headBefore := b.git("rev-parse", "HEAD")
		before := treeDigest(t, b.root, ".git", ".llmwiki/logs")
		if _, _, code := b.lw("sync", "status"); code != 0 {
			t.Fatal("status failed")
		}
		if b.git("rev-parse", "HEAD") != headBefore {
			t.Error("status moved HEAD")
		}
		if got := b.git("status", "--porcelain", "--untracked-files=all"); !strings.Contains(got, "notes/20261009-130000-b.md") {
			t.Errorf("status committed or removed the user's uncommitted file: %q", got)
		}
		if treeDigest(t, b.root, ".git", ".llmwiki/logs") != before {
			t.Error("status changed files in the vault")
		}
	})
}
