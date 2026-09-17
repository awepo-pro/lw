package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// testAPIKey is the fake key the doctor tests resolve. It must never appear
// in any output; two tests assert exactly that (the gotcha-2 leak check).
const testAPIKey = "lw-test-key-not-a-real-secret"

// testKeyEnv is the variable config.Default() references — the one a
// default install resolves — and the one doctorTestEnv sets, so the tests
// are hermetic even where a real key is exported.
const testKeyEnv = "DEEPSEEK_API_KEY"

// doctorTestEnv makes a doctor run hermetic: the config is read from an
// empty XDG_CONFIG_HOME (so config.Load returns Default and resolves
// llm.api_key from testKeyEnv), the key itself is set to testAPIKey, and
// probeProvider is swapped for a fake reporting a healthy, tool-calling
// provider — so no test touches the network no matter what the real
// environment carries.
func doctorTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg-config"))
	t.Setenv(testKeyEnv, testAPIKey)
	withProbe(t, healthyProbe)
}

// healthyProbe is the fake every "all clear" case runs behind: reachable,
// and one that returns the tool call the probe exists to verify.
func healthyProbe(ctx context.Context, cfg *config.Config) llm.ProbeResult {
	return llm.ProbeResult{Reachable: true, Model: cfg.LLM.Model, ToolCalling: true, Latency: 1}
}

// withProbe swaps the package-level probeProvider seam for fn, restoring the
// original on cleanup — the same shape as cmd_ingest_test.go's withFakeAgent.
func withProbe(t *testing.T, fn func(ctx context.Context, cfg *config.Config) llm.ProbeResult) {
	t.Helper()
	orig := probeProvider
	probeProvider = fn
	t.Cleanup(func() { probeProvider = orig })
}

// openEngine opens the staging engine for root and registers its close, for
// tests that build on-disk state before checking it.
func openEngine(t *testing.T, root string) *stage.Engine {
	t.Helper()
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("open engine: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

// openVaultOnly opens the vault without the staging engine. checkIndex must
// be called through this, not openEngine: opening the engine rebuilds the
// index cache as a side effect and would silently repair the fault under
// test.
func openVaultOnly(t *testing.T, root string) *vault.Vault {
	t.Helper()
	v, err := vault.Open(root)
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	return v
}

// checkByName returns the named check from a report, failing if it is absent.
func checkByName(t *testing.T, rep doctorReport, name string) doctorCheck {
	t.Helper()
	for _, c := range rep.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %q check in %+v", name, rep.Checks)
	return doctorCheck{}
}

// wantFailed asserts that c failed and names a remedy containing every
// remedyParts entry — gotcha 4: a failure with no remedy is the failure mode
// this verb exists to prevent.
func wantFailed(t *testing.T, c doctorCheck, remedyParts ...string) {
	t.Helper()
	if c.OK {
		t.Fatalf("%s check reported OK, want a failure: %+v", c.Name, c)
	}
	if c.Skipped {
		t.Errorf("%s check was skipped; want a reported failure: %+v", c.Name, c)
	}
	if c.Remedy == "" {
		t.Errorf("%s check failed with no remedy: %+v", c.Name, c)
	}
	for _, part := range remedyParts {
		if !strings.Contains(c.Remedy, part) {
			t.Errorf("%s remedy %q does not name the fix %q", c.Name, c.Remedy, part)
		}
	}
}

// fixedTS is the single timestamp every crafted event carries, so the state
// the doctor tests build is deterministic.
func fixedTS() time.Time { return time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC) }

func TestDoctorHealthyVaultAllClear(t *testing.T) {
	doctorTestEnv(t)
	root := testutil.CopyFixture(t, "minimal")

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"doctor", "--vault", root})
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	for _, name := range []string{"index", "objects", "journal", "recovery", "lock", "config", "provider"} {
		if !strings.Contains(stdout, "✓ "+name) {
			t.Errorf("stdout does not report a passing %s check:\n%s", name, stdout)
		}
	}
	if strings.Contains(stdout, "✗") {
		t.Errorf("stdout reports a failure on a healthy vault:\n%s", stdout)
	}
	if !strings.Contains(stdout, root) {
		t.Errorf("stdout %q does not name the vault %q", stdout, root)
	}
}

