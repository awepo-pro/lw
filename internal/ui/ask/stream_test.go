// stream_test.go covers the event pump itself: a scripted channel driven
// entirely through Listen + Update's re-arm, the drain-to-StreamClosed
// contract, and ErrorEv (populated or nil) rendering visibly without
// panicking.
package ask

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/ui"
)

// TestRendersScriptedStream is s4-tui.md S4-T6's named verification: a
// scripted fake channel, driven entirely through Listen + Update's re-arm,
// proves deltas accumulate in order into one message, the tool call
// renders as one collapsed line, and StageEv reaches the shell as a
// ui.StageChangedMsg carrying the same fields.
func TestRendersScriptedStream(t *testing.T) {
	d := newTestDeps(t)
	m, ok := New(d).(*Model)
	if !ok {
		t.Fatalf("New did not return *Model")
	}

	ch := make(chan agent.Event, 16)
	ch <- agent.TextDelta{Text: "Hello "}
	ch <- agent.TextDelta{Text: "world"}
	ch <- agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{"q":"kv cache"}`}
	ch <- agent.ToolResEv{ID: "t1", Name: "wiki.search", Content: "kv-cache: a memoization technique for attention", IsError: false}
	ch <- agent.StageEv{ChangesetID: "cs-abc123", Ops: 2}
	ch <- agent.DoneEv{Reason: "stop", Rounds: 1}
	close(ch)
	m.ch = ch

	var seen []tea.Msg
	pane := runCmd(t, m, Listen(ch), &seen)
	m = pane.(*Model)

	// Deltas accumulate, in arrival order, into one growing message —
	// never one entry per delta.
	var assistant []string
	for _, e := range m.entries {
		if e.kind == kindAssistant {
			assistant = append(assistant, e.text)
		}
	}
	if len(assistant) != 1 || assistant[0] != "Hello world" {
		t.Fatalf("assistant entries = %#v, want exactly one entry \"Hello world\"", assistant)
	}

	// Exactly one collapsed tool-call line: the ToolResEv updates the
	// ToolCallEv's own entry rather than appending a second one.
	var tools []*toolCall
	for _, e := range m.entries {
		if e.kind == kindTool {
			tools = append(tools, e.tool)
		}
	}
	if len(tools) != 1 {
		t.Fatalf("tool entries = %d, want exactly 1", len(tools))
	}
	if tools[0].expanded {
		t.Fatalf("tool entry is expanded by default")
	}
	if !tools[0].resolved || tools[0].isError {
		t.Fatalf("tool entry = %#v, want resolved and not an error", tools[0])
	}

	view := m.View(60, 10)
	toolLines := 0
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "▸") {
			toolLines++
			if !strings.Contains(line, "wiki.search") {
				t.Fatalf("tool line %q missing the tool name", line)
			}
		}
	}
	if toolLines != 1 {
		t.Fatalf("rendered %d lines with a collapsed tool marker, want 1:\n%s", toolLines, view)
	}
	if strings.Contains(view, "args: "+`{"q":"kv cache"}`) {
		t.Fatalf("collapsed view already shows the expanded args block:\n%s", view)
	}

	// StageEv is forwarded as ui.StageChangedMsg, fields unchanged.
	var gotStage *ui.StageChangedMsg
	for _, msg := range seen {
		if sc, ok := msg.(ui.StageChangedMsg); ok {
			sc := sc
			gotStage = &sc
		}
	}
	if gotStage == nil {
		t.Fatalf("no ui.StageChangedMsg observed; seen = %#v", seen)
	}
	if gotStage.ChangesetID != "cs-abc123" || gotStage.Ops != 2 {
		t.Fatalf("StageChangedMsg = %+v, want {cs-abc123 2}", *gotStage)
	}

	// The stream ended, and the whole channel was drained through Listen
	// (not bypassed): the pump's own terminal message was observed
	// somewhere among the batch (StageEv's ui.StageChangedMsg and the
	// re-armed Listen race inside one tea.Batch, same as a real
	// tea.Program — order between the two branches of one batch is not
	// guaranteed) and the pane stopped re-arming.
	var sawClosed bool
	for _, msg := range seen {
		if _, ok := msg.(StreamClosedMsg); ok {
			sawClosed = true
		}
	}
	if !sawClosed {
		t.Fatalf("StreamClosedMsg never observed; seen = %#v", seen)
	}
	if m.ch != nil {
		t.Fatalf("m.ch = %v after StreamClosedMsg, want nil", m.ch)
	}
}

// TestPumpDrainsToStreamClosed isolates the Listen/re-arm mechanics from
// rendering: every EventMsg delivered must carry the scripted events in
// order, and the pump must end on StreamClosedMsg.
func TestPumpDrainsToStreamClosed(t *testing.T) {
	d := newTestDeps(t)
	m := New(d).(*Model)

	ch := make(chan agent.Event, 4)
	ch <- agent.TextDelta{Text: "a"}
	ch <- agent.TextDelta{Text: "b"}
	close(ch)
	m.ch = ch

	var seen []tea.Msg
	pane := runCmd(t, m, Listen(ch), &seen)
	m = pane.(*Model)

	if len(seen) == 0 {
		t.Fatalf("no messages observed")
	}
	last := seen[len(seen)-1]
	if _, ok := last.(StreamClosedMsg); !ok {
		t.Fatalf("last message = %#v (%T), want StreamClosedMsg", last, last)
	}
	if m.ch != nil {
		t.Fatalf("m.ch = %v after StreamClosedMsg, want nil", m.ch)
	}

	var texts []string
	for _, msg := range seen {
		if em, ok := msg.(EventMsg); ok {
			if td, ok := em.Ev.(agent.TextDelta); ok {
				texts = append(texts, td.Text)
			}
		}
	}
	if len(texts) != 2 || texts[0] != "a" || texts[1] != "b" {
		t.Fatalf("delivered deltas = %#v, want [a b] in order", texts)
	}
}

// TestErrorEventNeverPanics covers both a populated and a nil Err — the
// goal explicitly calls out ErrorEv{Err: nil} as a case that must not
// panic.
func TestErrorEventNeverPanics(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"withError", errors.New("boom")},
		{"nilError", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newTestDeps(t)
			m := New(d).(*Model)

			cmd := m.applyEvent(agent.ErrorEv{Err: tt.err})
			if cmd != nil {
				cmd()
			}
			view := m.View(40, 10)
			if !strings.Contains(view, "error") {
				t.Fatalf("view does not render the error visibly:\n%s", view)
			}
		})
	}
}
