package eval

import (
	"path/filepath"
	"strings"
	"testing"
)

// scSearchRefusalText is 053's refusal of a wiki.search over the ingest search
// budget, as internal/agent/searchbudget.go formats it with the budget, 10. The
// scorer recognises it by a needle in internal/eval/score, whose own test
// reads the agent's source to keep that needle honest; this copy only has to be
// what a refused call's tool message holds.
const scSearchRefusalText = "wiki.search refused: this ingest has searched the wiki 10 times since it last staged a change. The wiki has nothing closer than what you have found; stage the pages for the source now (stage.create_page / stage.patch_page) — a new page is right when nothing related exists. Searches are allowed again after a change is staged."

// scSearchOK is what a served wiki.search returns, as far as the scorer cares.
const scSearchOK = `3 results for "agent memory": wiki/concepts/kv-cache.md`

// TestScoreIngestSearchRefusals pins search_refusals (053): the wiki.search
// calls of the WHOLE case that the engine's search budget refused, a value (0
// included) for every run that recorded a turn and absent for one that did not.
// search_calls keeps counting every call, the refused too — that is what lets a
// baseline scored before 053 stay comparable — so the two differ by exactly
// the refusals.
func TestScoreIngestSearchRefusals(t *testing.T) {
	set := newPagesSet(t)
	run := scNewRun(t, set, "r1")

	// Run 1: one search served, then one refused — the text is recovered from
	// the final answer round's request.
	caseDir := filepath.Join(run, "paper-text", "1")
	scWriteTurn(t, caseDir, "20260101T000001Z-0001", "stop",
		[]scTurnCall{{name: "wiki.search", result: scSearchOK}},
		[]scTurnCall{{name: "wiki.search", fail: true, result: scSearchRefusalText}},
	)
	scWriteCase(t, run, scAnsweredMeta("paper-text", "ingest", "ingest", 1, "20260101T000001Z-0001"), map[string]string{"stdout.txt": "ingested\n"})

	// Run 2: the refusal is the turn's last round (max_rounds there), so no
	// next request holds its text and only result_bytes says what it was.
	caseDir = filepath.Join(run, "paper-text", "2")
	scWriteTurn(t, caseDir, "20260101T000002Z-0002", "max_rounds",
		[]scTurnCall{{name: "wiki.search", result: scSearchOK}},
		[]scTurnCall{{name: "wiki.search", fail: true, bytes: len(scSearchRefusalText)}},
	)
	scWriteCase(t, run, scAnsweredMeta("paper-text", "ingest", "ingest", 2, "20260101T000002Z-0002"), map[string]string{"stdout.txt": "ingested\n"})

	// Run 3: two turns of one case, a refusal in each: the case has both.
	caseDir = filepath.Join(run, "paper-text", "3")
	first, second := "20260101T000003Z-0003", "20260101T000004Z-0004"
	scWriteTurn(t, caseDir, first, "stop",
		[]scTurnCall{{name: "wiki.search", fail: true, result: scSearchRefusalText}},
	)
	scWriteTurn(t, caseDir, second, "stop",
		[]scTurnCall{{name: "wiki.search", result: scSearchOK}, {name: "wiki.search", fail: true, result: scSearchRefusalText}},
	)
	scWriteCase(t, run, scAnsweredMeta("paper-text", "ingest", "ingest", 3, first, second), map[string]string{"stdout.txt": "ingested\n"})

	// Run 4: searches that all came back, a read refusal of 048's and a search
	// that failed for its own reason: none of them is 053's.
	caseDir = filepath.Join(run, "paper-text", "4")
	scWriteTurn(t, caseDir, "20260101T000005Z-0005", "stop",
		[]scTurnCall{{name: "wiki.search", result: scSearchOK}, {name: "wiki.search", fail: true, result: "wiki.search: q is required"}},
		[]scTurnCall{{name: "wiki.get", fail: true, result: scRefusalText}},
	)
	scWriteCase(t, run, scAnsweredMeta("paper-text", "ingest", "ingest", 4, "20260101T000005Z-0005"), map[string]string{"stdout.txt": "ingested\n"})

	// Run 5: no turn was recorded, so nobody counted anything.
	scWriteCase(t, run, scAnsweredMeta("paper-text", "ingest", "ingest", 5), map[string]string{"stdout.txt": "ingested\n"})

	res, err := Score(run)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	for _, want := range []struct {
		index           int
		calls, refusals float64
	}{{1, 2, 1}, {2, 2, 1}, {3, 3, 2}, {4, 2, 0}} {
		m := scResult(t, res, "paper-text", want.index).Metrics
		if got := m[MetricSearchRefusals]; got != want.refusals {
			t.Errorf("paper-text/%d: search_refusals = %v, want %v", want.index, got, want.refusals)
		}
		if got := m[MetricSearchCalls]; got != want.calls {
			t.Errorf("paper-text/%d: search_calls = %v, want %v (every call, the refused too)", want.index, got, want.calls)
		}
	}
	if _, ok := scResult(t, res, "paper-text", 5).Metrics[MetricSearchRefusals]; ok {
		t.Error("paper-text/5 has no recorded turn and must not have a search_refusals value")
	}
	if got := scResult(t, res, "paper-text", 4).Metrics[MetricReadRefusals]; got != 1 {
		t.Errorf("paper-text/4: read_refusals = %v, want 1: the new metric must not disturb 048's", got)
	}
}

