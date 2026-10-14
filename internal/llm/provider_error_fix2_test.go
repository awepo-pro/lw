package llm

// provider_error_fix2_test.go pins 061 FIX2's llm half, from the branch review:
//
//   R2  a transport failure before any response (dial, DNS, reset, TLS, EOF) is
//       a retryable *ProviderError, its Error() text the plain `llm: request:
//       <err>` it always was — it lost a turn's staged work when it was a bare
//       fmt.Errorf; a cancel, a deadline and a stall keep their own meaning.
//   R5  an error event AFTER the round's finish_reason is a lost tail on a
//       complete round, not a failure that discards the finished answer.
//   R7  provider strings are untrusted terminal input: the code is bounded and
//       neither it nor the message carries a control character.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode"
)

// refusedBaseURL is a base URL whose port is closed: a connect there is refused.
func refusedBaseURL(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return "http://" + addr
}

func streamOnce(c *Client, ctx context.Context) error {
	_, err := c.Stream(ctx, Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	return err
}

// TestStreamTransportErrorIsProviderError: connection refused is a typed,
// retryable ProviderError with the Transport mark — and its text and its error
// chain are exactly the plain `llm: request: %w` they were.
func TestStreamTransportErrorIsProviderError(t *testing.T) {
	base := refusedBaseURL(t)
	c := New(Config{BaseURL: base, Model: "test-model"})

	err := streamOnce(c, context.Background())
	pe := asProviderError(t, err)
	if !pe.Transport || pe.Status != 0 || pe.InStream {
		t.Errorf("ProviderError = %+v, want Transport, Status 0, not InStream", *pe)
	}
	if !pe.Retryable() {
		t.Error("Retryable() = false, want true for a transport failure")
	}
	if !strings.Contains(pe.Message, "connection refused") {
		t.Errorf("Message = %q, want the transport error's text", pe.Message)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Errorf("errors.Is(err, ECONNREFUSED) = false for %v: the cause chain was lost", err)
	}

	// Byte-identical to the text before the type: "llm: request: " + what the
	// plain client says for the same request.
	req, _ := http.NewRequest(http.MethodPost, base+"/chat/completions", strings.NewReader("{}"))
	_, plain := (&http.Client{}).Do(req)
	if plain == nil {
		t.Fatal("the reference request unexpectedly succeeded")
	}
	if want := "llm: request: " + plain.Error(); err.Error() != want {
		t.Errorf("Error() =\n%q\nwant the byte-identical\n%q", err.Error(), want)
	}
	if got := pe.Short(); got != "provider network" {
		t.Errorf("Short() = %q, want %q", got, "provider network")
	}
}

// TestStreamTransportErrorEOF: a server that takes the request and hangs up
// without a response is the same kind of failure (EOF before headers).
func TestStreamTransportErrorEOF(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	defer srv.Close()

	pe := asProviderError(t, streamOnce(New(Config{BaseURL: srv.URL, Model: "m"}), context.Background()))
	if !pe.Transport || !pe.Retryable() {
		t.Errorf("ProviderError = %+v, want a retryable Transport error", *pe)
	}
}

// TestStreamTransportErrorKeepsOtherMeanings: the caller's own cancel and
// deadline, and a stall, are NOT provider failures — they stay the plain wrapped
// errors every consumer already matches with errors.Is.
func TestStreamTransportErrorKeepsOtherMeanings(t *testing.T) {
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := streamOnce(New(Config{BaseURL: refusedBaseURL(t), Model: "m"}), ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled in the chain", err)
		}
		var pe *ProviderError
		if errors.As(err, &pe) {
			t.Errorf("a canceled request became a ProviderError: %+v", *pe)
		}
	})

	t.Run("deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
		defer cancel()
		time.Sleep(time.Millisecond)
		err := streamOnce(New(Config{BaseURL: refusedBaseURL(t), Model: "m"}), ctx)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded in the chain", err)
		}
		var pe *ProviderError
		if errors.As(err, &pe) {
			t.Errorf("an expired deadline became a ProviderError: %+v", *pe)
		}
	})

	t.Run("stalled", func(t *testing.T) {
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}))
		defer srv.Close()
		defer close(release)

		err := streamOnce(New(Config{BaseURL: srv.URL, Model: "m", StallTimeout: 60 * time.Millisecond}), context.Background())
		if !errors.Is(err, ErrStalled) {
			t.Fatalf("err = %v, want ErrStalled in the chain", err)
		}
		var pe *ProviderError
		if errors.As(err, &pe) {
			t.Errorf("a stall became a ProviderError (it would be retried): %+v", *pe)
		}
	})
}