func TestDoctorFreshVaultHasNoStateYet(t *testing.T) {
	doctorTestEnv(t)
	root := testutil.CopyFixture(t, "minimal")

	rep := runDoctor(context.Background(), root, doctorOptions{})

	if rep.failed() {
		t.Fatalf("fresh vault reported failures: %+v", rep.Checks)
	}
	c := checkByName(t, rep, "index")
	if !strings.Contains(c.Detail, "no "+stateRel+" yet") {
		t.Errorf("index detail = %q, want it to say there is no state yet", c.Detail)
	}
}

func TestDoctorFaultsAreDetectedWithRemedies(t *testing.T) {
	doctorTestEnv(t)
	deadPID := deadProcessPID(t)

	t.Run("missing index", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		openEngine(t, root)
		if err := os.Remove(filepath.Join(root, stateDirName, indexFileName)); err != nil {
			t.Fatalf("remove index: %v", err)
		}
		c := checkIndex(root, openVaultOnly(t, root))
		if !strings.Contains(c.Detail, "index missing") {
			t.Errorf("detail = %q, want it to say the index is missing", c.Detail)
		}
		wantFailed(t, c, "lw doctor --rebuild-index")
	})

	t.Run("corrupt index", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		openEngine(t, root)
		path := filepath.Join(root, stateDirName, indexFileName)
		if err := os.WriteFile(path, []byte("this is not a gob stream\n"), 0o644); err != nil {
			t.Fatalf("write index: %v", err)
		}
		c := checkIndex(root, openVaultOnly(t, root))
		if !strings.Contains(c.Detail, "index unreadable") {
			t.Errorf("detail = %q, want it to say the index is unreadable", c.Detail)
		}
		wantFailed(t, c, "lw doctor --rebuild-index")
	})

	t.Run("stale index", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		openEngine(t, root)
		path := filepath.Join(root, "wiki", "concepts", "kv-cache.md")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read page: %v", err)
		}
		if err := os.WriteFile(path, append(b, []byte("\nA line the index has never seen.\n")...), 0o644); err != nil {
			t.Fatalf("write page: %v", err)
		}
		c := checkIndex(root, openVaultOnly(t, root))
		if !strings.Contains(c.Detail, "stale") {
			t.Errorf("detail = %q, want it to say the index is stale", c.Detail)
		}
		wantFailed(t, c, "lw doctor --rebuild-index")
	})

	t.Run("missing object", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		sha := commitIngestSource(t, root)
		objPath := filepath.Join(root, stateDirName, objectsDirName, sha[:2], sha[2:])
		if err := os.Remove(objPath); err != nil {
			t.Fatalf("remove object: %v", err)
		}

		c := checkObjects(root, openEngine(t, root))
		if !strings.Contains(c.Detail, sha) {
			t.Errorf("detail = %q, want it to name the missing sha %s", c.Detail, sha)
		}
		wantFailed(t, c, sha, "lw doctor cannot repair")
	})

	t.Run("truncated journal after a good record", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		e := openEngine(t, root)
		if err := e.Journal().Append(stage.Event{
			TS:    fixedTS(),
			Kind:  stage.EvChangesetOpened,
			Actor: stage.Author{Kind: "human"},
		}); err != nil {
			t.Fatalf("journal event: %v", err)
		}
		path := filepath.Join(root, stateDirName, journalFileName)
		first, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read journal: %v", err)
		}
		// A record cut off mid-write: no terminating newline, unparsable.
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatalf("open journal: %v", err)
		}
		if _, err := f.WriteString(`{"ts":"2026-09-09T12:00:00Z","kind":"commit_beg`); err != nil {
			t.Fatalf("append truncated record: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("close journal: %v", err)
		}

		c := checkJournal(root)
		if !strings.Contains(c.Detail, "line 2") || !strings.Contains(c.Detail, fmt.Sprintf("byte offset %d", len(first))) {
			t.Errorf("detail = %q, want line 2 at byte offset %d", c.Detail, len(first))
		}
		wantFailed(t, c, "append-only", "line 2")
	})

	t.Run("journal that is only a truncated record", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		openEngine(t, root)
		path := filepath.Join(root, stateDirName, journalFileName)
		if err := os.WriteFile(path, []byte(`{"ts":"2026-09-09T12:00:00Z","kind":"changeset_opened"`), 0o644); err != nil {
			t.Fatalf("write journal: %v", err)
		}
		c := checkJournal(root)
		if !strings.Contains(c.Detail, "line 1") || !strings.Contains(c.Detail, "byte offset 0") {
			t.Errorf("detail = %q, want line 1 at byte offset 0", c.Detail)
		}
		wantFailed(t, c, "line 1")
	})

	t.Run("blank journal line", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		openEngine(t, root)
		path := filepath.Join(root, stateDirName, journalFileName)
		if err := os.WriteFile(path, []byte("\n"), 0o644); err != nil {
			t.Fatalf("write journal: %v", err)
		}
		wantFailed(t, checkJournal(root), "line 1")
	})

	t.Run("stale lock with a dead pid", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		openEngine(t, root)
		writeLock(t, root, fmt.Sprintf("%d 2026-09-09T12:00:00Z\n", deadPID))
		c := checkLock(root)
		if !strings.Contains(c.Detail, "stale lock") || !strings.Contains(c.Detail, fmt.Sprint(deadPID)) {
			t.Errorf("detail = %q, want it to name the stale lock and pid %d", c.Detail, deadPID)
		}
		wantFailed(t, c, "lw doctor --unlock")
	})

	t.Run("stale lock with an unparsable pid", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		openEngine(t, root)
		writeLock(t, root, "not a pid at all\n")
		c := checkLock(root)
		if !strings.Contains(c.Detail, "stale lock") || !strings.Contains(c.Detail, "unparsable pid") {
			t.Errorf("detail = %q, want the stale lock and its unparsable pid named", c.Detail)
		}
		wantFailed(t, c, "lw doctor --unlock")
	})

	t.Run("empty lock file", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		openEngine(t, root)
		writeLock(t, root, "")
		c := checkLock(root)
		if !strings.Contains(c.Detail, "empty") {
			t.Errorf("detail = %q, want the empty file named", c.Detail)
		}
		wantFailed(t, c, "lw doctor --unlock")
	})

	t.Run("live lock is not a failure", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		openEngine(t, root)
		writeLock(t, root, fmt.Sprintf("%d 2026-09-09T12:00:00Z\n", os.Getpid()))
		c := checkLock(root)
		if !c.OK {
			t.Fatalf("lock check = %+v, want OK for a lock this process holds", c)
		}
		if !strings.Contains(c.Detail, "live pid") {
			t.Errorf("detail = %q, want it to name the live holder", c.Detail)
		}
	})

	t.Run("interrupted apply", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		seedInterruptedApply(t, root)

		c := checkRecovery(openEngine(t, root))
		if !strings.Contains(c.Detail, "never completed") ||
			!strings.Contains(c.Detail, "1 path(s) applied, 1 pending") ||
			!strings.Contains(c.Detail, appliedRecoverPath) {
			t.Errorf("detail = %q, want the applied path named", c.Detail)
		}
		if !strings.Contains(c.Detail, pendingRecoverPath) {
			t.Errorf("detail = %q, want the pending path named", c.Detail)
		}
		wantFailed(t, c, "rolled forward", objectsRel, "lw doctor does not repair")
	})

	t.Run("interrupted apply with no object is not fixable", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		seedInterruptedApply(t, root)
		objectsDir := filepath.Join(root, stateDirName, objectsDirName)
		entries, err := os.ReadDir(objectsDir)
		if err != nil {
			t.Fatalf("list objects: %v", err)
		}
		for _, ent := range entries {
			if err := os.RemoveAll(filepath.Join(objectsDir, ent.Name())); err != nil {
				t.Fatalf("remove object: %v", err)
			}
		}

		wantFailed(t, checkRecovery(openEngine(t, root)), "cannot be rolled forward", "backup")
	})

	t.Run("unmoved changeset", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		e := openEngine(t, root)
		cs, err := e.OpenChangeset("doctor unmoved", stage.Author{Kind: "human"})
		if err != nil {
			t.Fatalf("open changeset: %v", err)
		}
		for _, kind := range []stage.EventKind{stage.EvCommitBegin, stage.EvCommitEnd} {
			if err := e.Journal().Append(stage.Event{
				TS:        fixedTS(),
				Kind:      kind,
				Changeset: cs.ID,
				Commit:    "000001",
				Actor:     stage.Author{Kind: "human"},
			}); err != nil {
				t.Fatalf("journal %s: %v", kind, err)
			}
		}

		// The journal reads complete (a matching commit_end follows), but the
		// changeset directory never left changesets/open. Doctor reports it
		// rather than completing the move: diagnosing is all this verb does
		// beyond the two repairs the stage file sanctions.
		c := checkRecovery(e)
		if !strings.Contains(c.Detail, cs.ID) || !strings.Contains(c.Detail, "still in") {
			t.Errorf("detail = %q, want it to name the unmoved changeset", c.Detail)
		}
		wantFailed(t, c, "changesets/committed", "by hand")
	})
}

