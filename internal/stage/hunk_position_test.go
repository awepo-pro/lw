// hunk_position_test.go is 052's frozen suite: a Hunk carries its position
// (At + Lines, unified-diff style), so a Review drop or undrop re-applies the
// op's live hunks byte-exactly (TD-15, probed by 050's review). Before 052 a
// hunk was position-free: a Del went to its first textual match, an Add-only
// hunk to its section's end, and ComputeHunks' merged windows lost the
// context between their changes — so `n` then `y` could move a new section,
// bury a body line inside the YAML, or eat the file's trailing newline while
// lint stayed clean.
//
// The legacy half (At == 0 keeps today's placement) lives in
// hunk_position_legacy_test.go; this file is everything about a positioned
// hunk. hunkPosReassemble is the oracle: it rebuilds a file from nothing but
// At and Lines, so no test here trusts applyHunksTraced to grade itself.
package stage

import (
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// hunkPosPath is where the fixture page lives in the scratch vault.
const hunkPosPath = "wiki/concepts/hunk-position-fixture.md"

// hunkPosBefore is the fixture page every round-trip test patches: a
// frontmatter with a sources line, an H1, four sections (the last a list),
// and plenty of blank and repeated lines — the shapes first-text-match and
// section-end placement got wrong. It is canonical (hunkPosEngine checks
// Serialize() reproduces it), so a staged op's Before blob is exactly these
// bytes.
const hunkPosBefore = "---\n" +
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
	"- [[gpt-4]] — the second related link.\n"

// hunkPosEngine opens an Engine over a private copy of the "minimal" fixture
// with hunkPosBefore written in as a page, and returns it with that page as
// the vault loaded it.
func hunkPosEngine(t *testing.T) (*Engine, string, *vault.Page) {
	t.Helper()
	dir := testutil.CopyFixture(t, "minimal")
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(hunkPosPath)), []byte(hunkPosBefore), 0o644); err != nil {
		t.Fatalf("write %s: %v", hunkPosPath, err)
	}
	e, err := OpenEngine(dir)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	e.now = testutil.FixedClock()
	page, ok := e.Vault().Page(hunkPosPath)
	if !ok {
		t.Fatalf("fixture page %s not loaded", hunkPosPath)
	}
	if got := string(page.Serialize()); got != hunkPosBefore {
		t.Fatalf("hunkPosBefore is not canonical:\n--- Serialize() ---\n%s\n--- literal ---\n%s", got, hunkPosBefore)
	}
	return e, dir, page
}

// stageHunkPosPatch stages after as a patch_page op against the fixture page
// the way internal/tools does: hunks from ComputeHunks, each stamped with the
// page path and the op's section. It returns the op id and the hunks as
// staged.
func stageHunkPosPatch(t *testing.T, e *Engine, page *vault.Page, section, after string) (string, []Hunk) {
	t.Helper()
	hunks := ComputeHunks(hunkPosBefore, after)
	for i := range hunks {
		hunks[i].Path = hunkPosPath
		hunks[i].Section = section
	}
	id, err := e.Append(Op{
		Kind:    OpPatchPage,
		Path:    hunkPosPath,
		Section: section,
		Before:  page.SHA256(),
		Content: []byte(after),
		Hunks:   hunks,
	})
	if err != nil {
		t.Fatalf("Append patch_page: %v", err)
	}
	return id, hunks
}

// hunkPosReassemble rebuilds a file from old and the hunks' At/Lines alone:
// lines outside every window are copied from old; inside a window a context
// line is copied, a "-" line is consumed (kept only when takeNew(i) is false,
// i.e. hunk i is dropped), and a "+" line is emitted only when takeNew(i).
// It fails the test if a window does not sit on the old lines it claims. It
// is the independent oracle: it shares no code with applyHunksTraced.
func hunkPosReassemble(t *testing.T, old string, hunks []Hunk, takeNew func(i int) bool) string {
	t.Helper()
	oldLines, endsNL := diffSplitLines(old)
	var out []string
	pos := 0
	for i, h := range hunks {
		start := h.At - 1
		if h.At < 1 || start < pos || start > len(oldLines) {
			t.Fatalf("hunk %s: At=%d is outside the file or overlaps the previous window (cursor %d, %d lines)", h.ID, h.At, pos, len(oldLines))
		}
		out = append(out, oldLines[pos:start]...)
		pos = start
		for _, l := range h.Lines {
			if l == "" {
				t.Fatalf("hunk %s: an empty diff line (every line needs a ' ', '-' or '+' prefix)", h.ID)
			}
			kind, text := l[0], l[1:]
			switch kind {
			case ' ', '-':
				if pos >= len(oldLines) || oldLines[pos] != text {
					t.Fatalf("hunk %s: line %q does not match old line %d (%q)", h.ID, l, pos+1, lineAt(oldLines, pos))
				}
				if kind == ' ' || !takeNew(i) {
					out = append(out, text)
				}
				pos++
			case '+':
				if takeNew(i) {
					out = append(out, text)
				}
			default:
				t.Fatalf("hunk %s: diff line %q has prefix %q, want ' ', '-' or '+'", h.ID, l, string(kind))
			}
		}
	}
	out = append(out, oldLines[pos:]...)
	if len(out) == 0 {
		return ""
	}
	s := strings.Join(out, "\n")
	if endsNL {
		s += "\n"
	}
	return s
}

// lineAt is lines[i], or "<past end>" — for failure messages only.
func lineAt(lines []string, i int) string {
	if i < 0 || i >= len(lines) {
		return "<past end>"
	}
	return lines[i]
}

// hunkPosOneNewline reports whether s ends with exactly one "\n".
func hunkPosOneNewline(s string) bool {
	return strings.HasSuffix(s, "\n") && !strings.HasSuffix(s, "\n\n")
}

