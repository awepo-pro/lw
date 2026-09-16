// messages_test.go covers the pane's shell-broadcast handlers
// (browse.go's Update): ui.VaultReloadedMsg rebuilding the tree and the
// ui.OpenPathMsg pair (C-108/D-CU, Lint's `enter` landing on the right
// page).
package browse

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/awepo-pro/lw/internal/ui"
)

// TestVaultReloadedMsgRebuildsTree: a page written to disk after
// construction appears in the tree once ui.VaultReloadedMsg arrives.
func TestVaultReloadedMsgRebuildsTree(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	p := New(d)
	m := p.(*Model)
	for _, pth := range allPaths(m.tree) {
		if pth == "wiki/concepts/new-page.md" {
			t.Fatal("new-page.md unexpectedly already present before it was written")
		}
	}

	const newPage = "---\n" +
		"title: New Page\n" +
		"created: 2026-08-20\n" +
		"updated: 2026-08-20\n" +
		"type: concept\n" +
		"---\n" +
		"\n" +
		"# New Page\n" +
		"\n" +
		"Body.\n"
	dst := filepath.Join(d.Engine.Vault().Root(), "wiki", "concepts", "new-page.md")
	if err := os.WriteFile(dst, []byte(newPage), 0o644); err != nil {
		t.Fatalf("write new page: %v", err)
	}
	if err := d.Engine.Vault().Reload(); err != nil {
		t.Fatalf("Vault.Reload: %v", err)
	}

	next, _ := m.Update(ui.VaultReloadedMsg{})
	m2 := next.(*Model)

	found := false
	for _, pth := range allPaths(m2.tree) {
		if pth == "wiki/concepts/new-page.md" {
			found = true
		}
	}
	if !found {
		t.Fatalf("tree after VaultReloadedMsg does not contain the new page: %v", allPaths(m2.tree))
	}
}

// TestOpenPathMsgSelectsAndExpandsAncestors is C-108/D-CU's Browse-side
// half: the shell delivers ui.OpenPathMsg to this pane so Lint's `enter`
// lands on the right page. The ancestor directory is collapsed first, so
// the test also proves the handler expands it rather than merely matching a
// node already visible.
func TestOpenPathMsgSelectsAndExpandsAncestors(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	p := New(d)
	m := p.(*Model)
	m.expanded["wiki/concepts"] = false
	m.refreshVisible()
	for _, n := range m.visible {
		if n.Path == "wiki/concepts/kv-cache.md" {
			t.Fatal("kv-cache.md unexpectedly visible while its parent is collapsed")
		}
	}

	next, cmd := m.Update(ui.OpenPathMsg{Path: "wiki/concepts/kv-cache.md"})
	if cmd != nil {
		t.Fatalf("Update(OpenPathMsg) returned a non-nil Cmd: %v", cmd())
	}
	m2 := next.(*Model)

	if !m2.expanded["wiki/concepts"] {
		t.Fatal("wiki/concepts was not expanded after OpenPathMsg")
	}
	n := m2.selectedNode()
	if n == nil || n.Path != "wiki/concepts/kv-cache.md" {
		t.Fatalf("selectedNode() after OpenPathMsg = %+v, want wiki/concepts/kv-cache.md", n)
	}
}

// TestOpenPathMsgUnknownPathIsNoOp: a path the vault does not hold (a stale
// finding, or one from before a revert) must leave the tree cursor exactly
// where it was, never panic.
func TestOpenPathMsgUnknownPathIsNoOp(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	p := New(d)
	m := p.(*Model)
	if !m.selectPath("wiki/concepts/kv-cache.md") {
		t.Fatal("selectPath: wiki/concepts/kv-cache.md not found in tree")
	}
	before := m.selectedNode().Path

	next, _ := m.Update(ui.OpenPathMsg{Path: "wiki/concepts/does-not-exist.md"})
	m2 := next.(*Model)

	got := m2.selectedNode()
	if got == nil || got.Path != before {
		t.Fatalf("selectedNode() after OpenPathMsg(unknown path) = %+v, want unchanged %q", got, before)
	}
}
