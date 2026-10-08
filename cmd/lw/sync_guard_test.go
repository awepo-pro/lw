package main

// sync_guard_test.go pins the guards around 042: the checkout-collision
// refusal (A-042-6), the single place an engine is opened (A-042-4 M3), and
// the two places lw init and lw doctor had to learn about a synced vault
// (A-042-4 L3).

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/vaultsync"
)

const collisionText = "a checkout collision is unresolved (wiki/a.md, wiki/A.md) — rename the clashing files on the PC that created them, sync there, then run lw sync --take-remote here"

// markCollision plants the marker vaultsync leaves when a checkout collided.
func markCollision(t *testing.T, root string) {
	t.Helper()
	putFile(t, root, ".git/lw-collision", "wiki/a.md\nwiki/A.md\n")
}

// TestCollisionRefusalMatchesVaultsync: the text cmd/lw prints is the text
// vaultsync itself refuses with, read from the same marker.
func TestCollisionRefusalMatchesVaultsync(t *testing.T) {
	a, _, _ := syncPair(t)
	if err := collisionRefusal(a.root); err != nil {
		t.Fatalf("collisionRefusal without a marker = %v, want nil", err)
	}
	markCollision(t, a.root)
	err := collisionRefusal(a.root)
	if err == nil || err.Error() != collisionText {
		t.Fatalf("collisionRefusal = %v, want %q", err, collisionText)
	}
	_, verr := vaultsync.CommitWork(vaultsync.Options{Dir: a.root}, "x")
	if verr == nil || verr.Error() != err.Error() {
		t.Errorf("vaultsync refuses with %v; cmd/lw says %v — the two texts drifted", verr, err)
	}
}