// hunkPosShape is one patch kind: build returns the section the op names and
// the page text after the edit, derived from the page by the same vault
// primitives internal/tools' stage.patch_page uses.
type hunkPosShape struct {
	name       string
	minHunks   int
	build      func(t *testing.T, page *vault.Page) (section, after string)
	wantMerged bool // the op must contain a hunk with interior context between two change runs
}

// hunkPosPageWith returns the serialized fixture page with its body replaced.
func hunkPosPageWith(page *vault.Page, body string) string {
	p := vault.Page{Path: page.Path, FM: page.FM, Body: body}
	return string(p.Serialize())
}

// hunkPosSection is page.Section(heading), failing the test when absent.
func hunkPosSection(t *testing.T, page *vault.Page, heading string) vault.Section {
	t.Helper()
	sec, ok := page.Section(heading)
	if !ok {
		t.Fatalf("fixture has no section %q", heading)
	}
	return sec
}

// hunkPosShapes lists every patch kind 052 must round-trip, plus the two
// multi-hunk shapes (a frontmatter line and a body edit; three shifting body
// edits) that make a later hunk's position depend on an earlier one.
func hunkPosShapes() []hunkPosShape {
	return []hunkPosShape{
		{name: "replace_text mid-section insert", minHunks: 1, build: func(t *testing.T, page *vault.Page) (string, string) {
			body, n := vault.ReplaceTextInSection(page.Body, hunkPosSection(t, page, "## Alpha"),
				"Alpha second line.", "Alpha second line.\nAlpha inserted line.")
			if n != 1 {
				t.Fatalf("replace_text matched %d times, want 1", n)
			}
			return "## Alpha", hunkPosPageWith(page, body)
		}},
		{name: "replace_section", minHunks: 1, wantMerged: true, build: func(t *testing.T, page *vault.Page) (string, string) {
			sec := hunkPosSection(t, page, "## Beta")
			body := vault.ReplaceSection(page.Body, sec,
				"\nBeta first line, rewritten.\n\n- beta item one\n- beta item two\n- beta item three\n\nBeta closing line.\n\n")
			return "## Beta", hunkPosPageWith(page, body)
		}},
		{name: "append_section middle", minHunks: 1, build: func(t *testing.T, page *vault.Page) (string, string) {
			body := vault.AppendToSection(page.Body, hunkPosSection(t, page, "## Beta"), "Beta appended paragraph.\n\n")
			return "## Beta", hunkPosPageWith(page, body)
		}},
		{name: "append_section last", minHunks: 1, build: func(t *testing.T, page *vault.Page) (string, string) {
			body := vault.AppendToSection(page.Body, hunkPosSection(t, page, "## Related"), "- [[flash-attention]] — the appended link.\n\n")
			return "## Related", hunkPosPageWith(page, body)
		}},
		{name: "insert_after", minHunks: 1, build: func(t *testing.T, page *vault.Page) (string, string) {
			body := vault.InsertAfterSection(page.Body, hunkPosSection(t, page, "## Beta"), "## Delta\n\nDelta text.")
			return "## Beta", hunkPosPageWith(page, body)
		}},
		{name: "insert_before", minHunks: 1, build: func(t *testing.T, page *vault.Page) (string, string) {
			body := vault.InsertBeforeSection(page.Body, hunkPosSection(t, page, "## Beta"), "## Inserted\n\nInserted text.")
			return "## Beta", hunkPosPageWith(page, body)
		}},
		{name: "remove_section", minHunks: 1, build: func(t *testing.T, page *vault.Page) (string, string) {
			body := vault.RemoveSection(page.Body, hunkPosSection(t, page, "## Gamma"))
			return "## Gamma", hunkPosPageWith(page, body)
		}},
		{name: "frontmatter sources line plus a body edit", minHunks: 2, build: func(t *testing.T, page *vault.Page) (string, string) {
			after := strings.Replace(hunkPosBefore,
				"sources: [raw/articles/kv-cache-explained.md]",
				"sources: [raw/articles/kv-cache-explained.md, raw/papers/leviathan-2023.md]", 1)
			after = strings.Replace(after, "Gamma line three.", "Gamma line three, revised.", 1)
			return "## Gamma", after
		}},
		{name: "three body edits that shift the lines below them", minHunks: 3, build: func(t *testing.T, page *vault.Page) (string, string) {
			after := strings.Replace(hunkPosBefore, "Alpha second line.", "Alpha second line.\nAlpha inserted line.", 1)
			after = strings.Replace(after, "Beta closing line.", "Beta closing line.\n\nBeta extra paragraph one.\n\nBeta extra paragraph two.", 1)
			after = strings.Replace(after, "Gamma line four.", "Gamma line four, revised.", 1)
			return "## Gamma", after
		}},
	}
}

