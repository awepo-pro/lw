// review.go implements the changeset review screen (backbone §12 Pane,
// §5.4 Engine, §5.6 Diff; s4-tui.md S4-T3 — "ship this first"): walking
// the open changeset's ops and hunks, accepting and dropping hunks through
// stage.Engine, and committing with the same lint-regression gate
// cmd/lw's `lw commit` applies — Engine.Commit itself performs no such
// check. LintBaseline (S6-C127) is the one shared implementation of that
// gate's baseline computation; cmd/lw imports this package already
// (cmd_tui.go) and calls it directly, which closes that half of TD-3.
//
// Every mutation goes through stage.Engine; nothing in this package writes
// a vault file directly (00-conventions.md §5.4).
package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// statusKind selects which of Theme's status colours a Model's status
// line renders with.
type statusKind int

const (
	statusNone statusKind = iota
	statusInfo
	statusGood
	statusWarn
)

// Model is the review screen (backbone §12 ui.Pane). New captures Deps and
// nothing else; Init is what loads the open changeset and its diff.
type Model struct {
	deps  ui.Deps
	theme ui.Theme // C-81: a copy taken at construction, rebuilt locally on tea.BackgroundColorMsg

	hasChangeset bool
	loadErr      error
	changeset    *stage.Changeset
	diff         stage.Diff
	ops          []stage.Op // flattened live ops (hunks.go), the op list's render order
	stops        []cursorStop
	cursor       int

	status     string
	statusKind statusKind
}

var _ ui.Pane = (*Model)(nil)

// New constructs the review screen (backbone §12). It captures d and
// nothing else — Init is what loads the open changeset and its diff, and
// every mutation from then on goes through d.Engine.
func New(d ui.Deps) ui.Pane {
	return &Model{deps: d, theme: d.Theme}
}

// Title returns the pane's name for the shell's tab bar (backbone §12).
func (m *Model) Title() string { return "Review" }

// Help returns the review screen's key bindings (backbone §12), in the
// order s4-tui.md S4-T3 lists them.
func (m *Model) Help() []key.Binding {
	k := m.deps.Keys
	return []key.Binding{
		k.AcceptHunk, k.DropHunk, k.SplitHunk, k.AcceptAll,
		k.Reject, k.Commit, k.MoveDown, k.MoveUp, k.Top, k.Bottom,
	}
}

// loadedMsg carries the result of loading the open changeset and its diff
// (s4-tui.md S4-T3 item 2). Unexported: nothing outside this package's
// Update ever sees one.
type loadedMsg struct {
	changeset *stage.Changeset
	diff      stage.Diff
	err       error
}

// loadCmd returns a tea.Cmd that loads e's open changeset and diff and
// delivers them as a loadedMsg. A nil e (a headless pane built with no
// engine at all) reports stage.ErrNoChangeset — the same "nothing staged"
// path a real engine takes when no changeset is open — rather than
// panicking.
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
		return loadedMsg{changeset: cs, diff: d}
	}
}

// stageChangedCmd reports e's currently open changeset (or the empty one)
// as a ui.StageChangedMsg, so the shell's STAGE panel — and any other pane
// active when it arrives — stays live (s4-tui.md S4-T3 item 6).
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

// Init loads the currently open changeset and its diff (item 2).
func (m *Model) Init() tea.Cmd {
	return loadCmd(m.deps.Engine)
}

// Update handles the review keymap and the shell messages this screen
// cares about (item 4: matches tea.KeyPressMsg, never tea.KeyMsg — C-80).
//
// Contract (C-106/TD-4): the shell's App.propagate delivers
// StageChangedMsg and VaultReloadedMsg only while this pane is active, so
// this Update must never assume it saw every event — it reloads cheaply
// whenever one does arrive, and every mutation reloads on its own besides.
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

