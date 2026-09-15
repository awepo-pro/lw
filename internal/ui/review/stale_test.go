// stale_test.go is the ORCH-9 key-refusal evidence (s2-screens.md T06
// keys, MASTER §8): `y`/`n`/`A` are refused on a stale op with the frozen
// warn text and no engine call, and an ownerless window (HunkID "") is
// never a y/n target.
package review

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// TestStaleOpRefusesReviewKeys is s2-screens.md T06's frozen refusal
// (MASTER §8 ORCH-9): `y`, `n` and `A` on a stale op set the warn status
// `op <id> is stale: refresh before reviewing its hunks` and call NO
// engine method — a stale op's OpDiff windows carry HunkID "" (contract
// §1 note 4), so a key here would act on content the reviewer cannot see
// as attributed, and DropHunk/UndropHunk have no stale guard to catch it.
// Each key's refusal is proven against engine state the forbidden call
// would have changed.
func TestStaleOpRefusesReviewKeys(t *testing.T) {
	d, e, root := newTestDeps(t, "minimal")

	if _, err := e.OpenChangeset("goes stale under review", stage.Author{Kind: "agent", Model: "test"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	opID := appendTwoHunkPatch(t, e)
	if err := e.DropHunk(opID, "h1"); err != nil {
		t.Fatalf("DropHunk: %v", err)
	}

	// Race: rewrite the target page out from under the open changeset —
	// a patch_page goes stale when the path's current sha no longer matches
	// Before (backbone §5.4).
	full := filepath.Join(root, filepath.FromSlash("wiki/concepts/kv-cache.md"))
	body, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("read raced page: %v", err)
	}
	if err := os.WriteFile(full, append(body, []byte("\n<!-- raced under the changeset -->\n")...), 0o644); err != nil {
		t.Fatalf("write raced page: %v", err)
	}
	if err := e.Vault().Reload(); err != nil {
		t.Fatalf("Vault.Reload: %v", err)
	}
	if err := e.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	m := initModel(t, d)
	staleMsg := fmt.Sprintf("op %s is stale: refresh before reviewing its hunks", opID)

	// `y` is refused: h1 stays dropped, which UndropHunk would have undone.
	m = send(t, m, keyPress('y'))
	if msg, level := statusOf(t, m); msg != staleMsg || level != ui.StatusWarn {
		t.Errorf("after y: Status = (%q, %v), want (%q, StatusWarn)", msg, level, staleMsg)
	}
	if op, ok := mustOp(t, e, opID); !ok || !op.Hunks[0].Dropped {
		t.Error("y on a stale op undropped its hunk: an engine method ran")
	}

	// `n` is refused: h2 stays live, which DropHunk would have dropped.
	m = send(t, m, keyPress('n'))
	if msg, level := statusOf(t, m); msg != staleMsg || level != ui.StatusWarn {
		t.Errorf("after n: Status = (%q, %v), want (%q, StatusWarn)", msg, level, staleMsg)
	}
	if op, ok := mustOp(t, e, opID); !ok || op.Hunks[1].Dropped {
		t.Error("n on a stale op dropped its hunk: an engine method ran")
	}

	// `A` is refused before any undrop: h1 stays dropped.
	m = send(t, m, keyPress('A'))
	if msg, level := statusOf(t, m); msg != staleMsg || level != ui.StatusWarn {
		t.Errorf("after A: Status = (%q, %v), want (%q, StatusWarn)", msg, level, staleMsg)
	}
	if op, ok := mustOp(t, e, opID); !ok || !op.Hunks[0].Dropped {
		t.Error("A on a changeset with a stale op undropped a hunk: an engine method ran")
	}

	if _, err := e.Current(); err != nil {
		t.Errorf("Current after the refusals: %v (the changeset should still be open)", err)
	}
}

// TestOwnerlessWindowIsNeverYNCursorTarget pins s2-screens.md T06's
// second key rule: when the window under the cursor carries no HunkID —
// a create, an ingest, a derived index.md, or any window whose ownership
// cannot be proven (contract §1 note 4) — `y` and `n` refuse instead of
// calling the engine. The model is hand-built with a nil engine
// deliberately: proceeding past the refusal would nil-panic on the engine
// call, so the assertion is the proof that no engine method ran.
func TestOwnerlessWindowIsNeverYNCursorTarget(t *testing.T) {
	m := &Model{
		deps:         ui.Deps{Theme: testTheme(t), Keys: defaultTestKeys(t)}, // no engine
		theme:        testTheme(t),
		hasChangeset: true,
		changeset:    &stage.Changeset{ID: "cs-ownerless"},
		ops:          []stage.Op{{ID: "op1", Kind: stage.OpCreatePage, State: stage.StateProposed}},
		// The cursor walk has a stop on the op's file hunk...
		diff:  stage.Diff{Files: []stage.FileDiff{{OpID: "op1", Hunks: []stage.Hunk{{ID: "h1"}}}}},
		stops: buildCursorStops(stage.Diff{Files: []stage.FileDiff{{OpID: "op1", Hunks: []stage.Hunk{{ID: "h1"}}}}}),
		// ...but the displayed windows are ownerless, as OpDiff shows them
		// for an op that persists no hunks.
		opDiffs: map[string][]stage.FileOpDiff{
			"op1": {{Path: "wiki/concepts/ownerless.md", Hunks: []stage.DisplayHunk{{HunkID: "", Lines: []stage.DisplayLine{{Kind: '+', Text: "whole file"}}}}}},
		},
	}

	const want = "this window has no hunk id — it cannot be accepted or dropped individually"

	p := send(t, m, keyPress('y'))
	if msg, level := statusOf(t, p); msg != want || level != ui.StatusWarn {
		t.Errorf("after y: Status = (%q, %v), want (%q, StatusWarn)", msg, level, want)
	}
	p = send(t, p, keyPress('n'))
	if msg, level := statusOf(t, p); msg != want || level != ui.StatusWarn {
		t.Errorf("after n: Status = (%q, %v), want (%q, StatusWarn)", msg, level, want)
	}
}