// TestCollisionBlocksWritingVerbs: while .git/lw-collision exists, every
// writing verb — and lw tui at its start — refuses with vaultsync's text
// before doing anything; read-only verbs and the two sync escapes work.
// Reason: --take-remote discards tracked edits made while the marker exists,
// so an lw commit then would be lost.
func TestCollisionBlocksWritingVerbs(t *testing.T) {
	opsFile := filepath.Join(t.TempDir(), "ops.json")
	if err := os.WriteFile(opsFile, []byte("[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "note.md")
	if err := os.WriteFile(src, []byte("# A Note\n\nBody.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// An absolute path: were the refusal ever skipped, init must not create a
	// repository under the working directory.
	initRemote := filepath.Join(syncTemp(t), "init-remote")

	writing := []struct {
		verb string
		args func(r string) []string
	}{
		{"ingest", func(r string) []string { return []string{"ingest", "--vault", r, src} }},
		{"commit", func(r string) []string { return []string{"commit", "--vault", r, "-m", "x"} }},
		{"revert", func(r string) []string { return []string{"revert", "--vault", r, "000001"} }},
		{"note", func(r string) []string { return []string{"note", "--vault", r, "-m", "x"} }},
		{"lint", func(r string) []string { return []string{"lint", "--fix", "--vault", r} }},
		{"sync", func(r string) []string { return []string{"sync", "--vault", r} }},
		{"sync", func(r string) []string { return []string{"sync", "init", initRemote, "--vault", r} }},
		{"tui", func(r string) []string { return []string{"tui", "--vault", r} }},
		{"stage", func(r string) []string { return []string{"stage", "--vault", r, "--from", opsFile} }},
		{"mcp", func(r string) []string { return []string{"mcp", "--vault", r} }},
		{"doctor", func(r string) []string { return []string{"doctor", "--vault", r, "--discard-changeset"} }},
	}
	for _, w := range writing {
		args := w.args("")
		t.Run(strings.Join(args[:min(len(args), 2)], "_"), func(t *testing.T) {
			a, _, _ := syncPair(t)
			markCollision(t, a.root)
			before := treeDigest(t, a.root, ".llmwiki/logs")
			stdout, stderr, code := a.lw(w.args(a.root)...)
			if want := "lw: " + w.verb + ": " + collisionText + "\n"; code != 1 || stdout != "" || stderr != want {
				t.Fatalf("exit %d stdout %q stderr %q; want 1 and %q", code, stdout, stderr, want)
			}
			if treeDigest(t, a.root, ".llmwiki/logs") != before {
				t.Error("a refused verb changed files under the vault")
			}
		})
	}

	t.Run("a bare lw is the tui", func(t *testing.T) {
		a, _, _ := syncPair(t)
		markCollision(t, a.root)
		chdir(t, a.root)
		a.act()
		_, stderr, code := captureRun(t, func() int { return run(nil) })
		if want := "lw: tui: " + collisionText + "\n"; code != 1 || stderr != want {
			t.Errorf("exit %d stderr %q; want 1 and %q", code, stderr, want)
		}
	})

	t.Run("read-only verbs keep working", func(t *testing.T) {
		a, _, _ := syncPair(t)
		markCollision(t, a.root)
		for _, args := range [][]string{
			{"status"}, {"log"}, {"diff"}, {"lint"}, {"note", "list"}, {"session", "list"}, {"trace"}, {"sync", "status"}, {"doctor"},
		} {
			full := append(append([]string{}, args...), "--vault", a.root)
			_, stderr, _ := a.lw(full...)
			if strings.Contains(stderr, "checkout collision") {
				t.Errorf("lw %v was refused for the collision: %q", args, stderr)
			}
		}
		if _, stderr, code := a.lw("status", "--vault", a.root); code != 0 {
			t.Errorf("status: exit %d stderr %q", code, stderr)
		}
		if stdout, _, code := a.lw("note", "list", "--vault", a.root); code != 0 || stdout == "" {
			t.Errorf("note list: exit %d stdout %q", code, stdout)
		}
	})

	t.Run("sync --take-remote is the way out", func(t *testing.T) {
		a, _, remote := syncPair(t)
		markCollision(t, a.root)
		stdout, stderr, code := a.lw("sync", "--take-remote")
		if code != 0 || !strings.HasPrefix(stdout, "took "+remote+";") {
			t.Fatalf("exit %d stdout %q stderr %q; want --take-remote to run", code, stdout, stderr)
		}
		if _, err := os.Stat(filepath.Join(a.root, ".git", "lw-collision")); err == nil {
			t.Error("--take-remote left the marker behind")
		}
		// The way is open again.
		noteAt(t, 0)
		if _, stderr, code := a.lw("note", "-m", "back to work", "--vault", a.root); code != 0 {
			t.Errorf("note after take-remote: exit %d stderr %q", code, stderr)
		}
	})

	t.Run("a vault without lw sync has no marker and no refusal", func(t *testing.T) {
		syncHermetic(t)
		pc := newSyncPC(t, "c")
		pc.useFixture()
		noteAt(t, 0)
		if _, stderr, code := pc.lw("note", "-m", "x", "--vault", pc.root); code != 0 {
			t.Errorf("exit %d stderr %q", code, stderr)
		}
	})
}

// TestEngineOpensGoThroughOneHelper (A-042-4 M3): every verb that opens an
// engine does it through openVaultEngine, the one helper that installs the
// terminal hook. A verb that called stage.OpenEngine itself would commit or
// reject without ever syncing, and nothing would say so.
func TestEngineOpensGoThroughOneHelper(t *testing.T) {
	// The three places that may open an engine without the helper: lw init
	// proves its scaffold with a throwaway engine, and status reads the open
	// changeset — neither can commit or reject.
	allowed := map[string]bool{"openVaultEngine": true, "runInit": true, "changesetStatusLine": true}

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	var offenders []string
	helperOpens := false
	for _, pkg := range pkgs {
		for fname, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "stage" && sel.Sel.Name == "OpenEngine" {
						if fn.Name.Name == "openVaultEngine" {
							helperOpens = true
						}
						if !allowed[fn.Name.Name] {
							offenders = append(offenders, filepath.Base(fname)+":"+fn.Name.Name)
						}
					}
					if sel.Sel.Name == "OnTerminal" && fn.Name.Name != "openVaultEngine" && fn.Name.Name != "attach" {
						offenders = append(offenders, filepath.Base(fname)+":"+fn.Name.Name+" installs the hook itself")
					}
					return true
				})
			}
		}
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("these functions open an engine (or install the hook) without the one helper:\n  %s", strings.Join(offenders, "\n  "))
	}
	if !helperOpens {
		t.Error("openVaultEngine does not call stage.OpenEngine — the helper is not the one place")
	}
}

// TestInitLeavesAManagedGitignoreAlone (A-042-4 L3): lw init never appends to
// the .gitignore lw sync owns; its first line marks it.
func TestInitLeavesAManagedGitignoreAlone(t *testing.T) {
	t.Run("ensureGitignore", func(t *testing.T) {
		dir := t.TempDir()
		putFile(t, dir, ".gitignore", vaultsync.Ignore)
		wrote, err := ensureGitignore(dir)
		if err != nil || wrote {
			t.Fatalf("ensureGitignore = %v, %v; want false, nil for a managed file", wrote, err)
		}
		if got, _ := readFileString(filepath.Join(dir, ".gitignore")); got != vaultsync.Ignore {
			t.Errorf("the managed .gitignore changed:\n%s", got)
		}
	})

	t.Run("a user's file that only mentions it is still the user's", func(t *testing.T) {
		dir := t.TempDir()
		putFile(t, dir, ".gitignore", "build/\n# managed by lw sync: not really\n")
		wrote, err := ensureGitignore(dir)
		if err != nil || !wrote {
			t.Fatalf("ensureGitignore = %v, %v; want the entry appended", wrote, err)
		}
		if got, _ := readFileString(filepath.Join(dir, ".gitignore")); !strings.HasSuffix(got, ".llmwiki/\n") {
			t.Errorf("no .llmwiki/ entry appended:\n%s", got)
		}
	})

	t.Run("lw init --force into a synced vault", func(t *testing.T) {
		a, _, _ := syncPair(t)
		chdir(t, a.root)
		before := a.read(".gitignore")
		if before != vaultsync.Ignore {
			t.Fatalf("setup: .gitignore is not the managed one: %q", before)
		}
		if _, stderr, code := captureRun(t, func() int { return run([]string{"init", "--schema", "ml", "--force"}) }); code != 0 {
			t.Fatalf("init: exit %d stderr %q", code, stderr)
		}
		if got := a.read(".gitignore"); got != before {
			t.Errorf("lw init rewrote the managed .gitignore:\n%s", got)
		}
	})
}

