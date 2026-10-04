package score

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/trace"
)

// The fixtures below are written with the real trace.Start and Recorder, and
// every request body is json.Marshal of an llm-shaped request — the shape
// internal/trace/testdata/glm-real-request-01.json has — so the readers under
// test see exactly what a live turn leaves on disk. All content is synthetic
// (037 T2).

// turnID names the n-th fixture turn in a shape trace.Load accepts.
func turnID(n int) string {
	return fmt.Sprintf("20260101T0000%02dZ-%04x", n, n)
}

// startTurn opens a traced turn under dir and fails the test if tracing
// could not start (Start swallows its own errors into a nil Recorder).
func startTurn(t *testing.T, dir string, n int) *trace.Recorder {
	t.Helper()
	_, rec := trace.Start(context.Background(), dir, turnID(n), trace.Meta{
		Verb: "query", Session: "query", Version: "v0.0.0-test", Model: "fixture-model",
		MaxRounds: 8,
	}, 0)
	if rec == nil {
		t.Fatalf("trace.Start(%s) returned no Recorder", dir)
	}
	return rec
}

// loadTurn reads fixture turn n back.
func loadTurn(t *testing.T, dir string, n int) *trace.Turn {
	t.Helper()
	tu, err := trace.Load(dir, turnID(n))
	if err != nil {
		t.Fatal(err)
	}
	return tu
}

// request marshals an llm-shaped chat-completions request body.
func request(t *testing.T, msgs ...llm.Message) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"model": "fixture-model", "messages": msgs, "stream": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// send records one request attempt: BeginRequest names it, Request stores it.
func send(rec *trace.Recorder, round, attempt int, body []byte) {
	rec.BeginRequest(round, attempt, 4, 18)
	rec.Request(body)
}

// call builds an llm tool call in the wire shape.
func call(id, name, args string) llm.ToolCall {
	tc := llm.ToolCall{ID: id, Type: "function"}
	tc.Function.Name, tc.Function.Arguments = name, args
	return tc
}

// toolCalls converts llm calls to the trace shape, as the agent loop does.
func toolCalls(calls ...llm.ToolCall) []trace.ToolCall {
	out := make([]trace.ToolCall, len(calls))
	for i, c := range calls {
		out[i] = trace.ToolCall{ID: c.ID, Name: c.Function.Name, Arguments: c.Function.Arguments}
	}
	return out
}

// bodyOfChunks returns a raw body that raw.get would serve in exactly k
// chunks, found through tools.RawChunkCount so the fixture follows the
// chunk size rather than hard-coding it.
func bodyOfChunks(t *testing.T, k int) string {
	t.Helper()
	for n := 1; n < 1<<20; n += 500 {
		if b := strings.Repeat("a", n); tools.RawChunkCount(b) == k {
			return b
		}
	}
	t.Fatalf("no body of %d chunks found", k)
	return ""
}

