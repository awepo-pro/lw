package llm

// first_delta_test.go pins the 025-T4 headers→first-visible-delta timing
// against the fake SSE servers: the "llm finish" record must carry a
// first_delta_ms field that is >= 0 on any stream that showed a reasoning
// or content delta, must grow when the provider stalls between response
// headers and the first chunk, must NOT include time spent before headers
// arrived, and must read the -1 sentinel when a stream finished with no
// visible delta at all. The logger under test is exactly what
// logging.Init installed as slog.Default (installFileLog, logging_test.go),
// restored at cleanup so sibling tests never see it.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// preDeltaGap is the deliberate stall stagedSSE puts between response
// headers and the first content chunk — long enough to dwarf scheduling
// noise, short enough to keep the suite fast.
const preDeltaGap = 150 * time.Millisecond

// stagedSSE serves headers immediately (WriteHeader + Flush, so the client
// really has parsed them), stalls gap, then replays a content delta, the
// finish chunk and [DONE]. A gap > 0 therefore lands strictly inside the
// headers→first-delta window first_delta_ms measures.
func stagedSSE(t *testing.T, gap time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter does not support flushing")
		}
		flusher.Flush()
		if gap > 0 {
			time.Sleep(gap)
		}
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"d\"},\"finish_reason\":null}]}\n\n")
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
}

// firstDeltaRe pulls first_delta_ms out of the finish record; -1 and any
// measured non-negative value are the only shapes production writes.
var firstDeltaRe = regexp.MustCompile(`msg="llm finish".*first_delta_ms=(-?\d+)`)

// finishDeltaFromLog returns the first_delta_ms value of the log's finish
// record, failing if the record or the field is missing.
func finishDeltaFromLog(t *testing.T, log string) int64 {
	t.Helper()
	m := firstDeltaRe.FindStringSubmatch(log)
	if m == nil {
		t.Fatalf("no \"llm finish\" record carrying first_delta_ms in log:\n%s", log)
	}
	v, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		t.Fatalf("parse first_delta_ms %q: %v", m[1], err)
	}
	return v
}

// streamFinishDelta runs one Stream against srv and returns the
// first_delta_ms from the finish record, failing if the stream did not
// finish with stop.
func streamFinishDelta(t *testing.T, srv *httptest.Server) int64 {
	t.Helper()
	logPath := installFileLog(t)
	c := New(Config{BaseURL: srv.URL, Model: "test-model", Timeout: 5 * time.Second})
	ch, err := c.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	chunks := collect(t, ch)
	if len(chunks) == 0 || chunks[len(chunks)-1].Finish != "stop" {
		t.Fatalf("scripted stream did not finish with stop: %#v", chunks)
	}
	return finishDeltaFromLog(t, readLog(t, logPath))
}

// TestFinishLineCarriesFirstDelta: any Stream that showed a visible delta
// logs a first_delta_ms >= 0 on the finish line.
func TestFinishLineCarriesFirstDelta(t *testing.T) {
	srv := stagedSSE(t, 0)
	defer srv.Close()

	if d := streamFinishDelta(t, srv); d < 0 {
		t.Fatalf("first_delta_ms = %d, want >= 0 on a stream with a content delta", d)
	}
}

// TestFirstDeltaGrowsWithPreDeltaGap: the value is measured from response
// headers to the first visible delta, so stalling the provider between
// those two points yields a strictly larger value than an immediate first
// chunk — and at least the staged gap itself.
func TestFirstDeltaGrowsWithPreDeltaGap(t *testing.T) {
	plainSrv := stagedSSE(t, 0)
	defer plainSrv.Close()
	plain := streamFinishDelta(t, plainSrv)

	gappedSrv := stagedSSE(t, preDeltaGap)
	defer gappedSrv.Close()
	gapped := streamFinishDelta(t, gappedSrv)

	if gapped < preDeltaGap.Milliseconds() {
		t.Errorf("gapped first_delta_ms = %d, want >= %d (the staged pre-delta gap)", gapped, preDeltaGap.Milliseconds())
	}
	if gapped <= plain {
		t.Errorf("gapped first_delta_ms = %d, want > plain %d", gapped, plain)
	}
}

