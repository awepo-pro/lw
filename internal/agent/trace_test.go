package agent

// trace_test.go pins 038 T4's frozen contract for the agent loop's half of
// the turn trace (MASTER §5): one turn id per Send — minted, or taken from
// the ctx — on every session Record and every loop/budget lw.log line, and
// one event stream per traced turn (request, response, tool, elide, retry,
// done) that tells the turn's whole story, on every exit path, without
// changing a byte of what lw sends. The streamer is the scripted fake
// wrapped in the one thing the real client does that the bare fake skips:
// handing the marshaled Request to the ctx's trace.Observer inside Stream
// (llm client.go newHTTPRequest), so a trace's request files are written
// exactly as production writes them.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/logging"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/trace"
)

// turnIDRE is NewID's output shape — the same one trace.turnNameRE matches
// turn directories by — so a test that reads an id off a Record knows it is
// looking at a minted id, not an empty or inherited one.
var turnIDRE = regexp.MustCompile(`^\d{8}T\d{6}Z-[0-9a-f]{4}$`)

// observingStreamer wraps fakeStreamer with the Observer call the real
// client makes inside Stream (038 T1): the Request marshaled and handed to
// trace.Observer just before the stream starts, so every scripted attempt
// writes its req-RR[-A].json.gz the way a real POST writes it.
type observingStreamer struct {
	fake *fakeStreamer
}

func (o *observingStreamer) Stream(ctx context.Context, req llm.Request) (<-chan llm.Chunk, error) {
	if b, err := json.Marshal(req); err == nil {
		trace.Observer{}.OnRequest(ctx, b)
	}
	return o.fake.Stream(ctx, req)
}

// newTraceLoop is newTestLoop with a traces dir under a private temp dir and
// an observing streamer: the traced twin of the suite's ordinary loops. cfg
// carries everything else (MaxToolRounds, ContextTokens, …) verbatim.
func newTraceLoop(t *testing.T, rounds [][]llm.Chunk, cfg LoopConfig) (*Loop, *testLoopFixture, *fakeStreamer, string) {
	t.Helper()
	fx := newTestLoopFixture(t)
	fake := &fakeStreamer{rounds: rounds}
	dir := filepath.Join(t.TempDir(), "traces")
	cfg.TraceDir = dir
	l := newLoop(&observingStreamer{fake: fake}, fx.reg, fx.store, fx.engine, cfg)
	return l, fx, fake, dir
}

// turnID returns the turn id the run stamped on its records — the same id
// that names the trace dir — failing if the records carry none.
func turnID(t *testing.T, fx *testLoopFixture) string {
	t.Helper()
	sess, err := fx.store.Get(fx.csID)
	if err != nil {
		t.Fatalf("Get session: %v", err)
	}
	if len(sess.Records) == 0 {
		t.Fatalf("session has no records")
	}
	return sess.Records[0].Turn
}

// eventKinds lists the turn's event kinds, one per events.ndjson line, in
// file order — the shape every "kinds in order" pin below asserts on.
func eventKinds(t *testing.T, dir, id string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, id, "events.ndjson"))
	if err != nil {
		t.Fatalf("read events.ndjson: %v", err)
	}
	var kinds []string
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			continue
		}
		var ev struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("event line %q does not unmarshal: %v", line, err)
		}
		kinds = append(kinds, ev.Kind)
	}
	return kinds
}

// twoRoundRounds is the canonical 2-round script: a stage.close call, then
// a prose stop.
func twoRoundRounds() [][]llm.Chunk {
	return [][]llm.Chunk{
		{toolCallChunk("call-1", "stage.close", ""), {Finish: "tool_calls"}},
		{{Text: "All done."}, {Finish: "stop"}},
	}
}