// TestChunkCoverage drives the frozen case: raw_get on chunk 1 (no chunk
// argument), chunk 2, chunk 2 again, and a different source, against a body
// of 3 chunks — two distinct chunks of raw/a.md were read of 3. The calls
// sit in response tool_calls under their WIRE names (raw_get), which is
// where the model's arguments live; the tool events carry canonical names
// and no arguments (037 T2).
func TestChunkCoverage(t *testing.T) {
	body := bodyOfChunks(t, 3)

	t.Run("frozen case across two rounds", func(t *testing.T) {
		dir := t.TempDir()
		rec := startTurn(t, dir, 1)
		send(rec, 1, 1, request(t, llm.Message{Role: "user", Content: "q"}))
		rec.Response(trace.Response{Round: 1, Attempt: 1, Finish: "tool_calls", ToolCalls: toolCalls(
			call("c1", "raw_get", `{"source":"raw/a.md"}`),
			call("c2", "raw_get", `{"source":"raw/a.md","chunk":2}`),
		)})
		send(rec, 2, 1, request(t, llm.Message{Role: "user", Content: "q"}))
		rec.Response(trace.Response{Round: 2, Attempt: 1, Finish: "tool_calls", ToolCalls: toolCalls(
			call("c3", "raw_get", `{"source":"raw/a.md","chunk":2}`),
			call("c4", "raw_get", `{"source":"raw/b.md"}`),
		)})
		rec.Done(trace.Done{Reason: "stop", Rounds: 2})

		read, total := ChunkCoverage([]*trace.Turn{loadTurn(t, dir, 1)}, "raw/a.md", body)
		if read != 2 || total != 3 {
			t.Errorf("ChunkCoverage = read %d, total %d, want 2 of 3", read, total)
		}
	})

	t.Run("edge cases", func(t *testing.T) {
		dir := t.TempDir()
		rec := startTurn(t, dir, 1)
		send(rec, 1, 1, request(t, llm.Message{Role: "user", Content: "q"}))
		rec.Response(trace.Response{Round: 1, Attempt: 1, Finish: "tool_calls", ToolCalls: toolCalls(
			call("c1", "raw_get", `{"source":"raw/a.md","chunk":0}`),    // chunk 0 means chunk 1
			call("c2", "raw_get", `{"source":"  raw/a.md ","chunk":3}`), // source is trimmed
			call("c3", "raw_get", `{"source":"raw/a.md","chunk":4}`),    // beyond total
			call("c4", "raw_get", `{"source":"raw/a.md","chunk":-1}`),   // below 1
			call("c5", "wiki_get", `{"source":"raw/a.md","chunk":2}`),   // not raw.get
			call("c6", "raw_get", `{not json`),                          // unparseable arguments
			call("c7", "raw_get", ``),                                   // no arguments, no source
		)})
		rec.Done(trace.Done{Reason: "stop", Rounds: 1})

		read, total := ChunkCoverage([]*trace.Turn{loadTurn(t, dir, 1)}, "raw/a.md", body)
		if read != 2 || total != 3 {
			t.Errorf("ChunkCoverage = read %d, total %d, want chunks 1 and 3 of 3", read, total)
		}
	})

	t.Run("a canonical name counts like a wire name", func(t *testing.T) {
		dir := t.TempDir()
		rec := startTurn(t, dir, 1)
		send(rec, 1, 1, request(t, llm.Message{Role: "user", Content: "q"}))
		rec.Response(trace.Response{Round: 1, Attempt: 1, Finish: "tool_calls", ToolCalls: toolCalls(
			call("c1", "raw.get", `{"source":"raw/a.md","chunk":3}`),
		)})
		rec.Done(trace.Done{Reason: "stop", Rounds: 1})
		read, total := ChunkCoverage([]*trace.Turn{loadTurn(t, dir, 1)}, "raw/a.md", body)
		if read != 1 || total != 3 {
			t.Errorf("ChunkCoverage = read %d, total %d, want 1 of 3", read, total)
		}
	})

	t.Run("distinct chunks are unioned across turns", func(t *testing.T) {
		dir := t.TempDir()
		for n, chunk := range []int{1, 3, 3} {
			rec := startTurn(t, dir, n+1)
			send(rec, 1, 1, request(t, llm.Message{Role: "user", Content: "q"}))
			rec.Response(trace.Response{Round: 1, Attempt: 1, Finish: "tool_calls", ToolCalls: toolCalls(
				call("c1", "raw_get", fmt.Sprintf(`{"source":"raw/a.md","chunk":%d}`, chunk)),
			)})
			rec.Done(trace.Done{Reason: "stop", Rounds: 1})
		}
		turns := []*trace.Turn{loadTurn(t, dir, 1), loadTurn(t, dir, 2), loadTurn(t, dir, 3)}
		read, total := ChunkCoverage(turns, "raw/a.md", body)
		if read != 2 || total != 3 {
			t.Errorf("ChunkCoverage = read %d, total %d, want 2 of 3", read, total)
		}
	})

	t.Run("no turns, nil turn and an empty body", func(t *testing.T) {
		if read, total := ChunkCoverage(nil, "raw/a.md", body); read != 0 || total != 3 {
			t.Errorf("no turns: read %d, total %d, want 0 of 3", read, total)
		}
		if read, total := ChunkCoverage([]*trace.Turn{nil}, "raw/a.md", ""); read != 0 || total != 1 {
			t.Errorf("nil turn, empty body: read %d, total %d, want 0 of 1", read, total)
		}
	})
}

