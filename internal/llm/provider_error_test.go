package llm

// provider_error_test.go pins 061 S1: the provider's own refusal is a typed
// *ProviderError, whether it came as an HTTP status or as an error event
// inside a 200 stream. The measured cause (s47): OpenRouter's in-stream error
// event was a 328-byte data line with an "error" object and, live, no usable
// choice — the stream skipped it as a choice-less chunk and the turn was
// reported as "stream ended early after 328 bytes", the provider's message
// lost. The same file pins the base_url guard (D3: lw appends
// /chat/completions itself) and OpenRouter's delta.reasoning (D4). Bodies are
// replayed verbatim through httptest servers, as the 035 tests do; nothing
// here touches a real network.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// asProviderError unwraps err to the *ProviderError it must carry.
func asProviderError(t *testing.T, err error) *ProviderError {
	t.Helper()
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v (%T), want errors.As *ProviderError", err, err)
	}
	return pe
}

// sseLines renders payloads as SSE data lines, each followed by the blank
// separator — the way a provider writes them. A payload "[DONE]" is the
// stream terminator.
func sseLines(payloads ...string) []byte {
	var b strings.Builder
	for _, p := range payloads {
		b.WriteString("data: ")
		b.WriteString(p)
		b.WriteString("\n\n")
	}
	return []byte(b.String())
}

// The two chunks of the s47 shapes. The with-choices form is OpenRouter's
// documented mid-stream error (HTTP 200, a top-level error object, a choice
// whose finish_reason is "error"); the no-choices form is what the s47 live
// line looked like (328 bytes, parsed as a chunk, no usable finish_reason).
const (
	reasoningChunk  = `{"id":"g","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"reasoning_content":"thinking it over"}}]}`
	errorWithChoice = `{"id":"g","object":"chat.completion.chunk","error":{"code":502,"message":"Upstream error from Nvidia: overloaded"},"choices":[{"index":0,"delta":{"content":""},"finish_reason":"error"}]}`
	errorNoChoices  = `{"id":"g","object":"chat.completion.chunk","error":{"code":502,"message":"Upstream error from Nvidia: overloaded"}}`
)

