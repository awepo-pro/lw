// chained_staleness_test.go pins workflow 020 T-A's staleness half: a
// chained op's anchor is the CHAIN BASE — the projection of its live
// predecessors — not the bare working tree, so an intact chain stays fresh
// through unrelated edits while dropping or hand-editing the head flips the
// dependent ops StateStale exactly when their before-image stops being what
// Commit would write.
//
// Split from chained_ops_test.go (Append/compose) by theme; both files
// share that file's helpers and its fail-first provenance — see
// runs/T-A/report.md for the captured red state.
package stage

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDropOpFlipsChainedDependentStale covers goal 3: DropOp must flip a
// chained dependent StateStale IMMEDIATELY, in the same verb, with no
// Refresh call. The dependent's Before is the dropped op's After — a sha
// nothing will ever produce again once the op is gone — so leaving it
// looking fresh would let a commit silently apply a patch computed over a
// rewrite the reviewer just removed. Pre-020, persistAfterMutation only
// re-checked cascade sub-ops, so a dependent TOP-LEVEL op kept StateProposed
// until some later Refresh happened to run.
func TestDropOpFlipsChainedDependentStale(t *testing.T) {
	e, _ := newChainedEngine(t)
	if _, err := e.OpenChangeset("drop the head", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	op1, op2 := appendChainedPair(t, e)

	if err := e.DropOp(op1); err != nil {
		t.Fatalf("DropOp(%s): %v", op1, err)
	}

	c, err := e.Current()
	if err != nil {
		t.Fatalf("Current after DropOp: %v", err)
	}
	dependent, ok := c.Op(op2)
	if !ok {
		t.Fatalf("changeset lost %s", op2)
	}
	if dependent.State != StateStale {
		t.Fatalf("dependent %s state after dropping its head = %q; want %q with no Refresh call",
			op2, dependent.State, StateStale)
	}
}

// TestRefreshKeepsIntactChainFresh covers goal 2's two halves.
//
// First: an external edit to a file NO op touches must not stale either
// chained op. Pre-020, refreshOp anchored EVERY top-level patch_page on the
// working tree, so patch2 — whose Before is a staged sha — read as stale on
// the first Refresh and every commit of a chained changeset was refused
// with ErrStale.
//
// Second: editing the chained path itself on disk flips the chain HEAD
// (patch1, whose Before is the committed sha) stale. The dependent keeps
// its recorded state — the head is still live and still projects its
// post-image, so the dependent's own anchor still matches; the stale head
// is the reviewer's signal, the same semantics a stale cascade base already
// has (OR-13). Dropping the head is what flips the dependent (previous
// test).
func TestRefreshKeepsIntactChainFresh(t *testing.T) {
	e, dir := newChainedEngine(t)
	if _, err := e.OpenChangeset("refresh anchors", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	op1, op2 := appendChainedPair(t, e)

	// External edit to an unrelated page. Refresh re-hashes against
	// whatever e.vault already holds (apply_test.go's hand-edit precedent:
	// a real lw process reopens the engine, so the test reloads itself).
	unrelated := filepath.Join(dir, "wiki", "entities", "gpt-4.md")
	raw, err := os.ReadFile(unrelated)
	if err != nil {
		t.Fatalf("read gpt-4.md: %v", err)
	}
	if err := os.WriteFile(unrelated, append(raw, []byte("\nHand note.\n")...), 0o644); err != nil {
		t.Fatalf("edit gpt-4.md: %v", err)
	}
	if err := e.Vault().Reload(); err != nil {
		t.Fatalf("reload vault: %v", err)
	}
	if err := e.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	c, err := e.Current()
	if err != nil {
		t.Fatalf("Current after Refresh: %v", err)
	}
	for _, id := range []string{op1, op2} {
		op, _ := c.Op(id)
		if op.State == StateStale {
			t.Fatalf("%s went stale on an edit to a file no op touches (state %q)", id, op.State)
		}
	}

	// Now edit the chained path itself.
	target := filepath.Join(dir, filepath.FromSlash(chainedTargetPath))
	seed, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read %s: %v", chainedTargetPath, err)
	}
	if err := os.WriteFile(target, append(seed, []byte("\nHand-edited under the chain.\n")...), 0o644); err != nil {
		t.Fatalf("edit %s: %v", chainedTargetPath, err)
	}
	if err := e.Vault().Reload(); err != nil {
		t.Fatalf("reload vault: %v", err)
	}
	if err := e.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	c, err = e.Current()
	if err != nil {
		t.Fatalf("Current after second Refresh: %v", err)
	}
	head, _ := c.Op(op1)
	if head.State != StateStale {
		t.Fatalf("chain head %s state after the disk edit = %q; want %q", op1, head.State, StateStale)
	}
}
