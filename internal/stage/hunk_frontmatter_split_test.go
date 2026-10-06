// hunk_frontmatter_split_test.go is 052 S1b's suite: ComputeHunks never cuts
// one window across a page's frontmatter fence when both sides changed, so a
// reviewer can keep the body edit and drop the frontmatter change (or the
// reverse). Frontmatter is structured metadata and the body is prose; 050's
// review (M1, then M-1 on the merged tree) found one merged window that made
// that impossible. The windows' own byte placement is 052 S1's job
// (hunk_position_test.go); this file pins where the windows are cut.
package stage

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

// preS1bComputeHunks is ComputeHunks exactly as 052 S1 shipped it: windows
// straight from hunkWindows, no frontmatter split. It is the reference every
// "unchanged" claim below is checked against — any file the split does not
// touch must produce these hunks, byte for byte.
func preS1bComputeHunks(old, new string) []Hunk {
	oldLines, _ := diffSplitLines(old)
	newLines, _ := diffSplitLines(new)
	ops := diffOps(oldLines, newLines)
	windows := hunkWindows(ops, diffContext)
	oldPos, _ := prefixCounts(ops)

	hunks := make([]Hunk, 0, len(windows))
	for i, w := range windows {
		var before, add, del, lines []string
		for k := w.lo; k <= w.hi; k++ {
			lines = append(lines, string(ops[k].kind)+ops[k].text)
			switch ops[k].kind {
			case ' ':
				before = append(before, ops[k].text)
			case '-':
				before = append(before, ops[k].text)
				del = append(del, ops[k].text)
			case '+':
				add = append(add, ops[k].text)
			}
		}
		hunks = append(hunks, Hunk{
			ID:     fmt.Sprintf("h%d", i+1),
			Before: before,
			Add:    add,
			Del:    del,
			At:     oldPos[w.lo] + 1,
			Lines:  lines,
		})
	}
	return hunks
}

// hunkSplitFence is the 1-based line number of old's closing "---" when old
// opens with a "---" line and has a later one, else 0 — written out here
// from vault.ParseFrontmatter's contract, independently of the code under
// test.
func hunkSplitFence(old string) int {
	lines := strings.Split(old, "\n")
	if len(lines) < 2 || lines[0] != "---" {
		return 0
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			return i + 1
		}
	}
	return 0
}

// hunkSplitSides counts h's changed lines on each side of the closing fence
// (the 1-based old line number fence): a "-" line by the old line it removes,
// a "+" line by the old line it sits in front of. A line in front of the
// fence line, or the fence line itself, is frontmatter; a line in front of
// the first line below it is body.
func hunkSplitSides(h Hunk, fence int) (fm, body int) {
	p := h.At
	for _, l := range h.Lines {
		switch l[0] {
		case ' ':
			p++
		case '-', '+':
			if p <= fence {
				fm++
			} else {
				body++
			}
			if l[0] == '-' {
				p++
			}
		}
	}
	return fm, body
}

// hunkSplitContext counts h's context lines on each side of the fence, the
// same way hunkSplitSides places a line.
func hunkSplitContext(h Hunk, fence int) (fm, body int) {
	p := h.At
	for _, l := range h.Lines {
		switch l[0] {
		case ' ':
			if p <= fence {
				fm++
			} else {
				body++
			}
			p++
		case '-':
			p++
		}
	}
	return fm, body
}

