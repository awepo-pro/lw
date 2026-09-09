// stress_test.go is the budgeted 1000-note run: the verbs the coldstart
// scenarios walk, at a scale where a wrong complexity class shows up in wall
// time. It is opt-in — LW_STRESS_SCALE=1, or `make stress-scale` — because a
// run that takes tens of seconds must never sit inside `go test ./...` or CI.
// Every budget below is a ceiling for a catastrophic regression, with
// headroom for a busy host, and every measured step also logs a `wall:` line
// so the actual number lands in the run log as evidence.
package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/testutil"
)

// Shape of the run: stressNotes notes in the vault, stressOps create_page
// ops staged on top of them.
const (
	stressNotes = 1000
	stressOps   = 20
)

// Budgets, as the task pins them. A number here is the point at which a step
// has regressed catastrophically — seconds into minutes — not the point at
// which the machine is judged: in-process measurements at this scale put each
// of these steps well under a second, so the headroom is all variance margin.
const (
	budgetLint   = 60 * time.Second
	budgetStatus = 10 * time.Second
	budgetStage  = 60 * time.Second
	budgetCommit = 120 * time.Second
	budgetAgent  = 30 * time.Second // one ingest or query turn, fake LLM included
)

// runLWTimed runs one lw verb, logs the step's wall time as a `wall:` line
// and fails the test if the step outran budget. A budget of 0 means "measure,
// do not judge" — the wall line is still recorded, because the value of this
// run is the numbers it leaves behind.
//
// The kill ceiling is the step's budget plus stressSlack, never the harness's
// 60s smoke deadline: budgetStage and budgetCommit sit at or past 60s, so a
// step taking 60–120s is inside its budget and must be measured against it —
// killed at the smoke ceiling it would surface as exit -1 "did not exit
// within 60s", a hang it never was, and the frozen budget would be dead
// text. An unbudgeted step (budget 0) has nothing to be measured against, so
// it keeps the ordinary ceiling.
func runLWTimed(t *testing.T, e *env, budget time.Duration, args ...string) lwResult {
	t.Helper()

	deadline := lwDeadline
	if budget > 0 {
		deadline = budget + stressSlack
	}

	start := time.Now()
	res := runLWWithDeadline(t, e, deadline, args...)
	elapsed := time.Since(start)

	t.Logf("wall: %s %s", args[0], elapsed)
	if budget > 0 && elapsed > budget {
		t.Errorf("lw %s: %s exceeded the %s budget", args[0], elapsed, budget)
	}
	return res
}

// TestStressScale builds a 1000-note vault, drives it through the real binary
// one verb at a time and logs every step's wall time. Order matters once: the
// query step must run while the vault has no open changeset, because
// cmd_query rejects any changeset it finds when its turn ends — so query
// comes after the commit that closes the staged one and before the ingest
// that opens another.
func TestStressScale(t *testing.T) {
	switch {
	case os.Getenv("LW_STRESS_SCALE") != "1":
		t.Skip("LW_STRESS_SCALE != 1: the 1000-note scale run is opt-in (make stress-scale)")
	case testing.Short():
		t.Skip("-short: the 1000-note scale run is opt-in (make stress-scale)")
	}

	e := newEnv(t)
	vault := filepath.Join(t.TempDir(), "scale-vault")
	testutil.NewScaleVault(t, vault, stressNotes)
	t.Logf("scale: %d notes in %s", stressNotes, vault)

	// lint --json: the read-only check a review screen opens with, over the
	// generated vault. The generated notes carry info-level size-split
	// findings by design, which is why the exit code is judged and the
	// findings are not.
	res := runLWTimed(t, e, budgetLint, "lint", "--vault", vault, "--json")
	if res.Code != 0 {
		t.Fatalf("lw lint --json: exit %d, want 0\n%s", res.Code, res.Output)
	}

	res = runLWTimed(t, e, budgetStatus, "status", "--vault", vault)
	if res.Code != 0 {
		t.Fatalf("lw status: exit %d, want 0\n%s", res.Code, res.Output)
	}
	if want := fmt.Sprintf("%d pages", stressNotes); !strings.Contains(res.Output, want) {
		t.Errorf("lw status: output does not count %s, so the vault under test is not the vault built\n%s", want, res.Output)
	}

	ops := make([]stageOp, 0, stressOps)
	for i := 0; i < stressOps; i++ {
		ops = append(ops, createPageOp(
			fmt.Sprintf("wiki/concepts/stress-page-%02d.md", i),
			fmt.Sprintf("Stress Page %02d", i),
			"raw/articles/stress-ingest.md",
			[]string{"note-0000", "note-0001"},
		))
	}
	opsPath := writeOpsJSON(t, e, "ops.json", ops)

	res = runLWTimed(t, e, budgetStage, "stage", "--vault", vault, "--from", opsPath)
	if res.Code != 0 {
		t.Fatalf("lw stage --from: exit %d, want 0\n%s", res.Code, res.Output)
	}
	if want := fmt.Sprintf("staged %d op(s)", stressOps); !strings.Contains(res.Output, want) {
		t.Errorf("lw stage --from: output does not say %q\n%s", want, res.Output)
	}

	res = runLWTimed(t, e, 0, "diff", "--vault", vault, "--stat")
	if res.Code != 0 {
		t.Fatalf("lw diff --stat: exit %d, want 0\n%s", res.Code, res.Output)
	}

	res = runLWTimed(t, e, budgetCommit, "commit", "--vault", vault, "-m", "stress: 20 pages on a 1000-note vault")
	if res.Code != 0 {
		t.Fatalf("lw commit -m: exit %d, want 0\n%s", res.Code, res.Output)
	}
	id := committedRE.FindStringSubmatch(res.Stdout)
	if id == nil {
		t.Fatalf("lw commit: no commit id in stdout\n%s", res.Stdout)
	}

	res = runLWTimed(t, e, 0, "log", "--vault", vault, "--limit", "5")
	if res.Code != 0 {
		t.Fatalf("lw log --limit 5: exit %d, want 0\n%s", res.Code, res.Output)
	}
	if !strings.Contains(res.Output, "commit="+id[1]) {
		t.Errorf("lw log --limit 5: no line carries commit=%s\n%s", id[1], res.Output)
	}

	// query, while nothing is open — see this test's doc comment.
	qfake := newFakeLLM(t, fakeRound{Name: "query_stop", Body: queryStopSSE})
	writeConfig(t, e.config, qfake.URL()+"/v1")

	res = runLWTimed(t, e, budgetAgent, "query", "--vault", vault, queryQuestion)
	if res.Code != 0 {
		t.Fatalf("lw query: exit %d, want 0\n%s", res.Code, res.Output)
	}
	if !strings.Contains(res.Stdout, queryAnswerText) {
		t.Errorf("lw query: stdout does not carry the scripted answer %q\n%s", queryAnswerText, res.Stdout)
	}

	// ingest, last: it opens a changeset of its own and, like any real
	// ingest, leaves it for human review.
	ifake := newFakeLLM(t,
		sseFixture(t, "ingest_round1.sse"),
		sseFixture(t, "ingest_round2.sse"),
	)
	writeConfig(t, e.config, ifake.URL()+"/v1")

	src := e.writeSource(t, "sources/stress-ingest.md", "# Stress Ingest\n\nA local source the 1000-note ingest step compiles one page from.\n")

	res = runLWTimed(t, e, budgetAgent, "ingest", "--vault", vault, "--kind", "article", src)
	if res.Code != 0 {
		t.Fatalf("lw ingest: exit %d, want 0\n%s", res.Code, res.Output)
	}
}
