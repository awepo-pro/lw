// coldstart_test.go drives the real lw binary through every verb that exists
// today — the automated replacement for gate G6's manual walkthrough. Each
// scenario is independent: one private sandbox from the task-2 helpers, one
// vault of its own, and no network but the fake LLM the ingest and query
// scenarios script. init/config/doctor enter as placeholders rather than being
// silently absent, because their verbs are stubs in this build.
package e2e

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// smokeDate stamps every page the scenarios propose: fixed, not time.Now, so
// two runs produce byte-identical ops and diffs — and a date the fixtures'
// own pages predate, so no fm-dates finding can appear by accident.
const smokeDate = "2026-08-29"

// committedRE matches `lw commit`'s single stdout line — "committed <id>" —
// and captures the id, which a scenario hands straight to `lw log` or
// `lw revert`.
var committedRE = regexp.MustCompile(`committed (\S+)`)

// stageOp is one entry of the ops.json file `lw stage --from` reads. Its tags
// are stage.Op's own frozen JSON tags — cmd_stage.go decodes the file straight
// into []stage.Op — narrowed to the five keys a create_page carries, plus the
// one extra "content" key: Op.Content is json:"-" (backbone §5.3 D-AY), so
// cmd_stage.go's second decode pass is the only thing that can recover it.
type stageOp struct {
	Op         string   `json:"op"`
	Path       string   `json:"path"`
	Rationale  string   `json:"rationale"`
	Provenance []string `json:"provenance"`
	Content    string   `json:"content"`
}

// writeOpsJSON marshals ops as the JSON array cmd_stage.go's loader expects
// and writes it into e's working directory, returning the absolute path to
// hand to `lw stage --from`.
func writeOpsJSON(t *testing.T, e *env, name string, ops []stageOp) string {
	t.Helper()

	b, err := json.MarshalIndent(ops, "", "  ")
	if err != nil {
		t.Fatalf("e2e: marshal %d stage op(s): %v", len(ops), err)
	}
	return e.writeSource(t, name, string(b)+"\n")
}

// createPageOp builds one schema-valid create_page op: frontmatter whose tags
// sit in the target schema's taxonomy, a body carrying the two outbound
// wikilinks validateCreatePage demands, and the provenance entry the schema's
// required set adds on top of the backbone §5.5 bullets (MASTER §9 D-BH).
// links must name pages that already exist in the target vault — a link that
// resolves to nothing is a lint error, and a lint error is a story no
// coldstart scenario is about.
func createPageOp(path, title, provenance string, links []string) stageOp {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("title: " + title + "\n")
	b.WriteString("created: " + smokeDate + "\n")
	b.WriteString("updated: " + smokeDate + "\n")
	b.WriteString("type: concept\n")
	b.WriteString("tags: [inference, memory]\n")
	b.WriteString("confidence: high\n")
	b.WriteString("---\n\n")
	b.WriteString("# " + title + "\n\n")
	b.WriteString("A page the e2e suite proposes through the real lw binary, so the\nverb-by-verb walkthrough never depends on a model being reachable.\n")
	b.WriteString("\n## Related\n\n")
	for _, link := range links {
		b.WriteString("- [[" + link + "]] — a page that already exists in the vault.\n")
	}

	return stageOp{
		Op:         "create_page",
		Path:       path,
		Rationale:  "Coldstart scenario: one schema-valid page proposed for human review.",
		Provenance: []string{provenance},
		Content:    b.String(),
	}
}

// queryStopSSE is the single round the query scenarios script: an assistant
// text delta and a stop, so the agent loop ends after exactly one request and
// no tool call is ever made — `lw query` must be able to answer from what it
// already holds, and the answer it streams is the one thing a scenario can
// assert on without a second round of tool traffic.
const queryStopSSE = "data: {\"id\":\"chatcmpl-e2e-query\",\"choices\":[{\"index\":0," +
	"\"delta\":{\"role\":\"assistant\",\"content\":\"The vault is indexed; nothing is out of place.\"}," +
	"\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"chatcmpl-e2e-query\",\"choices\":[{\"index\":0,\"delta\":{}," +
	"\"finish_reason\":\"stop\"}]}\n\n" +
	"data: [DONE]\n"

// queryQuestion is the read-only question those scenarios ask, and
// queryAnswerText the substring of the scripted answer that must come back on
// stdout — proof the text deltas reached the terminal, not just exit 0.
const (
	queryQuestion   = "What does this vault say about attention?"
	queryAnswerText = "nothing is out of place"
)