func TestDoctorFlags(t *testing.T) {
	doctorTestEnv(t)

	t.Run("--unlock removes a stale lock", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		openEngine(t, root)
		writeLock(t, root, fmt.Sprintf("%d 2026-09-09T12:00:00Z\n", deadProcessPID(t)))

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"doctor", "--vault", root, "--unlock"})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stdout=%q", code, stdout)
		}
		if _, err := os.Stat(filepath.Join(root, stateDirName, lockFileName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("lock still present after --unlock (err=%v)", err)
		}
		if !strings.Contains(stdout, "removed "+lockRel) {
			t.Errorf("stdout = %q, want it to report the removal", stdout)
		}
		c := checkByName(t, runDoctor(context.Background(), root, doctorOptions{}), "lock")
		if !c.OK || !strings.Contains(c.Detail, "no lock held") {
			t.Errorf("lock check after --unlock = %+v, want no lock held", c)
		}
	})

	t.Run("--rebuild-index repairs a corrupt index", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		openEngine(t, root)
		if err := os.WriteFile(filepath.Join(root, stateDirName, indexFileName), []byte("garbage\n"), 0o644); err != nil {
			t.Fatalf("write index: %v", err)
		}

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"doctor", "--vault", root, "--rebuild-index"})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stdout=%q", code, stdout)
		}
		if !strings.Contains(stdout, "rebuilt "+indexRel) {
			t.Errorf("stdout = %q, want it to report the rebuild", stdout)
		}
		c := checkByName(t, runDoctor(context.Background(), root, doctorOptions{}), "index")
		if !c.OK {
			t.Fatalf("index check after --rebuild-index = %+v, want OK", c)
		}
		if !strings.Contains(c.Detail, "document(s) indexed") {
			t.Errorf("detail = %q, want the document count", c.Detail)
		}
	})

	t.Run("a deleted index still fails without the flag", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		openEngine(t, root)
		if err := os.Remove(filepath.Join(root, stateDirName, indexFileName)); err != nil {
			t.Fatalf("remove index: %v", err)
		}

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"doctor", "--vault", root})
		})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1; stdout=%q", code, stdout)
		}
		if !strings.Contains(stdout, "✗ index") || !strings.Contains(stdout, "index missing") {
			t.Errorf("stdout = %q, want the index failure and its remedy", stdout)
		}
		if !strings.Contains(stdout, "fix: run lw doctor --rebuild-index") {
			t.Errorf("stdout = %q, want the remedy line", stdout)
		}
		if !strings.Contains(stdout, "1 of ") {
			t.Errorf("stdout = %q, want the failure summary line", stdout)
		}
	})
}