// TestToolErrors is the frozen case: round 1 makes three tool calls and the
// second fails. The tool event records only THAT it failed — the result text
// lives in the next request's body, as the role:"tool" message carrying the
// call's id — so the reader goes to request 2 for it. Truncation is in runes
// (037 T2).
func TestToolErrors(t *testing.T) {
	const errText = "raw/x.md not found; provide the exact vault-relative path under raw/ — check a citing page's ^[raw/...] provenance marker; call raw.list to see every raw source"
	long := errText + strings.Repeat("语", 300)

	msgs1 := []llm.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "ingest it"}}
	assistant := llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{
		call("call-1", "wiki_search", `{"query":"x"}`),
		call("call-2", "raw_get", `{"source":"raw/x.md"}`),
		call("call-3", "raw_list", `{}`),
	}}
	toolMsgs := []llm.Message{
		{Role: "tool", ToolCallID: "call-1", Content: "no hits"},
		{Role: "tool", ToolCallID: "call-2", Content: long},
		{Role: "tool", ToolCallID: "call-3", Content: "raw/y.md"},
	}
	msgs2 := append(append(append([]llm.Message{}, msgs1...), assistant), toolMsgs...)

	// writeRound1 records the three-call round and its three tool events.
	writeRound1 := func(t *testing.T, rec *trace.Recorder) {
		t.Helper()
		send(rec, 1, 1, request(t, msgs1...))
		rec.Tool(trace.Tool{Round: 1, ID: "call-1", Name: "wiki.search", MS: 2, ResultBytes: 7})
		rec.Tool(trace.Tool{Round: 1, ID: "call-2", Name: "raw.get", IsError: true, MS: 1, ResultBytes: len(long)})
		rec.Tool(trace.Tool{Round: 1, ID: "call-3", Name: "raw.list", MS: 1, ResultBytes: 8})
		rec.Response(trace.Response{Round: 1, Attempt: 1, Finish: "tool_calls", ToolCalls: toolCalls(assistant.ToolCalls...)})
	}

	t.Run("the error text comes from the next request, first 200 runes", func(t *testing.T) {
		dir := t.TempDir()
		rec := startTurn(t, dir, 1)
		writeRound1(t, rec)
		send(rec, 2, 1, request(t, msgs2...))
		rec.Response(trace.Response{Round: 2, Attempt: 1, Finish: "stop", Text: "done"})
		rec.Done(trace.Done{Reason: "stop", Rounds: 2})

		got, err := ToolErrors(dir, turnID(1), loadTurn(t, dir, 1))
		if err != nil {
			t.Fatal(err)
		}
		want := []ToolError{{Round: 1, Name: "raw.get", Text: string([]rune(long)[:200])}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ToolErrors = %#v, want %#v", got, want)
		}
		if n := len([]rune(got[0].Text)); n != 200 {
			t.Errorf("Text is %d runes, want 200", n)
		}
	})

	t.Run("a short result is kept whole", func(t *testing.T) {
		dir := t.TempDir()
		rec := startTurn(t, dir, 1)
		writeRound1(t, rec)
		short := append([]llm.Message{}, msgs2...)
		short[len(msgs1)+1+1] = llm.Message{Role: "tool", ToolCallID: "call-2", Content: errText[:20]}
		send(rec, 2, 1, request(t, short...))
		rec.Response(trace.Response{Round: 2, Attempt: 1, Finish: "stop"})
		rec.Done(trace.Done{Reason: "stop", Rounds: 2})

		got, err := ToolErrors(dir, turnID(1), loadTurn(t, dir, 1))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Text != errText[:20] {
			t.Errorf("ToolErrors = %#v, want the 20-byte text whole", got)
		}
	})

	t.Run("the last round has no next request: empty Text, not an error", func(t *testing.T) {
		dir := t.TempDir()
		rec := startTurn(t, dir, 1)
		writeRound1(t, rec)
		rec.Done(trace.Done{Reason: "max_rounds", Rounds: 1})

		got, err := ToolErrors(dir, turnID(1), loadTurn(t, dir, 1))
		if err != nil {
			t.Fatalf("a missing next request must not be an error: %v", err)
		}
		want := []ToolError{{Round: 1, Name: "raw.get", Text: ""}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ToolErrors = %#v, want %#v", got, want)
		}
	})

	t.Run("the latest attempt of the next round is preferred", func(t *testing.T) {
		dir := t.TempDir()
		rec := startTurn(t, dir, 1)
		writeRound1(t, rec)
		stale := append([]llm.Message{}, msgs2...)
		stale[len(msgs1)+1+1] = llm.Message{Role: "tool", ToolCallID: "call-2", Content: "stale attempt text"}
		send(rec, 2, 1, request(t, stale...))
		rec.Response(trace.Response{Round: 2, Attempt: 1, Cut: true})
		rec.Retry(2, 2, "stream ended early")
		send(rec, 2, 2, request(t, msgs2...))
		rec.Response(trace.Response{Round: 2, Attempt: 2, Finish: "stop"})
		rec.Done(trace.Done{Reason: "stop", Rounds: 2})

		got, err := ToolErrors(dir, turnID(1), loadTurn(t, dir, 1))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Text != string([]rune(long)[:200]) {
			t.Errorf("ToolErrors = %#v, want the retry's (attempt 2) text", got)
		}
	})

	t.Run("a next request without that tool message gives empty Text", func(t *testing.T) {
		dir := t.TempDir()
		rec := startTurn(t, dir, 1)
		writeRound1(t, rec)
		send(rec, 2, 1, request(t, msgs1...)) // the results were elided
		rec.Response(trace.Response{Round: 2, Attempt: 1, Finish: "stop"})
		rec.Done(trace.Done{Reason: "stop", Rounds: 2})

		got, err := ToolErrors(dir, turnID(1), loadTurn(t, dir, 1))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Text != "" {
			t.Errorf("ToolErrors = %#v, want one error with empty Text", got)
		}
	})

	t.Run("a turn with no failing tool has no errors", func(t *testing.T) {
		dir := t.TempDir()
		rec := startTurn(t, dir, 1)
		send(rec, 1, 1, request(t, msgs1...))
		rec.Tool(trace.Tool{Round: 1, ID: "call-1", Name: "wiki.search"})
		rec.Response(trace.Response{Round: 1, Attempt: 1, Finish: "stop"})
		rec.Done(trace.Done{Reason: "stop", Rounds: 1})
		got, err := ToolErrors(dir, turnID(1), loadTurn(t, dir, 1))
		if err != nil || len(got) != 0 {
			t.Errorf("ToolErrors = %#v, %v, want none", got, err)
		}
	})

	t.Run("an unreadable next request is an error", func(t *testing.T) {
		dir := t.TempDir()
		rec := startTurn(t, dir, 1)
		writeRound1(t, rec)
		send(rec, 2, 1, request(t, msgs2...))
		rec.Done(trace.Done{Reason: "stop", Rounds: 2})
		if err := os.Remove(filepath.Join(dir, turnID(1), "req-02.json.gz")); err != nil {
			t.Fatal(err)
		}
		if _, err := ToolErrors(dir, turnID(1), loadTurn(t, dir, 1)); err == nil {
			t.Error("want an error when the next request's file is gone")
		}
	})
}

