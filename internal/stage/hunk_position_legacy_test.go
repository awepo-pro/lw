// hunk_position_legacy_test.go pins what a position-free hunk (At == 0) does,
// byte for byte, as base 569c577 did it (052, TD-15). Old open changesets on
// disk carry hunks without at/lines, and the contract for them is "apply as
// before" — so the three misplacements TD-15 measured are frozen here as
// golden bytes, taken from the base before any 052 code existed. A golden
// here is not an endorsement: it is the proof that the legacy path was not
// quietly "fixed" into something an old changeset never produced.
package stage

import (
	"strings"
	"testing"
)

// hunkPosLegacyReplaceText is base 569c577's applyHunks output for the
// replace_text shape — one Add-only hunk ("Alpha inserted line.", Section
// "## Alpha") that belongs after "Alpha second line.". The legacy applier
// puts it at the end of the section, after "Alpha closing line.", and eats
// the blank line before "## Beta" (TD-15, probed by 050's review).
const hunkPosLegacyReplaceText = "---\n" +
	"title: Hunk Position Fixture\n" +
	"created: 2026-08-29\n" +
	"updated: 2026-08-29\n" +
	"type: concept\n" +
	"tags: [inference, memory]\n" +
	"sources: [raw/articles/kv-cache-explained.md]\n" +
	"confidence: high\n" +
	"---\n" +
	"\n" +
	"# Hunk Position Fixture\n" +
	"\n" +
	"Intro paragraph linking to [[kv-cache]] and [[gpt-4]].\n" +
	"\n" +
	"## Alpha\n" +
	"\n" +
	"Alpha first line.\n" +
	"Alpha second line.\n" +
	"Alpha third line.\n" +
	"\n" +
	"Alpha closing line.\n" +
	"\n" +
	"Alpha inserted line.\n" +
	"## Beta\n" +
	"\n" +
	"Beta first line.\n" +
	"\n" +
	"- beta item one\n" +
	"- beta item two\n" +
	"\n" +
	"Beta closing line.\n" +
	"\n" +
	"## Gamma\n" +
	"\n" +
	"Gamma line one.\n" +
	"\n" +
	"Gamma line two.\n" +
	"\n" +
	"Gamma line three.\n" +
	"\n" +
	"Gamma line four.\n" +
	"\n" +
	"## Related\n" +
	"\n" +
	"- [[kv-cache]] — the first related link.\n" +
	"- [[gpt-4]] — the second related link.\n"

// hunkPosLegacyAppendLast is base 569c577's applyHunks output for the
// append_section shape on the LAST section: a blank line appears inside the
// "## Related" list and the trailing newline is lost, because the section
// end of the last section is the slice end, past the final "" element
// (TD-15).
const hunkPosLegacyAppendLast = "---\n" +
	"title: Hunk Position Fixture\n" +
	"created: 2026-08-29\n" +
	"updated: 2026-08-29\n" +
	"type: concept\n" +
	"tags: [inference, memory]\n" +
	"sources: [raw/articles/kv-cache-explained.md]\n" +
	"confidence: high\n" +
	"---\n" +
	"\n" +
	"# Hunk Position Fixture\n" +
	"\n" +
	"Intro paragraph linking to [[kv-cache]] and [[gpt-4]].\n" +
	"\n" +
	"## Alpha\n" +
	"\n" +
	"Alpha first line.\n" +
	"Alpha second line.\n" +
	"Alpha third line.\n" +
	"\n" +
	"Alpha closing line.\n" +
	"\n" +
	"## Beta\n" +
	"\n" +
	"Beta first line.\n" +
	"\n" +
	"- beta item one\n" +
	"- beta item two\n" +
	"\n" +
	"Beta closing line.\n" +
	"\n" +
	"## Gamma\n" +
	"\n" +
	"Gamma line one.\n" +
	"\n" +
	"Gamma line two.\n" +
	"\n" +
	"Gamma line three.\n" +
	"\n" +
	"Gamma line four.\n" +
	"\n" +
	"## Related\n" +
	"\n" +
	"- [[kv-cache]] — the first related link.\n" +
	"- [[gpt-4]] — the second related link.\n" +
	"\n" +
	"- [[flash-attention]] — the appended link."