func TestDoctorJSON(t *testing.T) {
	doctorTestEnv(t)

	t.Run("healthy vault", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		stdout, _, code := captureRun(t, func() int {
			return run([]string{"doctor", "--vault", root, "--json"})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stdout=%q", code, stdout)
		}

		var out struct {
			Vault  string `json:"vault"`
			OK     bool   `json:"ok"`
			Checks []struct {
				Name    string `json:"name"`
				OK      bool   `json:"ok"`
				Skipped bool   `json:"skipped"`
				Detail  string `json:"detail"`
				Remedy  string `json:"remedy"`
			} `json:"checks"`
		}
		if err := json.Unmarshal([]byte(stdout), &out); err != nil {
			t.Fatalf("parse --json output: %v\n%s", err, stdout)
		}
		if !out.OK || out.Vault != root {
			t.Errorf("report = %+v, want ok with vault %q", out, root)
		}
		if len(out.Checks) != 9 {
			t.Errorf("%d checks, want 9 (the 008 llm budget check included): %+v", len(out.Checks), out.Checks)
		}
		for _, c := range out.Checks {
			if !c.OK || c.Remedy != "" {
				t.Errorf("check %+v, want OK with no remedy", c)
			}
		}
		if !strings.Contains(stdout, "env:"+testKeyEnv) {
			t.Errorf("json output = %q, want the api_key reference env:%s", stdout, testKeyEnv)
		}
	})

	t.Run("faulty vault", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		openEngine(t, root)
		if err := os.Remove(filepath.Join(root, stateDirName, indexFileName)); err != nil {
			t.Fatalf("remove index: %v", err)
		}

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"doctor", "--vault", root, "--json"})
		})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1; stdout=%q", code, stdout)
		}
		if !strings.Contains(stdout, `"ok": false`) {
			t.Errorf("stdout = %q, want ok=false", stdout)
		}
		if !strings.Contains(stdout, `"remedy": "run lw doctor --rebuild-index`) {
			t.Errorf("stdout = %q, want the index remedy", stdout)
		}
	})

	t.Run("never prints the resolved key", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		stdout, _, code := captureRun(t, func() int {
			return run([]string{"doctor", "--vault", root})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stdout=%q", code, stdout)
		}
		if strings.Contains(stdout, testAPIKey) {
			t.Errorf("stdout prints the resolved API key:\n%s", stdout)
		}
	})
}