// applyLoaded installs a loadCmd result onto m. errors.Is(err,
// stage.ErrNoChangeset) is not an error state (item 2): it renders the
// empty state and clears every field a previously-open changeset left
// behind. The cursor is preserved (clamped, not reset) across a reload
// that carries a changeset, so an in-flight y/n's own advance survives the
// asynchronous round trip back through this message.
func (m *Model) applyLoaded(msg loadedMsg) {
	if msg.err != nil {
		if errors.Is(msg.err, stage.ErrNoChangeset) {
			m.hasChangeset = false
			m.loadErr = nil
			m.changeset = nil
			m.diff = stage.Diff{}
			m.ops = nil
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
	m.ops = flattenLiveOps(msg.changeset)
	m.stops = buildCursorStops(msg.diff)
	m.cursor = clampCursor(m.cursor, len(m.stops))
}

// setStatus records the message the status line renders until the next
// key handler replaces or clears it.
func (m *Model) setStatus(kind statusKind, msg string) {
	m.statusKind = kind
	m.status = msg
}

// handleKey dispatches one tea.KeyPressMsg against the review keymap. A
// changeset that is not open makes every key a no-op (item 2) — there is
// nothing to accept, drop, reject or commit, and no cursor to move.
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
	case key.Matches(msg, k.AcceptHunk):
		return m.acceptHunk()
	case key.Matches(msg, k.DropHunk):
		return m.dropHunk()
	case key.Matches(msg, k.SplitHunk):
		// C-90/TD-2: hunk splitting is not built in v0.1. No engine call.
		m.setStatus(statusInfo, "hunk split lands in v1.0 — drop the op and re-propose")
	case key.Matches(msg, k.AcceptAll):
		return m.acceptAll()
	case key.Matches(msg, k.Reject):
		return m.reject()
	case key.Matches(msg, k.Commit):
		return m.commit()
	}
	return m, nil
}

// acceptHunk is `y`: Engine.UndropHunk (C-89/D-CL). Hunks are live by
// default, so undropping one that is already live succeeds as a no-op —
// the engine's own Contract, not a special case here — and the cursor
// still advances so walking the review with `y` alone visits every hunk.
func (m *Model) acceptHunk() (ui.Pane, tea.Cmd) {
	opID, hunkID, ok := resolveCursor(m.diff, m.stops, m.cursor)
	if !ok {
		return m, nil
	}
	if err := m.deps.Engine.UndropHunk(opID, hunkID); err != nil {
		m.setStatus(statusWarn, fmt.Sprintf("accept hunk failed: %v", err))
		return m, nil
	}
	m.setStatus(statusNone, "")
	m.cursor = clampCursor(m.cursor+1, len(m.stops))
	return m, tea.Batch(loadCmd(m.deps.Engine), stageChangedCmd(m.deps.Engine))
}

// dropHunk is `n`: Engine.DropHunk.
func (m *Model) dropHunk() (ui.Pane, tea.Cmd) {
	opID, hunkID, ok := resolveCursor(m.diff, m.stops, m.cursor)
	if !ok {
		return m, nil
	}
	if err := m.deps.Engine.DropHunk(opID, hunkID); err != nil {
		m.setStatus(statusWarn, fmt.Sprintf("drop hunk failed: %v", err))
		return m, nil
	}
	m.setStatus(statusNone, "")
	m.cursor = clampCursor(m.cursor+1, len(m.stops))
	return m, tea.Batch(loadCmd(m.deps.Engine), stageChangedCmd(m.deps.Engine))
}

// acceptAll is `A`: refused unless the projected tree lints clean
// (s4-tui.md S4-T3 item 8, /PLAN.md §14's review-fatigue mitigation). A
// refusal changes nothing — no hunk is touched and no reload is issued.
func (m *Model) acceptAll() (ui.Pane, tea.Cmd) {
	e := m.deps.Engine

	report, err := e.ProjectedReport()
	if err != nil {
		m.setStatus(statusWarn, fmt.Sprintf("accept-all failed: %v", err))
		return m, nil
	}
	if !report.Clean() {
		m.setStatus(statusWarn, fmt.Sprintf(
			"accept-all refused: %d lint error(s), %d warning(s) projected — fix them or accept hunk by hunk",
			report.Errors, report.Warns))
		return m, nil
	}

	cs, err := e.Current()
	if err != nil {
		m.setStatus(statusWarn, fmt.Sprintf("accept-all failed: %v", err))
		return m, nil
	}
	for _, op := range flattenLiveOps(cs) {
		for _, h := range op.Hunks {
			if !h.Dropped {
				continue
			}
			if err := e.UndropHunk(op.ID, h.ID); err != nil {
				m.setStatus(statusWarn, fmt.Sprintf("accept-all failed on %s/%s: %v", op.ID, h.ID, err))
				return m, tea.Batch(loadCmd(e), stageChangedCmd(e))
			}
		}
	}
	m.setStatus(statusNone, "")
	return m, tea.Batch(loadCmd(e), stageChangedCmd(e))
}