// TestDoctorGitCheckKnowsSyncedVaults (A-042-4 L3): a vault under lw sync
// tracks .llmwiki/ on purpose (the journal, objects and committed changesets
// are what sync carries), so doctor must not warn about it.
func TestDoctorGitCheckKnowsSyncedVaults(t *testing.T) {
	a, _, _ := syncPair(t)
	// Give the vault some history, so .llmwiki holds what a sync carries.
	openCreatePageChangeset(t, a.root, "wiki/concepts/some-page.md", "Some Page")
	if _, stderr, code := a.lw("commit", "--vault", a.root, "-m", "a page"); code != 0 {
		t.Fatalf("commit: exit %d stderr %q", code, stderr)
	}
	if tracked := a.git("ls-files", ".llmwiki"); tracked == "" {
		t.Fatal("setup: the synced vault tracks nothing under .llmwiki")
	}
	ck := checkTracked(a.root)
	if !ck.OK || ck.Warn || ck.Skipped || !strings.Contains(ck.Detail, "lw sync") {
		t.Errorf("checkTracked on a synced vault = %+v; want a plain pass that mentions lw sync", ck)
	}

	// A vault with its own repository that tracks .llmwiki/ still warns.
	syncHermetic(t)
	pc := newSyncPC(t, "own")
	pc.useFixture()
	putFile(t, pc.root, ".llmwiki/journal.ndjson", "")
	pc.git("init", "--quiet", "-b", "main")
	pc.git("add", "-f", "-A")
	pc.git("commit", "--quiet", "-m", "mine")
	if ck := checkTracked(pc.root); !ck.Warn {
		t.Errorf("checkTracked on a hand-made repo that tracks .llmwiki = %+v; want the old warning", ck)
	}
}

// TestTUIWiresAutoSync: cmdTUI cannot be driven headless (tea.Program.Run
// never returns on EOF, C-83), so the wiring that makes the TUI sync is pinned
// at the source: it refuses a collision, pulls BEFORE the engine opens, opens
// the engine through the hook helper, and defers the exit flush — deferred, so
// it runs after the program has put the terminal back.
func TestTUIWiresAutoSync(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "cmd_tui.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fn *ast.FuncDecl
	for _, d := range file.Decls {
		if f, ok := d.(*ast.FuncDecl); ok && f.Name.Name == "cmdTUI" {
			fn = f
		}
	}
	if fn == nil {
		t.Fatal("cmdTUI not found")
	}
	pos := map[string]token.Pos{}
	deferred := map[string]token.Pos{}
	name := func(call *ast.CallExpr) string {
		switch f := call.Fun.(type) {
		case *ast.Ident:
			return f.Name
		case *ast.SelectorExpr:
			return f.Sel.Name
		}
		return ""
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.DeferStmt:
			deferred[name(x.Call)] = x.Pos()
		case *ast.CallExpr:
			if _, seen := pos[name(x)]; !seen {
				pos[name(x)] = x.Pos()
			}
		}
		return true
	})
	for _, want := range []string{"writableVaultRoot", "loadTUIAutoSync", "pull", "openVaultEngine"} {
		if _, ok := pos[want]; !ok {
			t.Errorf("cmdTUI never calls %s", want)
		}
	}
	if pos["pull"] > pos["openVaultEngine"] {
		t.Error("cmdTUI opens the engine before the auto-pull: the engine would open on the old vault")
	}
	fin, kill := deferred["finish"], deferred["Kill"]
	if fin == 0 {
		t.Fatal("cmdTUI does not defer the auto-sync's finish: the exit flush would not run")
	}
	if kill == 0 {
		t.Fatal("cmdTUI no longer defers p.Kill; the ordering this test relies on has changed")
	}
	// Deferred calls run last-in first-out: finish, deferred earlier, runs after
	// Kill has restored the terminal.
	if fin > kill {
		t.Error("cmdTUI defers finish after p.Kill, so the exit line would print before the terminal is restored")
	}
}
