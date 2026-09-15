// harness_test.go holds the harness helpers' own tests: Drive's command
// expansion and blocking timeout, Key's name round-trip, AssertGrid's
// cell invariant, SweepSizes' frozen sweep set, and FakeAgent's channel
// ownership (backbone §9, C-105).
package uitest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
)

// driveMsgA…driveMsgD are distinct message types, so a drive run's delivery
// order can be asserted by type name alone.
type (
	driveMsgA struct{}
	driveMsgB struct{}
	driveMsgC struct{}
	driveMsgD struct{}
)

// TestDriveRunsCommands drives a model whose first Update answers with a
// batch of [one command, a sequence of two], and pins the whole contract:
// the batch and the sequence both expand, their results feed back through
// Update in order, and the drive stops at quiescence. It then drives a
// model whose command blocks forever and pins that the drive returns
// without it (the 2s timeout), leaving the delivered set untouched.
func TestDriveRunsCommands(t *testing.T) {
	m := &scriptedModel{
		steps: []tea.Cmd{
			tea.Batch(
				func() tea.Msg { return driveMsgB{} },
				tea.Sequence(
					func() tea.Msg { return driveMsgC{} },
					func() tea.Msg { return driveMsgD{} },
				),
			),
		},
	}
	got := Drive(t, m, driveMsgA{})
	if got != tea.Model(m) {
		t.Fatalf("Drive returned %T, want the same model it was given", got)
	}
	want := []string{
		fmt.Sprintf("%T", driveMsgA{}),
		fmt.Sprintf("%T", driveMsgB{}),
		fmt.Sprintf("%T", driveMsgC{}),
		fmt.Sprintf("%T", driveMsgD{}),
	}
	if strings.Join(m.got, ",") != strings.Join(want, ",") {
		t.Errorf("Drive delivered [%s], want [%s]", strings.Join(m.got, ","), strings.Join(want, ","))
	}

	// A command that never produces a message is abandoned after the 2s
	// timeout, and the drive ends quiescent with just the original message
	// delivered.
	blocked := &scriptedModel{
		steps: []tea.Cmd{
			func() tea.Msg { <-make(chan struct{}); return nil },
		},
	}
	start := time.Now()
	got = Drive(t, blocked, driveMsgA{})
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("Drive took %s to give up on a blocking command, want ~%s", elapsed, cmdTimeout)
	}
	if len(blocked.got) != 1 {
		t.Errorf("blocking drive delivered %d messages (%v), want 1", len(blocked.got), blocked.got)
	}

	// A nil command — what Update returns to say "nothing to do" — delivers
	// nothing and does not stall the drive.
	quiet := &scriptedModel{}
	Drive(t, quiet, driveMsgA{})
	if len(quiet.got) != 1 {
		t.Errorf("quiet drive delivered %d messages, want 1", len(quiet.got))
	}
}

// TestKeyNames pins the round-trip the screens rely on: Key builds a
// tea.KeyPressMsg whose String() is the name key bindings are written
// against, and carries the modifier where one is named.
func TestKeyNames(t *testing.T) {
	for _, name := range []string{"j", "tab", "ctrl+r", "?", "esc", "enter"} {
		if got := Key(name).String(); got != name {
			t.Errorf("Key(%q).String() = %q, want it to round-trip", name, got)
		}
	}
	if got := Key("j").Code; got != 'j' {
		t.Errorf("Key(%q).Code = %q, want 'j'", "j", got)
	}
	if Key("ctrl+r").Mod&tea.ModCtrl == 0 {
		t.Error("Key(\"ctrl+r\") carries no ctrl modifier")
	}
	if got := Key("ctrl+r").String(); got != "ctrl+r" {
		t.Errorf("Key(\"ctrl+r\").String() = %q, want \"ctrl+r\"", got)
	}
}