// TestStreamErrorAfterFinishIsTailLost: the round's finish_reason has come, so
// the answer is complete; an error event behind it (a late upstream 502, a
// trailing gateway message) is logged as a lost tail and changes nothing — no
// Err chunk to retry the whole round on and discard a finished answer. The
// held-back usage still lands.
func TestStreamErrorAfterFinishIsTailLost(t *testing.T) {
	logPath := installFileLog(t)

	chunks := streamChunks(t, sseLines(
		`{"choices":[{"index":0,"delta":{"content":"hi"}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
		`{"error":{"code":502,"message":"late upstream hiccup"}}`,
		"[DONE]",
	))
	if len(chunks) != 3 || chunks[0].Text != "hi" || chunks[1].Finish != "stop" || chunks[2].Usage == nil {
		t.Fatalf("chunks = %+v, want the text, the stop finish and the usage — and no error", chunks)
	}
	for _, c := range chunks {
		if c.Err != nil {
			t.Fatalf("an error event after the finish became an Err chunk: %v", c.Err)
		}
	}

	log := readLog(t, logPath)
	if got := strings.Count(log, `msg="llm stream tail lost"`); got != 1 {
		t.Errorf("log holds %d tail-lost records, want exactly 1:\n%s", got, log)
	}
	if !strings.Contains(log, "late upstream hiccup") {
		t.Errorf("the tail-lost record does not carry the provider's message:\n%s", log)
	}
	if got := strings.Count(log, `msg="llm provider error"`); got != 0 {
		t.Errorf("log holds %d provider-error records, want 0:\n%s", got, log)
	}
}

// TestStreamErrorBeforeFinishStillFails: the same event BEFORE the finish is
// the failure it always was (the pair that makes the test above mean something).
func TestStreamErrorBeforeFinishStillFails(t *testing.T) {
	chunks := streamChunks(t, sseLines(
		`{"choices":[{"index":0,"delta":{"content":"hi"}}]}`,
		`{"error":{"code":502,"message":"early"}}`,
	))
	if len(chunks) != 2 || chunks[1].Err == nil {
		t.Fatalf("chunks = %+v, want the text then the provider error", chunks)
	}
}

// noControls reports whether s holds no control character.
func noControls(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// TestProviderStringsSanitized: a provider's code and message reach a terminal
// (the retry line, the partial mark, the log); a control character in either
// must not — ESC and the rest are dropped, line breaks become spaces, and the
// code is bounded to 64 runes.
func TestProviderStringsSanitized(t *testing.T) {
	const evil = "\x1b[31mX\ny"
	const want = "[31mX y"

	t.Run("in stream", func(t *testing.T) {
		line := `{"error":{"code":"bad\u001b[0m\ncode","message":"\u001b[31mX\ny"}}`
		chunks := streamChunks(t, sseLines(line))
		if len(chunks) != 1 || chunks[0].Err == nil {
			t.Fatalf("chunks = %+v, want one error chunk", chunks)
		}
		pe := asProviderError(t, chunks[0].Err)
		if pe.Message != want {
			t.Errorf("Message = %q, want %q", pe.Message, want)
		}
		if pe.Code != "bad[0m code" {
			t.Errorf("Code = %q, want %q", pe.Code, "bad[0m code")
		}
		if !noControls(pe.Message) || !noControls(pe.Code) || !noControls(pe.Error()) || !noControls(pe.Short()) {
			t.Errorf("control characters survived: %+v / %q / %q", *pe, pe.Error(), pe.Short())
		}
	})

	t.Run("code is bounded to 64 runes", func(t *testing.T) {
		line := `{"error":{"code":"` + strings.Repeat("é", 200) + `","message":"m"}}`
		chunks := streamChunks(t, sseLines(line))
		pe := asProviderError(t, chunks[0].Err)
		if got := []rune(pe.Code); len(got) != 64 || got[63] != '…' {
			t.Errorf("Code is %d runes (%.20q…), want 64 ending in …", len(got), pe.Code)
		}
	})

	t.Run("http message", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"message":"\u001b[31mX\ny"}}`))
		}))
		defer srv.Close()
		pe := asProviderError(t, streamOnce(New(Config{BaseURL: srv.URL, Model: "m"}), context.Background()))
		if pe.Message != want {
			t.Errorf("Message = %q, want %q", pe.Message, want)
		}
	})

	t.Run("http body that is not json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(evil))
		}))
		defer srv.Close()
		err := streamOnce(New(Config{BaseURL: srv.URL, Model: "m"}), context.Background())
		pe := asProviderError(t, err)
		if pe.Message != want {
			t.Errorf("Message = %q, want %q", pe.Message, want)
		}
		// Error() is the byte-identical Body, which stays as the provider sent it.
		if pe.Body != evil || err.Error() != "llm: unexpected status 502: "+evil {
			t.Errorf("Body/Error() = %q / %q, want the raw body untouched", pe.Body, err.Error())
		}
	})
}
