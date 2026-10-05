package main

import (
	"strings"
	"testing"
)

// batchSentence is the 040 addition to buildIngestMessage's fixed paragraph,
// byte for byte. It is the user message, not the system prompt, so the
// curator-prompt pins do not move; it asks for the two things a live ingest
// did not do unprompted: read every chunk, and batch independent calls.
const batchSentence = "Read every chunk of each source before you write pages from it, and save rounds: request several raw.get chunks in one response, and stage several pages or patches in one response when they do not depend on each other."

// TestIngestMessageBatchSentence: the sentence is in the message exactly
// once, and sits right before the closing instruction — after the per-file
// instructions it qualifies and before the stage.close ask it precedes.
func TestIngestMessageBatchSentence(t *testing.T) {
	msg := buildIngestMessage([]ingestItem{
		{path: "/tmp/a.md", kind: "article", title: "A", source: "https://example.test/a"},
		{path: "/tmp/b.md", kind: "paper", title: "B", source: "/home/u/b.pdf"},
	})
	if n := strings.Count(msg, batchSentence); n != 1 {
		t.Fatalf("batch sentence appears %d time(s), want exactly 1:\n%s", n, msg)
	}
	if !strings.Contains(msg, batchSentence+" When you are done") {
		t.Errorf("batch sentence is not immediately before \"When you are done\":\n%s", msg)
	}
	// The per-file list the rest of the CLI parses is untouched.
	if !strings.Contains(msg, "- path: /tmp/a.md\n  kind: article\n  title: A\n  original source: https://example.test/a\n") {
		t.Errorf("item list changed:\n%s", msg)
	}
}