// TestHunkFrontmatterBodySplit: a sources: line change and a body edit used to
// share one window when they sat within 7 ops of each other, so Review could
// not drop one without the other (050's review M-1: the second subtest of
// TestPatchSyncHunksSurviveDropUndrop). They are now two hunks — the first
// entirely within the frontmatter (the fence at most), the second entirely in
// the body, neither window holding a line of the other — and each can be
// dropped and undropped alone, byte-exact.
func TestHunkFrontmatterBodySplit(t *testing.T) {
	const (
		oldSources = "sources: [raw/articles/kv-cache-explained.md]"
		newSources = "sources: [raw/articles/kv-cache-explained.md, raw/papers/leviathan-2023.md]"
		introLine  = "Intro paragraph linking to [[kv-cache]] and [[gpt-4]]."
		bodyLine   = "Inserted body line."
	)
	fmEdit := func(s string) string { return strings.Replace(s, oldSources, newSources, 1) }
	cases := []struct {
		name     string
		bodyEdit func(s string) string
	}{
		{"a body line 3 lines below the closing fence", func(s string) string {
			return strings.Replace(s, introLine+"\n", bodyLine+"\n"+introLine+"\n", 1)
		}},
		{"050 M1: a body line right under the blank line after the fence", func(s string) string {
			return strings.Replace(s, "---\n\n# Hunk Position Fixture\n", "---\n\n"+bodyLine+"\n# Hunk Position Fixture\n", 1)
		}},
		{"a body line immediately under the closing fence", func(s string) string {
			return strings.Replace(s, "---\n\n# Hunk Position Fixture\n", "---\n"+bodyLine+"\n\n# Hunk Position Fixture\n", 1)
		}},
		{"the heading rewritten, 2 lines under the fence", func(s string) string {
			return strings.Replace(s, "# Hunk Position Fixture\n", "# Hunk Position Fixture, renamed\n", 1)
		}},
	}
	fence := hunkSplitFence(hunkPosBefore)
	if fence != 9 {
		t.Fatalf("fixture closing fence at line %d, want 9", fence)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, _, page := hunkPosEngine(t)
			if _, err := e.OpenChangeset("frontmatter body split", testAuthor); err != nil {
				t.Fatalf("OpenChangeset: %v", err)
			}
			bodyOnly := tc.bodyEdit(hunkPosBefore)
			fmOnly := fmEdit(hunkPosBefore)
			after := tc.bodyEdit(fmOnly)
			if bodyOnly == hunkPosBefore || fmOnly == hunkPosBefore || after == bodyOnly || after == fmOnly {
				t.Fatal("a fixture edit did not apply")
			}
			// This is the shape that used to merge: the pre-S1b windowing
			// holds both changes in ONE window.
			if merged := preS1bComputeHunks(hunkPosBefore, after); len(merged) != 1 {
				t.Fatalf("the reference windowing gave %d hunks, the case is meant to merge into 1", len(merged))
			}

			id, hunks := stageHunkPosPatch(t, e, page, "## Alpha", after)
			if len(hunks) != 2 {
				t.Fatalf("ComputeHunks gave %d hunks, want 2 (frontmatter, body): %+v", len(hunks), hunks)
			}
			if hunks[0].ID != "h1" || hunks[1].ID != "h2" {
				t.Fatalf("ids = %q, %q, want h1, h2", hunks[0].ID, hunks[1].ID)
			}

			// h1 is frontmatter only, h2 body only: every line, context included.
			fm0, body0 := hunkSplitSides(hunks[0], fence)
			fm1, body1 := hunkSplitSides(hunks[1], fence)
			if fm0 == 0 || body0 != 0 || fm1 != 0 || body1 == 0 {
				t.Fatalf("changed lines by side: h1 frontmatter %d body %d, h2 frontmatter %d body %d", fm0, body0, fm1, body1)
			}
			if _, ctx := hunkSplitContext(hunks[0], fence); ctx != 0 {
				t.Fatalf("h1 holds %d body context lines, its window must stop at the closing fence: %q", ctx, hunks[0].Lines)
			}
			if ctx, _ := hunkSplitContext(hunks[1], fence); ctx != 0 {
				t.Fatalf("h2 holds %d frontmatter context lines, its window must start after the closing fence: %q", ctx, hunks[1].Lines)
			}
			if last := hunks[0].Lines[len(hunks[0].Lines)-1]; last != " ---" {
				t.Fatalf("h1's last line = %q, want the closing fence as trailing context", last)
			}
			if hunks[1].At <= fence {
				t.Fatalf("h2 starts at old line %d, inside or on the frontmatter (fence at %d)", hunks[1].At, fence)
			}
			// Add and Del hold the same lines as the window's changes: no body
			// line in h1, no sources: line in h2.
			for _, l := range append(append([]string(nil), hunks[0].Del...), hunks[0].Add...) {
				if !strings.HasPrefix(l, "sources:") {
					t.Fatalf("h1 carries a non-frontmatter line %q", l)
				}
			}
			for _, l := range append(append([]string(nil), hunks[1].Del...), hunks[1].Add...) {
				if strings.HasPrefix(l, "sources:") {
					t.Fatalf("h2 carries the frontmatter line %q", l)
				}
			}
			// The two windows reassemble both files exactly, sharing no line.
			if got := hunkPosReassemble(t, hunkPosBefore, hunks, func(int) bool { return true }); got != after {
				t.Fatalf("At+Lines do not reassemble the after file:\n%s", got)
			}
			if got := hunkPosReassemble(t, hunkPosBefore, hunks, func(int) bool { return false }); got != hunkPosBefore {
				t.Fatalf("At+Lines do not reassemble the before file:\n%s", got)
			}

			want := string(currentOpAfterBytes(t, e, id))
			if want != after {
				t.Fatalf("op.After != the proposal:\n%s", want)
			}

			// Review's y/n target: OpDiff shows the sources: change under h1
			// and the body change under h2, and a hunk's dropped flag is its own.
			checkDisplay := func(step string, dropped map[string]bool) {
				t.Helper()
				fds, err := e.OpDiff(id)
				if err != nil {
					t.Fatalf("%s: OpDiff: %v", step, err)
				}
				added := map[string][]string{}
				for _, fd := range fds {
					if fd.Path != hunkPosPath {
						continue
					}
					for _, dh := range fd.Hunks {
						if dh.HunkID == "" {
							t.Fatalf("%s: a display window has no hunk id: %+v", step, dh)
						}
						if dh.Dropped != dropped[dh.HunkID] {
							t.Fatalf("%s: %s displays dropped=%v, want %v", step, dh.HunkID, dh.Dropped, dropped[dh.HunkID])
						}
						for _, l := range dh.Lines {
							if l.Kind == '+' {
								added[dh.HunkID] = append(added[dh.HunkID], l.Text)
							}
						}
					}
				}
				for _, h := range hunks {
					if !reflect.DeepEqual(added[h.ID], h.Add) {
						t.Fatalf("%s: OpDiff shows %q added under %s, the hunk adds %q", step, added[h.ID], h.ID, h.Add)
					}
				}
			}
			checkDisplay("proposed", map[string]bool{})

			// Each hunk dropped alone gives the old side of THAT hunk only.
			alone := map[string]string{"h1": bodyOnly, "h2": fmOnly}
			for _, h := range hunks {
				if err := e.DropHunk(id, h.ID); err != nil {
					t.Fatalf("DropHunk %s: %v", h.ID, err)
				}
				checkDisplay("after n on "+h.ID, map[string]bool{h.ID: true})
				got := string(currentOpAfterBytes(t, e, id))
				if got != alone[h.ID] {
					t.Fatalf("dropping %s alone:\n--- got ---\n%s\n--- want ---\n%s", h.ID, got, alone[h.ID])
				}
				if !hunkPosOneNewline(got) {
					t.Fatalf("dropping %s alone lost the file's single trailing newline: ends %q", h.ID, tailOf(got))
				}
				// n then y is byte-identical.
				if err := e.UndropHunk(id, h.ID); err != nil {
					t.Fatalf("UndropHunk %s: %v", h.ID, err)
				}
				if got := string(currentOpAfterBytes(t, e, id)); got != want {
					t.Fatalf("drop+undrop of %s changed the op:\n--- got ---\n%s\n--- want ---\n%s", h.ID, got, want)
				}
			}

			// Both dropped is the before file; y in either order restores it all.
			for _, h := range hunks {
				if err := e.DropHunk(id, h.ID); err != nil {
					t.Fatalf("DropHunk %s: %v", h.ID, err)
				}
			}
			if got := string(currentOpAfterBytes(t, e, id)); got != hunkPosBefore {
				t.Fatalf("dropping both != the before file:\n%s", got)
			}
			for _, h := range []string{"h2", "h1"} {
				if err := e.UndropHunk(id, h); err != nil {
					t.Fatalf("UndropHunk %s: %v", h, err)
				}
			}
			if got := string(currentOpAfterBytes(t, e, id)); got != want {
				t.Fatalf("undropping both != the proposal:\n%s", got)
			}
		})
	}
}