// TestFirstDeltaExcludesTimeBeforeHeaders pins the anchor: a stall BEFORE
// the response headers are sent is part of the request→headers wait (the
// "llm response" line's territory), not of headers→first delta, so the
// finish line must still report a small value — not the pre-header stall.
func TestFirstDeltaExcludesTimeBeforeHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(preDeltaGap) // stall before any header byte is written
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter does not support flushing")
		}
		flusher.Flush()
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"d\"},\"finish_reason\":null}]}\n\n")
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	d := streamFinishDelta(t, srv)
	if d >= preDeltaGap.Milliseconds() {
		t.Fatalf("first_delta_ms = %d, want far below %d — the pre-header stall must not count", d, preDeltaGap.Milliseconds())
	}
}

// TestFinishLineWithoutDeltaUsesSentinel: a stream that reaches
// finish_reason without ever emitting a chunk the UI can render — no
// reasoning, no content, no completed tool call, e.g. a finish-only
// degenerate — pins the -1 sentinel, so the field stays present on every
// finish line and log analysis never branches on presence. A
// tool-call-only turn is NOT this case: its completed call renders on
// arrival (TestToolCallOnlyStreamAnchorsFirstDelta).
func TestFinishLineWithoutDeltaUsesSentinel(t *testing.T) {
	logPath := installFileLog(t)
	body := []byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n")
	srv := sseServer(t, body, nil)
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "test-model"})
	ch, err := c.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	chunks := collect(t, ch)
	if len(chunks) == 0 || chunks[len(chunks)-1].Finish != "stop" {
		t.Fatalf("scripted stream did not finish with stop: %#v", chunks)
	}

	if d := finishDeltaFromLog(t, readLog(t, logPath)); d != -1 {
		t.Fatalf("first_delta_ms = %d, want the -1 no-visible-delta sentinel", d)
	}
}

// TestToolCallOnlyStreamAnchorsFirstDelta pins the 025-T4/T2 agreement on
// the path where the two definitions used to diverge: a stream whose only
// renderable output is a completed tool call (a cold first ask that opens
// with vault.orient, say). The ask pane ends its waiting state at the
// ToolCallEv — the transcript renders the call the moment it is emitted —
// so the finish line must anchor first_delta_ms to that completed call
// and NOT report the -1 sentinel: on tool-first rounds -1 would excise
// exactly the rounds the cold-start numbers exist to measure. The staged
// gap (150ms of silence between the header flush and the tool-call
// chunks) sits strictly inside the measured window, so the value must
// clear it minus slop for the headers-arrival capture (100ms floor).
func TestToolCallOnlyStreamAnchorsFirstDelta(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		flusher.Flush()
		time.Sleep(preDeltaGap)
		// The wire shape of split_tool_call.sse: silent argument
		// fragments, the completed call only assembling at finish_reason.
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"vault.orient\",\"arguments\":\"\"}}]},\"finish_reason\":null}]}\n\n")
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{}\"}}]},\"finish_reason\":null}]}\n\n")
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	logPath := installFileLog(t)
	c := New(Config{BaseURL: srv.URL, Model: "test-model", Timeout: 5 * time.Second})
	ch, err := c.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	chunks := collect(t, ch)
	if len(chunks) == 0 || chunks[len(chunks)-1].Finish != "tool_calls" {
		t.Fatalf("scripted stream did not finish with tool_calls: %#v", chunks)
	}

	if d := finishDeltaFromLog(t, readLog(t, logPath)); d < preDeltaGap.Milliseconds()-50 {
		t.Fatalf("first_delta_ms = %d, want ~%d — a completed tool call must anchor the metric, not the -1 sentinel", d, preDeltaGap.Milliseconds())
	}
}
