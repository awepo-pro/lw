package ui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

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

// TestStageChangedMsgRerendersHeader is the frame-note-6 regression test:
// the header's right side reflects StageChangedMsg's changeset id (the
// first 9 bytes) and op count once a changeset opens, having shown "no
// changeset" before it.
func TestStageChangedMsgRerendersHeader(t *testing.T) {
	a := NewApp(Options{Deps: testDeps(t), Start: ScreenBrowse})
	m, _ := a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	a = m.(*App)

	before := a.View().Content
	if !strings.Contains(before, "no changeset") {
		t.Fatalf("header before any changeset = %q, want it to say \"no changeset\"", before)
	}
	if strings.Contains(before, "cs-deadbe") {
		t.Fatalf("header already mentions the changeset before StageChangedMsg: %q", before)
	}

	m, _ = a.Update(StageChangedMsg{ChangesetID: "cs-deadbeef01234567", Ops: 3})
	a = m.(*App)

	after := a.View().Content
	if !strings.Contains(after, "cs-deadbe") {
		t.Fatalf("header after StageChangedMsg = %q, want it to contain the changeset id's first 9 bytes", after)
	}
	if !strings.Contains(after, "3 ops") {
		t.Fatalf("header after StageChangedMsg = %q, want it to contain the op count", after)
	}
	if strings.Contains(after, "no changeset") {
		t.Fatalf("header after StageChangedMsg still says \"no changeset\": %q", after)
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
	// 120 columns, not 80: at 80 the header drops the stats group first
	// (contract §5 frame note 1), same as the frozen header-review-80 case.
	m, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	a = m.(*App)

	content := a.View().Content
	want := "4 pages · 2 raw · 0 lint"
	if !strings.Contains(content, want) {
		t.Fatalf("View() = %q, want it to contain %q", content, want)
	}
	if !strings.Contains(content, "no changeset") {
		t.Fatalf("View() = %q, want it to say \"no changeset\" (the fixture opens none)", content)
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
