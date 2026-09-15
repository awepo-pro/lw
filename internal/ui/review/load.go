// load.go is the review screen's load round trip: one tea.Cmd that reads
// the open changeset, its Diff and — per op, for the Detail panel — its
// OpDiff display windows, and the applyLoaded step that installs the
// result on the model. All engine reads happen here, inside the command,
// never in View (backbone §12; the pane's View is a pure function of the
// model).
package review

import (
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// loadedMsg carries the result of loading the open changeset: the
// changeset itself, its Diff (the cursor's walk order and y/n targets)
// and every op's OpDiff windows (what the Detail panel displays,
// contract §1). Unexported: nothing outside this package's Update ever
// sees one.
type loadedMsg struct {
	changeset *stage.Changeset
	diff      stage.Diff
	opDiffs   map[string][]stage.FileOpDiff
	err       error
}

// loadCmd returns a tea.Cmd that loads e's open changeset, Diff and
// per-op OpDiff windows. A nil e (a headless pane built with no engine at
// all) reports stage.ErrNoChangeset — the same "nothing staged" path a
// real engine takes when no changeset is open — rather than panicking.
//
// An OpDiff failure for one op is not fatal: that op displays without
// windows (its glyph falls back to proposed) instead of costing the whole
// load. OpDiff is a read-only reconstruction (contract §1) and shares
// Diff's lock discipline, so running it here is exactly as safe as the
// Diff call above it.
func loadCmd(e *stage.Engine) tea.Cmd {
	return func() tea.Msg {
		if e == nil {
			return loadedMsg{err: stage.ErrNoChangeset}
		}
		cs, err := e.Current()
		if err != nil {
			return loadedMsg{err: err}
		}
		d, err := e.Diff()
		if err != nil {
			return loadedMsg{err: err}
		}
		opDiffs := make(map[string][]stage.FileOpDiff, len(cs.Ops))
		for _, op := range flattenAllOps(cs) {
			if _, done := opDiffs[op.ID]; done {
				continue
			}
			files, err := e.OpDiff(op.ID)
			if err != nil {
				files = nil
			}
			opDiffs[op.ID] = files
		}
		return loadedMsg{changeset: cs, diff: d, opDiffs: opDiffs}
	}
}

// stageChangedCmd reports e's currently open changeset (or the empty one)
// as a ui.StageChangedMsg, so the shell's header — and any other pane
// active when it arrives — stays live.
func stageChangedCmd(e *stage.Engine) tea.Cmd {
	return func() tea.Msg {
		if e == nil {
			return ui.StageChangedMsg{}
		}
		cs, err := e.Current()
		if err != nil {
			return ui.StageChangedMsg{}
		}
		return ui.StageChangedMsg{ChangesetID: cs.ID, Ops: len(cs.Live())}
	}
}

// applyLoaded installs a loadCmd result onto m. errors.Is(err,
// stage.ErrNoChangeset) is not an error state: it renders the empty state
// and clears every field a previously-open changeset left behind. The
// cursor is preserved (clamped, not reset) across a reload that carries a
// changeset, so an in-flight y/n's own advance survives the asynchronous
// round trip back through this message. The Preview mode is likewise left
// alone: p belongs to the reviewer, not to the changeset's churn.
func (m *Model) applyLoaded(msg loadedMsg) {
	if msg.err != nil {
		if errors.Is(msg.err, stage.ErrNoChangeset) {
			m.hasChangeset = false
			m.loadErr = nil
			m.changeset = nil
			m.diff = stage.Diff{}
			m.ops = nil
			m.opDiffs = nil
			m.stops = nil
			m.cursor = 0
			return
		}
		m.loadErr = msg.err
		return
	}

	m.hasChangeset = true
	m.loadErr = nil
	m.changeset = msg.changeset
	m.diff = msg.diff
	m.ops = flattenAllOps(msg.changeset)
	m.opDiffs = msg.opDiffs
	m.stops = buildCursorStops(msg.diff)
	m.cursor = clampCursor(m.cursor, len(m.stops))
}

// flattenAllOps returns cs's ops — top-level each immediately followed by
// its own cascade entries, recursively, in every state. This is the Ops
// panel's list, and the frozen grids show a dropped op in it (the mockup
// vault's fourth op is shown with the ✗ glyph, s2-screens.md T06), so
// unlike flattenLiveOps this walk does not filter states: a dropped op
// must stay visible for what it is.
func flattenAllOps(cs *stage.Changeset) []stage.Op {
	if cs == nil {
		return nil
	}
	var out []stage.Op
	for _, op := range cs.Ops {
		out = append(out, op)
		out = append(out, flattenAllCascade(op.Cascade)...)
	}
	return out
}

// flattenAllCascade is flattenAllOps' recursive step over one op's Cascade
// slice.
func flattenAllCascade(cascade []stage.Op) []stage.Op {
	var out []stage.Op
	for _, op := range cascade {
		out = append(out, op)
		out = append(out, flattenAllCascade(op.Cascade)...)
	}
	return out
}
