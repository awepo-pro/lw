package conformance

import (
	"image/color"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/ask"
	"github.com/awepo-pro/lw/internal/ui/browse"
	"github.com/awepo-pro/lw/internal/ui/lintview"
	"github.com/awepo-pro/lw/internal/ui/logview"
	"github.com/awepo-pro/lw/internal/ui/review"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// gridCase is one frozen grid: its subtest name and the terminal size the
// shell is driven to.
type gridCase struct {
	name string
	w, h int
}

// gridCases enumerates the 26 frozen grids (contract §9: exactly 26
// subtests, one per grid file, named <view>-<W>x<H>).
var gridCases = []gridCase{
	{"ask-80x24", 80, 24},
	{"ask-100x30", 100, 30},
	{"ask-120x40", 120, 40},
	{"ask-200x60", 200, 60},
	{"ask-conversation-80x24", 80, 24},
	{"ask-conversation-100x30", 100, 30},
	{"ask-conversation-120x40", 120, 40},
	{"ask-conversation-200x60", 200, 60},
	{"browse-80x24", 80, 24},
	{"browse-100x30", 100, 30},
	{"browse-120x40", 120, 40},
	{"browse-200x60", 200, 60},
	{"keys-80x24", 80, 24},
	{"keys-100x30", 100, 30},
	{"keys-120x40", 120, 40},
	{"keys-200x60", 200, 60},
	{"review-80x24", 80, 24},
	{"review-100x30", 100, 30},
	{"review-120x40", 120, 40},
	{"review-200x60", 200, 60},
	{"review-preview-80x24", 80, 24},
	{"review-preview-100x30", 100, 30},
	{"review-preview-120x40", 120, 40},
	{"review-preview-200x60", 200, 60},
	{"too-small-72x20", 72, 20},
	{"too-small-120x20", 120, 20},
}

// TestMockupConformance is the frozen-grid gate itself (contract §9): one
// subtest per frozen grid. It needs the private mockup vault, which never
// enters the repo (00-conventions.md §1 rule 7), so without
// LW_MOCKUP_VAULT it skips and CI stays green.
func TestMockupConformance(t *testing.T) {
	vault := os.Getenv(envVault)
	if vault == "" {
		t.Skip(skipReason)
	}

	// Hermetic theme and keys: uitest.Deps loads both through the config
	// directory, and the gate's colours are frozen expectations, not
	// whatever the developer's machine carries.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	grids := loadGrids(t, gridsDir(vault))

	for _, tc := range gridCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			runGridCase(t, tc, vault, grids[tc.name+".txt"])
		})
	}
}

// runGridCase drives one grid's subtest end to end (contract §9 note 3):
// copy the private vault, drop op4's hunk, wire the shell the way cmd/lw
// does, drive it to the grid's size and the view script's state, then
// compare — cells outside the mask byte-exact, rules inside it — and run
// the colour checks and the light-polarity re-render (note 6). Every
// failure before the comparison is a setup Fatalf: a red subtest must be a
// layout diff, never a broken harness.
func runGridCase(t *testing.T, tc gridCase, vault string, grid []string) {
	t.Helper()

	v := uitest.CopyVault(t, vault)
	if err := v.Engine.DropHunk("op4", "h1"); err != nil {
		t.Fatalf("setup: DropHunk(op4, h1): %v", err)
	}

	var ag agent.Agent
	if wantsAgent(tc.name) {
		ag = &uitest.FakeAgent{Events: fakeAgentEvents(), Store: agent.NewFileSessions(v.Root)}
	}

	app := newConformanceApp(t, v, ag)
	m := uitest.Drive(t, app,
		tea.WindowSizeMsg{Width: tc.w, Height: tc.h},
		tea.BackgroundColorMsg{Color: color.Black},
	)
	m = driveInit(t, m)
	m = runViewScript(t, tc.name, m)

	styled, plain := uitest.Screen(m)
	actual := strings.Split(plain, "\n")

	darkSet, lightSet := colourSetsFor(tc.name)

	compareGrid(t, tc.name, grid, actual)
	checkMaskRules(t, tc.name, grid, actual)
	checkColours(t, styled, plain, darkSet)

	// The same subtest re-renders in light polarity; the plain text must
	// be identical to the dark render (contract §9 note 6).
	m = uitest.Drive(t, m, tea.BackgroundColorMsg{Color: color.White})
	styledLight, plainLight := uitest.Screen(m)
	if plainLight != plain {
		t.Errorf("light polarity changed the plain text: %s",
			firstLineDiff(strings.Split(plainLight, "\n"), strings.Split(plain, "\n")))
	}
	checkColours(t, styledLight, plainLight, lightSet)
}