// TestDoctorDiscardChangeset covers TD-7's --discard-changeset: a stuck open
// changeset moves to changesets/rejected/ — never deleted — and stops
// refusing every future changeset. Three shapes matter: a changeset Engine
// can read (the ordinary `lw ingest` one), the C-118 fabrication
// (changesets/open/<id>/ holding only session.ndjson, which until now no
// verb could clear), and an empty changesets/open.
func TestDoctorDiscardChangeset(t *testing.T) {
	doctorTestEnv(t)

	t.Run("discards an open changeset", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		e := openEngine(t, root)
		cs, err := e.OpenChangeset("ingest under review", stage.Author{Kind: "human"})
		if err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		if err := e.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"doctor", "--vault", root, "--discard-changeset"})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stdout=%q", code, stdout)
		}
		if !strings.Contains(stdout, "discarded open changeset "+cs.ID) ||
			!strings.Contains(stdout, rejectedChangesetsRel) {
			t.Errorf("stdout = %q, want it to say what was discarded and where it went", stdout)
		}

		// The whole directory moved, session transcript and all.
		if _, err := os.Stat(filepath.Join(root, stateDirName, "changesets", "rejected", cs.ID, "changeset.json")); err != nil {
			t.Errorf("changeset.json did not travel with the move: %v", err)
		}
		entries, err := os.ReadDir(filepath.Join(root, stateDirName, "changesets", "open"))
		if err != nil {
			t.Fatalf("read open: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("%d entr(ies) left under %s, want none", len(entries), openChangesetsRel)
		}

		// The cure is the point: a changeset can be opened again.
		fresh := openEngine(t, root)
		if _, err := fresh.OpenChangeset("after the discard", stage.Author{Kind: "human"}); err != nil {
			t.Errorf("OpenChangeset after the discard: %v", err)
		}
	})

	t.Run("clears the C-118 fabricated directory", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		id := seedUnreadableChangeset(t, root)

		// What the engine sees before the discard: a changeset it cannot read.
		stuck := openEngine(t, root)
		if _, err := stuck.Current(); err == nil {
			t.Fatal("Current succeeded on the fabricated directory, want a read failure")
		} else if !strings.Contains(err.Error(), "changeset.json") {
			t.Errorf("Current error = %v, want it to name the unreadable changeset.json", err)
		}
		stuck.Close()

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"doctor", "--vault", root, "--discard-changeset"})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stdout=%q", code, stdout)
		}
		if !strings.Contains(stdout, id) || !strings.Contains(stdout, rejectedChangesetsRel) {
			t.Errorf("stdout = %q, want it to name %s and its destination", stdout, id)
		}
		if _, err := os.Stat(filepath.Join(root, stateDirName, "changesets", "rejected", id, "session.ndjson")); err != nil {
			t.Errorf("session.ndjson did not travel with the move: %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, stateDirName, "changesets", "open", id)); !os.IsNotExist(err) {
			t.Errorf("fabricated directory still under %s (err=%v)", openChangesetsRel, err)
		}

		// And the rejection is in the audit trail, not just on disk.
		e := openEngine(t, root)
		events, err := e.Journal().Query(stage.Filter{Kinds: []stage.EventKind{stage.EvChangesetRejected}, Limit: 1})
		if err != nil {
			t.Fatalf("query journal: %v", err)
		}
		if len(events) != 1 || events[0].Changeset != id {
			t.Fatalf("journalled %+v, want one changeset_rejected for %s", events, id)
		}
		if !strings.Contains(events[0].Message, discardReason) {
			t.Errorf("journal message = %q, want it to carry %q", events[0].Message, discardReason)
		}

		// A fresh ingest is no longer refused.
		if _, err := e.OpenChangeset("after the discard", stage.Author{Kind: "human"}); err != nil {
			t.Errorf("OpenChangeset after the discard: %v", err)
		}
	})

	t.Run("no open changeset exits 1", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"doctor", "--vault", root, "--discard-changeset"})
		})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1 — there was nothing to discard; stdout=%q", code, stdout)
		}
		if !strings.Contains(stdout, "no open changeset") {
			t.Errorf("stdout = %q, want it to say there was no open changeset", stdout)
		}
	})

	t.Run("without the flag nothing is discarded", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		e := openEngine(t, root)
		cs, err := e.OpenChangeset("stays open", stage.Author{Kind: "human"})
		if err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		e.Close()

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"doctor", "--vault", root})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stdout=%q", code, stdout)
		}
		if strings.Contains(stdout, "discarded open changeset") || strings.Contains(stdout, "discarded unreadable") {
			t.Errorf("stdout = %q, want no discard without the flag", stdout)
		}
		if _, err := os.Stat(filepath.Join(root, stateDirName, "changesets", "open", cs.ID)); err != nil {
			t.Errorf("open changeset disturbed by a plain doctor run: %v", err)
		}
	})
}

