package llm

// truncation_test.go pins 035 T1's stream-integrity contract: a provider
// stream that ends before [DONE] or a finish_reason is an error wrapping
// ErrStreamTruncated — never a silent clean stop — while the ends that have
// their own meaning (a stall, a cancelled context, an over-long line, a
// mid-stream parse failure on a still-talking provider) stay un-truncated.
// Every truncated exit also writes exactly one content-free "llm stream
// truncated" WARN record: counts only, never a payload byte. The bodies are
// replayed verbatim in the real GLM wire shape captured in 035's
// ground-truth round (glm-real-tool-round.sse), with the sentinel word
// ZEBRAPAYLOAD standing in for real payload text.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// glmLine renders one SSE line in the real GLM chunk shape — id/created/
// object/model fields around the given delta — as captured in 035's
// ground-truth round. No trailing newline: callers join lines with SSE's
// blank separator (glmBody) or cut it mid-line (TestTruncatedPartialLastLine).
func glmLine(delta string) string {
	return `data: {"id":"20260924170643c06bcfffa5d44648","created":1790240803,"object":"chat.completion.chunk","model":"glm-5.3-flash","choices":[{"index":0,"delta":{` + delta + `}}]}`
}

// glmFinish renders the finish chunk in the real GLM shape: finish_reason
// sits on the CHOICE, not in the delta, and the delta carries an empty
// role/content — exactly the ground-truth round's closing chunk.
func glmFinish(reason string) string {
	return `data: {"id":"20260924170643c06bcfffa5d44648","created":1790240803,"object":"chat.completion.chunk","model":"glm-5.3-flash","choices":[{"index":0,"finish_reason":"` + reason + `","delta":{"role":"assistant","content":""}}]}`
}

// glmBody joins full SSE lines the way a provider writes them: each line
// followed by the blank separator, so the body ends in "\n\n".
func glmBody(lines ...string) []byte {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString("\n\n")
	}
	return []byte(b.String())
}

// streamChunks runs body through Client.Stream against a verbatim-replay
// SSE server and returns every chunk the stream produced.
func streamChunks(t *testing.T, body []byte) []Chunk {
	t.Helper()
	srv := sseServer(t, body, nil)
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "test-model"})
	ch, err := c.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	return collect(t, ch)
}

// truncatedRecord returns the log's single "llm stream truncated" record,
// failing if there is none.
func truncatedRecord(t *testing.T, log string) string {
	t.Helper()
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, `msg="llm stream truncated"`) {
			return line
		}
	}
	t.Fatalf("no \"llm stream truncated\" record in log:\n%s", log)
	return ""
}

// TestTruncatedAfterText is R3's no-call shape: reasoning and text deltas
// arrived, then the connection ended with no finish_reason and no [DONE].
// The streamed deltas stay delivered (the consumer has already shown them)
// but the stream must end in one error wrapping ErrStreamTruncated, with
// the exact diagnostic text naming the byte count — the agent loop's retry
// decision and the log's triage value both hang on that shape.
func TestTruncatedAfterText(t *testing.T) {
	body := glmBody(
		glmLine(`"role":"assistant","reasoning_content":"The user"`),
		glmLine(`"role":"assistant","reasoning_content":" wants a claim checked"`),
		glmLine(`"role":"assistant","content":"ZEBRAPAYLOAD"`),
	)

	chunks := streamChunks(t, body)

	if len(chunks) != 4 {
		t.Fatalf("got %d chunks, want 2 reasoning + 1 text + 1 error: %+v", len(chunks), chunks)
	}
	if chunks[0].Reasoning == "" || chunks[1].Reasoning == "" {
		t.Errorf("chunks[0:2] = %+v, %+v, want the two reasoning fragments", chunks[0], chunks[1])
	}
	if chunks[2].Text != "ZEBRAPAYLOAD" {
		t.Errorf("chunks[2] = %+v, want the text delta", chunks[2])
	}
	err := chunks[3].Err
	if err == nil {
		t.Fatalf("chunks[3] = %+v, want the truncation error", chunks[3])
	}
	want := fmt.Sprintf("llm: provider stream ended early: no [DONE] or finish_reason after %d bytes", len(body))
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
	if !errors.Is(err, ErrStreamTruncated) {
		t.Errorf("err = %v, want errors.Is(err, ErrStreamTruncated)", err)
	}
}

