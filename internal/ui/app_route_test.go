package ui

import (
	"fmt"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

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

// probePane records every message Update delivers to it and can be scripted
// to answer some of them with a command, the way a real screen answers
// StageChangedMsg with its own load command. It is the instrument for the
// producer-routing tests: what it recorded is exactly what reached it,
// whichever screen was active at the time — which a pane's own View cannot
// show, because a screen ignores what it does not recognise.
type probePane struct {
	name    string
	initCmd tea.Cmd
	reply   func(tea.Msg) tea.Cmd
	got     []tea.Msg
}

func (p *probePane) Init() tea.Cmd { return p.initCmd }

func (p *probePane) Update(msg tea.Msg) (Pane, tea.Cmd) {
	p.got = append(p.got, msg)
	if p.reply == nil {
		return p, nil
	}
	return p, p.reply(msg)
}

func (p *probePane) View(w, h int) string { return "[" + p.name + "]" }
func (p *probePane) Title() string        { return p.name }
func (p *probePane) Help() []key.Binding  { return nil }

var _ Pane = (*probePane)(nil)

// countOf counts the recorded messages equal to want. Every message these
// tests record is a comparable struct, so an interface comparison never has
// to look inside a non-comparable dynamic type.
func countOf(got []tea.Msg, want tea.Msg) int {
	n := 0
	for _, m := range got {
		if m == want {
			n++
		}
	}
	return n
}

// runCmds executes cmd the way the runtime does: expanding any tea.BatchMsg
// into its constituent commands and feeding every resulting message back
// through app.Update, so a test asserts routing rather than a hand-off the
// shell never performs. maxSteps bounds the recursion so a chain that never
// settles fails the test instead of hanging it.
func runCmds(t *testing.T, app *App, cmd tea.Cmd, maxSteps int) {
	t.Helper()
	if cmd == nil {
		return
	}
	if maxSteps <= 0 {
		t.Fatal("runCmds: exceeded step budget without the chain settling")
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			runCmds(t, app, c, maxSteps-1)
		}
		return
	}
	_, next := app.Update(msg)
	runCmds(t, app, next, maxSteps-1)
}

// TestProducerEnvelopeReachesThePaneThatProducedIt is the producer-routing
// guarantee: a message that arrives inside the shell's envelope is delivered
// to the pane that produced it, active or not. This is the answer to
// TestUnnamedMessagesStayWithTheActivePane — that test drives a *bare*
// message, which is what the runtime delivers when the message did not come
// from a pane's command; a pane's own command comes back enveloped.
func TestProducerEnvelopeReachesThePaneThatProducedIt(t *testing.T) {
	browse := &probePane{name: "browse"}
	review := &probePane{name: "review"}

	a := NewApp(Options{
		Deps:  testDeps(t),
		Panes: map[Screen]Pane{ScreenBrowse: browse, ScreenReview: review},
		Start: ScreenBrowse, // Review is off screen
	})

	own := shellLocalMsg{tag: "review's loadCmd result"}
	a.Update(paneMsg{from: ScreenReview, msg: own})

	if got := countOf(review.got, tea.Msg(own)); got != 1 {
		t.Fatalf("review (off screen) received its own message %d time(s), want 1: %#v", got, review.got)
	}
	if got := countOf(browse.got, tea.Msg(own)); got != 0 {
		t.Fatalf("browse (active) received another pane's message %d time(s), want 0: %#v", got, browse.got)
	}
}

