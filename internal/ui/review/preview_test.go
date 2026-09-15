// preview_test.go covers the Detail panel's two reasons to re-render: the
// `p` Diff ↔ Preview toggle (cursor kept, Ops footnote unchanged) and the
// op's Rationale/Provenance showing up in the Diff render — the properties
// that make this a review of reasoning, not a plain diff viewer.
package review

import (
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// TestRationaleRendered checks that an op's Rationale and Provenance both
// show up in View(w, h) — the property that makes this a review of
// reasoning, not a diff viewer (/docs/design.md §9.2, s4-tui.md S4-T3 item 2).
func TestRationaleRendered(t *testing.T) {
	d, e, _ := newTestDeps(t, "minimal")

	if _, err := e.OpenChangeset("with rationale", stage.Author{Kind: "agent", Model: "test"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
	if !ok {
		t.Fatal("fixture missing wiki/concepts/kv-cache.md")
	}
	oldLine := "- [[flash-attention]] — a kernel design that reduces the memory-bandwidth cost"
	newLine := "- [[flash-attention]] — an even better kernel design that reduces bandwidth"
	rewritten := *page
	rewritten.Body = strings.Replace(page.Body, oldLine, newLine, 1)

	const rationale = "tighten the flash-attention cross-reference for clarity"
	const provenance = "raw/articles/kv-cache-explained.md"

	if _, err := e.Append(stage.Op{
		Kind:       stage.OpPatchPage,
		Path:       page.Path,
		Section:    "## Related",
		Before:     page.SHA256(),
		Content:    rewritten.Serialize(),
		Rationale:  rationale,
		Provenance: []string{provenance},
		Hunks: []stage.Hunk{
			{ID: "h1", Path: page.Path, Del: []string{oldLine}, Add: []string{newLine}},
		},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	m := initModel(t, d)
	view := m.View(120, 30)

	if !strings.Contains(view, rationale) {
		t.Errorf("View does not show the op's Rationale:\n%s", view)
	}
	if !strings.Contains(view, provenance) {
		t.Errorf("View does not show the op's Provenance:\n%s", view)
	}
}

// TestPreviewTogglesDetailMode presses `p` on the harness vault's patch
// op and asserts the frozen Detail swap: Diff → Preview with the `p diff`
// note and the staged page rendered, cursor and Ops footnote kept, and
// back (s2-screens.md T06).
func TestPreviewTogglesDetailMode(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	v := uitest.PublicVault(t, "review-preview-toggle")
	d := uitest.Deps(v, true, nil)

	m := initModel(t, d)
	// Walk the cursor to op3 (the autolyse patch), the way the conformance
	// script does: its head note is "op3 · 1 hunk".
	reached := false
	for i := 0; i < 10 && !reached; i++ {
		_, pl := uitest.PaneScreen(m, 100, 28)
		if strings.Contains(pl, "op3 · 1 hunk") {
			reached = true
			break
		}
		m = send(t, m, keyPress('j'))
	}
	if !reached {
		t.Fatal("cursor never reached op3 within 10 j presses")
	}

	m = send(t, m, keyPress('p'))
	_, pl := uitest.PaneScreen(m, 100, 28)
	if !strings.Contains(pl, "╭ Preview ") || !strings.Contains(pl, "p diff") {
		t.Errorf("p did not open Preview with the `p diff` note:\n%s", pl)
	}
	if !strings.Contains(pl, "op3 · staged page") {
		t.Errorf("Preview lacks the op head note:\n%s", pl)
	}
	if !strings.Contains(pl, "Autolyse") {
		t.Errorf("Preview does not render the staged page:\n%s", pl)
	}
	if !strings.Contains(pl, "3 of 4") {
		t.Errorf("toggling Preview moved the Ops cursor off op3:\n%s", pl)
	}

	m = send(t, m, keyPress('p'))
	_, pl = uitest.PaneScreen(m, 100, 28)
	if !strings.Contains(pl, "╭ Diff ") || !strings.Contains(pl, "p preview") {
		t.Errorf("p did not return to Diff with the `p preview` note:\n%s", pl)
	}
	if !strings.Contains(pl, "3 of 4") {
		t.Errorf("the round trip moved the Ops cursor:\n%s", pl)
	}
}