// TestTruncatedWhileToolAssembling is R3's assembling shape: one WHOLE
// tool-call delta arrived (the real GLM shape — a whole call in one delta)
// but the finish_reason that completes it never did. The call must never be
// emitted — dispatching it would stage an op the cut round never finished —
// and the error must name the index still assembling.
func TestTruncatedWhileToolAssembling(t *testing.T) {
	body := glmBody(
		glmLine(`"role":"assistant","reasoning_content":"Searching the vault"`),
		glmLine(`"tool_calls":[{"id":"call_6a61d9153a764ee7a01bdcef","index":0,"type":"function","function":{"name":"wiki_search","arguments":"{\"q\":\"ZEBRAPAYLOAD\"}"}}]`),
	)

	chunks := streamChunks(t, body)

	for i, c := range chunks {
		if c.ToolCall != nil {
			t.Fatalf("chunk %d carries a ToolCall from an unfinished stream: %+v", i, c.ToolCall)
		}
	}
	if len(chunks) != 2 || chunks[0].Reasoning == "" {
		t.Fatalf("chunks = %+v, want the reasoning fragment then the error", chunks)
	}
	err := chunks[1].Err
	if err == nil {
		t.Fatalf("chunks[1] = %+v, want the truncation error", chunks[1])
	}
	want := fmt.Sprintf("llm: provider stream ended early: tool call 0 was still assembling after %d bytes", len(body))
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
	if !errors.Is(err, ErrStreamTruncated) {
		t.Errorf("err = %v, want errors.Is(err, ErrStreamTruncated)", err)
	}
}

// TestTruncatedAfterDispatchedCall pins the boundary between R3's two
// shapes: call index 0 is COMPLETE the moment index 1 starts (the existing
// assembly rule), so it is emitted exactly once and stays delivered; call
// index 1 never gets its finishing signal and is never emitted. The retry
// may re-run the round but the already-dispatched call is real output.
func TestTruncatedAfterDispatchedCall(t *testing.T) {
	body := glmBody(
		glmLine(`"tool_calls":[{"id":"call_a","index":0,"type":"function","function":{"name":"wiki_search","arguments":"{\"q\":\"ZEBRAPAYLOAD\"}"}}]`),
		glmLine(`"tool_calls":[{"id":"call_b","index":1,"type":"function","function":{"name":"wiki_get","arguments":"{}"}}]`),
	)

	chunks := streamChunks(t, body)

	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want the index-0 ToolCall then the error: %+v", len(chunks), chunks)
	}
	tc := chunks[0].ToolCall
	if tc == nil {
		t.Fatalf("chunks[0] = %+v, want the completed index-0 call", chunks[0])
	}
	if tc.ID != "call_a" || tc.Function.Name != "wiki_search" || tc.Function.Arguments != `{"q":"ZEBRAPAYLOAD"}` {
		t.Errorf("emitted call = %+v, want the whole index-0 call unchanged", tc)
	}
	if chunks[1].ToolCall != nil {
		t.Errorf("chunks[1] carries a ToolCall for the call that never finished: %+v", chunks[1])
	}
	err := chunks[1].Err
	if err == nil {
		t.Fatalf("chunks[1] = %+v, want the truncation error", chunks[1])
	}
	want := fmt.Sprintf("llm: provider stream ended early: tool call 1 was still assembling after %d bytes", len(body))
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
	if !errors.Is(err, ErrStreamTruncated) {
		t.Errorf("err = %v, want errors.Is(err, ErrStreamTruncated)", err)
	}
}