func TestStreamHTTPErrorIsProviderError(t *testing.T) {
	const body = `{"error":{"message":"slow down","code":429}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "test-model"})
	ch, err := c.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("Stream: got nil error, want the provider's 429")
	}
	if ch != nil {
		t.Fatal("Stream: got a non-nil channel alongside an error")
	}
	pe := asProviderError(t, err)
	if pe.Status != 429 {
		t.Errorf("Status = %d, want 429", pe.Status)
	}
	if pe.RetryAfter != 7*time.Second {
		t.Errorf("RetryAfter = %s, want 7s", pe.RetryAfter)
	}
	if pe.InStream {
		t.Error("InStream = true, want false for an HTTP status")
	}
	if !pe.Retryable() {
		t.Error("Retryable() = false, want true for a 429")
	}
	// FIX1 A-061-3 amendment: Message is the body's error.message, Body the
	// whole body (it was: Message == the body).
	if pe.Message != "slow down" {
		t.Errorf("Message = %q, want the body's error.message %q", pe.Message, "slow down")
	}
	if pe.Body != body {
		t.Errorf("Body = %q, want the body %q", pe.Body, body)
	}
	const want = `llm: unexpected status 429: {"error":{"message":"slow down","code":429}}`
	if err.Error() != want {
		t.Errorf("err.Error() = %q, want the byte-identical status text %q", err.Error(), want)
	}
}

func TestStreamHTTPErrorRetryAfterDate(t *testing.T) {
	serve := func(t *testing.T, retryAfter string) error {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", retryAfter)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("busy"))
		}))
		defer srv.Close()
		c := New(Config{BaseURL: srv.URL, Model: "test-model"})
		_, err := c.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
		if err == nil {
			t.Fatal("Stream: got nil error, want the provider's 503")
		}
		return err
	}

	t.Run("http_date_30s_ahead", func(t *testing.T) {
		// Stream measures the date against the instant the response headers
		// arrived, so with no clock seam the assertion is a window: the date
		// is whole seconds (truncated) and the header arrives a little after
		// it was written.
		date := time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)
		pe := asProviderError(t, serve(t, date))
		if pe.RetryAfter < 25*time.Second || pe.RetryAfter > 30*time.Second {
			t.Errorf("RetryAfter = %s for Retry-After %q, want 25s..30s", pe.RetryAfter, date)
		}
	})
	t.Run("garbage_is_zero", func(t *testing.T) {
		pe := asProviderError(t, serve(t, "next Tuesday, probably"))
		if pe.RetryAfter != 0 {
			t.Errorf("RetryAfter = %s for a garbage header, want 0", pe.RetryAfter)
		}
	})
	t.Run("absent_is_zero", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer srv.Close()
		c := New(Config{BaseURL: srv.URL, Model: "test-model"})
		_, err := c.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
		pe := asProviderError(t, err)
		if pe.RetryAfter != 0 {
			t.Errorf("RetryAfter = %s with no header, want 0", pe.RetryAfter)
		}
		if err.Error() != "llm: unexpected status 503: " {
			t.Errorf("err.Error() = %q, want today's text for an empty body", err.Error())
		}
	})
}

// TestParseRetryAfter pins the header grammar on a fixed clock: delta-seconds
// or an HTTP-date, never negative, 0 for anything else.
func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		in   string
		want time.Duration
	}{
		{"seconds", "7", 7 * time.Second},
		{"seconds padded", "  7 ", 7 * time.Second},
		{"zero", "0", 0},
		{"negative clamps to zero", "-3", 0},
		{"empty", "", 0},
		{"garbage", "soon", 0},
		{"fraction is not delta-seconds", "1.5", 0},
		{"imf-fixdate 30s ahead", now.Add(30 * time.Second).Format(http.TimeFormat), 30 * time.Second},
		{"rfc850 date 90s ahead", now.Add(90 * time.Second).Format(time.RFC850), 90 * time.Second},
		{"date in the past clamps to zero", now.Add(-time.Minute).Format(http.TimeFormat), 0},
		{"huge seconds saturate instead of overflowing", "99999999999999999999", maxRetryAfter},
		{"seconds past the cap are capped", "172800", maxRetryAfter},
		{"date far ahead is capped", "Fri, 01 Jan 2100 00:00:00 GMT", maxRetryAfter},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseRetryAfter(tc.in, now); got != tc.want {
				t.Errorf("parseRetryAfter(%q) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestRetryableTable(t *testing.T) {
	cases := []struct {
		name string
		pe   ProviderError
		want bool
	}{
		{"400", ProviderError{Status: 400}, false},
		{"401", ProviderError{Status: 401}, false},
		{"402 plain", ProviderError{Status: 402}, false},
		{"402 with Retry-After", ProviderError{Status: 402, RetryAfter: 5 * time.Second}, true},
		{"403", ProviderError{Status: 403}, false},
		{"404", ProviderError{Status: 404}, false},
		{"408", ProviderError{Status: 408}, true},
		{"409", ProviderError{Status: 409}, true},
		{"413", ProviderError{Status: 413}, false},
		{"422", ProviderError{Status: 422}, false},
		{"425", ProviderError{Status: 425}, true},
		{"429", ProviderError{Status: 429}, true},
		{"500", ProviderError{Status: 500}, true},
		{"502", ProviderError{Status: 502}, true},
		{"503", ProviderError{Status: 503}, true},
		{"504", ProviderError{Status: 504}, true},
		{"529", ProviderError{Status: 529}, true},
		{"501 is not in the set", ProviderError{Status: 501}, false},
		{"other 4xx", ProviderError{Status: 418}, false},
		{"429 in stream", ProviderError{Status: 429, InStream: true}, true},
		{"401 in stream", ProviderError{Status: 401, InStream: true}, false},
		{"in stream, string code", ProviderError{Code: "server_error", InStream: true}, true},
		{"in stream, no code at all", ProviderError{InStream: true}, true},
		{"HTTP status 0 is unknown, not a provider fault", ProviderError{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pe := tc.pe
			if got := pe.Retryable(); got != tc.want {
				t.Errorf("%+v.Retryable() = %v, want %v", tc.pe, got, tc.want)
			}
		})
	}
	var nilPE *ProviderError
	if nilPE.Retryable() {
		t.Error("(*ProviderError)(nil).Retryable() = true, want false")
	}
}

func TestProviderErrorText(t *testing.T) {
	cases := []struct {
		name string
		pe   ProviderError
		want string
	}{
		{"http", ProviderError{Status: 401, Message: `{"error":"bad key"}`}, `llm: unexpected status 401: {"error":"bad key"}`},
		{"http empty body", ProviderError{Status: 502}, "llm: unexpected status 502: "},
		{"in stream numeric", ProviderError{Status: 502, Message: "overloaded", InStream: true}, "llm: provider error in stream (502): overloaded"},
		{"in stream string code", ProviderError{Code: "server_error", Message: "gone", InStream: true}, "llm: provider error in stream (server_error): gone"},
		{"numeric code wins over string", ProviderError{Status: 429, Code: "rate_limit", Message: "slow", InStream: true}, "llm: provider error in stream (429): slow"},
		{"in stream no code", ProviderError{Message: "boom", InStream: true}, "llm: provider error in stream (unknown): boom"},
		{"in stream no message", ProviderError{Status: 502, InStream: true}, "llm: provider error in stream (502)"},
		{"in stream nothing", ProviderError{InStream: true}, "llm: provider error in stream (unknown)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pe := tc.pe
			if got := pe.Error(); got != tc.want {
				t.Errorf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestStreamHTTPErrorBodyBound pins the HTTP message bound: the body is read
// to 4096 bytes and trimmed, exactly as before the type existed.
func TestStreamHTTPErrorBodyBound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("  " + strings.Repeat("x", 6000) + "  "))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "test-model"})
	_, err := c.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	pe := asProviderError(t, err)
	if want := strings.Repeat("x", 4094); pe.Message != want {
		t.Errorf("Message = %d bytes (%.20q...), want the first 4096 body bytes trimmed (4094 x's)", len(pe.Message), pe.Message)
	}
	if want := "llm: unexpected status 400: " + strings.Repeat("x", 4094); err.Error() != want {
		t.Errorf("err.Error() is %d bytes, want %d", len(err.Error()), len(want))
	}
}

func TestStreamInStreamErrorObject(t *testing.T) {
	body := sseLines(reasoningChunk, errorWithChoice)

	chunks := streamChunks(t, body)

	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want the reasoning fragment then exactly one error: %+v", len(chunks), chunks)
	}
	if chunks[0].Reasoning != "thinking it over" {
		t.Errorf("chunks[0] = %+v, want the reasoning fragment first", chunks[0])
	}
	err := chunks[1].Err
	if err == nil {
		t.Fatalf("chunks[1] = %+v, want the provider error", chunks[1])
	}
	pe := asProviderError(t, err)
	if pe.Status != 502 || pe.Code != "" || !pe.InStream {
		t.Errorf("ProviderError = %+v, want Status 502, no Code, InStream", *pe)
	}
	if pe.Message != "Upstream error from Nvidia: overloaded" {
		t.Errorf("Message = %q", pe.Message)
	}
	if pe.RetryAfter != 0 {
		t.Errorf("RetryAfter = %s, want 0 (no header exists mid-stream)", pe.RetryAfter)
	}
	if !pe.Retryable() {
		t.Error("Retryable() = false, want true for an in-stream 502")
	}
	const want = "llm: provider error in stream (502): Upstream error from Nvidia: overloaded"
	if err.Error() != want {
		t.Errorf("err.Error() = %q, want %q", err.Error(), want)
	}
	if errors.Is(err, ErrStreamTruncated) {
		t.Errorf("err = %v is errors.Is ErrStreamTruncated; a provider error is not a truncation", err)
	}
	for i, c := range chunks {
		if c.Usage != nil {
			t.Errorf("chunks[%d] carries Usage %+v; an errored round is never accounted", i, *c.Usage)
		}
		if c.Finish != "" {
			t.Errorf("chunks[%d] carries Finish %q; the error replaces the finish", i, c.Finish)
		}
	}
}

// TestStreamInStreamErrorNoChoices is the s47 live shape: the error object
// with no choices key at all. It must come out as the same typed error — it
// used to be skipped as a choice-less chunk and surface as "stream ended
// early after 328 bytes".
func TestStreamInStreamErrorNoChoices(t *testing.T) {
	chunks := streamChunks(t, sseLines(reasoningChunk, errorNoChoices))

	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want the reasoning fragment then exactly one error: %+v", len(chunks), chunks)
	}
	if chunks[0].Reasoning != "thinking it over" {
		t.Errorf("chunks[0] = %+v, want the reasoning fragment first", chunks[0])
	}
	err := chunks[1].Err
	if err == nil {
		t.Fatalf("chunks[1] = %+v, want the provider error", chunks[1])
	}
	pe := asProviderError(t, err)
	if pe.Status != 502 || !pe.InStream || pe.Message != "Upstream error from Nvidia: overloaded" {
		t.Errorf("ProviderError = %+v, want in-stream 502 with the provider's message", *pe)
	}
	const want = "llm: provider error in stream (502): Upstream error from Nvidia: overloaded"
	if err.Error() != want {
		t.Errorf("err.Error() = %q, want %q", err.Error(), want)
	}
	if errors.Is(err, ErrStreamTruncated) {
		t.Errorf("err = %v is errors.Is ErrStreamTruncated; the old misreport must not return", err)
	}
	if chunks[1].Usage != nil {
		t.Errorf("error chunk carries Usage %+v", *chunks[1].Usage)
	}
}

func TestStreamInStreamErrorStringCode(t *testing.T) {
	line := `{"id":"g","object":"chat.completion.chunk","error":{"code":"server_error","message":"Provider disconnected"}}`
	chunks := streamChunks(t, sseLines(line))

	if len(chunks) != 1 || chunks[0].Err == nil {
		t.Fatalf("chunks = %+v, want exactly one error chunk", chunks)
	}
	pe := asProviderError(t, chunks[0].Err)
	if pe.Status != 0 || pe.Code != "server_error" || !pe.InStream {
		t.Errorf("ProviderError = %+v, want Status 0, Code server_error, InStream", *pe)
	}
	const want = "llm: provider error in stream (server_error): Provider disconnected"
	if chunks[0].Err.Error() != want {
		t.Errorf("err.Error() = %q, want %q", chunks[0].Err.Error(), want)
	}
	if !pe.Retryable() {
		t.Error("Retryable() = false, want true: a mid-stream failure with a string code is the provider's")
	}
}

func TestStreamFinishReasonErrorWithoutObject(t *testing.T) {
	line := `{"id":"g","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":""},"finish_reason":"error"}]}`
	chunks := streamChunks(t, sseLines(reasoningChunk, line))

	if len(chunks) != 2 || chunks[1].Err == nil {
		t.Fatalf("chunks = %+v, want the reasoning fragment then one error chunk", chunks)
	}
	for _, c := range chunks {
		if c.Finish != "" {
			t.Errorf("chunk %+v carries Finish %q; finish_reason \"error\" is not a finish", c, c.Finish)
		}
	}
	pe := asProviderError(t, chunks[1].Err)
	if !pe.InStream || pe.Status != 0 || pe.Code != "" {
		t.Errorf("ProviderError = %+v, want InStream with no Status and no Code", *pe)
	}
	const wantMsg = `the provider ended the stream with finish_reason "error"`
	if pe.Message != wantMsg {
		t.Errorf("Message = %q, want %q", pe.Message, wantMsg)
	}
	if want := "llm: provider error in stream (unknown): " + wantMsg; chunks[1].Err.Error() != want {
		t.Errorf("err.Error() = %q, want %q", chunks[1].Err.Error(), want)
	}
	if errors.Is(chunks[1].Err, ErrStreamTruncated) {
		t.Error("finish_reason \"error\" is not a truncation")
	}
}

// TestStreamFinishReasonErrorDropsAssemblingCall: a tool call still being
// assembled when the provider errors is never dispatched, exactly as a
// truncation never dispatches one — the round did not finish it.
func TestStreamFinishReasonErrorDropsAssemblingCall(t *testing.T) {
	call := `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"wiki_get","arguments":"{\"page\":"}}]}}]}`
	fin := `{"choices":[{"index":0,"delta":{},"finish_reason":"error"}]}`
	chunks := streamChunks(t, sseLines(call, fin))

	if len(chunks) != 1 || chunks[0].Err == nil {
		t.Fatalf("chunks = %+v, want exactly one error chunk and no tool call", chunks)
	}
	asProviderError(t, chunks[0].Err)
}

// TestStreamInStreamErrorStopsStream: the error is terminal. Whatever the
// provider writes after it, including a usage object and [DONE], is never
// delivered — the errored round is not an accounted one.
func TestStreamInStreamErrorStopsStream(t *testing.T) {
	withUsage := `{"id":"g","error":{"code":502,"message":"gone"},"usage":{"prompt_tokens":10,"completion_tokens":2}}`
	trailing := `{"choices":[{"index":0,"delta":{"content":"late"},"finish_reason":"stop"}]}`
	chunks := streamChunks(t, sseLines(withUsage, trailing, "[DONE]"))

	if len(chunks) != 1 || chunks[0].Err == nil {
		t.Fatalf("chunks = %+v, want exactly one error chunk", chunks)
	}
	if chunks[0].Usage != nil {
		t.Errorf("error chunk carries Usage %+v", *chunks[0].Usage)
	}
}

// TestStreamInStreamErrorShapes covers the shapes a provider's error event can
// take beyond OpenRouter's documented one. None may fall back to a parse
// failure (which would lose the provider's message the way s47 did), and an
// empty error carrier must leave an ordinary stream untouched.
func TestStreamInStreamErrorShapes(t *testing.T) {
	long := strings.Repeat("é", 1500)
	cases := []struct {
		name       string
		line       string
		wantStatus int
		wantCode   string
		wantMsg    string
	}{
		{"bare string error", `{"error":"rate limited"}`, 0, "", "rate limited"},
		{"whole-number float code", `{"error":{"code":429.0,"message":"slow"}}`, 429, "", "slow"},
		{"null code", `{"error":{"code":null,"message":"odd"}}`, 0, "", "odd"},
		{"bool code is neither", `{"error":{"code":true,"message":"odd"}}`, 0, "", "odd"},
		{"fractional code is neither", `{"error":{"code":1.5,"message":"odd"}}`, 0, "", "odd"},
		{"zero code is no status", `{"error":{"code":0,"message":"odd"}}`, 0, "", "odd"},
		{"digit string code stays a string", `{"error":{"code":"429","message":"slow"}}`, 0, "429", "slow"},
		{"message is trimmed", `{"error":{"code":500,"message":"  spaced out \n"}}`, 500, "", "spaced out"},
		{"code only", `{"error":{"code":503}}`, 503, "", ""},
		{"metadata only", `{"error":{"metadata":{"provider_name":"Nvidia"}}}`, 0, "", ""},
		{"message that is not a string keeps its text", `{"error":{"code":500,"message":{"detail":"x"}}}`, 500, "", `{"detail":"x"}`},
		{"message is bounded to 1024 runes", `{"error":{"code":500,"message":"` + long + `"}}`, 500, "", strings.Repeat("é", 1023) + "…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chunks := streamChunks(t, sseLines(tc.line))
			if len(chunks) != 1 || chunks[0].Err == nil {
				t.Fatalf("chunks = %+v, want exactly one error chunk", chunks)
			}
			pe := asProviderError(t, chunks[0].Err)
			if pe.Status != tc.wantStatus || pe.Code != tc.wantCode || pe.Message != tc.wantMsg || !pe.InStream {
				t.Errorf("ProviderError = %+v, want Status %d Code %q Message %q InStream", *pe, tc.wantStatus, tc.wantCode, tc.wantMsg)
			}
		})
	}

	// An error key that carries nothing is not an error: the stream below is
	// an ordinary finished answer and must come out exactly as without it.
	for _, empty := range []string{`"error":null`, `"error":{}`, `"error":""`} {
		t.Run("empty carrier "+empty, func(t *testing.T) {
			text := `{` + empty + `,"choices":[{"index":0,"delta":{"content":"hi"}}]}`
			fin := `{` + empty + `,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
			chunks := streamChunks(t, sseLines(text, fin, "[DONE]"))
			if len(chunks) != 2 || chunks[0].Text != "hi" || chunks[1].Finish != "stop" {
				t.Fatalf("chunks = %+v, want the text then the stop finish and no error", chunks)
			}
		})
	}
}

