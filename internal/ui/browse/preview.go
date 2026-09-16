// preview.go feeds the selected page or raw source through the shared
// internal/ui/markdown renderer (workflow 003 contract §2/§7): the renderer
// draws the frontmatter header (title, meta line), then the body per block,
// each line padded to the exact width the preview panel's content area
// offers. The renderer memoizes its own output, so a large page is not
// re-rendered on every keypress.
package browse

import (
	"fmt"

	"github.com/awepo-pro/lw/internal/ui/markdown"
)

// previewLines renders n's full source (frontmatter included, so the
// renderer draws the title and meta header) at the given width. The width is
// the preview panel's content area, w-4; the renderer pads every line to
// exactly min(width, Measure+2) cells.
func (m *Model) previewLines(n *treeNode, width int) ([]string, error) {
	if m.deps.Engine == nil {
		return nil, nil
	}
	src, ok := m.sourceOf(n)
	if !ok {
		return nil, fmt.Errorf("browse: %s is no longer in the vault", n.Path)
	}
	if width < 1 {
		width = 1
	}
	return m.renderer.Render(src, markdown.Options{Width: width, Style: m.mdStyle()})
}

// sourceOf returns the full on-vault source of n — a page or raw source
// serialized the way the engine and `lw diff --render` see it — and whether
// n still resolves to content at all.
func (m *Model) sourceOf(n *treeNode) ([]byte, bool) {
	if m.deps.Engine == nil || n == nil || n.IsDir() {
		return nil, false
	}
	v := m.deps.Engine.Vault()
	switch n.Kind {
	case nodePage:
		if p, found := v.Page(n.Path); found {
			return p.Serialize(), true
		}
	case nodeRawSource:
		if r, found := v.RawSource(n.Path); found {
			return r.Serialize(), true
		}
	}
	return nil, false
}