// TestSmokeVersion runs `lw --version` and expects the exact release string
// on stdout and nothing on stderr (main.go:52-54).
func TestSmokeVersion(t *testing.T) {
	e := newEnv(t)

	res := runLW(t, e, "--version")
	if res.Code != 0 {
		t.Fatalf("lw --version: exit %d, want 0\n%s", res.Code, res.Output)
	}
	if got, want := strings.TrimSpace(res.Stdout), "lw 0.1.0-dev"; got != want {
		t.Errorf("lw --version: stdout = %q, want %q", got, want)
	}
	if res.Stderr != "" {
		t.Errorf("lw --version: stderr = %q, want empty", res.Stderr)
	}
}

// TestSmokeStageFromRoundtrip walks the whole human-review loop the agent-free
// way: one schema-valid create_page staged from an ops file, reviewed through
// status and diff, committed, read back out of the journal and reverted. It is
// deliberately network-free — no config, no fake LLM — so the loop's own
// machinery is the only thing under test.
func TestSmokeStageFromRoundtrip(t *testing.T) {
	e := newEnv(t)
	vault := testutil.CopyFixture(t, "minimal")

	ops := writeOpsJSON(t, e, "ops.json", []stageOp{
		createPageOp(
			"wiki/concepts/coldstart-page.md",
			"Coldstart Page",
			"raw/articles/kv-cache-explained.md",
			[]string{"kv-cache", "flash-attention"},
		),
	})

	stage := runLW(t, e, "stage", "--vault", vault, "--from", ops)
	if stage.Code != 0 {
		t.Fatalf("lw stage --from: exit %d, want 0\n%s", stage.Code, stage.Output)
	}
	if want := "staged 1 op(s)"; !strings.Contains(stage.Output, want) {
		t.Errorf("lw stage --from: output does not say %q\n%s", want, stage.Output)
	}

	status := runLW(t, e, "status", "--vault", vault)
	if status.Code != 0 {
		t.Fatalf("lw status: exit %d, want 0\n%s", status.Code, status.Output)
	}
	m := openOpsRE.FindStringSubmatch(status.Output)
	if m == nil {
		t.Fatalf("lw status: no open changeset line in output\n%s", status.Output)
	}
	if m[1] == "0" {
		t.Errorf("lw status: open changeset reports 0 op(s), want the staged one\n%s", status.Output)
	}

	const page = "wiki/concepts/coldstart-page.md"
	diff := runLW(t, e, "diff", "--vault", vault, "--stat")
	if diff.Code != 0 {
		t.Fatalf("lw diff --stat: exit %d, want 0\n%s", diff.Code, diff.Output)
	}
	if !strings.Contains(diff.Output, page) {
		t.Errorf("lw diff --stat: output does not mention the staged page %s\n%s", page, diff.Output)
	}

	commit := runLW(t, e, "commit", "--vault", vault, "-m", "first")
	if commit.Code != 0 {
		t.Fatalf("lw commit -m first: exit %d, want 0\n%s", commit.Code, commit.Output)
	}
	id := committedRE.FindStringSubmatch(commit.Stdout)
	if id == nil {
		t.Fatalf("lw commit: no commit id in stdout\n%s", commit.Stdout)
	}

	log := runLW(t, e, "log", "--vault", vault, "--limit", "10")
	if log.Code != 0 {
		t.Fatalf("lw log --limit 10: exit %d, want 0\n%s", log.Code, log.Output)
	}
	if !strings.Contains(log.Output, "commit="+id[1]) {
		t.Errorf("lw log --limit 10: no line carries commit=%s\n%s", id[1], log.Output)
	}

	revert := runLW(t, e, "revert", "--vault", vault, id[1])
	if revert.Code != 0 {
		t.Fatalf("lw revert %s: exit %d, want 0\n%s", id[1], revert.Code, revert.Output)
	}
	if !strings.Contains(revert.Output, "revert of "+id[1]) {
		t.Errorf("lw revert %s: output does not name the commit it reverses\n%s", id[1], revert.Output)
	}

	// Reverting is vault state, not narration: the reverse changeset must be
	// sitting open for review — the same "open changeset … (N op(s)…" line
	// every other leg is judged by — with an intent naming the commit it
	// reverses. Only presence and intent are asserted, never a count: the
	// reverse changeset's ops are whatever the revert staged, which need not
	// match the original's.
	status = runLW(t, e, "status", "--vault", vault)
	if status.Code != 0 {
		t.Fatalf("lw status after revert: exit %d, want 0\n%s", status.Code, status.Output)
	}
	m = openOpsRE.FindStringSubmatch(status.Output)
	if m == nil {
		t.Fatalf("lw status after revert: no open changeset line in output\n%s", status.Output)
	}
	if m[1] == "0" {
		t.Errorf("lw status after revert: open changeset reports 0 op(s), want the reverse ops\n%s", status.Output)
	}
	intent := openIntentRE.FindStringSubmatch(status.Output)
	if intent == nil {
		t.Fatalf("lw status after revert: no open changeset line in output\n%s", status.Output)
	}
	if !strings.Contains(intent[1], "revert of "+id[1]) {
		t.Errorf("lw status after revert: open changeset intent %q does not name the reverted commit %s\n%s", intent[1], id[1], status.Output)
	}
}

