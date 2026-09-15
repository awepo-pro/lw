// keys.go is the lint screen's key handling and its optional shell
// interfaces (003 s2-screens.md T09, contract §5 §7): FooterHelper shapes
// the footer, OverlayHelper adds the screen's `Lint` section to the `?`
// overlay, and StatusReporter moves the transient message — v1 drew it as a
// status line inside the pane — into the shell's footer, where the frame
// puts it. enter and f have no KeyMap field of their own — ui.KeyMap belongs
// to the shell and gets no additions here — so they are described with
// ad-hoc bindings for display purposes only; only the movement bindings are
// matched through deps.Keys.
package lintview

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/ui"
)

// handleKey dispatches one tea.KeyPressMsg. A report that has not loaded
// yet makes every key a no-op — there is nothing to navigate or open.
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
		m.cursor = len(m.report.Findings) - 1
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
		m.setStatus(ui.StatusInfo, "requires the agent (M5)")
		return m, nil
	}
	return m, nil
}

// handleEnter dispatches `enter` on the finding under the cursor: the shell
// is asked to open its page in Browse and switch to it (pinned item 2).
func (m *Model) handleEnter() (ui.Pane, tea.Cmd) {
	f, ok := m.finding()
	if !ok {
		return m, nil
	}
	return m, openFindingCmd(f)
}

// openFindingCmd is `enter` on a finding (pinned item 2, C-108/D-CU): a
// page-scoped finding asks the shell to open its Path in Browse and then
// switch to it; a vault-wide finding (Path == "") has nowhere to open, so
// only the screen switch is emitted. A pure function of the Finding so it
// is testable without an engine at all — nothing acts on ui.OpenPathMsg
// until the shell routes it, which this package neither works around nor
// depends on.
func openFindingCmd(f lint.Finding) tea.Cmd {
	switchCmd := func() tea.Msg { return ui.SwitchScreenMsg{To: ui.ScreenBrowse} }
	if f.Path == "" {
		return switchCmd
	}
	openCmd := func() tea.Msg { return ui.OpenPathMsg{Path: f.Path} }
	return tea.Batch(openCmd, switchCmd)
}

// setStatus records the message Status reports until the next key handler
// replaces or clears it.
func (m *Model) setStatus(level ui.StatusLevel, msg string) {
	m.statusLevel = level
	m.status = msg
}

// Help returns the lint screen's key bindings (backbone §12). The shell
// prefers FooterHelper's list when the pane implements it — which this pane
// does — so this is the pane's binding vocabulary, not what the footer
// draws.
func (m *Model) Help() []key.Binding {
	k := m.deps.Keys
	return []key.Binding{
		k.MoveDown, k.MoveUp, k.Top, k.Bottom,
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "fix")),
	}
}

// FooterHelp is the footer's binding list (contract §5): today's bindings,
// in today's order, with the merged movement labels the shell's KeyMap
// carries (contract §4: MoveDown's help is "j/k move", Top's is "g/G
// top/bottom", and a pane omits MoveUp and Bottom from its footer list) and
// one-word labels where the old ones were longer.
func (m *Model) FooterHelp() []key.Binding {
	k := m.deps.Keys
	return []key.Binding{
		k.MoveDown, // j/k move
		k.Top,      // g/G top/bottom
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")), // was "expand / open finding"
		key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "fix")),          // was "ask agent to fix"
	}
}

// OverlayHelp is the pane's own section of the `?` overlay (contract §5):
// titled Lint, the same bindings the footer shows, with the pair labels the
// other screens' overlay sections use.
func (m *Model) OverlayHelp() (string, []ui.HelpEntry) {
	return "Lint", []ui.HelpEntry{
		{Key: "j/k", Desc: "down / up"},
		{Key: "g/G", Desc: "top / bottom"},
		{Key: "enter", Desc: "open"},
		{Key: "f", Desc: "fix"},
	}
}

// Status is the pane's transient message (contract §5): the shell renders
// it in the footer, styled by level, while it is non-empty. The pane itself
// draws no status line.
func (m *Model) Status() (string, ui.StatusLevel) {
	return m.status, m.statusLevel
}
