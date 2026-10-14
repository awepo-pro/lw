package llm

// provider_error_message_test.go pins 061 FIX1 A-061-3: a provider's HTTP
// refusal keeps its whole body (Body, what Error() has always printed) and
// gains a Message that is the sentence a human wants — the JSON body's
// error.message — so the retry line and the partial mark name the provider's
// words, not a screenful of JSON. The measured cause (s47 live run):
// `[provider 429: {"error":{"message":"Rate limit exceeded:
// free-models-per-min","code":429}}; retrying in 1s]`.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestStreamHTTPErrorMessageUnwrapped: the free-models-per-min 429 body gives
// Message "Rate limit exceeded: free-models-per-min", Body the whole trimmed
// body, and Error() the byte-identical text it always printed.
func TestStreamHTTPErrorMessageUnwrapped(t *testing.T) {
	const body = `{"error":{"message":"Rate limit exceeded: free-models-per-min","code":429}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("\n" + body + "\n"))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "test-model"})
	_, err := c.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	pe := asProviderError(t, err)
	if want := "Rate limit exceeded: free-models-per-min"; pe.Message != want {
		t.Errorf("Message = %q, want %q", pe.Message, want)
	}
	if pe.Body != body {
		t.Errorf("Body = %q, want the whole trimmed body %q", pe.Body, body)
	}
	if want := "llm: unexpected status 429: " + body; err.Error() != want {
		t.Errorf("Error() = %q, want the byte-identical status text %q", err.Error(), want)
	}
	if pe.Status != 429 || !pe.Retryable() {
		t.Errorf("Status/Retryable changed: %+v", pe)
	}
}

// TestHTTPErrorMessageFromBody is the unwrap rule on its own: error.message
// when the body parses as {"error":{"message":<string>…}}, else a top-level
// "message" string, else the trimmed body whole.
func TestHTTPErrorMessageFromBody(t *testing.T) {
	long := strings.Repeat("x", 5000)
	cases := []struct {
		name string
		body string
		want string
	}{
		{"openrouter nested", `{"error":{"message":"slow down","code":429}}`, "slow down"},
		{"nested with metadata", `{"error":{"message":"Provider returned error","code":502,"metadata":{"raw":"upstream"}}}`, "Provider returned error"},
		{"openai style", `{"error":{"message":"Incorrect API key","type":"invalid_request_error","param":null,"code":"invalid_api_key"}}`, "Incorrect API key"},
		{"top level message", `{"message":"Not Found","code":404}`, "Not Found"},
		{"nested wins over top level", `{"message":"outer","error":{"message":"inner"}}`, "inner"},
		{"message trimmed", `{"error":{"message":"  padded\n"}}`, "padded"},
		{"error string is not unwrapped", `{"error":"bad key"}`, `{"error":"bad key"}`},
		{"error string, top level message present", `{"error":"bad key","message":"outer"}`, "outer"},
		{"message not a string", `{"error":{"message":{"detail":"x"}}}`, `{"error":{"message":{"detail":"x"}}}`},
		{"message empty falls through to top level", `{"error":{"message":""},"message":"outer"}`, "outer"},
		{"message empty everywhere", `{"error":{"message":"  "}}`, `{"error":{"message":"  "}}`},
		{"no message key", `{"error":{"code":500}}`, `{"error":{"code":500}}`},
		{"not json", `<html>Bad Gateway</html>`, `<html>Bad Gateway</html>`},
		{"truncated json", `{"error":{"message":"cut off`, `{"error":{"message":"cut off`},
		{"json array", `["a"]`, `["a"]`},
		{"json scalar", `"just a string"`, `"just a string"`},
		{"empty", ``, ``},
		{"huge non-json", long, long},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := httpErrorMessage(tc.body); got != tc.want {
				t.Errorf("httpErrorMessage(%.60q) = %.80q, want %.80q", tc.body, got, tc.want)
			}
		})
	}
}

// TestProviderErrorBodyPrintedNotMessage: for an HTTP refusal Error() prints
// Body, never the unwrapped Message — every existing caller, log line and e2e
// assertion matches the full text. A hand-built error with no Body (a test's,
// or any caller's) prints its Message, so such an error reads as before.
func TestProviderErrorBodyPrintedNotMessage(t *testing.T) {
	pe := ProviderError{Status: 429, Message: "slow down", Body: `{"error":{"message":"slow down"}}`}
	if want := `llm: unexpected status 429: {"error":{"message":"slow down"}}`; pe.Error() != want {
		t.Errorf("Error() = %q, want %q", pe.Error(), want)
	}
	bare := ProviderError{Status: 401, Message: "bad key"}
	if want := "llm: unexpected status 401: bad key"; bare.Error() != want {
		t.Errorf("Error() without a Body = %q, want %q", bare.Error(), want)
	}
	inStream := ProviderError{Status: 502, Message: "overloaded", Body: "ignored", InStream: true}
	if want := "llm: provider error in stream (502): overloaded"; inStream.Error() != want {
		t.Errorf("in-stream Error() = %q, want %q (Body is the HTTP form's)", inStream.Error(), want)
	}
}

// TestStreamHTTPErrorBodyKeptWhole: an HTTP body that is not the JSON shape
// stays whole in both fields, bounded exactly as before.
func TestStreamHTTPErrorBodyKeptWhole(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("  <html>Bad Gateway</html>  "))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "test-model"})
	_, err := c.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	pe := asProviderError(t, err)
	if pe.Message != "<html>Bad Gateway</html>" || pe.Body != "<html>Bad Gateway</html>" {
		t.Errorf("Message/Body = %q / %q, want the trimmed body in both", pe.Message, pe.Body)
	}
	if want := "llm: unexpected status 502: <html>Bad Gateway</html>"; err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

// TestProviderErrorShort: the `provider <code>` form D1 gives the in-stream
// code — the HTTP status when there is one, else the string code, else
// "unknown" — one definition for the retry line and the partial mark.
func TestProviderErrorShort(t *testing.T) {
	cases := []struct {
		name string
		pe   *ProviderError
		want string
	}{
		{"http status", &ProviderError{Status: 429, Message: "slow"}, "provider 429"},
		{"in stream status", &ProviderError{Status: 502, InStream: true}, "provider 502"},
		{"string code", &ProviderError{Code: "server_error", InStream: true}, "provider server_error"},
		{"numeric wins over string", &ProviderError{Status: 429, Code: "rate_limit", InStream: true}, "provider 429"},
		{"neither", &ProviderError{InStream: true}, "provider unknown"},
		{"nil", nil, "provider unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.pe.Short(); got != tc.want {
				t.Errorf("Short() = %q, want %q", got, tc.want)
			}
		})
	}
}