// TestSmokeIngestCommit runs the agent-driven half of the loop against the
// fake LLM: a local markdown source goes in, the scripted curator proposes one
// page, and that changeset is reviewed, committed and read back out of the
// journal — the path a real session takes, with no network beyond localhost.
func TestSmokeIngestCommit(t *testing.T) {
	e := newEnv(t)
	vault := newVault(t)
	fake := newFakeLLM(t,
		sseFixture(t, "ingest_round1.sse"),
		sseFixture(t, "ingest_round2.sse"),
	)
	writeConfig(t, e.config, fake.URL()+"/v1")

	src := e.writeSource(t, "sources/smoke-ingest.md", "# Smoke Ingest\n\nA local markdown source for the coldstart ingest scenario.\n")

	ingest := runLW(t, e, "ingest", "--vault", vault, "--kind", "article", src)
	if ingest.Code != 0 {
		t.Fatalf("lw ingest: exit %d, want 0\n%s", ingest.Code, ingest.Output)
	}

	status := runLW(t, e, "status", "--vault", vault)
	if status.Code != 0 {
		t.Fatalf("lw status: exit %d, want 0\n%s", status.Code, status.Output)
	}
	m := openOpsRE.FindStringSubmatch(status.Output)
	if m == nil {
		t.Fatalf("lw status: no open changeset line in output\n%s", status.Output)
	}
	if m[1] == "0" {
		t.Errorf("lw status: open changeset reports 0 op(s), want the proposed one\n%s", status.Output)
	}

	const page = "wiki/concepts/e2e-round-trip.md"
	diff := runLW(t, e, "diff", "--vault", vault, "--stat")
	if diff.Code != 0 {
		t.Fatalf("lw diff --stat: exit %d, want 0\n%s", diff.Code, diff.Output)
	}
	if !strings.Contains(diff.Output, page) {
		t.Errorf("lw diff --stat: output does not mention the proposed page %s\n%s", page, diff.Output)
	}

	commit := runLW(t, e, "commit", "--vault", vault, "-m", "smoke: first agent-proposed page")
	if commit.Code != 0 {
		t.Fatalf("lw commit -m: exit %d, want 0\n%s", commit.Code, commit.Output)
	}
	id := committedRE.FindStringSubmatch(commit.Stdout)
	if id == nil {
		t.Fatalf("lw commit: no commit id in stdout\n%s", commit.Stdout)
	}

	log := runLW(t, e, "log", "--vault", vault, "--limit", "10")
	if log.Code != 0 {
		t.Fatalf("lw log --limit 10: exit %d, want 0\n%s", log.Code, log.Output)
	}
	if !strings.Contains(log.Output, "commit="+id[1]) {
		t.Errorf("lw log --limit 10: no line carries commit=%s\n%s", id[1], log.Output)
	}
}

// TestSmokeQuery asks one read-only question against a one-round script and
// expects the streamed answer on stdout and nothing staged behind it. The
// round is inline rather than a testdata file: it is the whole conversation,
// and keeping it next to the scenario that plays it makes the one-request
// shape visible.
func TestSmokeQuery(t *testing.T) {
	e := newEnv(t)
	vault := newVault(t)
	fake := newFakeLLM(t, fakeRound{Name: "query_stop", Body: queryStopSSE})
	writeConfig(t, e.config, fake.URL()+"/v1")

	res := runLW(t, e, "query", "--vault", vault, queryQuestion)
	if res.Code != 0 {
		t.Fatalf("lw query: exit %d, want 0\n%s", res.Code, res.Output)
	}
	if !strings.Contains(res.Stdout, queryAnswerText) {
		t.Errorf("lw query: stdout does not carry the scripted answer %q\n%s", queryAnswerText, res.Stdout)
	}
	if got, want := fake.Served(), 1; got != want {
		t.Errorf("fake LLM served %d requests, want %d (one read-only turn)", got, want)
	}
}

