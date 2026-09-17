// commit.go is the review screen's C key: the lint-regression gate, 008
// contract §5's raw-only commit confirmation, and the Engine.Commit call
// itself. Moved out of review.go (route.go's file-split precedent) so
// review.go stays under conventions §2's ~400-line guideline. The commit is
// synchronous inside Update, which is what makes it safe next to the App's
// periodic ReloadIfChanged: the two can never overlap (008 MASTER §5
// threading rule).
package review

import (
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// commitMessage is the changeset's Intent, falling back to "review: <id>"
// when Intent is empty.
func (m *Model) commitMessage() string {
	if m.changeset == nil {
		return "review: "
	}
	if m.changeset.Intent != "" {
		return m.changeset.Intent
	}
	return "review: " + m.changeset.ID
}

// commit is `C`: calls the same LintBaseline this package exports before
// calling Engine.Commit, so it produces exactly the same result as
// `lw commit` (TD-3 — now the same call, not a second implementation of
// it). There is no --force in the TUI.
//
// After the lint gates pass, a raw-only changeset — at least one live
// ingest_source op, zero live create_page ops (008 contract §5) — needs a
// second, deliberate C: the first warns and arms with the changeset's id
// (rawOnlyWarning), any other key disarms (handleKey), and the next C
// commits — only while that same changeset is still the open one (C-807).
// A successful commit also batches a ui.VaultReloadedMsg producer (U4):
// the shell's header counts had subscribers but no producer before 008,
// so an in-TUI commit never refreshed them.
func (m *Model) commit() (ui.Pane, tea.Cmd) {
	e := m.deps.Engine

	projected, err := e.ProjectedReport()
	if err != nil {
		m.setStatus(ui.StatusWarn, fmt.Sprintf("commit failed: %v", err))
		return m, nil
	}
	baseline, err := LintBaseline(e)
	if err != nil {
		m.setStatus(ui.StatusWarn, fmt.Sprintf("commit failed: %v", err))
		return m, nil
	}
	if projected.Regresses(baseline) {
		m.setStatus(ui.StatusWarn, fmt.Sprintf(
			"commit refused: lint regressed: %d error(s) projected vs %d in the last commit; fix it or drop the offending hunk",
			projected.Errors, baseline.Errors))
		return m, nil
	}

	if m.rawOnlyWarning(e) {
		return m, nil
	}

	commitID, err := e.Commit(m.commitMessage())
	if err != nil {
		switch {
		case errors.Is(err, stage.ErrNothingToCommit):
			m.setStatus(ui.StatusWarn, "commit refused: nothing to commit — every op was dropped")
		case errors.Is(err, stage.ErrStale):
			m.setStatus(ui.StatusWarn,
				"commit refused: changeset has a stale op — the working tree changed since it was proposed; rebase or drop the stale op")
		default:
			m.setStatus(ui.StatusWarn, fmt.Sprintf("commit failed: %v", err))
		}
		return m, nil
	}

	m.commitArmedFor = "" // the confirmation was consumed with its changeset
	m.setStatus(ui.StatusGood, fmt.Sprintf("committed %s", commitID))
	return m, tea.Batch(loadCmd(e), func() tea.Msg { return ui.StageChangedMsg{} },
		func() tea.Msg { return ui.VaultReloadedMsg{} })
}

// rawOnlyWarning guards the confirmation arm and decides whether this C
// is a raw-only changeset's first press. It reads the changeset from the
// engine rather than the model's loaded copy, because the engine is what
// Commit would act on.
//
// The arm is bound to the changeset id it was armed for (C-807): when the
// engine's open changeset no longer has that id — a swap that happened
// with no key press to disarm, such as another process's commit reloading
// a new changeset in — the arm is stale and is dropped here, so the next
// raw-only changeset gets its own warning before any commit. It returns
// true, meaning the caller must not commit yet, only when it has just
// armed: StatusWarn with the contract §5 text — every raw path in live-op
// order, joined with ", ".
func (m *Model) rawOnlyWarning(e *stage.Engine) bool {
	c, err := e.Current()
	if err != nil {
		m.commitArmedFor = "" // nothing open; a stale arm must not survive
		return false          // Engine.Commit will report it itself
	}
	if m.commitArmedFor != "" {
		if m.commitArmedFor == c.ID {
			return false // armed for the changeset that is still open: commit
		}
		m.commitArmedFor = "" // the open changeset changed under the arm
	}
	var raws []string
	creates := 0
	for _, op := range c.Live() {
		switch op.Kind {
		case stage.OpIngestSource:
			raws = append(raws, op.Path)
		case stage.OpCreatePage:
			creates++
		}
	}
	if len(raws) == 0 || creates > 0 {
		return false
	}
	m.commitArmedFor = c.ID
	m.setStatus(ui.StatusWarn, fmt.Sprintf(
		"0 pages proposed — this commits raw source(s) only: %s · press C again to commit",
		strings.Join(raws, ", ")))
	return true
}
