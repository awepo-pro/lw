package eval

import (
	"context"
	"encoding/json"
	"fmt"
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

// gnfNotFound is wiki.get's answer for a page it cannot resolve, as
// internal/tools formats it. The scorer recognises it by its head and, for a
// call whose text was never recovered, by its length; internal/eval/score pins
// both against the real tool, so this copy only has to be what a not-found
// call's tool message holds.
func gnfNotFound(page string) string {
	return fmt.Sprintf("page %q was not found; pass the vault-relative path or the filename basename "+
		"(e.g. \"kv-cache\"), not the page title — wiki.search lists each result's path before the em dash", page)
}

// gnfPageBody is what a wiki.get that found its page returns, as far as the
// scorer cares.
const gnfPageBody = "---\ntitle: KV Cache\n---\n\n# KV Cache\n\nKeys and values."

// gnfCall is one tool call of a gnfWriteTurn round: the canonical name the loop
// records, the page it asked for (the model's JSON arguments are in the response,
// where the scorer finds them when a call's text was never recovered), whether
// it failed, what the model got back, and the result_bytes of the tool event
// (len(result) when 0).
type gnfCall struct {
	name   string
	page   string
	args   string // the JSON arguments when they are not {"page": page}
	fail   bool
	result string
	bytes  int
}

// gnfWriteTurn writes one ingest turn under caseDir/traces with the real
// Recorder, as scWriteTurn does, but keeps each call's arguments in the response
// — scWriteTurn sends `{}` — because get_not_found recognises a last-round call
// by the page it asked for. With done "stop" a final answer round follows the last
// tool round; with "max_rounds" the turn ends on it, so the last round has no next
// request and its results are never recovered.
func gnfWriteTurn(t *testing.T, caseDir, id, done string, rounds ...[]gnfCall) {
	t.Helper()
	_, rec := trace.Start(context.Background(), filepath.Join(caseDir, "traces"), id, trace.Meta{
		Verb: "ingest", Session: "ingest", Version: "v9.9.9-test", Model: "glm-test", MaxRounds: 8,
	}, 0)
	if rec == nil {
		t.Fatal("trace.Start returned no recorder")
	}
	msgs := []llm.Message{{Role: "system", Content: "You are the curator."}, {Role: "user", Content: "ingest"}}
	body := func() []byte {
		b, err := json.Marshal(map[string]any{"model": "glm-test", "stream": true, "messages": msgs})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for i, calls := range rounds {
		round := i + 1
		rec.BeginRequest(round, 1, len(msgs), 0)
		rec.Request(body())
		var asked []llm.ToolCall
		var wire []trace.ToolCall
		var results []llm.Message
		for j, c := range calls {
			cid := fmt.Sprintf("t%s-r%d-c%d", id[len(id)-4:], round, j+1)
			n := c.bytes
			if n == 0 {
				n = len(c.result)
			}
			args := []byte(c.args)
			if c.args == "" {
				var err error
				if args, err = json.Marshal(map[string]string{"page": c.page}); err != nil {
					t.Fatal(err)
				}
			}
			rec.Tool(trace.Tool{Round: round, ID: cid, Name: c.name, IsError: c.fail, ResultBytes: n})
			w := scWireName(c.name)
			asked = append(asked, scLLMCall(cid, w, string(args)))
			wire = append(wire, trace.ToolCall{ID: cid, Name: w, Arguments: string(args)})
			results = append(results, llm.Message{Role: "tool", ToolCallID: cid, Content: c.result})
		}
		rec.Response(trace.Response{Round: round, Attempt: 1, Finish: "tool_calls", ToolCalls: wire})
		msgs = append(append(msgs, llm.Message{Role: "assistant", ToolCalls: asked}), results...)
	}
	if done == "stop" {
		last := len(rounds) + 1
		rec.BeginRequest(last, 1, len(msgs), 0)
		rec.Request(body())
		rec.Response(trace.Response{Round: last, Attempt: 1, Finish: "stop", Text: "done"})
		rec.Done(trace.Done{Reason: "stop", Rounds: last, WallMS: 10})
		return
	}
	rec.Done(trace.Done{Reason: done, Rounds: len(rounds), WallMS: 10})
}

// TestScoreGetNotFound pins get_not_found (060): the wiki.get calls of the WHOLE
// case — every turn of it, so a joined second turn counts — whose result is the
// not-found refusal, a value (0 included) for every run that recorded a turn and
// absent for one that did not. It is the number the 10-08 failure would have
// shown: a joined recompile turn asked wiki.get for the pages its own earlier
// turn had staged six times, was told each did not exist, and ran out of rounds.
// The metric reaches the scorecard and `compare` without a place in metricOrder,
// printed by name like 053's search_refusals.
func TestScoreGetNotFound(t *testing.T) {
	set := newPagesSet(t)

	t.Run("counts", func(t *testing.T) {
		run := scNewRun(t, set, "r1")
		answered := func(index int, traces ...string) {
			scWriteCase(t, run, scAnsweredMeta("paper-text", "ingest", "ingest", index, traces...), map[string]string{"stdout.txt": "ingested\n"})
		}

		// Run 1: three wiki.get calls in one round, two answered "not found".
		gnfWriteTurn(t, filepath.Join(run, "paper-text", "1"), "20260101T000001Z-0001", "stop", []gnfCall{
			{name: "wiki.get", page: "wiki/concepts/paged-attention.md", fail: true, result: gnfNotFound("wiki/concepts/paged-attention.md")},
			{name: "wiki.get", page: "kv-cache", result: gnfPageBody},
			{name: "wiki.get", page: "paged-attention", fail: true, result: gnfNotFound("paged-attention")},
		})
		answered(1, "20260101T000001Z-0001")

		// Run 2: a turn with no not-found at all: a served read, a search and
		// a failure of another kind. The metric is 0, not absent.
		gnfWriteTurn(t, filepath.Join(run, "paper-text", "2"), "20260101T000002Z-0002", "stop",
			[]gnfCall{{name: "wiki.get", page: "kv-cache", result: gnfPageBody}, {name: "wiki.search", result: "no results"}},
			[]gnfCall{{name: "wiki.get", page: "kv-cache", fail: true, result: `section "## Nope" was not found on wiki/concepts/kv-cache.md; valid headings are: ## Abstract`}},
		)
		answered(2, "20260101T000002Z-0002")

		// Run 3: the turn ends on its last round (max_rounds there) with a
		// not-found nobody recovered the text of: the page it asked for and the
		// tool event's size say what it was.
		gnfWriteTurn(t, filepath.Join(run, "paper-text", "3"), "20260101T000003Z-0003", "max_rounds",
			[]gnfCall{{name: "wiki.get", page: "paged-attention", fail: true, result: gnfNotFound("paged-attention")}},
			[]gnfCall{
				{name: "wiki.get", page: "wiki/comparisons/http-versions-comparison.md", fail: true, bytes: len(gnfNotFound("wiki/comparisons/http-versions-comparison.md"))},
				{name: "wiki.get", page: "kv-cache", fail: true, bytes: 23},
			},
		)
		answered(3, "20260101T000003Z-0003")

		// Run 4: the joined shape — two turns of one case, a not-found in each.
		first, second := "20260101T000004Z-0004", "20260101T000005Z-0005"
		gnfWriteTurn(t, filepath.Join(run, "paper-text", "4"), first, "stop",
			[]gnfCall{{name: "wiki.get", page: "x", fail: true, result: gnfNotFound("x")}},
		)
		gnfWriteTurn(t, filepath.Join(run, "paper-text", "4"), second, "stop",
			[]gnfCall{{name: "wiki.get", page: "y", fail: true, result: gnfNotFound("y")}, {name: "wiki.get", page: "z", fail: true, result: gnfNotFound("z")}},
		)
		answered(4, first, second)

		// Run 5: the same text on the graph tools and on a staged tool's own
		// page-not-found is not a wiki.get.
		gnfWriteTurn(t, filepath.Join(run, "paper-text", "5"), "20260101T000006Z-0006", "stop", []gnfCall{
			{name: "wiki.neighbors", page: "x", fail: true, result: gnfNotFound("x")},
			{name: "wiki.backlinks", page: "x", fail: true, result: gnfNotFound("x")},
			{name: "stage.patch_page", page: "x", fail: true, result: `page "x" was not found; closest: wiki/concepts/y.md`},
			{name: "wiki.get", page: "x", result: gnfNotFound("x")}, // served: a page that quotes it
		})
		answered(5, "20260101T000006Z-0006")

		// Run 6: no turn was recorded, so nobody counted anything.
		answered(6)

		res, err := Score(run)
		if err != nil {
			t.Fatalf("Score: %v", err)
		}
		for _, want := range []struct {
			index int
			value float64
		}{{1, 2}, {2, 0}, {3, 2}, {4, 3}, {5, 0}} {
			m := scResult(t, res, "paper-text", want.index).Metrics
			if got, ok := m[MetricGetNotFound]; !ok || got != want.value {
				t.Errorf("paper-text/%d: get_not_found = %v (present %v), want %v", want.index, got, ok, want.value)
			}
		}
		if _, ok := scResult(t, res, "paper-text", 6).Metrics[MetricGetNotFound]; ok {
			t.Error("paper-text/6 has no recorded turn and must not have a get_not_found value")
		}
		if MetricGetNotFound != "get_not_found" {
			t.Errorf("MetricGetNotFound = %q, want get_not_found", MetricGetNotFound)
		}
		// The metric is its own: 048's read count and 053's search count are
		// what they were — run 1's three wiki.get are three reads, none refused.
		m := scResult(t, res, "paper-text", 1).Metrics
		if m[MetricReadsBeforeFirstStage] != 3 || m[MetricReadRefusals] != 0 || m[MetricSearchRefusals] != 0 {
			t.Errorf("paper-text/1: reads_before_first_stage %v, read_refusals %v, search_refusals %v; want 3, 0, 0",
				m[MetricReadsBeforeFirstStage], m[MetricReadRefusals], m[MetricSearchRefusals])
		}
	})

	t.Run("reports", func(t *testing.T) {
		// score builds a one-run set whose only wiki.get calls are n not-found
		// ones, and scores it.
		score := func(id string, notFound int) *Results {
			run := scNewRun(t, set, id)
			var calls []gnfCall
			calls = append(calls, gnfCall{name: "wiki.get", page: "kv-cache", result: gnfPageBody})
			for i := 0; i < notFound; i++ {
				page := fmt.Sprintf("missing-%d", i)
				calls = append(calls, gnfCall{name: "wiki.get", page: page, fail: true, result: gnfNotFound(page)})
			}
			turn := "20260101T000001Z-0001"
			gnfWriteTurn(t, filepath.Join(run, "paper-text", "1"), turn, "stop", calls)
			scWriteCase(t, run, scAnsweredMeta("paper-text", "ingest", "ingest", 1, turn), map[string]string{"stdout.txt": "ingested\n"})
			res, err := Score(run)
			if err != nil {
				t.Fatalf("Score %s: %v", id, err)
			}
			return res
		}
		control, candidate := score("control", 2), score("candidate", 0)

		var card strings.Builder
		if err := control.WriteScorecard(&card); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(card.String(), "get_not_found") {
			t.Errorf("the scorecard has no get_not_found row:\n%s", card.String())
		}
		var stat *Stat
		stats := Aggregate(control.Results)
		for i := range stats {
			if stats[i].Metric == MetricGetNotFound {
				stat = &stats[i]
			}
		}
		if stat == nil || stat.Value != 2 || stat.Cases != 1 || stat.Runs != 1 {
			t.Errorf("get_not_found stat = %+v, want value 2 over 1 case and 1 run", stat)
		}
		// A newer metric is printed after the listed ones, by name: get_not_found
		// sorts before search_refusals, which 053 placed the same way.
		var names []string
		for _, s := range stats {
			names = append(names, s.Metric)
		}
		if n := len(names); n < 2 || names[n-2] != MetricGetNotFound || names[n-1] != MetricSearchRefusals {
			t.Errorf("the last metrics are %v, want get_not_found then search_refusals", names[max(0, len(names)-2):])
		}

		cmp := Compare(control, candidate)
		row := rowFor(t, cmp, MetricGetNotFound)
		if row.A == nil || row.B == nil || row.A.Value != 2 || row.B.Value != 0 || !row.HasDelta || row.Delta != -2 {
			t.Errorf("get_not_found row = %+v, want A 2 -> B 0 with a delta of -2", row)
		}
		var out strings.Builder
		if err := WriteComparison(&out, cmp); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "get_not_found") {
			t.Errorf("the comparison has no get_not_found row:\n%s", out.String())
		}
	})
}

// TestScoreRecompileThenCoverage pins that a recompile case's chunk_coverage
// counts the raws of its `then` too (060): the second call compiles them like
// the first compiles its own, so the chunks of both are the total and a read of
// the joined call's raw is a read. A case without then counts what it always did.
func TestScoreRecompileThenCoverage(t *testing.T) {
	set := newPagesSet(t)
	scRewriteCases(t, set, `[[ingest]]
id = "joined"
recompile = true
inputs = ["raw/papers/leviathan-2023.md"]
then = ["raw/articles/kv-cache-explained.md"]

[[ingest]]
id = "plain"
recompile = true
inputs = ["raw/papers/leviathan-2023.md"]
`)
	set, err := LoadSet(set.Dir)
	if err != nil {
		t.Fatalf("LoadSet: %v", err)
	}

	// total is the chunk count of a committed raw, from the fixture the snapshot
	// was cut from.
	fixture := testutil.CopyFixture(t, "minimal")
	total := func(p string) int {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(fixture, filepath.FromSlash(p)))
		if err != nil {
			t.Fatal(err)
		}
		rs, err := vault.ParseRawSource(p, b)
		if err != nil {
			t.Fatal(err)
		}
		return tools.RawChunkCount(rs.Body)
	}
	first, second := total("raw/papers/leviathan-2023.md"), total("raw/articles/kv-cache-explained.md")

	run := scNewRun(t, set, "r1")
	turn := "20260101T000001Z-0001"
	for _, id := range []string{"joined", "plain"} {
		// The model read chunk 1 of the raw the joined call compiles, and
		// nothing of the first call's.
		gnfWriteTurn(t, filepath.Join(run, id, "1"), turn, "stop", []gnfCall{
			{name: "raw.get", args: `{"source":"raw/articles/kv-cache-explained.md","chunk":1}`, result: "chunk"},
		})
		scWriteCase(t, run, scAnsweredMeta(id, "ingest", "ingest", 1, turn), map[string]string{"stdout.txt": "ingested\n"})
	}
	res, err := Score(run)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if got, want := scResult(t, res, "joined", 1).Metrics[MetricChunkCoverage], 1/float64(first+second); got != want {
		t.Errorf("joined: chunk_coverage = %v, want %v (1 chunk read of the %d the case's two raws have)", got, want, first+second)
	}
	if got := scResult(t, res, "plain", 1).Metrics[MetricChunkCoverage]; got != 0 {
		t.Errorf("plain: chunk_coverage = %v, want 0: its one raw was not read", got)
	}
}