// TestSearchRefusalsInTables pins that the metric reaches the reports: the
// scorecard prints it, after the 049 block, and `compare` of a baseline with no
// refusals against a candidate with some has its row, with a delta. Without a
// place in metricOrder it is printed by name after the listed metrics, which
// is where the rule above metricOrder puts a newer metric.
func TestSearchRefusalsInTables(t *testing.T) {
	set := newPagesSet(t)
	score := func(id, text string) *Results {
		run := scNewRun(t, set, id)
		turn := "20260101T000001Z-0001"
		scWriteTurn(t, run+"/paper-text/1", turn, "stop",
			[]scTurnCall{{name: "wiki.search", result: scSearchOK}},
			[]scTurnCall{{name: "wiki.search", fail: text != scSearchOK, result: text}},
		)
		scWriteCase(t, run, scAnsweredMeta("paper-text", "ingest", "ingest", 1, turn), map[string]string{"stdout.txt": "ingested\n"})
		res, err := Score(run)
		if err != nil {
			t.Fatalf("Score %s: %v", id, err)
		}
		return res
	}
	baseline, candidate := score("r1", scSearchOK), score("r2", scSearchRefusalText)

	var card strings.Builder
	if err := candidate.WriteScorecard(&card); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(card.String(), "search_refusals") {
		t.Errorf("the scorecard has no search_refusals row:\n%s", card.String())
	}
	stats := Aggregate(candidate.Results)
	if last := stats[len(stats)-1]; last.Metric != MetricSearchRefusals || last.Value != 1 {
		t.Errorf("last metric = %s %v, want search_refusals 1", last.Metric, last.Value)
	}

	cmp := Compare(baseline, candidate)
	row := rowFor(t, cmp, MetricSearchRefusals)
	if row.A == nil || row.B == nil || row.A.Value != 0 || row.B.Value != 1 || !row.HasDelta || row.Delta != 1 {
		t.Errorf("search_refusals row = %+v, want A 0 -> B 1 with a delta of 1", row)
	}
	calls := rowFor(t, cmp, MetricSearchCalls)
	if calls.A == nil || calls.B == nil || calls.A.Value != calls.B.Value {
		t.Errorf("search_calls = %+v, want the same 2 on both sides: the refused call still counts", calls)
	}
	var out strings.Builder
	if err := WriteComparison(&out, cmp); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "search_refusals") {
		t.Errorf("the comparison has no search_refusals row:\n%s", out.String())
	}
}
