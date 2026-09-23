// drop_op_test.go is 030 T2's review-side evidence: the `d` two-press drop
// of one op with everything that cannot survive without it (D-30A's undo is
// `u`), the exact status texts S1–S9, the arm rules (any other key, a
// ShellKeyMsg, or a changeset swap disarms), D-30B's hunk-refusal pointer
// to `d`, and the U5 end-to-end: the live 030 shape — one 112-char raw
// ingest plus six dependent patches — where d,d on the middle op commits
// clean on the first C. Every key goes through Model.Update against a real
// engine on the minimal fixture.
package review

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// appendCitingKvPatch appends one patch_page on the fixture's kv-cache page
// whose post-image newly cites the 112-char raw path through a "^[<path>]"
// body marker (030 D3) — the dependent op of the ingest_with_dependent
// shape.
func appendCitingKvPatch(t *testing.T, e *stage.Engine) string {
	t.Helper()
	page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
	if !ok {
		t.Fatal("fixture missing wiki/concepts/kv-cache.md")
	}
	const oldFlash = "- [[flash-attention]] — a kernel design that reduces the memory-bandwidth cost"
	newFlash := oldFlash + " ^[" + tilelangRawPath + "]"
	content := []byte(strings.Replace(string(page.Serialize()), oldFlash, newFlash, 1))
	id, err := e.Append(kvCachePatchOp(page.Path, page.SHA256(), content, oldFlash, newFlash))
	if err != nil {
		t.Fatalf("Append citing patch: %v", err)
	}
	return id
}

// appendChainedKvPatches appends n dependent patch_page ops on the fixture's
// kv-cache page, each chaining on the previous op's staged After (the 020
// dependent-op shape, commit_patch_test.go's idiom), and returns their ids
// plus each op's post-image bytes keyed by id — the bytes a commit that
// keeps op k must land on disk.
func appendChainedKvPatches(t *testing.T, e *stage.Engine, n int) ([]string, map[string][]byte) {
	t.Helper()
	const (
		kvCachePath = "wiki/concepts/kv-cache.md"
		oldFlash    = "- [[flash-attention]] — a kernel design that reduces the memory-bandwidth cost"
	)
	page, ok := e.Vault().Page(kvCachePath)
	if !ok {
		t.Fatalf("fixture missing %s", kvCachePath)
	}
	prevLine := oldFlash
	before := page.SHA256()
	base := string(page.Serialize())
	var ids []string
	contents := make(map[string][]byte, n)
	for i := 1; i <= n; i++ {
		newLine := fmt.Sprintf("%s, edit %d", oldFlash, i)
		content := []byte(strings.Replace(base, prevLine, newLine, 1))
		id, err := e.Append(kvCachePatchOp(kvCachePath, before, content, prevLine, newLine))
		if err != nil {
			t.Fatalf("Append chained patch %d: %v", i, err)
		}
		ids = append(ids, id)
		contents[id] = content
		op, found := mustOp(t, e, id)
		if !found {
			t.Fatalf("changeset lost %s", id)
		}
		before = op.After
		base = string(content)
		prevLine = newLine
	}
	return ids, contents
}

// moveCursorTo parks the review cursor on opID's first cursor stop — the
// j/k-walk position a reviewer reaches the op at.
func moveCursorTo(t *testing.T, m ui.Pane, opID string) {
	t.Helper()
	mm, ok := m.(*Model)
	if !ok {
		t.Fatalf("pane is %T, want *review.Model", m)
	}
	for i := range mm.stops {
		if id, _, ok := resolveCursor(mm.diff, mm.stops, i); ok && id == opID {
			mm.cursor = i
			return
		}
	}
	t.Fatalf("no cursor stop resolves to op %s (%d stops)", opID, len(mm.stops))
}

// engineState asserts opID's state in the engine's open changeset.
func engineState(t *testing.T, e *stage.Engine, opID string) stage.OpState {
	t.Helper()
	op, ok := mustOp(t, e, opID)
	if !ok {
		t.Fatalf("changeset lost %s", opID)
	}
	return op.State
}

