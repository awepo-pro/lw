// frame_test.go pins the log screen to the 003 frame (s2-screens.md T10):
// the size sweep over uitest.SweepSizes, the frozen goldens on
// uitest.PublicVault, the shell's `?` overlay golden, and the UTC time
// rule (MASTER §8 C29).
package logview

import (
	"image/color"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// goldenDir is where this package's goldens live.
const goldenDir = "testdata/golden"

// publicPane builds a log pane over uitest.PublicVault's staged fixture
// changeset (fixed clock, deterministic ids) with its first journal query
// already run, at the given polarity. XDG_CONFIG_HOME is pointed at an
// empty temp dir first, so uitest.Deps's LoadTheme/LoadKeys see the
// compiled-in defaults and never a real user config.
func publicPane(t *testing.T, dark bool) *Model {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	v := uitest.PublicVault(t, "ml-notes")
	p := New(uitest.Deps(v, dark, nil))
	loaded := runCmd(t, p, p.Init())
	return loaded.(*Model)
}

// TestLogSweep is the frame's size invariant (conventions §4 rule 2): at
// every sweep size at or above the shell's 80×24 minimum — the shell gives
// a pane h-2 — the pane renders exactly h lines of exactly w cells.
func TestLogSweep(t *testing.T) {
	m := publicPane(t, true)

	for _, s := range uitest.SweepSizes() {
		if s.W < ui.MinWidth || s.H < ui.MinHeight {
			continue // below-minimum sizes are the shell's too-small notice, not a pane's
		}
		_, plain := uitest.PaneScreen(m, s.W, s.H-2)
		uitest.AssertGrid(t, plain, s.W, s.H-2)
	}
}

// TestLogGolden pins the Events panel's frozen evidence (T10 has no mockup
// grid; the goldens are what the orchestrator reviews): 120×38 plain plus
// its dark and light styled twins, and the 80×22 plain minimum. The plain
// golden must be polarity-independent.
func TestLogGolden(t *testing.T) {
	dark := publicPane(t, true)
	darkStyled, darkPlain := uitest.PaneScreen(dark, 120, 38)
	uitest.AssertGrid(t, darkPlain, 120, 38)
	testutil.GoldenString(t, goldenDir+"/log-120x38.txt.golden", darkPlain+"\n")
	testutil.GoldenString(t, goldenDir+"/log-120x38-dark.ansi.golden", darkStyled+"\n")

	light := publicPane(t, false)
	lightStyled, lightPlain := uitest.PaneScreen(light, 120, 38)
	uitest.AssertGrid(t, lightPlain, 120, 38)
	if lightPlain != darkPlain {
		t.Fatal("light polarity changed the plain text; the .txt golden must be polarity-independent")
	}
	testutil.GoldenString(t, goldenDir+"/log-120x38-light.ansi.golden", lightStyled+"\n")

	small := publicPane(t, true)
	_, smallPlain := uitest.PaneScreen(small, 80, 22)
	uitest.AssertGrid(t, smallPlain, 80, 22)
	testutil.GoldenString(t, goldenDir+"/log-80x22.txt.golden", smallPlain+"\n")
}

// TestLogOverlay pins the shell's `?` overlay drawn over Log at 120×40,
// through ui.NewApp with only this pane wired (T10 defers to T09's golden
// list).
func TestLogOverlay(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	v := uitest.PublicVault(t, "ml-notes")
	d := uitest.Deps(v, true, nil)

	app := ui.NewApp(ui.Options{
		Deps:  d,
		Panes: map[ui.Screen]ui.Pane{ui.ScreenLog: New(d)},
		Start: ui.ScreenLog,
	})
	m := uitest.Drive(t, app, append([]tea.Msg{
		tea.WindowSizeMsg{Width: 120, Height: 40},
		tea.BackgroundColorMsg{Color: color.Black},
	}, collectMsgs(t, app.Init())...)...)
	m = uitest.Drive(t, m, uitest.Key("?"))

	_, plain := uitest.Screen(m)
	uitest.AssertGrid(t, plain, 120, 40)
	testutil.GoldenString(t, goldenDir+"/overlay-120x40.txt.golden", plain+"\n")
}

// TestLogKindColumnCapsAt16 pins T10's kind column: padded to the longest
// kind in the filtered list, capped at 16 cells. The `rejected` filter's
// changeset_rejected is 18 cells, so its kind clips to the column — the
// rows stay aligned instead of the column breaking on one kind.
func TestLogKindColumnCapsAt16(t *testing.T) {
	d, e := newTestDeps(t, "minimal")
	seedHistory(t, e)

	p := initModel(t, d)
	m := p.(*Model)
	for i := 0; i < 2; i++ { // all → accepted → rejected
		_, cmd := m.handleKey(keyMsg("f"))
		p = runCmd(t, p, cmd)
	}
	m = p.(*Model)
	if m.filter != filterRejected {
		t.Fatalf("filter after two `f` = %v, want filterRejected", m.filter)
	}

	_, plain := uitest.PaneScreen(m, 80, 22)
	uitest.AssertGrid(t, plain, 80, 22)
	if !strings.Contains(plain, "changeset_rejec…") {
		t.Fatalf("the 18-cell kind was not clipped to the 16-cell column:\n%s", plain)
	}
}

// TestLogDoesNotCaptureText pins that Log takes no text input (its filter
// cycles with `f`): the pane must not implement ui.TextCapturer, or the
// shell would hand it `q` and `?` to type instead of quitting and opening
// help (C27/D-3Q).
func TestLogDoesNotCaptureText(t *testing.T) {
	d, _ := newTestDeps(t, "minimal")
	if _, captures := any(New(d)).(ui.TextCapturer); captures {
		t.Fatal("logview implements ui.TextCapturer; `q` and `?` would type instead of quit/help")
	}
}

// TestLogRowTimeIsUTC pins MASTER §8 C29: a row's time is the event's TS
// in UTC, never time.Local, so the goldens render identically on a UTC CI
// machine and a UTC+8 laptop. The event here carries a fixed UTC+8 zone;
// 20:00 HKT is 12:00 UTC.
func TestLogRowTimeIsUTC(t *testing.T) {
	d, _ := newTestDeps(t, "minimal")
	hkt := time.FixedZone("HKT", 8*3600)
	m := &Model{
		deps:    d,
		theme:   d.Theme,
		hasLoad: true,
		cursor:  0,
		events: []stage.Event{{
			TS:      time.Date(2026, 8, 29, 20, 0, 0, 0, hkt),
			Kind:    stage.EvOpProposed,
			Op:      "op1",
			Message: "test event",
		}},
	}

	_, plain := uitest.PaneScreen(m, 80, 22)
	if !strings.Contains(plain, "2026-08-29 12:00") {
		t.Fatalf("row time is not the event's UTC rendering:\n%s", plain)
	}
	if strings.Contains(plain, "20:00") {
		t.Fatalf("row time shows the event's local-zone clock (20:00), not UTC:\n%s", plain)
	}
}
