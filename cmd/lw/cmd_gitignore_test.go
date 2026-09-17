package main

// cmd_gitignore_test.go pins workflow 005 contract §7: `lw init` writes a
// vault .gitignore carrying .llmwiki/ (appending one line to an existing
// file, never rewriting it), `lw doctor` reports a vault whose git tracks
// .llmwiki/ as a warning that still exits 0, and neither ever modifies the
// user's repository — lw reports; the user decides.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

func TestInitWritesGitignore(t *testing.T) {
	t.Run("fresh_vault_gets_llmwiki_ignored", func(t *testing.T) {
		dir := t.TempDir()
		var out strings.Builder
		runInitIn(t, dir, initOpts{args: []string{"--schema", "ml-systems"}, out: &out})

		b, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
		if err != nil {
			t.Fatalf("read .gitignore: %v", err)
		}
		var seen bool
		for _, line := range strings.Split(string(b), "\n") {
			if line == ".llmwiki/" {
				seen = true
			}
		}
		if !seen {
			t.Errorf(".gitignore = %q, want a line exactly \".llmwiki/\"", b)
		}
		if !strings.Contains(out.String(), ".gitignore") {
			t.Errorf("init report does not mention .gitignore:\n%s", out.String())
		}
	})

	t.Run("existing_gitignore_gains_one_line", func(t *testing.T) {
		// The original bytes must survive as a PREFIX of the result, and the
		// only added bytes are the entry line (plus the newline that
		// completes the previous line when the file lacked one). Nothing is
		// reordered or rewritten.
		cases := []struct{ name, before, add string }{
			{"trailing newline", "# vault ignores\nnode_modules/\n*.log\n", ".llmwiki/\n"},
			{"no trailing newline", "# vault ignores\nnode_modules/", "\n.llmwiki/\n"},
			{"the word inside another path", "sub/.llmwiki/\nnode_modules/\n", ".llmwiki/\n"},
		}
		for _, tc := range cases {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(tc.before), 0o644); err != nil {
				t.Fatalf("write .gitignore: %v", err)
			}
			runInitIn(t, dir, initOpts{args: []string{"--force", "--schema", "ml-systems"}, out: io.Discard})

			after, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
			if err != nil {
				t.Fatalf("read .gitignore: %v", err)
			}
			if !bytes.HasPrefix(after, []byte(tc.before)) {
				t.Errorf("%s: original bytes were not preserved as a prefix:\nbefore %q\nafter  %q", tc.name, tc.before, after)
			}
			if got := string(after[len(tc.before):]); got != tc.add {
				t.Errorf("%s: appended %q, want exactly %q", tc.name, got, tc.add)
			}
		}
	})

	t.Run("existing_gitignore_with_entry_is_untouched", func(t *testing.T) {
		for _, entry := range []string{".llmwiki/", "/.llmwiki/", ".llmwiki"} {
			dir := t.TempDir()
			content := "# kept\n" + entry + "\nbuild/\n"
			if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(content), 0o644); err != nil {
				t.Fatalf("write .gitignore: %v", err)
			}
			runInitIn(t, dir, initOpts{args: []string{"--force", "--schema", "ml-systems"}, out: io.Discard})

			after, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
			if err != nil {
				t.Fatalf("read .gitignore: %v", err)
			}
			if string(after) != content {
				t.Errorf("entry %q: an already-covered .gitignore was modified:\nbefore %q\nafter  %q", entry, content, after)
			}
		}
	})

	t.Run("init_is_idempotent", func(t *testing.T) {
		dir := t.TempDir()
		runInitIn(t, dir, initOpts{args: []string{"--schema", "ml-systems"}, out: io.Discard})
		first, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
		if err != nil {
			t.Fatalf("read .gitignore: %v", err)
		}

		runInitIn(t, dir, initOpts{args: []string{"--force", "--schema", "ml-systems"}, out: io.Discard})
		second, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
		if err != nil {
			t.Fatalf("re-read .gitignore: %v", err)
		}
		if string(first) != string(second) {
			t.Errorf("a second init changed .gitignore:\nfirst  %q\nsecond %q", first, second)
		}
	})
}

// newGitVault copies the minimal fixture and turns it into a git repository
// with a deterministic local identity, returning the root and a runner for
// git commands inside it. It skips when git is not installed: the
// tracked-.llmwiki checks need a real repository, and faking git would
// prove nothing.
func newGitVault(t *testing.T) (string, func(...string)) {
	t.Helper()
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not on PATH; the tracked-.llmwiki doctor checks need a real repository")
	}
	root := testutil.CopyFixture(t, "minimal")
	empty := filepath.Join(t.TempDir(), "empty-gitconfig")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatalf("write empty git config: %v", err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL="+empty,
			"GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_TERMINAL_PROMPT=0",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git("init")
	git("config", "user.email", "lw-test@example.invalid")
	git("config", "user.name", "lw test")
	return root, git
}