// hunkPosLegacyInsertBefore is base 569c577's applyHunks output for the
// insert_before shape: the Add-only hunk carries Section "## Beta", so the
// legacy applier lands the new "## Inserted" section after Beta instead of
// before it (TD-15).
const hunkPosLegacyInsertBefore = "---\n" +
	"title: Hunk Position Fixture\n" +
	"created: 2026-08-29\n" +
	"updated: 2026-08-29\n" +
	"type: concept\n" +
	"tags: [inference, memory]\n" +
	"sources: [raw/articles/kv-cache-explained.md]\n" +
	"confidence: high\n" +
	"---\n" +
	"\n" +
	"# Hunk Position Fixture\n" +
	"\n" +
	"Intro paragraph linking to [[kv-cache]] and [[gpt-4]].\n" +
	"\n" +
	"## Alpha\n" +
	"\n" +
	"Alpha first line.\n" +
	"Alpha second line.\n" +
	"Alpha third line.\n" +
	"\n" +
	"Alpha closing line.\n" +
	"\n" +
	"## Beta\n" +
	"\n" +
	"Beta first line.\n" +
	"\n" +
	"- beta item one\n" +
	"- beta item two\n" +
	"\n" +
	"Beta closing line.\n" +
	"\n" +
	"## Inserted\n" +
	"\n" +
	"Inserted text.\n" +
	"\n" +
	"## Gamma\n" +
	"\n" +
	"Gamma line one.\n" +
	"\n" +
	"Gamma line two.\n" +
	"\n" +
	"Gamma line three.\n" +
	"\n" +
	"Gamma line four.\n" +
	"\n" +
	"## Related\n" +
	"\n" +
	"- [[kv-cache]] — the first related link.\n" +
	"- [[gpt-4]] — the second related link.\n"

// hunkPosLegacyBlank is base 569c577's applyHunks output for a hunk whose
// Del is a blank line: indexOfLine takes the FIRST blank line in the file —
// the one after the frontmatter — not the one the hunk meant in "## Gamma"
// (TD-15).
const hunkPosLegacyBlank = "---\n" +
	"title: Hunk Position Fixture\n" +
	"created: 2026-08-29\n" +
	"updated: 2026-08-29\n" +
	"type: concept\n" +
	"tags: [inference, memory]\n" +
	"sources: [raw/articles/kv-cache-explained.md]\n" +
	"confidence: high\n" +
	"---\n" +
	"Gamma inserted line.\n" +
	"# Hunk Position Fixture\n" +
	"\n" +
	"Intro paragraph linking to [[kv-cache]] and [[gpt-4]].\n" +
	"\n" +
	"## Alpha\n" +
	"\n" +
	"Alpha first line.\n" +
	"Alpha second line.\n" +
	"Alpha third line.\n" +
	"\n" +
	"Alpha closing line.\n" +
	"\n" +
	"## Beta\n" +
	"\n" +
	"Beta first line.\n" +
	"\n" +
	"- beta item one\n" +
	"- beta item two\n" +
	"\n" +
	"Beta closing line.\n" +
	"\n" +
	"## Gamma\n" +
	"\n" +
	"Gamma line one.\n" +
	"\n" +
	"Gamma line two.\n" +
	"\n" +
	"Gamma line three.\n" +
	"\n" +
	"Gamma line four.\n" +
	"\n" +
	"## Related\n" +
	"\n" +
	"- [[kv-cache]] — the first related link.\n" +
	"- [[gpt-4]] — the second related link.\n"