// TestTruncatedPartialLastLine is R4: the connection died mid-line, so the
// scanner's final token is a partial JSON chunk with no newline. The 60
// surviving bytes must be named in the error, next to the total read — that
// split is what tells a curator "the provider was cut talking", as opposed
// to a clean early stop.
func TestTruncatedPartialLastLine(t *testing.T) {
	full := glmLine(`"role":"assistant","content":"ZEBRAPAYLOAD"`)
	const cut = 60
	body := glmBody(glmLine(`"role":"assistant","content":"first"`))
	body = append(body, full[:cut]...) // second chunk line, no newline

	chunks := streamChunks(t, body)

	if len(chunks) != 2 || chunks[0].Text != "first" {
		t.Fatalf("chunks = %+v, want the first text delta then the error", chunks)
	}
	err := chunks[1].Err
	if err == nil {
		t.Fatalf("chunks[1] = %+v, want the truncation error", chunks[1])
	}
	want := fmt.Sprintf("llm: provider stream ended early: last line cut at %d bytes (%d bytes read)", cut, len(body))
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
	if !errors.Is(err, ErrStreamTruncated) {
		t.Errorf("err = %v, want errors.Is(err, ErrStreamTruncated)", err)
	}
}

// TestTruncatedBadFinalLineWithSeparator is the R4 shape the wire actually
// produces: SSE ends every line with a blank separator, so a garbage FINAL
// line arrives complete with its trailing "\n\n". The separator is an empty
// scanner token, not more body — the provider is done talking — so this
// must classify as the truncation it is, not as R5's live-provider parse
// error (which the agent loop would not retry). error_mid_stream.sse pins
// the same shape with a bare "\n"; this pins the real glmBody separator.
func TestTruncatedBadFinalLineWithSeparator(t *testing.T) {
	body := glmBody(glmLine(`"role":"assistant","content":"first"`))
	body = append(body, "data: {not json}\n\n"...)

	chunks := streamChunks(t, body)

	if len(chunks) != 2 || chunks[0].Text != "first" {
		t.Fatalf("chunks = %+v, want the first text delta then the error", chunks)
	}
	err := chunks[1].Err
	if err == nil {
		t.Fatalf("chunks[1] = %+v, want the truncation error", chunks[1])
	}
	want := fmt.Sprintf("llm: provider stream ended early: last line cut at %d bytes (%d bytes read)",
		len("data: {not json}"), len(body))
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
	if !errors.Is(err, ErrStreamTruncated) {
		t.Errorf("err = %v, want errors.Is(err, ErrStreamTruncated)", err)
	}
}

// TestMalformedMidStreamNotTruncated is R5: a bad line with more body
// behind it is a live provider talking nonsense, not a cut stream — the
// plain parse error it has always been, with no ErrStreamTruncated on it.
// Label that shape truncated and the agent loop would retry a round the
// provider is actively answering.
func TestMalformedMidStreamNotTruncated(t *testing.T) {
	body := glmBody(glmLine(`"role":"assistant","content":"before"`))
	body = append(body, "data: {not json}\n\n"...)
	body = append(body, glmBody(
		glmLine(`"role":"assistant","content":"after"`),
		glmFinish("stop"),
	)...)
	body = append(body, "data: [DONE]\n\n"...)

	chunks := streamChunks(t, body)

	last := chunks[len(chunks)-1]
	if last.Err == nil {
		t.Fatalf("last chunk = %+v, want the parse error", last)
	}
	if !strings.HasPrefix(last.Err.Error(), "llm: parse stream chunk:") {
		t.Errorf("err = %q, want the plain parse-stream-chunk error", last.Err)
	}
	if errors.Is(last.Err, ErrStreamTruncated) {
		t.Errorf("err = %v, want NO ErrStreamTruncated — the provider was still talking", last.Err)
	}
}

// TestFinishWithoutDoneIsClean is R2: a finish_reason seen and then the
// body just ends — no [DONE]. Fakes and some providers end the body right
// after the finish chunk, so this shape must stay clean or every one of
// them turns into a spurious retry.
func TestFinishWithoutDoneIsClean(t *testing.T) {
	body := glmBody(
		glmLine(`"role":"assistant","content":"answer"`),
		glmFinish("stop"),
	)

	chunks := streamChunks(t, body)

	for i, c := range chunks {
		if c.Err != nil {
			t.Fatalf("chunk %d carries an error; a seen finish_reason must end the stream clean: %v", i, c.Err)
		}
	}
	if len(chunks) != 2 || chunks[0].Text != "answer" || chunks[1].Finish != "stop" {
		t.Fatalf("chunks = %+v, want the text delta then the stop finish", chunks)
	}
}