// staleOps returns the ids of c's top-level stale ops.
func staleOps(t *testing.T, e *stage.Engine) []string {
	t.Helper()
	cs, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	var out []string
	for _, op := range cs.Ops {
		if op.State == stage.StateStale {
			out = append(out, op.ID)
		}
	}
	return out
}

func TestDropOpKey(t *testing.T) {
	t.Run("ingest_with_dependent", func(t *testing.T) {
		d, e, _ := newTestDeps(t, "minimal")
		if _, err := e.OpenChangeset("ingest plus citing patch", stage.Author{Kind: "agent", Model: "test"}); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		op1 := appendRawIngest(t, e, tilelangRawPath, "# TileLang\n\nA tiled programming model.\n")
		op2 := appendCitingKvPatch(t, e)
		m := initModel(t, d)
		moveCursorTo(t, m, op1)

		m = send(t, m, keyPress('d')) // first d: preview, arm

		want := fmt.Sprintf("press d again to drop %s + 1 dependent: %s", op1, op2)
		if msg, level := statusOf(t, m); msg != want || level != ui.StatusWarn {
			t.Errorf("Status after first d = (%q, %v), want (%q, StatusWarn)", msg, level, want)
		}
		if got := engineState(t, e, op1); got == stage.StateDropped {
			t.Error("op1 was dropped by the FIRST d — only the second may drop")
		}
		if got := engineState(t, e, op2); got == stage.StateDropped {
			t.Error("op2 was dropped by the FIRST d")
		}

		m = send(t, m, keyPress('d')) // second d: drop exactly the previewed ids

		want = fmt.Sprintf("dropped %s + 1 dependent · u restores", op1)
		if msg, level := statusOf(t, m); msg != want || level != ui.StatusGood {
			t.Errorf("Status after second d = (%q, %v), want (%q, StatusGood)", msg, level, want)
		}
		if got := engineState(t, e, op1); got != stage.StateDropped {
			t.Errorf("op1 state after second d = %s, want dropped", got)
		}
		if got := engineState(t, e, op2); got != stage.StateDropped {
			t.Errorf("op2 state after second d = %s, want dropped (the D3 dependent)", got)
		}
	})

	t.Run("no_dependents", func(t *testing.T) {
		d, e, _ := newTestDeps(t, "minimal")
		if _, err := e.OpenChangeset("one patch", stage.Author{Kind: "agent", Model: "test"}); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		id := appendTwoHunkPatch(t, e)
		m := initModel(t, d)
		moveCursorTo(t, m, id)

		m = send(t, m, keyPress('d'))

		want := fmt.Sprintf("press d again to drop %s", id)
		if msg, level := statusOf(t, m); msg != want || level != ui.StatusWarn {
			t.Errorf("Status after first d = (%q, %v), want (%q, StatusWarn)", msg, level, want)
		}

		m = send(t, m, keyPress('d'))

		want = fmt.Sprintf("dropped %s · u restores", id)
		if msg, level := statusOf(t, m); msg != want || level != ui.StatusGood {
			t.Errorf("Status after second d = (%q, %v), want (%q, StatusGood)", msg, level, want)
		}
		if got := engineState(t, e, id); got != stage.StateDropped {
			t.Errorf("%s state = %s, want dropped", id, got)
		}
		cs, err := e.Current()
		if err != nil {
			t.Fatalf("Current: %v", err)
		}
		if n := len(cs.Ops); n != 1 {
			t.Fatalf("changeset holds %d ops, want exactly the one that was dropped", n)
		}
	})

	t.Run("other_key_disarms", func(t *testing.T) {
		d, e, _ := newTestDeps(t, "minimal")
		if _, err := e.OpenChangeset("disarm", stage.Author{Kind: "agent", Model: "test"}); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		id := appendTwoHunkPatch(t, e)
		m := initModel(t, d)
		moveCursorTo(t, m, id)

		m = send(t, m, keyPress('d')) // arms
		m = send(t, m, keyPress('j')) // any other key disarms
		m = send(t, m, keyPress('d')) // warns again — the arm did not survive

		want := fmt.Sprintf("press d again to drop %s", id)
		if msg, level := statusOf(t, m); msg != want || level != ui.StatusWarn {
			t.Errorf("Status after d,j,d = (%q, %v), want the first-press warning (%q, StatusWarn)", msg, level, want)
		}
		if got := engineState(t, e, id); got == stage.StateDropped {
			t.Errorf("%s was dropped — j must disarm the arm between the two d presses", id)
		}
	})

	t.Run("u_restores_with_prerequisites", func(t *testing.T) {
		d, e, _ := newTestDeps(t, "minimal")
		if _, err := e.OpenChangeset("chain restore", stage.Author{Kind: "agent", Model: "test"}); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		ids, _ := appendChainedKvPatches(t, e, 3) // op1 -> op2 -> op3
		if err := e.DropOps(ids); err != nil {
			t.Fatalf("DropOps: %v", err)
		}
		m := initModel(t, d)
		moveCursorTo(t, m, ids[2])

		m = send(t, m, keyPress('u'))

		want := fmt.Sprintf("restored %s + 2 prerequisites: %s, %s", ids[2], ids[0], ids[1])
		if msg, level := statusOf(t, m); msg != want || level != ui.StatusGood {
			t.Errorf("Status after u = (%q, %v), want (%q, StatusGood)", msg, level, want)
		}
		for _, id := range ids {
			if got := engineState(t, e, id); got != stage.StateProposed {
				t.Errorf("%s state after u = %s, want proposed", id, got)
			}
		}
		if stale := staleOps(t, e); len(stale) != 0 {
			t.Errorf("stale ops after u = %v, want none — RestoreOps re-derives the chain fresh", stale)
		}
	})

	t.Run("u_zero_and_one_prerequisite", func(t *testing.T) {
		// S4's other two forms: a lone dropped op restores bare; the second
		// op of a chain restores with exactly its one prerequisite.
		t.Run("zero", func(t *testing.T) {
			d, e, _ := newTestDeps(t, "minimal")
			if _, err := e.OpenChangeset("u zero", stage.Author{Kind: "agent", Model: "test"}); err != nil {
				t.Fatalf("OpenChangeset: %v", err)
			}
			id := appendTwoHunkPatch(t, e)
			if err := e.DropOps([]string{id}); err != nil {
				t.Fatalf("DropOps: %v", err)
			}
			m := initModel(t, d)
			moveCursorTo(t, m, id)

			m = send(t, m, keyPress('u'))

			want := fmt.Sprintf("restored %s", id)
			if msg, level := statusOf(t, m); msg != want || level != ui.StatusGood {
				t.Errorf("Status after u = (%q, %v), want (%q, StatusGood)", msg, level, want)
			}
			if got := engineState(t, e, id); got != stage.StateProposed {
				t.Errorf("%s state = %s, want proposed", id, got)
			}
		})

		t.Run("one", func(t *testing.T) {
			d, e, _ := newTestDeps(t, "minimal")
			if _, err := e.OpenChangeset("u one", stage.Author{Kind: "agent", Model: "test"}); err != nil {
				t.Fatalf("OpenChangeset: %v", err)
			}
			ids, _ := appendChainedKvPatches(t, e, 2) // op1 -> op2
			if err := e.DropOps(ids); err != nil {
				t.Fatalf("DropOps: %v", err)
			}
			m := initModel(t, d)
			moveCursorTo(t, m, ids[1])

			m = send(t, m, keyPress('u'))

			want := fmt.Sprintf("restored %s + 1 prerequisite: %s", ids[1], ids[0])
			if msg, level := statusOf(t, m); msg != want || level != ui.StatusGood {
				t.Errorf("Status after u = (%q, %v), want (%q, StatusGood)", msg, level, want)
			}
			for _, id := range ids {
				if got := engineState(t, e, id); got != stage.StateProposed {
					t.Errorf("%s state = %s, want proposed", id, got)
				}
			}
		})
	})

	t.Run("u_on_live", func(t *testing.T) {
		d, e, _ := newTestDeps(t, "minimal")
		if _, err := e.OpenChangeset("u on live", stage.Author{Kind: "agent", Model: "test"}); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		id := appendTwoHunkPatch(t, e)
		m := initModel(t, d)
		moveCursorTo(t, m, id)

		m = send(t, m, keyPress('u'))

		want := fmt.Sprintf("%s is not dropped", id)
		if msg, level := statusOf(t, m); msg != want || level != ui.StatusWarn {
			t.Errorf("Status after u = (%q, %v), want (%q, StatusWarn)", msg, level, want)
		}
		if got := engineState(t, e, id); got == stage.StateDropped {
			t.Errorf("%s was dropped by u", id)
		}
	})

	t.Run("d_on_dropped", func(t *testing.T) {
		d, e, _ := newTestDeps(t, "minimal")
		if _, err := e.OpenChangeset("d on dropped", stage.Author{Kind: "agent", Model: "test"}); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		id := appendTwoHunkPatch(t, e)
		if err := e.DropOps([]string{id}); err != nil {
			t.Fatalf("DropOps: %v", err)
		}
		m := initModel(t, d)
		moveCursorTo(t, m, id)

		m = send(t, m, keyPress('d'))

		want := fmt.Sprintf("%s is already dropped · u restores", id)
		if msg, level := statusOf(t, m); msg != want || level != ui.StatusWarn {
			t.Errorf("Status after d = (%q, %v), want (%q, StatusWarn)", msg, level, want)
		}
		if got := engineState(t, e, id); got != stage.StateDropped {
			t.Errorf("%s state after the refused d = %s, want still dropped", id, got)
		}
	})

	t.Run("ownerless_pointer", func(t *testing.T) {
		d, e, _ := newTestDeps(t, "minimal")
		if _, err := e.OpenChangeset("ownerless ingest", stage.Author{Kind: "agent", Model: "test"}); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		op1 := appendRawIngest(t, e, "raw/articles/agent-memory.md", "# Agent Memory\n\nNotes outlive the session.\n")
		m := initModel(t, d)
		moveCursorTo(t, m, op1)

		m = send(t, m, keyPress('n'))

		want := fmt.Sprintf("this window has no hunk id — press d to drop the whole op (%s)", op1)
		if msg, level := statusOf(t, m); msg != want || level != ui.StatusWarn {
			t.Errorf("Status after n = (%q, %v), want (%q, StatusWarn)", msg, level, want)
		}
		if got := engineState(t, e, op1); got == stage.StateDropped {
			t.Errorf("%s was dropped by n — the ingest window has no hunk to drop", op1)
		}
	})

	t.Run("chain_hunk_refused", func(t *testing.T) {
		d, e, _ := newTestDeps(t, "minimal")
		if _, err := e.OpenChangeset("d-30b", stage.Author{Kind: "agent", Model: "test"}); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		ids, _ := appendChainedKvPatches(t, e, 3) // op1 -> op2 -> op3
		m := initModel(t, d)

		moveCursorTo(t, m, ids[0])
		m = send(t, m, keyPress('n'))

		want := fmt.Sprintf("%s cannot lose a hunk: %s, %s build on it — press d to drop them together",
			ids[0], ids[1], ids[2])
		if msg, level := statusOf(t, m); msg != want || level != ui.StatusWarn {
			t.Errorf("Status after n on the chain head = (%q, %v), want (%q, StatusWarn)", msg, level, want)
		}
		if got := engineState(t, e, ids[0]); got == stage.StateDropped {
			t.Errorf("%s was dropped by n — D-30B must refuse and point to d", ids[0])
		}
		if op, ok := mustOp(t, e, ids[0]); ok && op.Hunks[0].Dropped {
			t.Errorf("%s h1 was dropped by the refused n", ids[0])
		}

		// The last op in the chain has no dependents: D-30B must NOT fire
		// for it. What the key meets instead is the shipped OpDiff
		// attribution rule (internal/stage/opdiff.go:318 — a chained
		// patch's Before is the previous op's After, never the working
		// tree, so its windows carry no HunkID): the ownerless refusal,
		// pointing at d — never the D-30B text.
		moveCursorTo(t, m, ids[2])
		m = send(t, m, keyPress('n'))

		want = fmt.Sprintf("this window has no hunk id — press d to drop the whole op (%s)", ids[2])
		if msg, level := statusOf(t, m); msg != want || level != ui.StatusWarn {
			t.Errorf("Status after n on the chain tail = (%q, %v), want the ownerless pointer (%q, StatusWarn) — D-30B must not fire on the tail", msg, level, want)
		}
		if op, ok := mustOp(t, e, ids[2]); !ok {
			t.Fatalf("changeset lost %s", ids[2])
		} else if op.Hunks[0].Dropped {
			t.Errorf("%s h1 was dropped by the ownerless-refused n", ids[2])
		}

		// "still drops it": d is how the tail goes. No dependents — a
		// second d drops exactly the tail, nothing else.
		moveCursorTo(t, m, ids[2])
		m = send(t, m, keyPress('d'))
		m = send(t, m, keyPress('d'))
		if got := engineState(t, e, ids[2]); got != stage.StateDropped {
			t.Errorf("%s state after d,d = %s, want dropped — the chain tail is unaffected by D-30B", ids[2], got)
		}
		if got := engineState(t, e, ids[0]); got == stage.StateDropped {
			t.Errorf("%s was dropped too — only the tail may go", ids[0])
		}
	})

	t.Run("index_patch_is_droppable", func(t *testing.T) {
		// index.md is on patchableRootFiles (OQ-9 phase 2): a patch_page
		// against it is a REAL proposal whose windows are ownerless exactly
		// like the derivation's. S9 must not fire on it — n points at d
		// (S7), and d,d drops the op, u brings it back. Without the op-kind
		// discriminator in isDerivedIndexWindow this shape is un-reviewable:
		// every key answers "derived from the other ops", which is false.
		d, e, _ := newTestDeps(t, "minimal")
		if _, err := e.OpenChangeset("index patch", stage.Author{Kind: "agent", Model: "test"}); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		orig, err := e.Vault().Read("index.md")
		if err != nil {
			t.Fatalf("read index.md: %v", err)
		}
		sum := sha256.Sum256(orig)
		const oldLine = "- [[kv-cache]]"
		if !strings.Contains(string(orig), oldLine) {
			t.Fatalf("fixture index.md lacks %q:\n%s", oldLine, orig)
		}
		content := []byte(strings.Replace(string(orig), oldLine, oldLine+" (annotated)", 1))
		id, err := e.Append(stage.Op{
			Kind:    stage.OpPatchPage,
			Path:    "index.md",
			Before:  hex.EncodeToString(sum[:]),
			Content: content,
		})
		if err != nil {
			t.Fatalf("Append index.md patch: %v", err)
		}
		m := initModel(t, d)
		moveCursorTo(t, m, id)

		m = send(t, m, keyPress('n'))
		want := fmt.Sprintf("this window has no hunk id — press d to drop the whole op (%s)", id)
		if msg, level := statusOf(t, m); msg != want || level != ui.StatusWarn {
			t.Errorf("Status after n on the index patch = (%q, %v), want the S7 pointer (%q, StatusWarn) — not S9", msg, level, want)
		}

		m = send(t, m, keyPress('d'))
		want = fmt.Sprintf("press d again to drop %s", id)
		if msg, level := statusOf(t, m); msg != want || level != ui.StatusWarn {
			t.Errorf("Status after first d = (%q, %v), want (%q, StatusWarn) — S9 must not refuse a real index.md patch", msg, level, want)
		}
		m = send(t, m, keyPress('d'))
		want = fmt.Sprintf("dropped %s · u restores", id)
		if msg, level := statusOf(t, m); msg != want || level != ui.StatusGood {
			t.Errorf("Status after second d = (%q, %v), want (%q, StatusGood)", msg, level, want)
		}
		if got := engineState(t, e, id); got != stage.StateDropped {
			t.Errorf("%s state after d,d = %s, want dropped", id, got)
		}

		m = send(t, m, keyPress('u'))
		want = fmt.Sprintf("restored %s", id)
		if msg, level := statusOf(t, m); msg != want || level != ui.StatusGood {
			t.Errorf("Status after u = (%q, %v), want (%q, StatusGood)", msg, level, want)
		}
		if got := engineState(t, e, id); got != stage.StateProposed {
			t.Errorf("%s state after u = %s, want proposed", id, got)
		}
	})

	t.Run("commit_and_drop_arms_never_coexist", func(t *testing.T) {
		// The two confirmation arms are mutually exclusive (030): d clears
		// the commit arm and C clears the drop arm, so no second press can
		// act on a confirmation the reviewer can no longer see.
		d, e, root := newTestDeps(t, "minimal")
		if _, err := e.OpenChangeset("raw only", stage.Author{Kind: "agent", Model: "test"}); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		ing := appendRawIngest(t, e, "raw/articles/agent-memory.md", "# Agent Memory\n\nNotes outlive the session.\n")
		m := initModel(t, d)
		moveCursorTo(t, m, ing)
		wantC := "press C again to commit — raw source(s) only, no page changes: raw/articles/agent-memory.md"

		m = send(t, m, keyPress('C')) // arms the raw-only commit confirmation
		if msg, level := statusOf(t, m); msg != wantC || level != ui.StatusWarn {
			t.Fatalf("setup: first C = (%q, %v), want the raw-only warning", msg, level)
		}

		m = send(t, m, keyPress('d')) // must kill the commit arm and arm the drop arm
		want := fmt.Sprintf("press d again to drop %s", ing)
		if msg, level := statusOf(t, m); msg != want || level != ui.StatusWarn {
			t.Fatalf("Status after C,d = (%q, %v), want the S2 preview (%q, StatusWarn)", msg, level, want)
		}

		m = send(t, m, keyPress('C')) // must warn raw-only AGAIN — the commit arm died with d
		if msg, level := statusOf(t, m); msg != wantC || level != ui.StatusWarn {
			t.Fatalf("Status after C,d,C = (%q, %v), want the raw-only warning again — d must disarm the commit arm", msg, level)
		}
		if journalHasCommitBegin(t, root) {
			t.Fatal("C,d,C committed — d did not disarm the commit confirmation")
		}

		m = send(t, m, keyPress('d')) // must warn S2 again — the drop arm died with the C press
		want = fmt.Sprintf("press d again to drop %s", ing)
		if msg, level := statusOf(t, m); msg != want || level != ui.StatusWarn {
			t.Errorf("Status after C,d,C,d = (%q, %v), want the S2 preview again — C must disarm the drop arm", msg, level)
		}
		if got := engineState(t, e, ing); got == stage.StateDropped {
			t.Errorf("%s was dropped — the inter-leaved C,d presses must not act", ing)
		}
	})

	t.Run("shell_key_disarms", func(t *testing.T) {
		// The drop arm dies on a key the SHELL consumed (ui.ShellKeyMsg —
		// tab's screen switch, the ? overlay), the same rule the raw-only
		// commit arm has held since 008 A-801.
		d, e, _ := newTestDeps(t, "minimal")
		if _, err := e.OpenChangeset("shell key", stage.Author{Kind: "agent", Model: "test"}); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		id := appendTwoHunkPatch(t, e)
		m := initModel(t, d)
		moveCursorTo(t, m, id)

		m = send(t, m, keyPress('d')) // arms
		m = send(t, m, ui.ShellKeyMsg{})
		m = send(t, m, keyPress('d')) // warns again — the arm did not survive

		want := fmt.Sprintf("press d again to drop %s", id)
		if msg, level := statusOf(t, m); msg != want || level != ui.StatusWarn {
			t.Errorf("Status after d,ShellKeyMsg,d = (%q, %v), want the S2 preview (%q, StatusWarn)", msg, level, want)
		}
		if got := engineState(t, e, id); got == stage.StateDropped {
			t.Errorf("%s was dropped — a ShellKeyMsg must disarm the drop arm", id)
		}
	})

	t.Run("u5_end_to_end", func(t *testing.T) {
		d, e, root := newTestDeps(t, "minimal")
		if _, err := e.OpenChangeset("030 u5", stage.Author{Kind: "agent", Model: "test"}); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		ing := appendRawIngest(t, e, tilelangRawPath, "# TileLang\n\nA tiled programming model.\n")
		patches, contents := appendChainedKvPatches(t, e, 6) // op2..op7
		m := initModel(t, d)
		moveCursorTo(t, m, patches[2]) // op4

		m = send(t, m, keyPress('d'))
		want := fmt.Sprintf("press d again to drop %s + 3 dependents: %s, %s, %s",
			patches[2], patches[3], patches[4], patches[5])
		if msg, level := statusOf(t, m); msg != want || level != ui.StatusWarn {
			t.Fatalf("Status after first d = (%q, %v), want (%q, StatusWarn)", msg, level, want)
		}

		m = send(t, m, keyPress('d'))
		want = fmt.Sprintf("dropped %s + 3 dependents · u restores", patches[2])
		if msg, level := statusOf(t, m); msg != want || level != ui.StatusGood {
			t.Fatalf("Status after second d = (%q, %v), want (%q, StatusGood)", msg, level, want)
		}
		for _, id := range patches[2:] {
			if got := engineState(t, e, id); got != stage.StateDropped {
				t.Errorf("%s state = %s, want dropped", id, got)
			}
		}
		for _, id := range patches[:2] {
			if got := engineState(t, e, id); got == stage.StateDropped {
				t.Errorf("%s was dropped — the kept head must stay live", id)
			}
		}
		if got := engineState(t, e, ing); got == stage.StateDropped {
			t.Errorf("%s (the ingest) was dropped — the second d must drop EXACTLY the previewed ids", ing)
		}
		if stale := staleOps(t, e); len(stale) != 0 {
			t.Fatalf("stale ops after the drop = %v, want none", stale)
		}

		m = send(t, m, keyPress('C')) // the FIRST C: patches remain, so page work commits

		if msg, level := statusOf(t, m); !strings.HasPrefix(msg, "committed ") || level != ui.StatusGood {
			t.Errorf("Status after C = (%q, %v), want \"committed <id>\" at StatusGood on the first C", msg, level)
		}
		if !journalHasCommitBegin(t, root) {
			t.Fatal("no commit_begin in the journal — a stale op refused the commit")
		}

		onDisk, err := os.ReadFile(filepath.Join(root, filepath.FromSlash("wiki/concepts/kv-cache.md")))
		if err != nil {
			t.Fatalf("read committed page: %v", err)
		}
		if string(onDisk) != string(contents[patches[1]]) {
			t.Errorf("committed page is not op3's post-image:\n%s\nwant:\n%s", onDisk, contents[patches[1]])
		}
	})
}

