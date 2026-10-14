package ui

// header_partial_test.go pins 061 FIX1 A-061-1's Review badge: when the open
// changeset's intent starts with "[partial" — a turn the provider failed after
// it had staged work (stage.Engine.MarkPartial) — the shell header says
// "partial" next to the op count, at every width, so the reviewer knows the
// changeset is incomplete before reading a diff. The measured cause (s47 +
// the S2 tmux run): at 80 columns the header was `cs-44208c · 2 ops · checks
// ✗` and nothing on the Review screen showed the intent at all. A changeset
// that is not partial draws exactly the header it always did (the frozen
// header-*.txt goldens, TestHeaderGolden).

import (
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

func partialFixtureStage() headerStage {
	s := headerFixtureStage()
	s.Partial = true
	return s
}

// TestHeaderPartialBadge: ` · partial` follows the ops count at every width
// the frozen header goldens cover, and the vault name that fits the golden
// without it (ml-notes, 8 columns) does not collide with the tab bar.
func TestHeaderPartialBadge(t *testing.T) {
	setConfigDir(t)
	th, err := LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	th = th.WithDark(true)

	for _, w := range []int{100, 120, 200} {
		got := ansi.Strip(headerLine(th, w, "ml-notes", tabLabels(), "Review", 6, 1, 0, partialFixtureStage()))
		if want := "cs-df9e11 · 4 ops · partial · checks ✓"; !strings.Contains(got, want) {
			t.Errorf("width %d: header = %q, want it to contain %q", w, got, want)
		}
	}

	// The 80-column compact header: with a short vault name the whole group
	// fits, exactly as the coordinator's capture shows it.
	got := ansi.Strip(headerLine(th, 80, "vault", tabLabels(), "Review", 6, 1, 0, partialFixtureStage()))
	if want := "cs-df9e11 · 4 ops · partial · checks ✓"; !strings.Contains(got, want) {
		t.Errorf("width 80, vault %q: header = %q, want it to contain %q", "vault", got, want)
	}
}

// TestHeaderPartialNeverCollides: at 80 columns the badge costs ten, and a
// vault name of seven or more columns would push the right group into the tab
// bar. The group sheds its words (the "checks" label, then the op count)
// before it ever overwrites a tab; "partial" is never what goes.
func TestHeaderPartialNeverCollides(t *testing.T) {
	setConfigDir(t)
	th, err := LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	th = th.WithDark(true)

	tabs := tabLabels()
	for n := 1; n <= 16; n++ {
		vault := strings.Repeat("v", n)
		line := ansi.Strip(headerLine(th, 80, vault, tabs, "Review", 6, 1, 0, partialFixtureStage()))
		if got := len([]rune(line)); got != 80 {
			t.Errorf("vault %d wide: header is %d cells, want 80", n, got)
		}
		// Every tab label survives whole, in order, after the vault name.
		rest := line
		for _, tab := range tabs {
			i := strings.Index(rest, tab)
			if i < 0 {
				t.Errorf("vault %d wide: tab %q was overwritten:\n%q", n, tab, line)
				break
			}
			rest = rest[i+len(tab):]
		}
		if !strings.Contains(line, "partial") {
			t.Errorf("vault %d wide: the partial badge was dropped:\n%q", n, line)
		}
		if !strings.Contains(line, "cs-df9e11") {
			t.Errorf("vault %d wide: the changeset id was dropped:\n%q", n, line)
		}
		if !strings.Contains(line, "✓") {
			t.Errorf("vault %d wide: the checks glyph was dropped:\n%q", n, line)
		}
	}
}

// TestHeaderNotPartialIsUnchanged: Partial false draws the header it always
// drew — no badge, byte for byte the frozen goldens, at every golden width
// and for the vault-name range above.
func TestHeaderNotPartialIsUnchanged(t *testing.T) {
	setConfigDir(t)
	th, err := LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	th = th.WithDark(true)

	for _, w := range []int{80, 100, 120, 200} {
		got := headerLine(th, w, "ml-notes", tabLabels(), "Review", 6, 1, 0, headerFixtureStage())
		assertPlainMatchesFrozen(t, got, "header-review-"+strconv.Itoa(w)+".txt")
		if strings.Contains(ansi.Strip(got), "partial") {
			t.Errorf("width %d: a non-partial header mentions partial: %q", w, ansi.Strip(got))
		}
	}
}

// newPartialApp builds the shell around a real engine whose open changeset has
// the given intent, sized w columns.
func newPartialApp(t *testing.T, intent string, w int) *App {
	t.Helper()
	root := testutil.CopyFixture(t, "minimal")
	engine, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { engine.Close() })
	if _, err := engine.OpenChangeset(intent, stage.Author{Kind: "agent", Model: "test"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	deps := testDeps(t)
	deps.Engine = engine
	a := NewApp(Options{Deps: deps, Start: ScreenBrowse})
	m, _ := a.Update(tea.WindowSizeMsg{Width: w, Height: 24})
	a = m.(*App)
	// The engine was opened before the shell; a StageChangedMsg is how the
	// shell learns of the changeset (and re-reads its intent).
	cs, err := engine.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	m, _ = a.Update(StageChangedMsg{ChangesetID: cs.ID, Ops: 0})
	return m.(*App)
}

// TestAppHeaderShowsPartial: the shell reads the open changeset's intent and
// shows the badge at 80 and 120 columns; a changeset whose intent merely
// MENTIONS partial, or whose mark is not first, gets none; and it follows the
// intent when MarkPartial rewrites it.
func TestAppHeaderShowsPartial(t *testing.T) {
	headerOf := func(a *App) string { return ansi.Strip(strings.SplitN(a.View().Content, "\n", 2)[0]) }

	// "minimal" (the fixture vault's name) is seven wide: at 80 columns the
	// summary sheds its "checks" label to make room for the badge.
	for w, want := range map[int]string{80: "0 ops · partial · ", 120: "0 ops · partial · checks"} {
		a := newPartialApp(t, "[partial: provider 502 after 2 op(s)] ingest a.md", w)
		if h := headerOf(a); !strings.Contains(h, want) {
			t.Errorf("width %d: header = %q, want it to contain %q", w, h, want)
		}
	}

	for _, intent := range []string{"ingest a.md", "ingest partial.md", "ingest a.md [partial: x]", ""} {
		a := newPartialApp(t, intent, 120)
		if h := headerOf(a); strings.Contains(h, "partial") {
			t.Errorf("intent %q: header = %q, want no partial badge", intent, h)
		}
	}

	// MarkPartial after the shell has the changeset: the next stage refresh
	// picks the mark up.
	a := newPartialApp(t, "ingest a.md", 120)
	if h := headerOf(a); strings.Contains(h, "partial") {
		t.Fatalf("header already partial before MarkPartial: %q", h)
	}
	if err := a.deps.Engine.MarkPartial("partial: provider 502 after 2 op(s)"); err != nil {
		t.Fatalf("MarkPartial: %v", err)
	}
	cs, err := a.deps.Engine.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	m, _ := a.Update(StageChangedMsg{ChangesetID: cs.ID, Ops: 0})
	if h := headerOf(m.(*App)); !strings.Contains(h, "partial") {
		t.Errorf("header after MarkPartial = %q, want the partial badge", h)
	}

	// And it clears with the changeset.
	if err := a.deps.Engine.Reject("test"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	m, _ = a.Update(StageChangedMsg{})
	if h := headerOf(m.(*App)); strings.Contains(h, "partial") || !strings.Contains(h, "no changeset") {
		t.Errorf("header after the changeset is gone = %q, want no badge and \"no changeset\"", h)
	}
}
