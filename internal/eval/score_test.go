package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/trace"
	"github.com/awepo-pro/lw/internal/vault"
)

// scMetrics checks one result's metric map against the expected one: the same
// keys (a metric that does not apply must be ABSENT, not 0) and the same
// values to 1e-9.
func scMetrics(t *testing.T, who string, got, want map[string]float64) {
	t.Helper()
	var gk, wk []string
	for k := range got {
		gk = append(gk, k)
	}
	for k := range want {
		wk = append(wk, k)
	}
	sort.Strings(gk)
	sort.Strings(wk)
	if !reflect.DeepEqual(gk, wk) {
		t.Errorf("%s: metrics %v, want exactly %v", who, gk, wk)
		return
	}
	for _, k := range wk {
		if math.Abs(got[k]-want[k]) > 1e-9 {
			t.Errorf("%s: %s = %v, want %v", who, k, got[k], want[k])
		}
	}
}

// scProcess is the process metrics of the trace the fake lw writes: one
// round, 100 input and 10 output tokens, no tool calls, a 5 ms turn.
func scProcess(m map[string]float64) map[string]float64 {
	for k, v := range map[string]float64{
		"rounds": 1, "max_rounds_hit": 0, "input_tokens": 100, "cached_tokens": 0,
		"output_tokens": 10, "reasoning_tokens": 0, "wall_s": 0.005,
	} {
		m[k] = v
	}
	return m
}

