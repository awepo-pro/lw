// lintview.go implements the lint report screen (backbone §12 Pane, §4
// Check/Report; s4-tui.md S4-T5, corrected by C-85/D-V to the real 14
// checks — not the 11 the stage file originally said): one row per
// lint.All() check, expandable to its findings, `enter` asking the shell
// to jump to a finding's page in Browse, and `f` pointing at the agent
// repair path that lands only in M5.
//
// Every mutation this screen could cause still goes through stage.Engine
// or the shell's own message vocabulary — nothing here writes to the
// vault, and `f` is a message, not a repair (00-conventions.md §5.4;
// /docs/design.md §9.4: even repairs go through review).
package lintview

import (
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

// errNoEngine is what runReportCmd reports when this pane was constructed
// with a nil Deps.Engine (a headless shell with no vault at all, matching
// S4-T2's own "constructible headless" contract) — rendered visibly, never
// panicking.
var errNoEngine = errors.New("lintview: no engine loaded")

// statusKind selects which of Theme's status colours a Model's status
// line renders with (mirrors internal/ui/review's own statusKind).
type statusKind int

const (
	statusNone statusKind = iota
	statusInfo
	statusWarn
)

// row is one line of the flattened, expandable check list: either a check
// header (findingIdx < 0) or one of that check's findings, addressed by
// its index into Report.ByCheck[check.ID()].
type row struct {
	checkIdx   int
	findingIdx int
}

// Model is the lint screen (backbone §12 ui.Pane).
type Model struct {
	deps  ui.Deps
	theme ui.Theme // C-81: a copy taken at construction, rebuilt locally on tea.BackgroundColorMsg

	checks []lint.Check // lint.All(), fixed at 14 (C-85/D-V); computed once, checks hold no state

	hasReport bool
	loadErr   error
	report    lint.Report

	expanded map[string]bool // check ID -> expanded
	rows     []row
	cursor   int

	status     string
	statusKind statusKind
}

var _ ui.Pane = (*Model)(nil)

// New constructs the lint screen (backbone §12). It captures d and
// lint.All()'s fixed check list; Init is what runs the report.
func New(d ui.Deps) ui.Pane {
	return &Model{
		deps:     d,
		theme:    d.Theme,
		checks:   lint.All(),
		expanded: map[string]bool{},
	}
}

// Title returns the pane's name for the shell's tab bar (backbone §12).
func (m *Model) Title() string { return "Lint" }

// Help returns the lint screen's key bindings (backbone §12). enter and f
// have no KeyMap field of their own — ui.KeyMap belongs to the shell and
// gets no additions here (s4-tui.md item 4's precedent in browse.go) — so
// they are described with ad-hoc bindings for display purposes only; only
// MoveDown/MoveUp/Top/Bottom are matched through d.Keys.
func (m *Model) Help() []key.Binding {
	k := m.deps.Keys
	return []key.Binding{
		k.MoveDown, k.MoveUp, k.Top, k.Bottom,
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "expand / open finding")),
		key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "ask agent to fix")),
	}
}

// reportMsg carries the result of running the lint report (pinned item 1).
// Unexported: nothing outside this package's Update ever sees one.
type reportMsg struct {
	report lint.Report
	err    error
}

// runReportCmd returns a tea.Cmd that runs the lint report exactly as
// cmd/lw/cmd_lint.go does, but from the engine's own vault and index
// rather than a fresh index.Build (pinned item 1).
func runReportCmd(e *stage.Engine) tea.Cmd {
	return func() tea.Msg {
		if e == nil {
			return reportMsg{err: errNoEngine}
		}
		v := e.Vault()
		report := lint.Run(&lint.Context{Vault: v, Index: e.Index(), Graph: v.Graph()}, nil)
		return reportMsg{report: report}
	}
}

// Init runs the lint report (pinned item: "load in Init()").
func (m *Model) Init() tea.Cmd {
	return runReportCmd(m.deps.Engine)
}

