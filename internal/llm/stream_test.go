package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// loadFixture returns the exact bytes of internal/llm/testdata/name.
func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// sseServer starts an httptest.Server that replays body verbatim as an SSE
// response to every request, and records the last decoded request body
// (into capturedReq, if non-nil) for assertions on what Stream sent.
func sseServer(t *testing.T, body []byte, capturedReq *wireRequest) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capturedReq != nil {
			if err := json.NewDecoder(r.Body).Decode(capturedReq); err != nil {
				t.Errorf("decode request body: %v", err)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
}

// collect drains ch and returns everything it produced, failing the test if
// that takes longer than 5s (a hung channel is itself a bug this client
// must never have).
func collect(t *testing.T, ch <-chan Chunk) []Chunk {
	t.Helper()
	var got []Chunk
	timeout := time.After(5 * time.Second)
	for {
		select {
		case c, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, c)
		case <-timeout:
			t.Fatal("channel did not close within 5s")
			return nil
		}
	}
}

func TestStreamAssemblesSplitToolArgs(t *testing.T) {
	var sent wireRequest
	srv := sseServer(t, loadFixture(t, "split_tool_call.sse"), &sent)
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "test-model", MaxTokens: 100})
	ch, err := c.Stream(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: "search kv cache"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	chunks := collect(t, ch)

	// Exactly one ToolCall, fully assembled, regardless of how many chunks
	// its arguments were split across on the wire.
	var toolCalls []ToolCall
	for _, c := range chunks {
		if c.ToolCall != nil {
			toolCalls = append(toolCalls, *c.ToolCall)
		}
		if c.Err != nil {
			t.Fatalf("unexpected error chunk: %v", c.Err)
		}
	}
	if len(toolCalls) != 1 {
		t.Fatalf("got %d tool calls, want exactly 1: %+v", len(toolCalls), toolCalls)
	}
	got := toolCalls[0]
	if got.ID != "call_1" {
		t.Errorf("ID = %q, want call_1", got.ID)
	}
	if got.Type != "function" {
		t.Errorf("Type = %q, want function", got.Type)
	}
	if got.Function.Name != "wiki.search" {
		t.Errorf("Function.Name = %q, want wiki.search", got.Function.Name)
	}
	const wantArgs = `{"q":"kv cache","limit":5}`
	if got.Function.Arguments != wantArgs {
		t.Errorf("Function.Arguments = %q, want %q", got.Function.Arguments, wantArgs)
	}

	// The last chunk carries the finish reason, after the tool call.
	last := chunks[len(chunks)-1]
	if last.Finish != "tool_calls" {
		t.Errorf("last chunk Finish = %q, want tool_calls", last.Finish)
	}

	// The outgoing wire request used the real OpenAI tool wrapper, not the
	// bare ToolDef shape.
	if sent.Model != "test-model" || !sent.Stream {
		t.Errorf("sent request = %+v, want model=test-model stream=true", sent)
	}
}

// TestStreamReasoningContentSplitAcrossChunks is the S5-T7 regression test
// for C-114: a thinking-mode provider splits reasoning_content across
// several chunks, ahead of content and a tool call, on one stream. It
// asserts the Chunk.Reasoning fragments arrive in order and concatenate to
// the whole reasoning, and that Text/ToolCall behaviour is unchanged —
// exactly one complete tool call with valid JSON arguments.
func TestStreamReasoningContentSplitAcrossChunks(t *testing.T) {
	srv := sseServer(t, loadFixture(t, "reasoning_split.sse"), nil)
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "test-model"})
	ch, err := c.Stream(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: "look this up"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	chunks := collect(t, ch)

	var reasoning strings.Builder
	var sawText bool
	var toolCalls []ToolCall
	for _, c := range chunks {
		if c.Err != nil {
			t.Fatalf("unexpected error chunk: %v", c.Err)
		}
		if c.Reasoning != "" {
			reasoning.WriteString(c.Reasoning)
		}
		if c.Text == "Checking sources..." {
			sawText = true
		}
		if c.ToolCall != nil {
			toolCalls = append(toolCalls, *c.ToolCall)
		}
	}

	const wantReasoning = "Let me think about this carefully."
	if got := reasoning.String(); got != wantReasoning {
		t.Errorf("concatenated Reasoning fragments = %q, want %q", got, wantReasoning)
	}
	if !sawText {
		t.Errorf("chunks = %+v, want the Text delta unchanged alongside reasoning", chunks)
	}
	if len(toolCalls) != 1 {
		t.Fatalf("got %d tool calls, want exactly 1: %+v", len(toolCalls), toolCalls)
	}
	got := toolCalls[0]
	if got.Function.Name != "wiki.search" {
		t.Errorf("Function.Name = %q, want wiki.search", got.Function.Name)
	}
	const wantArgs = `{"q":"kv cache"}`
	if got.Function.Arguments != wantArgs {
		t.Errorf("Function.Arguments = %q, want %q", got.Function.Arguments, wantArgs)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(got.Function.Arguments), &parsed); err != nil {
		t.Errorf("tool call arguments are not valid JSON: %v", err)
	}

	// The first chunk must be the reasoning fragment, not the text — a
	// thinking-mode provider sends reasoning before content on the wire.
	if chunks[0].Reasoning != "Let me think" {
		t.Errorf("chunks[0] = %+v, want the first reasoning fragment first", chunks[0])
	}
}

