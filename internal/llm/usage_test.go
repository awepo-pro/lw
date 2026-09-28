package llm

// usage_test.go pins 038 T1's provider-accounting and observation contract:
// a usage object decoded from whichever chunk carries it (the finish chunk
// on z.ai, a trailing choices-less chunk on DeepSeek/OpenAI), delivered as
// exactly one final Chunk{Usage} on a CLEAN end only, one "llm usage" INFO
// record beside it, an Observer that sees the exact request bytes without
// moving them, and every llm record carrying the request's ctx so a turn id
// reaches the handler. The z.ai body is the verbatim 038 ground capture
// (runs/ground/glm-real-usage-round.sse), copied byte-for-byte into
// testdata/.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/awepo-pro/lw/internal/logging"
)

// TestUsageOnFinishChunk is the z.ai shape, on the real wire bytes: usage
// rides the finish_reason:"tool_calls" chunk itself. The stream yields the
// captured round unchanged — every reasoning fragment, the two tool calls
// (wiki_search completed when index 1 starts, wiki_get at the finish), the
// finish chunk — and then exactly one Usage chunk, last, before close.
func TestUsageOnFinishChunk(t *testing.T) {
	body := loadFixture(t, "glm-real-usage-round.sse")
	srv := sseServer(t, body, nil)
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "test-model"})
	ch, err := c.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	chunks := collect(t, ch)

	// The capture holds 87 reasoning deltas, then the two whole tool-call
	// deltas, then the finish chunk carrying the usage object, then [DONE].
	const reasoningChunks = 87
	if len(chunks) != reasoningChunks+4 {
		t.Fatalf("got %d chunks, want %d reasoning + 2 tool calls + finish + usage: %+v",
			len(chunks), reasoningChunks, chunks)
	}
	for i, c := range chunks[:reasoningChunks] {
		if c.Reasoning == "" {
			t.Errorf("chunks[%d] = %+v, want a reasoning fragment", i, c)
		}
	}
	if tc := chunks[87].ToolCall; tc == nil || tc.Function.Name != "wiki_search" {
		t.Errorf("chunks[87] = %+v, want the completed wiki_search call", chunks[87])
	}
	if tc := chunks[88].ToolCall; tc == nil || tc.Function.Name != "wiki_get" {
		t.Errorf("chunks[88] = %+v, want the completed wiki_get call", chunks[88])
	}
	if chunks[89].Finish != "tool_calls" {
		t.Errorf("chunks[89] = %+v, want Finish=tool_calls", chunks[89])
	}
	for i, c := range chunks {
		if c.Err != nil {
			t.Errorf("chunks[%d] carries an error; the captured round ends clean: %v", i, c.Err)
		}
	}

	last := chunks[len(chunks)-1]
	if last.Usage == nil {
		t.Fatalf("last chunk = %+v, want the Usage chunk", last)
	}
	want := Usage{InputTokens: 5473, OutputTokens: 115, CachedTokens: 0, ReasoningTokens: 87}
	if *last.Usage != want {
		t.Errorf("usage = %+v, want %+v (prompt_tokens_details.cached_tokens and "+
			"completion_tokens_details.reasoning_tokens mapped through)", *last.Usage, want)
	}
}

// TestUsageOnTrailingChunk is the DeepSeek/OpenAI shape: the usage object
// arrives on its OWN chunk, with an empty choices array, after the finish
// chunk. The choices-less chunk emits nothing of its own — the usage it
// carries becomes the stream's one final Usage chunk.
func TestUsageOnTrailingChunk(t *testing.T) {
	body := []byte(
		`data: {"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}` + "\n\n" +
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
			`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":8}}}` + "\n\n" +
			"data: [DONE]\n\n")

	chunks := streamChunks(t, body)

	if len(chunks) != 3 {
		t.Fatalf("got %d chunks, want text + finish + usage: %+v", len(chunks), chunks)
	}
	if chunks[0].Text != "hi" {
		t.Errorf("chunks[0] = %+v, want Text=hi", chunks[0])
	}
	if chunks[1].Finish != "stop" {
		t.Errorf("chunks[1] = %+v, want Finish=stop", chunks[1])
	}
	if chunks[2].Usage == nil {
		t.Fatalf("chunks[2] = %+v, want the Usage chunk last", chunks[2])
	}
	// reasoning_tokens is absent on this wire shape and decodes as 0.
	want := Usage{InputTokens: 10, OutputTokens: 2, CachedTokens: 8, ReasoningTokens: 0}
	if *chunks[2].Usage != want {
		t.Errorf("usage = %+v, want %+v", *chunks[2].Usage, want)
	}
}