// TestHunkFrontmatterOneSidedUnchanged pins the other half of the split: an
// edit confined to one side of the fence — frontmatter only or body only —
// yields exactly the hunks 052 S1 produced, context spilling across the fence
// included. The goldens are written out, and the reference windowing is
// compared as well, so neither can drift on its own.
func TestHunkFrontmatterOneSidedUnchanged(t *testing.T) {
	const (
		oldSources = "sources: [raw/articles/kv-cache-explained.md]"
		newSources = "sources: [raw/articles/kv-cache-explained.md, raw/papers/leviathan-2023.md]"
	)
	cases := []struct {
		name      string
		after     string
		wantAt    int
		wantLines []string
	}{
		{
			// The window's trailing context runs 2 lines past the fence.
			name:   "frontmatter-only edit keeps its context below the fence",
			after:  strings.Replace(hunkPosBefore, oldSources, newSources, 1),
			wantAt: 4,
			wantLines: []string{
				" updated: 2026-08-29", " type: concept", " tags: [inference, memory]",
				"-" + oldSources, "+" + newSources,
				" confidence: high", " ---", " ",
			},
		},
		{
			// The window's leading context starts 2 lines above the fence.
			name:   "body-only edit keeps its context above the fence",
			after:  strings.Replace(hunkPosBefore, "# Hunk Position Fixture\n", "# Hunk Position Fixture, renamed\n", 1),
			wantAt: 8,
			wantLines: []string{
				" confidence: high", " ---", " ",
				"-# Hunk Position Fixture", "+# Hunk Position Fixture, renamed",
				" ", " " + "Intro paragraph linking to [[kv-cache]] and [[gpt-4]].", " ",
			},
		},
		{
			name:   "body-only edit far below the fence",
			after:  strings.Replace(hunkPosBefore, "Gamma line three.", "Gamma line three, revised.", 1),
			wantAt: 0, // not pinned by value; the reference comparison covers it
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeHunks(hunkPosBefore, tc.after)
			if want := preS1bComputeHunks(hunkPosBefore, tc.after); !reflect.DeepEqual(got, want) {
				t.Fatalf("hunks differ from the pre-S1b windows:\n--- got ---\n%+v\n--- want ---\n%+v", got, want)
			}
			if len(got) != 1 || got[0].ID != "h1" {
				t.Fatalf("got %d hunks, want exactly h1: %+v", len(got), got)
			}
			if tc.wantAt != 0 {
				if got[0].At != tc.wantAt || !reflect.DeepEqual(got[0].Lines, tc.wantLines) {
					t.Fatalf("hunk h1: At = %d, Lines = %q\nwant At = %d, Lines = %q", got[0].At, got[0].Lines, tc.wantAt, tc.wantLines)
				}
			}
		})
	}
}