// Update handles the lint keymap and the shell messages this screen cares
// about (C-80: matches tea.KeyPressMsg, never tea.KeyMsg).
//
// Contract (C-106/TD-4): this pane is only sent messages while it is the
// active screen, so it must never assume it saw every StageChangedMsg or
// VaultReloadedMsg — it re-runs the report cheaply whenever one does
// arrive, never touching the shell to fix the gap.
func (m *Model) Update(msg tea.Msg) (ui.Pane, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.theme = m.theme.WithDark(msg.IsDark())
		return m, nil

	case ui.StageChangedMsg:
		return m, runReportCmd(m.deps.Engine)

	case ui.VaultReloadedMsg:
		return m, runReportCmd(m.deps.Engine)

	case reportMsg:
		m.applyReport(msg)
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// applyReport installs a runReportCmd result onto m and rebuilds the
// flattened row list from it.
func (m *Model) applyReport(msg reportMsg) {
	if msg.err != nil {
		m.loadErr = msg.err
		m.hasReport = false
		return
	}
	m.hasReport = true
	m.loadErr = nil
	m.report = msg.report
	m.rebuildRows()
}

// rebuildRows flattens m.checks and, for every expanded check, its
// findings into m.rows, in lint.All() table order (C-85: 14 rows when
// nothing is expanded). The cursor is clamped, never reset, so expanding
// or collapsing a row under the cursor does not jump the view.
func (m *Model) rebuildRows() {
	rows := make([]row, 0, len(m.checks))
	for ci, c := range m.checks {
		rows = append(rows, row{checkIdx: ci, findingIdx: -1})
		if m.expanded[c.ID()] {
			for fi := range m.report.ByCheck[c.ID()] {
				rows = append(rows, row{checkIdx: ci, findingIdx: fi})
			}
		}
	}
	m.rows = rows
	m.clampCursor()
}

// clampCursor keeps m.cursor inside [0, len(m.rows)-1], or 0 when m.rows
// is empty.
func (m *Model) clampCursor() {
	if len(m.rows) == 0 {
		m.cursor = 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
}

// setStatus records the message the status line renders until the next
// key handler replaces or clears it.
func (m *Model) setStatus(kind statusKind, msg string) {
	m.statusKind = kind
	m.status = msg
}

// handleKey dispatches one tea.KeyPressMsg. A report that has not loaded
// yet makes every key a no-op — there is nothing to navigate or expand.
func (m *Model) handleKey(msg tea.KeyPressMsg) (ui.Pane, tea.Cmd) {
	if !m.hasReport {
		return m, nil
	}
	k := m.deps.Keys
	switch {
	case key.Matches(msg, k.MoveDown):
		m.cursor++
		m.clampCursor()
		return m, nil
	case key.Matches(msg, k.MoveUp):
		m.cursor--
		m.clampCursor()
		return m, nil
	case key.Matches(msg, k.Top):
		m.cursor = 0
		return m, nil
	case key.Matches(msg, k.Bottom):
		m.cursor = len(m.rows) - 1
		m.clampCursor()
		return m, nil
	}

	switch msg.String() {
	case "enter":
		return m.handleEnter()
	case "f":
		// Pinned item 3: a message, not a repair — no engine call, even
		// though the engine is what would eventually perform one
		// (/docs/design.md §9.4: even repairs go through review).
		m.setStatus(statusInfo, "requires the agent (M5)")
		return m, nil
	}
	return m, nil
}

// handleEnter dispatches `enter`: a check-header row toggles its expanded
// state; a finding row asks the shell to open it (pinned item 2).
func (m *Model) handleEnter() (ui.Pane, tea.Cmd) {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return m, nil
	}
	r := m.rows[m.cursor]
	c := m.checks[r.checkIdx]

	if r.findingIdx < 0 {
		m.expanded[c.ID()] = !m.expanded[c.ID()]
		m.rebuildRows()
		return m, nil
	}

	findings := m.report.ByCheck[c.ID()]
	if r.findingIdx < 0 || r.findingIdx >= len(findings) {
		return m, nil
	}
	return m, openFindingCmd(findings[r.findingIdx])
}

// openFindingCmd is `enter` on a finding (pinned item 2, C-108/D-CU): a
// page-scoped finding asks the shell to open its Path in Browse and then
// switch to it; a vault-wide finding (Path == "") has nowhere to open, so
// only the screen switch is emitted. A pure function of the Finding so it
// is testable without an engine at all — nothing acts on ui.OpenPathMsg
// until S4-T8 routes it in wave 4, which this package neither works
// around nor depends on.
func openFindingCmd(f lint.Finding) tea.Cmd {
	switchCmd := func() tea.Msg { return ui.SwitchScreenMsg{To: ui.ScreenBrowse} }
	if f.Path == "" {
		return switchCmd
	}
	openCmd := func() tea.Msg { return ui.OpenPathMsg{Path: f.Path} }
	return tea.Batch(openCmd, switchCmd)
}

// severityStyle returns the Theme style a check's severity renders with.
func (m *Model) severityStyle(sev lint.Severity) lipgloss.Style {
	switch sev {
	case lint.SevError:
		return m.theme.Bad
	case lint.SevWarn:
		return m.theme.Warn
	default:
		return m.theme.Muted
	}
}

// statusStyle returns the Theme style m.statusKind renders the status
// line with.
func (m *Model) statusStyle() lipgloss.Style {
	switch m.statusKind {
	case statusWarn:
		return m.theme.Warn
	case statusInfo:
		return m.theme.Muted
	default:
		return m.theme.Base
	}
}

// View renders the lint screen at exactly w by h (backbone §12) — no line
// wider than w, never assuming 80x24, never panicking at small or large
// sizes.
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
		body = fmt.Sprintf("lint: %v", m.loadErr)
	case !m.hasReport:
		body = "loading lint report..."
	default:
		body = m.renderBody(w, h)
	}
	return strings.Join(fitLines(body, w, h), "\n")
}

