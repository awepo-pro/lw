package score

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/trace"
)

// toolStep is one tool event of a fixture round: the name the agent loop puts
// in the trace (canonical, "stage.create_page" with its underscore) and
// whether the call failed. The fixtures below go through the real trace
// Recorder and trace.Load, so the readers under test see what a live ingest
// turn leaves on disk (049).
type toolStep struct {
	name string
	fail bool
}

func okStep(name string) toolStep   { return toolStep{name: name} }
func failStep(name string) toolStep { return toolStep{name: name, fail: true} }

// writeToolTurn records turn n under dir: one round per entry of rounds, each
// round's tool events in the order given, then a done event. The round's
// response carries the same calls under their WIRE names, as a provider
// echoes them.
func writeToolTurn(t *testing.T, dir string, n int, rounds ...[]toolStep) *trace.Turn {
	t.Helper()
	rec := startTurn(t, dir, n)
	for i, steps := range rounds {
		round := i + 1
		send(rec, round, 1, request(t, llm.Message{Role: "user", Content: "ingest"}))
		var calls []llm.ToolCall
		for j, s := range steps {
			id := fmt.Sprintf("r%d-c%d", round, j+1)
			rec.Tool(trace.Tool{Round: round, ID: id, Name: s.name, IsError: s.fail})
			calls = append(calls, call(id, tools.WireName(s.name), `{}`))
		}
		rec.Response(trace.Response{Round: round, Attempt: 1, Finish: "tool_calls", ToolCalls: toolCalls(calls...)})
	}
	rec.Done(trace.Done{Reason: "stop", Rounds: len(rounds)})
	return loadTurn(t, dir, n)
}

// turnsOf writes every turn (turn -> round -> steps) into one trace dir and
// loads them back in order.
func turnsOf(t *testing.T, turns [][][]toolStep) []*trace.Turn {
	t.Helper()
	dir := t.TempDir()
	var out []*trace.Turn
	for i, rounds := range turns {
		out = append(out, writeToolTurn(t, dir, i+1, rounds...))
	}
	return out
}

// TestReadsBeforeFirstStage pins 048's read count as the scorer sees it: the
// wiki read calls (get, neighbors, backlinks) made before the first page
// change, REFUSED OR NOT, across turns in trace order — and every read when
// there is no page change at all (049). A page change is a successful stage.*
// call other than ingest_source, open and close, which is 048's own
// definition (agent.isPageChange).
func TestReadsBeforeFirstStage(t *testing.T) {
	g, n, b := okStep("wiki.get"), okStep("wiki.neighbors"), okStep("wiki.backlinks")
	create := okStep("stage.create_page")

	tests := []struct {
		name  string
		turns [][][]toolStep
		want  int
	}{
		{"the change ends the count", [][][]toolStep{{{g, g, n, create, g}}}, 3},
		{"no page change counts every read", [][][]toolStep{{{
			g, failStep("stage.patch_page"), b, okStep("stage.ingest_source"), okStep("stage.close"), g,
		}}}, 3},
		{"no reads", [][][]toolStep{{{okStep("wiki.search"), okStep("raw.get")}}}, 0},
		{"two turns, the change in the second", [][][]toolStep{
			{{g, g}},
			{{g, create, g}},
		}, 3},
		{"a refused read still counts", [][][]toolStep{{{
			g, g, g, g, g, g, failStep("wiki.get"), create,
		}}}, 7},
		{"a failed read of another tool counts too", [][][]toolStep{{{failStep("wiki.neighbors"), failStep("wiki.backlinks"), create}}}, 2},
		{"open, ingest_source and close are not page changes", [][][]toolStep{{{
			g, okStep("stage.open"), g, okStep("stage.ingest_source"), g, okStep("stage.close"), g, okStep("stage.patch_page"), g,
		}}}, 4},
		{"a failed page change is not a change", [][][]toolStep{{{g, failStep("stage.create_page"), g, create, g}}}, 2},
		{"search, raw and the digest are not reads", [][][]toolStep{{{
			okStep("wiki.search"), okStep("raw.get"), okStep("raw.list"), okStep("vault.orient"), okStep("wiki.lint"), okStep("web.search"), g,
		}}}, 1},
		{"reads across rounds of one turn", [][][]toolStep{{{g, g}, {n}, {create}, {g}}}, 3},
		{"a wire-spelled name is read like a canonical one", [][][]toolStep{{{
			okStep("wiki_get"), okStep("wiki_neighbors"), okStep("stage_create_page"), okStep("wiki_get"),
		}}}, 2},
		{"a turn with no calls", [][][]toolStep{{{}}}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ReadsBeforeFirstStage(turnsOf(t, tc.turns)); got != tc.want {
				t.Errorf("ReadsBeforeFirstStage = %d, want %d", got, tc.want)
			}
		})
	}

	t.Run("every page-changing tool ends the count", func(t *testing.T) {
		for _, name := range []string{
			"stage.create_page", "stage.patch_page", "stage.rename_page", "stage.merge_pages",
			"stage.split_page", "stage.add_link", "stage.retract",
		} {
			turns := turnsOf(t, [][][]toolStep{{{g, okStep(name), g}}})
			if got := ReadsBeforeFirstStage(turns); got != 1 {
				t.Errorf("%s: ReadsBeforeFirstStage = %d, want 1 (it is a page change)", name, got)
			}
		}
	})

	t.Run("no turns and a nil turn", func(t *testing.T) {
		if got := ReadsBeforeFirstStage(nil); got != 0 {
			t.Errorf("no turns: %d, want 0", got)
		}
		if got := ReadsBeforeFirstStage([]*trace.Turn{nil}); got != 0 {
			t.Errorf("a nil turn: %d, want 0", got)
		}
	})
}