// TestNoUsageNoChunk pins the negative half of U2: a stream whose provider
// sent no usage object never yields a Usage chunk — a nil Usage stays nil,
// and nothing stands in for accounting the provider never sent.
func TestNoUsageNoChunk(t *testing.T) {
	chunks := streamChunks(t, loadFixture(t, "simple.sse"))

	if len(chunks) != 2 || chunks[1].Finish != "stop" {
		t.Fatalf("chunks = %+v, want the clean text+finish stream", chunks)
	}
	for i, c := range chunks {
		if c.Usage != nil {
			t.Errorf("chunks[%d] carries Usage %+v; a stream with no usage object must yield no Usage chunk", i, *c.Usage)
		}
	}
}

// TestUsageNeverOnTruncation pins the boundary of U2: a usage object seen
// WITHOUT a finish_reason, then EOF (035 R3) — the round was cut, its
// accounting is worthless, and an Err path never emits Usage. A retrying
// consumer must never mistake a cut round for an accounted one.
func TestUsageNeverOnTruncation(t *testing.T) {
	body := glmBody(
		glmLine(`"role":"assistant","content":"ZEBRAPAYLOAD"`),
		`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":8}}}`,
	)

	chunks := streamChunks(t, body)

	if len(chunks) != 2 || chunks[0].Text != "ZEBRAPAYLOAD" {
		t.Fatalf("chunks = %+v, want the text delta then the truncation error", chunks)
	}
	err := chunks[1].Err
	if err == nil || !errors.Is(err, ErrStreamTruncated) {
		t.Fatalf("chunks[1] = %+v, want the ErrStreamTruncated error", chunks[1])
	}
	for i, c := range chunks {
		if c.Usage != nil {
			t.Errorf("chunks[%d] carries Usage %+v off a TRUNCATED stream — an Err path never emits Usage", i, *c.Usage)
		}
	}
}

// copyObserver is the observing side of TestObserverSeesExactBody: it
// counts its calls and clones the body it is handed — the trace's own
// obligation, since the slice is only valid for the call.
type copyObserver struct {
	calls int
	seen  []byte
}

var _ Observer = (*copyObserver)(nil)

func (o *copyObserver) OnRequest(ctx context.Context, body []byte) {
	o.calls++
	o.seen = bytes.Clone(body)
}

// TestObserverSeesExactBody pins U4: the Observer is handed the exact body
// bytes about to be POSTed — byte-identical to what the server receives,
// exactly once per Stream and once per Probe — and a nil Observer leaves
// the wire bytes identical for the same Request. Tracing is observation
// only: nothing it does may change a byte lw sends.
func TestObserverSeesExactBody(t *testing.T) {
	sseBody := loadFixture(t, "simple.sse")

	// recorder is the observing side: it clones what it is handed, as the
	// trace must — the slice is only valid for the call.
	rec := &copyObserver{}

	// receivingServer replays sseBody and records the raw request bodies
	// it receives, in order.
	var mu sync.Mutex
	var received [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		received = append(received, b)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(sseBody)
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "test-model", Observer: rec})

	ch, err := c.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	collect(t, ch)

	mu.Lock()
	streamSent := append([][]byte(nil), received...)
	mu.Unlock()
	if len(streamSent) != 1 {
		t.Fatalf("server received %d request bodies for one Stream, want 1", len(streamSent))
	}
	if rec.calls != 1 {
		t.Errorf("Observer called %d times for one Stream, want exactly 1", rec.calls)
	}
	if !bytes.Equal(rec.seen, streamSent[0]) {
		t.Errorf("Observer saw %d bytes, server received %d — the observed body must be "+
			"byte-identical to the wire body", len(rec.seen), len(streamSent[0]))
	}

	// Probe shares the pipeline, so it observes too — once.
	rec.calls, rec.seen = 0, nil
	if res := c.Probe(context.Background()); res.Err != nil {
		t.Fatalf("Probe: %v", res.Err)
	}
	mu.Lock()
	probeSent := append([][]byte(nil), received...)
	mu.Unlock()
	if len(probeSent) != 2 {
		t.Fatalf("server received %d request bodies after one Probe, want 2", len(probeSent))
	}
	if rec.calls != 1 {
		t.Errorf("Observer called %d times for one Probe, want exactly 1", rec.calls)
	}
	if !bytes.Equal(rec.seen, probeSent[1]) {
		t.Errorf("Observer saw %d bytes, server received %d on Probe", len(rec.seen), len(probeSent[1]))
	}

	// nil Observer: the same Request must reach the wire byte-identically —
	// observing changes nothing.
	var nilReceived []byte
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nilReceived, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(sseBody)
	}))
	defer srv2.Close()

	c2 := New(Config{BaseURL: srv2.URL, Model: "test-model"})
	ch2, err := c2.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream without Observer: %v", err)
	}
	collect(t, ch2)

	if !bytes.Equal(nilReceived, streamSent[0]) {
		t.Errorf("nil-Observer run sent %d bytes, observed run sent %d — the wire bytes must be "+
			"identical for the same Request", len(nilReceived), len(streamSent[0]))
	}
}