// TestProcessOf sums two turns: rounds that got a response, tool calls and
// errors from the tool events, tokens from the LAST attempt of each round,
// done reason max_rounds counted per turn, wall time summed. Turn 1's first
// round was retried and its cut first attempt carries (bogus) usage that must
// not be double-counted; turn 2 ends at the round cap (037 T2).
func TestProcessOf(t *testing.T) {
	dir := t.TempDir()
	msgs := []llm.Message{{Role: "user", Content: "q"}}

	rec := startTurn(t, dir, 1)
	send(rec, 1, 1, request(t, msgs...))
	rec.Response(trace.Response{Round: 1, Attempt: 1, Cut: true, Usage: &llm.Usage{InputTokens: 1000, OutputTokens: 100}})
	rec.Retry(1, 2, "stream ended early")
	send(rec, 1, 2, request(t, msgs...))
	rec.Tool(trace.Tool{Round: 1, ID: "a", Name: "raw.get"})
	rec.Tool(trace.Tool{Round: 1, ID: "b", Name: "raw.get", IsError: true})
	rec.Response(trace.Response{Round: 1, Attempt: 2, Finish: "tool_calls", Usage: &llm.Usage{InputTokens: 100, OutputTokens: 10, CachedTokens: 80, ReasoningTokens: 5}})
	send(rec, 2, 1, request(t, msgs...))
	rec.Response(trace.Response{Round: 2, Attempt: 1, Finish: "stop", Usage: &llm.Usage{InputTokens: 200, OutputTokens: 20, CachedTokens: 150}})
	rec.Done(trace.Done{Reason: "stop", Rounds: 2, WallMS: 1000})

	rec = startTurn(t, dir, 2)
	send(rec, 1, 1, request(t, msgs...))
	rec.Tool(trace.Tool{Round: 1, ID: "c", Name: "wiki.get"})
	rec.Response(trace.Response{Round: 1, Attempt: 1, Finish: "tool_calls", Usage: &llm.Usage{InputTokens: 50, OutputTokens: 5, ReasoningTokens: 1}})
	send(rec, 2, 1, request(t, msgs...))
	rec.Response(trace.Response{Round: 2, Attempt: 1, Finish: "tool_calls"}) // no usage object
	rec.Done(trace.Done{Reason: "max_rounds", Rounds: 2, WallMS: 2500})

	got := ProcessOf([]*trace.Turn{loadTurn(t, dir, 1), loadTurn(t, dir, 2)})
	want := Process{
		Rounds: 4, MaxRoundsHit: 1, ToolCalls: 3, ToolErrs: 1,
		InputTokens: 350, CachedTokens: 230, OutputTokens: 35, ReasoningTokens: 6,
		WallMS: 3500, HasUsage: true,
	}
	if got != want {
		t.Errorf("ProcessOf =\n%+v\nwant\n%+v", got, want)
	}
}