// TestClosed pins "the model reached stage.close": a successful call, in any
// turn, under either spelling of the name. A close that failed is not a close
// (049).
func TestClosed(t *testing.T) {
	tests := []struct {
		name  string
		turns [][][]toolStep
		want  bool
	}{
		{"a failed close only", [][][]toolStep{{{failStep("stage.close")}}}, false},
		{"a successful close", [][][]toolStep{{{okStep("stage.close")}}}, true},
		{"no close", [][][]toolStep{{{okStep("stage.create_page"), okStep("wiki.get")}}}, false},
		{"a failed close then a good one", [][][]toolStep{{{failStep("stage.close")}, {okStep("stage.close")}}}, true},
		{"the close is in the second turn", [][][]toolStep{{{okStep("wiki.get")}}, {{okStep("stage.close")}}}, true},
		{"the wire spelling", [][][]toolStep{{{okStep("stage_close")}}}, true},
		{"open is not close", [][][]toolStep{{{okStep("stage.open")}}}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Closed(turnsOf(t, tc.turns)); got != tc.want {
				t.Errorf("Closed = %v, want %v", got, tc.want)
			}
		})
	}
	if Closed(nil) || Closed([]*trace.Turn{nil}) {
		t.Error("no turns, and a nil turn, must not be closed")
	}
}

