package llm

// stall_test.go pins 026 T2's silence bound (frozen F.S4): a provider that
// stops sending bytes — before the response headers, or between body reads
// — must end the turn with ErrStalled within one StallTimeout, while a
// stream that keeps sending bytes is never cut, however long it runs.
// StallTimeout is 100ms throughout, so every stall case lands well under
// the frozen 1s wall-time bound.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testStall = 100 * time.Millisecond

// wantStallMsg is the frozen message shape both stall exits must carry: the
// sentinel text, then "no response bytes for <StallTimeout>" in Go's
// Duration.String form.
func wantStallMsg(err error) bool {
	return strings.Contains(err.Error(), "no response bytes for "+testStall.String())
}

func TestStallHeaderTimeout(t *testing.T) {
	// The provider accepts the connection but writes no response headers
	// until long after the stall bound has passed.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain the request body: net/http only starts watching for client
		// close once the body hits EOF, and without that the server never
		// notices the abort and srv.Close() waits out the full sleep.
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
			// The client gave up; return now so srv.Close() never waits
			// out the 2s a hung handler would cost the test.
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "test-model", StallTimeout: testStall})
	start := time.Now()
	_, err := c.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Stream: got nil error, want ErrStalled after the header wait exceeded the stall bound")
	}
	if !errors.Is(err, ErrStalled) {
		t.Errorf("err = %v, want errors.Is(err, ErrStalled)", err)
	}
	if !wantStallMsg(err) {
		t.Errorf("err = %v, want the message to carry %q", err, "no response bytes for "+testStall.String())
	}
	// The existing single transport retry still applies (026 T2 correction
	// log 3), so the bound is two windows — 200ms here — plus dial slack.
	if elapsed >= time.Second {
		t.Errorf("header stall surfaced after %s, want < 1s (two attempts × %s)", elapsed, testStall)
	}
}

func TestStallBodyTimeout(t *testing.T) {
	// Headers and one valid content chunk arrive, then the provider goes
	// silent with the stream still open.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // arm the server's client-close watch
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":"one"},"finish_reason":null}]}` + "\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done() // silent until the stall expiry closes the connection
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "test-model", StallTimeout: testStall})
	start := time.Now()
	ch, err := c.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var chunks []Chunk
	for chunk := range ch {
		chunks = append(chunks, chunk)
	}
	elapsed := time.Since(start)

	if len(chunks) != 2 {
		t.Fatalf("got %d chunks (%+v), want exactly the text delta then the stall error", len(chunks), chunks)
	}
	if chunks[0].Text != "one" {
		t.Errorf("chunks[0] = %+v, want the delivered Text delta first", chunks[0])
	}
	e := chunks[1].Err
	if e == nil {
		t.Fatalf("chunks[1] = %+v, want a stall error", chunks[1])
	}
	if !errors.Is(e, ErrStalled) {
		t.Errorf("err = %v, want errors.Is(err, ErrStalled)", e)
	}
	if !wantStallMsg(e) {
		t.Errorf("err = %v, want the message to carry %q", e, "no response bytes for "+testStall.String())
	}
	if elapsed >= time.Second {
		t.Errorf("body stall surfaced after %s, want < 1s", elapsed)
	}
}

func TestStallSlowStreamSurvives(t *testing.T) {
	// A chunk every 50ms — half the stall bound — for 600ms, then a normal
	// finish: the silence bound must never fire on a stream that keeps
	// sending bytes.
	const wantChunks = 12
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		for i := 0; i < wantChunks; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"c%d\"},\"finish_reason\":null}]}\n\n", i)
			flusher.Flush()
			select {
			case <-time.After(50 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "test-model", StallTimeout: testStall})
	start := time.Now()
	ch, err := c.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	got := collect(t, ch)
	elapsed := time.Since(start)

	var text strings.Builder
	for i, c := range got {
		if c.Err != nil {
			t.Fatalf("chunk %d carries an error; a slow-but-alive stream must never be cut: %v", i, c.Err)
		}
		text.WriteString(c.Text)
	}
	var want strings.Builder
	for i := 0; i < wantChunks; i++ {
		fmt.Fprintf(&want, "c%d", i)
	}
	if text.String() != want.String() {
		t.Errorf("streamed text = %q, want %q (every chunk, in order)", text.String(), want.String())
	}
	if last := got[len(got)-1]; last.Finish != "stop" {
		t.Errorf("last chunk = %+v, want Finish=stop", last)
	}
	if elapsed >= 2*time.Second {
		t.Errorf("%d chunks at a 50ms cadence took %s; want ~600ms", wantChunks, elapsed)
	}
}

func TestStallZeroIsUnbounded(t *testing.T) {
	c := New(Config{BaseURL: "http://example.com", Model: "test-model", StallTimeout: 0})

	tr, ok := c.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", c.httpClient.Transport)
	}
	if tr.ResponseHeaderTimeout != 0 {
		t.Errorf("ResponseHeaderTimeout = %s, want 0 — StallTimeout 0 leaves the header wait unbounded", tr.ResponseHeaderTimeout)
	}

	// And the body never gains a stall timer: wrapStallBody with 0 is the
	// identity — the exact predicate Stream applies at its single wrap site.
	rc := io.NopCloser(strings.NewReader("data: x\n\n"))
	if got := wrapStallBody(rc, 0, func() {}); got != rc {
		t.Errorf("wrapStallBody(rc, 0, cancel) = %T, want the body itself, unwrapped", got)
	}
}