// TestSmokeLintClean lints the clean fixture twice: once as the one-line
// report a human reads, once as JSON a tool could consume. Both exit 0, and
// the JSON's own error count must agree with the exit code.
func TestSmokeLintClean(t *testing.T) {
	e := newEnv(t)
	vault := testutil.CopyFixture(t, "minimal")

	res := runLW(t, e, "lint", "--vault", vault)
	if res.Code != 0 {
		t.Fatalf("lw lint: exit %d, want 0 on the clean fixture\n%s", res.Code, res.Output)
	}

	js := runLW(t, e, "lint", "--vault", vault, "--json")
	if js.Code != 0 {
		t.Fatalf("lw lint --json: exit %d, want 0\n%s", js.Code, js.Output)
	}
	var report struct {
		Errors int `json:"Errors"`
		Warns  int `json:"Warns"`
	}
	if err := json.Unmarshal([]byte(js.Stdout), &report); err != nil {
		t.Fatalf("lw lint --json: stdout does not parse as a lint report: %v\n%s", err, js.Stdout)
	}
	if report.Errors != 0 {
		t.Errorf("lw lint --json: %d error(s) on the clean fixture, want 0\n%s", report.Errors, js.Stdout)
	}
}

// TestSmokeLintDirty lints the dirty fixture and pins the JSON report to the
// golden's exact error count. spec/fixtures/dirty/EXPECTED-LINT.md carries 16
// findings of which exactly 7 are error rows (index-sync ×2, src-integrity ×2,
// link-broken, fm-required, fm-taxonomy), so 7 is the only number that proves
// every error-severity check still fires — a report that silently undercounts
// would pass a mere "nonzero" test. lint still exits 1 (cmd_lint.go:86), and
// the JSON form of the same report must still parse.
func TestSmokeLintDirty(t *testing.T) {
	e := newEnv(t)
	vault := testutil.CopyFixture(t, "dirty")

	res := runLW(t, e, "lint", "--vault", vault)
	if res.Code != 1 {
		t.Fatalf("lw lint: exit %d, want 1 on the dirty fixture\n%s", res.Code, res.Output)
	}

	js := runLW(t, e, "lint", "--vault", vault, "--json")
	if js.Code != 1 {
		t.Fatalf("lw lint --json: exit %d, want 1\n%s", js.Code, js.Output)
	}
	var report struct {
		Errors int `json:"Errors"`
		Warns  int `json:"Warns"`
	}
	if err := json.Unmarshal([]byte(js.Stdout), &report); err != nil {
		t.Fatalf("lw lint --json: stdout does not parse as a lint report: %v\n%s", err, js.Stdout)
	}
	// 7 is EXPECTED-LINT.md's error row count, pinned — see this test's doc
	// comment.
	const wantErrors = 7
	if report.Errors != wantErrors {
		t.Errorf("lw lint --json: %d error(s) on the dirty fixture, want exactly %d\n%s", report.Errors, wantErrors, js.Stdout)
	}
}

// TestSmokeInit, TestSmokeConfig and TestSmokeDoctor stand in for the three
// verbs this build still ships as stubs. They are listed and skipped, never
// dropped, so the smoke suite's inventory stays complete: when a verb lands,
// its scenario replaces its placeholder instead of arriving unnoticed.
func TestSmokeInit(t *testing.T) {
	t.Skip("S6-T2/T3/T1 not landed: init/config/doctor are stubs (cmd_init.go:7, cmd_config.go:7, cmd_doctor.go:7)")
}

func TestSmokeConfig(t *testing.T) {
	t.Skip("S6-T2/T3/T1 not landed: init/config/doctor are stubs (cmd_init.go:7, cmd_config.go:7, cmd_doctor.go:7)")
}

func TestSmokeDoctor(t *testing.T) {
	t.Skip("S6-T2/T3/T1 not landed: init/config/doctor are stubs (cmd_init.go:7, cmd_config.go:7, cmd_doctor.go:7)")
}