// TestDoneWithoutFinishIsClean is R1: [DONE] ends the stream clean, as it
// always has, whatever else the body carried.
func TestDoneWithoutFinishIsClean(t *testing.T) {
	body := append(glmBody(glmLine(`"role":"assistant","content":"answer"`)), "data: [DONE]\n\n"...)

	chunks := streamChunks(t, body)

	for i, c := range chunks {
		if c.Err != nil {
			t.Fatalf("chunk %d carries an error; [DONE] must end the stream clean: %v", i, c.Err)
		}
	}
	if len(chunks) != 1 || chunks[0].Text != "answer" {
		t.Fatalf("chunks = %+v, want the text delta only", chunks)
	}
}

// causeReader substitutes a test-owned cause for errAfterN's per-Read
// synthetic error (a fresh value each call, unreachable by errors.Is), so
// TestReadFailureIsTruncated can prove the cause is WRAPPED — errors.Is
// through the chain — rather than merely named in the message text.
type causeReader struct {
	*errAfterN
	cause error
}

func (c *causeReader) Read(p []byte) (int, error) {
	n, err := c.errAfterN.Read(p)
	if err != nil {
		return n, c.cause
	}
	return n, err
}

// TestReadFailureIsTruncated is R6's truncated branch: a read error that is
// not a stall, not an over-long line and not the caller's cancellation is
// the stream dying — the error must carry BOTH ErrStreamTruncated and the
// read's own cause, so a consumer can match the truncation while the log
// keeps the underlying failure.
func TestReadFailureIsTruncated(t *testing.T) {
	full := loadFixture(t, "simple.sse")
	cutAt := bytes.IndexByte(full, '\n') + 1 // let the first line through whole
	cause := errors.New("simulated read failure")

	out := make(chan Chunk)
	go consumeStream(context.Background(), io.NopCloser(&causeReader{
		errAfterN: &errAfterN{data: full, n: cutAt},
		cause:     cause,
	}), out)

	chunks := collect(t, out)
	if len(chunks) == 0 {
		t.Fatal("got no chunks, want at least a final error")
	}
	err := chunks[len(chunks)-1].Err
	if err == nil {
		t.Fatalf("last chunk = %+v, want a non-nil Err", chunks[len(chunks)-1])
	}
	want := fmt.Sprintf("llm: provider stream ended early: read failed after %d bytes: simulated read failure", cutAt)
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
	if !errors.Is(err, ErrStreamTruncated) {
		t.Errorf("err = %v, want errors.Is(err, ErrStreamTruncated)", err)
	}
	if !errors.Is(err, cause) {
		t.Errorf("err = %v, want errors.Is(err, cause) — the read failure must be wrapped, not formatted", err)
	}
}

