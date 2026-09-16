package stage

import (
	"bytes"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

// attributionTraceSubtests is TestOpDiffAttribution's traced-reconstruction
// half, moved here whole (a pure move, file-size split): single-owner equality
// with Diff, the trace/apply byte-identity, and the pre-T15 byte pin.
func attributionTraceSubtests(t *testing.T) {
	// single_owner_equals_diff pins the amended note 6: for an op with no
	// dropped hunks whose windows each have a single owner, the
	// concatenated windows equal Diff.UnifiedFile exactly — headers, line
	// kinds and texts.
	t.Run("single_owner_equals_diff", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("single owner windows", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
		if !ok {
			t.Fatal("fixture missing wiki/concepts/kv-cache.md")
		}
		// Two del-anchored edits far apart, so hunkWindows keeps them in
		// separate windows and no window ever spans two owners.
		old1 := "Without caching, generating token n would repeat O(n) work already done for"
		new1 := "Without caching, regenerating token n repeats O(n) work already completed for"
		old2 := "- [[speculative-decoding]] — both the draft and target model read the cache"
		new2 := "- [[speculative-decoding]] — both the draft and target models consult the cache"
		body := replaceLine(replaceLine(page.Body, old1, new1), old2, new2)
		rewritten := *page
		rewritten.Body = body
		hunks := []Hunk{
			{ID: "h1", Path: page.Path, Del: []string{old1}, Add: []string{new1}},
			{ID: "h2", Path: page.Path, Del: []string{old2}, Add: []string{new2}},
		}
		id, err := e.Append(Op{
			Kind:    OpPatchPage,
			Path:    page.Path,
			Section: "## Why it matters",
			Before:  page.SHA256(),
			Content: rewritten.Serialize(),
			Hunks:   hunks,
		})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}

		fods, err := e.OpDiff(id)
		if err != nil {
			t.Fatalf("OpDiff: %v", err)
		}
		fo := opDiffFileByPath(t, fods, page.Path)
		if len(fo.Hunks) != 2 {
			t.Fatalf("windows = %d, want 2 (the edits are far apart, so each window has one owner): %+v", len(fo.Hunks), fo.Hunks)
		}
		for _, w := range fo.Hunks {
			if w.HunkID != "h1" && w.HunkID != "h2" {
				t.Fatalf("window attributed to %q, want h1 or h2", w.HunkID)
			}
		}

		d, err := e.Diff()
		if err != nil {
			t.Fatalf("Diff: %v", err)
		}
		unified := d.UnifiedFile(page.Path)
		hdr := strings.Index(unified, "@@")
		if hdr < 0 {
			t.Fatalf("UnifiedFile(%s) has no hunk header:\n%s", page.Path, unified)
		}
		wantBody := unified[hdr:]

		var got strings.Builder
		for _, h := range fo.Hunks {
			got.WriteString(h.Header)
			got.WriteByte('\n')
			for _, l := range h.Lines {
				got.WriteByte(l.Kind)
				got.WriteString(l.Text)
				got.WriteByte('\n')
			}
		}
		if got.String() != wantBody {
			t.Fatalf("OpDiff windows != Diff.UnifiedFile body:\n--- got ---\n%s--- want ---\n%s", got.String(), wantBody)
		}
	})

	// traced_apply_equals_apply pins the wrapper relationship: over every
	// hunk-bearing op this suite can build — ComputeHunks-cut hunks for
	// every fixture page, the C-131 insert-only shape, a mixed-anchor op,
	// and the generated shape — applyHunksTraced's bytes are exactly
	// applyHunks', and both ownership slices are index-aligned with the
	// bytes they describe.
	t.Run("traced_apply_equals_apply", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("traced equals apply", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}

		type applyCase struct {
			name   string
			before []byte
			hunks  []Hunk
		}
		var cases []applyCase

		// Every fixture page: hunks cut by ComputeHunks from two-line
		// edits — the shape a proposer's own diff produces (§5.6).
		for _, p := range e.Vault().Pages() {
			before := p.Serialize()
			lines := strings.Split(string(before), "\n")
			if len(lines) < 12 {
				continue
			}
			after := append([]string(nil), lines...)
			after[10] += " (edited by traced_apply_equals_apply)"
			after[len(after)-3] += " (edited by traced_apply_equals_apply)"
			hunks := ComputeHunks(string(before), strings.Join(after, "\n"))
			if len(hunks) == 0 {
				continue
			}
			cases = append(cases, applyCase{name: "computehunks " + p.Path, before: before, hunks: hunks})
		}

		page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
		if !ok {
			t.Fatal("fixture missing wiki/concepts/kv-cache.md")
		}
		// The C-131 shape: an insert-only hunk with a Section.
		cases = append(cases, applyCase{
			name:   "insert-only with section",
			before: page.Serialize(),
			hunks: []Hunk{
				{ID: "h1", Path: page.Path, Section: "## Example", Add: []string{"Inserted at the end of the section.", ""}},
			},
		})
		// Del-anchored, section-anchored, unanchored end-of-body and a
		// second del anchor, in one op.
		cases = append(cases, applyCase{
			name:   "mixed anchors",
			before: page.Serialize(),
			hunks: []Hunk{
				{ID: "h1", Path: page.Path, Del: []string{oldKVCacheLine}, Add: []string{newKVCacheLine}},
				{ID: "h2", Path: page.Path, Section: "## Related", Add: []string{"A related note.", ""}},
				{ID: "h3", Path: page.Path, Add: []string{"An unanchored tail line."}},
				{ID: "h4", Path: page.Path, Del: []string{oldKVCacheBullet}, Add: []string{newKVCacheBullet}},
			},
		})
		// The generated shape, same generator as every_hunk_covered.
		cases = append(cases, applyCase{
			name:   "generated",
			before: generatedHunksPage(),
			hunks:  generateHunks(rand.New(rand.NewSource(1503)), "wiki/concepts/generated-hunks-fixture.md"),
		})

		if len(cases) < 4 {
			t.Fatalf("built %d apply cases, want at least 4", len(cases))
		}
		for _, tc := range cases {
			want := applyHunks(tc.before, tc.hunks)
			got, newOwner, oldRemover := applyHunksTraced(tc.before, tc.hunks)
			if !bytes.Equal(got, want) {
				t.Fatalf("%s: applyHunksTraced bytes != applyHunks bytes", tc.name)
			}
			if len(newOwner) != strings.Count(string(got), "\n")+1 {
				t.Fatalf("%s: newOwner has %d entries for %d output lines", tc.name, len(newOwner), strings.Count(string(got), "\n")+1)
			}
			if len(oldRemover) != strings.Count(string(tc.before), "\n")+1 {
				t.Fatalf("%s: oldRemover has %d entries for %d before lines", tc.name, len(oldRemover), strings.Count(string(tc.before), "\n")+1)
			}
		}
	})

	// apply_matches_pre_t15_bytes pins that the traced rewrite of
	// applyHunks' loop is byte-identical to the loop it replaced, for every
	// shape that does not take the C-131 section branch: Del-anchored
	// replaces, Del-only removals, add-without-Section appended at the end
	// of the body, mismatched Del/Add lengths, Del text shared between two
	// hunks, a Del anchor inside another hunk's produced lines, and several
	// hunks in one op. legacyApplyHunks001 below is the pre-T15 loop,
	// copied verbatim from op.go at branch HEAD (932220d) — the bytes
	// DropHunk/UndropHunk/Commit computed before the ownership trace
	// existed must be the bytes they compute now.
	t.Run("apply_matches_pre_t15_bytes", func(t *testing.T) {
		before := "one\none\nanchor A\nanchor B\nanchor C\ntail\n"
		cases := [][]Hunk{
			{{ID: "h1", Del: []string{"anchor B"}, Add: []string{"ANCHOR B"}}},
			{{ID: "h1", Del: []string{"anchor A", "anchor B"}, Add: []string{"ANCHOR A", "ANCHOR B"}}},
			{
				{ID: "h1", Del: []string{"anchor A", "anchor B", "anchor C"}, Add: []string{"ANCHOR A"}},
				{ID: "h2", Del: []string{"tail"}},
			},
			{
				// Add longer than Del: the extras insert after the last
				// matched position.
				{ID: "h1", Del: []string{"anchor B"}, Add: []string{"ANCHOR B", "inserted 1", "inserted 2"}},
			},
			{
				// Nothing matches: every Add appends at the end of the
				// body, in order.
				{ID: "h1", Add: []string{"unanchored 1", "unanchored 2"}},
			},
			{
				// Del text shared by two hunks: each takes the first
				// remaining match, in op order.
				{ID: "h1", Del: []string{"one"}},
				{ID: "h2", Del: []string{"one"}, Add: []string{"replaced one"}},
			},
			{
				// A Del anchor inside lines an earlier hunk produced.
				{ID: "h1", Del: []string{"anchor A"}, Add: []string{"anchor A", "h1 product"}},
				{ID: "h2", Del: []string{"h1 product"}, Add: []string{"h2 replacement"}},
			},
			{
				// Several hunks in one op, mixed shapes, one dropped (the
				// dropped one contributes nothing in either loop).
				{ID: "h1", Del: []string{"anchor C"}, Add: []string{"ANCHOR C", "h1 tail"}},
				{ID: "h2", Del: []string{"anchor A"}, Add: []string{"ANCHOR A"}},
				{ID: "h3", Del: []string{"anchor B"}, Add: []string{"never applied"}, Dropped: true},
				{ID: "h4", Add: []string{"h4 tail"}},
			},
		}
		for i, hunks := range cases {
			want := legacyApplyHunks001([]byte(before), hunks)
			got := applyHunks([]byte(before), hunks)
			if !bytes.Equal(got, want) {
				t.Fatalf("case %d: applyHunks bytes diverged from the pre-T15 loop:\n--- got ---\n%s\n--- want ---\n%s", i, got, want)
			}
		}
	})

}

