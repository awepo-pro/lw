package ui

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
)

// revertKick stands in for the user pressing `r` on Log: what matters here is
// that the log pane answers it with the same three commands the real revert
// does — its own query result, a StageChangedMsg and the jump to Review.
type revertKick struct{}

// TestRevertOnLogDeliversReviewItsOwnLoad is the reproduction that motivated
// the producer envelope. `r` on Log reverts a commit and jumps to Review;
// Review, off screen, reacts to the broadcast StageChangedMsg by issuing its
// own load command. Before the envelope, that load's result went to Log —
// the pane that happened to be active — and was dropped, so the jump landed
// on a Review pane that had never heard its own answer.
func TestRevertOnLogDeliversReviewItsOwnLoad(t *testing.T) {
	logsOwn := shellLocalMsg{tag: "log's eventsMsg"}
	loaded := shellLocalMsg{tag: "review's loadedMsg"}
	bc := StageChangedMsg{ChangesetID: "cs-reverted", Ops: 4}

	log := &probePane{name: "log", reply: func(msg tea.Msg) tea.Cmd {
		if _, ok := msg.(revertKick); ok {
			return tea.Batch(
				func() tea.Msg { return logsOwn }, // log's own queryCmd result
				func() tea.Msg { return bc },      // what Engine.Revert happened
				func() tea.Msg { return SwitchScreenMsg{To: ScreenReview} },
			)
		}
		return nil
	}}
	review := &probePane{name: "review", reply: func(msg tea.Msg) tea.Cmd {
		if _, ok := msg.(StageChangedMsg); ok {
			return func() tea.Msg { return loaded } // review's own loadCmd result
		}
		return nil
	}}

	a := NewApp(Options{
		Deps:  testDeps(t),
		Panes: map[Screen]Pane{ScreenLog: log, ScreenReview: review},
		Start: ScreenLog,
	})

	_, cmd := a.Update(revertKick{})
	runCmds(t, a, cmd, 32)

	if a.order[a.cur] != ScreenReview {
		t.Fatalf("active screen = %v, want ScreenReview (the jump must still land)", a.order[a.cur])
	}
	if a.stageID != bc.ChangesetID || a.stageOps != bc.Ops {
		t.Fatalf("shell stage panel = (%q, %d), want (%q, %d)", a.stageID, a.stageOps, bc.ChangesetID, bc.Ops)
	}
	if got := countOf(review.got, tea.Msg(loaded)); got != 1 {
		t.Fatalf("review received its own load result %d time(s), want 1 — this is the gap the envelope closes: %#v", got, review.got)
	}
	if got := countOf(review.got, tea.Msg(bc)); got != 1 {
		t.Fatalf("review received the broadcast %d time(s), want exactly 1: %#v", got, review.got)
	}
	if got := countOf(log.got, tea.Msg(logsOwn)); got != 1 {
		t.Fatalf("log received its own query result %d time(s), want 1: %#v", got, log.got)
	}
	// And neither pane swallowed the other's internal traffic.
	if got := countOf(log.got, tea.Msg(loaded)); got != 0 {
		t.Fatalf("log received review's load result %d time(s), want 0", got)
	}
	if got := countOf(review.got, tea.Msg(logsOwn)); got != 0 {
		t.Fatalf("review received log's query result %d time(s), want 0", got)
	}
}

// TestInitEnvelopesPaneCommands covers the same gap at startup, where every
// pane but the start screen is off screen: a pane's Init command is
// enveloped with its screen, so review's Init load reaches review even when
// the session begins elsewhere.
func TestInitEnvelopesPaneCommands(t *testing.T) {
	opened := shellLocalMsg{tag: "review's Init load"}
	review := &probePane{name: "review", initCmd: func() tea.Msg { return opened }}
	browse := &probePane{name: "browse"}

	a := NewApp(Options{
		Deps:  testDeps(t),
		Panes: map[Screen]Pane{ScreenBrowse: browse, ScreenReview: review},
		Start: ScreenBrowse,
	})

	cmd := a.Init()
	runCmds(t, a, cmd, 16)

	if got := countOf(review.got, tea.Msg(opened)); got != 1 {
		t.Fatalf("review received its own Init result %d time(s), want 1: %#v", got, review.got)
	}
	if got := countOf(browse.got, tea.Msg(opened)); got != 0 {
		t.Fatalf("start screen received another pane's Init result %d time(s), want 0", got)
	}
}

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
