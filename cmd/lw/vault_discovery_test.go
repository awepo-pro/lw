package main

// vault_discovery_test.go pins 042 D1's discovery order and D5/A-042-2's
// format pre-check: which vault a verb works on, and that a vault written by a
// newer lw is refused by EVERY verb that touches it, not just the ones that
// happen to open the engine.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// discoveryVault makes a directory holding a SCHEMA.md and returns its path.
func discoveryVault(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	putFile(t, dir, "SCHEMA.md", testSchema)
	return dir
}

// noVaultDir is a working directory with no SCHEMA.md above it.
func noVaultDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// TestFindVaultRootOrder: --vault > $LW_VAULT > the nearest ancestor of the
// working directory with a SCHEMA.md > [vault] path > today's error text. A
// [vault] path with no SCHEMA.md has its own exact text.
func TestFindVaultRootOrder(t *testing.T) {
	home := syncHermetic(t)
	pc := newSyncPC(t, "a")
	pc.act()

	explicit := discoveryVault(t, "explicit")
	fromEnv := discoveryVault(t, "from-env")
	ancestor := discoveryVault(t, "ancestor")
	configured := discoveryVault(t, "configured")
	deep := filepath.Join(ancestor, "wiki", "concepts")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	other := noVaultDir(t)
	pc.setConfig("[vault]\npath = " + `"` + configured + `"` + "\n")

	chdir(t, deep)
	t.Setenv("LW_VAULT", fromEnv)

	t.Run("--vault beats everything", func(t *testing.T) {
		got, err := findVaultRoot(explicit)
		if err != nil || got != explicit {
			t.Fatalf("findVaultRoot(--vault) = %q, %v; want %q", got, err, explicit)
		}
	})
	t.Run("$LW_VAULT beats the working directory and the config", func(t *testing.T) {
		got, err := findVaultRoot("")
		if err != nil || got != fromEnv {
			t.Fatalf("findVaultRoot() = %q, %v; want $LW_VAULT %q", got, err, fromEnv)
		}
	})
	t.Run("the nearest ancestor beats the config", func(t *testing.T) {
		t.Setenv("LW_VAULT", "")
		got, err := findVaultRoot("")
		if err != nil || got != ancestor {
			t.Fatalf("findVaultRoot() = %q, %v; want the ancestor %q", got, err, ancestor)
		}
	})
	t.Run("[vault] path is used when nothing more specific names a vault", func(t *testing.T) {
		t.Setenv("LW_VAULT", "")
		chdir(t, other)
		got, err := findVaultRoot("")
		if err != nil || got != configured {
			t.Fatalf("findVaultRoot() = %q, %v; want the configured %q", got, err, configured)
		}
	})
	t.Run("~ in [vault] path expands", func(t *testing.T) {
		t.Setenv("LW_VAULT", "")
		chdir(t, other)
		putFile(t, home, "tilde-vault/SCHEMA.md", testSchema)
		pc.setConfig("[vault]\npath = \"~/tilde-vault\"\n")
		got, err := findVaultRoot("")
		if want := filepath.Join(home, "tilde-vault"); err != nil || got != want {
			t.Fatalf("findVaultRoot() = %q, %v; want %q", got, err, want)
		}
		pc.setConfig("[vault]\npath = " + `"` + configured + `"` + "\n")
	})
	t.Run("a [vault] path with no SCHEMA.md says so", func(t *testing.T) {
		t.Setenv("LW_VAULT", "")
		chdir(t, other)
		bad := filepath.Join(t.TempDir(), "not-a-vault")
		if err := os.MkdirAll(bad, 0o755); err != nil {
			t.Fatal(err)
		}
		pc.setConfig("[vault]\npath = " + `"` + bad + `"` + "\n")
		defer pc.setConfig("[vault]\npath = " + `"` + configured + `"` + "\n")
		_, err := findVaultRoot("")
		if want := "[vault] path " + bad + ": no SCHEMA.md there"; err == nil || err.Error() != want {
			t.Fatalf("error = %v, want %q", err, want)
		}
	})
	t.Run("today's error text when nothing names a vault", func(t *testing.T) {
		t.Setenv("LW_VAULT", "")
		chdir(t, other)
		pc.setConfig("")
		_, err := findVaultRoot("")
		wd, _ := os.Getwd()
		if want := "no SCHEMA.md found in " + wd + " or any parent directory; pass --vault"; err == nil || err.Error() != want {
			t.Fatalf("error = %v, want %q", err, want)
		}
		// A config that names no [vault] table behaves the same.
		pc.setConfig("theme = \"nord\"\n")
		if _, err := findVaultRoot(""); err == nil || err.Error() != "no SCHEMA.md found in "+wd+" or any parent directory; pass --vault" {
			t.Fatalf("error with an unrelated config = %v", err)
		}
	})
	t.Run("a malformed config does not hide today's error", func(t *testing.T) {
		t.Setenv("LW_VAULT", "")
		chdir(t, other)
		pc.setConfig("not = [valid toml")
		_, err := findVaultRoot("")
		if err == nil || !strings.HasPrefix(err.Error(), "no SCHEMA.md found in ") {
			t.Fatalf("error = %v, want today's no-SCHEMA.md text", err)
		}
	})
	t.Run("the host:path refusal applies to --vault and $LW_VAULT", func(t *testing.T) {
		chdir(t, other)
		want := `--vault "home:~/ai-vault" looks like host:path, but lw has no remote vaults; run lw on that host instead: ssh home -t lw tui --vault ~/ai-vault`
		if _, err := findVaultRoot("home:~/ai-vault"); err == nil || err.Error() != want {
			t.Fatalf("--vault error = %v, want %q", err, want)
		}
		t.Setenv("LW_VAULT", "home:~/ai-vault")
		if _, err := findVaultRoot(""); err == nil || err.Error() != want {
			t.Fatalf("$LW_VAULT error = %v, want the same text %q", err, want)
		}
	})
	t.Run("an explicit --vault is used as given, even without a SCHEMA.md", func(t *testing.T) {
		chdir(t, other)
		bare := filepath.Join(t.TempDir(), "bare")
		if got, err := findVaultRoot(bare); err != nil || got != bare {
			t.Fatalf("findVaultRoot(%q) = %q, %v", bare, got, err)
		}
	})
}