// TestProducerEnvelopeNeverReachesTheActivePane is the tightening's other
// half, kept: an off-screen pane's own traffic must not leak onto the screen
// the user is looking at. Broadcasts fan out by name; a producer envelope is
// addressed, and addressed means one recipient.
func TestProducerEnvelopeNeverReachesTheActivePane(t *testing.T) {
	var internal [3]shellLocalMsg
	for i := range internal {
		internal[i] = shellLocalMsg{tag: fmt.Sprintf("pane %d's own message", i)}
	}

	panes := map[Screen]Pane{
		ScreenBrowse: &probePane{name: "browse"},
		ScreenReview: &probePane{name: "review"},
		ScreenLog:    &probePane{name: "log"},
	}
	a := NewApp(Options{Deps: testDeps(t), Panes: panes, Start: ScreenReview})

	// Two off-screen panes each report a message of their own, in the same
	// frame, the way tea.Batch delivers them: no ordering guarantee.
	a.Update(paneMsg{from: ScreenLog, msg: internal[0]})
	a.Update(paneMsg{from: ScreenBrowse, msg: internal[1]})
	// And one addressed to a screen with no pane injected at all, which must
	// be dropped rather than fall through to the active pane.
	if _, cmd := a.Update(paneMsg{from: ScreenAsk, msg: internal[2]}); cmd != nil {
		t.Fatalf("an envelope for a missing pane returned a Cmd (%v), want nil", cmd)
	}

	active := panes[ScreenReview].(*probePane)
	for i, want := range internal {
		if got := countOf(active.got, tea.Msg(want)); got != 0 {
			t.Fatalf("active pane received pane %d's own message %d time(s), want 0: %#v", i, got, active.got)
		}
	}
	for screen, want := range map[Screen]tea.Msg{
		ScreenLog:    internal[0],
		ScreenBrowse: internal[1],
	} {
		if got := countOf(panes[screen].(*probePane).got, want); got != 1 {
			t.Fatalf("pane %v received its own message %d time(s), want 1: %#v", screen, got, panes[screen].(*probePane).got)
		}
	}
}

// TestKeypressStillGoesOnlyToTheActivePane pins the two halves of key
// routing after the envelope: a bare keypress reaches the active pane and no
// other, and a key that arrives inside an envelope is a pane's own business
// — it goes to that pane and is never taken for user input, so a pane cannot
// quit the shell or cycle the screen by producing one.
func TestKeypressStillGoesOnlyToTheActivePane(t *testing.T) {
	browse := &probePane{name: "browse"}
	review := &probePane{name: "review"}

	a := NewApp(Options{
		Deps:  testDeps(t),
		Panes: map[Screen]Pane{ScreenBrowse: browse, ScreenReview: review},
		Start: ScreenBrowse,
	})

	m, _ := a.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	a = m.(*App)
	if got := countOf(browse.got, tea.Msg(tea.KeyPressMsg{Code: 'j', Text: "j"})); got != 1 {
		t.Fatalf("active pane received the keypress %d time(s), want 1", got)
	}
	if len(review.got) != 0 {
		t.Fatalf("off-screen pane received %d message(s) for an active-pane key, want 0: %#v", len(review.got), review.got)
	}
}

// TestPaneEmittedBroadcastStillReachesTheShell pins the seam the envelope
// must not disturb: panes emit shell-level messages too (logview's revert
// emits StageChangedMsg and SwitchScreenMsg, ask's ctrl+r emits
// SwitchScreenMsg), and those are commands to the shell, not the emitting
// pane's own answer. They therefore travel bare — not enveloped — so the
// shell's own panel still updates and the broadcast still fans out to every
// pane, exactly once.
func TestPaneEmittedBroadcastStillReachesTheShell(t *testing.T) {
	bc := StageChangedMsg{ChangesetID: "cs-produced-by-a-pane", Ops: 2}
	browse := &probePane{name: "browse"}
	review := &probePane{name: "review", reply: func(msg tea.Msg) tea.Cmd {
		if _, ok := msg.(revertKick); ok {
			return func() tea.Msg { return bc } // what a screen's stageChangedCmd emits
		}
		return nil
	}}

	a := NewApp(Options{
		Deps:  testDeps(t),
		Panes: map[Screen]Pane{ScreenBrowse: browse, ScreenReview: review},
		Start: ScreenReview,
	})

	_, cmd := a.Update(revertKick{})
	// The shell's message stream still carries the pane-emitted broadcast
	// itself, not an envelope around it.
	if got := cmd(); got != tea.Msg(bc) {
		t.Fatalf("a pane-emitted broadcast reached the shell as %T (%#v), want the bare message", got, got)
	}
	a.Update(bc)

	if a.stageID != bc.ChangesetID || a.stageOps != bc.Ops {
		t.Fatalf("shell stage panel = (%q, %d), want (%q, %d)", a.stageID, a.stageOps, bc.ChangesetID, bc.Ops)
	}
	for _, p := range []*probePane{browse, review} {
		if got := countOf(p.got, tea.Msg(bc)); got != 1 {
			t.Fatalf("%s pane received the broadcast %d time(s), want exactly 1: %#v", p.name, got, p.got)
		}
	}
}
