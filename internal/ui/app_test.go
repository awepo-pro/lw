package ui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

// fakePane is a tiny, deterministic Pane used only by this test file
// (s4-tui.md S4-T2 item 6: "do not import a real screen package"). It
// records the last size it was asked to render at and its own name in its
// output, so a test can tell which pane the shell actually rendered.
type fakePane struct {
	name    string
	updates int
	lastMsg tea.Msg
}

func (f *fakePane) Init() tea.Cmd { return nil }

func (f *fakePane) Update(msg tea.Msg) (Pane, tea.Cmd) {
	f.updates++
	f.lastMsg = msg
	return f, nil
}

func (f *fakePane) View(w, h int) string {
	return fmt.Sprintf("[%s %dx%d]", f.name, w, h)
}

func (f *fakePane) Title() string       { return f.name }
func (f *fakePane) Help() []key.Binding { return nil }

var _ Pane = (*fakePane)(nil)

// testDeps builds a Deps with a real Theme and KeyMap — both loaded with
// setConfigDir (theme_test.go) pointing XDG_CONFIG_HOME at an empty temp
// dir, so these tests see lw's compiled-in defaults regardless of what is
// on the machine actually running them — and no Engine, matching the
// "constructible headless" requirement.
func testDeps(t *testing.T) Deps {
	t.Helper()
	setConfigDir(t)

	theme, err := LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	keys, err := LoadKeys()
	if err != nil {
		t.Fatalf("LoadKeys: %v", err)
	}
	return Deps{Theme: theme, Keys: keys}
}

func TestNewAppConstructibleHeadless(t *testing.T) {
	a := NewApp(Options{Deps: testDeps(t), Start: ScreenBrowse})
	if a == nil {
		t.Fatal("NewApp returned nil")
	}
	var _ tea.Model = a // App must satisfy tea.Model without a terminal

	v := a.View()
	if v.Content == "" {
		t.Fatal("View().Content is empty with no engine and no panes")
	}
}

func TestUpdateResizeDoesNotPanic(t *testing.T) {
	a := NewApp(Options{Deps: testDeps(t), Start: ScreenBrowse})

	sizes := []tea.WindowSizeMsg{
		{Width: 0, Height: 0},
		{Width: 1, Height: 1},
		{Width: 80, Height: 24},
		{Width: 200, Height: 50},
		{Width: 3, Height: 50},
	}
	for _, sz := range sizes {
		m, _ := a.Update(sz)
		next, ok := m.(*App)
		if !ok {
			t.Fatalf("Update(%+v) returned %T, want *App", sz, m)
		}
		a = next
		_ = a.View() // must not panic at any of these sizes
	}
}