// seedUnreadableChangeset fabricates the C-118 shape: a directory under
// changesets/open/ holding a session.ndjson and no changeset.json, which is
// what turn-start Sessions.Create leaves behind when the changeset it was
// created for vanished between Engine.Current and Create.
func seedUnreadableChangeset(t *testing.T, root string) string {
	t.Helper()
	id := "cs-fabricate0001"
	dir := filepath.Join(root, stateDirName, "changesets", "open", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.ndjson"), nil, 0o644); err != nil {
		t.Fatalf("write session.ndjson: %v", err)
	}
	return id
}

func TestDoctorUsage(t *testing.T) {
	doctorTestEnv(t)

	if _, stderr, code := captureRun(t, func() int {
		return run([]string{"doctor", "--bogusflag"})
	}); code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%q", code, stderr)
	}

	root := testutil.CopyFixture(t, "minimal")
	if _, stderr, code := captureRun(t, func() int {
		return run([]string{"doctor", "--vault", root, "unexpected"})
	}); code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%q", code, stderr)
	}
}

func TestDoctorConfigCheck(t *testing.T) {
	doctorTestEnv(t)
	testutil.CopyFixture(t, "minimal")

	t.Run("env reference set", func(t *testing.T) {
		c := checkConfig(configWithKey("env:"+testKeyEnv), nil)
		if !c.OK {
			t.Fatalf("check = %+v, want OK", c)
		}
		if !strings.Contains(c.Detail, "(set)") || !strings.Contains(c.Detail, "env:"+testKeyEnv) {
			t.Errorf("detail = %q, want the reference and its status", c.Detail)
		}
		if c.Remedy != "" {
			t.Errorf("remedy = %q, want empty for a passing check", c.Remedy)
		}
		if strings.Contains(c.Detail, testAPIKey) {
			t.Errorf("detail prints the key's value: %q", c.Detail)
		}
	})

	t.Run("env reference missing", func(t *testing.T) {
		t.Setenv(testKeyEnv, "")
		c := checkConfig(configWithKey("env:"+testKeyEnv), nil)
		if !strings.Contains(c.Detail, "(missing)") {
			t.Errorf("detail = %q, want (missing)", c.Detail)
		}
		wantFailed(t, c, "export "+testKeyEnv, "lw config set llm.api_key")
	})

	t.Run("no api_key at all", func(t *testing.T) {
		c := checkConfig(configWithKey(""), nil)
		if !strings.Contains(c.Detail, "no api_key configured") {
			t.Errorf("detail = %q, want the missing-config finding", c.Detail)
		}
		wantFailed(t, c, "lw config set llm.api_key")
	})

	t.Run("keyring unsupported", func(t *testing.T) {
		c := checkConfig(configWithKey("keyring:lw"), nil)
		if !strings.Contains(c.Detail, "not supported yet") {
			t.Errorf("detail = %q, want the keyring finding", c.Detail)
		}
		wantFailed(t, c, "env:NAME")
	})

	t.Run("literal key is reported without its value", func(t *testing.T) {
		c := checkConfig(configWithKey(testAPIKey), nil)
		if !c.OK {
			t.Fatalf("check = %+v, want OK: a literal key works", c)
		}
		if !strings.Contains(c.Detail, "literal (set)") {
			t.Errorf("detail = %q, want the literal status", c.Detail)
		}
		if strings.Contains(c.Detail, testAPIKey) {
			t.Errorf("detail prints the literal key value: %q", c.Detail)
		}
	})

	t.Run("config load error", func(t *testing.T) {
		c := checkConfig(nil, errors.New("config: parse config.toml: bad toml"))
		wantFailed(t, c, "lw config")
	})

	t.Run("provider skipped without a key", func(t *testing.T) {
		t.Setenv(testKeyEnv, "")
		c := checkProvider(context.Background(), configWithKey("env:"+testKeyEnv))
		if !c.OK || !c.Skipped {
			t.Fatalf("provider check = %+v, want a skipped, non-failing check", c)
		}
		if !strings.Contains(c.Detail, "skipped") {
			t.Errorf("detail = %q, want it to say why it was skipped", c.Detail)
		}
	})
}

