package main

// nosync_test.go pins the promise 042 makes to every user who never configures
// a remote: without [sync] remotes, no verb behaves differently — not one more
// byte on stdout or stderr, not one more file in the vault. The oracle is the
// pre-042 binary (v2.38.0, 2d4ca72): testdata/nosync-transcript.golden is what
// it printed for the scenario below, and the test replays the scenario against
// this build under several configs that all mean "no sync".
//
// Regenerate the golden — only ever from the old binary:
//
//	git archive v2.38.0 | tar -x -C /tmp/lw-base && (cd /tmp/lw-base && go build -o /tmp/lw-base/lw ./cmd/lw)
//	LW_BASE_BIN=/tmp/lw-base/lw go test ./cmd/lw -run TestNoSyncConfigByteIdentical -update
//
// With LW_BASE_BIN set and no -update the test is the base-vs-head
// differential itself: the old binary and this build run the same scenario and
// their transcripts must be equal.

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// nosyncRunner runs one lw invocation and returns what it printed.
type nosyncRunner func(t *testing.T, args []string) (stdout, stderr string, code int)

// inProcess runs lw in this process.
func inProcess(t *testing.T, args []string) (string, string, int) {
	return captureRun(t, func() int { return run(args) })
}

// asBinary runs the lw binary at bin in a clean working directory.
func asBinary(bin string) nosyncRunner {
	return func(t *testing.T, args []string) (string, string, int) {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Dir = t.TempDir()
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		code := 0
		if err := cmd.Run(); err != nil {
			ee, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("run %s %v: %v", bin, args, err)
			}
			code = ee.ExitCode()
		}
		return out.String(), errb.String(), code
	}
}

var (
	nosyncStamp = regexp.MustCompile(`\b\d{8}-\d{6}\b`)
	nosyncCS    = regexp.MustCompile(`\bcs-[0-9a-f]{16}\b`)
	nosyncObj   = regexp.MustCompile(`objects/[0-9a-f]{2}/[0-9a-f]{40,}`)
)

// nosyncScenario replays the scenario against a fresh copy of the minimal
// vault with runner and returns the transcript: every command with its stdout,
// stderr and exit code, then the vault's file list.
func nosyncScenario(t *testing.T, runner nosyncRunner) string {
	t.Helper()
	root := testutil.CopyFixture(t, "minimal")
	src := filepath.Join(t.TempDir(), "src.md")
	if err := os.WriteFile(src, []byte("# A Local Note\n\nSome body text for the dry run.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ops := filepath.Join(testutil.FixtureRoot(t), "ops", "create-page.json")

	norm := func(s string) string {
		s = strings.ReplaceAll(s, root, "<vault>")
		s = strings.ReplaceAll(s, src, "<src>")
		s = strings.ReplaceAll(s, ops, "<ops>")
		s = nosyncStamp.ReplaceAllString(s, "<stamp>")
		s = nosyncCS.ReplaceAllString(s, "<cs>")
		return nosyncObj.ReplaceAllString(s, "objects/<xx>/<sha>")
	}

	steps := [][]string{
		{"status"},
		{"note", "-m", "hello"},
		{"stage", "--from", ops},
		{"status"},
		{"commit", "-m", "first page"},
		{"status"},
		{"ingest", "--dry-run", src},
	}
	var b strings.Builder
	for _, step := range steps {
		args := append(append([]string{}, step...), "--vault", root)
		stdout, stderr, code := runner(t, args)
		fmt.Fprintf(&b, "$ lw %s\n[stdout]\n%s[stderr]\n%s[exit %d]\n\n", norm(strings.Join(step, " ")), norm(stdout), norm(stderr), code)
	}

	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		files = append(files, norm(filepath.ToSlash(rel)))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	b.WriteString("[files]\n" + strings.Join(files, "\n") + "\n")
	return b.String()
}

// TestNoSyncConfigByteIdentical: without [sync] remotes, note, commit,
// ingest --dry-run and status print what the pre-042 binary printed and leave
// the vault with the files it left — under every config that means "no sync".
func TestNoSyncConfigByteIdentical(t *testing.T) {
	golden := filepath.Join("testdata", "nosync-transcript.golden")

	if bin := os.Getenv("LW_BASE_BIN"); bin != "" {
		syncHermetic(t)
		base := nosyncScenario(t, asBinary(bin))
		if testutil.UpdateEnabled() {
			testutil.GoldenString(t, golden, base)
			return
		}
		syncHermetic(t)
		if head := nosyncScenario(t, inProcess); head != base {
			t.Fatalf("this build differs from the pre-042 binary %s:\n--- base\n%s\n--- head\n%s", bin, base, head)
		}
	} else if testutil.UpdateEnabled() {
		t.Fatal("the golden is the pre-042 binary's output and is regenerated only from it: set LW_BASE_BIN")
	}

	configs := []struct{ name, body string }{
		{"no config file", ""},
		{"a config naming other keys", "theme = \"nord\"\n[llm]\nmodel = \"custom\"\n"},
		{"auto = true but no remotes", "[sync]\nauto = true\n"},
		{"an empty remotes list", "[sync]\nremotes = []\n"},
		{"a [vault] path alone", "[vault]\npath = \"/somewhere\"\n"},
	}
	for _, c := range configs {
		t.Run(c.name, func(t *testing.T) {
			syncHermetic(t)
			pc := newSyncPC(t, "a")
			pc.setConfig(c.body)
			pc.act()
			got := nosyncScenario(t, inProcess)
			testutil.GoldenString(t, golden, got)
		})
	}
}
