// review.go implements the changeset review screen's model: walking the
// open changeset's ops and hunks, accepting and dropping hunks through
// stage.Engine, and committing with the same lint-regression gate
// cmd/lw's `lw commit` applies — Engine.Commit itself performs no such
// check. LintBaseline (S6-C127) is the one shared implementation of that
// gate's baseline computation; cmd/lw imports this package already
// (cmd_tui.go) and calls it directly, which closes that half of TD-3.
//
// Rendering lives in view.go (frame layout), diffblock.go (the Detail
// panel's Diff and Preview modes) and opsview.go (Ops and Changeset
// panels); the cursor model is hunks.go and the load round trip is
// load.go. Every mutation goes through stage.Engine; nothing in this
// package writes a vault file directly (00-conventions.md §5.4).
package review

import (
	"encoding/json"
	"errors"
	"fmt"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/markdown"
)

// Model is the review screen (backbone §12 ui.Pane). New captures Deps
// and nothing else; Init is what loads the open changeset and its diff.
type Model struct {
	deps  ui.Deps
	theme ui.Theme // C-81: a copy taken at construction, rebuilt locally on tea.BackgroundColorMsg
	md    *markdown.Renderer

	hasChangeset bool
	loadErr      error
	changeset    *stage.Changeset
	diff         stage.Diff
	ops          []stage.Op // every op, dropped included: the Ops panel's list (load.go)
	opDiffs      map[string][]stage.FileOpDiff
	stops        []cursorStop
	cursor       int
	preview      bool // p toggles the Detail panel between Diff and Preview

	status string         // transient StatusReporter message; "" shows the bindings
	level  ui.StatusLevel // the level the footer styles status with
}

var (
	_ ui.Pane           = (*Model)(nil)
	_ ui.FooterHelper   = (*Model)(nil)
	_ ui.OverlayHelper  = (*Model)(nil)
	_ ui.StatusReporter = (*Model)(nil)
)

// New constructs the review screen (backbone §12). It captures d and
// creates the shared markdown renderer — nothing else; Init is what loads
// the open changeset and its diff, and every mutation from then on goes
// through d.Engine.
func New(d ui.Deps) ui.Pane {
	return &Model{deps: d, theme: d.Theme, md: markdown.NewRenderer()}
}

// Title returns the pane's name for the shell's tab bar (backbone §12).
func (m *Model) Title() string { return "Review" }

// Help returns the review screen's key bindings (backbone §12) — the
// fallback list a shell shows when a pane implements no FooterHelper.
// The footer itself is FooterHelp (keys.go), whose order is the frozen
// one; this list keeps the same bindings.
func (m *Model) Help() []key.Binding {
	k := m.deps.Keys
	return []key.Binding{
		k.AcceptHunk, k.DropHunk, k.MoveDown, k.Preview, k.AcceptAll,
		k.Commit, k.Reject, k.Top, k.NextPane, k.Quit,
	}
}

// Init loads the currently open changeset and its diff (s2-screens.md
// T06; the load round trip is load.go).
func (m *Model) Init() tea.Cmd {
	return loadCmd(m.deps.Engine)
}

