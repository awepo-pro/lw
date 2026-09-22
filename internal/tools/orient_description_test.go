package tools

import (
	"strings"
	"testing"
)

// 025-T5 (ask cold-start). The engine injects an orientation digest into
// every turn's context (internal/agent/context.go), so a tool description
// telling the model to call vault.orient at session start just bought a
// redundant round trip for a digest it already had. The description must
// present the tool as an explicit, on-demand re-orientation — what it
// returns, and nothing that nudges a call before real work begins.
//
// F.W9's banned substring is only a proxy: the old description's second
// clause ("before searching or proposing anything") carries no "at the
// start of" at all. The nudges table below pins the realistic rewordings
// of the same cue, lowercased so a sentence-initial "Call this once"
// cannot slip through. The "on-demand" pin is the positive half: a later
// edit that over-corrects into a bare read-summary — never saying when
// the tool is for — fails too.
func TestOrientDescriptionHasNoCallInstruction(t *testing.T) {
	reg := minimalRegistry(t)

	tool, ok := reg.Get("vault.orient")
	if !ok {
		t.Fatal("vault.orient not registered")
	}

	desc := strings.ToLower(tool.Description)

	// Every entry is a way of saying "call this proactively/early" that
	// is not the frozen substring. None can appear in a description
	// that only states what the tool is and when it is wanted.
	nudges := []string{
		// F.W9's banned substring, plus its near-miss rewordings.
		"at the start of",
		"at session start",
		"session start",
		"start of a session",
		"start of the session",
		"beginning of a session",
		"beginning of the session",
		// The old second clause and its rewordings — sequencing cues
		// that anchor the tool before real work.
		"before searching",
		"before proposing",
		"before doing anything",
		"before any real work",
		// Early-call imperatives in other clothes.
		"call this once",
		"call it once",
		"once per session",
		"call this first",
		"call it first",
		"call this early",
		"call it early",
		"first thing",
	}
	for _, nudge := range nudges {
		if strings.Contains(desc, nudge) {
			t.Errorf("vault.orient description nudges a proactive call (%q): %q",
				nudge, tool.Description)
		}
	}

	// The tool must stay usable, not merely silent: it names the files
	// it reads (F.W9) and keeps the explicit on-demand framing, so a
	// model that genuinely needs re-orientation can tell this is the
	// tool for it and what it returns.
	for _, want := range []string{"SCHEMA.md", "index.md", "on-demand"} {
		if !strings.Contains(desc, strings.ToLower(want)) {
			t.Errorf("vault.orient description must still say %q: %q",
				want, tool.Description)
		}
	}
}