// TestReadFailureAfterFinishIsClean is amended R6 (A-035-1): a read error
// AFTER a seen finish_reason ends the stream CLEAN — the round is complete
// once its finish arrived (R2's premise), so a retry would discard a
// finished answer. No Err chunk is emitted; the lost tail is reported as
// exactly one content-free "llm stream tail lost" WARN record, and neither
// the already-delivered finish chunk nor its "llm finish" line changes.
func TestReadFailureAfterFinishIsClean(t *testing.T) {
	content := glmLine(`"role":"assistant","content":"ZEBRAPAYLOAD"`)
	finish := glmFinish("stop")
	body := glmBody(content, finish, "data: [DONE]")
	cutAt := len(content) + 2 + len(finish) + 2 // right after the finish line + blank separator: [DONE] never arrives
	logPath := installFileLog(t)

	out := make(chan Chunk)
	go consumeStreamTimed(context.Background(), io.NopCloser(&errAfterN{data: body, n: cutAt}), out, time.Now())

	chunks := collect(t, out)
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks (%+v), want the text delta then the stop finish — no error chunk", len(chunks), chunks)
	}
	if chunks[0].Text != "ZEBRAPAYLOAD" {
		t.Errorf("chunks[0] = %+v, want the text delta", chunks[0])
	}
	if chunks[1].Finish != "stop" {
		t.Errorf("chunks[1] = %+v, want the stop finish, delivered unchanged", chunks[1])
	}

	log := readLog(t, logPath)
	if !strings.Contains(log, `msg="llm finish"`) {
		t.Errorf("log missing the already-delivered finish line:\n%s", log)
	}
	if got := strings.Count(log, `msg="llm stream tail lost"`); got != 1 {
		t.Fatalf("log holds %d tail-lost records, want exactly one:\n%s", got, log)
	}
	if got := strings.Count(log, `msg="llm stream truncated"`); got != 0 {
		t.Errorf("log holds %d truncated records, want 0 — the round was complete:\n%s", got, log)
	}
	if got := strings.Count(log, `msg="llm error"`); got != 0 {
		t.Errorf("log holds %d \"llm error\" records, want 0:\n%s", got, log)
	}
	if strings.Contains(log, "ZEBRAPAYLOAD") {
		t.Errorf("log leaked a payload byte:\n%s", log)
	}

	var rec string
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, `msg="llm stream tail lost"`) {
			rec = line
			break
		}
	}
	for _, want := range []string{
		"bytes=" + strconv.Itoa(cutAt),
		`scan_err="simulated read failure"`,
	} {
		if !strings.Contains(rec, want) {
			t.Errorf("tail-lost record missing %q:\n%s", want, rec)
		}
	}
	// t0 is non-zero here, so elapsed_ms is a real measurement, never the
	// -1 no-anchor sentinel.
	i := strings.Index(rec, "elapsed_ms=")
	if i < 0 {
		t.Fatalf("tail-lost record missing elapsed_ms:\n%s", rec)
	}
	fields := strings.Fields(rec[i+len("elapsed_ms="):])
	if len(fields) != 1 {
		t.Fatalf("elapsed_ms value = %q, want one trailing number", fields)
	}
	if ms, err := strconv.Atoi(fields[0]); err != nil || ms < 0 {
		t.Errorf("elapsed_ms = %q, want a measurement >= 0", fields[0])
	}
}

// TestStallIsNotTruncated is R6's untouched branch: the provider going
// SILENT is 026 T2's stall, a bounded deliberate end with its own sentinel
// and its own retry policy — it must not be re-labelled as truncation.
func TestStallIsNotTruncated(t *testing.T) {
	// The TestStallBodyTimeout harness: headers and one chunk, then
	// silence until the stall expiry closes the connection.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":"one"},"finish_reason":null}]}` + "\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "test-model", StallTimeout: testStall})
	ch, err := c.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	chunks := collect(t, ch)
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks (%+v), want the text delta then the stall error", len(chunks), chunks)
	}
	e := chunks[1].Err
	if e == nil {
		t.Fatalf("chunks[1] = %+v, want the stall error", chunks[1])
	}
	if !errors.Is(e, ErrStalled) {
		t.Errorf("err = %v, want errors.Is(err, ErrStalled)", e)
	}
	if errors.Is(e, ErrStreamTruncated) {
		t.Errorf("err = %v, want NO ErrStreamTruncated — a stall is not a truncation", e)
	}
}

// TestTruncatedOverH2 runs R3's no-call shape over a real HTTP/2 response:
// truncation is a property of the byte stream's end, not of any one
// protocol's framing, and the agent talks to https providers over h2. The
// handler asserts the negotiation actually happened — a regression to h1
// would leave the test passing against the wrong transport.
func TestTruncatedOverH2(t *testing.T) {
	body := glmBody(
		glmLine(`"role":"assistant","reasoning_content":"The user"`),
		glmLine(`"role":"assistant","reasoning_content":" wants a claim checked"`),
		glmLine(`"role":"assistant","content":"ZEBRAPAYLOAD"`),
	)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Errorf("request ProtoMajor = %d, want 2 — this test only means something over h2", r.ProtoMajor)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		// No [DONE], no finish_reason: the handler returns, ending the h2
		// stream with the body cut.
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "test-model"})
	c.httpClient = srv.Client()

	ch, err := c.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	chunks := collect(t, ch)
	if len(chunks) != 4 {
		t.Fatalf("got %d chunks, want 2 reasoning + 1 text + 1 error: %+v", len(chunks), chunks)
	}
	err = chunks[3].Err
	if err == nil {
		t.Fatalf("chunks[3] = %+v, want the truncation error", chunks[3])
	}
	want := fmt.Sprintf("llm: provider stream ended early: no [DONE] or finish_reason after %d bytes", len(body))
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
	if !errors.Is(err, ErrStreamTruncated) {
		t.Errorf("err = %v, want errors.Is(err, ErrStreamTruncated)", err)
	}
}

