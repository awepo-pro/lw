package llm

// provider_error.go is 061's typed provider refusal. Until then a provider's
// "no" was a bare fmt.Errorf string, and one shape of it was no error at all:
// OpenRouter reports a mid-stream failure as an "error" object inside an HTTP
// 200 stream, which the chunk decoder had no field for, so s47's live ingest
// skipped it as a choice-less chunk and reported "stream ended early after 328
// bytes" with the provider's message lost. A caller that can tell a transient
// refusal from a permanent one (agent: back off and retry; cmd: keep the
// staged work) needs the status, the code and Retry-After, not a string.

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ProviderError is the provider's own refusal: an HTTP error status, or an
// error event inside a 200 stream (061).
type ProviderError struct {
	Status     int           // HTTP status, or the in-stream error.code when numeric; 0 if unknown
	Code       string        // the in-stream error.code verbatim when it is a string ("server_error"); "" otherwise
	Message    string        // provider text, trimmed, ≤ 4096 bytes (HTTP) / ≤ 1024 runes (in-stream)
	RetryAfter time.Duration // Retry-After header (delta-seconds or HTTP-date, clamped ≥ 0); 0 when absent/unparseable
	InStream   bool          // true: arrived as an SSE data event after a 200
}

// Error keeps the HTTP form byte-identical to the text Stream returned before
// the type existed — callers, logs and e2e assertions match on it — and gives
// the in-stream form the same "llm:" prefix with the code in parentheses.
func (e *ProviderError) Error() string {
	if !e.InStream {
		return fmt.Sprintf("llm: unexpected status %d: %s", e.Status, e.Message)
	}
	code := "unknown"
	switch {
	case e.Status != 0:
		code = strconv.Itoa(e.Status)
	case e.Code != "":
		code = e.Code
	}
	if e.Message == "" {
		return "llm: provider error in stream (" + code + ")"
	}
	return "llm: provider error in stream (" + code + "): " + e.Message
}

// Retryable reports whether trying the same request again can reasonably
// succeed: the statuses that mean "the provider (or the path to it) is busy or
// broke", never the ones that mean "this request is wrong". 402 is retryable
// only when the provider named a wait (OpenRouter's in-flight budget sends
// Retry-After; a 402 without it is a real out-of-credit). An in-stream error
// with no numeric status is a mid-stream failure on the provider's side — the
// request was already accepted and had begun to be answered.
func (e *ProviderError) Retryable() bool {
	if e == nil {
		return false
	}
	switch e.Status {
	case 408, 409, 425, 429, 500, 502, 503, 504, 529:
		return true
	case 402:
		return e.RetryAfter > 0
	case 0:
		return e.InStream
	}
	return false
}

// maxRetryAfter caps a parsed Retry-After. Past a day the number is a quota
// reset, not a wait, and the caller's own limit (agent: more than a minute is
// not waited out) sits far below it; the cap exists so an absurd header can
// neither overflow time.Duration nor read as "unparseable".
const maxRetryAfter = 24 * time.Hour

// parseRetryAfter reads a Retry-After header value: whole delta-seconds or an
// HTTP-date, measured against now (Stream passes the instant the response
// headers arrived). A date already past, a negative number, an absent value
// and anything unparseable are all 0 — "no wait named".
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err == nil {
		switch {
		case n <= 0:
			return 0
		case n >= int64(maxRetryAfter/time.Second):
			return maxRetryAfter
		}
		return time.Duration(n) * time.Second
	}
	if errors.Is(err, strconv.ErrRange) {
		// All digits, only too big for an int64: a saturating wait, not noise.
		if strings.HasPrefix(v, "-") {
			return 0
		}
		return maxRetryAfter
	}
	t, err := http.ParseTime(v)
	if err != nil {
		return 0
	}
	d := t.Sub(now)
	if d <= 0 {
		return 0
	}
	if d > maxRetryAfter {
		return maxRetryAfter
	}
	return d
}

// inStreamMessageRunes bounds an in-stream provider message. The HTTP path is
// bounded by its 4096-byte body read; an SSE event has no such read, and the
// message is user-facing (stdout, the retry reason, the log), so it is cut
// here.
const inStreamMessageRunes = 1024

// wireStreamError is the "error" member of a stream chunk. OpenRouter sends
// {"code":<HTTP status number>,"message":"…","metadata":{…}}; OpenAI-style
// providers send {"message","type","param","code":<string or null>}; some
// gateways send a bare string. UnmarshalJSON accepts all of them and never
// fails: a stream whose error event the decoder rejected would fall back to a
// parse failure and lose the provider's message — the very bug this type
// exists to fix.
type wireStreamError struct {
	Code    json.RawMessage
	Message string
	// empty is an error carrier with nothing in it ("error":{} or "error":""):
	// some gateways send one on every ordinary chunk, and it must not turn a
	// healthy stream into an error.
	empty bool
}

// UnmarshalJSON decodes the object, string and any-other-JSON shapes of an
// "error" member. A JSON null never reaches it (encoding/json leaves the
// pointer nil), so nil means "no error key".
func (e *wireStreamError) UnmarshalJSON(b []byte) error {
	*e = wireStreamError{}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(b, &obj); err == nil {
		if len(obj) == 0 {
			e.empty = true
			return nil
		}
		e.Code = obj["code"]
		e.Message = rawText(obj["message"])
		return nil
	}
	e.Message = rawText(b)
	e.empty = e.Message == ""
	return nil
}

// rawText is a JSON value as human text: a string unquoted, null as nothing,
// any other value (a nested object, a number) as its own JSON.
func rawText(raw json.RawMessage) string {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

// providerError builds the in-stream *ProviderError from the decoded event.
// A numeric code is the HTTP status the provider reports for the failure; a
// string code is kept verbatim; every other code shape (null, a boolean, a
// fraction, zero or negative) names nothing and leaves both unset.
func (e *wireStreamError) providerError() *ProviderError {
	pe := &ProviderError{InStream: true, Message: boundRunes(strings.TrimSpace(e.Message), inStreamMessageRunes)}
	raw := strings.TrimSpace(string(e.Code))
	switch {
	case raw == "" || raw == "null":
	case strings.HasPrefix(raw, `"`):
		var s string
		if json.Unmarshal(e.Code, &s) == nil {
			pe.Code = s
		}
	default:
		var f float64
		if json.Unmarshal(e.Code, &f) == nil && f >= 1 && f <= math.MaxInt32 && f == math.Trunc(f) {
			pe.Status = int(f)
		}
	}
	return pe
}

// boundRunes cuts s to at most max runes, the last of them "…" when anything
// was cut — the same marker the stage package uses for a bounded intent.
func boundRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-1]) + "…"
}
