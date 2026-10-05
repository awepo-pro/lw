package main

import (
	"strings"
	"testing"
)

// batchSentence is the 040 addition to buildIngestMessage's fixed paragraph,
// byte for byte, as A-040-2 amended it. It is the user message, not the
// system prompt, so the curator-prompt pins do not move. It asks for the
// three things a live ingest did not do on its own: read every chunk (in
// batches), read only the existing pages that wiki.search ties to the source,
// and write pages as soon as they have what they need. The first wording,
// "before you write pages from it", sent a DeepSeek model reading pages one
// per round until max_rounds with nothing staged — the baseline traces
// (eval/runs/20261005T015919Z) — so it is gone and must not return.
const batchSentence = "Read every chunk of each source, requesting several raw.get chunks in one response. Read only the existing pages that wiki.search shows are related to the source — do not survey the whole wiki. Write pages as soon as you have what they need, and stage several pages or patches in one response when they do not depend on each other."

// supersededBatchSentence is the pre-A-040-2 wording, kept only to pin its
// absence.
const supersededBatchSentence = "Read every chunk of each source before you write pages from it, and save rounds"

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
	if strings.Contains(msg, supersededBatchSentence) {
		t.Errorf("the superseded \"before you write pages from it\" wording is back:\n%s", msg)
	}
	// The per-file list the rest of the CLI parses is untouched.
	if !strings.Contains(msg, "- path: /tmp/a.md\n  kind: article\n  title: A\n  original source: https://example.test/a\n") {
		t.Errorf("item list changed:\n%s", msg)
	}
}