func TestDoctorProviderCheck(t *testing.T) {
	doctorTestEnv(t)
	cfg := configWithKey("env:" + testKeyEnv)

	t.Run("unreachable", func(t *testing.T) {
		withProbe(t, func(ctx context.Context, cfg *config.Config) llm.ProbeResult {
			return llm.ProbeResult{Reachable: false, Model: cfg.LLM.Model, Err: errors.New("dial tcp: connection refused")}
		})
		c := checkProvider(context.Background(), cfg)
		if !strings.Contains(c.Detail, "unreachable") || !strings.Contains(c.Detail, "connection refused") {
			t.Errorf("detail = %q, want the reachability failure", c.Detail)
		}
		wantFailed(t, c, "llm.base_url", "lw config")
	})

	t.Run("reachable but no tool call", func(t *testing.T) {
		withProbe(t, func(ctx context.Context, cfg *config.Config) llm.ProbeResult {
			return llm.ProbeResult{Reachable: true, Model: cfg.LLM.Model}
		})
		c := checkProvider(context.Background(), cfg)
		if !strings.Contains(c.Detail, "returned no tool call") {
			t.Errorf("detail = %q, want the tool-calling failure", c.Detail)
		}
		wantFailed(t, c, "tool_calls", "lw config set llm.model")
	})

	t.Run("healthy", func(t *testing.T) {
		withProbe(t, healthyProbe)
		c := checkProvider(context.Background(), cfg)
		if !c.OK {
			t.Fatalf("provider check = %+v, want OK", c)
		}
		if !strings.Contains(c.Detail, "tool calling ok") {
			t.Errorf("detail = %q, want the tool-calling confirmation", c.Detail)
		}
	})

	t.Run("probe runs under a deadline", func(t *testing.T) {
		var gotDeadline bool
		withProbe(t, func(ctx context.Context, cfg *config.Config) llm.ProbeResult {
			_, gotDeadline = ctx.Deadline()
			return llm.ProbeResult{Reachable: true, Model: cfg.LLM.Model, ToolCalling: true}
		})
		if c := checkProvider(context.Background(), cfg); !c.OK {
			t.Fatalf("provider check = %+v, want OK", c)
		}
		if !gotDeadline {
			t.Error("probeProvider ran with no deadline; a hung endpoint would hang doctor")
		}
	})
}

func TestMCPDepsCarryExtractor(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	e := openEngine(t, root)

	deps := mcpDeps(e)
	if deps.Extract == nil {
		t.Fatal("mcp Deps.Extract is nil; stage_ingest_source would answer \"no extractor configured\" over MCP")
	}
	if !deps.Extract.CanHandle("https://example.org/article") {
		t.Error("mcp Deps.Extract cannot handle an http URL; the HTML extractor is missing from the chain")
	}
	if !deps.Extract.CanHandle("notes/local.md") {
		t.Error("mcp Deps.Extract cannot handle a local path; the file extractor is missing from the chain")
	}
	if deps.Vault == nil || deps.Index == nil || deps.Engine == nil {
		t.Errorf("mcp Deps = %+v, want vault, index and engine set", deps)
	}
}

// configWithKey is Default with llm.api_key overridden, for the config
// check's table.
func configWithKey(ref string) *config.Config {
	cfg := config.Default()
	cfg.LLM.APIKey = ref
	return cfg
}

