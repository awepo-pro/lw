package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/trace"
)

// scTurnCall is one tool call of a scWriteTurn round: the canonical name the
// loop records, whether it failed, what the model got back (it lands in the
// NEXT round's request, so a call of the turn's last round never shows it),
// and the result_bytes the tool event carries — len(result) when 0.
type scTurnCall struct {
	name   string
	fail   bool
	result string
	bytes  int
}

// scWriteTurn writes one ingest turn under caseDir/traces with the real
// Recorder. Round i makes rounds[i-1]; every round after the first has a
// request whose tool messages are the previous round's results. With done
// "stop" a final answer round follows the last tool round; with "max_rounds"
// the turn ends on its last tool round, which therefore has no next request
// (049, A-049-5).
func scWriteTurn(t *testing.T, caseDir, id, done string, rounds ...[]scTurnCall) {
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
			rec.Tool(trace.Tool{Round: round, ID: cid, Name: c.name, IsError: c.fail, ResultBytes: n})
			w := scWireName(c.name)
			asked = append(asked, scLLMCall(cid, w, `{}`))
			wire = append(wire, trace.ToolCall{ID: cid, Name: w, Arguments: `{}`})
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

// TestScoreIngestReadRefusalLastRound pins A-049-5. An ingest that is refused
// a read on its LAST round (it hit max_rounds there) has no next request, so
// ToolErrors cannot recover the refusal's text; the tool event still carries
// result_bytes, and a failed read whose bytes are the length of 048's refusal
// for that tool is that refusal. Any other length is an ordinary failure
// (049).
func TestScoreIngestReadRefusalLastRound(t *testing.T) {
	set := newPagesSet(t)
	run := scNewRun(t, set, "r1")
	for _, tc := range []struct {
		index, bytes, refusals int
	}{
		{1, len(scRefusalText), 1}, // the refusal's length for wiki.get: a refusal
		{2, 17, 0},                 // some other failure's
	} {
		caseDir := filepath.Join(run, "paper-text", fmt.Sprint(tc.index))
		turn := "20260101T000001Z-0001"
		scWriteTurn(t, caseDir, turn, "max_rounds", []scTurnCall{{name: "wiki.get", fail: true, bytes: tc.bytes}})
		scWriteCase(t, run, scAnsweredMeta("paper-text", "ingest", "ingest", tc.index, turn), map[string]string{"stdout.txt": "ingested\n"})
	}

	res, err := Score(run)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	for _, want := range []struct {
		index   int
		refused float64
	}{{1, 1}, {2, 0}} {
		scMetrics(t, fmt.Sprintf("paper-text/%d", want.index), scOnly(scResult(t, res, "paper-text", want.index).Metrics), map[string]float64{
			MetricReadsBeforeFirstStage: 1, MetricReadRefusals: want.refused, MetricClosed: 0, MetricSearchCalls: 0,
			MetricPagesNew: 0, MetricDupPages: 0,
		})
	}
}

// TestScoreIngestMultiTurn pins that the trace metrics are those of the WHOLE
// case, not of one of its turns. A case dir can hold several turns (a resumed
// session, a retried ingest): reads before the first stage run on across the
// turn boundary — the first turn's two reads and the second's two before its
// create_page make four — and a refusal in the second turn counts though the
// first has none. A scorer that looked at the first turn only, or the last
// only, passes every single-turn test in this file and fails this one (049).
func TestScoreIngestMultiTurn(t *testing.T) {
	set := newPagesSet(t)
	run := scNewRun(t, set, "r1")
	caseDir := filepath.Join(run, "paper-text", "1")
	first, second := "20260101T000001Z-0001", "20260101T000002Z-0002"
	scWriteTurn(t, caseDir, first, "stop",
		[]scTurnCall{{name: "wiki.get", result: "# HTTP Version"}, {name: "wiki.get", result: "# TileLang"}},
	)
	scWriteTurn(t, caseDir, second, "stop",
		[]scTurnCall{{name: "wiki.get", result: "# KV cache"}, {name: "wiki.get", fail: true, result: scRefusalText}},
		[]scTurnCall{{name: "stage.create_page", result: "staged"}, {name: "stage.close", result: "closed"}},
	)
	scWriteCase(t, run, scAnsweredMeta("paper-text", "ingest", "ingest", 1, first, second), map[string]string{"stdout.txt": "ingested\n"})

	res, err := Score(run)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	scMetrics(t, "paper-text/1", scOnly(scResult(t, res, "paper-text", 1).Metrics), map[string]float64{
		MetricReadsBeforeFirstStage: 4, MetricReadRefusals: 1, MetricClosed: 1, MetricSearchCalls: 0,
		MetricPagesNew: 0, MetricDupPages: 0,
	})
}
