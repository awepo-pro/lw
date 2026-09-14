// Package browse is the vault browser screen: a directory tree of wiki pages
// and raw sources alongside a glamour-rendered preview, a backlinks strip,
// and a `/` fuzzy finder (backbone §12, /.dev-notes/PLAN-v1.md §9, s4-tui.md S4-T4).
package browse

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/awepo-pro/lw/internal/ui"
)

// minTreeWidth is the smallest column width the tree side of the screen is
// given, so long file names still get some room to render; the column
// shrinks below this only when the whole pane is narrower still.
const minTreeWidth = 16

// finderState holds the `/` fuzzy finder's transient state (s4-tui.md S4-T4
// item 10). It is its own struct, rather than fields on Model, so opening and
// closing the finder is one assignment.
type finderState struct {
	open      bool
	query     string
	matches   []findMatch
	cursor    int // index into matches
	preCursor int // Model.cursor to restore on esc, leaving the tree where it was
}

// Model is the browse screen's ui.Pane implementation.
type Model struct {
	deps ui.Deps

	tree     []*treeNode
	expanded map[string]bool
	visible  []*treeNode
	cursor   int

	finder finderState

	cache            *previewCache
	renderMarkdownFn func(style string, width int, src string) (string, error)

	width, height int
}

var _ ui.Pane = (*Model)(nil)

