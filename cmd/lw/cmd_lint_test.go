package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

// captureRun invokes fn (typically a call to run) with os.Stdout and
// os.Stderr redirected to pipes, and returns everything written to each.
// Testing run(args) in-process — rather than shelling out to a built
// binary — is what lets these tests assert on the exit code directly.
func captureRun(t *testing.T, fn func() int) (stdout, stderr string, code int) {
	t.Helper()

	origOut, origErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout, os.Stderr = outW, errW
	t.Cleanup(func() {
		os.Stdout, os.Stderr = origOut, origErr
	})

	outCh := make(chan string, 1)
	errCh := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(outR)
		outCh <- string(b)
	}()
	go func() {
		b, _ := io.ReadAll(errR)
		errCh <- string(b)
	}()

	code = fn()

	outW.Close()
	errW.Close()
	stdout = <-outCh
	stderr = <-errCh
	os.Stdout, os.Stderr = origOut, origErr
	return stdout, stderr, code
}

// chdir changes the process working directory to dir and restores the
// original directory when the test ends, however it ends.
func chdir(t *testing.T, dir string) {
	t.Helper()

	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatalf("restore chdir %s: %v", orig, err)
		}
	})
}

func TestCmdLintMinimalIsClean(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"lint", "--vault", root})
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if stdout != "clean\n" {
		t.Fatalf("stdout = %q, want %q", stdout, "clean\n")
	}
}

func TestCmdLintDirtyGolden(t *testing.T) {
	root := testutil.CopyFixture(t, "dirty")

	stdout, _, code := captureRun(t, func() int {
		return run([]string{"lint", "--vault", root})
	})

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}

	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 17 {
		t.Fatalf("got %d lines, want 17 (16 findings + summary):\n%s", len(lines), stdout)
	}

	wantFirst := "index.md:0: error: wiki/concepts/thin-links.md has no line in index.md; add one (index-sync)"
	if lines[0] != wantFirst {
		t.Errorf("first line = %q, want %q", lines[0], wantFirst)
	}

	wantSummary := "7 errors, 6 warnings, 3 info"
	if lines[len(lines)-1] != wantSummary {
		t.Errorf("summary line = %q, want %q", lines[len(lines)-1], wantSummary)
	}
}

func TestCmdLintJSON(t *testing.T) {
	root := testutil.CopyFixture(t, "dirty")

	stdout, _, code := captureRun(t, func() int {
		return run([]string{"lint", "--vault", root, "--json"})
	})

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}

	var report struct {
		Findings []lint.Finding
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("invalid JSON: %v\noutput:\n%s", err, stdout)
	}
	if len(report.Findings) != 16 {
		t.Fatalf("Findings count = %d, want 16", len(report.Findings))
	}
}

func TestCmdLintChecksFilter(t *testing.T) {
	root := testutil.CopyFixture(t, "dirty")

	stdout, _, code := captureRun(t, func() int {
		return run([]string{"lint", "--vault", root, "--checks", "link-broken"})
	})

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}

	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2 (1 finding + summary):\n%s", len(lines), stdout)
	}
	if !strings.Contains(lines[0], "(link-broken)") {
		t.Errorf("finding line = %q, want it to name check link-broken", lines[0])
	}
	if lines[1] != "1 errors, 0 warnings, 0 info" {
		t.Errorf("summary line = %q, want %q", lines[1], "1 errors, 0 warnings, 0 info")
	}
}

// TestCmdLintWarningsOnlyDoesNotFail proves that warnings and info alone do
// not fail the build (backbone §13's exit-code contract) — otherwise
// size-split would block every commit. --checks restricts dirty to only
// warn-severity checks, none of which is error-severity.
func TestCmdLintWarningsOnlyDoesNotFail(t *testing.T) {
	root := testutil.CopyFixture(t, "dirty")

	warnChecks := "fm-dates,path-convention,link-min-out,link-orphan,src-provenance,src-stale"
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"lint", "--vault", root, "--checks", warnChecks})
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 for warnings-only; stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "warn:") {
		t.Fatalf("stdout has no warn findings, test is not exercising anything: %q", stdout)
	}
}

func TestCmdLintBadFlag(t *testing.T) {
	_, stderr, code := captureRun(t, func() int {
		return run([]string{"lint", "--bogusflag"})
	})

	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%q", code, stderr)
	}
}

// TestCmdLintFixCleanVaultNeedsNoAgent proves --fix short-circuits before
// ever constructing an agent when there is nothing to repair: newAgent is
// swapped for a function that fails the test if called at all.
func TestCmdLintFixCleanVaultNeedsNoAgent(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	withFakeAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
		t.Fatal("newAgent should never be called for a clean vault")
		return nil, nil
	})

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"lint", "--fix", "--vault", root})
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if stdout != "clean\n" {
		t.Fatalf("stdout = %q, want %q", stdout, "clean\n")
	}

	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()
	if _, err := e.Current(); err == nil {
		t.Fatal("a changeset was opened for a clean vault")
	}
}

// TestCmdLintFixStagesRepairsForFindings drives --fix over the "dirty"
// fixture (which lint.Run reports real findings for) with a fake agent
// that proposes one repair op, and requires the result to be exactly one
// open changeset holding that op — the same "still stages" contract
// `lw ingest` has (/docs/design.md §9.4): nothing here commits.
func TestCmdLintFixStagesRepairsForFindings(t *testing.T) {
	root := testutil.CopyFixture(t, "dirty")

	var sawFindings bool
	withFakeAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
		sawFindings = true
		return &fakeStageAgent{
			e:        e,
			sessions: sessions,
			ops: []stage.Op{
				{
					Kind:      stage.OpIngestSource,
					Path:      "raw/articles/lint-fix-repair.md",
					Content:   []byte("a repair, staged by the fake agent\n"),
					Extractor: "test-fake",
				},
			},
		}, nil
	})

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"lint", "--fix", "--vault", root})
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	if !sawFindings {
		t.Fatal("newAgent was never called even though the dirty fixture has findings")
	}
	if !strings.Contains(stdout, "opened changeset cs-") {
		t.Fatalf("stdout = %q, want it to mention an opened changeset", stdout)
	}

	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()
	cs, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if len(cs.Ops) != 1 {
		t.Fatalf("len(cs.Ops) = %d, want 1", len(cs.Ops))
	}
	if cs.Ops[0].Path != "raw/articles/lint-fix-repair.md" {
		t.Errorf("cs.Ops[0].Path = %q, want %q", cs.Ops[0].Path, "raw/articles/lint-fix-repair.md")
	}
}

// TestCmdLintVaultAutoDiscovery proves that with no --vault, run walks up
// from the current directory to find the nearest ancestor holding
// SCHEMA.md.
func TestCmdLintVaultAutoDiscovery(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	nested := filepath.Join(root, "wiki", "concepts")
	chdir(t, nested)

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"lint"})
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if stdout != "clean\n" {
		t.Fatalf("stdout = %q, want %q", stdout, "clean\n")
	}
}
