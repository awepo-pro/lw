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
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
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

// headerTimeoutErr builds the observable shape of a ResponseHeaderTimeout
// failure as the transport delivers it to do(): a *url.Error whose Err is a
// net.Error with Timeout() true and the transport's documented message. The
// message differs per protocol — "net/http: …" on HTTP/1.1, "http2: …" on a
// TLS connection that negotiated h2 (both verified against Go 1.27's
// net/http and its bundled http2) — and asStalled must relabel BOTH; the
// frozen header-stall e2e test above can only ever produce the h1 one.
type headerTimeoutErr struct{ msg string }

func (e headerTimeoutErr) Error() string { return e.msg }
func (e headerTimeoutErr) Timeout() bool { return true }
func (e headerTimeoutErr) Temporary() bool {
	return true
}

func asStalledInput(msg string) error {
	return &url.Error{
		Op:  "Post",
		URL: "https://api.example.com/v1/chat/completions",
		Err: headerTimeoutErr{msg: msg},
	}
}

func TestAsStalledClassification(t *testing.T) {
	stallCases := []struct {
		name string
		err  error
	}{
		{"h1 header timeout", asStalledInput("net/http: timeout awaiting response headers")},
		{"h2 header timeout", asStalledInput("http2: timeout awaiting response headers")},
	}
	for _, tc := range stallCases {
		t.Run(tc.name, func(t *testing.T) {
			c := New(Config{StallTimeout: testStall})
			got := c.asStalled(tc.err)
			if !errors.Is(got, ErrStalled) {
				t.Fatalf("asStalled(%v) = %v, want errors.Is(_, ErrStalled)", tc.err, got)
			}
			if !wantStallMsg(got) {
				t.Errorf("err = %v, want the message to carry %q", got, "no response bytes for "+testStall.String())
			}
		})
	}

	// Timeouts that are NOT the provider going silent after connect: dial
	// and TLS handshake failures, and the caller's own cancel/deadline.
	// asStalled must hand every one of them back unchanged — a mislabelled
	// retryable/connect error would report ErrStalled for a turn that
	// never reached the provider.
	unstalled := []struct {
		name string
		err  error
	}{
		{"dial timeout", &url.Error{Op: "Post", URL: "https://api.example.com/v1/chat/completions",
			Err: headerTimeoutErr{msg: "dial tcp 1.2.3.4:443: i/o timeout"}}},
		{"tls handshake timeout", &url.Error{Op: "Post", URL: "https://api.example.com/v1/chat/completions",
			Err: headerTimeoutErr{msg: "net/http: TLS handshake timeout"}}},
		{"caller cancel", &url.Error{Op: "Post", URL: "https://api.example.com/v1/chat/completions",
			Err: headerTimeoutErr{msg: "context canceled"}}},
	}
	c := New(Config{StallTimeout: testStall})
	for _, tc := range unstalled {
		t.Run(tc.name, func(t *testing.T) {
			got := c.asStalled(tc.err)
			if errors.Is(got, ErrStalled) {
				t.Fatalf("asStalled(%v) = %v, want the original error unchanged", tc.err, got)
			}
			if got.Error() != tc.err.Error() {
				t.Errorf("asStalled(%v) = %v, want the error returned untouched", tc.err, got)
			}
		})
	}

	// StallTimeout 0 never relabels anything, whatever the text says.
	c0 := New(Config{StallTimeout: 0})
	got := c0.asStalled(asStalledInput("net/http: timeout awaiting response headers"))
	if errors.Is(got, ErrStalled) {
		t.Errorf("asStalled with StallTimeout 0 = %v, want the error unchanged", got)
	}
}

