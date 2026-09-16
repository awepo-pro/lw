// finder_test.go drives the `/` finder's flow through Model.Update: open,
// type, rank, commit, esc — plus the StatusReporter path a finder error
// takes (contract §7: browse reports finder errors).
package browse

import (
	"testing"

	"github.com/awepo-pro/lw/internal/ui"
)

// TestFuzzyFinderOpenTypeCommitOpensMatch drives the whole `/` flow through
// Model.Update: open, type a query, commit with enter, and confirm the tree
// cursor landed on the top-ranked match.
func TestFuzzyFinderOpenTypeCommitOpensMatch(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	p := New(d)

	p, _ = p.Update(keyMsg("/"))
	m := p.(*Model)
	if !m.finder.open {
		t.Fatal("finder did not open on /")
	}

	for _, r := range "kv" {
		p, _ = p.Update(keyMsg(string(r)))
	}
	m = p.(*Model)
	if len(m.finder.matches) == 0 {
		t.Fatal("no finder matches for \"kv\"")
	}
	if m.finder.matches[0].path != "wiki/concepts/kv-cache.md" {
		t.Fatalf("finder.matches[0] = %q, want wiki/concepts/kv-cache.md", m.finder.matches[0].path)
	}

	p, _ = p.Update(keyMsg("enter"))
	m = p.(*Model)
	if m.finder.open {
		t.Fatal("finder still open after enter")
	}
	n := m.selectedNode()
	if n == nil || n.Path != "wiki/concepts/kv-cache.md" {
		t.Fatalf("selectedNode() after finder commit = %+v, want wiki/concepts/kv-cache.md", n)
	}
}

// TestFuzzyFinderEscRestoresCursor: closing the finder without committing
// leaves the tree cursor exactly where it was before / was pressed.
func TestFuzzyFinderEscRestoresCursor(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	p := New(d)
	m := p.(*Model)
	m.cursor = 1
	origCursor := m.cursor

	p, _ = p.Update(keyMsg("/"))
	for _, r := range "kv" {
		p, _ = p.Update(keyMsg(string(r)))
	}
	p, _ = p.Update(keyMsg("esc"))
	m = p.(*Model)

	if m.finder.open {
		t.Fatal("finder still open after esc")
	}
	if m.cursor != origCursor {
		t.Fatalf("cursor after esc = %d, want restored to %d", m.cursor, origCursor)
	}
}

// TestFuzzyFinderBackspaceEditsQuery: backspace removes the last rune and
// re-runs the search.
func TestFuzzyFinderBackspaceEditsQuery(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	p := New(d)
	p, _ = p.Update(keyMsg("/"))
	for _, r := range "kx" {
		p, _ = p.Update(keyMsg(string(r)))
	}
	m := p.(*Model)
	if m.finder.query != "kx" {
		t.Fatalf("finder.query = %q, want %q", m.finder.query, "kx")
	}

	p, _ = p.Update(keyMsg("backspace"))
	m = p.(*Model)
	if m.finder.query != "k" {
		t.Fatalf("finder.query after backspace = %q, want %q", m.finder.query, "k")
	}
}

// TestFinderCommitWithoutMatchesReportsStatus: committing a query with no
// matches closes the finder and leaves the StatusReporter message the
// footer shows (contract §7: browse reports finder errors).
func TestFinderCommitWithoutMatchesReportsStatus(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	p := New(d)
	p, _ = p.Update(keyMsg("/"))
	for _, r := range "zzz" {
		p, _ = p.Update(keyMsg(string(r)))
	}
	p, _ = p.Update(keyMsg("enter"))
	m := p.(*Model)

	if m.finder.open {
		t.Fatal("finder still open after committing an empty match list")
	}
	msg, level := m.Status()
	if msg == "" {
		t.Fatal("Status() is empty after committing a query with no matches, want an error message")
	}
	if level != ui.StatusWarn {
		t.Fatalf("Status() level = %v, want StatusWarn", level)
	}

	// The message is transient: the next key press clears it.
	p, _ = m.Update(keyMsg("j"))
	m = p.(*Model)
	if msg, _ := m.Status(); msg != "" {
		t.Fatalf("Status() after the next key = %q, want cleared", msg)
	}
}
