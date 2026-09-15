package stage

import (
	"errors"
	"strings"
	"testing"
)

// TestOpDiff exercises contract §1's OpDiff over the "minimal" fixture
// (helpers_test.go's newTestEngine), one subtest per contract note.
func TestOpDiff(t *testing.T) {
	t.Run("no_changeset", func(t *testing.T) {
		e, _ := newTestEngine(t)
		_, err := e.OpDiff("op1")
		if !errors.Is(err, ErrNoChangeset) {
			t.Fatalf("OpDiff error = %v, want ErrNoChangeset", err)
		}
	})

	t.Run("unknown_op", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("unknown op", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		_, err := e.OpDiff("op99")
		if err == nil {
			t.Fatal("OpDiff: want an error for an unknown op id")
		}
		want := `stage: op diff: no such op "op99"`
		if err.Error() != want {
			t.Fatalf("OpDiff error = %q, want %q", err.Error(), want)
		}
	})

	// matches_unified_when_nothing_dropped proves contract §1 note 6: for
	// an op with no dropped hunks, OpDiff's concatenated windows equal what
	// Diff.UnifiedFile(path) renders for that op's file — same headers,
	// same line kinds and texts.
	t.Run("matches_unified_when_nothing_dropped", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("equality", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
		if !ok {
			t.Fatal("fixture missing wiki/concepts/kv-cache.md")
		}
		old := "Without caching, generating token n would repeat O(n) work already done for"
		newLine := "Without caching, regenerating token n repeats O(n) work already completed for"
		rewritten := *page
		rewritten.Body = replaceLine(page.Body, old, newLine)
		id, err := e.Append(Op{
			Kind:    OpPatchPage,
			Path:    page.Path,
			Section: "## Why it matters",
			Before:  page.SHA256(),
			Content: rewritten.Serialize(),
			Hunks: []Hunk{
				{ID: "h1", Path: page.Path, Del: []string{old}, Add: []string{newLine}},
			},
		})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}

		fods, err := e.OpDiff(id)
		if err != nil {
			t.Fatalf("OpDiff: %v", err)
		}
		fo := opDiffFileByPath(t, fods, page.Path)

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

	// dropped_hunk_still_displayed proves the whole reason DisplayHunk
	// exists: a dropped hunk's window is still rendered, marked Dropped,
	// with its original text intact — even though DropHunk already
	// overwrote Op.After to the post-drop projection.
	t.Run("dropped_hunk_still_displayed", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("dropped hunk", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
		if !ok {
			t.Fatal("fixture missing wiki/concepts/kv-cache.md")
		}
		// Two edits in one op. Where they sit in the fixture is beside the
		// point since the amended note 4: attribution is positional (the
		// traced reconstruction), so whether hunkWindows keeps the edits in
		// separate windows or merges them, each window/run carries its own
		// hunk's id. The merged case itself is covered explicitly by
		// TestOpDiffAttribution's two_insert_only_hunks_same_section_split
		// and drop_second_of_merged_shows_each_correctly.
		old1 := "Without caching, generating token n would repeat O(n) work already done for"
		new1 := "Without caching, regenerating token n repeats O(n) work already completed for"
		old2 := "- [[speculative-decoding]] — both the draft and target model read the cache"
		new2 := "- [[speculative-decoding]] — both the draft and target models consult the cache"
		body := replaceLine(replaceLine(page.Body, old1, new1), old2, new2)
		rewritten := *page
		rewritten.Body = body
		id, err := e.Append(Op{
			Kind:    OpPatchPage,
			Path:    page.Path,
			Section: "## Why it matters",
			Before:  page.SHA256(),
			Content: rewritten.Serialize(),
			Hunks: []Hunk{
				{ID: "h1", Path: page.Path, Del: []string{old1}, Add: []string{new1}},
				{ID: "h2", Path: page.Path, Del: []string{old2}, Add: []string{new2}},
			},
		})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		if err := e.DropHunk(id, "h2"); err != nil {
			t.Fatalf("DropHunk h2: %v", err)
		}

		fods, err := e.OpDiff(id)
		if err != nil {
			t.Fatalf("OpDiff: %v", err)
		}
		fo := opDiffFileByPath(t, fods, page.Path)

		// One window per owner run: a hunk may span several windows when
		// the other hunk's changes interleave with its own, so collect by
		// id instead of assuming a count tied to how far apart the edits
		// sit in the fixture.
		var h1wins, h2wins []DisplayHunk
		for _, w := range fo.Hunks {
			switch w.HunkID {
			case "h1":
				h1wins = append(h1wins, w)
			case "h2":
				h2wins = append(h2wins, w)
			default:
				t.Fatalf("window attributed to %q, want only h1 and h2: %+v", w.HunkID, fo.Hunks)
			}
		}
		if len(h1wins) == 0 {
			t.Fatalf("no window attributed to h1: %+v", fo.Hunks)
		}
		if len(h2wins) == 0 {
			t.Fatalf("no window attributed to h2: %+v", fo.Hunks)
		}
		for _, w := range h1wins {
			if w.Dropped {
				t.Fatal("h1's window must not be marked Dropped")
			}
		}
		for _, w := range h2wins {
			if !w.Dropped {
				t.Fatal("h2's window must be marked Dropped, even though it is still displayed")
			}
		}
		var hasOld2, hasNew2 bool
		for _, w := range h2wins {
			for _, l := range w.Lines {
				if l.Kind == '-' && l.Text == old2 {
					hasOld2 = true
				}
				if l.Kind == '+' && l.Text == new2 {
					hasNew2 = true
				}
			}
		}
		if !hasOld2 || !hasNew2 {
			t.Fatalf("dropped hunk's windows are missing its original old/new text: %+v", h2wins)
		}
	})
	opDiffWindowSubtests(t)
}

// opDiffFileByPath finds the FileOpDiff for path in fods, failing t if
// absent.
func opDiffFileByPath(t *testing.T, fods []FileOpDiff, path string) FileOpDiff {
	t.Helper()
	for _, fo := range fods {
		if fo.Path == path {
			return fo
		}
	}
	t.Fatalf("no FileOpDiff for %s in %+v", path, fods)
	return FileOpDiff{}
}