func TestStallHeaderTimeoutRetries(t *testing.T) {
	// F.S2 correction log 3: the transport's single retry survives the stall
	// machinery — a FIRST attempt whose headers outrun the bound is the
	// same transient transport failure do() has always replayed, so the
	// second attempt must be made and must succeed. The bound is two
	// windows, not one; only a provider silent across BOTH reports
	// ErrStalled (TestStallHeaderTimeout).
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if atomic.AddInt32(&attempts, 1) == 1 {
			<-r.Context().Done() // first attempt: headers never come
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"second try\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "test-model", StallTimeout: testStall})
	start := time.Now()
	ch, err := c.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v — a header stall on the FIRST attempt must be retried, not reported", err)
	}
	var text strings.Builder
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("chunk: %v", chunk.Err)
		}
		text.WriteString(chunk.Text)
	}
	if text.String() != "second try" {
		t.Errorf("streamed text = %q, want the retry attempt's answer", text.String())
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Errorf("server saw %d attempts, want 2 (the stall window, then the retry)", got)
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Errorf("the retried turn took %s, want ~%s (one stall window + the retry)", elapsed, testStall)
	}
}

func TestStallErrorStatusBodyStall(t *testing.T) {
	// An error-status response whose BODY stalls: the status line and
	// headers arrive, then the provider goes silent with the error body
	// still open. The read of that body is byte-bounded by LimitReader —
	// which bounds bytes, never time — so without the stall wrapper the
	// turn parks here forever, exactly the silence F.S1 forbids. The same
	// stall machinery the 200 path uses must end it with ErrStalled.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusInternalServerError)
		w.(http.Flusher).Flush()
		<-r.Context().Done() // silent forever after the headers
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "test-model", StallTimeout: testStall})
	start := time.Now()
	_, err := c.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Stream: got nil error, want ErrStalled after the error body's silence exceeded the stall bound")
	}
	if !errors.Is(err, ErrStalled) {
		t.Errorf("err = %v, want errors.Is(err, ErrStalled)", err)
	}
	if !wantStallMsg(err) {
		t.Errorf("err = %v, want the message to carry %q", err, "no response bytes for "+testStall.String())
	}
	if elapsed >= time.Second {
		t.Errorf("error-body stall surfaced after %s, want < 1s", elapsed)
	}
}

func TestStallErrorStatusBodyPreserved(t *testing.T) {
	// A non-200 response carries the provider's error text, and the turn's
	// error must keep it: the request cancel that T2 arms must not fire
	// before that 4KB error body has been read. The body arrives in two
	// flushes with a gap, so a cancel fired before the read has a real
	// window to close the connection mid-read — the fast-body shape wins
	// that race locally every time and would let the broken order hide.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, "rate limit ")
		w.(http.Flusher).Flush()
		time.Sleep(50 * time.Millisecond)
		fmt.Fprint(w, "exceeded for model test-model")
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "test-model", StallTimeout: testStall})
	_, err := c.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("Stream: got nil error, want the 429 surfaced")
	}
	if !strings.Contains(err.Error(), "rate limit exceeded for model test-model") {
		t.Errorf("err = %v, want it to carry the response body's error text", err)
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("err = %v, want the status code in the message", err)
	}
}

func TestStallStreamReuseAcrossTurns(t *testing.T) {
	// Two full turns on one client must share ONE TCP connection: T2's
	// wrapper and its cancel must not cost the 025 idle-pool reuse that the
	// cold-start work exists for. The count is of connections the server
	// accepted, not requests it served.
	var conns int32
	// Unstarted, so ConnState is installed before the server's Serve loop
	// ever reads the Config field — assigning it post-Start is a data race.
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	srv.Config.ConnState = func(c net.Conn, s http.ConnState) {
		if s == http.StateNew {
			atomic.AddInt32(&conns, 1)
		}
	}
	srv.Start()
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "test-model", StallTimeout: testStall})
	for turn := 0; turn < 2; turn++ {
		ch, err := c.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
		if err != nil {
			t.Fatalf("turn %d: Stream: %v", turn, err)
		}
		for chunk := range ch {
			if chunk.Err != nil {
				t.Fatalf("turn %d: %v", turn, chunk.Err)
			}
		}
	}
	if got := atomic.LoadInt32(&conns); got != 1 {
		t.Errorf("server accepted %d connections for 2 turns, want 1 (keep-alive reuse)", got)
	}
}