// legacyApplyHunks001 is op.go's applyHunks exactly as it stood at branch
// HEAD 932220d, before the ownership trace: the reference the
// apply_matches_pre_t15_bytes case pins current bytes against.
func legacyApplyHunks001(before []byte, hunks []Hunk) []byte {
	lines := strings.Split(string(before), "\n")
	for _, h := range hunks {
		if h.Dropped {
			continue
		}
		n := len(h.Del)
		if len(h.Add) > n {
			n = len(h.Add)
		}
		pos := len(lines)
		for i := 0; i < n; i++ {
			switch {
			case i < len(h.Del) && i < len(h.Add):
				if idx := indexOfLine(lines, h.Del[i]); idx >= 0 {
					lines[idx] = h.Add[i]
					pos = idx + 1
				}
			case i < len(h.Del):
				if idx := indexOfLine(lines, h.Del[i]); idx >= 0 {
					lines = append(lines[:idx], lines[idx+1:]...)
					pos = idx
				}
			default:
				ins := h.Add[i]
				tail := append([]string{ins}, lines[pos:]...)
				lines = append(lines[:pos], tail...)
				pos++
			}
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

// generatedHunksPage is the page every_hunk_covered builds its hunks
// against: three sections of four unique content lines, so every hunk
// replaces a line only one hunk can match. Same frontmatter shape as
// nestedHeadingFixtureBefore — valid against the minimal fixture's schema.
func generatedHunksPage() []byte {
	var b strings.Builder
	b.WriteString("---\ntitle: Generated Hunks Fixture\ncreated: 2026-08-29\nupdated: 2026-08-29\ntype: concept\ntags: [inference]\nconfidence: medium\n---\n\n")
	b.WriteString("# Generated Hunks Fixture\n\nIntro linking [[kv-cache]] and [[gpt-4]].\n")
	for s := 1; s <= 3; s++ {
		fmt.Fprintf(&b, "\n## Section %d\n\n", s)
		for l := 1; l <= 4; l++ {
			fmt.Fprintf(&b, "Section %d line %d carries unique content %d-%d.\n", s, l, s, l)
		}
	}
	return []byte(b.String())
}

// duplicateLinePage is the page unowned_lines_stay_out_of_other_hunks_windows
// builds its hunks against: one section carries the same line twice, so a
// hunk deleting one copy exercises the duplicate-text corner of the trace.
// Same frontmatter shape as generatedHunksPage — valid against the minimal
// fixture's schema.
func duplicateLinePage() []byte {
	var b strings.Builder
	b.WriteString("---\ntitle: Duplicate Line Fixture\ncreated: 2026-08-29\nupdated: 2026-08-29\ntype: concept\ntags: [inference]\nconfidence: medium\n---\n\n")
	b.WriteString("# Duplicate Line Fixture\n\nIntro linking [[kv-cache]] and [[gpt-4]].\n")
	b.WriteString("\n## Section 1\n\nDuplicated line.\nDuplicated line.\nSection 1 tail line.\n")
	b.WriteString("\n## Section 2\n\nSection 2 body line.\n")
	return []byte(b.String())
}

// generateHunks returns 1-4 replace-style hunks over generatedHunksPage,
// drawn from a fixed-seed rand.Rand so the case is deterministic: each
// hunk replaces one of the twelve unique content lines with a hunk-specific
// text. Del lines are unique across hunks and Add texts are hunk-specific,
// so no hunk can match another's lines and every owned line pins its own
// hunk.
func generateHunks(rng *rand.Rand, path string) []Hunk {
	n := 1 + rng.Intn(4)
	used := map[[2]int]bool{}
	hunks := make([]Hunk, 0, n)
	for i := 1; len(hunks) < n; {
		pos := [2]int{rng.Intn(3), rng.Intn(4)}
		if used[pos] {
			continue
		}
		used[pos] = true
		hunks = append(hunks, Hunk{
			ID:   fmt.Sprintf("h%d", i),
			Path: path,
			Del:  []string{fmt.Sprintf("Section %d line %d carries unique content %d-%d.", pos[0]+1, pos[1]+1, pos[0]+1, pos[1]+1)},
			Add:  []string{fmt.Sprintf("h%d replaced section %d line %d.", i, pos[0]+1, pos[1]+1)},
		})
		i++
	}
	return hunks
}

// assertPlusTexts fails t unless win's '+' line texts are exactly want, in
// order.
func assertPlusTexts(t *testing.T, win DisplayHunk, want ...string) {
	t.Helper()
	var got []string
	for _, l := range win.Lines {
		if l.Kind == '+' {
			got = append(got, l.Text)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("window %s '+' texts = %q, want %q", win.HunkID, got, want)
	}
}

// parseHeader splits "@@ -o,oc +n,nc @@" into its four numbers.
func parseHeader(t *testing.T, header string) (oldStart, oldCount, newStart, newCount int) {
	t.Helper()
	if _, err := fmt.Sscanf(header, "@@ -%d,%d +%d,%d @@", &oldStart, &oldCount, &newStart, &newCount); err != nil {
		t.Fatalf("parse header %q: %v", header, err)
	}
	return oldStart, oldCount, newStart, newCount
}

// kv-cache lines the attribution cases edit, hoisted so every case in this
// file quotes the same fixture texts.
const (
	oldKVCacheLine   = "Without caching, generating token n would repeat O(n) work already done for"
	newKVCacheLine   = "Without caching, regenerating token n repeats O(n) work already completed for"
	oldKVCacheBullet = "- [[speculative-decoding]] — both the draft and target model read the cache"
	newKVCacheBullet = "- [[speculative-decoding]] — both the draft and target models consult the cache"
)