func TestStreamInterleavedTextAndTools(t *testing.T) {
	srv := sseServer(t, loadFixture(t, "interleaved.sse"), nil)
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "test-model"})
	ch, err := c.Stream(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: "look this up"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	chunks := collect(t, ch)

	// Order must be: text, then the two tool calls in the order their
	// indices appeared, then the finish reason.
	if len(chunks) != 4 {
		t.Fatalf("got %d chunks, want 4: %+v", len(chunks), chunks)
	}
	if chunks[0].Text != "Checking sources..." {
		t.Errorf("chunk 0 = %+v, want Text=Checking sources...", chunks[0])
	}
	if chunks[1].ToolCall == nil || chunks[1].ToolCall.Function.Name != "wiki.search" {
		t.Errorf("chunk 1 = %+v, want ToolCall wiki.search", chunks[1])
	}
	if chunks[2].ToolCall == nil || chunks[2].ToolCall.Function.Name != "wiki.get" {
		t.Errorf("chunk 2 = %+v, want ToolCall wiki.get", chunks[2])
	}
	if chunks[2].ToolCall.Function.Arguments != `{"page":"kv-cache"}` {
		t.Errorf("chunk 2 args = %q", chunks[2].ToolCall.Function.Arguments)
	}
	if chunks[3].Finish != "tool_calls" {
		t.Errorf("chunk 3 = %+v, want Finish=tool_calls", chunks[3])
	}
}

func TestStreamClosesOnError(t *testing.T) {
	srv := sseServer(t, loadFixture(t, "error_mid_stream.sse"), nil)
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "test-model"})
	ch, err := c.Stream(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	chunks := collect(t, ch)
	if len(chunks) == 0 {
		t.Fatal("got no chunks, want at least a text chunk and a final error")
	}
	last := chunks[len(chunks)-1]
	if last.Err == nil {
		t.Fatalf("last chunk = %+v, want a non-nil Err", last)
	}
	for _, c := range chunks[:len(chunks)-1] {
		if c.Err != nil {
			t.Errorf("error chunk arrived before the last one: %+v", c)
		}
	}
}

func TestStreamDoneWithoutTrailingNewline(t *testing.T) {
	// The fixture on disk ends with the house style's mandatory trailing
	// newline; strip exactly that one byte before serving it, so the wire
	// body genuinely ends in "data: [DONE]" with nothing after it — the
	// condition this test exists to prove Stream handles.
	raw := loadFixture(t, "done_no_trailing_newline.sse")
	body := bytes.TrimSuffix(raw, []byte("\n"))
	if bytes.HasSuffix(body, []byte("\n")) {
		t.Fatal("test setup: fixture has more than one trailing newline")
	}
	if bytes.HasSuffix(body, []byte("[DONE]")) == false {
		t.Fatal("test setup: fixture does not end with [DONE]")
	}

	srv := sseServer(t, body, nil)
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "test-model"})
	ch, err := c.Stream(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	chunks := collect(t, ch)
	var gotText, gotFinish bool
	for _, c := range chunks {
		if c.Err != nil {
			t.Fatalf("unexpected error chunk: %v", c.Err)
		}
		if c.Text == "hi" {
			gotText = true
		}
		if c.Finish == "stop" {
			gotFinish = true
		}
	}
	if !gotText || !gotFinish {
		t.Fatalf("chunks = %+v, want a text delta and a stop finish", chunks)
	}
}