// recordingTB is the fake testing.TB AssertGrid's failure paths run
// against: it records every error instead of failing the real test.
type recordingTB struct {
	testing.TB // embedded nil; an unexpected method call panics loudly

	errors []string
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

// TestAssertGrid passes a well-formed grid and rejects a short one and a
// narrow one, the second two on the fake TB above so a deliberate failure
// is itself asserted rather than failing this test.
func TestAssertGrid(t *testing.T) {
	good := strings.Repeat("abcde\n", 3) + "abcde"
	AssertGrid(t, good, 5, 4)

	// Cells, not bytes: three one-cell runes worth seven bytes between them
	// still make a three-cell line, and a fullwidth rune — three bytes that
	// occupy TWO cells — makes "％x" a three-cell line, not a four-byte
	// count's guess.
	AssertGrid(t, "éé…\n▌▌▌\n x ", 3, 3)
	AssertGrid(t, "％x", 3, 1)

	short := &recordingTB{TB: nil}
	assertGrid(short, "abcde\nabcde", 5, 4)
	if len(short.errors) == 0 {
		t.Fatal("AssertGrid accepted a 2-line grid at h=4")
	}
	if !strings.Contains(short.errors[0], "2 lines") {
		t.Errorf("height error = %q, want it to name the line count", short.errors[0])
	}

	narrow := &recordingTB{TB: nil}
	assertGrid(narrow, "abcd\nabcde\nabcde", 5, 3)
	if len(narrow.errors) != 1 {
		t.Fatalf("AssertGrid reported %d errors for one narrow line, want 1: %v", len(narrow.errors), narrow.errors)
	}
	if !strings.Contains(narrow.errors[0], "line 1") || !strings.Contains(narrow.errors[0], "4 cells") {
		t.Errorf("width error = %q, want it to name the line and its cell count", narrow.errors[0])
	}
}

// TestSweepSizes pins the frozen sweep set: 991 entries, first (72,20),
// last (220,60), sorted by (W,H) with no duplicates, and the below-minimum
// and boundary pairs present.
func TestSweepSizes(t *testing.T) {
	sizes := SweepSizes()
	if len(sizes) != 991 {
		t.Fatalf("SweepSizes() has %d entries, want 991", len(sizes))
	}
	if first := sizes[0]; first.W != 72 || first.H != 20 {
		t.Errorf("first entry = {%d,%d}, want {72,20}", first.W, first.H)
	}
	if last := sizes[len(sizes)-1]; last.W != 220 || last.H != 60 {
		t.Errorf("last entry = {%d,%d}, want {220,60}", last.W, last.H)
	}
	seen := make(map[[2]int]bool, len(sizes))
	for i, s := range sizes {
		if seen[[2]int{s.W, s.H}] {
			t.Errorf("entry %d is a duplicate: {%d,%d}", i, s.W, s.H)
		}
		seen[[2]int{s.W, s.H}] = true
		if i > 0 {
			p := sizes[i-1]
			if p.W > s.W || (p.W == s.W && p.H >= s.H) {
				t.Fatalf("sizes not sorted at %d: {%d,%d} then {%d,%d}", i, p.W, p.H, s.W, s.H)
			}
		}
	}
	for _, want := range [][2]int{{79, 24}, {80, 23}, {72, 20}, {120, 20}, {99, 30}, {100, 30}, {179, 40}, {180, 40}} {
		if !seen[want] {
			t.Errorf("SweepSizes() is missing {%d,%d}", want[0], want[1])
		}
	}
}

// TestFakeAgentReplay replays a scripted turn and pins the channel contract
// Loop.Send honours: events in order, out closed on both the clean and the
// cancelled path, and Sessions handing back the store the caller built.
func TestFakeAgentReplay(t *testing.T) {
	store := agent.NewFileSessions(t.TempDir())
	events := []agent.Event{
		agent.TextDelta{Text: "A lean dough needs only flour, water, salt and time."},
		agent.ToolCallEv{ID: "c1", Name: "wiki_search", Args: `{"query":"autolyse hydration"}`},
		agent.DoneEv{Reason: "stop", Rounds: 1},
	}
	f := &FakeAgent{Events: events, Store: store}

	out := make(chan agent.Event, len(events)+1)
	if err := f.Send(context.Background(), "sess-1", "what is an autolyse?", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	var got []agent.Event
	for ev := range out {
		got = append(got, ev)
	}
	if len(got) != len(events) {
		t.Fatalf("replayed %d events, want %d", len(got), len(events))
	}
	for i := range events {
		if got[i] != events[i] {
			t.Errorf("event %d = %#v, want %#v", i, got[i], events[i])
		}
	}

	// A cancelled context stops the replay, closes out, and returns the
	// context error — Send closes out on every exit path (C-105).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out = make(chan agent.Event)
	err := f.Send(ctx, "sess-1", "again", out)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Send with a cancelled ctx returned %v, want context.Canceled", err)
	}
	if _, open := <-out; open {
		t.Error("Send with a cancelled ctx left out open")
	}

	// Sessions returns the caller's store, and a real one: a session
	// created through it round-trips from disk.
	if f.Sessions() != store {
		t.Fatal("Sessions() returned a different store than FakeAgent.Store")
	}
	s, err := f.Sessions().Create("cs-uitest")
	if err != nil {
		t.Fatalf("Sessions().Create: %v", err)
	}
	if err := f.Sessions().Append(s.ID, agent.Record{Role: "user", Content: "hello"}); err != nil {
		t.Fatalf("Sessions().Append: %v", err)
	}
	back, err := f.Sessions().Get(s.ID)
	if err != nil {
		t.Fatalf("Sessions().Get: %v", err)
	}
	if len(back.Records) != 1 || back.Records[0].Content != "hello" {
		t.Errorf("reloaded session records = %+v, want the one appended record", back.Records)
	}
}