// reject is `X`: Engine.Reject. There is no changeset left to show
// afterward, so the emitted StageChangedMsg is the empty one directly
// (item 9) — Current would return stage.ErrNoChangeset and produce the
// identical value, but stating it directly needs no round trip through
// the engine to know.
func (m *Model) reject() (ui.Pane, tea.Cmd) {
	if err := m.deps.Engine.Reject("rejected in review"); err != nil {
		m.setStatus(statusWarn, fmt.Sprintf("reject failed: %v", err))
		return m, nil
	}
	m.setStatus(statusNone, "")
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
// When it does not — a vault that has never been committed — there is no
// commit_end to read. Before S6-C127 that meant "no baseline, so the
// check passes" (backbone §5.7 D-AG's literal wording): a vault's very
// first commit had no gate at all, which is backwards, since an agent's
// first ingest is exactly the commit this gate exists to catch. S6-C127
// resolves it here: the baseline becomes the lint.Report of the CURRENT
// COMMITTED working tree — e.Vault(), e.Index() and e.Vault().Graph(),
// the same {Vault, Index, Graph} shape `lw lint` and cmd_lint's --fix
// path build (no check reads Index at all, but the shape matches so the
// counts can never disagree with what `lw lint` would print right now).
// A vault's first commit is then refused exactly like every later one —
// against "no worse than what's on disk right now" — rather than
// unconditionally, and a vault that already carries N lint errors before
// its first commit is not penalized for them: only a NEW error, added by
// the changeset being committed, regresses.
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
// when Intent is empty (item 10).
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
// `lw commit` (item 10, TD-3 — now the same call, not a second
// implementation of it). There is no --force in the TUI in v0.1.
func (m *Model) commit() (ui.Pane, tea.Cmd) {
	e := m.deps.Engine

	projected, err := e.ProjectedReport()
	if err != nil {
		m.setStatus(statusWarn, fmt.Sprintf("commit failed: %v", err))
		return m, nil
	}
	baseline, err := LintBaseline(e)
	if err != nil {
		m.setStatus(statusWarn, fmt.Sprintf("commit failed: %v", err))
		return m, nil
	}
	if projected.Regresses(baseline) {
		m.setStatus(statusWarn, fmt.Sprintf(
			"commit refused: lint regressed: %d error(s) projected vs %d in the last commit; fix it or drop the offending hunk",
			projected.Errors, baseline.Errors))
		return m, nil
	}

	commitID, err := e.Commit(m.commitMessage())
	if err != nil {
		if errors.Is(err, stage.ErrStale) {
			m.setStatus(statusWarn,
				"commit refused: changeset has a stale op — the working tree changed since it was proposed; rebase or drop the stale op")
		} else {
			m.setStatus(statusWarn, fmt.Sprintf("commit failed: %v", err))
		}
		return m, nil
	}

	m.setStatus(statusGood, fmt.Sprintf("committed %s", commitID))
	return m, tea.Batch(loadCmd(e), func() tea.Msg { return ui.StageChangedMsg{} })
}

// View renders the review screen at exactly w by h (backbone §12; item 1)
// — no line wider than w, never assuming 80x24.
func (m *Model) View(w, h int) string {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}

	var body string
	switch {
	case m.loadErr != nil:
		body = fmt.Sprintf("review: %v", m.loadErr)
	case !m.hasChangeset:
		body = "nothing staged"
	default:
		body = m.renderBody(w, h)
	}

	return strings.Join(fitLines(body, w, h), "\n")
}

// statusStyle returns the Theme style m.statusKind renders the status line
// with.
func (m *Model) statusStyle() lipgloss.Style {
	switch m.statusKind {
	case statusGood:
		return m.theme.Good
	case statusWarn:
		return m.theme.Warn
	case statusInfo:
		return m.theme.Muted
	default:
		return m.theme.Base
	}
}

// renderBody lays out the op list and the selected op's diff side by
// side, with a status line beneath when one is set (item 1, item 11).
func (m *Model) renderBody(w, h int) string {
	statusH := 0
	if m.status != "" {
		statusH = 1
	}
	bodyH := h - statusH
	if bodyH < 0 {
		bodyH = 0
	}

	leftW := w / 3
	if leftW > 32 {
		leftW = 32
	}
	if leftW < 1 {
		leftW = w
	}
	sepW := 0
	if w > leftW {
		sepW = 1
	}
	rightW := w - leftW - sepW
	if rightW < 0 {
		rightW = 0
	}

	currentOpID, _, _ := resolveCursor(m.diff, m.stops, m.cursor)

	leftLines := fitLines(renderOpList(m.theme, m.ops, currentOpID, leftW), leftW, bodyH)
	rightLines := fitLines(renderDiffPane(m.theme, m.changeset, m.diff, m.stops, m.cursor, rightW), rightW, bodyH)

	sep := ""
	if sepW > 0 {
		sep = m.theme.Border.Render(fitLine("|", sepW))
	}

	rows := make([]string, 0, h)
	for i := 0; i < bodyH; i++ {
		rows = append(rows, leftLines[i]+sep+rightLines[i])
	}
	if statusH > 0 {
		rows = append(rows, m.statusStyle().Render(fitLine(m.status, w)))
	}
	return strings.Join(rows, "\n")
}