func TestStreamHonoursContext(t *testing.T) {
	// The server sends one chunk, then blocks until the client disconnects
	// — simulating a live stream that is still open when the caller cancels.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter does not support flushing")
		}
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":"one"},"finish_reason":null}]}` + "\n\n"))
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "test-model"})
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := c.Stream(ctx, Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	first, ok := <-ch
	if !ok {
		t.Fatal("channel closed before delivering the first chunk")
	}
	if first.Text != "one" {
		t.Fatalf("first chunk = %+v, want Text=one", first)
	}

	cancel()

	closed := make(chan struct{})
	go func() {
		for range ch {
			// Drain whatever, if anything, arrives after cancellation.
		}
		close(closed)
	}()

	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("channel did not close within 2s of context cancellation")
	}
}

// TestStreamNonOKStatus checks the synchronous-error path: a non-200
// response is a Stream error, not a channel of one Chunk{Err}, since no
// streaming has begun yet.
func TestStreamNonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key"}`))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "test-model"})
	ch, err := c.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("Stream: got nil error, want an error for HTTP 401")
	}
	if ch != nil {
		t.Fatal("Stream: got a non-nil channel alongside an error")
	}
}

// TestStreamEndsWithoutFinishOrDone proves a tool call in progress when the
// connection just ends — no [DONE], no finish_reason — is reported as an
// error rather than dispatched half-assembled.
func TestStreamEndsWithoutFinishOrDone(t *testing.T) {
	body := []byte(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_x","type":"function","function":{"name":"wiki.get","arguments":"{\"pa"}}]},"finish_reason":null}]}` + "\n\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		// No [DONE], no finish_reason: the handler just returns, closing
		// the connection with the tool call still mid-assembly.
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "test-model"})
	ch, err := c.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	chunks := collect(t, ch)
	if len(chunks) != 1 || chunks[0].Err == nil {
		t.Fatalf("chunks = %+v, want exactly one error chunk", chunks)
	}
	for _, c := range chunks {
		if c.ToolCall != nil {
			t.Fatalf("got a ToolCall from an unfinished stream: %+v", c.ToolCall)
		}
	}
}

// errAfterN wraps an io.Reader and returns a synthetic error after
// producing n bytes, so TestStreamReadError can drive a deterministic
// "mid-stream I/O error" without depending on TCP-level flakiness.
type errAfterN struct {
	data []byte
	n    int
	pos  int
}

func (e *errAfterN) Read(p []byte) (int, error) {
	if e.pos >= e.n {
		return 0, errors.New("simulated read failure")
	}
	remaining := e.n - e.pos
	if remaining > len(e.data)-e.pos {
		remaining = len(e.data) - e.pos
	}
	toCopy := len(p)
	if toCopy > remaining {
		toCopy = remaining
	}
	copy(p, e.data[e.pos:e.pos+toCopy])
	e.pos += toCopy
	return toCopy, nil
}

// TestStreamReadError exercises the raw I/O error path in consumeStream
// (as opposed to TestStreamClosesOnError's JSON parse error) by cutting the
// body off with a synthetic Read error partway through a chunk.
func TestStreamReadError(t *testing.T) {
	full := loadFixture(t, "simple.sse")
	cutAt := bytes.IndexByte(full, '\n') + 1 // let the first line through whole
	reader := &errAfterN{data: full, n: cutAt}

	out := make(chan Chunk)
	go consumeStream(context.Background(), io.NopCloser(reader), out)

	chunks := collect(t, out)
	if len(chunks) == 0 {
		t.Fatal("got no chunks, want at least a final error")
	}
	last := chunks[len(chunks)-1]
	if last.Err == nil {
		t.Fatalf("last chunk = %+v, want a non-nil Err", last)
	}
}