// TestHunkRoundTripEveryPatchKind is the 052 acceptance in miniature: for
// every patch kind, built through ComputeHunks on the fixture page, a Review
// `n` then `y` on any hunk leaves the op byte-identical, `n` on everything
// gives exactly the before bytes, and `n` on one hunk gives exactly the
// other hunks applied (with the file's single trailing newline intact).
func TestHunkRoundTripEveryPatchKind(t *testing.T) {
	for _, shape := range hunkPosShapes() {
		t.Run(shape.name, func(t *testing.T) {
			e, _, page := hunkPosEngine(t)
			if _, err := e.OpenChangeset("hunk position round trip", testAuthor); err != nil {
				t.Fatalf("OpenChangeset: %v", err)
			}
			section, after := shape.build(t, page)
			if after == hunkPosBefore {
				t.Fatal("the shape does not change the page")
			}
			id, hunks := stageHunkPosPatch(t, e, page, section, after)
			if len(hunks) < shape.minHunks {
				t.Fatalf("ComputeHunks gave %d hunks, the shape needs at least %d", len(hunks), shape.minHunks)
			}
			want := string(currentOpAfterBytes(t, e, id))
			if want != after {
				t.Fatalf("op.After right after Append != the proposed page:\n--- got ---\n%s\n--- want ---\n%s", want, after)
			}

			// The oracle agrees with the two files before the engine is asked.
			if got := hunkPosReassemble(t, hunkPosBefore, hunks, func(int) bool { return true }); got != after {
				t.Fatalf("At+Lines do not reassemble the after file:\n--- got ---\n%s\n--- want ---\n%s", got, after)
			}
			if got := hunkPosReassemble(t, hunkPosBefore, hunks, func(int) bool { return false }); got != hunkPosBefore {
				t.Fatalf("At+Lines do not reassemble the before file:\n--- got ---\n%s\n--- want ---\n%s", got, hunkPosBefore)
			}

			if shape.wantMerged {
				merged := false
				for _, h := range hunks {
					merged = merged || hunkPosInterior(h) > 0
				}
				if !merged {
					t.Fatalf("no hunk has interior context, the shape is meant to produce one: %+v", hunks)
				}
			}

			for i, h := range hunks {
				// n on hunk h alone.
				if err := e.DropHunk(id, h.ID); err != nil {
					t.Fatalf("DropHunk %s: %v", h.ID, err)
				}
				alone := string(currentOpAfterBytes(t, e, id))

				others := make([]Hunk, len(hunks))
				copy(others, hunks)
				others[i].Dropped = true
				if viaApply := string(applyHunks([]byte(hunkPosBefore), others)); alone != viaApply {
					t.Fatalf("dropping %s alone != applyHunks of the others:\n--- got ---\n%s\n--- want ---\n%s", h.ID, alone, viaApply)
				}
				if viaOracle := hunkPosReassemble(t, hunkPosBefore, hunks, func(k int) bool { return k != i }); alone != viaOracle {
					t.Fatalf("dropping %s alone != the other hunks' reassembly:\n--- got ---\n%s\n--- want ---\n%s", h.ID, alone, viaOracle)
				}
				if !hunkPosOneNewline(alone) {
					t.Fatalf("dropping %s alone lost the file's single trailing newline: ends %q", h.ID, tailOf(alone))
				}

				// y on it again.
				if err := e.UndropHunk(id, h.ID); err != nil {
					t.Fatalf("UndropHunk %s: %v", h.ID, err)
				}
				if got := string(currentOpAfterBytes(t, e, id)); got != want {
					t.Fatalf("drop+undrop of %s changed the op:\n--- got ---\n%s\n--- want ---\n%s", h.ID, got, want)
				}
			}

			// n on every hunk is the before file, byte for byte.
			for _, h := range hunks {
				if err := e.DropHunk(id, h.ID); err != nil {
					t.Fatalf("DropHunk %s: %v", h.ID, err)
				}
			}
			if got := string(currentOpAfterBytes(t, e, id)); got != hunkPosBefore {
				t.Fatalf("dropping every hunk != the before file:\n--- got ---\n%s\n--- want ---\n%s", got, hunkPosBefore)
			}
			// y on every hunk, last first, is the proposal again.
			for i := len(hunks) - 1; i >= 0; i-- {
				if err := e.UndropHunk(id, hunks[i].ID); err != nil {
					t.Fatalf("UndropHunk %s: %v", hunks[i].ID, err)
				}
			}
			if got := string(currentOpAfterBytes(t, e, id)); got != want {
				t.Fatalf("undropping every hunk != the proposal:\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}
		})
	}
}

// tailOf is the last 20 bytes of s, for failure messages.
func tailOf(s string) string {
	if len(s) > 20 {
		return s[len(s)-20:]
	}
	return s
}

// hunkPosInterior counts the context lines between h's first and last change
// lines — nonzero for a window ComputeHunks merged from separate changes.
func hunkPosInterior(h Hunk) int {
	first, last := -1, -1
	for i, l := range h.Lines {
		if l != "" && l[0] != ' ' {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	n := 0
	for i := first + 1; first >= 0 && i < last; i++ {
		if h.Lines[i] != "" && h.Lines[i][0] == ' ' {
			n++
		}
	}
	return n
}

// hunkPosLines joins lines with "\n" and ends the text with one.
func hunkPosLines(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

// TestHunkAtSetByComputeHunks pins ComputeHunks' new output: every hunk
// carries a 1-based At and its window's diff Lines, the Add/Del/Before
// fields are filled as before, the window count is the diff's own, and the
// before and after files reassemble from the hunks plus the untouched lines.
func TestHunkAtSetByComputeHunks(t *testing.T) {
	twelve := func(edit func(l []string) []string) (string, string) {
		old := []string{"l01", "l02", "l03", "l04", "l05", "l06", "l07", "l08", "l09", "l10", "l11", "l12"}
		cp := append([]string(nil), old...)
		return hunkPosLines(old...), hunkPosLines(edit(cp)...)
	}
	set := func(i int, v string) func(l []string) []string {
		return func(l []string) []string { l[i] = v; return l }
	}
	startOld, startNew := twelve(set(0, "L01"))
	midOld, midNew := twelve(set(5, "L06"))
	endOld, endNew := twelve(set(11, "L12"))
	insStartOld, insStartNew := twelve(func(l []string) []string { return append([]string{"L00"}, l...) })
	appendOld, appendNew := twelve(func(l []string) []string { return append(l, "l13") })
	delEndOld, delEndNew := twelve(func(l []string) []string { return l[:11] })
	mergedOld, mergedNew := twelve(func(l []string) []string { l[2] = "L03"; l[6] = "L07"; return l })
	twoOld, twoNew := twelve(func(l []string) []string {
		l = append(l, "l13", "l14")
		l[1] = "L02"
		l[12] = "L13"
		return l
	})

	cases := []struct {
		name      string
		old, new  string
		wantCount int
		wantAt    []int
	}{
		{"change at the start", startOld, startNew, 1, []int{1}},
		{"change in the middle", midOld, midNew, 1, []int{3}},
		{"change at the end", endOld, endNew, 1, []int{9}},
		{"insertion before the first line", insStartOld, insStartNew, 1, []int{1}},
		{"insertion after the last line", appendOld, appendNew, 1, []int{10}},
		{"deletion of the last line", delEndOld, delEndNew, 1, []int{9}},
		{"two changes with interior context", mergedOld, mergedNew, 1, []int{1}},
		{"two separate windows", twoOld, twoNew, 2, []int{1, 10}},
		{"a patched page", hunkPosBefore, strings.Replace(hunkPosBefore, "Alpha second line.", "Alpha second line.\nAlpha inserted line.", 1), 1, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hunks := ComputeHunks(tc.old, tc.new)
			oldLines, _ := diffSplitLines(tc.old)
			newLines, _ := diffSplitLines(tc.new)
			if wantWindows := len(hunkWindows(diffOps(oldLines, newLines), diffContext)); len(hunks) != wantWindows || len(hunks) != tc.wantCount {
				t.Fatalf("got %d hunks, the diff has %d windows, want %d", len(hunks), wantWindows, tc.wantCount)
			}
			for i, h := range hunks {
				if wantID := "h" + string(rune('1'+i)); h.ID != wantID {
					t.Fatalf("hunk %d id = %q, want %q", i, h.ID, wantID)
				}
				if h.At < 1 {
					t.Fatalf("hunk %s: At = %d, want >= 1", h.ID, h.At)
				}
				if len(h.Lines) == 0 {
					t.Fatalf("hunk %s: no Lines", h.ID)
				}
				if tc.wantAt != nil && h.At != tc.wantAt[i] {
					t.Fatalf("hunk %s: At = %d, want %d", h.ID, h.At, tc.wantAt[i])
				}
				// Add, Del and Before are filled from the same lines as before 052.
				var add, del, before []string
				for _, l := range h.Lines {
					switch l[0] {
					case '+':
						add = append(add, l[1:])
					case '-':
						del = append(del, l[1:])
						before = append(before, l[1:])
					case ' ':
						before = append(before, l[1:])
					default:
						t.Fatalf("hunk %s: Lines entry %q has no ' ', '-' or '+' prefix", h.ID, l)
					}
				}
				if !reflect.DeepEqual(add, h.Add) && !(len(add) == 0 && len(h.Add) == 0) {
					t.Fatalf("hunk %s: Add = %q, but Lines hold %q", h.ID, h.Add, add)
				}
				if !reflect.DeepEqual(del, h.Del) && !(len(del) == 0 && len(h.Del) == 0) {
					t.Fatalf("hunk %s: Del = %q, but Lines hold %q", h.ID, h.Del, del)
				}
				if !reflect.DeepEqual(before, h.Before) {
					t.Fatalf("hunk %s: Before = %q, but Lines hold %q", h.ID, h.Before, before)
				}
			}
			if got := hunkPosReassemble(t, tc.old, hunks, func(int) bool { return true }); got != tc.new {
				t.Fatalf("At+Lines reassemble the after file wrongly:\n--- got ---\n%s\n--- want ---\n%s", got, tc.new)
			}
			if got := hunkPosReassemble(t, tc.old, hunks, func(int) bool { return false }); got != tc.old {
				t.Fatalf("At+Lines reassemble the before file wrongly:\n--- got ---\n%s\n--- want ---\n%s", got, tc.old)
			}
		})
	}

	// One window's exact shape, so "1-based, context included" cannot drift.
	t.Run("a middle change's Lines", func(t *testing.T) {
		hunks := ComputeHunks(midOld, midNew)
		want := []string{" l03", " l04", " l05", "-l06", "+L06", " l07", " l08", " l09"}
		if len(hunks) != 1 || !reflect.DeepEqual(hunks[0].Lines, want) {
			t.Fatalf("Lines = %q, want %q", hunks[0].Lines, want)
		}
	})
}

// TestHunkMergedWindowRoundTrip: two body changes 3 lines apart share ONE
// window (ComputeHunks merges changes up to 7 ops apart), so the window has
// interior context the flat Add/Del lists lose. This is 050's review M1 shape
// — there a frontmatter `sources:` line and a body line shared the window and
// the body line landed inside the YAML. The flat Add list re-applied the later
// Add right after the earlier one (here: the inserted line took the place of
// "Alpha first line." and the rewrite followed it). With the window's interior
// context in Lines, Review's n then y is byte-identical and each line sits
// where it was proposed.
//
// A-052-2 (052 S1b): the original fixture was a sources: line plus a body
// line, which S1b's frontmatter split (TestHunkFrontmatterBodySplit) now cuts
// into two hunks, so both changes moved into the body, three context lines
// apart, to keep exercising ONE merged window. The frontmatter case lives in
// the new test; every assertion here is otherwise the original's.
func TestHunkMergedWindowRoundTrip(t *testing.T) {
	e, _, page := hunkPosEngine(t)
	if _, err := e.OpenChangeset("merged window", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	const (
		introLine = "Intro paragraph linking to [[kv-cache]] and [[gpt-4]]."
		bodyLine  = "Inserted body line."
		oldAlpha  = "Alpha first line."
		newAlpha  = "Alpha first line, rewritten."
	)
	// A blank line, "## Alpha" and another blank line sit between the two
	// changes.
	after := strings.Replace(hunkPosBefore, introLine+"\n", introLine+"\n"+bodyLine+"\n", 1)
	after = strings.Replace(after, oldAlpha+"\n", newAlpha+"\n", 1)
	if after == hunkPosBefore || !strings.Contains(after, bodyLine) || !strings.Contains(after, newAlpha) {
		t.Fatal("the fixture edit did not apply")
	}

	id, hunks := stageHunkPosPatch(t, e, page, "## Alpha", after)
	if len(hunks) != 1 {
		t.Fatalf("ComputeHunks gave %d hunks, want exactly 1 merged window", len(hunks))
	}
	h := hunks[0]
	if len(h.Del) != 1 || h.Del[0] != oldAlpha || len(h.Add) != 2 || h.Add[0] != bodyLine || h.Add[1] != newAlpha {
		t.Fatalf("not M1's shape: Del=%q Add=%q", h.Del, h.Add)
	}
	if got := hunkPosInterior(h); got != 3 {
		t.Fatalf("interior context = %d lines, want 3: %q", got, h.Lines)
	}

	want := string(currentOpAfterBytes(t, e, id))
	if want != after {
		t.Fatalf("op.After != the proposal:\n%s", want)
	}
	if err := e.DropHunk(id, h.ID); err != nil {
		t.Fatalf("DropHunk: %v", err)
	}
	if got := string(currentOpAfterBytes(t, e, id)); got != hunkPosBefore {
		t.Fatalf("dropping the merged hunk != the before file:\n%s", got)
	}
	if err := e.UndropHunk(id, h.ID); err != nil {
		t.Fatalf("UndropHunk: %v", err)
	}
	got := string(currentOpAfterBytes(t, e, id))
	if got != want {
		t.Fatalf("drop+undrop of the merged hunk is not byte-identical:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	// The inserted line sits right under the intro paragraph, and the
	// rewrite is where "Alpha first line." was — not swapped by pairing the
	// first Add with the first Del.
	lines := strings.Split(got, "\n")
	at := -1
	for i, l := range lines {
		if l == bodyLine {
			at = i
			break
		}
	}
	if at < 1 || lines[at-1] != introLine {
		t.Fatalf("the inserted line (index %d) is not right under the intro paragraph:\n%s", at, got)
	}
	if strings.Count(got, oldAlpha+"\n") != 0 || strings.Count(got, newAlpha+"\n") != 1 {
		t.Fatalf("the Alpha rewrite is not exactly where the old line was:\n%s", got)
	}
}

// TestHunkAtMismatchFallsBack: a positioned hunk whose window cannot be
// located, or whose context or "-" lines no longer match the lines under it,
// is applied by NOTHING of the positioned path — it falls back to the
// legacy placement in full, so a stale position is never worse than before
// 052. The hunk here is a blank-line replacement, where legacy and
// positioned results differ (first blank line vs. the one in "## Gamma"), so
// "fell back" is observable and "applied partially" is not hiding.
func TestHunkAtMismatchFallsBack(t *testing.T) {
	after := strings.Replace(hunkPosBefore, "Gamma line two.\n\nGamma line three.", "Gamma line two.\nGamma inserted line.\nGamma line three.", 1)
	hunks := ComputeHunks(hunkPosBefore, after)
	if len(hunks) != 1 {
		t.Fatalf("ComputeHunks gave %d hunks, want 1", len(hunks))
	}
	good := hunks[0]
	good.Path = hunkPosPath
	if !reflect.DeepEqual(good.Del, []string{""}) || !reflect.DeepEqual(good.Add, []string{"Gamma inserted line."}) {
		t.Fatalf("not the blank-line shape: Del=%q Add=%q", good.Del, good.Add)
	}
	if got := string(applyHunks([]byte(hunkPosBefore), []Hunk{good})); got != after {
		t.Fatalf("the unmutated positioned hunk does not reproduce the after file:\n%s", got)
	}

	legacy := good
	legacy.At, legacy.Lines = 0, nil
	wantLegacy := string(applyHunks([]byte(hunkPosBefore), []Hunk{legacy}))
	if wantLegacy != hunkPosLegacyBlank {
		t.Fatal("the legacy placement of this hunk is not the pinned base output")
	}
	if wantLegacy == after {
		t.Fatal("the shape cannot tell legacy from positioned placement")
	}

	mutate := func(f func(h *Hunk)) Hunk {
		h := good
		h.Lines = append([]string(nil), good.Lines...)
		f(&h)
		return h
	}
	cases := []struct {
		name string
		hunk Hunk
	}{
		{"At points at other lines", mutate(func(h *Hunk) { h.At += 3 })},
		{"At is past the end of the file", mutate(func(h *Hunk) { h.At = 999 })},
		{"the first context line changed", mutate(func(h *Hunk) { h.Lines[0] = " not the line under At" })},
		{"a later context line changed", mutate(func(h *Hunk) { h.Lines[len(h.Lines)-1] = " not the last context line" })},
		{"a removed line changed", mutate(func(h *Hunk) {
			for i, l := range h.Lines {
				if l[0] == '-' {
					h.Lines[i] = "-text the file never had"
				}
			}
		})},
		{"the window runs past the last line into the split's empty tail", mutate(func(h *Hunk) {
			// The file's last real line, then a blank context line: the only
			// "line" left is the empty string strings.Split leaves after the
			// final "\n", which is not a line of the file.
			h.At = strings.Count(hunkPosBefore, "\n")
			h.Lines = []string{" - [[gpt-4]] — the second related link.", " "}
		})},
		{"a malformed diff line", mutate(func(h *Hunk) { h.Lines[1] = "?" + h.Lines[1][1:] })},
		{"an empty diff line", mutate(func(h *Hunk) { h.Lines[2] = "" })},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(applyHunks([]byte(hunkPosBefore), []Hunk{tc.hunk}))
			if got != wantLegacy {
				t.Fatalf("a mismatched position did not fall back to the legacy placement:\n--- got ---\n%s\n--- want (legacy) ---\n%s", got, wantLegacy)
			}
		})
	}
}

// TestHunkAtJSON: at and lines are persisted fields (json "at", "lines",
// omitempty). They round-trip through the changeset JSON, an unpositioned
// hunk writes neither key, and a hunk read from an old changeset.json — no
// `at` key — loads as At 0 with no Lines. They also survive the engine's
// own persistence (a second Engine reading the open changeset from disk) and
// the deep copy Current hands out.
func TestHunkAtJSON(t *testing.T) {
	t.Run("round trip", func(t *testing.T) {
		in := Hunk{ID: "h1", Path: "wiki/a.md", Section: "## S", Add: []string{"new"}, Del: []string{"old"},
			At: 7, Lines: []string{" ctx", "-old", "+new", " ctx2"}}
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(b, &raw); err != nil {
			t.Fatalf("Unmarshal raw: %v", err)
		}
		if string(raw["at"]) != "7" {
			t.Fatalf(`json "at" = %s, want 7 (%s)`, raw["at"], b)
		}
		if string(raw["lines"]) != `[" ctx","-old","+new"," ctx2"]` {
			t.Fatalf(`json "lines" = %s (%s)`, raw["lines"], b)
		}
		var out Hunk
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if out.At != in.At || !reflect.DeepEqual(out.Lines, in.Lines) {
			t.Fatalf("round trip lost the position: got At=%d Lines=%q, want At=%d Lines=%q", out.At, out.Lines, in.At, in.Lines)
		}
	})

	t.Run("an unpositioned hunk writes neither key", func(t *testing.T) {
		b, err := json.Marshal(Hunk{ID: "h1", Path: "wiki/a.md", Add: []string{"x"}})
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if strings.Contains(string(b), `"at"`) || strings.Contains(string(b), `"lines"`) {
			t.Fatalf("unpositioned hunk serialized a position: %s", b)
		}
	})

	t.Run("a hunk with no at key loads as 0", func(t *testing.T) {
		var h Hunk
		if err := json.Unmarshal([]byte(`{"id":"h1","path":"wiki/a.md","section":"## S","+":["x"],"-":["y"]}`), &h); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if h.At != 0 || h.Lines != nil {
			t.Fatalf("an old hunk loaded as positioned: At=%d Lines=%q", h.At, h.Lines)
		}
		if !reflect.DeepEqual(h.Add, []string{"x"}) || !reflect.DeepEqual(h.Del, []string{"y"}) {
			t.Fatalf("legacy fields lost: Add=%q Del=%q", h.Add, h.Del)
		}
	})

	t.Run("survives the engine's persistence and Current's copy", func(t *testing.T) {
		e, dir, page := hunkPosEngine(t)
		if _, err := e.OpenChangeset("persist positions", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		after := strings.Replace(hunkPosBefore, "Gamma line three.", "Gamma line three, revised.", 1)
		id, staged := stageHunkPosPatch(t, e, page, "## Gamma", after)

		fromDisk, err := foreignEngine(t, dir).Current()
		if err != nil {
			t.Fatalf("Current (second engine, from disk): %v", err)
		}
		op, ok := fromDisk.Op(id)
		if !ok || len(op.Hunks) != len(staged) {
			t.Fatalf("op %s not reloaded intact: ok=%v hunks=%d", id, ok, len(op.Hunks))
		}
		for i, h := range op.Hunks {
			if h.At != staged[i].At || h.At < 1 || !reflect.DeepEqual(h.Lines, staged[i].Lines) {
				t.Fatalf("hunk %s reloaded as At=%d Lines=%q, staged At=%d Lines=%q", h.ID, h.At, h.Lines, staged[i].At, staged[i].Lines)
			}
		}

		cur, err := e.Current()
		if err != nil {
			t.Fatalf("Current: %v", err)
		}
		cop, _ := cur.Op(id)
		if cop.Hunks[0].At != staged[0].At || !reflect.DeepEqual(cop.Hunks[0].Lines, staged[0].Lines) {
			t.Fatalf("Current's copy lost the position: At=%d Lines=%q", cop.Hunks[0].At, cop.Hunks[0].Lines)
		}
		// The copy is deep: mutating it must not reach the engine's.
		cop.Hunks[0].Lines[0] = "?mutated"
		again, err := e.Current()
		if err != nil {
			t.Fatalf("Current (again): %v", err)
		}
		aop, _ := again.Op(id)
		if aop.Hunks[0].Lines[0] == "?mutated" {
			t.Fatal("Current handed out a shallow copy of Hunk.Lines")
		}
	})
}

// TestHunkDuplicateBlankLines: a page full of blank lines and a hunk whose
// Del is a blank line in the third section. Before 052 the first blank line
// in the FILE — the one after the frontmatter — was the one replaced. With
// At the round trip is byte-exact for a blank replaced, removed and added.
func TestHunkDuplicateBlankLines(t *testing.T) {
	gamma := strings.Index(hunkPosBefore, "## Gamma")
	if gamma < 0 {
		t.Fatal("the fixture has no Gamma section")
	}
	if blanks := strings.Count(hunkPosBefore[:gamma], "\n\n"); blanks < 6 {
		t.Fatalf("the fixture has %d blank lines before Gamma, the test needs many", blanks)
	}
	cases := []struct {
		name         string
		old, new     string
		wantDel      []string
		wantAdd      []string
		legacyBroken bool // the legacy placement picks the wrong blank line, so the old code failed here
	}{
		{"a blank line replaced by text", "Gamma line two.\n\nGamma line three.", "Gamma line two.\nGamma inserted line.\nGamma line three.", []string{""}, []string{"Gamma inserted line."}, true},
		{"a blank line removed", "Gamma line two.\n\nGamma line three.", "Gamma line two.\nGamma line three.", []string{""}, nil, true},
		{"a blank line added", "Gamma line three.\n\nGamma line four.", "Gamma line three.\n\n\nGamma line four.", nil, []string{""}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, _, page := hunkPosEngine(t)
			if _, err := e.OpenChangeset("duplicate blank lines", testAuthor); err != nil {
				t.Fatalf("OpenChangeset: %v", err)
			}
			after := strings.Replace(hunkPosBefore, tc.old, tc.new, 1)
			if after == hunkPosBefore {
				t.Fatal("the edit did not apply")
			}
			id, hunks := stageHunkPosPatch(t, e, page, "## Gamma", after)
			if len(hunks) != 1 {
				t.Fatalf("ComputeHunks gave %d hunks, want 1", len(hunks))
			}
			if !reflect.DeepEqual(hunks[0].Del, tc.wantDel) && !(len(hunks[0].Del) == 0 && len(tc.wantDel) == 0) {
				t.Fatalf("Del = %q, want %q", hunks[0].Del, tc.wantDel)
			}
			if !reflect.DeepEqual(hunks[0].Add, tc.wantAdd) && !(len(hunks[0].Add) == 0 && len(tc.wantAdd) == 0) {
				t.Fatalf("Add = %q, want %q", hunks[0].Add, tc.wantAdd)
			}

			if got := string(applyHunks([]byte(hunkPosBefore), hunks)); got != after {
				t.Fatalf("applying the live hunk != the after file:\n--- got ---\n%s\n--- want ---\n%s", got, after)
			}
			if err := e.DropHunk(id, "h1"); err != nil {
				t.Fatalf("DropHunk: %v", err)
			}
			if got := string(currentOpAfterBytes(t, e, id)); got != hunkPosBefore {
				t.Fatalf("dropping the hunk != the before file:\n%s", got)
			}
			if err := e.UndropHunk(id, "h1"); err != nil {
				t.Fatalf("UndropHunk: %v", err)
			}
			if got := string(currentOpAfterBytes(t, e, id)); got != after {
				t.Fatalf("drop+undrop is not byte-exact:\n--- got ---\n%s\n--- want ---\n%s", got, after)
			}

			if tc.legacyBroken {
				legacy := hunks[0]
				legacy.At, legacy.Lines = 0, nil
				if got := string(applyHunks([]byte(hunkPosBefore), []Hunk{legacy})); got == after {
					t.Fatal("the legacy placement also gets this right: the case does not exercise TD-15")
				}
			}
		})
	}
}

// TestHunkPositionedOwnership: the per-line ownership record the Review
// attribution consumes stays exact on the positioned path. Two hunks of one
// op — a frontmatter line replaced and a body line replaced — each own
// exactly the line they produced, and the before line each removed is
// attributed to it; dropping one hunk takes its ownership with it.
func TestHunkPositionedOwnership(t *testing.T) {
	const (
		oldSources = "sources: [raw/articles/kv-cache-explained.md]"
		newSources = "sources: [raw/articles/kv-cache-explained.md, raw/papers/leviathan-2023.md]"
		oldBody    = "Gamma line three."
		newBody    = "Gamma line three, revised."
	)
	after := strings.Replace(hunkPosBefore, oldSources, newSources, 1)
	after = strings.Replace(after, oldBody, newBody, 1)
	hunks := ComputeHunks(hunkPosBefore, after)
	if len(hunks) != 2 {
		t.Fatalf("ComputeHunks gave %d hunks, want 2", len(hunks))
	}
	for i := range hunks {
		hunks[i].Path = hunkPosPath
	}

	index := func(lines []string, s string) int {
		for i, l := range lines {
			if l == s {
				return i
			}
		}
		t.Fatalf("line %q not found", s)
		return -1
	}
	check := func(name string, hs []Hunk, wantOut string, newOwners, oldRemovers map[string]string) {
		t.Helper()
		out, newOwner, oldRemover := applyHunksTraced([]byte(hunkPosBefore), hs)
		if string(out) != wantOut {
			t.Fatalf("%s: bytes differ:\n%s", name, out)
		}
		outLines := strings.Split(string(out), "\n")
		beforeLines := strings.Split(hunkPosBefore, "\n")
		if len(newOwner) != len(outLines) || len(oldRemover) != len(beforeLines) {
			t.Fatalf("%s: trace sizes %d/%d, want %d/%d", name, len(newOwner), len(oldRemover), len(outLines), len(beforeLines))
		}
		wantNew := make([]string, len(outLines))
		for line, owner := range newOwners {
			wantNew[index(outLines, line)] = owner
		}
		wantOld := make([]string, len(beforeLines))
		for line, owner := range oldRemovers {
			wantOld[index(beforeLines, line)] = owner
		}
		if !reflect.DeepEqual(newOwner, wantNew) {
			t.Fatalf("%s: newOwner = %q, want %q", name, newOwner, wantNew)
		}
		if !reflect.DeepEqual(oldRemover, wantOld) {
			t.Fatalf("%s: oldRemover = %q, want %q", name, oldRemover, wantOld)
		}
	}

	check("both live", hunks, after,
		map[string]string{newSources: "h1", newBody: "h2"},
		map[string]string{oldSources: "h1", oldBody: "h2"})

	h1Dropped := []Hunk{hunks[0], hunks[1]}
	h1Dropped[0].Dropped = true
	check("h1 dropped", h1Dropped, strings.Replace(hunkPosBefore, oldBody, newBody, 1),
		map[string]string{newBody: "h2"},
		map[string]string{oldBody: "h2"})

	h2Dropped := []Hunk{hunks[0], hunks[1]}
	h2Dropped[1].Dropped = true
	check("h2 dropped", h2Dropped, strings.Replace(hunkPosBefore, oldSources, newSources, 1),
		map[string]string{newSources: "h1"},
		map[string]string{oldSources: "h1"})

	// An insertion owns exactly its added lines, and removes nothing.
	insAfter := strings.Replace(hunkPosBefore, "Alpha second line.", "Alpha second line.\nAlpha inserted line.", 1)
	ins := ComputeHunks(hunkPosBefore, insAfter)
	if len(ins) != 1 {
		t.Fatalf("ComputeHunks gave %d hunks for the insertion, want 1", len(ins))
	}
	ins[0].Path = hunkPosPath
	ins[0].Section = "## Alpha" // the stamp that sent this hunk to the section's end before 052
	check("insertion", ins, insAfter, map[string]string{"Alpha inserted line.": "h1"}, nil)
}

// TestHunkMixedPositionedAndLegacy: one op may hold both kinds — an old
// changeset's position-free hunk next to a positioned one (a hand-built op,
// or a future tool that stages a legacy hunk). Each is applied by its own
// path, in either order, and the positioned one still finds its window after
// the legacy one has shifted or replaced lines above it.
func TestHunkMixedPositionedAndLegacy(t *testing.T) {
	legacy := Hunk{ID: "h1", Path: hunkPosPath, Del: []string{"Alpha first line."}, Add: []string{"Alpha first line, edited.", "Alpha added line."}}
	insAfter := strings.Replace(hunkPosBefore, "Gamma line one.", "Gamma line one.\nGamma added line.", 1)
	positioned := ComputeHunks(hunkPosBefore, insAfter)
	if len(positioned) != 1 {
		t.Fatalf("ComputeHunks gave %d hunks, want 1", len(positioned))
	}
	positioned[0].ID = "h2"
	positioned[0].Path = hunkPosPath
	positioned[0].Section = "## Gamma"

	want := strings.Replace(insAfter, "Alpha first line.", "Alpha first line, edited.\nAlpha added line.", 1)
	for name, hunks := range map[string][]Hunk{
		"legacy then positioned": {legacy, positioned[0]},
		"positioned then legacy": {positioned[0], legacy},
	} {
		t.Run(name, func(t *testing.T) {
			if got := string(applyHunks([]byte(hunkPosBefore), hunks)); got != want {
				t.Fatalf("mixed hunks:\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}
		})
	}
}

// TestHunkRoundTripRandomEdits is the property behind the shape tests: for
// any pair of files, applying ComputeHunks' hunks reproduces the after file,
// dropping them all reproduces the before file, dropping any one gives the
// other hunks' result, and the ownership trace tags exactly the lines each
// hunk added and removed. The files draw from five distinct lines so that
// duplicates and blank lines — what first-text-match got wrong — are
// everywhere; the seed is fixed so a failure reproduces.
func TestHunkRoundTripRandomEdits(t *testing.T) {
	rng := rand.New(rand.NewSource(52))
	alphabet := []string{"", "a", "b", "c", "x"}
	randomLines := func(n int) []string {
		l := make([]string, n)
		for i := range l {
			l[i] = alphabet[rng.Intn(len(alphabet))]
		}
		return l
	}
	for trial := 0; trial < 400; trial++ {
		old := randomLines(1 + rng.Intn(40))
		cur := append([]string(nil), old...)
		for edits := 1 + rng.Intn(6); edits > 0; edits-- {
			i := rng.Intn(len(cur) + 1)
			switch kind := rng.Intn(3); {
			case kind == 0: // insert
				cur = append(cur[:i], append(randomLines(1+rng.Intn(3)), cur[i:]...)...)
			case kind == 1 && i < len(cur) && len(cur) > 1: // delete
				cur = append(cur[:i], cur[i+1:]...)
			case i < len(cur): // replace
				cur[i] = alphabet[rng.Intn(len(alphabet))]
			}
		}
		oldText, newText := hunkPosLines(old...), hunkPosLines(cur...)
		hunks := ComputeHunks(oldText, newText)
		for i := range hunks {
			hunks[i].Path = hunkPosPath
		}
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("trial %d: "+format+"\n--- old ---\n%s--- new ---\n%s", append(append([]any{trial}, args...), oldText, newText)...)
		}

		out, newOwner, oldRemover := applyHunksTraced([]byte(oldText), hunks)
		if string(out) != newText {
			fail("all live != the after file:\n%s", out)
		}
		for _, h := range hunks {
			var adds, dels int
			for _, l := range h.Lines {
				switch l[0] {
				case '+':
					adds++
				case '-':
					dels++
				}
			}
			var gotAdds, gotDels int
			for _, o := range newOwner {
				if o == h.ID {
					gotAdds++
				}
			}
			for _, o := range oldRemover {
				if o == h.ID {
					gotDels++
				}
			}
			if gotAdds != adds || gotDels != dels {
				fail("hunk %s owns %d added and %d removed lines, its window has %d and %d", h.ID, gotAdds, gotDels, adds, dels)
			}
		}

		all := make([]Hunk, len(hunks))
		copy(all, hunks)
		for i := range all {
			all[i].Dropped = true
		}
		if got := string(applyHunks([]byte(oldText), all)); got != oldText {
			fail("all dropped != the before file:\n%s", got)
		}
		for i := range hunks {
			one := make([]Hunk, len(hunks))
			copy(one, hunks)
			one[i].Dropped = true
			want := hunkPosReassemble(t, oldText, hunks, func(k int) bool { return k != i })
			if got := string(applyHunks([]byte(oldText), one)); got != want {
				fail("dropping %s alone:\n--- got ---\n%s--- want ---\n%s", hunks[i].ID, got, want)
			}
		}
	}
}
