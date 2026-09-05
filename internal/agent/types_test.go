package agent

import "testing"

// Compile-time assertions that every concrete event type satisfies Event.
// If any of these stops compiling, isEvent() was dropped from that type.
var (
	_ Event = TextDelta{}
	_ Event = ToolCallEv{}
	_ Event = ToolResEv{}
	_ Event = StageEv{}
	_ Event = DoneEv{}
	_ Event = ErrorEv{}
)

func TestEventTypesSatisfyEvent(t *testing.T) {
	// The var block above is the real assertion — it fails to compile if any
	// of the six event types loses its isEvent() marker. This test exists so
	// that fact shows up by name in `go test -v` output.
	events := []Event{
		TextDelta{Text: "hello"},
		ToolCallEv{ID: "1", Name: "wiki.search", Args: `{"q":"kv-cache"}`},
		ToolResEv{ID: "1", Name: "wiki.search", Content: "[]", IsError: false},
		StageEv{ChangesetID: "cs-1", Ops: 2},
		DoneEv{Reason: "complete", Rounds: 3},
		ErrorEv{Err: errTest},
	}
	if len(events) != 6 {
		t.Fatalf("len(events) = %d, want 6", len(events))
	}
	for i, ev := range events {
		if ev == nil {
			t.Errorf("events[%d] is nil", i)
		}
	}
}

var errTest = testError("boom")

type testError string

func (e testError) Error() string { return string(e) }