// renderBody lays out a summary header, the scrollable check/finding list,
// and a status line beneath when one is set.
func (m *Model) renderBody(w, h int) string {
	statusH := 0
	if m.status != "" {
		statusH = 1
	}
	listH := h - 1 - statusH
	if listH < 0 {
		listH = 0
	}

	info := len(m.report.Findings) - m.report.Errors - m.report.Warns
	header := m.theme.Title.Render(fitLine(fmt.Sprintf(
		"%d checks, %d findings (%d error(s), %d warning(s), %d info)",
		len(m.checks), len(m.report.Findings), m.report.Errors, m.report.Warns, info), w))

	lines := make([]string, len(m.rows))
	for i, r := range m.rows {
		lines[i] = m.renderRow(r, i == m.cursor, w)
	}
	visible := scrollWindow(lines, m.cursor, listH)

	rows := make([]string, 0, 2+listH)
	rows = append(rows, header)
	rows = append(rows, fitLines(strings.Join(visible, "\n"), w, listH)...)
	if statusH > 0 {
		rows = append(rows, m.statusStyle().Render(fitLine(m.status, w)))
	}
	return strings.Join(rows, "\n")
}

// renderRow renders one flattened row: a check header shows its ID(),
// Describe() and Severity(), plus either "clean" or its finding count
// (pinned item 1: "one row per check ... using ID(), Describe() and
// Severity()"); a finding row shows its location and message, indented
// beneath its check.
func (m *Model) renderRow(r row, selected bool, w int) string {
	c := m.checks[r.checkIdx]
	findings := m.report.ByCheck[c.ID()]

	var plain string
	var style lipgloss.Style
	if r.findingIdx < 0 {
		marker := "  "
		if len(findings) > 0 {
			if m.expanded[c.ID()] {
				marker = "▾ "
			} else {
				marker = "▸ "
			}
		}
		status := "clean"
		style = m.theme.Good
		if len(findings) > 0 {
			status = fmt.Sprintf("%d finding(s)", len(findings))
			style = m.severityStyle(c.Severity())
		}
		plain = fmt.Sprintf("%s%-18s %-6s %-46s %s", marker, c.ID(), c.Severity(), c.Describe(), status)
	} else if r.findingIdx < len(findings) {
		f := findings[r.findingIdx]
		loc := f.Path
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.Path, f.Line)
		}
		if loc == "" {
			loc = "(vault-wide)"
		}
		plain = fmt.Sprintf("    %s: %s", loc, f.Message)
		style = m.theme.Muted
	}

	if selected {
		style = m.theme.Selected
	}
	return style.Render(fitLine(plain, w))
}

// fitLine clips s to at most w display columns, ANSI-aware via lipgloss,
// and pads it with spaces up to exactly w when it is shorter.
func fitLine(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = lipgloss.NewStyle().MaxWidth(w).Render(s)
	if cur := lipgloss.Width(s); cur < w {
		s += strings.Repeat(" ", w-cur)
	}
	return s
}

// fitLines splits s on "\n" and returns exactly n lines, each fitLine'd to
// w: lines beyond n are dropped, missing ones come back blank padding.
func fitLines(s string, w, n int) []string {
	if n < 0 {
		n = 0
	}
	src := strings.Split(s, "\n")
	out := make([]string, n)
	for i := range out {
		var line string
		if i < len(src) {
			line = src[i]
		}
		out[i] = fitLine(line, w)
	}
	return out
}

// scrollWindow returns at most h consecutive lines from lines, positioned
// so index cursor is inside the window whenever the full list is longer
// than h.
func scrollWindow(lines []string, cursor, h int) []string {
	if h <= 0 || len(lines) == 0 {
		return nil
	}
	start := 0
	if cursor >= h {
		start = cursor - h + 1
	}
	if max := len(lines) - h; start > max {
		start = max
	}
	if start < 0 {
		start = 0
	}
	end := start + h
	if end > len(lines) {
		end = len(lines)
	}
	return lines[start:end]
}
