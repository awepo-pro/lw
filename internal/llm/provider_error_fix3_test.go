package llm

// provider_error_fix3_test.go pins 061 FIX3's llm half, from the focused review
// of FIX2:
//
//   F1  a failure the network will not cure is not retried. A scheme-less
//       base_url (`api.example.com/v1`) was typed as a transport failure and
//       waited out for 2+4+8 s (0.04 s -> 14.06 s), once per page under
//       `lw lint --fix`. A TLS certificate the machine does not trust, a host
//       name that does not match it, a server that does not speak TLS: the same.
//   F2  a dial that times out IS the network failing — its error matches
//       context.DeadlineExceeded, and treating it as the caller's deadline rolled
//       back the staged work — while a deadline the caller (or a client-wide
//       Timeout, or the stall bound) owns still is not retried.
//   F3  the transport error's Message is the cause (`dial tcp …: connection
//       refused`), not `Post "<url>": …`, so the 120-rune retry reason keeps the
//       useful end of a long URL; Error() stays what it was.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestStreamSchemeLessBaseURLFailsAtOnce: the value is refused before any I/O
// with the config package's text — a plain error, not a retryable one — and it
// does so immediately, not after three waits.
func TestStreamSchemeLessBaseURLFailsAtOnce(t *testing.T) {
	const want = "llm: config: llm.base_url must be an http(s) URL with a host, e.g. https://api.deepseek.com/v1"
	for _, base := range []string{"api.example.com/v1", "localhost:8080/v1", "ftp://api.example.com/v1", "http://"} {
		t.Run(base, func(t *testing.T) {
			start := time.Now()
			err := streamOnce(New(Config{BaseURL: base, Model: "m"}), context.Background())
			if err == nil || err.Error() != want {
				t.Fatalf("err = %v, want %q", err, want)
			}
			var pe *ProviderError
			if errors.As(err, &pe) {
				t.Errorf("a malformed base_url became a ProviderError (it would be retried): %+v", *pe)
			}
			if d := time.Since(start); d > time.Second {
				t.Errorf("took %s, want an immediate refusal", d)
			}
		})
	}
}

// plainClientErr is what a stock http.Client says for req — the real error a
// transport-failure classifier is shown.
func plainClientErr(t *testing.T, client *http.Client, rawurl string) error {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, rawurl, strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("NewRequest(%q): %v", rawurl, err)
	}
	_, err = client.Do(req)
	if err == nil {
		t.Fatalf("request to %q unexpectedly succeeded", rawurl)
	}
	return err
}

