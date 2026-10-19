package agent

// ingestprompt_test.go is 063 S1's pin of IngestSystemPrompt: `lw ingest
// --prepare` prints the curator rules into the brief an external agent reads,
// and those rules must be the very bytes the in-process ingest turn sends the
// remote model (056: no web paragraphs), not a copy that can drift. It reuses
// 056's newInwFixture/inwStopRound (ingestnoweb_test.go) and 048's rbCleanStop.
// Permanent regression test (D-10C).

import (
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/llm"
)

// TestIngestSystemPromptIsTheIngestTurnPrompt: the exported helper returns
// systemPromptFor(false), never names web.search, and is byte-for-byte message
// 0 of a real ingest turn over a registry that DOES offer web.search — the case
// 056 exists for, where a careless helper (systemPromptFor(true), or the prompt
// of a lint turn) would promise a tool the ingest turn is not offered.
func TestIngestSystemPromptIsTheIngestTurnPrompt(t *testing.T) {
	got := IngestSystemPrompt()
	if want := systemPromptFor(false); got != want {
		t.Fatalf("IngestSystemPrompt() is not systemPromptFor(false):\n got %q\nwant %q", got, want)
	}
	if strings.Contains(got, "web.search") {
		t.Errorf("IngestSystemPrompt() mentions web.search; an ingest turn is not offered it")
	}
	if got == systemPromptFor(true) {
		t.Errorf("IngestSystemPrompt() equals the web-capable prompt; the two must differ or this test proves nothing")
	}

	f := newInwFixture(t, true, [][]llm.Chunk{inwStopRound()})
	events, err := f.send(t, "ingest")
	rbCleanStop(t, events, err)
	reqs := f.fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("Stream called %d times, want 1", len(reqs))
	}
	if m := reqs[0].Messages[0]; m.Role != "system" || m.Content != got {
		t.Fatalf("the ingest turn's message 0 is not IngestSystemPrompt():\n%q", m.Content)
	}
}