// TestTruncationLogIsContentFree pins R7: every truncated exit replaces its
// old "llm error" line with exactly one "llm stream truncated" record whose
// attrs are the stream's shape in counts — bytes, data lines, parsed
// chunks, emitted calls, whether a call was mid-assembly, the partial
// line's size, the scan error's text, the elapsed time — and nothing else.
// The sentinel payload word must appear nowhere: the file log is read by
// hand during triage and the payloads are the user's answer.
func TestTruncationLogIsContentFree(t *testing.T) {
	cases := []struct {
		name       string
		body       []byte
		dataLines  int
		chunks     int
		partialBar int
	}{
		{
			// TestTruncatedAfterText's body: three whole lines, then EOF.
			name: "clean cut after text",
			body: glmBody(
				glmLine(`"role":"assistant","reasoning_content":"The user"`),
				glmLine(`"role":"assistant","reasoning_content":" wants a claim checked"`),
				glmLine(`"role":"assistant","content":"ZEBRAPAYLOAD"`),
			),
			dataLines:  3,
			chunks:     3,
			partialBar: 0,
		},
		{
			// TestTruncatedPartialLastLine's body: one parsed line plus the
			// 60-byte cut line, which counts as a data line though it never
			// parsed.
			name:       "cut mid last line",
			body:       append(glmBody(glmLine(`"role":"assistant","content":"first"`)), glmLine(`"role":"assistant","content":"ZEBRAPAYLOAD"`)[:60]...),
			dataLines:  2,
			chunks:     1,
			partialBar: 60,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logPath := installFileLog(t)

			chunks := streamChunks(t, tc.body)
			if len(chunks) == 0 || chunks[len(chunks)-1].Err == nil {
				t.Fatalf("chunks = %+v, want the stream to end in the truncation error", chunks)
			}

			log := readLog(t, logPath)
			if got := strings.Count(log, `msg="llm stream truncated"`); got != 1 {
				t.Fatalf("log holds %d truncated records, want exactly one:\n%s", got, log)
			}
			if got := strings.Count(log, `msg="llm error"`); got != 0 {
				t.Errorf("log holds %d \"llm error\" records, want 0 — the truncated record replaces it:\n%s", got, log)
			}
			if strings.Contains(log, "ZEBRAPAYLOAD") {
				t.Errorf("log leaked a payload byte:\n%s", log)
			}

			rec := truncatedRecord(t, log)
			for _, want := range []string{
				"bytes=" + strconv.Itoa(len(tc.body)),
				"data_lines=" + strconv.Itoa(tc.dataLines),
				"chunks=" + strconv.Itoa(tc.chunks),
				"tool_calls=0",
				"assembling=false",
				"partial_line_bytes=" + strconv.Itoa(tc.partialBar),
				`scan_err="" elapsed_ms=`, // empty scan_err text, present elapsed
			} {
				if !strings.Contains(rec, want) {
					t.Errorf("truncated record missing %q:\n%s", want, rec)
				}
			}
			// Stream anchors t0 at the response headers, so the elapsed time
			// is a real measurement — never the -1 no-anchor sentinel.
			i := strings.Index(rec, "elapsed_ms=")
			if i < 0 {
				t.Fatalf("truncated record missing elapsed_ms:\n%s", rec)
			}
			fields := strings.Fields(rec[i+len("elapsed_ms="):])
			if len(fields) != 1 {
				t.Fatalf("elapsed_ms value = %q, want one trailing number", fields)
			}
			ms, err := strconv.Atoi(fields[0])
			if err != nil || ms < 0 {
				t.Errorf("elapsed_ms = %q, want a measurement >= 0", fields[0])
			}
		})
	}
}