// TestDerivedIndexWindowRefusal pins S9: on the derived index.md window —
// the one window an op shows that no op owns, the line every create
// derives — y/n/d/u all refuse with the derived-index text and nothing is
// dropped or restored (the pane is built by hand like stale_test.go's, so
// the refusal is provable with no engine behind it: no engine call may
// happen, and there is none to happen).
func TestDerivedIndexWindowRefusal(t *testing.T) {
	ops := []stage.Op{{ID: "op1", Kind: stage.OpCreatePage, State: stage.StateProposed}}
	d := stage.Diff{Files: []stage.FileDiff{{OpID: "op1", Hunks: []stage.Hunk{{ID: "h1"}}}}}
	m := &Model{
		deps:         ui.Deps{Theme: testTheme(t), Keys: defaultTestKeys(t)}, // no engine
		theme:        testTheme(t),
		hasChangeset: true,
		changeset:    &stage.Changeset{ID: "cs-derived"},
		ops:          ops,
		diff:         d,
		stops:        buildCursorStops(d, ops),
		opDiffs: map[string][]stage.FileOpDiff{
			"op1": {{Path: "index.md", Hunks: []stage.DisplayHunk{{HunkID: "", Lines: []stage.DisplayLine{{Kind: '+', Text: "- [[derived-line]]"}}}}}},
		},
	}

	const want = "index.md is derived from the other ops — it cannot be dropped or restored"
	var p ui.Pane = m
	for _, key := range []rune{'y', 'n', 'd', 'u'} {
		p = send(t, p, keyPress(key))
		if msg, level := statusOf(t, p); msg != want || level != ui.StatusWarn {
			t.Errorf("after %q: Status = (%q, %v), want (%q, StatusWarn)", string(key), msg, level, want)
		}
	}
}