// TestCallCount pins the tool-call tally the scorer reads from the tool
// events: by canonical name, whatever spelling the event carries, and
// failed calls included — a wiki.search that errored was still a search the
// model chose to make (049). The canonical rows with an underscore
// (stage.create_page) are the trap tools.CanonicalName sets: it maps WIRE
// names, and on an already-canonical name it would turn the underscore into a
// dot.
func TestCallCount(t *testing.T) {
	t.Run("wire-spelled wiki_search events count as wiki.search", func(t *testing.T) {
		turns := turnsOf(t, [][][]toolStep{{
			{okStep("wiki_search"), okStep("wiki_get")},
			{okStep("wiki_search"), okStep("wiki_search")},
		}})
		if got := CallCount(turns, "wiki.search"); got != 3 {
			t.Errorf("CallCount(wiki.search) = %d, want 3", got)
		}
		if got := CallCount(turns, "wiki.get"); got != 1 {
			t.Errorf("CallCount(wiki.get) = %d, want 1", got)
		}
	})

	t.Run("canonical events, failures and several turns", func(t *testing.T) {
		turns := turnsOf(t, [][][]toolStep{
			{{okStep("wiki.search"), failStep("wiki.search")}},
			{{okStep("wiki.search")}},
		})
		if got := CallCount(turns, "wiki.search"); got != 3 {
			t.Errorf("CallCount(wiki.search) = %d, want 3", got)
		}
	})

	t.Run("a canonical name with an underscore in its leaf", func(t *testing.T) {
		turns := turnsOf(t, [][][]toolStep{{{
			okStep("stage.create_page"), okStep("stage_create_page"), okStep("stage.patch_page"),
		}}})
		if got := CallCount(turns, "stage.create_page"); got != 2 {
			t.Errorf("CallCount(stage.create_page) = %d, want 2", got)
		}
	})

	t.Run("nothing to count", func(t *testing.T) {
		if got := CallCount(nil, "wiki.search"); got != 0 {
			t.Errorf("no turns: %d, want 0", got)
		}
		if got := CallCount([]*trace.Turn{nil}, "wiki.search"); got != 0 {
			t.Errorf("a nil turn: %d, want 0", got)
		}
	})
}

// refusalText is the 048 refusal a wiki.get over the budget is answered with,
// formatted exactly as internal/agent/readbudget.go's readBudgetRefusalFmt
// would with ("wiki.get", 6). It is a copy — this package never imports the
// agent — and TestReadRefusalNeedleTracksAgentSource is what notices when the
// original moves on.
const refusalText = "wiki.get refused: this ingest has read 6 wiki pages since it last staged a change. Stage the pages for the source now (stage.create_page / stage.patch_page) from what you have read; wiki reads are allowed again after a change is staged."

// TestIsReadRefusal pins the needle: the 048 text for wiki.get is a refusal,
// the ordinary tool errors the same call can return are not (049).
func TestIsReadRefusal(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"the exact 048 text for wiki.get", refusalText, true},
		{"the same text for wiki.neighbors", strings.Replace(refusalText, "wiki.get", "wiki.neighbors", 1), true},
		{"the text a 200-rune ToolErrors cut leaves", string([]rune(refusalText)[:200]), true},
		{"a not-found error", "wiki.get: page not found", false},
		{"another tool's refusal", "tool wiki.search is not available in this turn; use one of: raw.get", false},
		{"a different budget", strings.Replace(refusalText, "read 6 wiki pages", "read 5 wiki pages", 1), false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsReadRefusal(tc.text); got != tc.want {
				t.Errorf("IsReadRefusal(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// TestReadRefusalNeedleTracksAgentSource is the drift guard for the copy
// above and for the needle itself: it reads internal/agent/readbudget.go as
// TEXT (no import), formats the real refusal with the real budget, and
// requires IsReadRefusal to recognise it. If 048's wording or its budget is
// ever changed, read_refusals would silently count zero; this fails instead.
func TestReadRefusalNeedleTracksAgentSource(t *testing.T) {
	src, err := os.ReadFile("../../agent/readbudget.go")
	if err != nil {
		t.Fatal(err)
	}
	fm := regexp.MustCompile(`const readBudgetRefusalFmt = (".*")\n`).FindSubmatch(src)
	bm := regexp.MustCompile(`const ingestReadBudget = (\d+)\n`).FindSubmatch(src)
	if fm == nil || bm == nil {
		t.Fatal("readBudgetRefusalFmt or ingestReadBudget not found in internal/agent/readbudget.go; update this guard with the refactor")
	}
	format, err := strconv.Unquote(string(fm[1]))
	if err != nil {
		t.Fatal(err)
	}
	budget, err := strconv.Atoi(string(bm[1]))
	if err != nil {
		t.Fatal(err)
	}
	real := fmt.Sprintf(format, "wiki.get", budget)
	if !IsReadRefusal(real) {
		t.Errorf("IsReadRefusal does not recognise the agent's refusal %q", real)
	}
	if real != refusalText {
		t.Errorf("this file's refusalText is stale\n got  %q\n want %q", refusalText, real)
	}
}
