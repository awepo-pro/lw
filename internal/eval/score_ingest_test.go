package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/trace"
	"github.com/awepo-pro/lw/internal/vault"
)

// scRefusalText is 048's refusal of a wiki read over the ingest budget, as
// internal/agent/readbudget.go formats it for ("wiki.get", 6). The scorer
// recognises it by a needle in internal/eval/score, whose own test reads the
// agent's source to keep that needle honest; this copy only has to be what a
// refused call's tool message holds.
const scRefusalText = "wiki.get refused: this ingest has read 6 wiki pages since it last staged a change. Stage the pages for the source now (stage.create_page / stage.patch_page) from what you have read; wiki reads are allowed again after a change is staged."

// newIngestMetrics are the eight metrics 049 added for ingest runs, in the order they print.
var newIngestMetrics = []string{
	MetricReadsBeforeFirstStage, MetricReadRefusals, MetricClosed, MetricSearchCalls,
	MetricPagesNew, MetricPatchedLossless, MetricDupPages, MetricOrphansNew,
}

// scOnly keeps the 049 metrics of m, so a test about them does not restate
// the process numbers of the trace it happens to carry.
func scOnly(m map[string]float64) map[string]float64 {
	out := map[string]float64{}
	for _, k := range newIngestMetrics {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return out
}

// scPage is a wiki page file: frontmatter, then body.
func scPage(title, body string) string {
	return "---\ntitle: " + title + "\ncreated: 2026-10-05\nupdated: 2026-10-05\ntype: concept\n" +
		"tags: [inference]\nconfidence: medium\n---\n\n" + body
}

const (
	scTilelangBody = "# TileLang\n\nTileLang compiles kernels.  \n\n## Notes\n\nFirst note.\nFirst note.\n"
	scHTTPBody     = "x\ny\n"
)

// newPagesSet snapshots the minimal fixture vault plus the two pages 049's
// frozen scoring test needs — wiki/concepts/http-version.md (body "x\ny") and
// wiki/entities/tilelang.md, plus any extraPages (paths) a test adds — into a
// set with one single-input ingest case, and loads it.
func newPagesSet(t *testing.T, extraPages ...string) *Set {
	t.Helper()
	vaultDir := testutil.CopyFixture(t, "minimal")
	pages := map[string]string{
		"wiki/concepts/http-version.md": scPage("HTTP Version", scHTTPBody),
		"wiki/entities/tilelang.md":     scPage("TileLang", scTilelangBody),
	}
	for _, p := range extraPages { // more snapshot pages, by vault path
		pages[p] = scPage(p, "Filler.\n")
	}
	for p, content := range pages {
		if err := os.WriteFile(filepath.Join(vaultDir, filepath.FromSlash(p)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	setDir := t.TempDir()
	sha, err := Snapshot(vaultDir, filepath.Join(setDir, "vault-20261005.tar.gz"))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(setDir, "inputs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(setDir, "inputs", "paper.txt"), []byte("HTTP versions.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := fmt.Sprintf("version = 1\nsnapshot = \"vault-20261005.tar.gz\"\nsnapshot_sha256 = %q\n\n[[ingest]]\nid = \"paper-text\"\ninput = \"inputs/paper.txt\"\n", sha)
	if err := os.WriteFile(filepath.Join(setDir, "cases.toml"), []byte(cases), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := LoadSet(setDir)
	if err != nil {
		t.Fatalf("LoadSet: %v", err)
	}
	return set
}

// scLLMCall builds a tool call in the wire shape.
func scLLMCall(id, name, args string) llm.ToolCall {
	tc := llm.ToolCall{ID: id, Type: "function"}
	tc.Function.Name, tc.Function.Arguments = name, args
	return tc
}

// scWriteReadingTrace writes one ingest turn with the real Recorder: round 1
// is two wiki.get calls and a THIRD wiki.get that the 048 budget refused (the
// tool event is_error, the refusal text in round 2's request body as the
// role:"tool" message), round 2 a create_page and a wiki.search, round 3 an
// ok stage.close and the answer. It returns the turn id.
func scWriteReadingTrace(t *testing.T, caseDir string) string {
	t.Helper()
	dir := filepath.Join(caseDir, "traces")
	id := "20260101T000001Z-0001"
	_, rec := trace.Start(context.Background(), dir, id, trace.Meta{
		Verb: "ingest", Session: "ingest", Version: "v9.9.9-test", Model: "glm-test", MaxRounds: 8,
	}, 0)
	if rec == nil {
		t.Fatal("trace.Start returned no recorder")
	}
	req := func(extra ...llm.Message) []byte {
		msgs := append([]llm.Message{{Role: "system", Content: "You are the curator."}, {Role: "user", Content: "ingest"}}, extra...)
		b, err := json.Marshal(map[string]any{"model": "glm-test", "stream": true, "messages": msgs})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	round1 := []llm.ToolCall{
		scLLMCall("c1", "wiki_get", `{"path":"wiki/concepts/http-version.md"}`),
		scLLMCall("c2", "wiki_get", `{"path":"wiki/entities/tilelang.md"}`),
		scLLMCall("c3", "wiki_get", `{"path":"wiki/concepts/kv-cache.md"}`),
	}
	rec.BeginRequest(1, 1, 2, 0)
	rec.Request(req())
	rec.Tool(trace.Tool{Round: 1, ID: "c1", Name: "wiki.get"})
	rec.Tool(trace.Tool{Round: 1, ID: "c2", Name: "wiki.get"})
	rec.Tool(trace.Tool{Round: 1, ID: "c3", Name: "wiki.get", IsError: true})
	rec.Response(trace.Response{Round: 1, Attempt: 1, Finish: "tool_calls", ToolCalls: []trace.ToolCall{
		{ID: "c1", Name: "wiki_get", Arguments: round1[0].Function.Arguments},
		{ID: "c2", Name: "wiki_get", Arguments: round1[1].Function.Arguments},
		{ID: "c3", Name: "wiki_get", Arguments: round1[2].Function.Arguments},
	}})

	after1 := req(
		llm.Message{Role: "assistant", ToolCalls: round1},
		llm.Message{Role: "tool", ToolCallID: "c1", Content: "# HTTP Version"},
		llm.Message{Role: "tool", ToolCallID: "c2", Content: "# TileLang"},
		llm.Message{Role: "tool", ToolCallID: "c3", Content: scRefusalText},
	)
	rec.BeginRequest(2, 1, 6, 0)
	rec.Request(after1)
	rec.Tool(trace.Tool{Round: 2, ID: "c4", Name: "stage.create_page"})
	rec.Tool(trace.Tool{Round: 2, ID: "c5", Name: "wiki.search"})
	rec.Response(trace.Response{Round: 2, Attempt: 1, Finish: "tool_calls", ToolCalls: []trace.ToolCall{
		{ID: "c4", Name: "stage_create_page", Arguments: `{}`},
		{ID: "c5", Name: "wiki_search", Arguments: `{"query":"tls"}`},
	}})

	rec.BeginRequest(3, 1, 8, 0)
	rec.Request(after1)
	rec.Tool(trace.Tool{Round: 3, ID: "c6", Name: "stage.close"})
	rec.Response(trace.Response{Round: 3, Attempt: 1, Finish: "stop", Text: "done"})
	rec.Done(trace.Done{Reason: "stop", Rounds: 3, WallMS: 30})
	return id
}

// scLintJSON is a lint.json with the given link-orphan paths and one finding
// of another check on the first of them.
func scLintJSON(orphans ...string) string {
	lf := lintFile{Findings: []lintFinding{}}
	for _, p := range orphans {
		lf.Findings = append(lf.Findings, lintFinding{Check: "link-orphan", Path: p, Line: 1, Severity: "warn", Message: "nothing links here"})
		lf.Warns++
	}
	if len(orphans) > 0 {
		lf.Findings = append(lf.Findings, lintFinding{Check: "link-broken", Path: orphans[0], Line: 3, Severity: "error", Message: "dangling"})
		lf.Errors++
	}
	b, _ := json.Marshal(lf)
	return string(b)
}

// TestScoreIngestNewMetrics pins 049's eight ingest metrics on artifacts
// written by hand. The snapshot holds http-version.md (body "x\ny") and
// tilelang.md. The ingest staged http-versions.md (new, and a near-duplicate
// of http-version), tls.md (new) and tilelang.md with every old line kept
// (plus a new one, and its trailing spaces gone). lint.json has a link-orphan
// on tls.md and one on tilelang.md — only the first is a NEW page's. The
// trace reads twice, is refused a third read, creates a page, searches once
// and closes (049).
func TestScoreIngestNewMetrics(t *testing.T) {
	set := newPagesSet(t)
	run := scNewRun(t, set, "r1")
	caseDir := filepath.Join(run, "paper-text", "1")
	turn := scWriteReadingTrace(t, caseDir)
	scWriteCase(t, run, scAnsweredMeta("paper-text", "ingest", "ingest", 1, turn), map[string]string{
		"stdout.txt":                            "ingested\n",
		"staged/wiki/concepts/http-versions.md": scPage("HTTP Versions", "# HTTP Versions\n\nHTTP/1.1, HTTP/2 and HTTP/3.\n"),
		"staged/wiki/concepts/tls.md":           scPage("TLS", "# TLS\n\nTransport security.\n"),
		"staged/wiki/entities/tilelang.md": scPage("TileLang", "# TileLang\n\nTileLang compiles kernels.\n\n## Notes\n\n"+
			"First note.\nFirst note.\nA brand new line.\n"),
		"lint.json": scLintJSON("wiki/concepts/tls.md", "wiki/entities/tilelang.md"),
	})

	res, err := Score(run)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	scMetrics(t, "paper-text/1 (049 metrics)", scOnly(scResult(t, res, "paper-text", 1).Metrics), map[string]float64{
		MetricPagesNew:              2,
		MetricDupPages:              1,
		MetricPatchedLossless:       1,
		MetricOrphansNew:            1,
		MetricReadsBeforeFirstStage: 3,
		MetricReadRefusals:          1,
		MetricClosed:                1,
		MetricSearchCalls:           1,
	})
}

// TestScoreIngestNewMetricsLossyAndAbsent pins the corners of the same
// metrics: a patch that drops a line, a pre-existing page patched or not, and
// every "absent, not zero" condition — no turns, no lint.json, nothing staged
// that already existed. pages_new and dup_pages are values even at 0 (049).
func TestScoreIngestNewMetricsLossyAndAbsent(t *testing.T) {
	set := newPagesSet(t)
	run := scNewRun(t, set, "r1")

	// Run 1: tilelang patched lossily (the second "First note." is gone) and
	// http-version re-staged with a changed line: 0 of 2 pages kept; nothing
	// is new; no lint.json; no trace.
	scWriteCase(t, run, scAnsweredMeta("paper-text", "ingest", "ingest", 1), map[string]string{
		"stdout.txt":                           "ingested\n",
		"staged/wiki/entities/tilelang.md":     scPage("TileLang", "# TileLang\n\nTileLang compiles kernels.\n\n## Notes\n\nFirst note.\n"),
		"staged/wiki/concepts/http-version.md": scPage("HTTP Version", "x\nz\n"),
	})
	// Run 2: one of two existing pages lossless: 0.5; a trace and a lint.json
	// with no link-orphan on a new page: orphans_new 0 is a value.
	caseDir := filepath.Join(run, "paper-text", "2")
	turn := scWriteReadingTrace(t, caseDir)
	scWriteCase(t, run, scAnsweredMeta("paper-text", "ingest", "ingest", 2, turn), map[string]string{
		"stdout.txt":                           "ingested\n",
		"staged/wiki/entities/tilelang.md":     scPage("TileLang", "# TileLang\n\nTileLang compiles kernels.\n\n## Notes\n\nFirst note.\nFirst note.\n"),
		"staged/wiki/concepts/http-version.md": scPage("HTTP Version", "y\n"),
		"lint.json":                            scLintJSON("wiki/entities/tilelang.md"),
	})
	// Run 3: nothing staged at all.
	scWriteCase(t, run, scAnsweredMeta("paper-text", "ingest", "ingest", 3), map[string]string{"stdout.txt": "nothing\n"})

	res, err := Score(run)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	scMetrics(t, "run 1", scOnly(scResult(t, res, "paper-text", 1).Metrics), map[string]float64{
		MetricPagesNew: 0, MetricDupPages: 0, MetricPatchedLossless: 0,
	})
	scMetrics(t, "run 2", scOnly(scResult(t, res, "paper-text", 2).Metrics), map[string]float64{
		MetricPagesNew: 0, MetricDupPages: 0, MetricPatchedLossless: 0.5, MetricOrphansNew: 0,
		MetricReadsBeforeFirstStage: 3, MetricReadRefusals: 1, MetricClosed: 1, MetricSearchCalls: 1,
	})
	scMetrics(t, "run 3", scOnly(scResult(t, res, "paper-text", 3).Metrics), map[string]float64{
		MetricPagesNew: 0, MetricDupPages: 0,
	})
}

// scResultCall is one tool call of scWriteResultTrace: the canonical name the
// loop records, whether the call failed, and the text the model got back.
type scResultCall struct {
	name   string
	fail   bool
	result string
}

// scWriteResultTrace writes one turn whose round 1 makes calls and whose
// round 2's request carries every result as a role:"tool" message, so
// ToolErrors can recover the text of each failure. It returns the turn id.
func scWriteResultTrace(t *testing.T, caseDir string, calls ...scResultCall) string {
	t.Helper()
	dir := filepath.Join(caseDir, "traces")
	id := "20260101T000002Z-0002"
	_, rec := trace.Start(context.Background(), dir, id, trace.Meta{
		Verb: "ingest", Session: "ingest", Version: "v9.9.9-test", Model: "glm-test", MaxRounds: 8,
	}, 0)
	if rec == nil {
		t.Fatal("trace.Start returned no recorder")
	}
	var asked []llm.ToolCall
	var wire []trace.ToolCall
	var results []llm.Message
	for i, c := range calls {
		cid := fmt.Sprintf("c%d", i+1)
		w := scWireName(c.name)
		asked = append(asked, scLLMCall(cid, w, `{}`))
		wire = append(wire, trace.ToolCall{ID: cid, Name: w, Arguments: `{}`})
		results = append(results, llm.Message{Role: "tool", ToolCallID: cid, Content: c.result})
	}
	body := func(extra ...llm.Message) []byte {
		msgs := append([]llm.Message{{Role: "system", Content: "You are the curator."}, {Role: "user", Content: "ingest"}}, extra...)
		b, err := json.Marshal(map[string]any{"model": "glm-test", "stream": true, "messages": msgs})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	rec.BeginRequest(1, 1, 2, 0)
	rec.Request(body())
	for i, c := range calls {
		rec.Tool(trace.Tool{Round: 1, ID: fmt.Sprintf("c%d", i+1), Name: c.name, IsError: c.fail})
	}
	rec.Response(trace.Response{Round: 1, Attempt: 1, Finish: "tool_calls", ToolCalls: wire})
	rec.BeginRequest(2, 1, 2+1+len(results), 0)
	rec.Request(body(append([]llm.Message{{Role: "assistant", ToolCalls: asked}}, results...)...))
	rec.Response(trace.Response{Round: 2, Attempt: 1, Finish: "stop", Text: "done"})
	rec.Done(trace.Done{Reason: "stop", Rounds: 2, WallMS: 10})
	return id
}

// scWireName is the wire spelling of a canonical tool name: the dot becomes
// an underscore (which is all the names used here need).
func scWireName(canonical string) string {
	return strings.ReplaceAll(canonical, ".", "_")
}

// TestScoreIngestReadRefusalsAreRefusals pins what read_refusals counts: a
// FAILED call to a READ tool whose result is 048's refusal text — for any of
// the three read tools — and nothing else: not a read that failed for another
// reason, not a refusal text on another tool, and not a successful read whose
// page merely quotes the text (049).
func TestScoreIngestReadRefusalsAreRefusals(t *testing.T) {
	set := newPagesSet(t)
	run := scNewRun(t, set, "r1")
	caseDir := filepath.Join(run, "paper-text", "1")
	turn := scWriteResultTrace(t, caseDir,
		scResultCall{"wiki.get", true, "wiki.get: page not found"},
		scResultCall{"wiki.get", true, scRefusalText},
		scResultCall{"wiki.neighbors", true, strings.Replace(scRefusalText, "wiki.get", "wiki.neighbors", 1)},
		scResultCall{"wiki.backlinks", true, strings.Replace(scRefusalText, "wiki.get", "wiki.backlinks", 1)},
		scResultCall{"stage.create_page", true, scRefusalText},
		scResultCall{"wiki.get", false, "A page that quotes: " + scRefusalText},
	)
	scWriteCase(t, run, scAnsweredMeta("paper-text", "ingest", "ingest", 1, turn), map[string]string{"stdout.txt": "ingested\n"})

	res, err := Score(run)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	// Five reads, the failed create_page is no page change; no search, no close.
	scMetrics(t, "paper-text/1", scOnly(scResult(t, res, "paper-text", 1).Metrics), map[string]float64{
		MetricReadsBeforeFirstStage: 5, MetricReadRefusals: 3, MetricClosed: 0, MetricSearchCalls: 0,
		MetricPagesNew: 0, MetricDupPages: 0,
	})
}

// TestScoreIngestDupPagesCountedOnce pins the two corners of dup_pages that
// the frozen row does not reach: a new page that is a near-duplicate of
// SEVERAL snapshot pages is one duplicate, not several; and a new page whose
// slug repeats an existing page's in ANOTHER directory (entities/ instead of
// concepts/) is a duplicate too — the wiki would hold two pages for one topic
// (049).
func TestScoreIngestDupPagesCountedOnce(t *testing.T) {
	// http-versions ~ http-version (2 of 2) and ~ http-version-history
	// (2 of 3): two snapshot matches for one new page.
	set := newPagesSet(t, "wiki/concepts/http-version-history.md")
	run := scNewRun(t, set, "r1")
	scWriteCase(t, run, scAnsweredMeta("paper-text", "ingest", "ingest", 1), map[string]string{
		"stdout.txt":                            "ingested\n",
		"staged/wiki/concepts/http-versions.md": scPage("HTTP Versions", "# HTTP Versions\n"),
		"staged/wiki/entities/http-version.md":  scPage("HTTP Version", "# HTTP Version\n"),
		"staged/wiki/concepts/tls.md":           scPage("TLS", "# TLS\n"),
	})
	res, err := Score(run)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	scMetrics(t, "paper-text/1", scOnly(scResult(t, res, "paper-text", 1).Metrics), map[string]float64{
		MetricPagesNew: 3, MetricDupPages: 2,
	})
}

// TestScoreIngestPatchedLosslessAppendedLine pins A-049-4 and A-049-6 through
// the scorer: a patch that only INSERTS into an existing line (a sentence
// that gained a link at its end or in its middle, an item that gained a note)
// lost nothing and is lossless, while one that shortens the line, rewrites it,
// or edits it in two places is not. Run 1 appends on both staged pages, run 2
// shortens one of two, run 3 rewrites one of two, run 4 inserts mid-line on
// one page, run 5 inserts at two points of one line (049).
func TestScoreIngestPatchedLosslessAppendedLine(t *testing.T) {
	set := newPagesSet(t)
	run := scNewRun(t, set, "r1")
	tilelang := func(kernels string) string {
		return scPage("TileLang", "# TileLang\n\n"+kernels+"\n\n## Notes\n\nFirst note.\nFirst note.\n")
	}
	scWriteCase(t, run, scAnsweredMeta("paper-text", "ingest", "ingest", 1), map[string]string{
		"staged/wiki/entities/tilelang.md":     tilelang("TileLang compiles kernels. See [[http-version]]."),
		"staged/wiki/concepts/http-version.md": scPage("HTTP Version", "x\ny, and [[tilelang]]\n"),
	})
	scWriteCase(t, run, scAnsweredMeta("paper-text", "ingest", "ingest", 2), map[string]string{
		"staged/wiki/entities/tilelang.md":     tilelang("TileLang compiles."),
		"staged/wiki/concepts/http-version.md": scPage("HTTP Version", "x\ny, and more\n"),
	})
	scWriteCase(t, run, scAnsweredMeta("paper-text", "ingest", "ingest", 3), map[string]string{
		"staged/wiki/entities/tilelang.md":     tilelang("Kernels are compiled by TileLang."),
		"staged/wiki/concepts/http-version.md": scPage("HTTP Version", "x\ny\n"),
	})
	scWriteCase(t, run, scAnsweredMeta("paper-text", "ingest", "ingest", 4), map[string]string{
		"staged/wiki/entities/tilelang.md":     tilelang("TileLang compiles [[http-version]] kernels."),
		"staged/wiki/concepts/http-version.md": scPage("HTTP Version", "x\ny\n"),
	})
	scWriteCase(t, run, scAnsweredMeta("paper-text", "ingest", "ingest", 5), map[string]string{
		"staged/wiki/entities/tilelang.md":     tilelang("TileLang really compiles fast kernels."),
		"staged/wiki/concepts/http-version.md": scPage("HTTP Version", "x\ny\n"),
	})

	res, err := Score(run)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	for _, want := range []struct {
		index    int
		lossless float64
	}{{1, 1}, {2, 0.5}, {3, 0.5}, {4, 1}, {5, 0.5}} {
		scMetrics(t, fmt.Sprintf("paper-text/%d", want.index), scOnly(scResult(t, res, "paper-text", want.index).Metrics), map[string]float64{
			MetricPagesNew: 0, MetricDupPages: 0, MetricPatchedLossless: want.lossless,
		})
	}
}

// TestIngestMetricNames pins the new metric names and their place at the
// end of the metric order, after lint_warns, in the order the table prints
// them; and that the two that are shares of one are bounded for the
// zero-variance floor (049).
func TestIngestMetricNames(t *testing.T) {
	want := []string{
		"reads_before_first_stage", "read_refusals", "closed", "search_calls",
		"pages_new", "patched_lossless", "dup_pages", "orphans_new",
	}
	if fmt.Sprint(newIngestMetrics) != fmt.Sprint(want) {
		t.Fatalf("metric names %v, want %v", newIngestMetrics, want)
	}
	n := len(metricOrder)
	if n < len(want)+1 || fmt.Sprint(metricOrder[n-len(want):]) != fmt.Sprint(want) || metricOrder[n-len(want)-1] != MetricLintWarns {
		t.Errorf("metricOrder ends %v, want lint_warns then %v", metricOrder[max(0, n-len(want)-1):], want)
	}
	for _, m := range []string{MetricClosed, MetricPatchedLossless} {
		if !boundedMetrics[m] {
			t.Errorf("%s is a 0/1 outcome or a share and must be a bounded metric", m)
		}
	}
	for _, m := range []string{MetricReadsBeforeFirstStage, MetricReadRefusals, MetricSearchCalls, MetricPagesNew, MetricDupPages, MetricOrphansNew} {
		if boundedMetrics[m] {
			t.Errorf("%s is a count and must not be bounded", m)
		}
	}
}

// scWriteRawGetTrace writes one ingest turn with the real Recorder whose first
// round asks raw.get for each of calls (JSON argument objects) and whose second
// round answers, and returns the turn id.
func scWriteRawGetTrace(t *testing.T, caseDir string, calls ...string) string {
	t.Helper()
	dir := filepath.Join(caseDir, "traces")
	id := "20260101T000002Z-0001"
	_, rec := trace.Start(context.Background(), dir, id, trace.Meta{
		Verb: "ingest", Session: "ingest", Version: "v9.9.9-test", Model: "glm-test", MaxRounds: 6,
	}, 0)
	if rec == nil {
		t.Fatal("trace.Start returned no recorder")
	}
	body, err := json.Marshal(map[string]any{
		"model": "glm-test", "stream": true,
		"messages": []llm.Message{{Role: "system", Content: "You are the curator."}, {Role: "user", Content: "recompile"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var tcs []trace.ToolCall
	rec.BeginRequest(1, 1, 2, 0)
	rec.Request(body)
	for i, args := range calls {
		cid := fmt.Sprintf("c%d", i+1)
		rec.Tool(trace.Tool{Round: 1, ID: cid, Name: "raw.get"})
		tcs = append(tcs, trace.ToolCall{ID: cid, Name: "raw_get", Arguments: args})
	}
	rec.Response(trace.Response{Round: 1, Attempt: 1, Finish: "tool_calls", ToolCalls: tcs})
	rec.BeginRequest(2, 1, 4, 0)
	rec.Request(body)
	rec.Response(trace.Response{Round: 2, Attempt: 1, Finish: "stop", Text: "done"})
	rec.Done(trace.Done{Reason: "stop", Rounds: 2, WallMS: 20})
	return id
}

// TestScoreRecompileChunkCoverage pins 055 D6's scoring half. A recompile case
// stages no ingest_source, so its chunk_coverage used to be absent whatever the
// model read; now its raw paths count — the body is the committed raw the
// runner kept in the case directory, or the snapshot's when none was kept.
// Reading chunks 1 and 3 of a three-chunk raw scores 2/3, an ingest_source op
// in the same run adds to the same fraction, and a plain case is unchanged.
func TestScoreRecompileChunkCoverage(t *testing.T) {
	const rcPath = "raw/articles/rc-source.md"
	body := strings.Repeat("a", 40000) // 40 000 runes: three chunks of 16 000
	if n := tools.RawChunkCount(body); n != 3 {
		t.Fatalf("fixture body has %d chunks, want 3", n)
	}
	ingested, _ := vault.ParseDate("2026-10-05")
	rawFile := func(b string) string {
		return string((&vault.RawSource{SourceURL: "https://example.test/rc", Ingested: ingested, SHA256: vault.BodySHA256(b), Body: b}).Serialize())
	}
	coverage := func(t *testing.T, res *Results, id string) (float64, bool) {
		t.Helper()
		v, ok := scResult(t, res, id, 1).Metrics[MetricChunkCoverage]
		return v, ok
	}

	t.Run("two of three chunks", func(t *testing.T) {
		set := newRunSet(t)
		scRewriteCases(t, set, "[[ingest]]\nid = \"rc\"\nrecompile = true\ninput = \""+rcPath+"\"\nfacts = [[\"x\"]]\n")
		run := scNewRun(t, set, "r1")
		caseDir := filepath.Join(run, "rc", "1")
		turn := scWriteRawGetTrace(t, caseDir,
			`{"source":"`+rcPath+`","chunk":1}`, `{"source":"`+rcPath+`","chunk":3}`, `{"source":"raw/articles/other.md"}`)
		scWriteCase(t, run, scAnsweredMeta("rc", "ingest", "ingest", 1, turn), map[string]string{
			"stdout.txt":       "recompiled\n",
			"staged/" + rcPath: rawFile(body),
		})

		res, err := Score(run)
		if err != nil {
			t.Fatalf("Score: %v", err)
		}
		got, ok := coverage(t, res, "rc")
		if !ok || math.Abs(got-2.0/3) > 1e-9 {
			t.Errorf("chunk_coverage = %v (present %v), want 2/3", got, ok)
		}
	})

	t.Run("nothing read is zero, not absent", func(t *testing.T) {
		set := newRunSet(t)
		scRewriteCases(t, set, "[[ingest]]\nid = \"rc\"\nrecompile = true\ninput = \""+rcPath+"\"\nfacts = [[\"x\"]]\n")
		run := scNewRun(t, set, "r1")
		caseDir := filepath.Join(run, "rc", "1")
		turn := scWriteRawGetTrace(t, caseDir, `{"source":"raw/articles/other.md"}`)
		scWriteCase(t, run, scAnsweredMeta("rc", "ingest", "ingest", 1, turn), map[string]string{
			"stdout.txt":       "recompiled\n",
			"staged/" + rcPath: rawFile(body),
		})
		res, err := Score(run)
		if err != nil {
			t.Fatalf("Score: %v", err)
		}
		if got, ok := coverage(t, res, "rc"); !ok || got != 0 {
			t.Errorf("chunk_coverage = %v (present %v), want 0", got, ok)
		}
	})

	t.Run("a raw only the snapshot holds", func(t *testing.T) {
		set := newRunSet(t)
		const snapRaw = "raw/articles/kv-cache-explained.md" // one chunk in the snapshot vault
		scRewriteCases(t, set, "[[ingest]]\nid = \"rc\"\nrecompile = true\ninput = \""+snapRaw+"\"\nfacts = [[\"x\"]]\n")
		run := scNewRun(t, set, "r1")
		caseDir := filepath.Join(run, "rc", "1")
		turn := scWriteRawGetTrace(t, caseDir, `{"source":"`+snapRaw+`"}`)
		scWriteCase(t, run, scAnsweredMeta("rc", "ingest", "ingest", 1, turn), map[string]string{"stdout.txt": "recompiled\n"})
		res, err := Score(run)
		if err != nil {
			t.Fatalf("Score: %v", err)
		}
		if got, ok := coverage(t, res, "rc"); !ok || got != 1 {
			t.Errorf("chunk_coverage = %v (present %v), want 1", got, ok)
		}
	})

	t.Run("an ingest_source op adds to the same fraction", func(t *testing.T) {
		set := newRunSet(t)
		scRewriteCases(t, set, "[[ingest]]\nid = \"rc\"\nrecompile = true\ninput = \""+rcPath+"\"\nfacts = [[\"x\"]]\n")
		run := scNewRun(t, set, "r1")
		caseDir := filepath.Join(run, "rc", "1")
		two := scIngestBody(t) // two chunks
		const newPath = "raw/articles/new-source.md"
		turn := scWriteRawGetTrace(t, caseDir,
			`{"source":"`+rcPath+`","chunk":1}`, `{"source":"`+rcPath+`","chunk":2}`, `{"source":"`+newPath+`","chunk":1}`)
		scWriteCase(t, run, scAnsweredMeta("rc", "ingest", "ingest", 1, turn), map[string]string{
			"stdout.txt":        "recompiled\n",
			"staged/" + rcPath:  rawFile(body),
			"staged/" + newPath: rawFile(two),
		})
		if err := writeJSON(filepath.Join(caseDir, "changeset.json"), changesetFile{ID: "cs1", Ops: []changesetOp{
			{ID: "op1", Op: "ingest_source", Path: newPath, State: "proposed"},
		}}); err != nil {
			t.Fatal(err)
		}
		res, err := Score(run)
		if err != nil {
			t.Fatalf("Score: %v", err)
		}
		// rc-source: chunks 1, 2 of 3; new-source: chunk 1 of 2 -> 3 of 5.
		if got, ok := coverage(t, res, "rc"); !ok || math.Abs(got-3.0/5) > 1e-9 {
			t.Errorf("chunk_coverage = %v (present %v), want 3/5", got, ok)
		}
	})

	t.Run("a raw both recompiled and staged counts once", func(t *testing.T) {
		set := newRunSet(t)
		scRewriteCases(t, set, "[[ingest]]\nid = \"rc\"\nrecompile = true\ninput = \""+rcPath+"\"\nfacts = [[\"x\"]]\n")
		run := scNewRun(t, set, "r1")
		caseDir := filepath.Join(run, "rc", "1")
		const newPath = "raw/articles/new-source.md"
		turn := scWriteRawGetTrace(t, caseDir,
			`{"source":"`+rcPath+`","chunk":1}`, `{"source":"`+rcPath+`","chunk":2}`, `{"source":"`+newPath+`","chunk":1}`)
		scWriteCase(t, run, scAnsweredMeta("rc", "ingest", "ingest", 1, turn), map[string]string{
			"stdout.txt":        "recompiled\n",
			"staged/" + rcPath:  rawFile(body),
			"staged/" + newPath: rawFile(scIngestBody(t)),
		})
		if err := writeJSON(filepath.Join(caseDir, "changeset.json"), changesetFile{ID: "cs1", Ops: []changesetOp{
			{ID: "op1", Op: "ingest_source", Path: rcPath, State: "proposed"},
			{ID: "op2", Op: "ingest_source", Path: newPath, State: "proposed"},
		}}); err != nil {
			t.Fatal(err)
		}
		res, err := Score(run)
		if err != nil {
			t.Fatalf("Score: %v", err)
		}
		// rc-source is both an op and a recompile input: 2 of 3 once, not twice.
		if got, ok := coverage(t, res, "rc"); !ok || math.Abs(got-3.0/5) > 1e-9 {
			t.Errorf("chunk_coverage = %v (present %v), want 3/5", got, ok)
		}
	})

	t.Run("a plain case does not count its input as a raw", func(t *testing.T) {
		set := newRunSet(t)
		run := scNewRun(t, set, "r1")
		caseDir := filepath.Join(run, "paper-text", "1")
		turn := scWriteRawGetTrace(t, caseDir, `{"source":"inputs/paper.txt"}`)
		scWriteCase(t, run, scAnsweredMeta("paper-text", "ingest", "ingest", 1, turn), map[string]string{"stdout.txt": "ingested\n"})
		res, err := Score(run)
		if err != nil {
			t.Fatalf("Score: %v", err)
		}
		if got, ok := coverage(t, res, "paper-text"); ok {
			t.Errorf("chunk_coverage = %v for a plain case that staged no source, want absent", got)
		}
	})
}
