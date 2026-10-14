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
	"unicode"
	"unicode/utf8"
)

// ProviderError is the provider's own refusal: an HTTP error status, or an
// error event inside a 200 stream (061).
type ProviderError struct {
	Status     int           // HTTP status, or the in-stream error.code when numeric; 0 if unknown
	Code       string        // the in-stream error.code verbatim when it is a string ("server_error"); "" otherwise
	Message    string        // the provider's sentence: an HTTP body's error.message (else the body), ≤ 4096 bytes; an in-stream message, ≤ 1024 runes
	Body       string        // HTTP only: the whole response body, trimmed, ≤ 4096 bytes — what Error() prints; "" in-stream
	RetryAfter time.Duration // Retry-After header (delta-seconds or HTTP-date, clamped ≥ 0); 0 when absent/unparseable
	InStream   bool          // true: arrived as an SSE data event after a 200
	Transport  bool          // true: no response at all — the dial, DNS, TLS handshake or connection failed before headers (061 FIX2); Status 0, not InStream

	// cause is the transport error a Transport failure wraps: Error() prints its
	// text and Unwrap hands it on, so errors.Is(err, syscall.ECONNREFUSED) and
	// the like keep working exactly as with the plain `llm: request: %w`.
	cause error
}

// Error keeps the HTTP form byte-identical to the text Stream returned before
// the type existed — callers, logs and e2e assertions match on it, so it
// prints the whole Body, never the unwrapped Message (061 FIX1: Message is the
// short sentence for the retry line and the partial mark). An error built
// without a Body prints its Message, so it reads as it always did. A transport
// failure (061 FIX2) prints the `llm: request: <err>` it printed before it was
// typed. It gives the in-stream form the same "llm:" prefix with the code in
// parentheses.
func (e *ProviderError) Error() string {
	if e.Transport {
		if e.cause != nil {
			return "llm: request: " + e.cause.Error()
		}
		return "llm: request: " + e.Message
	}
	if !e.InStream {
		text := e.Body
		if text == "" {
			text = e.Message
		}
		return fmt.Sprintf("llm: unexpected status %d: %s", e.Status, text)
	}
	if e.Message == "" {
		return "llm: provider error in stream (" + e.code() + ")"
	}
	return "llm: provider error in stream (" + e.code() + "): " + e.Message
}

// code names the failure the way an in-stream Error() does: the HTTP status
// when there is one, else the provider's string code, else "unknown" — and
// "network" for a transport failure, which has neither.
func (e *ProviderError) code() string {
	switch {
	case e.Status != 0:
		return strconv.Itoa(e.Status)
	case e.Code != "":
		return e.Code
	case e.Transport:
		return "network"
	}
	return "unknown"
}

// Unwrap hands on the transport error a Transport failure wraps (nil for every
// other kind), so the chain behind `llm: request: %w` survives the typing.
func (e *ProviderError) Unwrap() error { return e.cause }

// Short is the failure in two words, "provider <code>" — "provider 502",
// "provider server_error", "provider unknown" — the one form the agent's retry
// line and `lw ingest`'s partial mark both name a provider failure by (061
// FIX1: it was written twice, in agent and cmd/lw). Nil-safe, like Retryable.
func (e *ProviderError) Short() string {
	if e == nil {
		return "provider unknown"
	}
	return "provider " + e.code()
}

// httpErrorMessage is the provider's sentence out of an HTTP error body: the
// JSON body's error.message when it parses as {"error":{"message":<string>…}},
// else a top-level "message" string, else the body whole. Message is what the
// retry line and the partial mark print, so a 429 reads `Rate limit exceeded:
// free-models-per-min`, not `{"error":{"message":"Rate limit exceeded: …","code":429}}`
// (s47 live run). Anything that is not that shape — HTML from a gateway, a
// body cut at the 4096-byte read, an error given as a bare string with no
// top-level message, a non-string or blank message — is returned as it came,
// so no information is lost to the guess.
func httpErrorMessage(body string) string {
	var top map[string]json.RawMessage
	if json.Unmarshal([]byte(body), &top) != nil {
		return body
	}
	var nested struct {
		Message json.RawMessage `json:"message"`
	}
	// A bare-string or otherwise odd "error" leaves nested empty; the
	// top-level message below still gets its turn.
	_ = json.Unmarshal(top["error"], &nested)
	for _, raw := range []json.RawMessage{nested.Message, top["message"]} {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			if s = strings.TrimSpace(s); s != "" {
				return s
			}
		}
	}
	return body
}