// TestHunkAtZeroLegacyPlacement applies hand-built position-free hunks — the
// shapes ComputeHunks produced before 052, with the Section the tools stamp —
// to hunkPosBefore and compares against the base's bytes. It also pins the
// two ways a hunk is "not positioned" besides At == 0: At without Lines, and
// Lines without At. Either keeps the legacy placement.
func TestHunkAtZeroLegacyPlacement(t *testing.T) {
	replaceText := Hunk{ID: "h1", Path: hunkPosPath, Section: "## Alpha", Add: []string{"Alpha inserted line."}}
	appendLast := Hunk{ID: "h1", Path: hunkPosPath, Section: "## Related", Add: []string{"- [[flash-attention]] — the appended link."}}
	insertBefore := Hunk{ID: "h1", Path: hunkPosPath, Section: "## Beta", Add: []string{"## Inserted", "", "Inserted text.", ""}}
	blank := Hunk{ID: "h1", Path: hunkPosPath, Del: []string{""}, Add: []string{"Gamma inserted line."}}

	cases := []struct {
		name  string
		hunks []Hunk
		want  string
	}{
		{"replace_text mid-section insert", []Hunk{replaceText}, hunkPosLegacyReplaceText},
		{"append_section to the last section", []Hunk{appendLast}, hunkPosLegacyAppendLast},
		{"insert_before", []Hunk{insertBefore}, hunkPosLegacyInsertBefore},
		{"del of a blank line takes the first blank line", []Hunk{blank}, hunkPosLegacyBlank},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, h := range tc.hunks {
				if h.At != 0 || h.Lines != nil {
					t.Fatalf("hand-built hunk %s is not position-free: At=%d Lines=%q", h.ID, h.At, h.Lines)
				}
			}
			if got := string(applyHunks([]byte(hunkPosBefore), tc.hunks)); got != tc.want {
				t.Fatalf("At == 0 placement drifted from base 569c577:\n--- got ---\n%s\n--- want ---\n%s", got, tc.want)
			}
		})
	}

	// Positions that cannot place a hunk must not change it: At without
	// Lines and Lines without At both fall to the legacy path.
	t.Run("At without Lines keeps the legacy placement", func(t *testing.T) {
		h := insertBefore
		h.At = 23
		if got := string(applyHunks([]byte(hunkPosBefore), []Hunk{h})); got != hunkPosLegacyInsertBefore {
			t.Fatalf("At without Lines was positioned:\n%s", got)
		}
	})
	t.Run("Lines without At keeps the legacy placement", func(t *testing.T) {
		h := insertBefore
		h.Lines = []string{" ## Beta", "+## Inserted"}
		if got := string(applyHunks([]byte(hunkPosBefore), []Hunk{h})); got != hunkPosLegacyInsertBefore {
			t.Fatalf("Lines without At was positioned:\n%s", got)
		}
	})
}

// TestHunkAtZeroIgnoresLinesAfterAnEarlierHunk is the guard behind "At == 0
// means unknown, apply as before" (050/052 review L-1). applyWindow finds a
// window's start as the output line whose origin is At-1, and a line an
// earlier hunk produced has origin -1 — so a hunk with At == 0 but Lines that
// happen to match that line would be positioned by accident if the guard were
// only "Lines is non-empty". Here legacy hunk h1 replaces "Alpha first line."
// with "X" (that line now has origin -1) and h2, At == 0, carries Lines
// [" X", "+Y"]; h2 must take the legacy placement (the end of the file, as an
// Add-only hunk with no Section), exactly as if it had no Lines at all.
func TestHunkAtZeroIgnoresLinesAfterAnEarlierHunk(t *testing.T) {
	replace := Hunk{ID: "h1", Path: hunkPosPath, Del: []string{"Alpha first line."}, Add: []string{"X"}}
	withLines := Hunk{ID: "h2", Path: hunkPosPath, Add: []string{"Y"}, Lines: []string{" X", "+Y"}}
	plain := Hunk{ID: "h2", Path: hunkPosPath, Add: []string{"Y"}}
	if withLines.At != 0 || replace.At != 0 {
		t.Fatal("the fixture hunks must be position-free")
	}

	want := string(applyHunks([]byte(hunkPosBefore), []Hunk{replace, plain}))
	got := string(applyHunks([]byte(hunkPosBefore), []Hunk{replace, withLines}))
	if got != want {
		t.Fatalf("a hunk with At == 0 was positioned by its Lines:\n--- got ---\n%s\n--- want (the same hunk without Lines) ---\n%s", got, want)
	}
	if strings.Contains(got, "X\nY\n") {
		t.Fatalf("Y landed right after X, where the At == 0 hunk's Lines point:\n%s", got)
	}

	// The mirror: the same two hunks WITH a position that does locate a
	// window do land by it, so the assertions above can fail.
	positioned := withLines
	positioned.At = 18 // "Alpha second line."
	positioned.Lines = []string{" Alpha second line.", "+Y"}
	posOut := string(applyHunks([]byte(hunkPosBefore), []Hunk{replace, positioned}))
	if !strings.Contains(posOut, "Alpha second line.\nY\nAlpha third line.\n") {
		t.Fatalf("a positioned hunk was not placed by its window:\n%s", posOut)
	}
}
