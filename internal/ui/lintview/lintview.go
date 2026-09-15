// lintview.go holds the lint screen's model and its report pipeline
// (backbone §12 Pane, §4 Check/Report; 003 s2-screens.md T09): the screen
// runs lint.All() over the engine's vault and shows the findings as one
// flat, focused Findings panel — one row per finding, severity as a
// coloured glyph — replacing v1's expandable per-check checklist.
//
// Rendering lives in view.go, keys and the shell's optional interfaces in
// keys.go.
//
// Every mutation this screen could cause still goes through stage.Engine
// or the shell's own message vocabulary — nothing here writes to the
// vault, and `f` is a message, not a repair (00-conventions.md §5.4;
// /docs/design.md §9.4: even repairs go through review).
package lintview

import (
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// errNoEngine is what runReportCmd reports when this pane was constructed
// with a nil Deps.Engine (a headless shell with no vault at all, matching
// S4-T2's own "constructible headless" contract) — rendered visibly, never
// panicking.
var errNoEngine = errors.New("lintview: no engine loaded")

// Model is the lint screen (backbone §12 ui.Pane): one flat list of the
// report's findings, navigated with the cursor, `enter` opening a finding's
// page in Browse.
type Model struct {
	deps  ui.Deps
	theme ui.Theme // C-81: a copy taken at construction, rebuilt locally on tea.BackgroundColorMsg

	checks []lint.Check // lint.All(), fixed at 14 (C-85/D-V); the fixed set the report runs

	hasReport bool
	loadErr   error
	report    lint.Report

	cursor int // index into report.Findings

	status      string
	statusLevel ui.StatusLevel
}

var _ ui.Pane = (*Model)(nil)

// New constructs the lint screen (backbone §12). It captures d and
// lint.All()'s fixed check list; Init is what runs the report.
func New(d ui.Deps) ui.Pane {
	return &Model{
		deps:   d,
		theme:  d.Theme,
		checks: lint.All(),
	}
}

// Title returns the pane's name for the shell's tab bar (backbone §12).
func (m *Model) Title() string { return "Lint" }

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

	case tea.ColorProfileMsg:
		// C-81 again, for the profile: the cursor tint is the one token
		// that depends on it (contract §5 frame note 8, W5 F1).
		m.theme = m.theme.WithProfile(msg.Profile)
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

// applyReport installs a runReportCmd result onto m. The report's Findings
// are already sorted (backbone §4: Path, then Line, then Check) and are the
// pane's rows in that order.
func (m *Model) applyReport(msg reportMsg) {
	if msg.err != nil {
		m.loadErr = msg.err
		m.hasReport = false
		return
	}
	m.hasReport = true
	m.loadErr = nil
	m.report = msg.report
	m.clampCursor()
}

// clampCursor keeps m.cursor inside [0, len(m.report.Findings)-1], or 0
// when there are no findings. A fresh report never resets the cursor, so a
// reload that changes the finding count does not jump the view.
func (m *Model) clampCursor() {
	n := len(m.report.Findings)
	if n == 0 {
		m.cursor = 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= n {
		m.cursor = n - 1
	}
}

// finding returns the finding under the cursor, or false when the report
// has not loaded or holds no findings.
func (m *Model) finding() (lint.Finding, bool) {
	if !m.hasReport || m.cursor < 0 || m.cursor >= len(m.report.Findings) {
		return lint.Finding{}, false
	}
	return m.report.Findings[m.cursor], true
}