// TestScoreRun pins the score pass on T1's own runner artifacts, regenerated
// here with the same fake lw: one result per (case, index), each holding
// exactly the C5 metrics that apply to it. One ask run is made to fail, so
// the failed record is checked too. The ingest facts are rewritten AFTER the
// run — scoring reads the current cases.toml, which is what lets a fact fix
// be re-scored without another provider call (037 T3).
func TestScoreRun(t *testing.T) {
	set := newRunSet(t)
	f := newFakeLW(t)
	f.exit = func(key string, attempt int) int {
		if key == "no-topic-2" {
			return 1 // both attempts: a failed run
		}
		return 0
	}
	answers := map[string]string{
		"kv-cache-1": "The KV cache stores keys and values for earlier tokens ^[raw/articles/kv-cache-explained.md]; " +
			"see wiki/concepts/kv-cache.md and wiki/concepts/nope.md.\n",
		"kv-cache-2": "Only the keys are kept.\n",
		"no-topic-1": "Not from your vault: nothing about the 2030 cup.\n",
		"no-topic-2": "Not from your vault (but this run failed).\n",
	}
	out := filepath.Join(set.Dir, "runs", "20261005T120000Z-v9.9.9-test")
	r := newRunner(set, f, out)
	r.N = 2
	r.Exec = func(ctx context.Context, c Cmd) (Output, error) {
		o, err := f.Exec(ctx, c)
		if len(c.Args) == 4 {
			if s, ok := answers[filepath.Base(c.Args[2])]; ok {
				o.Stdout = []byte(s)
			}
		}
		return o, err
	}
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	casesPath := filepath.Join(set.Dir, "cases.toml")
	b, err := os.ReadFile(casesPath)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(b), `facts = [["speculative"]]`, `facts = [["second note"], ["nonexistent fact"]]`, 1)
	if edited == string(b) {
		t.Fatal("the ingest case's facts were not found to rewrite")
	}
	if err := os.WriteFile(casesPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Score(out)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(out, "results.json"))
	if err != nil {
		t.Fatalf("results.json not written: %v", err)
	}
	if !strings.HasPrefix(string(raw), "{\n  \"run\": {\n") || !strings.HasSuffix(string(raw), "}\n") {
		t.Errorf("results.json is not 2-space indented JSON with a trailing newline:\n%.200s", raw)
	}
	var file struct {
		Run       RunInfo `json:"run"`
		SetSHA256 string  `json:"set_sha256"`
		Results   []struct {
			Case    string             `json:"case"`
			Index   int                `json:"index"`
			Verb    string             `json:"verb"`
			Kind    string             `json:"kind"`
			Failed  bool               `json:"failed"`
			Metrics map[string]float64 `json:"metrics"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("results.json: %v", err)
	}
	var runInfo RunInfo
	readJSON(t, filepath.Join(out, "run.json"), &runInfo)
	if !reflect.DeepEqual(file.Run, runInfo) {
		t.Errorf("results.json run = %+v, want run.json's %+v", file.Run, runInfo)
	}
	// A-037-8: results.json stamps the cases.toml it was scored against —
	// the edited one here, not the one the run started with.
	if want := sha256Hex([]byte(edited)); file.SetSHA256 != want || res.SetSHA256 != want {
		t.Errorf("set_sha256 = %q (returned %q), want the scored cases.toml's %q", file.SetSHA256, res.SetSHA256, want)
	}
	if file.SetSHA256 == runInfo.SetSHA256 {
		t.Errorf("the stamp equals the run-time set_sha256 %q although cases.toml changed after the run", runInfo.SetSHA256)
	}
	if !strings.Contains(string(raw), `"metrics": {}`) {
		t.Errorf("a failed run's metrics are not an empty object:\n%s", raw)
	}

	// The ingest oracle: lint.json's warn count and the live op count, read
	// from the artifacts T1 wrote.
	var lf struct {
		Warns int `json:"warns"`
	}
	readJSON(t, filepath.Join(out, "paper-text", "1", "lint.json"), &lf)

	type want struct {
		case_, verb, kind string
		index             int
		failed            bool
		metrics           map[string]float64
	}
	ingest := func() map[string]float64 {
		// A-049-1: the 049 ingest metrics. The one staged page is new and its
		// slug is nobody's near-duplicate; lint.json holds a link-orphan on
		// it; the fake's turn makes no tool call and no existing page was
		// staged (patched_lossless is absent).
		return scProcess(map[string]float64{
			"fact_recall": 0.5, "chunk_coverage": 0, "ops": 4, "pages_staged": 1, "lint_warns": float64(lf.Warns),
			"pages_new": 1, "dup_pages": 0, "orphans_new": 1,
			"reads_before_first_stage": 0, "read_refusals": 0, "closed": 0, "search_calls": 0,
		})
	}
	wants := []want{
		{"kv-cache", "query", "covered", 1, false, scProcess(map[string]float64{
			"fact_recall": 1, "cite_valid": 2.0 / 3, "cite_expected": 1,
		})},
		{"kv-cache", "query", "covered", 2, false, scProcess(map[string]float64{
			"fact_recall": 0.5, "cite_expected": 0,
		})},
		{"no-topic", "query", "absent", 1, false, scProcess(map[string]float64{"abstain_ok": 1})},
		{"no-topic", "query", "absent", 2, true, map[string]float64{}},
		{"paper-text", "ingest", "ingest", 1, false, ingest()},
		{"paper-text", "ingest", "ingest", 2, false, ingest()},
	}
	if len(file.Results) != len(wants) || len(res.Results) != len(wants) {
		t.Fatalf("%d results in the file, %d returned; want %d (one per case and index, holdouts excluded)",
			len(file.Results), len(res.Results), len(wants))
	}
	for i, w := range wants {
		g := file.Results[i]
		who := fmt.Sprintf("%s/%d", w.case_, w.index)
		if g.Case != w.case_ || g.Index != w.index || g.Verb != w.verb || g.Kind != w.kind || g.Failed != w.failed {
			t.Errorf("result %d = %s/%d %s %s failed=%v; want %s", i, g.Case, g.Index, g.Verb, g.Kind, g.Failed, who)
		}
		scMetrics(t, who, g.Metrics, w.metrics)
		scMetrics(t, who+" (returned)", res.Results[i].Metrics, w.metrics)
	}
	// Scoring is deterministic and re-runnable: the same bytes again, and
	// LoadResults reads back what Score wrote.
	if _, err := Score(out); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(filepath.Join(out, "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, again) {
		t.Errorf("scoring twice gave different results.json bytes")
	}
	loaded, err := LoadResults(out)
	if err != nil {
		t.Fatalf("LoadResults: %v", err)
	}
	a, _ := json.Marshal(loaded)
	b2, _ := json.Marshal(res)
	if !bytes.Equal(a, b2) {
		t.Errorf("LoadResults differs from Score's own return")
	}
}

// scNewRun makes an empty run directory <set>/runs/<id> holding a run.json
// that matches the set's snapshot.
func scNewRun(t *testing.T, set *Set, id string) string {
	t.Helper()
	dir := filepath.Join(set.Dir, "runs", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	info := RunInfo{
		ID: id, LW: "lw-fake", LWVersion: "lw v9.9.9-test", SnapshotSHA256: set.SnapshotSHA256,
		N: 1, Parallel: 1, EnvKeys: []string{},
		Started: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC), Finished: time.Date(2026, 10, 5, 12, 1, 0, 0, time.UTC),
	}
	if err := writeJSON(filepath.Join(dir, "run.json"), info); err != nil {
		t.Fatal(err)
	}
	return dir
}

// scWriteCase lays down one (case, index) directory: meta.json from meta,
// and every file in files (slash path relative to the case dir).
func scWriteCase(t *testing.T, runDir string, meta RunMeta, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(runDir, meta.Case, fmt.Sprint(meta.Index))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeJSON(filepath.Join(dir, "meta.json"), meta); err != nil {
		t.Fatal(err)
	}
	return dir
}

// scRewriteCases rewrites the set's cases.toml with a new body, keeping the
// snapshot lines that LoadSet verifies.
func scRewriteCases(t *testing.T, set *Set, body string) {
	t.Helper()
	text := fmt.Sprintf("version = 1\nsnapshot = %q\nsnapshot_sha256 = %q\n\n%s", set.Snapshot, set.SnapshotSHA256, body)
	if err := os.WriteFile(filepath.Join(set.Dir, "cases.toml"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// scAnsweredMeta is the meta.json of a run that finished.
func scAnsweredMeta(id, verb, kind string, index int, traces ...string) RunMeta {
	if traces == nil {
		traces = []string{}
	}
	return RunMeta{Case: id, Verb: verb, Kind: kind, Index: index, Attempts: 1, Traces: traces}
}

// scResult finds the result of (case, index).
func scResult(t *testing.T, res *Results, id string, index int) CaseResult {
	t.Helper()
	for _, r := range res.Results {
		if r.Case == id && r.Index == index {
			return r
		}
	}
	t.Fatalf("no result for %s/%d in %+v", id, index, res.Results)
	return CaseResult{}
}

// TestScoreAskMetrics pins the ask metrics on answers written by hand:
// abstention, a ref that spoils it, the ref check against the snapshot vault,
// and cite_expected, which counts a citation as a citation even when the page
// it names does not exist (037 T3).
func TestScoreAskMetrics(t *testing.T) {
	set := newRunSet(t)
	scRewriteCases(t, set, `[[ask]]
id = "kv-cache"
kind = "covered"
q = "How does the KV cache work?"
facts = [["keys"], ["values"], ["re:\\bquantum\\b"]]
cite_any = ["wiki/concepts/kv-cache.md"]

[[ask]]
id = "no-topic"
kind = "absent"
q = "Who won the 2030 cup?"
`)
	run := scNewRun(t, set, "r1")
	ask := func(id, kind string, index int, stdout string) {
		scWriteCase(t, run, scAnsweredMeta(id, "query", kind, index), map[string]string{"stdout.txt": stdout})
	}
	ask("kv-cache", "covered", 1,
		"Keys and VALUES are cached ^[raw/articles/kv-cache-explained.md] ^[raw/articles/kv-cache-explained.md p.1] "+
			"and `^[raw/never.md]` in code does not count.\n")
	ask("kv-cache", "covered", 2, "It stores values; see wiki/concepts/kv-cache.md.\n")
	ask("no-topic", "absent", 1, "Not from your vault: see wiki/concepts/kv-cache.md for something else.\n")
	ask("no-topic", "absent", 2, "NOT FROM\nYOUR VAULT: nothing.\n")
	ask("no-topic", "absent", 3, "The 2030 cup was won by nobody yet.\n")

	res, err := Score(run)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	// run 1: facts keys+values hit, the regexp fact misses -> 2/3. Refs: the
	// pageless marker resolves, the paged one does not (no anchors) -> 1/2.
	// The code-span marker is no ref. cite_any wants the wiki path: 0.
	scMetrics(t, "kv-cache/1", scResult(t, res, "kv-cache", 1).Metrics, map[string]float64{
		"fact_recall": 2.0 / 3, "cite_valid": 0.5, "cite_expected": 0,
	})
	// run 2: values only -> 1/3; one valid prose path; cites the expected page.
	scMetrics(t, "kv-cache/2", scResult(t, res, "kv-cache", 2).Metrics, map[string]float64{
		"fact_recall": 1.0 / 3, "cite_valid": 1, "cite_expected": 1,
	})
	// A-037-3: naming a wiki page is not citing evidence — the abstention
	// stands (and the ref is still checked: it resolves).
	scMetrics(t, "no-topic/1", scResult(t, res, "no-topic", 1).Metrics, map[string]float64{
		"abstain_ok": 1, "cite_valid": 1,
	})
	// Whitespace and case do not stop the phrase matching.
	scMetrics(t, "no-topic/2", scResult(t, res, "no-topic", 2).Metrics, map[string]float64{"abstain_ok": 1})
	scMetrics(t, "no-topic/3", scResult(t, res, "no-topic", 3).Metrics, map[string]float64{"abstain_ok": 0})
}

// scIngestBody is a raw source body of two chunks with page anchors 1 and 2.
func scIngestBody(t *testing.T) string {
	t.Helper()
	body := "<!-- page 1 -->\n" + strings.Repeat("Speculative drafting words. ", 300) +
		"\n\n<!-- page 2 -->\n" + strings.Repeat("Verification words follow here. ", 600) + "\n"
	if n := tools.RawChunkCount(body); n != 2 {
		t.Fatalf("the fixture body has %d chunks, want 2", n)
	}
	return body
}

// scWriteIngestTrace writes one traced turn under caseDir/traces with the
// real trace.Start and Recorder: round 1 reads chunk 1 of the staged source
// and a source that does not exist (a failing call), round 2 answers.
func scWriteIngestTrace(t *testing.T, caseDir string) string {
	t.Helper()
	dir := filepath.Join(caseDir, "traces")
	id := "20260101T000001Z-0001"
	_, rec := trace.Start(context.Background(), dir, id, trace.Meta{
		Verb: "ingest", Session: "ingest", Version: "v9.9.9-test", Model: "glm-test", MaxRounds: 6,
	}, 0)
	if rec == nil {
		t.Fatal("trace.Start returned no recorder")
	}
	body, err := json.Marshal(map[string]any{
		"model": "glm-test", "stream": true,
		"messages": []llm.Message{{Role: "system", Content: "You are the curator."}, {Role: "user", Content: "ingest"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec.BeginRequest(1, 1, 2, 0)
	rec.Request(body)
	rec.Response(trace.Response{
		Round: 1, Attempt: 1, Finish: "tool_calls",
		ToolCalls: []trace.ToolCall{
			{ID: "c1", Name: "raw_get", Arguments: `{"source":"raw/articles/new-source.md"}`},
			{ID: "c2", Name: "raw_get", Arguments: `{"source":"raw/articles/gone.md"}`},
		},
		Usage: &llm.Usage{InputTokens: 50, OutputTokens: 5},
	})
	rec.Tool(trace.Tool{Round: 1, ID: "c1", Name: "raw.get"})
	rec.Tool(trace.Tool{Round: 1, ID: "c2", Name: "raw.get", IsError: true})
	rec.BeginRequest(2, 1, 4, 0)
	rec.Request(body)
	rec.Response(trace.Response{
		Round: 2, Attempt: 1, Finish: "stop",
		Usage: &llm.Usage{InputTokens: 70, OutputTokens: 15, CachedTokens: 40, ReasoningTokens: 8},
	})
	rec.Done(trace.Done{Reason: "stop", Rounds: 2, WallMS: 20})
	return id
}

// TestScoreIngestMetrics pins the ingest metrics on artifacts written by
// hand: the staged pages are scored by their BODY (a fact that is only in a
// tag, or a path that is only in the sources list, is not content), refs
// resolve against staged/ as well as the snapshot, chunk coverage counts the
// live ingest_source ops only, and the process numbers come from the trace
// (037 T3).
func TestScoreIngestMetrics(t *testing.T) {
	set := newRunSet(t)
	scRewriteCases(t, set, `[[ingest]]
id = "paper-text"
input = "inputs/paper.txt"
facts = [["speculative"], ["inference"], ["verifies them"]]
`)
	run := scNewRun(t, set, "r1")
	body := scIngestBody(t)
	d, _ := vault.ParseDate("2026-10-05")
	rawSrc := (&vault.RawSource{
		SourceURL: "https://example.test/new-source", Ingested: d, SHA256: vault.BodySHA256(body), Body: body,
	}).Serialize()

	page := func(title, text string) string {
		return "---\ntitle: " + title + "\ncreated: 2026-10-05\nupdated: 2026-10-05\ntype: concept\n" +
			"tags: [inference]\nsources: [raw/articles/new-source.md]\nconfidence: medium\n---\n\n# " + title + "\n\n" + text
	}
	caseDir := filepath.Join(run, "paper-text", "1")
	turn := scWriteIngestTrace(t, caseDir)
	scWriteCase(t, run, scAnsweredMeta("paper-text", "ingest", "ingest", 1, turn), map[string]string{
		"stdout.txt":                        "ingested\n",
		"staged/raw/articles/new-source.md": string(rawSrc),
		"staged/wiki/concepts/b.md":         page("B", "Nothing cited here, but raw/articles/ghost.md is named in prose.\n"),
		"staged/wiki/concepts/a.md": page("A", "Speculative decoding drafts tokens.^[raw/articles/new-source.md p.2] "+
			"It then verifies them.^[raw/articles/new-source.md p.9] "+
			"See ^[raw/articles/kv-cache-explained.md] and wiki/concepts/kv-cache.md.\n"),
		"lint.json": `{"errors":0,"warns":3,"findings":[]}`,
	})
	if err := writeJSON(filepath.Join(caseDir, "changeset.json"), changesetFile{ID: "cs1", Ops: []changesetOp{
		{ID: "op1", Op: "ingest_source", Path: "raw/articles/new-source.md", State: "proposed"},
		{ID: "op2", Op: "ingest_source", Path: "raw/articles/old.md", State: "dropped"},
		{ID: "op3", Op: "create_page", Path: "wiki/concepts/a.md", State: "proposed"},
		{ID: "op4", Op: "create_page", Path: "wiki/concepts/b.md", State: "proposed"},
		{ID: "op5", Op: "patch_page", Path: "curator-memory.md", State: "proposed"},
	}}); err != nil {
		t.Fatal(err)
	}

	res, err := Score(run)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	scMetrics(t, "paper-text/1", scResult(t, res, "paper-text", 1).Metrics, map[string]float64{
		// speculative + verifies them hit; "inference" is only a tag.
		"fact_recall": 2.0 / 3,
		// a.md: p.2 ok, p.9 past the last anchor (2), kv-cache-explained ok,
		// kv-cache page ok; b.md: ghost.md missing -> 3 of 5. The sources
		// list in the frontmatter adds no refs.
		"cite_valid": 3.0 / 5,
		// chunk 1 of 2 read on the one live ingest_source; the dropped one
		// has no staged file and adds nothing.
		"chunk_coverage":   0.5,
		"tool_error_rate":  0.5,
		"rounds":           2,
		"max_rounds_hit":   0,
		"input_tokens":     120,
		"cached_tokens":    40,
		"output_tokens":    20,
		"reasoning_tokens": 8,
		"wall_s":           0.02,
		// op2 is dropped: 4 live ops.
		"ops":          4,
		"pages_staged": 2,
		"lint_warns":   3,
		// A-049-2: the 049 ingest metrics. Both staged pages are new (a.md
		// and b.md share no token with a snapshot slug), lint.json lists no
		// finding, and the trace never reads the wiki, searches, closes or is
		// refused — its one failing call is a raw.get.
		"pages_new": 2, "dup_pages": 0, "orphans_new": 0,
		"reads_before_first_stage": 0, "read_refusals": 0, "closed": 0, "search_calls": 0,
	})
}

// TestScoreIngestNothingStaged pins an ingest that exited 0 and staged
// nothing: it is a result, not a failure — fact_recall 0, no ops — and the
// metrics that need an artifact that is not there are absent (037 T3).
func TestScoreIngestNothingStaged(t *testing.T) {
	set := newRunSet(t)
	run := scNewRun(t, set, "r1")
	scWriteCase(t, run, scAnsweredMeta("paper-text", "ingest", "ingest", 1), map[string]string{"stdout.txt": "nothing to do\n"})
	res, err := Score(run)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	// A-049-3: pages_new and dup_pages are values even at 0; everything that
	// needs a trace, a lint.json or a pre-existing staged page is absent.
	scMetrics(t, "paper-text/1", scResult(t, res, "paper-text", 1).Metrics, map[string]float64{
		"fact_recall": 0, "ops": 0, "pages_staged": 0, "pages_new": 0, "dup_pages": 0,
	})
}

// TestScoreRefusals pins what scoring will not do silently: a run of another
// snapshot, a case the set no longer has, an unfinished job, a damaged
// trace, a run directory it cannot place (037 T3).
func TestScoreRefusals(t *testing.T) {
	set := newRunSet(t)

	t.Run("run.json missing", func(t *testing.T) {
		dir := filepath.Join(set.Dir, "runs", "empty")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := Score(dir)
		if err == nil || !strings.Contains(err.Error(), "run.json") {
			t.Errorf("err = %v, want one naming run.json", err)
		}
	})

	t.Run("snapshot of another vault", func(t *testing.T) {
		dir := scNewRun(t, set, "other-vault")
		var info RunInfo
		readJSON(t, filepath.Join(dir, "run.json"), &info)
		info.SnapshotSHA256 = strings.Repeat("0", 64)
		if err := writeJSON(filepath.Join(dir, "run.json"), info); err != nil {
			t.Fatal(err)
		}
		_, err := Score(dir)
		if err == nil || !strings.Contains(err.Error(), "snapshot") {
			t.Errorf("err = %v, want a snapshot mismatch", err)
		}
	})

	t.Run("a case the set does not have", func(t *testing.T) {
		dir := scNewRun(t, set, "ghost-case")
		scWriteCase(t, dir, scAnsweredMeta("ghost", "query", "covered", 1), map[string]string{"stdout.txt": "x"})
		_, err := Score(dir)
		if err == nil || !strings.Contains(err.Error(), `"ghost"`) || !strings.Contains(err.Error(), "cases.toml") {
			t.Errorf("err = %v, want one naming the case and cases.toml", err)
		}
	})

	t.Run("a job that did not finish", func(t *testing.T) {
		dir := scNewRun(t, set, "unfinished")
		if err := os.MkdirAll(filepath.Join(dir, "kv-cache", "1"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := Score(dir)
		if err == nil || !strings.Contains(err.Error(), "no meta.json") {
			t.Errorf("err = %v, want one saying meta.json is missing", err)
		}
	})

	t.Run("a trace that is gone", func(t *testing.T) {
		dir := scNewRun(t, set, "no-trace")
		scWriteCase(t, dir, scAnsweredMeta("kv-cache", "query", "covered", 1, "20260101T000001Z-0001"), map[string]string{"stdout.txt": "x"})
		_, err := Score(dir)
		if err == nil || !strings.Contains(err.Error(), "20260101T000001Z-0001") {
			t.Errorf("err = %v, want one naming the turn", err)
		}
	})

	t.Run("a run directory outside <set>/runs", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "loose")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := Score(dir)
		if err == nil || !strings.Contains(err.Error(), "runs") {
			t.Errorf("err = %v, want one explaining the <set>/runs/<id> layout", err)
		}
	})

	t.Run("ScoreSet takes the set explicitly", func(t *testing.T) {
		// The same loose layout works when the set is given.
		loose := filepath.Join(t.TempDir(), "loose")
		if err := os.MkdirAll(loose, 0o755); err != nil {
			t.Fatal(err)
		}
		info := RunInfo{ID: "loose", SnapshotSHA256: set.SnapshotSHA256, EnvKeys: []string{}}
		if err := writeJSON(filepath.Join(loose, "run.json"), info); err != nil {
			t.Fatal(err)
		}
		scWriteCase(t, loose, scAnsweredMeta("kv-cache", "query", "covered", 1), map[string]string{"stdout.txt": "keys and values"})
		res, err := ScoreSet(loose, set)
		if err != nil {
			t.Fatalf("ScoreSet: %v", err)
		}
		if got := scResult(t, res, "kv-cache", 1).Metrics["fact_recall"]; got != 1 {
			t.Errorf("fact_recall = %v, want 1", got)
		}
	})
}

// TestAbstainOKShapes pins A-037-3 on the shapes lw really answers an
// out-of-vault question with. Its DESIGNED behaviour is the "Not from your
// vault:" label followed by a knowledge answer, which may name the wiki pages
// the vault does hold; what makes an abstention dirty is pointing at RAW
// evidence — a marker or a raw/ path — for the part the vault does not
// cover (037 T3).
func TestAbstainOKShapes(t *testing.T) {
	set := newRunSet(t)
	scRewriteCases(t, set, `[[ask]]
id = "no-topic"
kind = "absent"
q = "Who won the 2030 cup?"
`)
	run := scNewRun(t, set, "r1")
	shapes := []struct {
		name, stdout string
		want         float64
	}{
		{"label, figures, no refs", "Not from your vault: Llama 3 has 8B and 70B parameters, trained on 15T tokens.\n", 1},
		{"label and a wikilink to a wiki page", "Not from your vault: for the general idea see [[wiki/queries/cups]].\n", 1},
		{"label and a wiki path in prose", "Not from your vault: the vault only has wiki/concepts/kv-cache.md on caching.\n", 1},
		{"label and a wiki page marker", "Not from your vault: related ^[wiki/concepts/kv-cache.md].\n", 1},
		{"label and a raw marker", "Not from your vault: the winner was X ^[raw/articles/kv-cache-explained.md].\n", 0},
		{"label and a paged raw marker", "Not from your vault: see ^[raw/papers/leviathan-2023.md p.2].\n", 0},
		{"label and a raw path in prose", "Not from your vault: compare raw/articles/kv-cache-explained.md.\n", 0},
		{"label and a wikilink to a raw source", "Not from your vault: [[raw/articles/kv-cache-explained]].\n", 0},
		{"label, a wiki link and a raw marker", "Not from your vault: [[wiki/queries/cups]] and ^[raw/a.md].\n", 0},
		{"no label", "The 2030 cup was won by nobody yet.\n", 0},
		{"no label, with a wiki link", "See [[wiki/queries/cups]].\n", 0},
		{"the label in the middle", "Short answer first. Not from your vault: nothing on this.\n", 1},
	}
	for i, sh := range shapes {
		scWriteCase(t, run, scAnsweredMeta("no-topic", "query", "absent", i+1), map[string]string{"stdout.txt": sh.stdout})
	}
	res, err := Score(run)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	for i, sh := range shapes {
		got, ok := scResult(t, res, "no-topic", i+1).Metrics["abstain_ok"]
		if !ok || got != sh.want {
			t.Errorf("%s: abstain_ok = %v (present %v), want %v", sh.name, got, ok, sh.want)
		}
	}
}

// TestScoreWikilinks pins A-037-4 in the score pass: a wikilink counts in
// cite_valid like any ref and is checked against the vault (".md" appended
// when missing), but it never satisfies cite_expected, which measures citing
// evidence (037 T3).
func TestScoreWikilinks(t *testing.T) {
	set := newRunSet(t)
	scRewriteCases(t, set, `[[ask]]
id = "kv-cache"
kind = "covered"
q = "How does the KV cache work?"
facts = [["keys"]]
cite_any = ["wiki/concepts/kv-cache.md", "raw/articles/kv-cache-explained.md"]
`)
	run := scNewRun(t, set, "r1")
	ask := func(i int, stdout string) {
		scWriteCase(t, run, scAnsweredMeta("kv-cache", "query", "covered", i), map[string]string{"stdout.txt": stdout})
	}
	// 1: one wikilink that resolves, one that does not.
	ask(1, "Keys are cached; see [[wiki/concepts/kv-cache]] and [[wiki/queries/missing|more]].\n")
	// 2: wikilinks to the expected targets, written out in full.
	ask(2, "Keys: [[wiki/concepts/kv-cache.md]] and [[raw/articles/kv-cache-explained.md]].\n")
	// 3: a wikilink plus a real marker to an expected source.
	ask(3, "Keys: [[wiki/concepts/kv-cache]] ^[raw/articles/kv-cache-explained.md].\n")
	res, err := Score(run)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	scMetrics(t, "1", scResult(t, res, "kv-cache", 1).Metrics, map[string]float64{
		"fact_recall": 1, "cite_valid": 0.5, "cite_expected": 0,
	})
	scMetrics(t, "2", scResult(t, res, "kv-cache", 2).Metrics, map[string]float64{
		"fact_recall": 1, "cite_valid": 1, "cite_expected": 0,
	})
	scMetrics(t, "3", scResult(t, res, "kv-cache", 3).Metrics, map[string]float64{
		"fact_recall": 1, "cite_valid": 1, "cite_expected": 1,
	})
}
