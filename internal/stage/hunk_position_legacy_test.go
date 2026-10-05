// hunk_position_legacy_test.go pins what a position-free hunk (At == 0) does,
// byte for byte, as base 569c577 did it (052, TD-15). Old open changesets on
// disk carry hunks without at/lines, and the contract for them is "apply as
// before" — so the three misplacements TD-15 measured are frozen here as
// golden bytes, taken from the base before any 052 code existed. A golden
// here is not an endorsement: it is the proof that the legacy path was not
// quietly "fixed" into something an old changeset never produced.
package stage

import "testing"

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