// deadProcessPID returns the pid of a process that has been started and
// reaped, so the lock-staleness test exercises a genuinely dead holder
// rather than a guessed pid.
func deadProcessPID(t *testing.T) int {
	t.Helper()
	path, err := exec.LookPath("true")
	if err != nil {
		t.Skip("no true(1) on PATH; cannot reap a real process")
	}
	cmd := exec.Command(path)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start true: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait true: %v", err)
	}
	if pidAlive(pid) {
		t.Skipf("reaped pid %d was recycled; cannot prove staleness", pid)
	}
	return pid
}

// writeLock writes the lock file the lock tests need, in the format
// internal/stage's AcquireLock uses: "<pid> <RFC3339>\n".
func writeLock(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, stateDirName, lockFileName), []byte(content), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
}

func TestDoctorObjectsAfterCommit(t *testing.T) {
	doctorTestEnv(t)
	root := testutil.CopyFixture(t, "minimal")
	sha := commitIngestSource(t, root)

	c := checkObjects(root, openEngine(t, root))
	if !c.OK {
		t.Fatalf("objects check = %+v, want OK on a freshly committed vault", c)
	}
	// The commit's own raw source must have been checked: its sha is both
	// the snapshot's post-image for that path and the object the CAS holds.
	if !strings.Contains(c.Detail, "snapshots: 1") || !strings.Contains(c.Detail, "snapshot 000001 holds") {
		t.Errorf("detail = %q, want the committed path checked against the snapshot", c.Detail)
	}
	if strings.Contains(c.Detail, sha) {
		t.Errorf("detail = %q, want shas out of the summary line", c.Detail)
	}

	// An unreadable snapshot is damage: Revert reconstructs from it.
	if err := os.WriteFile(filepath.Join(root, stateDirName, snapshotsDirName, "000001.tree"), []byte("not a snapshot line\n"), 0o644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	wantFailed(t, checkObjects(root, openEngine(t, root)), "000001", "lw revert")
}

// TestDoctorObjectsAfterCreatePageCommit is the S6-C126 regression pin: a
// create_page commit rewrites index.md IMPLICITLY, through its own
// index-derivation (backbone §5.4 D-CA rule (a), derive.go) rather than
// through any op that carries a Before/After sha — unlike
// commitIngestSource above, whose ingest_source op never touches index.md
// at all, which is exactly why that helper's coverage missed this defect.
// Found live at gate G6, 2026-09-14: on a freshly `lw init`-ed vault, the
// first commit that creates a page left index.md's pre-commit blob out of
// the CAS entirely, and this exact check reported it missing. internal/stage
// pins the underlying storage fix directly (apply_preimage_test.go); this
// test pins it through the same doctor entry point the live defect was
// diagnosed with, so a regression here is caught the way it was found.
func TestDoctorObjectsAfterCreatePageCommit(t *testing.T) {
	doctorTestEnv(t)
	root := testutil.CopyFixture(t, "minimal")
	commitCreatePageForDoctorTest(t, root)

	c := checkObjects(root, openEngine(t, root))
	if !c.OK {
		t.Fatalf("objects check = %+v, want OK after a create_page commit (S6-C126)", c)
	}
	if strings.Contains(c.Detail, "missing") {
		t.Errorf("detail = %q, want no missing objects", c.Detail)
	}
}

// commitCreatePageForDoctorTest opens a changeset, proposes one well-formed
// create_page op against a page under wiki/concepts/, and commits it — the
// shape that triggers create_page's own index.md derivation, unlike
// commitIngestSource's ingest_source op.
func commitCreatePageForDoctorTest(t *testing.T, root string) {
	t.Helper()
	e := openEngine(t, root)

	const path = "wiki/concepts/doctor-create.md"
	content := []byte("---\n" +
		"title: Doctor Create\n" +
		"created: 2026-09-14\n" +
		"updated: 2026-09-14\n" +
		"type: concept\n" +
		"tags: [inference]\n" +
		"confidence: medium\n" +
		"---\n" +
		"\n" +
		"# Doctor Create\n" +
		"\n" +
		"See [[kv-cache]] and [[gpt-4]] for background.\n")

	if _, err := e.OpenChangeset("doctor test create", stage.Author{Kind: "human"}); err != nil {
		t.Fatalf("open changeset: %v", err)
	}
	if _, err := e.Append(stage.Op{
		Kind:       stage.OpCreatePage,
		Path:       path,
		Content:    content,
		Rationale:  "test",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	}); err != nil {
		t.Fatalf("append create_page: %v", err)
	}
	if _, err := e.Commit("doctor test create commit"); err != nil {
		t.Fatalf("commit: %v", err)
	}
}
