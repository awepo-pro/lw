package tools

import "sync"

// shrinkArms is the set of pending 043 shrink confirmations of one registry,
// keyed by shrinkArmKey (path+section): a key is armed by a stage.patch_page
// refusal that measured a shrink, and consumed by the flagged repeat.
//
// 063 moved it out of stagePatchPageTool's closure onto the registry's copy of
// Deps (like 040's read log) for one reason: Registry.Replay has to reach it. An
// external agent drives ingest one `lw tool` process per call, so the registry
// that armed a key is gone before the call that confirms it, and without Replay
// an allow_shrink call from a fresh process could never succeed — every call
// would re-refuse and re-arm. Replay is the only writer besides the tool
// itself; a registry that never calls it behaves exactly as 043 specified, and
// 043's "an arm must not survive a restart" still holds for every one of them
// (A-063-1).
//
// NewRegistry makes a fresh set per registry, so two registries over the same
// engine never share an arm. mu covers MCP, where tool calls arrive on many
// goroutines: confirm tests and consumes under one lock (043 T2r), because a
// test-then-delete across the lock would let two concurrent flagged calls both
// read the one arm and both apply.
type shrinkArms struct {
	mu    sync.Mutex
	armed map[string]bool
}

func newShrinkArms() *shrinkArms { return &shrinkArms{armed: map[string]bool{}} }

// confirm reports whether a call carrying allow == its allow_shrink may apply a
// shrink under key — the flag is set AND a prior refusal armed the key — and
// consumes the arm when it does, atomically with the test.
func (a *shrinkArms) confirm(key string, allow bool) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	ok := allow && a.armed[key]
	if ok {
		delete(a.armed, key)
	}
	return ok
}

// arm marks key as awaiting a confirmation: a shrink refusal made it, or a
// consumed arm is put back because the call it was spent on did not stage.
func (a *shrinkArms) arm(key string) {
	a.mu.Lock()
	a.armed[key] = true
	a.mu.Unlock()
}

// disarm forgets key: any staged edit of the path+section ends the arm (043 T3).
func (a *shrinkArms) disarm(key string) {
	a.mu.Lock()
	delete(a.armed, key)
	a.mu.Unlock()
}
