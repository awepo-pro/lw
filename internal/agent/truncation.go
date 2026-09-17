package agent

// truncation.go holds 008's U1 fix (contract §1): a round whose provider
// finish reason is neither "stop" nor "tool_calls" — in practice "length",
// the output-token cap — and that completed no tool call is a truncated
// turn, not a clean one. Reporting it as a clean stop is how 000006 ended
// a turn with exit 0 and committed an ingest of zero pages (MASTER §6 W0).

import "errors"

// ErrTruncated is returned by Send (and carried by its one ErrorEv) when a
// round ends with a provider finish reason other than "stop" or
// "tool_calls" — in practice "length", the output-token cap — and the round
// did NOT complete a tool call. The model stopped before finishing its turn;
// treating that as a clean stop is how 000006 committed zero pages (U1).
var ErrTruncated = errors.New("agent: the model stopped before finishing its turn")

// truncated reports whether finish — the round's last non-empty
// Chunk.Finish — is an abnormal provider stop. "" counts as clean: fakes
// and providers that never send a finish reason must not start failing
// (contract §1). Callers combine this with "the round completed no tool
// call" — a round that dispatched a tool proceeds whatever its finish
// reason — and both call sites (runRound's closing record and Send's
// terminal event) must agree, which is why the predicate lives here once.
func truncated(finish string) bool {
	return finish != "" && finish != "stop" && finish != "tool_calls"
}
