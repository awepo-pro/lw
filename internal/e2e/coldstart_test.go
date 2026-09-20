// coldstart_test.go drives the real lw binary through every verb that exists
// today — the automated replacement for gate G6's manual walkthrough. Each
// scenario is independent: one private sandbox from the task-2 helpers, one
// vault of its own, and no network but the fake LLM the ingest, query,
// config and doctor scenarios script.
package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// TestSmokeVersion runs `lw --version` and expects the stamped version
// string on stdout and nothing on stderr (cmd/lw/cmd_version.go).
func TestSmokeVersion(t *testing.T) {
	e := newEnv(t)

	res := runLW(t, e, "--version")
	if res.Code != 0 {
		t.Fatalf("lw --version: exit %d, want 0\n%s", res.Code, res.Output)
	}
	if got, want := strings.TrimSpace(res.Stdout), "lw "+e2eVersion; got != want {
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
// fake LLM: a local markdown source goes in, the scripted curator ingests it
// into raw/ and proposes one page citing exactly that raw path, and that
// changeset is reviewed, committed and read back out of the journal — the
// path a real session takes, with no network beyond localhost.
//
// Three rounds, not two: newVault seeds six pages and NO raw/ sources at all,
// so a page whose sources: cites a raw/ path that nothing ever staged is a
// src-integrity error the moment the changeset is committed — and S6-C127
// made a vault's first commit lint-gated on exactly that count. Round 1
// stages stage_ingest_source on the scratch path lw ingest's own message
// names (what a real model does with buildIngestMessage — C-817), round 2
// proposes the page citing the raw path that call produces, round 3 stops.
// testdata/ingest_round{1,2}.sse stay untouched: TestHarness's own two-round
// script never commits, so the dangling citation these round files also
// carry is not this fixture's to fix.
func TestSmokeIngestCommit(t *testing.T) {
	e := newEnv(t)
	vault := newVault(t)
	fake := newFakeLLM(t,
		stageIngestScratchSSE("smoke-ingest", 1),
		sseFixture(t, "smoke_ingest_round2.sse"),
		sseFixture(t, "smoke_ingest_round3.sse"),
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

// initDir builds a fresh empty directory under e's working directory and an
// *env whose subprocess cwd is that directory — `lw init` takes no --vault
// flag and always scaffolds the process's own working directory (README
// "Every verb ... takes -vault <dir> ... (init is the exception)"), so a
// scenario driving it needs a sandbox rooted there rather than at e.work.
func initDir(t *testing.T, e *env, name string) (*env, string) {
	t.Helper()

	dir := filepath.Join(e.work, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("e2e: create %s: %v", dir, err)
	}
	return &env{root: e.root, home: e.home, config: e.config, tmp: e.tmp, work: dir}, dir
}

// TestSmokeInit scaffolds a fresh vault with `lw init --schema`, checks every
// path README's Quickstart promises gets created, that the result lints
// clean and reports zero pages/raw sources, and that a second init into the
// now-occupied directory is refused without --force.
func TestSmokeInit(t *testing.T) {
	e := newEnv(t)
	ei, dir := initDir(t, e, "new-vault")

	init0 := runLW(t, ei, "init", "--schema", "ml-systems")
	if init0.Code != 0 {
		t.Fatalf("lw init --schema ml-systems: exit %d, want 0\n%s", init0.Code, init0.Output)
	}

	wantPaths := []string{
		"SCHEMA.md", "index.md", "log.md", "curator-memory.md",
		filepath.Join("raw", "articles"), filepath.Join("raw", "papers"),
		filepath.Join("raw", "transcripts"), filepath.Join("raw", "assets"),
		filepath.Join("wiki", "entities"), filepath.Join("wiki", "concepts"),
		filepath.Join("wiki", "comparisons"), filepath.Join("wiki", "queries"),
		".llmwiki",
	}
	for _, want := range wantPaths {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("lw init: %s was not created: %v", want, err)
		}
	}

	lint := runLW(t, e, "lint", "--vault", dir)
	if lint.Code != 0 {
		t.Fatalf("lw lint --vault (freshly initialized): exit %d, want 0 (clean)\n%s", lint.Code, lint.Output)
	}

	status := runLW(t, e, "status", "--vault", dir)
	if status.Code != 0 {
		t.Fatalf("lw status --vault (freshly initialized): exit %d, want 0\n%s", status.Code, status.Output)
	}
	if want := "0 pages"; !strings.Contains(status.Output, want) {
		t.Errorf("lw status: output does not say %q\n%s", want, status.Output)
	}
	if want := "0 raw"; !strings.Contains(status.Output, want) {
		t.Errorf("lw status: output does not say %q\n%s", want, status.Output)
	}

	// The directory is no longer empty: a second init without --force must
	// refuse it (README: "Refuses a non-empty directory unless --force").
	init1 := runLW(t, ei, "init", "--schema", "x")
	if init1.Code == 0 {
		t.Fatalf("lw init --schema x (occupied directory, no --force): exit 0, want non-zero\n%s", init1.Output)
	}
	assertNoPanic(t, init1.Output)
}

// configAPIKeyEnvRE matches `lw config`'s llm.api_key row naming its env:
// reference — loosely on whitespace, since writeConfigRows pads every key to
// the widest one in the table and that width shifts if the field list ever
// grows.
var configAPIKeyEnvRE = regexp.MustCompile(`llm\.api_key\s*=\s*env:`)

// TestSmokeConfig drives `lw config`'s show/set/path surface with no vault
// and no fake LLM at all — every one of these is offline (README's verb
// table). It never writes a real secret to disk: the one `set` this scenario
// tries with a key-shaped literal must be refused, and neither the config
// file nor any command output may ever carry it.
func TestSmokeConfig(t *testing.T) {
	e := newEnv(t)

	show := runLW(t, e, "config")
	if show.Code != 0 {
		t.Fatalf("lw config: exit %d, want 0\n%s", show.Code, show.Output)
	}
	for _, want := range []string{"llm.base_url", "llm.model"} {
		if !strings.Contains(show.Stdout, want) {
			t.Errorf("lw config: stdout does not mention %q\n%s", want, show.Stdout)
		}
	}
	if !configAPIKeyEnvRE.MatchString(show.Stdout) {
		t.Errorf("lw config: stdout does not show an llm.api_key = env:... row\n%s", show.Stdout)
	}

	set := runLW(t, e, "config", "set", "llm.model", "some-model")
	if set.Code != 0 {
		t.Fatalf("lw config set llm.model some-model: exit %d, want 0\n%s", set.Code, set.Output)
	}

	show2 := runLW(t, e, "config")
	if show2.Code != 0 {
		t.Fatalf("lw config (after set): exit %d, want 0\n%s", show2.Code, show2.Output)
	}
	if want := regexp.MustCompile(`some-model\s*\(file\)`); !want.MatchString(show2.Stdout) {
		t.Errorf("lw config: stdout does not show some-model marked (file) after set\n%s", show2.Stdout)
	}

	// A key-shaped literal is refused outright (README "Secrets"), and never
	// reaches the config file or any output — stdout, stderr, or disk.
	const secret = "sk-literal-secret-value"
	refused := runLW(t, e, "config", "set", "llm.api_key", secret)
	if refused.Code == 0 {
		t.Fatalf("lw config set llm.api_key <literal secret>: exit 0, want non-zero\n%s", refused.Output)
	}
	if strings.Contains(refused.Output, secret) {
		t.Errorf("lw config set llm.api_key: refusal output leaks the literal\n%s", refused.Output)
	}
	cfgPath := filepath.Join(e.config, "lw", "config.toml")
	if b, err := os.ReadFile(cfgPath); err == nil && strings.Contains(string(b), secret) {
		t.Errorf("config file %s carries the refused literal secret", cfgPath)
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read config file %s: %v", cfgPath, err)
	}

	path := runLW(t, e, "config", "path")
	if path.Code != 0 {
		t.Fatalf("lw config path: exit %d, want 0\n%s", path.Code, path.Output)
	}
	if got := strings.TrimSpace(path.Stdout); !strings.HasPrefix(got, e.config) {
		t.Errorf("lw config path: stdout %q is not under the env's config dir %s", got, e.config)
	}
}

// doctorProbeSSE is the single round `lw doctor`'s provider check consumes:
// a tool call and nothing else, so llm.Client.Probe reports the endpoint
// both reachable and tool-calling — the shape that makes doctor's own
// "provider" line read ✓ (backbone §8's Probe contract), preferred here over
// asserting on a ✗ the fake would otherwise manufacture for a reason that
// has nothing to do with what this scenario tests.
const doctorProbeSSE = "data: {\"id\":\"chatcmpl-e2e-doctor\",\"choices\":[{\"index\":0," +
	"\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_e2e_ping\"," +
	"\"type\":\"function\",\"function\":{\"name\":\"ping\",\"arguments\":\"{}\"}}]},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"chatcmpl-e2e-doctor\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
	"data: [DONE]\n"

// assertFixFollowsFailure checks doctorReport.writeText's own contract: every
// "✗ <name> ..." line is immediately followed by a "  fix: ..." line (see
// cmd_doctor.go's writeText) — never a bare failure with no stated remedy.
func assertFixFollowsFailure(t *testing.T, output string) {
	t.Helper()

	lines := strings.Split(output, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "✗") {
			continue
		}
		if i+1 >= len(lines) || !strings.HasPrefix(strings.TrimSpace(lines[i+1]), "fix:") {
			t.Errorf("lw doctor: failing line %q is not immediately followed by a fix: line\n%s", line, output)
		}
	}
}

// TestSmokeDoctor runs `lw doctor` over a freshly initialized vault pointed
// at the fake LLM (so the provider probe reports ✓ rather than a ✗ this
// scenario is not about), checks every check this build ships is either ✓ or
// paired with a `fix:` line, then injects the one fault that needs no
// provider — a stale lock file — and drives it through detect, --unlock,
// and confirm.
func TestSmokeDoctor(t *testing.T) {
	e := newEnv(t)
	ei, dir := initDir(t, e, "doctor-vault")

	init0 := runLW(t, ei, "init", "--schema", "ml-systems")
	if init0.Code != 0 {
		t.Fatalf("lw init --schema ml-systems: exit %d, want 0\n%s", init0.Code, init0.Output)
	}

	// One scripted round per doctor invocation below: each resolves the
	// literal api_key writeConfig wrote and probes it exactly once.
	fake := newFakeLLM(t,
		fakeRound{Name: "doctor_probe_1", Body: doctorProbeSSE},
		fakeRound{Name: "doctor_probe_2", Body: doctorProbeSSE},
		fakeRound{Name: "doctor_probe_3", Body: doctorProbeSSE},
		fakeRound{Name: "doctor_probe_4", Body: doctorProbeSSE},
	)
	writeConfig(t, e.config, fake.URL()+"/v1")

	assertCheckLines := func(t *testing.T, output string, names ...string) {
		t.Helper()
		for _, name := range names {
			re := regexp.MustCompile(`(?m)^(✓|✗) +` + regexp.QuoteMeta(name) + ` `)
			m := re.FindStringSubmatch(output)
			if m == nil {
				t.Errorf("lw doctor: no %s check line found\n%s", name, output)
				continue
			}
			if m[1] != "✓" {
				t.Errorf("lw doctor: %s check is %s, want ✓\n%s", name, m[1], output)
			}
		}
	}

	healthy := runLW(t, e, "doctor", "--vault", dir)
	if healthy.Code != 0 {
		t.Fatalf("lw doctor (freshly initialized): exit %d, want 0\n%s", healthy.Code, healthy.Output)
	}
	assertCheckLines(t, healthy.Output, "index", "objects", "journal", "recovery", "lock")
	assertFixFollowsFailure(t, healthy.Output)

	// Inject a lock fault that needs no provider: an unparsable pid makes
	// checkLock report it stale without touching the network at all.
	lockPath := filepath.Join(dir, ".llmwiki", "lock")
	if err := os.WriteFile(lockPath, []byte("not-a-pid\n"), 0o600); err != nil {
		t.Fatalf("e2e: write stale lock %s: %v", lockPath, err)
	}

	faulted := runLW(t, e, "doctor", "--vault", dir)
	if faulted.Code == 0 {
		t.Fatalf("lw doctor (stale lock): exit 0, want non-zero\n%s", faulted.Output)
	}
	if !regexp.MustCompile(`(?m)^✗ +lock `).MatchString(faulted.Output) {
		t.Errorf("lw doctor: no ✗ lock line for the stale lock\n%s", faulted.Output)
	}
	if !strings.Contains(faulted.Output, "--unlock") {
		t.Errorf("lw doctor: stale-lock output does not name --unlock as the fix\n%s", faulted.Output)
	}
	assertFixFollowsFailure(t, faulted.Output)
	assertNoPanic(t, faulted.Output)

	unlocked := runLW(t, e, "doctor", "--unlock", "--vault", dir)
	if unlocked.Code != 0 {
		t.Fatalf("lw doctor --unlock: exit %d, want 0\n%s", unlocked.Code, unlocked.Output)
	}

	confirmed := runLW(t, e, "doctor", "--vault", dir)
	if confirmed.Code != 0 {
		t.Fatalf("lw doctor (after --unlock): exit %d, want 0\n%s", confirmed.Code, confirmed.Output)
	}
	assertCheckLines(t, confirmed.Output, "lock")
}
