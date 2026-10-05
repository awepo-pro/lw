package agent

// repeatcall.go is 051's repeat-call guard for every turn.
//
// The measured failure (eval B49, v2.30.0): an ingest sent the identical
// wiki.search twice per round for rounds 4-20 and staged only at round 21 of 24,
// after 040's nudge; 74 of 845 calls in that run were exact duplicates, against
// 2 of 261 before 048's read budget pushed the crawl into wiki.search, which is
// not a budgeted read. A call whose identical twin already ran this turn, whose
// answer is still in the model's context and cannot have changed since, tells
// the model nothing. Prompt text does not bound this model (the 039-048 lesson),
// so the loop answers it with a refusal instead of a result.

import (
	"fmt"
	"strings"

	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/tools"
)

// repeatCallRefusalFmt is the refusal an identical repeat gets, with the
// canonical tool name and the round of the earlier call as its two verbs. It
// says the answer is above and unchanged — the reason the call was not run —
// and what to do instead, because a model told only "no" sent the same call
// again. The wording is a frozen contract: the bytes ride the wire and are
// pinned by repeatcall_test.go.
const repeatCallRefusalFmt = "%s refused: this exact call already ran in round %d of this turn and its result is still above, unchanged. Use that result, or call with different arguments."

// guardedReads are the tools whose identical repeat is refused, by canonical
// name: the reads. wiki.lint and every stage.* tool are untouched — a stage
// call is the model acting, not asking, and its second call is not a question
// already answered.
var guardedReads = map[string]bool{
	"wiki.search":    true,
	"wiki.get":       true,
	"wiki.neighbors": true,
	"wiki.backlinks": true,
	"raw.get":        true,
	"raw.list":       true,
	"vault.orient":   true,
	"web.search":     true,
}

// isStateChange reports whether canonical, when it succeeds, changes what a
// read may return: every stage.* call but open (starts or joins a changeset)
// and close (only summarizes it). It is wider than 048's isPageChange on
// purpose — stage.ingest_source puts a source in raw.list and raw.get's view
// without touching a wiki page — so it is a list of its own, and a stage tool
// added later counts as a change until it is named here.
func isStateChange(canonical string) bool {
	return strings.HasPrefix(canonical, "stage.") && canonical != "stage.open" && canonical != "stage.close"
}

// repeatRan is one earlier call the guard holds an answer for: the tool call id
// whose result message carries it, and the round it ran in.
type repeatRan struct {
	id    string
	round int
}

// repeatGuard is one Send's record of which guarded reads have an answer above.
// Like readBudget and 041's staged map it lives in the turn, never in the Loop,
// so a TUI Loop reused across turns starts every turn empty, and it applies to
// every verb. Dispatch is sequential — dispatchToolCall has one caller, runRound's
// stream loop — so plain maps passed by pointer need no mutex.
//
// A nil *repeatGuard is the guard of a turn that has none, and every method is a
// no-op on it, the way a nil *readBudget is the budget of every verb but ingest:
// a call site never has to ask. Send always makes one.
type repeatGuard struct {
	// ran maps a call's signature (callSignature, A-004-2's identity) to the
	// latest successful call with it whose result has not been elided and has not
	// been outlived by a state change.
	ran map[string]repeatRan

	// sig maps the id of every call in ran to its signature, for syncElided: an
	// elided result message names only its ToolCallID. An id is expected to be
	// unique within a turn; noteResult forgets the older call when one is not.
	sig map[string]string

	// synced remembers which message indices syncElided already processed, so
	// each elision is applied once, at the round it happened in.
	synced map[int]bool
}

// newRepeatGuard returns the empty guard a turn starts with.
func newRepeatGuard() *repeatGuard {
	return &repeatGuard{ran: map[string]repeatRan{}, sig: map[string]string{}, synced: map[int]bool{}}
}

// repeatSignature is the guard's identity for one call: callSignature of the
// arguments the dispatcher would run, so a call with no arguments is the same
// call however it is spelled — "" and whitespace are {} to the dispatcher
// (backbone §9 item 7), and so to the guard — while spacing inside the JSON is
// callSignature's own business (json.Compact).
func repeatSignature(callName, args string) string {
	args = strings.TrimSpace(args)
	if args == "" {
		args = "{}"
	}
	return callSignature(callName, args)
}

// refusal reports whether the call callName(args) is an identical repeat of a
// guarded read whose answer is still above, the round that earlier call ran in,
// and the text to answer the repeat with. A refused repeat is not recorded: the
// round named stays that of the call that actually ran.
func (g *repeatGuard) refusal(callName, args string) (string, int, bool) {
	if g == nil {
		return "", 0, false
	}
	canonical := tools.CanonicalName(callName)
	if !guardedReads[canonical] {
		return "", 0, false
	}
	prior, ok := g.ran[repeatSignature(callName, args)]
	if !ok {
		return "", 0, false
	}
	return fmt.Sprintf(repeatCallRefusalFmt, canonical, prior.round), prior.round, true
}

// noteResult records a call that reached the registry. A successful stage.*
// call that changes state clears everything, since a search or page read may now
// return something different; a successful guarded read becomes the call a
// repeat is measured against. A call that came back IsError is never recorded —
// it left the model with nothing worth keeping, so its identical retry is
// dispatched — and neither is any other tool.
func (g *repeatGuard) noteResult(callName, args, id string, round int, isError bool) {
	if g == nil || isError {
		return
	}
	canonical := tools.CanonicalName(callName)
	if isStateChange(canonical) {
		clear(g.ran)
		clear(g.sig)
		return
	}
	if !guardedReads[canonical] {
		return
	}
	sig := repeatSignature(callName, args)
	// A provider that reuses one tool call id across rounds leaves two result
	// messages the guard cannot tell apart by id, so the elision of either would
	// be attributed to whichever call named the id last, and the older call's
	// entry would outlive its elided result and refuse a read the model can no
	// longer see. Forgetting the older call here fails open: at worst a read that
	// is still above is served again.
	if old, ok := g.sig[id]; ok && old != sig {
		if prior, ok := g.ran[old]; ok && prior.id == id {
			delete(g.ran, old)
		}
	}
	g.ran[sig] = repeatRan{id: id, round: round}
	g.sig[id] = sig
}

// syncElided drops the answers boundContext has elided since the last call:
// "still above" is false for a result replaced by its placeholder, and A-004-2
// requires that an elided read may be made again. elided is the Send's map of
// message indices boundContext replaced, msgs the list it returned; only the
// turn's own messages (index turnStart on) are ever in it. A re-read after an
// elision is pinned by boundContext, so it stays above, and it is the call
// recorded here once it has run.
func (g *repeatGuard) syncElided(msgs []llm.Message, turnStart int, elided map[int]bool) {
	if g == nil || len(elided) == 0 {
		return
	}
	for i := turnStart; i < len(msgs); i++ {
		if !elided[i] || g.synced[i] {
			continue
		}
		g.synced[i] = true
		id := msgs[i].ToolCallID
		sig, ok := g.sig[id]
		if !ok {
			continue
		}
		if prior, ok := g.ran[sig]; ok && prior.id == id {
			delete(g.ran, sig)
		}
		delete(g.sig, id)
	}
}
