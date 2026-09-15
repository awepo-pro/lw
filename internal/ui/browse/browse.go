package browse

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/markdown"
)

// Model is the browse screen's ui.Pane implementation (contract §7): the
// optional shell interfaces — FooterHelper, OverlayHelper, StatusReporter and
// TextCapturer — are implemented on it; the shell discovers each by type
// assertion, so none of them appears in the exported constructor surface.
type Model struct {
	deps ui.Deps

	// renderer is the shared vault-markdown renderer, created once here and
	// kept on the model (contract §7: no package-level state). Its own LRU
	// memo is keyed on source, width and polarity, so a theme flip needs no
	// invalidation here.
	renderer *markdown.Renderer

	tree     []*treeNode
	expanded map[string]bool
	visible  []*treeNode
	cursor   int

	finder finderState

	// status is the transient StatusReporter message (contract §5): a finder
	// error, shown in the footer until the next key press.
	status      string
	statusLevel ui.StatusLevel
}

var _ ui.Pane = (*Model)(nil)

// New constructs the browse screen (contract §7). d.Engine may be nil (a
// headless shell with no vault at all) — the tree is then just the two empty
// roots, and the preview, Links panel and finder render their empty states.
//
// On open the cursor sits on the first file row, not the first visible node
// (C8): the selection is a page or raw source, so the preview and Links
// panels show content immediately.
func New(d ui.Deps) ui.Pane {
	m := &Model{
		deps:     d,
		renderer: markdown.NewRenderer(),
	}
	if d.Engine != nil {
		m.rebuildTree()
	} else {
		m.tree = buildTree(nil, nil)
		m.expanded = defaultExpanded(m.tree)
		m.refreshVisible()
	}
	return m
}

// Init returns no command: the tree is already built from d.Engine's vault
// at construction time, and there is nothing else to load asynchronously.
func (m *Model) Init() tea.Cmd { return nil }

// Title returns the screen's name for the shell's tab/help chrome.
func (m *Model) Title() string { return "Browse" }

// Help returns the browse screen's key bindings. The shell shows
// FooterHelp's list instead while this pane is active (contract §5); Help
// remains the Pane interface's fallback.
func (m *Model) Help() []key.Binding { return m.FooterHelp() }

// FooterHelp is the footer's binding list, in the frozen order
// (s2-screens.md T07); the shell appends `tab screen` and `q quit` (Browse
// does not capture text) after them, then "? help", and drops whole
// bindings from the end when the row is too narrow (ORCH-13/D-3T).
func (m *Model) FooterHelp() []key.Binding {
	return []key.Binding{
		m.deps.Keys.MoveDown, // help "j/k", "move"
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		key.NewBinding(key.WithKeys("h", "l"), key.WithHelp("h/l", "collapse/expand")),
		key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "find")),
		m.deps.Keys.Top, // help "g/G", "top/bottom"
	}
}

// OverlayHelp is the Browse section of the shell's `?` overlay
// (s2-screens.md T07).
func (m *Model) OverlayHelp() (string, []ui.HelpEntry) {
	return "Browse", []ui.HelpEntry{
		{Key: "j/k", Desc: "down / up"},
		{Key: "enter", Desc: "open / toggle"},
		{Key: "h/l", Desc: "collapse / expand"},
		{Key: "/", Desc: "find"},
		{Key: "g/G", Desc: "top / bottom"},
	}
}

// Status is the pane's transient message (contract §5): a finder error,
// until the next key press clears it.
func (m *Model) Status() (string, ui.StatusLevel) {
	return m.status, m.statusLevel
}

// CapturesText reports whether the finder is taking text input (contract §5
// TextCapturer, C27): while it is open, the shell delivers every printable
// key — `q` and `?` included — straight here instead of matching them
// against the global quit/help bindings.
func (m *Model) CapturesText() bool {
	return m.finder.open
}