// TestHunkFrontmatterSplitShapes walks the edges of the fence rule on small
// files. Each case is checked three ways: the hunk count and ids, the
// no-shared-line rule (hunkPosReassemble fails on overlapping windows), and
// both files reassembling exactly from At+Lines. A case that must not split
// is compared to the pre-S1b windowing.
func TestHunkFrontmatterSplitShapes(t *testing.T) {
	join := func(lines ...string) string { return strings.Join(lines, "\n") + "\n" }
	page := join("---", "title: T", "tags: [a]", "sources: [s1]", "---", "", "# T", "", "one", "two", "three", "four", "five", "six")
	cases := []struct {
		name      string
		old, new  string
		wantCount int
		unchanged bool // must equal the pre-S1b windows
	}{
		{"a line added just before the closing fence, a body edit below",
			page, join("---", "title: T", "tags: [a]", "sources: [s1]", "extra: x", "---", "", "# T", "", "ONE", "two", "three", "four", "five", "six"), 2, false},
		{"a line added just after the closing fence",
			page, join("---", "title: T", "tags: [a]", "sources: [s1, s2]", "---", "added", "", "# T", "", "one", "two", "three", "four", "five", "six"), 2, false},
		{"two frontmatter changes and two body changes",
			page, join("---", "title: T2", "tags: [a]", "sources: [s1, s2]", "---", "", "# T", "", "ONE", "two", "THREE", "four", "five", "six"), 2, false},
		// The fence line is the last line of the frontmatter, so a change to it
		// is a frontmatter change and what replaces it, one op later, is not.
		{"the closing fence line itself rewritten, a body edit near it",
			page, join("---", "title: T", "tags: [a]", "sources: [s1]", "--- ", "", "# T", "", "ONE", "two", "three", "four", "five", "six"), 2, false},
		{"the closing fence removed, a body edit",
			page, join("---", "title: T", "tags: [a]", "sources: [s1]", "", "# T", "", "ONE", "two", "three", "four", "five", "six"), 2, false},
		{"the frontmatter dropped altogether, a body edit",
			page, join("# T", "", "ONE", "two", "three", "four", "five", "six"), 2, false},
		{"far apart frontmatter and body changes are two windows already",
			page, join("---", "title: T", "tags: [a]", "sources: [s1, s2]", "---", "", "# T", "", "one", "two", "three", "four", "five", "SIX"), 2, true},
		{"no frontmatter: --- lines in the body are thematic breaks",
			join("intro", "---", "one", "two", "---", "three", "four"),
			join("INTRO", "---", "one", "TWO", "---", "three", "four"), 1, true},
		{"an opening --- with no closing one",
			join("---", "title: T", "sources: [s1]", "", "# T", "", "body", "text"),
			join("---", "title: T", "sources: [s1, s2]", "", "# T", "", "BODY", "text"), 1, true},
		{"a file that is only frontmatter",
			join("---", "a: 1", "---"), join("---", "a: 2", "---"), 1, true},
		{"frontmatter changed and a body appended to a frontmatter-only file",
			join("---", "a: 1", "---"), join("---", "a: 2", "---", "new body"), 2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeHunks(tc.old, tc.new)
			if len(got) != tc.wantCount {
				t.Fatalf("got %d hunks, want %d: %+v", len(got), tc.wantCount, got)
			}
			for i, h := range got {
				if want := fmt.Sprintf("h%d", i+1); h.ID != want {
					t.Fatalf("hunk %d id = %q, want %q", i, h.ID, want)
				}
			}
			if tc.unchanged {
				if want := preS1bComputeHunks(tc.old, tc.new); !reflect.DeepEqual(got, want) {
					t.Fatalf("hunks differ from the pre-S1b windows:\n--- got ---\n%+v\n--- want ---\n%+v", got, want)
				}
			}
			if out := hunkPosReassemble(t, tc.old, got, func(int) bool { return true }); out != tc.new {
				t.Fatalf("At+Lines do not reassemble the after file:\n--- got ---\n%s--- want ---\n%s", out, tc.new)
			}
			if out := hunkPosReassemble(t, tc.old, got, func(int) bool { return false }); out != tc.old {
				t.Fatalf("At+Lines do not reassemble the before file:\n--- got ---\n%s--- want ---\n%s", out, tc.old)
			}
			// Every window the split made applies byte-exactly on its own.
			for i := range got {
				one := make([]Hunk, len(got))
				copy(one, got)
				one[i].Dropped = true
				want := hunkPosReassemble(t, tc.old, got, func(k int) bool { return k != i })
				if applied := string(applyHunks([]byte(tc.old), one)); applied != want {
					t.Fatalf("dropping %s alone:\n--- got ---\n%s--- want ---\n%s", got[i].ID, applied, want)
				}
			}
			if fence := hunkSplitFence(tc.old); fence > 0 {
				for _, h := range got {
					if fm, body := hunkSplitSides(h, fence); fm > 0 && body > 0 {
						t.Fatalf("hunk %s changes %d frontmatter and %d body lines in one window: %q", h.ID, fm, body, h.Lines)
					}
				}
			}
		})
	}
}