// TestTurnIDOnRecordsAndLog: one minted id per turn — matching NewID's
// shape — lands on every session Record, names the trace dir, and tags
// every loop log line in lw.log as turn=<id>.
func TestTurnIDOnRecordsAndLog(t *testing.T) {
	logPath := installFileLog(t)
	l, fx, _, dir := newTraceLoop(t, twoRoundRounds(), LoopConfig{})

	out := make(chan Event, 64)
	if err := l.Send(context.Background(), fx.csID, "tag this turn", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	drain(out)

	sess, err := fx.store.Get(fx.csID)
	if err != nil {
		t.Fatalf("Get session: %v", err)
	}
	id := sess.Records[0].Turn
	if !turnIDRE.MatchString(id) {
		t.Fatalf("first record Turn = %q, want a minted id matching %s", id, turnIDRE)
	}
	for i, r := range sess.Records {
		if r.Turn != id {
			t.Errorf("record %d (%s) Turn = %q, want the turn's own id %q", i, r.Role, r.Turn, id)
		}
	}

	if _, err := os.Stat(filepath.Join(dir, id)); err != nil {
		t.Errorf("trace dir %s: %v — the id that tags the records must name the trace", filepath.Join(dir, id), err)
	}

	for _, line := range strings.Split(readLog(t, logPath), "\n") {
		if !strings.Contains(line, `msg="agent`) {
			continue
		}
		if !strings.Contains(line, "turn="+id) {
			t.Errorf("loop log line carries no turn=%s:\n%s", id, line)
		}
	}
}

// TestTurnIDFromContextHonoured: an id the caller put on the ctx with
// logging.WithTurn is kept — it names the Records and the trace dir —
// rather than being replaced by a freshly minted one.
func TestTurnIDFromContextHonoured(t *testing.T) {
	const id = "20260101T000000Z-abcd"
	l, fx, _, dir := newTraceLoop(t, twoRoundRounds(), LoopConfig{})

	out := make(chan Event, 64)
	if err := l.Send(logging.WithTurn(context.Background(), id), fx.csID, "name this turn yourself", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	drain(out)

	sess, err := fx.store.Get(fx.csID)
	if err != nil {
		t.Fatalf("Get session: %v", err)
	}
	for i, r := range sess.Records {
		if r.Turn != id {
			t.Errorf("record %d (%s) Turn = %q, want the ctx's id %q", i, r.Role, r.Turn, id)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, id)); err != nil {
		t.Errorf("trace dir %s: %v — the ctx's id must name the trace", filepath.Join(dir, id), err)
	}
	if _, err := trace.Load(dir, id); err != nil {
		t.Errorf("Load: %v", err)
	}
}

// TestTraceTwoRoundEvents: a clean 2-round turn's events read, in order,
// turn, request, tool, response, request, response, done — the request
// landing before its round's tool (the Observer fires inside Stream, before
// any chunk), the response after it (the round ends after the dispatch).
// The final response carries the fake's usage chunk, and done is {stop, 2}.
func TestTraceTwoRoundEvents(t *testing.T) {
	rounds := [][]llm.Chunk{
		{toolCallChunk("call-1", "stage.close", ""), {Finish: "tool_calls"}},
		{{Text: "All done."}, {Finish: "stop"},
			// The trailing usage chunk, the DeepSeek shape (038 C-4): after
			// the finish chunk, empty of content. Numbers are the live z.ai
			// round's (runs/ground/glm-real-usage-round.sse).
			{Usage: &llm.Usage{InputTokens: 5473, OutputTokens: 115, ReasoningTokens: 87}}},
	}
	l, fx, _, dir := newTraceLoop(t, rounds, LoopConfig{})

	out := make(chan Event, 64)
	if err := l.Send(context.Background(), fx.csID, "trace me end to end", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	drain(out)

	id := turnID(t, fx)
	got := eventKinds(t, dir, id)
	want := []string{"turn", "request", "tool", "response", "request", "response", "done"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("event kinds = %v, want %v", got, want)
	}

	turn, err := trace.Load(dir, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(turn.Attempts) != 2 {
		t.Fatalf("Load gave %d attempts, want 2: %+v", len(turn.Attempts), turn.Attempts)
	}
	final := turn.Attempts[1].Response
	if final == nil {
		t.Fatalf("round 2's attempt carries no response: %+v", turn.Attempts[1])
	}
	if final.Usage == nil {
		t.Fatalf("final response has no usage, want the fake's trailing usage chunk")
	}
	if final.Usage.InputTokens != 5473 || final.Usage.OutputTokens != 115 ||
		final.Usage.CachedTokens != 0 || final.Usage.ReasoningTokens != 87 {
		t.Errorf("final response usage = %+v, want {5473 115 0 87}", final.Usage)
	}
	if turn.Done == nil || turn.Done.Reason != "stop" || turn.Done.Rounds != 2 {
		t.Errorf("done = %+v, want {stop, 2}", turn.Done)
	}
}

// TestTraceMaxRounds: a turn the round cap ends is done {max_rounds}, with
// every round it ran traced.
func TestTraceMaxRounds(t *testing.T) {
	rounds := [][]llm.Chunk{
		{toolCallChunk("c1", "stage.close", ""), {Finish: "tool_calls"}},
		{toolCallChunk("c2", "stage.close", ""), {Finish: "tool_calls"}},
	}
	l, fx, _, dir := newTraceLoop(t, rounds, LoopConfig{MaxToolRounds: 2})

	out := make(chan Event, 64)
	if err := l.Send(context.Background(), fx.csID, "hit the cap", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	drain(out)

	turn, err := trace.Load(dir, turnID(t, fx))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if turn.Done == nil || turn.Done.Reason != "max_rounds" || turn.Done.Rounds != 2 {
		t.Fatalf("done = %+v, want {max_rounds, 2}", turn.Done)
	}
}

// TestTraceRetryBothAttempts: a 035 (B) cut — nothing dispatched — keeps
// both attempts visible: the cut attempt's response is Cut with its partial
// text, a retry event names the attempt that will be sent (round 1,
// attempt 2, C-5), and the two request files are req-01.json.gz and
// req-01-2.json.gz. The turn then stops cleanly.
func TestTraceRetryBothAttempts(t *testing.T) {
	rounds := [][]llm.Chunk{
		{{Text: "partial "}, cutChunk()},
		{{Text: "recovered"}, {Finish: "stop"}},
	}
	l, fx, _, dir := newTraceLoop(t, rounds, LoopConfig{})

	out := make(chan Event, 64)
	if err := l.Send(context.Background(), fx.csID, "cut me once", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	drain(out)

	turn, err := trace.Load(dir, turnID(t, fx))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(turn.Attempts) != 2 {
		t.Fatalf("Load gave %d attempts, want both of the round's attempts: %+v", len(turn.Attempts), turn.Attempts)
	}
	first, second := turn.Attempts[0], turn.Attempts[1]
	if first.File != "req-01.json.gz" || second.File != "req-01-2.json.gz" {
		t.Errorf("request files = %q, %q; want req-01.json.gz, req-01-2.json.gz", first.File, second.File)
	}
	if first.Attempt != 1 || second.Attempt != 2 {
		t.Errorf("attempt numbers = %d, %d; want 1, 2", first.Attempt, second.Attempt)
	}
	if first.Response == nil || !first.Response.Cut {
		t.Errorf("attempt 1 response = %+v, want Cut:true", first.Response)
	} else if first.Response.Text != "partial " {
		t.Errorf("attempt 1 kept text %q, want the partial %q the cut threw away", first.Response.Text, "partial ")
	}
	if len(turn.Retries) != 1 || turn.Retries[0] != (trace.RetryInfo{Round: 1, Attempt: 2, Reason: "stream ended early"}) {
		t.Errorf("retries = %+v, want [{1 2 stream ended early}]", turn.Retries)
	}
	if turn.Done == nil || turn.Done.Reason != "stop" || turn.Done.Rounds != 1 {
		t.Errorf("done = %+v, want {stop, 1}", turn.Done)
	}
}

// TestTraceContinueCut: a 035 (A) cut — a tool call was already dispatched
// mid-stream — marks that attempt's response Cut (the stream did end early)
// and the turn carries on to round 2, with no retry event (a retry would
// re-dispatch the call).
func TestTraceContinueCut(t *testing.T) {
	rounds := [][]llm.Chunk{
		{toolCallChunk("call-1", "stage.close", ""), cutChunk()},
		{{Text: "carried on"}, {Finish: "stop"}},
	}
	l, fx, _, dir := newTraceLoop(t, rounds, LoopConfig{})

	out := make(chan Event, 64)
	if err := l.Send(context.Background(), fx.csID, "cut after a dispatch", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	drain(out)

	turn, err := trace.Load(dir, turnID(t, fx))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(turn.Attempts) != 2 {
		t.Fatalf("Load gave %d attempts, want one per round: %+v", len(turn.Attempts), turn.Attempts)
	}
	if turn.Attempts[0].Response == nil || !turn.Attempts[0].Response.Cut {
		t.Errorf("round 1 response = %+v, want Cut:true", turn.Attempts[0].Response)
	}
	if len(turn.Retries) != 0 {
		t.Errorf("retries = %+v, want none — the cut round's call already ran", turn.Retries)
	}
	if turn.Done == nil || turn.Done.Reason != "stop" || turn.Done.Rounds != 2 {
		t.Errorf("done = %+v, want {stop, 2}", turn.Done)
	}
}

// TestTraceErrorAndCanceled: a Stream that fails before opening ends the
// turn done {error} carrying the error text; a ctx canceled mid-stream ends
// the attempt's response with "context canceled" and the turn done
// {canceled} — never mistaken for a stop.
func TestTraceErrorAndCanceled(t *testing.T) {
	t.Run("stream_error", func(t *testing.T) {
		l, fx, _, dir := newTraceLoop(t, [][]llm.Chunk{}, LoopConfig{}) // nothing scripted: Stream fails

		out := make(chan Event, 64)
		err := l.Send(context.Background(), fx.csID, "fail to stream", out)
		if err == nil {
			t.Fatalf("Send: want the Stream error")
		}
		drain(out)

		turn, err2 := trace.Load(dir, turnID(t, fx))
		if err2 != nil {
			t.Fatalf("Load: %v", err2)
		}
		if turn.Done == nil || turn.Done.Reason != "error" {
			t.Fatalf("done = %+v, want reason error", turn.Done)
		}
		if turn.Done.Error != err.Error() {
			t.Errorf("done.Error = %q, want Send's own error text %q", turn.Done.Error, err.Error())
		}
		if len(turn.Attempts) != 1 || turn.Attempts[0].Response == nil || turn.Attempts[0].Response.Error == "" {
			t.Errorf("attempts = %+v, want one attempt whose response carries the error", turn.Attempts)
		}
	})

	t.Run("canceled_mid_stream", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		fake := &fakeStreamer{rounds: [][]llm.Chunk{{{Text: "half a thought"}}}, afterFirstChunk: cancel}
		dir := filepath.Join(t.TempDir(), "traces")
		fx := newTestLoopFixture(t)
		l := newLoop(&observingStreamer{fake: fake}, fx.reg, fx.store, fx.engine, LoopConfig{TraceDir: dir})

		out := make(chan Event, 64)
		err := l.Send(ctx, fx.csID, "hang up mid-sentence", out)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Send error = %v, want context.Canceled", err)
		}
		drain(out)

		turn, lerr := trace.Load(dir, turnID(t, fx))
		if lerr != nil {
			t.Fatalf("Load: %v", lerr)
		}
		if len(turn.Attempts) != 1 || turn.Attempts[0].Response == nil {
			t.Fatalf("attempts = %+v, want one with a response", turn.Attempts)
		}
		if got := turn.Attempts[0].Response.Error; got != context.Canceled.Error() {
			t.Errorf("response error = %q, want %q", got, context.Canceled.Error())
		}
		if turn.Done == nil || turn.Done.Reason != "canceled" {
			t.Errorf("done = %+v, want reason canceled", turn.Done)
		}
	})
}

// TestTraceOffSameRequests: tracing is observation only. The same script
// sent with TraceDir set and with it "" puts deep-equal llm.Request slices
// in front of the streamer; the untraced run creates nothing on disk and
// still tags its records with a minted turn id.
func TestTraceOffSameRequests(t *testing.T) {
	const msg = "the very same turn text, twice"
	l1, fx1, fake1, _ := newTraceLoop(t, twoRoundRounds(), LoopConfig{})
	out := make(chan Event, 64)
	if err := l1.Send(context.Background(), fx1.csID, msg, out); err != nil {
		t.Fatalf("Send (traced): %v", err)
	}
	drain(out)

	l2, fx2, fake2, _ := newTraceLoop(t, twoRoundRounds(), LoopConfig{})
	l2.cfg.TraceDir = "" // the untraced twin: same everything else
	out2 := make(chan Event, 64)
	if err := l2.Send(context.Background(), fx2.csID, msg, out2); err != nil {
		t.Fatalf("Send (untraced): %v", err)
	}
	drain(out2)

	// The two fixtures mint their own changeset ids and stage.close's
	// result quotes its own, so the requests are compared as marshaled
	// bytes with each run's id normalized out — leaving exactly what
	// tracing could have changed: nothing.
	requestBytes := func(fake *fakeStreamer, csID string) []string {
		out := make([]string, 0)
		for _, r := range fake.Requests() {
			b, err := json.Marshal(r)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			out = append(out, strings.ReplaceAll(string(b), csID, "CS"))
		}
		return out
	}
	if !reflect.DeepEqual(requestBytes(fake1, fx1.csID), requestBytes(fake2, fx2.csID)) {
		t.Fatalf("traced and untraced runs sent different requests:\ntraced:   %s\nuntraced: %s",
			requestBytes(fake1, fx1.csID), requestBytes(fake2, fx2.csID))
	}

	root := fx2.engine.Vault().Root()
	if _, err := os.Stat(filepath.Join(root, ".llmwiki", "traces")); !os.IsNotExist(err) {
		t.Errorf("untraced run left a traces dir behind: %v", err)
	}
	for i, r := range mustRecords(t, fx2) {
		if !turnIDRE.MatchString(r.Turn) {
			t.Errorf("untraced record %d Turn = %q, want a minted id — tracing off changes nothing about the id", i, r.Turn)
		}
	}
}

// mustRecords returns the run's session records.
func mustRecords(t *testing.T, fx *testLoopFixture) []Record {
	t.Helper()
	sess, err := fx.store.Get(fx.csID)
	if err != nil {
		t.Fatalf("Get session: %v", err)
	}
	return sess.Records
}

// TestTraceElide: an elision the budget performs is an elide event on the
// turn's trace, with the same count and bytes its budget log line carries.
func TestTraceElide(t *testing.T) {
	logPath := installFileLog(t)

	// Size off a control run at 1 000 000 (the P2 sizing): budget = round
	// 3's over-budget estimate minus 2000 tokens, so exactly one elision —
	// round 1's 20000-byte wiki.get result — must fire in round 3.
	ctlFake, _, _ := runBigTurn(t, LoopConfig{ContextTokens: 1000000})
	budget := wireEstimate(ctlFake.Requests()[2].Messages) - 2000

	fx, _, _ := newBudgetFixture(t, bigResultBytes)
	fake := &fakeStreamer{rounds: bigWikiRounds()}
	dir := filepath.Join(t.TempDir(), "traces")
	l := newLoop(&observingStreamer{fake: fake}, fx.reg, fx.store, fx.engine,
		LoopConfig{ContextTokens: budget, TraceDir: dir})

	out := make(chan Event, 256)
	if err := l.Send(context.Background(), fx.csID, "read past the budget", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	drain(out)

	turn, err := trace.Load(dir, turnID(t, fx))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(turn.Elisions) != 1 {
		t.Fatalf("elisions = %+v, want exactly one", turn.Elisions)
	}
	el := turn.Elisions[0]
	if el.Round != 3 || el.Count != 1 || el.Bytes != bigResultBytes {
		t.Fatalf("elide event = %+v, want {round 3, count 1, bytes %d}", el, bigResultBytes)
	}

	// The same facts the log line carries (F.C5), from the same round.
	var logCount, logBytes int
	for _, line := range strings.Split(readLog(t, logPath), "\n") {
		if !strings.Contains(line, `msg="context elided"`) {
			continue
		}
		logCount = scanAttr(t, line, "messages")
		logBytes = scanAttr(t, line, "bytes")
	}
	if logCount != el.Count || logBytes != el.Bytes {
		t.Errorf("budget log line says messages=%d bytes=%d; elide event says %d/%d — they must agree",
			logCount, logBytes, el.Count, el.Bytes)
	}
}

// scanAttr reads one key=value attribute out of a slog text line.
func scanAttr(t *testing.T, line, key string) int {
	t.Helper()
	var n int
	if _, err := fmt.Sscanf(line, key+"=%d", &n); err == nil {
		return n
	}
	i := strings.Index(line, key+"=")
	if i < 0 {
		t.Fatalf("line has no %s=: %s", key, line)
	}
	if _, err := fmt.Sscanf(line[i:], key+"=%d", &n); err != nil {
		t.Fatalf("scan %s from %s: %v", key, line, err)
	}
	return n
}

// nilDocExtractor hands stage.ingest_source a nil document — the one
// dispatch this suite can reach that makes Registry.Call fail with a HARD
// error (the turn aborts) rather than an IsError result the model could
// correct: an extractor returning no document is an internal fault, not
// model-visible feedback (tools stage_source.go).
type nilDocExtractor struct{}

func (nilDocExtractor) CanHandle(string) bool { return true }

func (nilDocExtractor) Extract(context.Context, string) (*extract.Doc, error) {
	return nil, nil
}

// TestTraceToolHardError: a dispatched call whose registry Call fails with
// a non-correctable error aborts the turn done {error} — but its one tool
// row still lands, IsError under the canonical name, so the dispatch that
// killed the turn is not the one missing from the trace (038 T4, A7: one
// rec.Tool per dispatched call, however it ends).
func TestTraceToolHardError(t *testing.T) {
	fx := newTestLoopFixture(t)
	// The fixture's own registry has no extractor, so its stage.ingest_source
	// answers IsError; this twin carries the nil-document extractor that
	// makes the Call itself fail. Everything else is the fixture's.
	reg := tools.NewRegistry(tools.Deps{
		Vault:   fx.engine.Vault(),
		Index:   fx.engine.Index(),
		Engine:  fx.engine,
		Author:  stage.Author{Kind: "agent", Model: "test-model"},
		Extract: nilDocExtractor{},
	})
	fake := &fakeStreamer{rounds: [][]llm.Chunk{
		// The wire-spelled name, as a provider echoes it back — the trace's
		// tool row must carry the canonical form (038 T4, A7).
		{toolCallChunk("call-1", "stage_ingest_source", `{"uri":"/tmp/no-such-source.html","kind":"paper"}`)},
	}}
	dir := filepath.Join(t.TempDir(), "traces")
	l := newLoop(&observingStreamer{fake: fake}, reg, fx.store, fx.engine, LoopConfig{TraceDir: dir})

	out := make(chan Event, 64)
	err := l.Send(context.Background(), fx.csID, "break a tool", out)
	if err == nil {
		t.Fatalf("Send: want the hard Call error")
	}
	drain(out)

	turn, lerr := trace.Load(dir, turnID(t, fx))
	if lerr != nil {
		t.Fatalf("Load: %v", lerr)
	}
	if turn.Done == nil || turn.Done.Reason != "error" {
		t.Fatalf("done = %+v, want reason error", turn.Done)
	}
	if len(turn.Attempts) != 1 || len(turn.Attempts[0].Calls) != 1 {
		t.Fatalf("attempts = %+v, want one attempt carrying the failed call", turn.Attempts)
	}
	call := turn.Attempts[0].Calls[0]
	if call.ID != "call-1" || call.Name != "stage.ingest_source" || !call.IsError {
		t.Errorf("tool row = %+v, want {ID call-1, Name stage.ingest_source, IsError true}", call)
	}
}

// TestTraceStartFailureTurnSucceeds: a traces dir lw cannot create costs
// exactly one WARN — the turn itself runs to its clean stop, untraced,
// never hindered.
func TestTraceStartFailureTurnSucceeds(t *testing.T) {
	logPath := installFileLog(t)
	ro := t.TempDir()
	if err := os.Chmod(ro, 0o500); err != nil {
		t.Fatalf("chmod read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) }) // let the temp dir be removed

	fx := newTestLoopFixture(t)
	fake := &fakeStreamer{rounds: [][]llm.Chunk{{{Text: "42."}, {Finish: "stop"}}}}
	l := newLoop(&observingStreamer{fake: fake}, fx.reg, fx.store, fx.engine,
		LoopConfig{TraceDir: filepath.Join(ro, "traces")})

	out := make(chan Event, 64)
	if err := l.Send(context.Background(), fx.csID, "trace what you cannot", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	events := drain(out)

	last := events[len(events)-1]
	done, ok := last.(DoneEv)
	if !ok || done.Reason != "stop" || done.Rounds != 1 {
		t.Fatalf("last event = %#v, want DoneEv{stop, 1} — a failed Start must not fail the turn", last)
	}
	if n := strings.Count(readLog(t, logPath), "trace write failed"); n != 1 {
		t.Errorf("log carries %d \"trace write failed\" records, want exactly 1:\n%s", n, readLog(t, logPath))
	}
}