// Update handles the shell's broadcast messages and key presses.
func (m *Model) Update(msg tea.Msg) (ui.Pane, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		// The shell rebuilds its own Theme on this message; Deps.Theme
		// handed to New was a copy taken once, so this screen must rebuild
		// its own copy too. The renderer's memo is keyed on polarity, so
		// nothing cached needs dropping here.
		m.deps.Theme = m.deps.Theme.WithDark(msg.IsDark())
		return m, nil

	case ui.VaultReloadedMsg:
		m.reload()
		return m, nil

	case ui.OpenPathMsg:
		// C-108/D-CU: Lint's `enter` landing on the right page. selectPath
		// force-opens every ancestor directory, then moves the cursor onto
		// msg.Path when the tree holds it; a path the vault no longer holds
		// is a no-op on the cursor.
		m.selectPath(msg.Path)
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// setStatus records the pane's transient footer message.
func (m *Model) setStatus(msg string, level ui.StatusLevel) {
	m.status, m.statusLevel = msg, level
}

// handleKey dispatches one key press. Any key clears the transient status
// message first; the handlers below may set a new one. The finder, when
// open, consumes every key that reaches the pane itself.
func (m *Model) handleKey(msg tea.KeyPressMsg) (ui.Pane, tea.Cmd) {
	m.status, m.statusLevel = "", ui.StatusInfo

	if m.finder.open {
		m.handleFinderKey(msg)
		return m, nil
	}

	switch {
	case key.Matches(msg, m.deps.Keys.MoveDown):
		m.moveCursor(1)
	case key.Matches(msg, m.deps.Keys.MoveUp):
		m.moveCursor(-1)
	case key.Matches(msg, m.deps.Keys.Top):
		m.cursor = 0
	case key.Matches(msg, m.deps.Keys.Bottom):
		m.cursor = len(m.visible) - 1
	default:
		switch msg.String() {
		case "h", "left":
			m.collapseCurrent()
		case "l", "right":
			m.expandCurrent()
		case "enter":
			m.toggleOrOpen()
		case "/":
			m.openFinder()
		}
	}
	m.clampCursor()
	return m, nil
}

// moveCursor shifts the tree cursor by delta, clamped to the visible list.
func (m *Model) moveCursor(delta int) {
	m.cursor += delta
	m.clampCursor()
}

// clampCursor keeps m.cursor inside [0, len(m.visible)-1], or 0 when the
// visible list is empty.
func (m *Model) clampCursor() {
	if len(m.visible) == 0 {
		m.cursor = 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.visible) {
		m.cursor = len(m.visible) - 1
	}
}

// selectedNode returns the node currently under the tree cursor, or nil when
// the visible list is empty.
func (m *Model) selectedNode() *treeNode {
	if m.cursor < 0 || m.cursor >= len(m.visible) {
		return nil
	}
	return m.visible[m.cursor]
}

// collapseCurrent is "h"/"left": it collapses the current node if it is an
// expanded directory, otherwise moves the cursor up to its parent directory.
func (m *Model) collapseCurrent() {
	n := m.selectedNode()
	if n == nil {
		return
	}
	if n.IsDir() && m.expanded[n.Path] {
		m.expanded[n.Path] = false
		m.refreshVisible()
		return
	}
	if parent := parentPath(n.Path); parent != "" {
		m.selectPath(parent)
	}
}

// expandCurrent is "l"/"right": it expands the current node if it is a
// directory. A file is left untouched.
func (m *Model) expandCurrent() {
	n := m.selectedNode()
	if n == nil || !n.IsDir() {
		return
	}
	m.expanded[n.Path] = true
	m.refreshVisible()
}

// toggleOrOpen is "enter": on a directory it toggles expand/collapse; on a
// page or raw source it "opens" it — which, since the preview and Links
// panels already track whatever the cursor sits on, means there is nothing
// further to change. The branch exists so the directory case is explicit
// rather than falling through silently.
func (m *Model) toggleOrOpen() {
	n := m.selectedNode()
	if n == nil {
		return
	}
	if n.IsDir() {
		m.expanded[n.Path] = !m.expanded[n.Path]
		m.refreshVisible()
	}
}

// refreshVisible recomputes m.visible from m.tree and m.expanded, then
// clamps the cursor back into range.
func (m *Model) refreshVisible() {
	m.visible = visibleNodes(m.tree, m.expanded)
	m.clampCursor()
}

// selectPath moves the tree cursor onto p, forcing every ancestor directory
// open first so p is guaranteed to be in the visible list. It reports
// whether p was found in the tree at all.
func (m *Model) selectPath(p string) bool {
	for _, d := range ancestorDirs(p) {
		m.expanded[d] = true
	}
	m.refreshVisible()
	for i, n := range m.visible {
		if n.Path == p {
			m.cursor = i
			return true
		}
	}
	return false
}

// reload rebuilds the tree from the vault after ui.VaultReloadedMsg. It
// never assumes it saw every reload — an inactive pane can miss this message
// entirely, which is fine: the tree is simply stale until the next one
// arrives, not wrong in a way that corrupts state.
func (m *Model) reload() {
	if m.deps.Engine != nil {
		m.rebuildTree()
	}
}

// rebuildTree re-reads the vault's page and raw-source paths and rebuilds
// the tree. A rebuild that still holds the current selection keeps it;
// otherwise — the first build included — the cursor lands on the first file
// row (C8), never on a directory node.
func (m *Model) rebuildTree() {
	v := m.deps.Engine.Vault()

	pages := v.Pages()
	pagePaths := make([]string, len(pages))
	for i, p := range pages {
		pagePaths[i] = p.Path
	}
	rawSources := v.RawSources()
	rawPaths := make([]string, len(rawSources))
	for i, r := range rawSources {
		rawPaths[i] = r.Path
	}

	selected := ""
	if n := m.selectedNode(); n != nil {
		selected = n.Path
	}

	m.tree = buildTree(pagePaths, rawPaths)
	m.expanded = defaultExpanded(m.tree)
	m.refreshVisible()

	if selected == "" || !m.selectPath(selected) {
		m.cursorToFirstFile()
	}
}

// cursorToFirstFile moves the cursor onto the first file row of the visible
// list (C8), leaving it where clampCursor puts it when the tree holds no
// file at all.
func (m *Model) cursorToFirstFile() {
	for i, n := range m.visible {
		if !n.IsDir() {
			m.cursor = i
			return
		}
	}
	m.cursor = 0
}