// formatTooNewVault copies the minimal fixture and gives it a format file
// naming a version newer than this lw.
func formatTooNewVault(t *testing.T, version string) string {
	t.Helper()
	root := testutil.CopyFixture(t, "minimal")
	putFile(t, root, ".llmwiki/format", `{"version": `+version+`}`+"\n")
	return root
}

// TestEngineFormatTooNewFromCLI: a vault with format 2 fails EVERY verb that
// touches the vault with the FormatError text and exit 1 — status, lint and
// doctor open the vault before the engine, status turns an engine error into
// a status line, and note, session and trace never open the engine at all —
// and not one file under the vault changes.
func TestEngineFormatTooNewFromCLI(t *testing.T) {
	syncHermetic(t)
	const text = "vault format 2 is newer than this lw supports (1): upgrade lw"

	// lw stage reads its ops file before it looks for the vault, so the
	// refusal is only reachable with one that parses.
	opsFile := filepath.Join(t.TempDir(), "ops.json")
	if err := os.WriteFile(opsFile, []byte("[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// An absolute remote, so a verb that wrongly got past the refusal cannot
	// create a repository under the working directory.
	initRemote := filepath.Join(syncTemp(t), "init-remote")

	verbs := []struct {
		name string
		args func(root string) []string
	}{
		{"status", func(r string) []string { return []string{"status", "--vault", r} }},
		{"note", func(r string) []string { return []string{"note", "-m", "hello", "--vault", r} }},
		{"note", func(r string) []string { return []string{"note", "list", "--vault", r} }},
		{"lint", func(r string) []string { return []string{"lint", "--vault", r} }},
		{"lint", func(r string) []string { return []string{"lint", "--fix", "--vault", r} }},
		{"doctor", func(r string) []string { return []string{"doctor", "--vault", r} }},
		{"log", func(r string) []string { return []string{"log", "--vault", r} }},
		{"diff", func(r string) []string { return []string{"diff", "--vault", r} }},
		{"session", func(r string) []string { return []string{"session", "list", "--vault", r} }},
		{"session", func(r string) []string { return []string{"session", "show", "--vault", r} }},
		{"trace", func(r string) []string { return []string{"trace", "--vault", r} }},
		{"trace", func(r string) []string { return []string{"trace", "show", "--vault", r} }},
		{"ingest", func(r string) []string { return []string{"ingest", "--vault", r, "somewhere.md"} }},
		{"commit", func(r string) []string { return []string{"commit", "--vault", r, "-m", "x"} }},
		{"revert", func(r string) []string { return []string{"revert", "--vault", r, "000001"} }},
		{"query", func(r string) []string { return []string{"query", "--vault", r, "what is a kv cache"} }},
		{"tui", func(r string) []string { return []string{"tui", "--vault", r} }},
		{"mcp", func(r string) []string { return []string{"mcp", "--vault", r} }},
		{"stage", func(r string) []string { return []string{"stage", "--vault", r, "--from", opsFile} }},
		{"sync", func(r string) []string { return []string{"sync", "--vault", r} }},
		{"sync", func(r string) []string { return []string{"sync", "status", "--vault", r} }},
		{"sync", func(r string) []string { return []string{"sync", "init", initRemote, "--vault", r} }},
	}
	for _, v := range verbs {
		args := v.args("")
		t.Run(strings.Join(args[:min(len(args), 2)], "_"), func(t *testing.T) {
			root := formatTooNewVault(t, "2")
			before := treeDigest(t, root)
			stdout, stderr, code := captureRun(t, func() int { return run(v.args(root)) })
			if code != 1 {
				t.Fatalf("exit code = %d, want 1; stdout=%q stderr=%q", code, stdout, stderr)
			}
			if want := "lw: " + v.name + ": " + text + "\n"; stderr != want {
				t.Errorf("stderr = %q, want %q", stderr, want)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want nothing", stdout)
			}
			if after := treeDigest(t, root); after != before {
				t.Error("the refusal changed files under the vault")
			}
		})
	}

	t.Run("a bare lw is the tui", func(t *testing.T) {
		root := formatTooNewVault(t, "2")
		chdir(t, root)
		_, stderr, code := captureRun(t, func() int { return run(nil) })
		if want := "lw: tui: " + text + "\n"; code != 1 || stderr != want {
			t.Errorf("exit %d stderr %q, want 1 and %q", code, stderr, want)
		}
	})

	t.Run("a version above 2 names that version", func(t *testing.T) {
		root := formatTooNewVault(t, "7")
		_, stderr, code := captureRun(t, func() int { return run([]string{"status", "--vault", root}) })
		if want := "lw: status: vault format 7 is newer than this lw supports (1): upgrade lw\n"; code != 1 || stderr != want {
			t.Errorf("exit %d stderr %q, want 1 and %q", code, stderr, want)
		}
	})

	t.Run("a malformed format file is refused, not guessed at", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		putFile(t, root, ".llmwiki/format", "not json\n")
		_, stderr, code := captureRun(t, func() int { return run([]string{"status", "--vault", root}) })
		if code != 1 || !strings.HasPrefix(stderr, "lw: status: read .llmwiki/format: ") {
			t.Errorf("exit %d stderr %q, want 1 and a read .llmwiki/format error", code, stderr)
		}
	})

	t.Run("format 1 and no format file open as before", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		if _, stderr, code := captureRun(t, func() int { return run([]string{"status", "--vault", root}) }); code != 0 {
			t.Fatalf("no format file: exit %d stderr %q", code, stderr)
		}
		putFile(t, root, ".llmwiki/format", `{"version": 1}`+"\n")
		if _, stderr, code := captureRun(t, func() int { return run([]string{"status", "--vault", root}) }); code != 0 {
			t.Fatalf("format 1: exit %d stderr %q", code, stderr)
		}
	})
}
