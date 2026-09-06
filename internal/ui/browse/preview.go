// preview.go renders a page or raw source's body with glamour, reflowed to
// the pane's current width, and memoizes the result so a large page is not
// re-rendered on every keypress (s4-tui.md S4-T4 items 5, 7, 8).
package browse

import (
	"fmt"
	"strings"

	glamour "charm.land/glamour/v2"
)

// previewMaxLines caps how many lines of a body are ever fed to glamour.
// s4-tui.md S4-T4 item 7: truncate, never block, on the vault's largest
// pages. spec/fixtures/dirty/wiki/concepts/long-page.md is 250 lines and is
// the fixture that proves this.
const previewMaxLines = 200

// truncateBody returns body's first previewMaxLines lines, with a visible
// "… N more lines" marker appended when body had more than that. A body at
// or under the limit is returned unchanged.
func truncateBody(body string) string {
	lines := strings.Split(body, "\n")
	if len(lines) <= previewMaxLines {
		return body
	}
	kept := strings.Join(lines[:previewMaxLines], "\n")
	more := len(lines) - previewMaxLines
	return fmt.Sprintf("%s\n\n… %d more lines\n", kept, more)
}

// glamourStyle picks glamour v2's built-in style name for the given
// polarity. There is no glamour.WithAutoStyle in v2 (s4-tui.md S4-T4 item 5;
// backbone §12 C-81) — the caller must choose.
func glamourStyle(isDark bool) string {
	if isDark {
		return "dark"
	}
	return "light"
}

// renderMarkdown renders src with glamour reflowed to width using the named
// standard style (s4-tui.md S4-T4 item 5). It is a plain function — never a
// package-level var — so it can be substituted per *Model instance via
// Model.renderMarkdownFn without introducing mutable package state
// (00-conventions.md §2).
func renderMarkdown(style string, width int, src string) (string, error) {
	if width < 1 {
		width = 1
	}
	r, err := glamour.NewTermRenderer(glamour.WithStandardStyle(style), glamour.WithWordWrap(width))
	if err != nil {
		return "", err
	}
	return r.Render(src)
}

// previewKey is preview memoization's cache key: a rendered page or raw
// source is a pure function of its path and the pane's current width
// (s4-tui.md S4-T4 item 8).
type previewKey struct {
	path  string
	width int
}

// previewCache memoizes rendered preview output by (path, width).
type previewCache struct {
	entries map[previewKey]string
}

// newPreviewCache returns an empty previewCache.
func newPreviewCache() *previewCache {
	return &previewCache{entries: map[previewKey]string{}}
}

// invalidate clears every cached entry. Called on ui.VaultReloadedMsg (the
// underlying body may have changed) and on a theme polarity flip (the same
// body now renders with different colors) — s4-tui.md S4-T4 items 6 and 8.
func (c *previewCache) invalidate() {
	c.entries = map[previewKey]string{}
}

// render returns the cached rendering for (path, width), truncating src and
// calling renderFn to produce it when no entry exists yet. renderFn is
// expected to have already folded in glamour's own error-fallback (see
// Model.renderBody) — this cache does not itself distinguish an error from a
// plain-text result, it only ever stores what renderFn returns.
func (c *previewCache) render(path string, width int, src string, renderFn func(truncated string) string) string {
	key := previewKey{path: path, width: width}
	if out, ok := c.entries[key]; ok {
		return out
	}
	out := renderFn(truncateBody(src))
	c.entries[key] = out
	return out
}

// backlinkStripEntries is how many distinct backlinking pages the strip
// under the preview shows before collapsing the rest into "+N more"
// (s4-tui.md S4-T4 item 9).
const backlinkStripEntries = 3

// renderBody renders body for path at width, through the preview cache
// (s4-tui.md S4-T4 items 5, 7, 8). A glamour renderer error falls back to the
// plain, truncated body — never a panic, never a log line
// (00-conventions.md §2).
func (m *Model) renderBody(path string, width int, body string) string {
	style := glamourStyle(m.deps.Theme.IsDark)
	return m.cache.render(path, width, body, func(truncated string) string {
		out, err := m.renderMarkdownFn(style, width, truncated)
		if err != nil {
			return truncated
		}
		return out
	})
}

// renderBacklinks renders the distinct pages that link to path, in
// Theme.Muted, capped to backlinkStripEntries with a "+N more" line when it
// overflows (s4-tui.md S4-T4 item 9). No backlinks is a legitimate state — it
// returns nil, not an error and not a placeholder line.
func (m *Model) renderBacklinks(path string) []string {
	if m.deps.Engine == nil {
		return nil
	}
	refs := m.deps.Engine.Vault().Graph().Backlinks(path)

	var froms []string
	seen := map[string]bool{}
	for _, r := range refs {
		if !seen[r.From] {
			seen[r.From] = true
			froms = append(froms, r.From)
		}
	}
	if len(froms) == 0 {
		return nil
	}

	lines := []string{m.deps.Theme.Muted.Render("Backlinks:")}
	shown := froms
	overflow := 0
	if len(shown) > backlinkStripEntries {
		overflow = len(shown) - backlinkStripEntries
		shown = shown[:backlinkStripEntries]
	}
	for _, f := range shown {
		lines = append(lines, m.deps.Theme.Muted.Render("  "+f))
	}
	if overflow > 0 {
		lines = append(lines, m.deps.Theme.Muted.Render(fmt.Sprintf("  +%d more", overflow)))
	}
	return lines
}

// renderMainLines renders the preview and backlinks strip for whatever node
// the tree cursor is on, or a placeholder when it is a directory or has no
// readable content.
func (m *Model) renderMainLines(w, h int) []string {
	n := m.selectedNode()
	if n == nil || n.IsDir() {
		return []string{m.deps.Theme.Faint.Render("(select a page to preview)")}
	}

	body, ok := m.selectedBody(n)
	if !ok {
		return []string{m.deps.Theme.Faint.Render("(no content)")}
	}

	backlinks := m.renderBacklinks(n.Path)
	previewH := h - len(backlinks)
	if previewH < 0 {
		previewH = 0
	}

	rendered := m.renderBody(n.Path, w, body)
	previewLines := strings.Split(rendered, "\n")
	if len(previewLines) > previewH {
		previewLines = previewLines[:previewH]
	}

	out := make([]string, 0, len(previewLines)+len(backlinks))
	out = append(out, previewLines...)
	out = append(out, backlinks...)
	return out
}