// New constructs the browse screen (backbone §12). d.Engine may be nil (a
// headless shell with no vault at all, matching S4-T2's own
// "constructible headless" contract) — the tree is then just the two empty
// roots, and the preview/backlinks/finder all render their empty states.
func New(d ui.Deps) ui.Pane {
	m := &Model{
		deps:             d,
		cache:            newPreviewCache(),
		renderMarkdownFn: renderMarkdown,
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

// Help returns the browse screen's key bindings (backbone §12). h/l, enter
// and / have no KeyMap field of their own (s4-tui.md S4-T4 item 4 — ui.KeyMap
// belongs to the shell and gets no additions here), so they are described
// with ad-hoc bindings for display purposes only; only MoveDown/MoveUp/
// Top/Bottom are matched through d.Keys.
func (m *Model) Help() []key.Binding {
	return []key.Binding{
		m.deps.Keys.MoveDown,
		m.deps.Keys.MoveUp,
		m.deps.Keys.Top,
		m.deps.Keys.Bottom,
		key.NewBinding(key.WithKeys("h", "left"), key.WithHelp("h", "collapse")),
		key.NewBinding(key.WithKeys("l", "right"), key.WithHelp("l", "expand")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "find")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close find")),
	}
}

// Update handles the shell's broadcast messages and key presses (backbone
// §12; C-80: matched against tea.KeyPressMsg, never tea.KeyMsg).
func (m *Model) Update(msg tea.Msg) (ui.Pane, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		// C-81: the shell rebuilds its own Theme on this message; Deps.Theme
		// handed to New was a copy taken once, so this screen must rebuild
		// its own copy too and drop anything cached under the old polarity
		// (s4-tui.md S4-T4 item 6).
		m.deps.Theme = m.deps.Theme.WithDark(msg.IsDark())
		m.cache.invalidate()
		return m, nil

	case ui.VaultReloadedMsg:
		m.reload()
		return m, nil

	case ui.OpenPathMsg:
		// C-108/D-CU: Lint's `enter` landing on the right page. selectPath
		// already does exactly what this needs — force every ancestor
		// directory open, then move the cursor onto msg.Path if it is in
		// the tree — and its own no-op-when-absent behavior (a bool return,
		// never a panic) is exactly the contract this handler must have
		// for a path the vault no longer holds.
		m.selectPath(msg.Path)
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// handleKey dispatches one key press: the finder, when open, consumes every
// key itself; otherwise tree navigation.
func (m *Model) handleKey(msg tea.KeyPressMsg) (ui.Pane, tea.Cmd) {
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

// collapseCurrent is "h"/"left" (s4-tui.md S4-T4 item 4): it collapses the
// current node if it is an expanded directory, otherwise moves the cursor up
// to its parent directory.
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

// expandCurrent is "l"/"right" (s4-tui.md S4-T4 item 4): it expands the
// current node if it is a directory. A file is left untouched.
func (m *Model) expandCurrent() {
	n := m.selectedNode()
	if n == nil || !n.IsDir() {
		return
	}
	m.expanded[n.Path] = true
	m.refreshVisible()
}

// toggleOrOpen is "enter" (s4-tui.md S4-T4 item 4): on a directory it toggles
// expand/collapse; on a page or raw source it "opens" it — which, since the
// preview already tracks whatever the cursor sits on, means there is nothing
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

// reload rebuilds the tree from the vault and clears the preview cache,
// after ui.VaultReloadedMsg (s4-tui.md S4-T4 item 8). It never assumes it saw
// every reload — TD-4/C-106 means an inactive pane can miss this message
// entirely, which is fine: the tree is simply stale until the next one
// arrives, not wrong in a way that corrupts state.
func (m *Model) reload() {
	m.cache.invalidate()
	if m.deps.Engine != nil {
		m.rebuildTree()
	}
}

// rebuildTree re-reads the vault's page and raw-source paths, rebuilds the
// tree, and tries to keep the current selection (falling back to the first
// node when it no longer exists).
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
		m.cursor = 0
		m.clampCursor()
	}
}

// selectedBody returns n's rendered-source body (a page's Body or a raw
// source's Body) read live from the vault, and whether n resolved to
// content at all — false for a directory node, or a leaf the vault no longer
// holds.
func (m *Model) selectedBody(n *treeNode) (body string, ok bool) {
	if m.deps.Engine == nil || n == nil {
		return "", false
	}
	v := m.deps.Engine.Vault()
	switch n.Kind {
	case nodePage:
		if p, found := v.Page(n.Path); found {
			return p.Body, true
		}
	case nodeRawSource:
		if r, found := v.RawSource(n.Path); found {
			return r.Body, true
		}
	}
	return "", false
}

// View renders the screen at exactly w columns by h rows (backbone §12): the
// tree on the left, the preview and backlinks strip on the right — or the
// finder overlay, full-pane, when it is open.
func (m *Model) View(w, h int) string {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	m.width, m.height = w, h

	if m.finder.open {
		return m.renderFinder(w, h)
	}

	treeW := w / 3
	if treeW < minTreeWidth {
		treeW = minTreeWidth
	}
	if treeW > w {
		treeW = w
	}
	sepW := 0
	if treeW < w {
		sepW = 1
	}
	mainW := w - treeW - sepW

	treeLines := m.renderTreeLines(treeW, h)
	mainLines := m.renderMainLines(mainW, h)

	rows := make([]string, h)
	for i := 0; i < h; i++ {
		var left, right string
		if i < len(treeLines) {
			left = treeLines[i]
		}
		if i < len(mainLines) {
			right = mainLines[i]
		}
		line := clipPad(left, treeW)
		if sepW > 0 {
			line += m.deps.Theme.Border.Render("│")
		}
		line += clipPad(right, mainW)
		rows[i] = line
	}
	return strings.Join(rows, "\n")
}

// renderTreeLines renders the visible tree as one line per node, indented by
// depth (strings.Count(n.Path, "/") — see treeNode's doc comment) and marked
// with the cursor's row highlighted, then scrolled so the cursor stays in
// view within h rows.
func (m *Model) renderTreeLines(w, h int) []string {
	lines := make([]string, len(m.visible))
	for i, n := range m.visible {
		depth := strings.Count(n.Path, "/")
		indent := strings.Repeat("  ", depth)

		marker := "  "
		if n.IsDir() {
			if m.expanded[n.Path] {
				marker = "▾ "
			} else {
				marker = "▸ "
			}
		}

		name := n.Name
		if !n.IsDir() {
			name = strings.TrimSuffix(name, ".md")
		}

		line := indent + marker + name
		if i == m.cursor {
			line = m.deps.Theme.Selected.Render(line)
		}
		lines[i] = clipPad(line, w)
	}
	return scrollWindow(lines, m.cursor, h)
}

// clipPad clips s to at most w display columns and pads it with spaces up to
// exactly w when it is shorter, ANSI-aware via lipgloss so a styled line is
// never cut mid-escape-sequence.
func clipPad(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = lipgloss.NewStyle().MaxWidth(w).Render(s)
	if cur := lipgloss.Width(s); cur < w {
		s += strings.Repeat(" ", w-cur)
	}
	return s
}

// scrollWindow returns at most h consecutive lines from lines, positioned so
// index cursor is inside the window whenever the full list is longer than h.
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

// openFinder opens the `/` fuzzy finder (s4-tui.md S4-T4 item 10),
// remembering the current cursor so esc can restore it untouched.
func (m *Model) openFinder() {
	m.finder = finderState{open: true, preCursor: m.cursor}
	m.refreshFinderMatches()
}

// handleFinderKey handles one key press while the finder is open. Every key
// not named here is treated as typed text appended to the query.
func (m *Model) handleFinderKey(msg tea.KeyPressMsg) {
	switch msg.String() {
	case "esc":
		m.cursor = m.finder.preCursor
		m.clampCursor()
		m.finder = finderState{}
		return
	case "enter":
		m.commitFinder()
		return
	case "backspace":
		if r := []rune(m.finder.query); len(r) > 0 {
			m.finder.query = string(r[:len(r)-1])
			m.refreshFinderMatches()
		}
		return
	case "up":
		if m.finder.cursor > 0 {
			m.finder.cursor--
		}
		return
	case "down":
		if m.finder.cursor < len(m.finder.matches)-1 {
			m.finder.cursor++
		}
		return
	}

	// A plain printable key (no ctrl/alt) types into the query.
	if msg.Text != "" && msg.Mod&^tea.ModShift == 0 {
		m.finder.query += msg.Text
		m.refreshFinderMatches()
	}
}

// commitFinder moves the tree cursor to the selected match and opens it
// (s4-tui.md S4-T4 item 10), then closes the finder.
func (m *Model) commitFinder() {
	if len(m.finder.matches) == 0 {
		m.finder = finderState{}
		return
	}
	target := m.finder.matches[m.finder.cursor].path
	m.finder = finderState{}
	m.selectPath(target)
}

// refreshFinderMatches re-runs fuzzyFind against the current query and
// clamps the finder's own cursor back into range.
func (m *Model) refreshFinderMatches() {
	m.finder.matches = fuzzyFind(m.finder.query, m.findItems())
	if m.finder.cursor >= len(m.finder.matches) {
		m.finder.cursor = len(m.finder.matches) - 1
	}
	if m.finder.cursor < 0 {
		m.finder.cursor = 0
	}
}

// findItems returns every page and raw source's (path, display title) pair
// the finder searches over (s4-tui.md S4-T4 item 10).
func (m *Model) findItems() []findItem {
	if m.deps.Engine == nil {
		return nil
	}
	v := m.deps.Engine.Vault()
	pages := v.Pages()
	rawSources := v.RawSources()

	items := make([]findItem, 0, len(pages)+len(rawSources))
	for _, p := range pages {
		items = append(items, findItem{path: p.Path, title: p.FM.Title})
	}
	for _, r := range rawSources {
		items = append(items, findItem{path: r.Path, title: r.Title})
	}
	return items
}

// renderFinder renders the finder overlay: the query on the first line, then
// every match with the finder's own cursor highlighted.
func (m *Model) renderFinder(w, h int) string {
	lines := make([]string, 0, len(m.finder.matches)+1)
	lines = append(lines, m.deps.Theme.Title.Render("/"+m.finder.query))

	for i, match := range m.finder.matches {
		line := match.path
		if match.title != "" {
			line = fmt.Sprintf("%s  (%s)", line, match.title)
		}
		if i == m.finder.cursor {
			line = m.deps.Theme.Selected.Render(line)
		}
		lines = append(lines, line)
	}

	if len(lines) > h {
		lines = lines[:h]
	}
	for i, l := range lines {
		lines[i] = clipPad(l, w)
	}
	return strings.Join(lines, "\n")
}