// Retryable reports whether trying the same request again can reasonably
// succeed: the statuses that mean "the provider (or the path to it) is busy or
// broke", never the ones that mean "this request is wrong". 402 is retryable
// only when the provider named a wait (OpenRouter's in-flight budget sends
// Retry-After; a 402 without it is a real out-of-credit). An in-stream error
// with no numeric status is a mid-stream failure on the provider's side — the
// request was already accepted and had begun to be answered — unless its
// string code names a permanent problem (permanentCode: a bad key, a request
// over the context window, an unknown model). A transport failure (061 FIX2:
// the dial, DNS, TLS handshake or connection failed before any response) is
// the path to the provider breaking, retryable like a 503.
func (e *ProviderError) Retryable() bool {
	if e == nil {
		return false
	}
	if e.Transport {
		return true
	}
	switch e.Status {
	case 408, 409, 425, 429, 500, 502, 503, 504, 529:
		return true
	case 402:
		return e.RetryAfter > 0
	case 0:
		return e.InStream && !permanentCode(e.Code)
	}
	return false
}

// permanentCodes are the in-stream string codes that say the REQUEST is wrong
// or the account cannot serve it — re-sending the same request three times
// after waiting 2, 4 and 8 seconds cannot change that. Anything else stays
// retryable: a mid-stream failure with an unrecognised code is more likely the
// provider's than the request's (061 FIX2 R4). Compared case-insensitively;
// "invalid_*" is the whole OpenAI family (invalid_api_key,
// invalid_request_error, …).
var permanentCodes = map[string]bool{
	"context_length_exceeded": true,
	"insufficient_quota":      true,
	"model_not_found":         true,
	"unsupported_parameter":   true,
	"authentication_error":    true,
	"permission_denied":       true,
}

func permanentCode(code string) bool {
	c := strings.ToLower(code)
	return strings.HasPrefix(c, "invalid_") || permanentCodes[c]
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
	// empty is an error carrier that does not count as an error (061 FIX2 R1).
	// Some gateways stamp an "error" on EVERY ordinary chunk — null fields, empty
	// strings, a 0, a false, an empty list — and an error that says nothing must
	// not turn a healthy stream into a failed turn. A carrier counts ONLY if its
	// message is a non-empty string, or its code is a non-empty string or a
	// number of 1 or more; the choice's own finish_reason "error" is handled
	// separately, in the stream loop.
	empty bool
}

// UnmarshalJSON decodes the object and string shapes of an "error" member and
// settles whether the carrier counts (wireStreamError.empty). Every other JSON
// value — false, true, a number, a list — carries no message and no code, so it
// does not count. A JSON null never reaches it (encoding/json leaves the
// pointer nil), so nil means "no error key".
func (e *wireStreamError) UnmarshalJSON(b []byte) error {
	*e = wireStreamError{}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(b, &obj); err == nil {
		e.Code = obj["code"]
		e.Message = rawText(obj["message"])
		e.empty = !(isNonEmptyString(obj["message"]) || codeCounts(obj["code"]))
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		e.Message = s
		e.empty = strings.TrimSpace(s) == ""
		return nil
	}
	e.empty = true
	return nil
}

// isNonEmptyString reports whether raw is a JSON string with something in it
// besides white space.
func isNonEmptyString(raw json.RawMessage) bool {
	var s string
	return json.Unmarshal(raw, &s) == nil && strings.TrimSpace(s) != ""
}

// codeCounts reports whether an error's "code" says anything: a non-empty
// string, or a number of 1 or more. null, false, 0, a negative or fractional
// number below one, and an empty string name nothing.
func codeCounts(raw json.RawMessage) bool {
	if isNonEmptyString(raw) {
		return true
	}
	var f float64
	return json.Unmarshal(raw, &f) == nil && f >= 1
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
	pe := &ProviderError{InStream: true, Message: boundRunes(cleanText(e.Message), inStreamMessageRunes)}
	raw := strings.TrimSpace(string(e.Code))
	switch {
	case raw == "" || raw == "null":
	case strings.HasPrefix(raw, `"`):
		var s string
		if json.Unmarshal(e.Code, &s) == nil {
			pe.Code = boundRunes(cleanText(s), providerCodeRunes)
		}
	default:
		var f float64
		if json.Unmarshal(e.Code, &f) == nil && f >= 1 && f <= math.MaxInt32 && f == math.Trunc(f) {
			pe.Status = int(f)
		}
	}
	return pe
}

// providerCodeRunes bounds an in-stream string code. A code is an identifier
// ("server_error", "rate_limit_exceeded"); one that runs to a screenful is the
// provider shouting, and it is printed in the retry line and the partial mark.
const providerCodeRunes = 64

// cleanText makes a provider's string safe to print: a provider's code and
// message reach the terminal (the retry notice, the partial mark, the log), and
// bytes the provider chose must not move the cursor, recolour the screen or
// forge a line. Line breaks and tabs become a space, every other control
// character (ESC included) is dropped, and the result is trimmed (061 FIX2
// R7). An escape SEQUENCE loses its ESC and keeps its printable remainder
// ("[31m"): inert text, not a cleverer parser to get wrong.
func cleanText(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t' || r == '\u2028' || r == '\u2029':
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s))
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
