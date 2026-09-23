// dropop.go is workflow 030's review verbs: `d` drops one op together with
// everything that cannot survive without it (Engine.OpDependents/DropOps),
// `u` brings a dropped op back with the dropped ops it builds on
// (Engine.OpPrerequisites/RestoreOps). Split out of review.go (commit.go's
// file-split precedent) so review.go stays under conventions §2's ~400-line
// guideline. The two-press arm is the raw-only commit arm's mirror
// (commitArmedFor, 008 contract §5 / C-807): the first press warns and
// arms, any other key, a ShellKeyMsg, or a changeset swap disarms, and the
// second press acts — bound to the same changeset and op it previewed.
package review

import (
	"fmt"
	"log/slog"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// dropArm is the op-drop confirmation's arm: the changeset id and op id it
// was armed for, plus exactly the dependent ids the first d previewed — the
// second d drops exactly those ids, never a fresh recomputation, so what
// the reviewer approved is what the engine is asked to drop.
type dropArm struct {
	csID string
	opID string
	deps []string
}

// indexDerivedRefusal is S9's text: the derived index.md window belongs to
// the engine's derivation over the other ops, so no verb may drop or
// restore it.
const indexDerivedRefusal = "index.md is derived from the other ops — it cannot be dropped or restored"

// dropOpKey is `d`: the two-press drop of the cursor's op with its
// dependents. The first press warns with the dependent list and arms
// (S1/S2); the second, with no key in between and the same changeset open,
// drops exactly the previewed ids and says so (S3). A refused press — the
// derived index window (S9), an already-dropped op (S6) — issues no engine
// mutation.
func (m *Model) dropOpKey() (ui.Pane, tea.Cmd) {
	e := m.deps.Engine
	opID, _, ok := resolveCursor(m.diff, m.stops, m.cursor)
	if !ok {
		return m, nil
	}
	if refusal := m.dropRestoreRefusal(opID); refusal != "" {
		m.setStatus(ui.StatusWarn, refusal)
		return m, nil
	}
	op, ok := findOp(m.ops, opID)
	if !ok {
		return m, nil
	}
	if op.State == stage.StateDropped {
		m.setStatus(ui.StatusWarn, fmt.Sprintf("%s is already dropped · u restores", opID))
		return m, nil
	}
	c, err := e.Current()
	if err != nil {
		m.setStatus(ui.StatusWarn, fmt.Sprintf("drop op failed: %v", err))
		return m, nil
	}
	if m.dropArm.opID != "" {
		if m.dropArm.opID == opID && m.dropArm.csID == c.ID {
			return m.dropArmedDrop(e, c.ID)
		}
		m.dropArm = dropArm{} // armed for another op or changeset: re-arm below
	}
	deps, err := e.OpDependents(opID)
	if err != nil {
		m.setStatus(ui.StatusWarn, fmt.Sprintf("drop op failed: %v", err))
		return m, nil
	}
	m.dropArm = dropArm{csID: c.ID, opID: opID, deps: deps}
	slog.Info("review drop armed", "changeset", c.ID, "op", opID, "dependents", len(deps))
	m.setStatus(ui.StatusWarn, armDropStatus(opID, deps))
	return m, nil
}

// dropArmedDrop is the second d: Engine.DropOps over exactly the ids the
// first press previewed, then the S3 confirmation at StatusGood. The arm is
// consumed with its changeset either way — a failed drop must not leave a
// live confirmation behind.
func (m *Model) dropArmedDrop(e *stage.Engine, csID string) (ui.Pane, tea.Cmd) {
	opID := m.dropArm.opID
	n := len(m.dropArm.deps)
	ids := append([]string{opID}, m.dropArm.deps...)
	if err := e.DropOps(ids); err != nil {
		m.dropArm = dropArm{}
		m.setStatus(ui.StatusWarn, fmt.Sprintf("drop op failed: %v", err))
		return m, nil
	}
	m.dropArm = dropArm{} // the confirmation was consumed with its changeset
	slog.Info("review drop op", "changeset", csID, "op", opID, "dropped", len(ids))
	m.setStatus(ui.StatusGood, droppedOpStatus(opID, n))
	return m, tea.Batch(m.load(), stageChangedCmd(e))
}

// restoreOpKey is `u` (030, D-30A): the cursor's dropped op comes back with
// every dropped op it builds on — OpPrerequisites' ids ride along in the
// same RestoreOps call, whose refresh re-derives the chain's staleness —
// and the S4 confirmation names them. A live op is S5's refusal; the
// derived index window is S9's.
func (m *Model) restoreOpKey() (ui.Pane, tea.Cmd) {
	e := m.deps.Engine
	opID, _, ok := resolveCursor(m.diff, m.stops, m.cursor)
	if !ok {
		return m, nil
	}
	if refusal := m.dropRestoreRefusal(opID); refusal != "" {
		m.setStatus(ui.StatusWarn, refusal)
		return m, nil
	}
	op, ok := findOp(m.ops, opID)
	if !ok {
		return m, nil
	}
	if op.State != stage.StateDropped {
		m.setStatus(ui.StatusWarn, fmt.Sprintf("%s is not dropped", opID))
		return m, nil
	}
	prereqs, err := e.OpPrerequisites(opID)
	if err != nil {
		m.setStatus(ui.StatusWarn, fmt.Sprintf("restore op failed: %v", err))
		return m, nil
	}
	if err := e.RestoreOps(append([]string{opID}, prereqs...)); err != nil {
		m.setStatus(ui.StatusWarn, fmt.Sprintf("restore op failed: %v", err))
		return m, nil
	}
	slog.Info("review restore op", "op", opID, "prerequisites", len(prereqs))
	m.setStatus(ui.StatusGood, restoredOpStatus(opID, prereqs))
	return m, tea.Batch(m.load(), stageChangedCmd(e))
}

// dropRestoreRefusal names the reason d/u on the cursor's stop must not
// act, or "" when the key may proceed. d and u address whole OPS, so
// unlike reviewRefusal they require no hunk id — an op-level stop (a
// create, an ingest) is exactly what d exists to drop. The one refusal is
// the derived index.md window (S9).
func (m *Model) dropRestoreRefusal(opID string) string {
	if m.isDerivedIndexWindow(opID) {
		return indexDerivedRefusal
	}
	return ""
}

// isDerivedIndexWindow reports whether the op's whole OpDiff display is the
// derived index.md window: the op a create_page (the owner-of-record the
// engine's Diff attributes the derived line to — applyDerivedIndexDiff,
// internal/stage/diff.go) and at least one window, every window at Path
// "index.md". The kind test is load-bearing: a patch_page ON index.md is a
// real proposal (OQ-9 phase 2 put index.md on patchableRootFiles) whose
// windows are ownerless exactly like the derivation's, and a rename's
// cascade index sub-op is independently droppable (hunks.go) — S9 must
// forbid neither, or `d` could never drop an op the engine accepts. A
// create that merely CONTRIBUTES an index line also shows its own content
// window, so this stays false there too and d keeps working on the op
// itself — the create-only ownerless refusals stay S7's (A-030-2).
func (m *Model) isDerivedIndexWindow(opID string) bool {
	op, ok := findOp(m.ops, opID)
	if !ok || op.Kind != stage.OpCreatePage {
		return false
	}
	ws := m.opDiffs[opID]
	if len(ws) == 0 {
		return false
	}
	for _, w := range ws {
		if w.Path != "index.md" {
			return false
		}
	}
	return true
}

// armDropStatus is S1/S2, the first d's warning: the action leads (the
// footer clips status at w-10, internal/ui/frame.go:318 — A-029-2's rule),
// then the dependent count and the ids, joined with ", ".
func armDropStatus(opID string, deps []string) string {
	if len(deps) == 0 {
		return fmt.Sprintf("press d again to drop %s", opID)
	}
	if len(deps) == 1 {
		return fmt.Sprintf("press d again to drop %s + 1 dependent: %s", opID, deps[0])
	}
	return fmt.Sprintf("press d again to drop %s + %d dependents: %s",
		opID, len(deps), strings.Join(deps, ", "))
}

// droppedOpStatus is S3, the second d's confirmation at StatusGood.
func droppedOpStatus(opID string, n int) string {
	if n == 0 {
		return fmt.Sprintf("dropped %s · u restores", opID)
	}
	if n == 1 {
		return fmt.Sprintf("dropped %s + 1 dependent · u restores", opID)
	}
	return fmt.Sprintf("dropped %s + %d dependents · u restores", opID, n)
}

// restoredOpStatus is S4, u's confirmation at StatusGood.
func restoredOpStatus(opID string, prereqs []string) string {
	if len(prereqs) == 0 {
		return fmt.Sprintf("restored %s", opID)
	}
	if len(prereqs) == 1 {
		return fmt.Sprintf("restored %s + 1 prerequisite: %s", opID, prereqs[0])
	}
	return fmt.Sprintf("restored %s + %d prerequisites: %s",
		opID, len(prereqs), strings.Join(prereqs, ", "))
}
