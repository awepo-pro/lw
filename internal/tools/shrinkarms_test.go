package tools

// shrinkarms_test.go pins the arm set 063 moved from stage.patch_page's closure
// onto the registry (shrinkArms): the same 043 rules at the unit level, and the
// atomic consume that keeps concurrent flagged calls to one winner (043 T2r).
// The registry-level behaviour is pinned by patch_guard_test.go, unchanged.
// Permanent regression test (D-10C).

import (
	"sync"
	"testing"
)

func TestShrinkArmsRules(t *testing.T) {
	a := newShrinkArms()
	const k = "wiki/x.md\n## S"

	if a.confirm(k, true) {
		t.Error("an unarmed key was confirmed by the flag alone")
	}
	a.arm(k)
	if a.confirm(k, false) {
		t.Error("an armed key was confirmed by a call without the flag")
	}
	if !a.confirm(k, true) {
		t.Error("an armed key was not confirmed by the flag")
	}
	if a.confirm(k, true) {
		t.Error("a confirmation was consumable twice")
	}

	a.arm(k)
	a.arm(wikiOtherKey)
	a.disarm(k)
	if a.confirm(k, true) {
		t.Error("a disarmed key was confirmed")
	}
	if !a.confirm(wikiOtherKey, true) {
		t.Error("disarming one key disarmed another")
	}
}

const wikiOtherKey = "wiki/y.md\n## T"

// TestShrinkArmsOneConsumer: many goroutines race to confirm the one arm and
// exactly one wins. Under -race this also proves the set is guarded.
func TestShrinkArmsOneConsumer(t *testing.T) {
	a := newShrinkArms()
	const k = "wiki/x.md\n## S"
	a.arm(k)
	const n = 32
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins int
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if a.confirm(k, true) {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d of %d racing confirmations won the one arm, want exactly 1", wins, n)
	}
}
