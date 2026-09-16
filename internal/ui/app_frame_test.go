package ui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// TestShellRendersEveryBuiltInTheme is the headless stand-in for "start the
// TUI with each theme" (C-83: Program.Run never returns in a test): each
// built-in theme drives a real frame at two terminal sizes, and every line
// of it must be renderable and no wider than the terminal it was drawn for.
func TestShellRendersEveryBuiltInTheme(t *testing.T) {
	for _, name := range []string{"default", "dark", "light", "nord"} {
		for _, sz := range []tea.WindowSizeMsg{{Width: 80, Height: 24}, {Width: 200, Height: 50}} {
			t.Run(fmt.Sprintf("%s/%dx%d", name, sz.Width, sz.Height), func(t *testing.T) {
				setConfigDir(t)
				theme, err := LoadTheme(name)
				if err != nil {
					t.Fatalf("LoadTheme(%q): %v", name, err)
				}
				deps := Deps{Theme: theme, Keys: defaultKeyMap()}

				a := NewApp(Options{
					Deps:  deps,
					Panes: map[Screen]Pane{ScreenBrowse: &fakePane{name: "browse"}},
					Start: ScreenBrowse,
				})
				m, _ := a.Update(sz)
				a = m.(*App)

				view := a.View()
				if view.Content == "" {
					t.Fatal("View().Content is empty")
				}
				for i, line := range strings.Split(view.Content, "\n") {
					if w := lipgloss.Width(line); w > sz.Width {
						t.Errorf("line %d is %d columns wide, want <= %d: %q", i, w, sz.Width, line)
					}
				}
			})
		}
	}
}

// TestShellDispatchesReboundKeysToActivePane closes S6-T4's loop on
// hotkeys.toml: a KeyMap loaded from a file that rebinds j/k to n/p reaches
// the shell, the shell still answers its own keys, and the rebound ones
// arrive at the active pane as ordinary keypresses — which is what a screen
// matches its bindings against.
func TestShellDispatchesReboundKeysToActivePane(t *testing.T) {
	configDir := setConfigDir(t)
	writeConfigFile(t, configDir, "hotkeys.toml", `
move_down = ["n"]
move_up = ["p"]
`)

	keys, err := LoadKeys()
	if err != nil {
		t.Fatalf("LoadKeys: %v", err)
	}
	deps := testDeps(t)
	deps.Keys = keys

	pane := &fakePane{name: "browse"}
	a := NewApp(Options{Deps: deps, Panes: map[Screen]Pane{ScreenBrowse: pane}, Start: ScreenBrowse})
	m, _ := a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	a = m.(*App)

	for _, want := range []string{"n", "p", "j", "k"} {
		m, _ := a.Update(tea.KeyPressMsg{Code: rune(want[0]), Text: want})
		a = m.(*App)
		if pane.lastMsg == nil {
			t.Fatalf("shell swallowed %q; the active pane never saw it", want)
		}
		got, ok := pane.lastMsg.(tea.KeyPressMsg)
		if !ok {
			t.Fatalf("pane.lastMsg = %#v (%T), want tea.KeyPressMsg", pane.lastMsg, pane.lastMsg)
		}
		if got.String() != want {
			t.Fatalf("pane got key %q, want %q", got.String(), want)
		}
		if key.Matches(got, keys.Quit) || key.Matches(got, keys.NextPane) {
			t.Fatalf("%q matched a shell key after the rebind", want)
		}
	}

	// The shell's own keys are untouched by a move-key rebind: tab still
	// cycles and q still quits.
	m, cmd := a.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	a = m.(*App)
	if cmd == nil {
		t.Fatal("Update(q) returned a nil Cmd; the rebind disturbed the shell keys")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("Update(q) produced %T, want tea.QuitMsg", cmd())
	}
	m, cmd = a.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	a = m.(*App)
	if cmd != nil {
		t.Fatalf("Update(tab) returned a non-nil Cmd (%v), want nil", cmd)
	}
	if a.order[a.cur] == ScreenBrowse {
		t.Fatal("tab did not cycle the active screen")
	}
}

// sweepPane is a fake Pane whose View always returns exactly h lines of
// exactly w blank cells — TestFrameSweep's instrument for checking the
// *shell's* own sizing, independent of whether a real screen gets contract
// §4.2's per-pane invariant right.
type sweepPane struct{}