// TestHunkFrontmatterSplitRandomEdits is the property behind the shape tests:
// on pages that open with a "---" fence (closing one somewhere in the first
// lines, or none), for any random edit script, ComputeHunks' windows (1)
// never hold changes on both sides of the fence, (2) reassemble the after
// and the before files, (3) share no line, and (4) apply so that dropping any
// one of them alone gives the other hunks' bytes. A file the split does not
// touch — no closing fence, or every window one-sided — gets exactly the
// pre-S1b hunks. The seed is fixed so a failure reproduces.
func TestHunkFrontmatterSplitRandomEdits(t *testing.T) {
	rng := rand.New(rand.NewSource(521))
	alphabet := []string{"", "a", "b", "c", "x", "---"}
	randomLines := func(n int) []string {
		l := make([]string, n)
		for i := range l {
			l[i] = alphabet[rng.Intn(len(alphabet))]
		}
		return l
	}
	split := 0
	for trial := 0; trial < 600; trial++ {
		old := append([]string{"---"}, randomLines(2+rng.Intn(30))...)
		if rng.Intn(3) > 0 { // usually a real frontmatter: a closing fence early on
			old[1+rng.Intn(min(6, len(old)-1))] = "---"
		}
		cur := append([]string(nil), old...)
		for edits := 1 + rng.Intn(7); edits > 0; edits-- {
			i := rng.Intn(len(cur) + 1)
			switch kind := rng.Intn(3); {
			case kind == 0:
				cur = append(cur[:i], append(randomLines(1+rng.Intn(3)), cur[i:]...)...)
			case kind == 1 && i < len(cur) && len(cur) > 1:
				cur = append(cur[:i], cur[i+1:]...)
			case i < len(cur):
				cur[i] = alphabet[rng.Intn(len(alphabet))]
			}
		}
		oldText, newText := hunkPosLines(old...), hunkPosLines(cur...)
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("trial %d: "+format+"\n--- old ---\n%s--- new ---\n%s", append(append([]any{trial}, args...), oldText, newText)...)
		}

		hunks := ComputeHunks(oldText, newText)
		ref := preS1bComputeHunks(oldText, newText)
		for i, h := range hunks {
			if want := fmt.Sprintf("h%d", i+1); h.ID != want {
				fail("hunk %d id = %q, want %q", i, h.ID, want)
			}
		}
		if len(hunks) > 2*len(ref) {
			fail("%d hunks from %d windows: more than one split per window", len(hunks), len(ref))
		}
		if len(hunks) != len(ref) {
			split++
		} else if !reflect.DeepEqual(hunks, ref) {
			fail("same window count as the pre-S1b windowing, but the hunks differ:\n%+v\nvs\n%+v", hunks, ref)
		}
		if fence := hunkSplitFence(oldText); fence > 0 {
			for _, h := range hunks {
				if fm, body := hunkSplitSides(h, fence); fm > 0 && body > 0 {
					fail("hunk %s changes %d frontmatter and %d body lines in one window: %q", h.ID, fm, body, h.Lines)
				}
			}
		} else if !reflect.DeepEqual(hunks, ref) {
			fail("no frontmatter, yet the hunks differ from the pre-S1b windows")
		}
		if got := hunkPosReassemble(t, oldText, hunks, func(int) bool { return true }); got != newText {
			fail("At+Lines reassemble the after file wrongly:\n%s", got)
		}
		if got := hunkPosReassemble(t, oldText, hunks, func(int) bool { return false }); got != oldText {
			fail("At+Lines reassemble the before file wrongly:\n%s", got)
		}
		if got := string(applyHunks([]byte(oldText), hunks)); got != newText {
			fail("all live != the after file:\n%s", got)
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
	if split == 0 {
		t.Fatal("no trial exercised a split: the generator never put changes on both sides of a fence")
	}
}
