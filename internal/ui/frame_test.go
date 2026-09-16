package ui

import (
	"strconv"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"

	"github.com/awepo-pro/lw/internal/stage"
)

// headerFixtureStage is s0-foundation.md T03 / MASTER §5 T03's header
// fixture: vault ml-notes, 6 pages, 1 raw, 0 lint, changeset
// cs-df9e11771b4c304a, 4 live ops, checks all pass.
func headerFixtureStage() headerStage {
	return headerStage{
		ID:  "cs-df9e11771b4c304a",
		Ops: 4,
		Checks: stage.Checks{
			Schema: "pass", Lint: "pass", Orphans: 0, BrokenLinks: 0,
		},
		Has: true,
	}
}

// TestHeaderGolden covers widths 80/100/120/200 on Review and 80 on Browse
// (MASTER §5 T03), against testdata/frozen/header-*.txt.
func TestHeaderGolden(t *testing.T) {
	setConfigDir(t)
	th, err := LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	th = th.WithDark(true)

	stg := headerFixtureStage()

	for _, w := range []int{80, 100, 120, 200} {
		got := headerLine(th, w, "ml-notes", tabLabels(), "Review", 6, 1, 0, stg)
		assertPlainMatchesFrozen(t, got, "header-review-"+strconv.Itoa(w)+".txt")
	}

	got := headerLine(th, 80, "ml-notes", tabLabels(), "Browse", 6, 1, 0, stg)
	assertPlainMatchesFrozen(t, got, "header-browse-80.txt")
}

// TestHeaderOpCountPlural covers ORCH-3's clarification of contract §5 note
// 1: "1 op", any other count "N ops" — 0, 1 and 2 specifically, since those
// are the boundary and the one case (1) that reads wrong unpluralized.
// TestHeaderGolden's own fixture is unaffected: it stays at 4 ops.
func TestHeaderOpCountPlural(t *testing.T) {
	setConfigDir(t)
	th, err := LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	th = th.WithDark(true)

	cases := []struct {
		ops  int
		want string
	}{
		{0, "0 ops"},
		{1, "1 op"},
		{2, "2 ops"},
	}
	for _, c := range cases {
		stg := headerStage{ID: "cs-df9e11771b4c304a", Ops: c.ops,
			Checks: stage.Checks{Schema: "pass", Lint: "pass"}, Has: true}
		got := headerLine(th, 120, "ml-notes", tabLabels(), "Review", 6, 1, 0, stg)
		if !strings.Contains(got, c.want) {
			t.Errorf("Ops=%d: header = %q, want it to contain %q", c.ops, got, c.want)
		}
		// And never the wrong form alongside it (e.g. "1 op" must not also
		// satisfy a stray "1 ops" substring check elsewhere).
		bad := "1 ops"
		if c.ops == 1 && strings.Contains(got, bad) {
			t.Errorf("Ops=1: header = %q, still contains the unpluralized %q", got, bad)
		}
	}
}

// reviewFooterBindings is MASTER §5 T03's footer-review-* fixture.
func reviewFooterBindings() []key.Binding {
	mk := func(k, keys, desc string) key.Binding {
		return key.NewBinding(key.WithKeys(keys), key.WithHelp(k, desc))
	}
	return []key.Binding{
		mk("y", "y", "accept hunk"),
		mk("n", "n", "drop hunk"),
		mk("j/k", "j", "move"),
		mk("p", "p", "preview"),
		mk("A", "A", "accept all"),
		mk("C", "C", "commit"),
		mk("X", "X", "reject changeset"),
		mk("g/G", "g", "top/bottom"),
		mk("tab", "tab", "screen"),
		mk("q", "q", "quit"),
	}
}

// TestFooterGolden covers widths 80/100/120/200 with the Review binding
// list (MASTER §5 T03), against testdata/frozen/footer-review-*.txt.
func TestFooterGolden(t *testing.T) {
	setConfigDir(t)
	th, err := LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	th = th.WithDark(true)

	bindings := reviewFooterBindings()
	for _, w := range []int{80, 100, 120, 200} {
		got := footerLine(th, w, bindings)
		assertPlainMatchesFrozen(t, got, "footer-review-"+strconv.Itoa(w)+".txt")
	}
}