func TestStreamErrorLogLine(t *testing.T) {
	logPath := installFileLog(t)

	chunks := streamChunks(t, sseLines(reasoningChunk, errorWithChoice))
	if len(chunks) != 2 || chunks[1].Err == nil {
		t.Fatalf("chunks = %+v, want the reasoning fragment then the provider error", chunks)
	}

	log := readLog(t, logPath)
	if got := strings.Count(log, `msg="llm provider error"`); got != 1 {
		t.Fatalf("log holds %d \"llm provider error\" records, want exactly one:\n%s", got, log)
	}
	if got := strings.Count(log, `msg="llm stream truncated"`); got != 0 {
		t.Errorf("log holds %d truncated records, want 0 — the error replaces the truncation:\n%s", got, log)
	}
	var rec string
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, `msg="llm provider error"`) {
			rec = line
		}
	}
	for _, want := range []string{
		"level=WARN",
		"status=502",
		`message="Upstream error from Nvidia: overloaded"`,
		"in_stream=true",
		"bytes=",
	} {
		if !strings.Contains(rec, want) {
			t.Errorf("provider error record missing %q:\n%s", want, rec)
		}
	}
}

func TestStreamDeltaReasoning(t *testing.T) {
	t.Run("reasoning_field_streams_as_reasoning", func(t *testing.T) {
		chunks := streamChunks(t, sseLines(
			`{"choices":[{"index":0,"delta":{"reasoning":"The"}}]}`,
			`{"choices":[{"index":0,"delta":{"reasoning":" user"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			"[DONE]",
		))
		if len(chunks) != 4 {
			t.Fatalf("got %d chunks, want 2 reasoning + text + finish: %+v", len(chunks), chunks)
		}
		if chunks[0].Reasoning != "The" || chunks[1].Reasoning != " user" {
			t.Errorf("reasoning chunks = %q, %q, want \"The\", \" user\"", chunks[0].Reasoning, chunks[1].Reasoning)
		}
		if chunks[2].Text != "Hello" || chunks[3].Finish != "stop" {
			t.Errorf("chunks[2:] = %+v, want the text then the finish", chunks[2:])
		}
	})

	t.Run("both_fields_emit_once", func(t *testing.T) {
		chunks := streamChunks(t, sseLines(
			`{"choices":[{"index":0,"delta":{"reasoning_content":"A","reasoning":"A"}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			"[DONE]",
		))
		if len(chunks) != 2 || chunks[0].Reasoning != "A" || chunks[1].Finish != "stop" {
			t.Fatalf("chunks = %+v, want ONE reasoning chunk \"A\" then the finish", chunks)
		}
	})

	t.Run("reasoning_content_wins_when_they_differ", func(t *testing.T) {
		chunks := streamChunks(t, sseLines(
			`{"choices":[{"index":0,"delta":{"reasoning_content":"A","reasoning":"B"}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			"[DONE]",
		))
		if len(chunks) != 2 || chunks[0].Reasoning != "A" {
			t.Fatalf("chunks = %+v, want the single reasoning chunk \"A\" (reasoning_content wins)", chunks)
		}
	})

	t.Run("reasoning_details_is_ignored", func(t *testing.T) {
		chunks := streamChunks(t, sseLines(
			`{"choices":[{"index":0,"delta":{"content":"ok","reasoning_details":[{"type":"reasoning.text","text":"secret","index":0}]}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			"[DONE]",
		))
		if len(chunks) != 2 || chunks[0].Text != "ok" || chunks[0].Reasoning != "" {
			t.Fatalf("chunks = %+v, want the text only: reasoning_details carries no reasoning chunk", chunks)
		}
	})
}

// countingTransport is a RoundTripper that never leaves the process: it counts
// the requests that reach it and fails each one. TestBaseURLChatCompletions
// swaps it in so a regression of the guard cannot send a real request to the
// real host the test names.
type countingTransport struct{ hits atomic.Int32 }

func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.hits.Add(1)
	return nil, errors.New("countingTransport: no network in tests")
}

func TestBaseURLChatCompletionsRefused(t *testing.T) {
	const want = "llm: config: llm.base_url ends in /chat/completions, but lw adds that path itself — set it to the API root: lw config set llm.base_url https://openrouter.ai/api/v1"
	req := Request{Messages: []Message{{Role: "user", Content: "hi"}}}

	t.Run("stream", func(t *testing.T) {
		tr := &countingTransport{}
		c := New(Config{BaseURL: "https://openrouter.ai/api/v1/chat/completions/", Model: "m"})
		c.httpClient = &http.Client{Transport: tr}

		ch, err := c.Stream(context.Background(), req)
		if err == nil {
			t.Fatal("Stream: got nil error, want the base_url refusal")
		}
		if ch != nil {
			t.Error("Stream: got a non-nil channel alongside an error")
		}
		if err.Error() != want {
			t.Errorf("err = %q\nwant %q", err.Error(), want)
		}
		if n := tr.hits.Load(); n != 0 {
			t.Errorf("%d request(s) reached the transport, want 0 — the guard runs before any I/O", n)
		}
	})

	t.Run("probe", func(t *testing.T) {
		tr := &countingTransport{}
		c := New(Config{BaseURL: "https://openrouter.ai/api/v1/chat/completions/", Model: "m"})
		c.httpClient = &http.Client{Transport: tr}

		res := c.Probe(context.Background())
		if res.Reachable || res.ToolCalling {
			t.Errorf("Probe = %+v, want not reachable", res)
		}
		if res.Err == nil || res.Err.Error() != want {
			t.Errorf("Probe.Err = %v\nwant %q", res.Err, want)
		}
		if n := tr.hits.Load(); n != 0 {
			t.Errorf("%d request(s) reached the transport, want 0", n)
		}
	})

	t.Run("httptest_server_sees_no_request", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		c := New(Config{BaseURL: srv.URL + "/v1/chat/completions", Model: "m"})
		_, err := c.Stream(context.Background(), req)
		if err == nil {
			t.Fatal("Stream: got nil error, want the base_url refusal")
		}
		if wantHere := "llm: config: llm.base_url ends in /chat/completions, but lw adds that path itself — set it to the API root: lw config set llm.base_url " + srv.URL + "/v1"; err.Error() != wantHere {
			t.Errorf("err = %q\nwant %q", err.Error(), wantHere)
		}
		if n := hits.Load(); n != 0 {
			t.Errorf("server saw %d request(s), want 0", n)
		}
	})

	t.Run("observer_sees_nothing", func(t *testing.T) {
		obs := &copyObserver{}
		c := New(Config{BaseURL: "https://x/v1/chat/completions", Model: "m", Observer: obs})
		c.httpClient = &http.Client{Transport: &countingTransport{}}
		if _, err := c.Stream(context.Background(), req); err == nil {
			t.Fatal("Stream: got nil error, want the base_url refusal")
		}
		if obs.calls != 0 {
			t.Errorf("Observer saw %d request(s); a refused request was never sent and must not be traced", obs.calls)
		}
	})

	t.Run("api_root_still_works", func(t *testing.T) {
		srv := sseServer(t, sseLines(`{"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}]}`, "[DONE]"), nil)
		defer srv.Close()
		c := New(Config{BaseURL: srv.URL + "/", Model: "m"})
		ch, err := c.Stream(context.Background(), req)
		if err != nil {
			t.Fatalf("Stream with an API-root base_url: %v", err)
		}
		collect(t, ch)
	})
}