// trackLlmwiki stages a file under .llmwiki/, producing the pre-existing
// repository state the doctor check exists to report — the state a user is
// in before lw ever runs doctor. The staged file is .llmwiki/journal.ndjson
// deliberately: a changesets/open/<id>/ dir holding only session.ndjson is
// itself a fault doctor reports (the C-118 shape), and these tests are
// about the git check, not about that fault. The engine is opened first so
// the vault's doctor state is otherwise healthy — the tracked-.llmwiki
// tests must show that the WARNING is what the user sees, with lw doctor
// still exiting 0.
func trackLlmwiki(t *testing.T, root string, git func(...string)) {
	t.Helper()
	openEngine(t, root)
	journal := filepath.Join(root, stateDirName, journalFileName)
	if err := os.WriteFile(journal, nil, 0o644); err != nil {
		t.Fatalf("write %s: %v", journal, err)
	}
	git("add", stateDirName)
}

// hashTree fingerprints every file under root — path plus content hash — so
// never_modifies_the_repo can prove lw doctor left .git byte-identical.
// This is the mechanical form of contract §7's hard promise.
func hashTree(t *testing.T, root string) string {
	t.Helper()
	type entry struct{ path, sum string }
	var entries []entry
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		entries = append(entries, entry{filepath.ToSlash(rel), hex.EncodeToString(sum[:])})
		return nil
	})
	if err != nil {
		t.Fatalf("hash %s: %v", root, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "%s %s\n", e.path, e.sum)
	}
	return b.String()
}

func TestDoctorTrackedLlmwiki(t *testing.T) {
	doctorTestEnv(t)

	t.Run("never_modifies_the_repo", func(t *testing.T) {
		root, git := newGitVault(t)
		trackLlmwiki(t, root, git)

		before := hashTree(t, filepath.Join(root, ".git"))
		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"doctor", "--vault", root})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		if after := hashTree(t, filepath.Join(root, ".git")); after != before {
			t.Errorf("lw doctor changed .git:\nbefore:\n%s\nafter:\n%s", before, after)
		}
	})

	t.Run("non_git_vault_is_skipped", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		rep := runDoctor(context.Background(), root, doctorOptions{})
		c := checkByName(t, rep, "git")
		if !c.OK || !c.Skipped || c.Warn {
			t.Fatalf("git check = %+v, want a skipped, passing, non-warning check", c)
		}
		if !strings.Contains(strings.ToLower(c.Detail), "skip") {
			t.Errorf("detail %q does not say it was skipped", c.Detail)
		}
	})

	t.Run("clean_vault_is_silent", func(t *testing.T) {
		root, git := newGitVault(t)
		git("add", "SCHEMA.md")
		rep := runDoctor(context.Background(), root, doctorOptions{})
		c := checkByName(t, rep, "git")
		if !c.OK || c.Warn || c.Skipped {
			t.Fatalf("git check = %+v, want a passing, non-warning check", c)
		}
		if c.Remedy != "" {
			t.Errorf("passing check carries a remedy: %+v", c)
		}
	})

	t.Run("tracked_llmwiki_warns_with_the_command", func(t *testing.T) {
		root, git := newGitVault(t)
		trackLlmwiki(t, root, git)

		rep := runDoctor(context.Background(), root, doctorOptions{})
		c := checkByName(t, rep, "git")
		if !c.OK || !c.Warn || c.Skipped {
			t.Fatalf("git check = %+v, want OK with Warn and no skip", c)
		}
		if rep.failed() {
			t.Errorf("a warning must not fail the report — the exit code contract: %+v", rep.Checks)
		}
		for _, part := range []string{"git rm", "--cached", stateDirName, "history"} {
			if !strings.Contains(c.Remedy, part) {
				t.Errorf("remedy %q does not name %q", c.Remedy, part)
			}
		}

		// The full command path agrees: the warning is visible and exits 0.
		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"doctor", "--vault", root})
		})
		if code != 0 {
			t.Fatalf("lw doctor exit code = %d, want 0 for a warned vault; stderr=%q", code, stderr)
		}
		if !strings.Contains(stdout, "! git") {
			t.Errorf("lw doctor stdout does not mark the git warning:\n%s", stdout)
		}
		if !strings.Contains(stdout, c.Remedy) {
			t.Errorf("lw doctor stdout does not print the remedy:\n%s", stdout)
		}
	})
}