func TestViewFitsWidthAt80x24And200x50(t *testing.T) {
	for _, sz := range []tea.WindowSizeMsg{{Width: 80, Height: 24}, {Width: 200, Height: 50}} {
		t.Run(fmt.Sprintf("%dx%d", sz.Width, sz.Height), func(t *testing.T) {
			a := NewApp(Options{
				Deps: testDeps(t),
				Panes: map[Screen]Pane{
					ScreenBrowse: &fakePane{name: "browse"},
					ScreenReview: &fakePane{name: "review"},
				},
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

func TestTabAdvancesFocusedPane(t *testing.T) {
	browse := &fakePane{name: "browse"}
	review := &fakePane{name: "review"}

	a := NewApp(Options{
		Deps: testDeps(t),
		Panes: map[Screen]Pane{
			ScreenBrowse: browse,
			ScreenReview: review,
		},
		Start: ScreenBrowse,
	})
	m, _ := a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	a = m.(*App)

	if !strings.Contains(a.View().Content, "[browse") {
		t.Fatalf("before tab: View() = %q, want it to contain the browse pane's output", a.View().Content)
	}

	m, _ = a.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	a = m.(*App)

	if !strings.Contains(a.View().Content, "[review") {
		t.Fatalf("after tab: View() = %q, want it to contain the review pane's output", a.View().Content)
	}
}

func TestStageChangedMsgRerendersSidebar(t *testing.T) {
	a := NewApp(Options{Deps: testDeps(t), Start: ScreenBrowse})
	m, _ := a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	a = m.(*App)

	before := a.View().Content
	if strings.Contains(before, "cs-deadbeef01234567") {
		t.Fatalf("sidebar already mentions the changeset before StageChangedMsg: %q", before)
	}

	m, _ = a.Update(StageChangedMsg{ChangesetID: "cs-deadbeef01234567", Ops: 3})
	a = m.(*App)

	after := a.View().Content
	if !strings.Contains(after, "cs-deadbeef01234567") {
		t.Fatalf("sidebar after StageChangedMsg = %q, want it to contain the changeset id", after)
	}
	if !strings.Contains(after, "3 op(s)") {
		t.Fatalf("sidebar after StageChangedMsg = %q, want it to contain the op count", after)
	}
}

func TestQuitKeyReturnsTeaQuit(t *testing.T) {
	a := NewApp(Options{Deps: testDeps(t), Start: ScreenBrowse})

	_, cmd := a.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd == nil {
		t.Fatal("Update(q) returned a nil Cmd, want tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("Update(q) command produced %T, want tea.QuitMsg", cmd())
	}
}

func TestVaultCountsFromRealEngine(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	engine, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { engine.Close() })

	deps := testDeps(t)
	deps.Engine = engine

	a := NewApp(Options{Deps: deps, Start: ScreenBrowse})
	m, _ := a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	a = m.(*App)

	content := a.View().Content
	want := "4 pages · 2 raw · ⚠ 0 lint"
	if !strings.Contains(content, want) {
		t.Fatalf("View() = %q, want it to contain %q", content, want)
	}
}

// TestStageChangedMsgReachesInactivePane is the C-106/TD-4 regression test:
// App.propagate used to deliver only to the active pane, so a screen the
// user was not looking at silently missed StageChangedMsg. Review starts
// active; Browse must still see the message.
func TestStageChangedMsgReachesInactivePane(t *testing.T) {
	browse := &fakePane{name: "browse"}
	review := &fakePane{name: "review"}

	a := NewApp(Options{
		Deps: testDeps(t),
		Panes: map[Screen]Pane{
			ScreenBrowse: browse,
			ScreenReview: review,
		},
		Start: ScreenReview,
	})

	m, _ := a.Update(StageChangedMsg{ChangesetID: "cs-inactive-pane", Ops: 1})
	a = m.(*App)

	if browse.updates != 1 {
		t.Fatalf("browse.updates = %d, want 1 (inactive panes must still see StageChangedMsg)", browse.updates)
	}
	if _, ok := browse.lastMsg.(StageChangedMsg); !ok {
		t.Fatalf("browse.lastMsg = %#v (%T), want StageChangedMsg", browse.lastMsg, browse.lastMsg)
	}
	if review.updates != 1 {
		t.Fatalf("review.updates = %d, want 1 (it is also active)", review.updates)
	}
}

// TestBroadcastMessagesReachEveryPane rounds out C-106/TD-4 for the other
// three messages the fix names: VaultReloadedMsg, tea.WindowSizeMsg and
// tea.BackgroundColorMsg must all reach a pane that is not on screen.
func TestBroadcastMessagesReachEveryPane(t *testing.T) {
	msgs := []tea.Msg{
		VaultReloadedMsg{},
		tea.WindowSizeMsg{Width: 100, Height: 30},
		tea.BackgroundColorMsg{},
	}
	for _, msg := range msgs {
		t.Run(fmt.Sprintf("%T", msg), func(t *testing.T) {
			browse := &fakePane{name: "browse"}
			review := &fakePane{name: "review"}

			a := NewApp(Options{
				Deps: testDeps(t),
				Panes: map[Screen]Pane{
					ScreenBrowse: browse,
					ScreenReview: review,
				},
				Start: ScreenReview,
			})

			a.Update(msg)

			if browse.updates != 1 {
				t.Errorf("browse (inactive).updates = %d, want 1", browse.updates)
			}
			if review.updates != 1 {
				t.Errorf("review (active).updates = %d, want 1", review.updates)
			}
		})
	}
}

// TestOpenPathMsgSwitchesToBrowseAndDelivers is C-108/D-CU's contract:
// App.Update must switch to Browse and deliver the same OpenPathMsg to the
// Browse pane specifically — not through the active-pane path — so Lint's
// `enter` lands on the right page. S4-T5 emits OpenPathMsg and
// SwitchScreenMsg{ScreenBrowse} together through tea.Batch, which delivers
// the two resulting messages as separate Update calls in an unspecified
// order, so both interleavings are exercised here.
func TestOpenPathMsgSwitchesToBrowseAndDelivers(t *testing.T) {
	for _, order := range []string{"open-then-switch", "switch-then-open"} {
		t.Run(order, func(t *testing.T) {
			browse := &fakePane{name: "browse"}
			review := &fakePane{name: "review"}

			a := NewApp(Options{
				Deps: testDeps(t),
				Panes: map[Screen]Pane{
					ScreenBrowse: browse,
					ScreenReview: review,
				},
				Start: ScreenReview,
			})

			openMsg := OpenPathMsg{Path: "wiki/concepts/kv-cache.md"}
			switchMsg := SwitchScreenMsg{To: ScreenBrowse}

			var m tea.Model
			if order == "open-then-switch" {
				m, _ = a.Update(openMsg)
				a = m.(*App)
				m, _ = a.Update(switchMsg)
				a = m.(*App)
			} else {
				m, _ = a.Update(switchMsg)
				a = m.(*App)
				m, _ = a.Update(openMsg)
				a = m.(*App)
			}

			if got := a.order[a.cur]; got != ScreenBrowse {
				t.Fatalf("active screen = %v, want ScreenBrowse", got)
			}
			if browse.updates == 0 {
				t.Fatal("browse pane never received an Update")
			}
			gotMsg, ok := browse.lastMsg.(OpenPathMsg)
			if !ok {
				t.Fatalf("browse pane's last message = %#v (%T), want OpenPathMsg", browse.lastMsg, browse.lastMsg)
			}
			if gotMsg.Path != openMsg.Path {
				t.Fatalf("browse received OpenPathMsg{Path: %q}, want %q", gotMsg.Path, openMsg.Path)
			}
		})
	}
}

func TestKeyPropagatesToUnfocusedNotFocusedPane(t *testing.T) {
	browse := &fakePane{name: "browse"}
	review := &fakePane{name: "review"}

	a := NewApp(Options{
		Deps: testDeps(t),
		Panes: map[Screen]Pane{
			ScreenBrowse: browse,
			ScreenReview: review,
		},
		Start: ScreenBrowse,
	})

	m, _ := a.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	a = m.(*App)

	if browse.updates != 1 {
		t.Errorf("browse.updates = %d, want 1 (it is focused)", browse.updates)
	}
	if review.updates != 0 {
		t.Errorf("review.updates = %d, want 0 (it is not focused)", review.updates)
	}
}

// shellLocalMsg stands in for the messages that reach App.Update's default
// branch from a pane's tea.Cmd results — the messages the shell neither acts
// on nor names in its fan-out set. It cannot be one of the shell's own types
// (those all have cases of their own) and a test cannot name a screen
// package's type without importing it, which the shell never does.
type shellLocalMsg struct{ tag string }

// TestUnnamedMessagesStayWithTheActivePane is the C-117/D-DA routing rule
// after the S6 tightening: only the named fan-out set reaches an off-screen
// pane. An unnamed message — here a stand-in for a screen's own internal
// message, such as review's loadedMsg or ask's sessionClosedMsg — goes to
// the active pane alone, the way it did before the temporary key/no-key
// split broadcast everything that was not a key.
func TestUnnamedMessagesStayWithTheActivePane(t *testing.T) {
	browse := &fakePane{name: "browse"}
	review := &fakePane{name: "review"}

	a := NewApp(Options{
		Deps: testDeps(t),
		Panes: map[Screen]Pane{
			ScreenBrowse: browse,
			ScreenReview: review,
		},
		Start: ScreenReview,
	})

	m, _ := a.Update(shellLocalMsg{tag: "a pane's own message"})
	a = m.(*App)

	if browse.updates != 0 {
		t.Errorf("browse (inactive).updates = %d, want 0 — an unnamed message is not fanned out", browse.updates)
	}
	if review.updates != 1 {
		t.Errorf("review (active).updates = %d, want 1", review.updates)
	}
	if _, ok := review.lastMsg.(shellLocalMsg); !ok {
		t.Fatalf("review.lastMsg = %#v (%T), want shellLocalMsg", review.lastMsg, review.lastMsg)
	}

	// A key event, including a key *release* (C-80: v2 has both, and both
	// satisfy the tea.KeyMsg interface the guard matches), stays with the
	// active pane: an off-screen pane must never be able to eat one.
	m, _ = a.Update(tea.KeyReleaseMsg(tea.Key{Code: 'q', Text: "q"}))
	a = m.(*App)
	if review.updates != 2 {
		t.Errorf("review (active).updates = %d, want 2 after the key release", review.updates)
	}
	if browse.updates != 0 {
		t.Errorf("browse (inactive).updates = %d, want 0 (keys never fan out)", browse.updates)
	}
}

// TestAskPumpMessagesReachEveryPane pins the fan-out set by name: the three
// ask pump messages (pane.go, declared there so the shell can route them
// without importing a screen package) must reach an inactive pane, which is
// the whole of C-117/D-DA — a turn that keeps streaming behind a screen
// switch, with its pump re-armed by the pane that owns it.
func TestAskPumpMessagesReachEveryPane(t *testing.T) {
	events := []agent.Event{
		agent.TextDelta{Text: "in flight"},
		agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{}`},
	}
	ch := make(chan agent.Event, len(events)+1)
	for _, ev := range events {
		ch <- ev
	}
	defer close(ch)

	msgs := []tea.Msg{
		StreamMsg{Ch: ch},
		EventMsg{Ev: events[0]},
		StreamClosedMsg{},
	}
	for _, msg := range msgs {
		t.Run(fmt.Sprintf("%T", msg), func(t *testing.T) {
			browse := &fakePane{name: "browse"}
			review := &fakePane{name: "review"}

			a := NewApp(Options{
				Deps: testDeps(t),
				Panes: map[Screen]Pane{
					ScreenBrowse: browse,
					ScreenReview: review,
				},
				Start: ScreenReview, // the ask pane is not even injected here
			})

			a.Update(msg)

			if browse.updates != 1 {
				t.Errorf("browse (inactive).updates = %d, want 1", browse.updates)
			}
			if _, ok := browse.lastMsg.(tea.Msg); !ok || browse.lastMsg == nil {
				t.Fatalf("browse.lastMsg = %#v, want the routed message", browse.lastMsg)
			}
			if review.updates != 1 {
				t.Errorf("review (active).updates = %d, want 1", review.updates)
			}
		})
	}
}

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