// TestTransportFailureClassification: which errors from a request with no
// response are the network's (worth waiting out) and which are permanent here.
func TestTransportFailureClassification(t *testing.T) {
	ctx := context.Background()

	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer tlsSrv.Close()
	plainSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer plainSrv.Close()

	hangup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	defer hangup.Close()

	wrap := func(err error) error { return &url.Error{Op: "Post", URL: "https://x/v1/chat/completions", Err: err} }
	c := &http.Client{}
	cases := []struct {
		name string
		err  error
		want bool // true: the network failing, retryable
	}{
		// the network's
		{"connection refused", plainClientErr(t, c, "http://"+refusedBaseURL(t)[len("http://"):]+"/chat/completions"), true},
		{"EOF before headers", plainClientErr(t, c, hangup.URL+"/chat/completions"), true},
		{"connection reset", wrap(&net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset by peer")}), true},
		{"dns failure", wrap(&net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "x", IsNotFound: true}}), true},
		{"tls handshake failure that is not about trust", wrap(errors.New("remote error: tls: handshake failure")), true},
		// permanent, local
		{"no scheme", plainClientErr(t, c, "api.example.com/v1/chat/completions"), false},
		{"ftp scheme", plainClientErr(t, c, "ftp://api.example.com/v1/chat/completions"), false},
		{"no host in url", wrap(errors.New("http: no Host in request URL")), false},
		{"tls unknown authority (real)", plainClientErr(t, c, tlsSrv.URL+"/chat/completions"), false},
		{"https to a plain http server (real)", plainClientErr(t, c, "https://"+plainSrv.Listener.Addr().String()+"/chat/completions"), false},
		{"x509 unknown authority", wrap(x509.UnknownAuthorityError{}), false},
		{"x509 hostname mismatch", wrap(x509.HostnameError{Host: "x"}), false},
		{"x509 invalid certificate", wrap(x509.CertificateInvalidError{Reason: x509.Expired}), false},
		{"tls verification error", wrap(&tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}), false},
		{"tls verification error with an opaque cause", wrap(&tls.CertificateVerificationError{Err: errors.New("x509: certificate has expired or is not yet valid")}), false},
		{"tls record header (not a TLS server)", wrap(tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}), false},
		// not the network's to retry
		{"canceled", wrap(context.Canceled), false},
		{"deadline", wrap(context.DeadlineExceeded), false},
		{"stalled", wrap(ErrStalled), false},
		{"client timeout while awaiting headers", wrap(&timeoutErr{"net/http: request canceled (Client.Timeout exceeded while awaiting headers)"}), false},
		// F2: a dial that timed out is the network
		{"dial i/o timeout", wrap(&net.OpError{Op: "dial", Net: "tcp", Err: context.DeadlineExceeded}), true},
		{"dial timeout, os error", wrap(&net.OpError{Op: "dial", Net: "tcp", Err: timeoutNetErr{}}), true},
		{"read i/o timeout is not a dial", wrap(&net.OpError{Op: "read", Net: "tcp", Err: context.DeadlineExceeded}), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := transportFailure(ctx, tc.err); got != tc.want {
				t.Errorf("transportFailure(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}

	// The caller's own ctx ending wins over everything, a dial error included.
	dead, cancel := context.WithCancel(ctx)
	cancel()
	if transportFailure(dead, wrap(&net.OpError{Op: "dial", Net: "tcp", Err: context.Canceled})) {
		t.Error("transportFailure treated a canceled caller ctx as the network failing")
	}
	expired, cancel2 := context.WithTimeout(ctx, time.Nanosecond)
	defer cancel2()
	time.Sleep(time.Millisecond)
	if transportFailure(expired, wrap(&net.OpError{Op: "dial", Net: "tcp", Err: context.DeadlineExceeded})) {
		t.Error("transportFailure treated the caller's own deadline during a dial as the network failing")
	}
}

type timeoutErr struct{ s string }

func (e *timeoutErr) Error() string { return e.s }
func (e *timeoutErr) Timeout() bool { return true }
func (e *timeoutErr) Is(target error) bool {
	return target == context.DeadlineExceeded
}

// timeoutNetErr is the os-level "i/o timeout" shape: a timeout that is a
// net.Error and is not (in the chain) the context package's deadline.
type timeoutNetErr struct{}

func (timeoutNetErr) Error() string   { return "i/o timeout" }
func (timeoutNetErr) Timeout() bool   { return true }
func (timeoutNetErr) Temporary() bool { return true }

// TestStreamTLSUnknownAuthorityNotRetried: through Stream, a TLS server whose
// certificate the client does not trust is a plain wrapped error — the
// verification failure travels in the chain — never a retryable ProviderError.
func TestStreamTLSUnknownAuthorityNotRetried(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	err := streamOnce(New(Config{BaseURL: srv.URL, Model: "m"}), context.Background())
	if err == nil {
		t.Fatal("Stream trusted a self-signed certificate")
	}
	var pe *ProviderError
	if errors.As(err, &pe) {
		t.Fatalf("a certificate the machine does not trust became a retryable ProviderError: %+v", *pe)
	}
	var ua x509.UnknownAuthorityError
	var cv *tls.CertificateVerificationError
	if !errors.As(err, &ua) && !errors.As(err, &cv) {
		t.Errorf("err = %v, want the TLS verification failure in its chain", err)
	}
	if !strings.HasPrefix(err.Error(), "llm: request: ") {
		t.Errorf("err = %q, want the plain `llm: request:` text", err)
	}
}

// TestStreamDialTimeoutIsTransport (F2): the dial gives up with an i/o timeout —
// the shape that matches context.DeadlineExceeded and was therefore left plain.
func TestStreamDialTimeoutIsTransport(t *testing.T) {
	c := New(Config{BaseURL: "http://" + refusedBaseURL(t)[len("http://"):], Model: "m"})
	c.httpClient.Transport.(*http.Transport).DialContext = (&net.Dialer{Timeout: time.Nanosecond}).DialContext

	err := streamOnce(c, context.Background())
	pe := asProviderError(t, err)
	if !pe.Transport || !pe.Retryable() {
		t.Fatalf("ProviderError = %+v, want a retryable Transport error", *pe)
	}
	if !strings.Contains(pe.Message, "i/o timeout") {
		t.Errorf("Message = %q, want the dial timeout's text", pe.Message)
	}
}

// TestStreamClientTimeoutAndCallerDeadlineStayPlain: F2 does not widen the rule —
// a client-wide Timeout elapsing while the server holds the request, and the
// caller's own deadline, keep their plain wrapped errors.
func TestStreamClientTimeoutAndCallerDeadlineStayPlain(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	defer close(release)

	t.Run("client timeout", func(t *testing.T) {
		err := streamOnce(New(Config{BaseURL: slow.URL, Model: "m", Timeout: 40 * time.Millisecond}), context.Background())
		var pe *ProviderError
		if err == nil || errors.As(err, &pe) {
			t.Fatalf("err = %v, want a plain (non-ProviderError) timeout", err)
		}
	})
	t.Run("caller deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		defer cancel()
		err := streamOnce(New(Config{BaseURL: slow.URL, Model: "m"}), ctx)
		var pe *ProviderError
		if !errors.Is(err, context.DeadlineExceeded) || errors.As(err, &pe) {
			t.Fatalf("err = %v, want a plain DeadlineExceeded", err)
		}
	})
}

// TestStreamTransportMessageIsTheCause (F3): Message is what the network said,
// without the `Post "<url>": ` the HTTP client puts in front; Error() is
// unchanged. The retry reason cuts Message to 120 runes, and a long URL used to
// take all of them.
func TestStreamTransportMessageIsTheCause(t *testing.T) {
	base := refusedBaseURL(t)
	err := streamOnce(New(Config{BaseURL: base, Model: "m"}), context.Background())
	pe := asProviderError(t, err)

	addr := strings.TrimPrefix(base, "http://")
	if want := "dial tcp " + addr + ": connect: connection refused"; pe.Message != want {
		t.Errorf("Message = %q, want %q", pe.Message, want)
	}
	if strings.Contains(pe.Message, "Post") || strings.Contains(pe.Message, "/chat/completions") {
		t.Errorf("Message %q still carries the request line", pe.Message)
	}
	if want := `llm: request: Post "` + base + `/chat/completions": dial tcp ` + addr + ": connect: connection refused"; err.Error() != want {
		t.Errorf("Error() =\n%q\nwant the unchanged\n%q", err.Error(), want)
	}
}
