package stage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// attributionOwnerSubtests is TestOpDiffAttribution's ownership-boundary
// half, moved here whole (a pure move, file-size split): stale windows carry
// no hunk id, two-owner merged windows share context, and unowned lines stay
// out of other hunks' windows.
func attributionOwnerSubtests(t *testing.T) {
	// stale_op_windows_carry_no_hunk_id pins the root rule that closes the
	// stale fallback hole: a stale patch_page's Old is the working tree,
	// not the Before its hunks were cut against, so the trace's line
	// indices are fiction there and NO id may be attached to a window —
	// not even by text matching. DropHunk/UndropHunk have no stale guard,
	// so a guessed id on a stale window would let y/n drop a hunk the
	// reviewer never saw, and the flag would persist until Commit wrote
	// the wrong projection. The stale op's content is still displayed; it
	// just carries no y/n target. The two hunks here share their Add text,
	// the exact case text attribution cannot tell apart.
	t.Run("stale_op_windows_carry_no_hunk_id", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("stale attribution", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
		if !ok {
			t.Fatal("fixture missing wiki/concepts/kv-cache.md")
		}
		hunks := []Hunk{
			{ID: "h1", Path: page.Path, Section: "## Why it matters", Add: []string{"Shared added line.", ""}},
			{ID: "h2", Path: page.Path, Section: "## Related", Add: []string{"Shared added line.", ""}},
		}
		id, err := e.Append(Op{
			Kind:    OpPatchPage,
			Path:    page.Path,
			Section: "## Why it matters",
			Before:  page.SHA256(),
			Content: applyHunks([]byte(page.Serialize()), hunks),
			Hunks:   hunks,
		})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}

		// The working tree changes under the op: edit the file on disk
		// directly and reload, exactly as TestRefreshFlipsStaleWithout-
		// TouchingHunks simulates a concurrent Obsidian edit.
		onDisk := filepath.Join(e.root, filepath.FromSlash(page.Path))
		newBytes := append([]byte{}, page.Serialize()...)
		newBytes = append(newBytes, []byte("\nEdited directly on disk.\n")...)
		if err := os.WriteFile(onDisk, newBytes, 0o644); err != nil {
			t.Fatalf("write on-disk change: %v", err)
		}
		if err := e.Vault().Reload(); err != nil {
			t.Fatalf("Vault.Reload: %v", err)
		}
		if err := e.Refresh(); err != nil {
			t.Fatalf("Refresh: %v", err)
		}

		fods, err := e.OpDiff(id)
		if err != nil {
			t.Fatalf("OpDiff: %v", err)
		}
		fo := opDiffFileByPath(t, fods, page.Path)
		if !fo.Stale {
			t.Fatal("op must be reported Stale after the working tree changed under it")
		}
		if len(fo.Hunks) == 0 {
			t.Fatal("a stale op's proposed change must still be displayed")
		}
		for _, w := range fo.Hunks {
			if w.HunkID != "" {
				t.Fatalf("stale window carries guessed HunkID %q: y/n could act on a hunk that does not own the lines shown", w.HunkID)
			}
			if w.Dropped {
				t.Fatal("an ownerless window must not claim a Dropped flag")
			}
		}
	})

	// merged_window_two_owners_share_context pins the per-run header
	// arithmetic of note 4's splitting rule: one hunkWindows window whose
	// first half is h1's replace, then ONE shared context line, then h2's
	// replace. The context travels with the following run, so h1's window
	// has no trailing context and h2's begins with the shared line — and
	// each window's header must be a valid unified hunk over exactly its
	// own lines, with the second window continuing where the first ended.
	// The concatenated kinds and texts must still equal Diff's one merged
	// window (note 6).
	t.Run("merged_window_two_owners_share_context", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("shared context", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
		if !ok {
			t.Fatal("fixture missing wiki/concepts/kv-cache.md")
		}
		old1 := oldKVCacheLine
		new1 := newKVCacheLine
		mid := "earlier tokens. Caching turns that into a constant amount of new work per"
		old2 := "step, at the cost of memory that grows with sequence length."
		new2 := "step, at the cost of memory that grows with the sequence length."
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

		// Precondition: Diff really does render the two edits as ONE
		// merged window, so the per-owner split below is a split of one
		// unified hunk.
		d, err := e.Diff()
		if err != nil {
			t.Fatalf("Diff: %v", err)
		}
		unified := d.UnifiedFile(page.Path)
		if n := strings.Count(unified, "@@ "); n != 1 {
			t.Fatalf("Diff.UnifiedFile has %d windows, want 1 merged window for this precondition", n)
		}

		fods, err := e.OpDiff(id)
		if err != nil {
			t.Fatalf("OpDiff: %v", err)
		}
		fo := opDiffFileByPath(t, fods, page.Path)
		if len(fo.Hunks) != 2 {
			t.Fatalf("windows = %d, want 2 (one per owner run of the merged window): %+v", len(fo.Hunks), fo.Hunks)
		}
		if fo.Hunks[0].HunkID != "h1" || fo.Hunks[1].HunkID != "h2" {
			t.Fatalf("HunkIDs = [%q, %q], want [h1 h2]", fo.Hunks[0].HunkID, fo.Hunks[1].HunkID)
		}
		w1, w2 := fo.Hunks[0], fo.Hunks[1]
		// h1's window ends with its own replace and never shows the shared
		// context line — that line borders h2's run.
		if n := len(w1.Lines); n < 2 || w1.Lines[n-2].Kind != '-' || w1.Lines[n-2].Text != old1 || w1.Lines[n-1].Kind != '+' || w1.Lines[n-1].Text != new1 {
			t.Fatalf("h1's window must end with its own -/+ pair, got %q", w1.Lines)
		}
		for _, l := range w1.Lines {
			if l.Text == mid {
				t.Fatalf("h1's window must not carry the context line that borders h2's run: %q", w1.Lines)
			}
		}
		// h2's window opens with the shared context line.
		if w2.Lines[0].Kind != ' ' || w2.Lines[0].Text != mid {
			t.Fatalf("h2's window must open with the shared context line, got %q", w2.Lines[0])
		}
		// Each header is a valid unified hunk over exactly its own lines,
		// and the second window continues where the first ended.
		o1, oc1, n1, nc1 := parseHeader(t, w1.Header)
		o2, oc2, n2, nc2 := parseHeader(t, w2.Header)
		count := func(lines []DisplayLine, kinds ...byte) int {
			n := 0
			for _, l := range lines {
				for _, k := range kinds {
					if l.Kind == k {
						n++
					}
				}
			}
			return n
		}
		if oc1 != count(w1.Lines, ' ', '-') || nc1 != count(w1.Lines, ' ', '+') {
			t.Fatalf("h1's header %q does not match its lines %q", w1.Header, w1.Lines)
		}
		if oc2 != count(w2.Lines, ' ', '-') || nc2 != count(w2.Lines, ' ', '+') {
			t.Fatalf("h2's header %q does not match its lines %q", w2.Header, w2.Lines)
		}
		if o2 != o1+oc1 || n2 != n1+nc1 {
			t.Fatalf("h2's window must continue where h1's ended: headers %q then %q", w1.Header, w2.Header)
		}
		// Note 6: the per-owner runs concatenate to Diff's one window.
		var got strings.Builder
		for _, w := range fo.Hunks {
			for _, l := range w.Lines {
				got.WriteByte(l.Kind)
				got.WriteString(l.Text)
				got.WriteByte('\n')
			}
		}
		wantBody := unified[strings.Index(unified, "@@"):]
		wantLines := strings.SplitN(wantBody, "\n", 2)[1]
		if got.String() != wantLines {
			t.Fatalf("concatenated per-owner lines != Diff's merged window:\n--- got ---\n%s--- want ---\n%s", got.String(), wantLines)
		}
	})

	// unowned_lines_stay_out_of_other_hunks_windows pins the trace's
	// duplicate-line corner: h1 deletes one of two byte-identical lines
	// and h2 rewrites a line a few ops later, so both changes land in ONE
	// hunkWindows window. applyHunks removes the FIRST duplicate while the
	// LCS aligns the KEPT duplicate with the first old position — so the
	// '-' op for the deleted line points at a before-index no hunk removed
	// and its owner is provably unnameable. That line must surface in an
	// OWNERLESS window, never folded into h2's run: h2's HunkID must own
	// exactly the lines it shows, because y/n acts on it.
	t.Run("unowned_lines_stay_out_of_other_hunks_windows", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		fixtureRel := filepath.Join("wiki", "concepts", "duplicate-line-fixture.md")
		if err := os.WriteFile(filepath.Join(dir, fixtureRel), []byte(duplicateLinePage()), 0o644); err != nil {
			t.Fatalf("write duplicate-line-fixture.md: %v", err)
		}
		e, err := OpenEngine(dir)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		t.Cleanup(func() { e.Close() })
		e.now = testutil.FixedClock()

		if _, err := e.OpenChangeset("unowned lines", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		path := filepath.ToSlash(fixtureRel)
		page, ok := e.Vault().Page(path)
		if !ok {
			t.Fatalf("fixture page %s not loaded", path)
		}
		hunks := []Hunk{
			{ID: "h1", Path: path, Del: []string{"Duplicated line."}},
			{ID: "h2", Path: path, Del: []string{"Section 2 body line."}, Add: []string{"Section 2 body line, rewritten."}},
		}
		id, err := e.Append(Op{
			Kind:    OpPatchPage,
			Path:    path,
			Section: "## Section 1",
			Before:  page.SHA256(),
			Content: applyHunks([]byte(page.Serialize()), hunks),
			Hunks:   hunks,
		})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}

		fods, err := e.OpDiff(id)
		if err != nil {
			t.Fatalf("OpDiff: %v", err)
		}
		fo := opDiffFileByPath(t, fods, path)
		h2Seen, dupShown := false, false
		for _, w := range fo.Hunks {
			for _, l := range w.Lines {
				if l.Kind != '-' {
					continue
				}
				owner := w.HunkID
				if owner == "h2" && !lineIn(hunks[1].Del, l.Text) {
					t.Fatalf("h2's window carries a '-' line h2 never deleted: %q (window: %+v)", l.Text, w.Lines)
				}
				if l.Text == "Duplicated line." {
					dupShown = true
					if owner != "" {
						t.Fatalf("the duplicate's '-' line must be ownerless (its before-index names no removed line), got %q", owner)
					}
				}
			}
			if w.HunkID == "h2" {
				h2Seen = true
			}
		}
		if !h2Seen {
			t.Fatalf("h2 must still own a window: %+v", fo.Hunks)
		}
		if !dupShown {
			t.Fatalf("the duplicate's deletion must still be displayed: %+v", fo.Hunks)
		}
	})
}