func (sweepPane) Init() tea.Cmd                  { return nil }
func (sweepPane) Update(tea.Msg) (Pane, tea.Cmd) { return sweepPane{}, nil }
func (sweepPane) Title() string                  { return "sweep" }
func (sweepPane) Help() []key.Binding            { return nil }
func (sweepPane) View(w, h int) string {
	line := strings.Repeat(" ", w)
	lines := make([]string, h)
	for i := range lines {
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

var _ Pane = sweepPane{}

// frameSweepSize is one width/height pair from contract §6's sweep set.
type frameSweepSize struct{ w, h int }

// frameSweepSizes reproduces contract §6's SweepSizes set locally — uitest
// doesn't exist yet (T05): every width 80..220 at heights
// {24,25,30,31,32,40,60}, plus the below-minimum sizes (79,24), (80,23),
// (72,20), (120,20).
func frameSweepSizes() []frameSweepSize {
	var out []frameSweepSize
	for w := 80; w <= 220; w++ {
		for _, h := range []int{24, 25, 30, 31, 32, 40, 60} {
			out = append(out, frameSweepSize{w, h})
		}
	}
	out = append(out,
		frameSweepSize{79, 24},
		frameSweepSize{80, 23},
		frameSweepSize{72, 20},
		frameSweepSize{120, 20},
	)
	return out
}

// TestFrameSweep puts fake panes that return exact-size blank views into
// NewApp, over contract §6's sweep set: every frame must be exactly h
// lines of exactly w cells, and below 80×24 it must be the too-small
// notice (contract §5 frame note 3).
func TestFrameSweep(t *testing.T) {
	panes := map[Screen]Pane{}
	for _, s := range screenOrder {
		panes[s] = sweepPane{}
	}
	a := NewApp(Options{Deps: testDeps(t), Panes: panes, Start: ScreenReview})

	for _, sz := range frameSweepSizes() {
		t.Run(fmt.Sprintf("%dx%d", sz.w, sz.h), func(t *testing.T) {
			m, _ := a.Update(tea.WindowSizeMsg{Width: sz.w, Height: sz.h})
			a = m.(*App)

			content := a.View().Content
			lines := strings.Split(content, "\n")
			if len(lines) != sz.h {
				t.Fatalf("got %d lines, want %d", len(lines), sz.h)
			}
			for i, line := range lines {
				if w := lipgloss.Width(line); w != sz.w {
					t.Errorf("line %d width = %d, want %d: %q", i, w, sz.w, line)
				}
			}
			if sz.w < MinWidth || sz.h < MinHeight {
				if !strings.Contains(content, "Terminal too small") {
					t.Errorf("below-minimum size %dx%d did not render the too-small notice", sz.w, sz.h)
				}
			}
		})
	}
}

// TestOverlayClosesOnDropBelowMinimum is repair-1's Minor-finding fix: the
// `?` overlay used to survive a resize below D11's minimum, so growing the
// terminal back re-showed it with no key press behind it. Opening it, then
// shrinking to 72×20, then growing back to 120×40 must leave it closed.
func TestOverlayClosesOnDropBelowMinimum(t *testing.T) {
	const overlayMarker = "esc to close"

	a := NewApp(Options{
		Deps:  testDeps(t),
		Panes: map[Screen]Pane{ScreenReview: &fakePane{name: "review"}},
		Start: ScreenReview,
	})
	m, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	a = m.(*App)

	m, _ = a.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	a = m.(*App)
	if !a.overlayOpen {
		t.Fatal("overlay did not open on ?")
	}
	if !strings.Contains(a.View().Content, overlayMarker) {
		t.Fatalf("overlay open but the frame shows no %q: %q", overlayMarker, a.View().Content)
	}

	m, _ = a.Update(tea.WindowSizeMsg{Width: 72, Height: 20})
	a = m.(*App)
	if a.overlayOpen {
		t.Fatal("overlayOpen is still true after the window dropped below the minimum")
	}

	m, _ = a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	a = m.(*App)
	if a.overlayOpen {
		t.Fatal("overlay re-opened on growing back, with no ? press behind it")
	}
	if strings.Contains(a.View().Content, overlayMarker) {
		t.Fatalf("frame still shows the overlay after growing back: %q", a.View().Content)
	}
}