// newConformanceApp mirrors cmd/lw's buildTUIOptions (cmd_tui.go:173),
// which package main cannot be imported to share: one ui.Deps — the
// harness's fixed-clock vault, the default keys, the default theme at the
// gate's polarity — and the five screens constructed from it, injected
// under their Screen key, with Start pinned to ScreenReview. When
// buildTUIOptions grows a screen or changes a constructor, this is the
// line that changes with it.
func newConformanceApp(t *testing.T, v *uitest.Vault, ag agent.Agent) *ui.App {
	t.Helper()

	d := uitest.Deps(v, true, ag) // dark: the gate drives the polarity explicitly
	askPane := ask.New(d).(*ask.Model)
	askPane.SetShowProvenance(true) // A-027-2: the goldens pin the sources-shown state
	return ui.NewApp(ui.Options{
		Deps: d,
		Panes: map[ui.Screen]ui.Pane{
			ui.ScreenBrowse: browse.New(d),
			ui.ScreenReview: review.New(d),
			ui.ScreenAsk:    askPane,
			ui.ScreenLint:   lintview.New(d),
			ui.ScreenLog:    logview.New(d),
		},
		Start: ui.ScreenReview,
	})
}

// driveInit delivers what tea.Program produces between Run and the
// caller's first message: Init's commands run and every result comes back
// through Update. The shell batches each injected pane's own Init behind
// tea.Batch, and those commands are bounded vault reads — none parks on a
// channel — so running the batch here is faithful and prompt.
func driveInit(t *testing.T, m tea.Model) tea.Model {
	t.Helper()
	return uitest.Drive(t, m, cmdMsgs(t, m.Init())...)
}

// cmdMsgs runs cmd — recursively through tea.BatchMsg — and returns every
// message it yields.
func cmdMsgs(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, cmdMsgs(t, c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// TestGridCompareSelfCheck proves the comparator on the grids alone, no
// TUI (contract §9): identity passes, one changed unmasked cell in row 0
// fails, one changed masked cell that keeps the rules passes, and a `#` at
// the start of a masked row fails. It skips without the vault exactly as
// the gate does.
func TestGridCompareSelfCheck(t *testing.T) {
	vault := os.Getenv(envVault)
	if vault == "" {
		t.Skip(skipReason)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	grids := loadGrids(t, gridsDir(vault))

	t.Run("identity passes on every grid", func(t *testing.T) {
		for _, tc := range gridCases {
			grid := grids[tc.name+".txt"]
			if !gridEqual(grid, grid, locateMask(t, tc.name, grid)) {
				t.Errorf("frozen grid %s does not equal itself", tc.name)
			}
		}
	})

	t.Run("one changed cell in row 0 fails", func(t *testing.T) {
		const name = "review-120x40"
		grid := grids[name+".txt"]
		cell := strings.IndexRune(grid[0], 'm') // the vault name starts the header
		if cell < 0 {
			t.Fatalf("setup: %s row 0 has no vault name to change", name)
		}
		actual := cloneLines(grid)
		row := []rune(actual[0])
		row[cell] = 'X'
		actual[0] = string(row)
		if gridEqual(grid, actual, locateMask(t, name, grid)) {
			t.Error("changing a row 0 cell was not caught, though no mask covers row 0")
		}
	})

	t.Run("one changed masked cell passes", func(t *testing.T) {
		const name = "review-preview-120x40"
		grid := grids[name+".txt"]
		mask := locateMask(t, name, grid)

		r, c, ok := firstLetterCell(grid, mask)
		if !ok {
			t.Fatal("setup: no letter cell inside the masked region to change")
		}
		actual := cloneLines(grid)
		row := []rune(actual[r])
		if row[c] == 'a' {
			row[c] = 'b'
		} else {
			row[c] = 'a'
		}
		actual[r] = string(row)

		if !gridEqual(grid, actual, mask) {
			t.Errorf("masked cell (row %d, column %d) was not excluded from the comparison", r+1, c+1)
		}
		for _, problem := range maskRuleProblems(name, grid, actual, mask) {
			t.Error(problem)
		}
	})

	t.Run("hash at the start of a masked row fails", func(t *testing.T) {
		const name = "review-preview-120x40"
		grid := grids[name+".txt"]
		mask := locateMask(t, name, grid)

		actual := cloneLines(grid)
		row := []rune(actual[mask.rows[0]])
		row[mask.x+1] = '#'
		actual[mask.rows[0]] = string(row)

		if !gridEqual(grid, actual, mask) {
			t.Error("the comparison failed outside the masked cell, though only a masked cell changed")
		}
		if problems := maskRuleProblems(name, grid, actual, mask); len(problems) == 0 {
			t.Error("a # at the start of a masked row's content broke no rule")
		}
	})

	t.Run("rules do not index a short frame", func(t *testing.T) {
		const name = "review-preview-120x40"
		grid := grids[name+".txt"]
		mask := locateMask(t, name, grid)

		// A screen that breaks its h-lines invariant fails the exact
		// comparison on every row; the rules must stay out of its way
		// instead of panicking past the masked rows they index.
		short := cloneLines(grid)[:mask.bottomRow]
		maskRuleProblems(name, grid, short, mask)
	})
}

// firstLetterCell returns the first cell of the masked region holding a
// letter — the masked cell the "one changed masked cell" case edits.
func firstLetterCell(grid []string, mask *maskRegion) (row, col int, ok bool) {
	for r := mask.rows[0]; r <= mask.rows[1]; r++ {
		for c := mask.x + 4; c < mask.x+mask.w-1; c++ {
			if cell := cellAt(grid[r], c); cell >= 'a' && cell <= 'z' {
				return r, c, true
			}
		}
	}
	return 0, 0, false
}