// TestUsageLogLine pins U3: when the Usage chunk is emitted, exactly one
// "llm usage" INFO record rides beside it, with the four counts as attrs in
// the contract's order — and the "llm finish" line stays byte-unchanged,
// carrying none of the usage fields.
func TestUsageLogLine(t *testing.T) {
	logPath := installFileLog(t)

	chunks := streamChunks(t, loadFixture(t, "glm-real-usage-round.sse"))
	if len(chunks) == 0 || chunks[len(chunks)-1].Usage == nil {
		t.Fatalf("stream did not end in the Usage chunk: %+v", chunks)
	}

	log := readLog(t, logPath)
	if got := strings.Count(log, `msg="llm usage"`); got != 1 {
		t.Fatalf("log holds %d \"llm usage\" records, want exactly one:\n%s", got, log)
	}
	var rec string
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, `msg="llm usage"`) {
			rec = line
			break
		}
	}
	// The attrs ride in the contract's order: input, output, cached,
	// reasoning.
	last := -1
	for _, want := range []string{
		"input_tokens=5473",
		"output_tokens=115",
		"cached_tokens=0",
		"reasoning_tokens=87",
	} {
		i := strings.Index(rec, want)
		if i < 0 {
			t.Fatalf("usage record missing %q:\n%s", want, rec)
		}
		if i < last {
			t.Fatalf("usage record attrs out of order at %q:\n%s", want, rec)
		}
		last = i
	}

	// The finish line keeps its frozen shape: the finish reason and
	// first_delta_ms, and no usage field folded into it.
	finishes := 0
	for _, line := range strings.Split(log, "\n") {
		if !strings.Contains(line, `msg="llm finish"`) {
			continue
		}
		finishes++
		for _, banned := range []string{"input_tokens", "output_tokens", "cached_tokens", "reasoning_tokens"} {
			if strings.Contains(line, banned) {
				t.Errorf("llm finish line grew a usage field %q — it must stay byte-unchanged:\n%s", banned, line)
			}
		}
	}
	if finishes != 1 {
		t.Errorf("log holds %d \"llm finish\" records, want exactly one:\n%s", finishes, log)
	}
}

// turnCapture is a slog.Handler that keeps the ctx each record arrived
// with, keyed by message — the direct view of U5: a *Context slog call
// hands the handler the caller's ctx, so a turn id riding it reaches every
// llm record and can join lw.log to the turn's trace.
type turnCapture struct {
	mu    sync.Mutex
	byMsg map[string]context.Context
}

func (h *turnCapture) Handle(ctx context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.byMsg == nil {
		h.byMsg = make(map[string]context.Context)
	}
	h.byMsg[r.Message] = ctx
	return nil
}

func (h *turnCapture) Enabled(context.Context, slog.Level) bool { return true }
func (h *turnCapture) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *turnCapture) WithGroup(string) slog.Handler            { return h }

// TestLogsCarryTurnContext pins U5 end to end: every record a turn's Stream
// writes — request, response, finish, usage — reaches the handler with the
// caller's ctx, so logging.WithTurn's id is readable from each of them.
func TestLogsCarryTurnContext(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	capture := &turnCapture{}
	slog.SetDefault(slog.New(capture))

	srv := sseServer(t, loadFixture(t, "glm-real-usage-round.sse"), nil)
	defer srv.Close()

	ctx := logging.WithTurn(context.Background(), "T-038")
	c := New(Config{BaseURL: srv.URL, Model: "test-model"})
	ch, err := c.Stream(ctx, Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	chunks := collect(t, ch)
	if len(chunks) == 0 || chunks[len(chunks)-1].Usage == nil {
		t.Fatalf("stream did not end in the Usage chunk: %+v", chunks)
	}

	for _, msg := range []string{"llm request", "llm response", "llm finish", "llm usage"} {
		capture.mu.Lock()
		rec, ok := capture.byMsg[msg]
		capture.mu.Unlock()
		if !ok {
			t.Fatalf("no %q record captured", msg)
		}
		if got := logging.TurnFrom(rec); got != "T-038" {
			t.Errorf("%q record's ctx carries turn %q, want T-038", msg, got)
		}
	}
}
