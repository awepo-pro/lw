package agent

// streamcut.go holds 035's recovery policy for a provider stream that ended
// before [DONE] or a finish_reason — llm.ErrStreamTruncated arriving as a
// Chunk.Err mid-round. The policy is three-way because tool calls are
// dispatched MID-STREAM (loop.go runRound): whether the cut round already
// dispatched one decides everything.
//
//	(A) A call is out. The tool already ran; re-sending the request would
//	    run it a second time and stage the same op twice. The only safe move
//	    is to end the round as if it had finished and continue.
//	(B) Nothing is out. Nothing was dispatched, nothing was recorded
//	    (flushRecord has not fired), so the round's stream is thrown away
//	    whole and the identical request is sent again — once.
//	(C) The re-send was cut too. The provider is failing the same request
//	    twice in a row; retrying again would loop forever, so the turn
//	    fails, wrapping llm.ErrStreamTruncated.
//
// planCut is that table; runRound applies it.

// cutPlan is what runRound does with a truncated-stream chunk (035).
type cutPlan int

const (
	cutFail     cutPlan = iota // (C): fail the turn through l.fail
	cutRetry                   // (B): discard the round, re-send once
	cutContinue                // (A): end the round as if it had finished
)

// planCut classifies one truncated-stream chunk for a round in which
// toolCalled records whether a tool call was already dispatched and retried
// whether this round already consumed its one retry (035). The budget is
// per round, not per turn: each round's request is a fresh roll of the
// provider's dice.
func planCut(toolCalled, retried bool) cutPlan {
	switch {
	case toolCalled:
		return cutContinue
	case !retried:
		return cutRetry
	default:
		return cutFail
	}
}