// TestProcessOfUsage pins the HasUsage corners: false when no response
// carried a usage object, and — by the frozen definition — true when ANY
// response did even if the round's last attempt did not, while tokens still
// come from the last attempt only (037 T2).
func TestProcessOfUsage(t *testing.T) {
	msgs := []llm.Message{{Role: "user", Content: "q"}}

	t.Run("no usage anywhere", func(t *testing.T) {
		dir := t.TempDir()
		rec := startTurn(t, dir, 1)
		send(rec, 1, 1, request(t, msgs...))
		rec.Response(trace.Response{Round: 1, Attempt: 1, Finish: "stop"})
		rec.Done(trace.Done{Reason: "stop", Rounds: 1, WallMS: 7})
		got := ProcessOf([]*trace.Turn{loadTurn(t, dir, 1)})
		want := Process{Rounds: 1, WallMS: 7}
		if got != want {
			t.Errorf("ProcessOf = %+v, want %+v", got, want)
		}
	})

	t.Run("an earlier attempt had usage, the last did not", func(t *testing.T) {
		dir := t.TempDir()
		rec := startTurn(t, dir, 1)
		send(rec, 1, 1, request(t, msgs...))
		rec.Response(trace.Response{Round: 1, Attempt: 1, Cut: true, Usage: &llm.Usage{InputTokens: 9, OutputTokens: 9}})
		send(rec, 1, 2, request(t, msgs...))
		rec.Response(trace.Response{Round: 1, Attempt: 2, Finish: "stop"})
		rec.Done(trace.Done{Reason: "stop", Rounds: 1})
		got := ProcessOf([]*trace.Turn{loadTurn(t, dir, 1)})
		want := Process{Rounds: 1, HasUsage: true}
		if got != want {
			t.Errorf("ProcessOf = %+v, want %+v", got, want)
		}
	})

	t.Run("no turns, a nil turn and a turn that never finished", func(t *testing.T) {
		if got := ProcessOf(nil); got != (Process{}) {
			t.Errorf("ProcessOf(nil) = %+v, want zero", got)
		}
		dir := t.TempDir()
		rec := startTurn(t, dir, 1)
		send(rec, 1, 1, request(t, msgs...)) // a request that never got a response, and no done
		rec.Close()
		got := ProcessOf([]*trace.Turn{nil, loadTurn(t, dir, 1)})
		if got != (Process{}) {
			t.Errorf("ProcessOf = %+v, want zero (no response, no done)", got)
		}
	})
}