// Update handles the review keymap and the shell messages this screen
// cares about. The shell's App.propagate delivers StageChangedMsg and
// VaultReloadedMsg only while this pane is active, so this Update must
// never assume it saw every event — it reloads cheaply whenever one does
// arrive, and every mutation reloads on its own besides.
func (m *Model) Update(msg tea.Msg) (ui.Pane, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.theme = m.theme.WithDark(msg.IsDark())
		return m, nil

	case ui.StageChangedMsg:
		return m, loadCmd(m.deps.Engine)

	case ui.VaultReloadedMsg:
		return m, loadCmd(m.deps.Engine)

	case loadedMsg:
		m.applyLoaded(msg)
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// handleKey dispatches one tea.KeyPressMsg against the review keymap. A
// changeset that is not open makes every key a no-op — there is nothing
// to accept, drop, reject or commit, and no cursor to move.
func (m *Model) handleKey(msg tea.KeyPressMsg) (ui.Pane, tea.Cmd) {
	if !m.hasChangeset {
		return m, nil
	}
	k := m.deps.Keys
	switch {
	case key.Matches(msg, k.MoveDown):
		m.cursor = clampCursor(m.cursor+1, len(m.stops))
	case key.Matches(msg, k.MoveUp):
		m.cursor = clampCursor(m.cursor-1, len(m.stops))
	case key.Matches(msg, k.Top):
		m.cursor = 0
	case key.Matches(msg, k.Bottom):
		m.cursor = clampCursor(len(m.stops)-1, len(m.stops))
	case key.Matches(msg, k.Preview):
		// s2-screens.md T06: p toggles Diff ↔ Preview and keeps the
		// cursor; the Preview follows whatever op the cursor lands on.
		m.preview = !m.preview
	case key.Matches(msg, k.AcceptHunk):
		return m.acceptHunk()
	case key.Matches(msg, k.DropHunk):
		return m.dropHunk()
	case key.Matches(msg, k.SplitHunk):
		// C-90/TD-2: hunk splitting is not built (the ? overlay says so).
		// No engine call.
		m.setStatus(ui.StatusInfo, "hunk split is not built yet — drop the op and re-propose")
	case key.Matches(msg, k.AcceptAll):
		return m.acceptAll()
	case key.Matches(msg, k.Reject):
		return m.reject()
	case key.Matches(msg, k.Commit):
		return m.commit()
	}
	return m, nil
}

// reviewRefusal names the reason the cursor's hunk must not be accepted
// or dropped, or "" when the key may proceed. Two refusals, both decided
// BEFORE any engine call (s2-screens.md T06 keys, MASTER §8 ORCH-9):
//
//   - a stale op: Engine.DropHunk/UndropHunk have no stale guard, and
//     OpDiff shows a stale op's windows with HunkID "" — acting on a key
//     would touch content the reviewer cannot see as attributed;
//   - an ownerless window: a window with HunkID "" (a create, an ingest,
//     a derived index.md) has no persisted hunk to accept or drop.
func (m *Model) reviewRefusal(opID, hunkID string) string {
	if op, ok := findOp(m.ops, opID); ok && op.State == stage.StateStale {
		return fmt.Sprintf("op %s is stale: refresh before reviewing its hunks", opID)
	}
	if !hasWindow(m.opDiffs[opID], hunkID) {
		return "this window has no hunk id — it cannot be accepted or dropped individually"
	}
	return ""
}

// acceptHunk is `y`: Engine.UndropHunk (C-89/D-CL). Hunks are live by
// default, so undropping one that is already live succeeds as a no-op —
// the engine's own contract, not a special case here — and the cursor
// still advances so walking the review with `y` alone visits every hunk.
func (m *Model) acceptHunk() (ui.Pane, tea.Cmd) {
	opID, hunkID, ok := resolveCursor(m.diff, m.stops, m.cursor)
	if !ok {
		return m, nil
	}
	if refusal := m.reviewRefusal(opID, hunkID); refusal != "" {
		m.setStatus(ui.StatusWarn, refusal)
		return m, nil
	}
	if err := m.deps.Engine.UndropHunk(opID, hunkID); err != nil {
		m.setStatus(ui.StatusWarn, fmt.Sprintf("accept hunk failed: %v", err))
		return m, nil
	}
	m.setStatus(ui.StatusInfo, "")
	m.cursor = clampCursor(m.cursor+1, len(m.stops))
	return m, tea.Batch(loadCmd(m.deps.Engine), stageChangedCmd(m.deps.Engine))
}

// dropHunk is `n`: Engine.DropHunk.
func (m *Model) dropHunk() (ui.Pane, tea.Cmd) {
	opID, hunkID, ok := resolveCursor(m.diff, m.stops, m.cursor)
	if !ok {
		return m, nil
	}
	if refusal := m.reviewRefusal(opID, hunkID); refusal != "" {
		m.setStatus(ui.StatusWarn, refusal)
		return m, nil
	}
	if err := m.deps.Engine.DropHunk(opID, hunkID); err != nil {
		m.setStatus(ui.StatusWarn, fmt.Sprintf("drop hunk failed: %v", err))
		return m, nil
	}
	m.setStatus(ui.StatusInfo, "")
	m.cursor = clampCursor(m.cursor+1, len(m.stops))
	return m, tea.Batch(loadCmd(m.deps.Engine), stageChangedCmd(m.deps.Engine))
}

// acceptAll is `A`: refused on a stale op (before ANY engine call — a
// refusal must not touch the changeset it warns about), then refused
// unless the projected tree lints clean (s4-tui.md S4-T3 item 8,
// /docs/design.md §14's review-fatigue mitigation). A lint refusal
// changes nothing — no hunk is touched and no reload is issued.
func (m *Model) acceptAll() (ui.Pane, tea.Cmd) {
	e := m.deps.Engine

	for _, op := range m.ops {
		if op.State == stage.StateStale {
			m.setStatus(ui.StatusWarn, fmt.Sprintf("op %s is stale: refresh before reviewing its hunks", op.ID))
			return m, nil
		}
	}

	report, err := e.ProjectedReport()
	if err != nil {
		m.setStatus(ui.StatusWarn, fmt.Sprintf("accept-all failed: %v", err))
		return m, nil
	}
	if !report.Clean() {
		m.setStatus(ui.StatusWarn, fmt.Sprintf(
			"accept-all refused: %d lint error(s), %d warning(s) projected — fix them or accept hunk by hunk",
			report.Errors, report.Warns))
		return m, nil
	}

	cs, err := e.Current()
	if err != nil {
		m.setStatus(ui.StatusWarn, fmt.Sprintf("accept-all failed: %v", err))
		return m, nil
	}
	for _, op := range flattenLiveOps(cs) {
		for _, h := range op.Hunks {
			if !h.Dropped {
				continue
			}
			if err := e.UndropHunk(op.ID, h.ID); err != nil {
				m.setStatus(ui.StatusWarn, fmt.Sprintf("accept-all failed on %s/%s: %v", op.ID, h.ID, err))
				return m, tea.Batch(loadCmd(e), stageChangedCmd(e))
			}
		}
	}
	m.setStatus(ui.StatusInfo, "")
	return m, tea.Batch(loadCmd(e), stageChangedCmd(e))
}

// reject is `X`: Engine.Reject. There is no changeset left to show
// afterward, so the emitted StageChangedMsg is the empty one directly —
// Current would return stage.ErrNoChangeset and produce the identical
// value, but stating it directly needs no round trip through the engine.
func (m *Model) reject() (ui.Pane, tea.Cmd) {
	if err := m.deps.Engine.Reject("rejected in review"); err != nil {
		m.setStatus(ui.StatusWarn, fmt.Sprintf("reject failed: %v", err))
		return m, nil
	}
	m.setStatus(ui.StatusInfo, "")
	return m, tea.Batch(loadCmd(m.deps.Engine), func() tea.Msg { return ui.StageChangedMsg{} })
}

// commitEndData is the wire shape of a commit_end event's Data (backbone
// §5.7 D-AG): the counts of the report computed for that commit.
type commitEndData struct {
	LintErrors int `json:"lint_errors"`
	LintWarns  int `json:"lint_warns"`
}

// LintBaseline computes the lint-regression baseline the commit gate
// compares a projected report against (backbone §5.7 D-AG; S6-C127). It is
// exported and lives here — rather than duplicated a second time — because
// cmd/lw already imports this package (cmd_tui.go, to wire the review
// screen into the shell), which is what closes TD-3's "the gate is
// implemented twice" for this half of it: cmd/lw's cmdCommit calls
// review.LintBaseline directly instead of running its own copy of this
// journal query.
//
// When the journal already holds a commit_end event, its counts ARE the
// baseline — the tree exactly as it was committed, decoded from Data
// exactly as Commit wrote it. Filter.Limit selects the most recent N
// events and returns them oldest-first (backbone §5.7 D-AU), so Limit:1
// is read as evs[0]; a larger Limit read as evs[0] would silently compare
// every future commit against the FIRST commit ever made.
//
// When it does not — a vault that has never been committed — the baseline
// becomes the lint.Report of the CURRENT COMMITTED working tree
// (e.Vault(), e.Index() and e.Vault().Graph(), the same {Vault, Index,
// Graph} shape `lw lint` builds), so a vault's first commit is refused
// exactly like every later one and a vault that already carries N lint
// errors is not penalized for them: only a NEW error, added by the
// changeset being committed, regresses.
func LintBaseline(e *stage.Engine) (lint.Report, error) {
	evs, err := e.Journal().Query(stage.Filter{
		Kinds: []stage.EventKind{stage.EvCommitEnd},
		Limit: 1,
	})
	if err != nil {
		return lint.Report{}, err
	}
	if len(evs) > 0 {
		var data commitEndData
		if err := json.Unmarshal(evs[0].Data, &data); err != nil {
			return lint.Report{}, fmt.Errorf("review: parse commit_end data: %w", err)
		}
		return lint.Report{Errors: data.LintErrors, Warns: data.LintWarns}, nil
	}

	ctx := &lint.Context{Vault: e.Vault(), Index: e.Index(), Graph: e.Vault().Graph()}
	return lint.Run(ctx, nil), nil
}

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

	commitID, err := e.Commit(m.commitMessage())
	if err != nil {
		if errors.Is(err, stage.ErrStale) {
			m.setStatus(ui.StatusWarn,
				"commit refused: changeset has a stale op — the working tree changed since it was proposed; rebase or drop the stale op")
		} else {
			m.setStatus(ui.StatusWarn, fmt.Sprintf("commit failed: %v", err))
		}
		return m, nil
	}

	m.setStatus(ui.StatusGood, fmt.Sprintf("committed %s", commitID))
	return m, tea.Batch(loadCmd(e), func() tea.Msg { return ui.StageChangedMsg{} })
}

// setStatus records the message the footer renders until the next key
// handler replaces or clears it (contract §5 StatusReporter; panes no
// longer draw a status line inside their own View).
func (m *Model) setStatus(level ui.StatusLevel, msg string) {
	m.level = level
	m.status = msg
}

// Status is the shell's StatusReporter hook (contract §5): the transient
// message and the level the footer styles it with. An empty message keeps
// the footer on the bindings.
func (m *Model) Status() (string, ui.StatusLevel) { return m.status, m.level }

// findOp returns the op with id from ops, and false when no such op is
// listed.
func findOp(ops []stage.Op, id string) (stage.Op, bool) {
	if id == "" {
		return stage.Op{}, false
	}
	for _, op := range ops {
		if op.ID == id {
			return op, true
		}
	}
	return stage.Op{}, false
}
