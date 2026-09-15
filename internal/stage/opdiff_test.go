package stage

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
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

	// dropped_op_still_displayed proves a whole DropOp'd op still renders
	// its full proposed content through OpDiff, even though Diff() (and
	// therefore Commit) excludes it entirely.
	t.Run("dropped_op_still_displayed", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("dropped op", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		path := "wiki/concepts/speculative-rejection.md"
		content := newConceptPageContent("Speculative Rejection")
		id, err := e.Append(Op{
			Kind:       OpCreatePage,
			Path:       path,
			Content:    content,
			Rationale:  "test fixture",
			Provenance: []string{"raw/papers/leviathan-2023.md"},
		})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		if err := e.DropOp(id); err != nil {
			t.Fatalf("DropOp: %v", err)
		}

		d, err := e.Diff()
		if err != nil {
			t.Fatalf("Diff: %v", err)
		}
		for _, fd := range d.Files {
			if fd.OpID == id {
				t.Fatalf("Diff() still lists the dropped op %s; test fixture is not exercising the dropped path", id)
			}
		}

		fods, err := e.OpDiff(id)
		if err != nil {
			t.Fatalf("OpDiff on a dropped op: %v", err)
		}
		fo := opDiffFileByPath(t, fods, path)
		if len(fo.Hunks) != 1 || len(fo.Hunks[0].Lines) == 0 {
			t.Fatalf("dropped op's window is empty: %+v", fo.Hunks)
		}
		for _, l := range fo.Hunks[0].Lines {
			if l.Kind != '+' {
				t.Fatalf("dropped create_page window has a non-'+' line: %+v", l)
			}
		}
	})

	// create_page_single_window proves a whole-file create renders as one
	// window covering every added line, "@@ -0,0 +1,N @@".
	t.Run("create_page_single_window", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("create page", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		path := "wiki/concepts/prompt-caching.md"
		content := newConceptPageContent("Prompt Caching")
		id, err := e.Append(Op{
			Kind:       OpCreatePage,
			Path:       path,
			Content:    content,
			Rationale:  "test fixture",
			Provenance: []string{"raw/papers/leviathan-2023.md"},
		})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}

		fods, err := e.OpDiff(id)
		if err != nil {
			t.Fatalf("OpDiff: %v", err)
		}
		fo := opDiffFileByPath(t, fods, path)
		if fo.OpID != id {
			t.Fatalf("OpID = %q, want %q", fo.OpID, id)
		}
		if fo.Kind != OpCreatePage {
			t.Fatalf("Kind = %q, want %q", fo.Kind, OpCreatePage)
		}
		if len(fo.Hunks) != 1 {
			t.Fatalf("Hunks = %d windows, want exactly 1 for a whole-file create: %+v", len(fo.Hunks), fo.Hunks)
		}

		win := fo.Hunks[0]
		wantLines := strings.Count(strings.TrimSuffix(string(content), "\n"), "\n") + 1
		wantHeader := fmt.Sprintf("@@ -0,0 +1,%d @@", wantLines)
		if win.Header != wantHeader {
			t.Fatalf("Header = %q, want %q", win.Header, wantHeader)
		}
		if len(win.Lines) != wantLines {
			t.Fatalf("window has %d lines, want %d", len(win.Lines), wantLines)
		}
		for _, l := range win.Lines {
			if l.Kind != '+' {
				t.Fatalf("create_page window has a non-'+' line: %+v", l)
			}
		}
	})

	// derived_index_attached proves contract §1 note 5: the create_page's
	// derived index.md insertion is attached as its own FileOpDiff, HunkID
	// always "".
	t.Run("derived_index_attached", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("derived index", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		title := "Prompt Caching"
		path := "wiki/concepts/prompt-caching.md"
		content := newConceptPageContent(title)
		id, err := e.Append(Op{
			Kind:       OpCreatePage,
			Path:       path,
			Content:    content,
			Rationale:  "test fixture",
			Provenance: []string{"raw/papers/leviathan-2023.md"},
		})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}

		fods, err := e.OpDiff(id)
		if err != nil {
			t.Fatalf("OpDiff: %v", err)
		}
		idxFo := opDiffFileByPath(t, fods, "index.md")
		if idxFo.OpID != id {
			t.Fatalf("index.md OpID = %q, want %q", idxFo.OpID, id)
		}
		if len(idxFo.Hunks) != 1 {
			t.Fatalf("index.md Hunks = %d windows, want 1: %+v", len(idxFo.Hunks), idxFo.Hunks)
		}
		win := idxFo.Hunks[0]
		if win.HunkID != "" {
			t.Fatalf("derived index.md window HunkID = %q, want \"\"", win.HunkID)
		}
		if win.Dropped {
			t.Fatal("derived index.md window must not be Dropped")
		}
		var plusLines []DisplayLine
		for _, l := range win.Lines {
			if l.Kind == '+' {
				plusLines = append(plusLines, l)
			}
		}
		if len(plusLines) != 1 {
			t.Fatalf("derived index.md window has %d '+' lines, want exactly 1: %+v", len(plusLines), win.Lines)
		}
		wantEntry := "- [[prompt-caching]] — " + title
		if plusLines[0].Text != wantEntry {
			t.Fatalf("derived index.md '+' line = %q, want %q", plusLines[0].Text, wantEntry)
		}
	})

	// read_only snapshots changeset.json, journal.ndjson and the object
	// store's file list before and after 50 OpDiff calls (a mix of valid
	// and unknown ids) and asserts nothing moved.
	t.Run("read_only", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("read only", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}

		page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
		if !ok {
			t.Fatal("fixture missing wiki/concepts/kv-cache.md")
		}
		old := "step, at the cost of memory that grows with sequence length."
		newLine := "step, at the cost of memory proportional to sequence length."
		rewritten := *page
		rewritten.Body = replaceLine(page.Body, old, newLine)
		patchID, err := e.Append(Op{
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
			t.Fatalf("Append patch_page: %v", err)
		}
		if err := e.DropHunk(patchID, "h1"); err != nil {
			t.Fatalf("DropHunk: %v", err)
		}

		createID, err := e.Append(Op{
			Kind:       OpCreatePage,
			Path:       "wiki/concepts/read-only-fixture.md",
			Content:    newConceptPageContent("Read Only Fixture"),
			Rationale:  "test fixture",
			Provenance: []string{"raw/papers/leviathan-2023.md"},
		})
		if err != nil {
			t.Fatalf("Append create_page: %v", err)
		}

		c, err := e.Current()
		if err != nil {
			t.Fatalf("Current: %v", err)
		}

		csPath := filepath.Join(e.changesetOpenDir(), c.ID, "changeset.json")
		journalPath := filepath.Join(e.llmwikiDir(), "journal.ndjson")
		objectsDir := filepath.Join(e.llmwikiDir(), "objects")

		snapshot := func() (cs, journal []byte, objects []string) {
			t.Helper()
			var err error
			cs, err = os.ReadFile(csPath)
			if err != nil {
				t.Fatalf("read changeset.json: %v", err)
			}
			journal, err = os.ReadFile(journalPath)
			if err != nil {
				t.Fatalf("read journal.ndjson: %v", err)
			}
			err = filepath.WalkDir(objectsDir, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				rel, relErr := filepath.Rel(objectsDir, p)
				if relErr != nil {
					return relErr
				}
				objects = append(objects, rel)
				return nil
			})
			if err != nil {
				t.Fatalf("walk objects: %v", err)
			}
			sort.Strings(objects)
			return
		}

		beforeCS, beforeJournal, beforeObjects := snapshot()

		ids := []string{patchID, createID, "op-does-not-exist"}
		for i := 0; i < 50; i++ {
			id := ids[i%len(ids)]
			_, err := e.OpDiff(id)
			if id == "op-does-not-exist" {
				if err == nil {
					t.Fatalf("OpDiff(%s) call %d: want an error", id, i)
				}
				continue
			}
			if err != nil {
				t.Fatalf("OpDiff(%s) call %d: %v", id, i, err)
			}
		}

		afterCS, afterJournal, afterObjects := snapshot()

		if !bytes.Equal(beforeCS, afterCS) {
			t.Fatal("changeset.json bytes changed across 50 OpDiff calls")
		}
		if !bytes.Equal(beforeJournal, afterJournal) {
			t.Fatal("journal.ndjson bytes changed across 50 OpDiff calls")
		}
		if !equalStrings(beforeObjects, afterObjects) {
			t.Fatalf("object store file list changed:\nbefore=%v\nafter=%v", beforeObjects, afterObjects)
		}
	})
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

// TestOpDiffMockupVault asserts the frozen T01 table (MASTER §5) against
// the private mockup vault. Skipped unless LW_MOCKUP_VAULT is set — the
// vault holds third-party text and local paths and must never enter the
// repo, so it is copied to a t.TempDir() and read only from there.
func TestOpDiffMockupVault(t *testing.T) {
	src := os.Getenv("LW_MOCKUP_VAULT")
	if src == "" {
		t.Skip("LW_MOCKUP_VAULT not set: this conformance check runs locally against the private mockup vault")
	}

	dst := filepath.Join(t.TempDir(), "ml-notes")
	if err := copyDirRecursive(src, dst); err != nil {
		t.Fatalf("copy mockup vault: %v", err)
	}

	e, err := OpenEngine(dst)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { e.Close() })

	if err := e.DropHunk("op4", "h1"); err != nil {
		t.Fatalf("DropHunk(op4, h1): %v", err)
	}

	type wantCounts struct{ spaces, plus, minus int } // -1 means "don't check"
	type wantWin struct {
		header  string // "" means "don't check"
		hunkID  string
		dropped bool
		counts  wantCounts
	}
	type wantFile struct {
		path string
		wins []wantWin
	}

	cases := []struct {
		op    string
		files []wantFile
	}{
		{"op1", []wantFile{
			{"raw/articles/claude-in-a-box.md", []wantWin{
				{"@@ -0,0 +1,286 @@", "", false, wantCounts{0, 286, 0}},
			}},
		}},
		{"op2", []wantFile{
			{"wiki/concepts/network-namespace-egress-isolation.md", []wantWin{
				{"@@ -0,0 +1,52 @@", "", false, wantCounts{0, 52, 0}},
			}},
			{"index.md", []wantWin{
				// The frozen table pins only "exactly 1 +" for this window;
				// context ('- '') line counts are unspecified.
				{"", "", false, wantCounts{-1, 1, -1}},
			}},
		}},
		{"op3", []wantFile{
			{"wiki/concepts/private-network-access.md", []wantWin{
				{"@@ -34,6 +34,10 @@", "h1", false, wantCounts{6, 4, 0}},
			}},
		}},
		{"op4", []wantFile{
			{"wiki/entities/claude.md", []wantWin{
				{"@@ -21,6 +21,10 @@", "h1", true, wantCounts{6, 4, 0}},
			}},
		}},
	}

	totalNonDropped, totalDropped := 0, 0
	for _, tc := range cases {
		fods, err := e.OpDiff(tc.op)
		if err != nil {
			t.Fatalf("OpDiff(%s): %v", tc.op, err)
		}
		if len(fods) != len(tc.files) {
			t.Fatalf("OpDiff(%s) returned %d files, want %d: %+v", tc.op, len(fods), len(tc.files), fods)
		}
		for i, wf := range tc.files {
			fo := fods[i]
			if fo.Path != wf.path {
				t.Fatalf("OpDiff(%s) file[%d].Path = %q, want %q", tc.op, i, fo.Path, wf.path)
			}
			if len(fo.Hunks) != len(wf.wins) {
				t.Fatalf("OpDiff(%s) %s has %d windows, want %d: %+v", tc.op, wf.path, len(fo.Hunks), len(wf.wins), fo.Hunks)
			}
			for j, ww := range wf.wins {
				win := fo.Hunks[j]
				if ww.header != "" && win.Header != ww.header {
					t.Fatalf("OpDiff(%s) %s window[%d].Header = %q, want %q", tc.op, wf.path, j, win.Header, ww.header)
				}
				if win.HunkID != ww.hunkID {
					t.Fatalf("OpDiff(%s) %s window[%d].HunkID = %q, want %q", tc.op, wf.path, j, win.HunkID, ww.hunkID)
				}
				if win.Dropped != ww.dropped {
					t.Fatalf("OpDiff(%s) %s window[%d].Dropped = %v, want %v", tc.op, wf.path, j, win.Dropped, ww.dropped)
				}
				var got wantCounts
				for _, l := range win.Lines {
					switch l.Kind {
					case ' ':
						got.spaces++
					case '+':
						got.plus++
					case '-':
						got.minus++
					}
				}
				if ww.counts.spaces != -1 && got.spaces != ww.counts.spaces {
					t.Fatalf("OpDiff(%s) %s window[%d] ' ' count = %d, want %d", tc.op, wf.path, j, got.spaces, ww.counts.spaces)
				}
				if ww.counts.plus != -1 && got.plus != ww.counts.plus {
					t.Fatalf("OpDiff(%s) %s window[%d] '+' count = %d, want %d", tc.op, wf.path, j, got.plus, ww.counts.plus)
				}
				if ww.counts.minus != -1 && got.minus != ww.counts.minus {
					t.Fatalf("OpDiff(%s) %s window[%d] '-' count = %d, want %d", tc.op, wf.path, j, got.minus, ww.counts.minus)
				}
				if win.Dropped {
					totalDropped++
				} else {
					totalNonDropped++
				}
			}
		}
	}

	if totalNonDropped != 4 || totalDropped != 1 {
		t.Fatalf("totals across live ops = %d non-dropped, %d dropped windows; want 4 and 1", totalNonDropped, totalDropped)
	}
}

// copyDirRecursive recursively copies src into dst, creating directories as
// needed (this test file's own copy helper — the mockup vault lives outside
// spec/fixtures/, so testutil.CopyFixture, which only ever reads under
// spec/fixtures/, does not apply).
func copyDirRecursive(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		}

		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

// TestOpDiffAttribution is the amended contract §1 note 4's suite (joint
// T01+T15 review C1, MASTER §8 ORCH-7): hunk ownership is carried through
// the traced reconstruction and correlated to line indices, never inferred
// from text — so merged windows split per owner, identical-text hunks stay
// distinct, every hunk is covered by at least one of its windows, and the
// traced bytes are exactly applyHunks'.
func TestOpDiffAttribution(t *testing.T) {
	// two_insert_only_hunks_same_section_split is C1's case 1: two
	// insert-only hunks naming the SAME section land adjacent
	// (insertAtSectionEnd puts each at the section's end, the second right
	// after the first's own lines), so hunkWindows merges all four '+' ops
	// into ONE window. The trace must split it into one DisplayHunk per
	// hunk, each carrying exactly its own '+' lines — text attribution
	// labelled the whole merged window h1, and y/n then acted on h1 while
	// both hunks were shown.
	t.Run("two_insert_only_hunks_same_section_split", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("same section", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
		if !ok {
			t.Fatal("fixture missing wiki/concepts/kv-cache.md")
		}
		hunks := []Hunk{
			{ID: "h1", Path: page.Path, Section: "## Example", Add: []string{"h1 note line.", ""}},
			{ID: "h2", Path: page.Path, Section: "## Example", Add: []string{"h2 note line.", ""}},
		}
		id, err := e.Append(Op{
			Kind:    OpPatchPage,
			Path:    page.Path,
			Section: "## Example",
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
		fo := opDiffFileByPath(t, fods, page.Path)
		if len(fo.Hunks) != 2 {
			t.Fatalf("windows = %d, want 2 (one per hunk after the merged window is split by owner): %+v", len(fo.Hunks), fo.Hunks)
		}
		if fo.Hunks[0].HunkID != "h1" || fo.Hunks[1].HunkID != "h2" {
			t.Fatalf("HunkIDs = [%q, %q], want [h1 h2] in body order", fo.Hunks[0].HunkID, fo.Hunks[1].HunkID)
		}
		if fo.Hunks[0].Dropped || fo.Hunks[1].Dropped {
			t.Fatal("nothing was dropped; no window may be marked Dropped")
		}
		assertPlusTexts(t, fo.Hunks[0], "h1 note line.", "")
		assertPlusTexts(t, fo.Hunks[1], "h2 note line.", "")
	})

	// identical_text_hunks_attributed_by_position is C1's case 2: the SAME
	// Add text in two DIFFERENT sections. Text attribution returned the
	// first hunk whose Add contained the line, so BOTH windows were
	// labelled h1 and a y/n on the second mutated the first. The positional
	// trace labels each window with its own hunk, and each window's header
	// position matches its own hunk's section (h1's "## Why it matters"
	// anchor precedes h2's end-of-body one).
	t.Run("identical_text_hunks_attributed_by_position", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("identical text", testAuthor); err != nil {
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

		fods, err := e.OpDiff(id)
		if err != nil {
			t.Fatalf("OpDiff: %v", err)
		}
		fo := opDiffFileByPath(t, fods, page.Path)
		// THREE windows: h1's insert, then an OWNERLESS one, then h2's.
		// h2 anchors at end of body — after the page's own trailing
		// newline — so that pre-existing blank becomes a visible '+' line
		// of its own. The trace proves it is inherited (both hunks insert
		// byte-identical blanks, so its owner is provably unnameable), and
		// it must carry NO id: folding it into h2's window would put a
		// line h2 does not own under h2's id, and y/n acts on that id.
		if len(fo.Hunks) != 3 {
			t.Fatalf("windows = %d, want 3 (h1, the ownerless trailing blank, h2): %+v", len(fo.Hunks), fo.Hunks)
		}
		if fo.Hunks[0].HunkID != "h1" || fo.Hunks[1].HunkID != "" || fo.Hunks[2].HunkID != "h2" {
			t.Fatalf("HunkIDs = [%q, %q, %q], want [h1 \"\" h2]: identical text must not swap the attribution, and the unowned blank must stay ownerless", fo.Hunks[0].HunkID, fo.Hunks[1].HunkID, fo.Hunks[2].HunkID)
		}
		_, _, newStart1, _ := parseHeader(t, fo.Hunks[0].Header)
		_, _, newStart2, _ := parseHeader(t, fo.Hunks[1].Header)
		_, _, newStart3, _ := parseHeader(t, fo.Hunks[2].Header)
		if !(newStart1 < newStart2 && newStart2 < newStart3) {
			t.Fatalf("windows must sit in body order (new starts %d, %d, %d): each window's header must match its own hunk's position", newStart1, newStart2, newStart3)
		}
		for _, w := range fo.Hunks {
			if w.Dropped {
				t.Fatalf("window %q must not be marked Dropped; nothing was dropped", w.HunkID)
			}
		}
		// Each window carries exactly its own lines: each owned window
		// exactly its own hunk's two added lines, the ownerless middle one
		// only the inherited blank.
		wantPlus := map[string][]string{
			"h1": {"Shared added line.", ""},
			"":   {""},
			"h2": {"Shared added line."},
		}
		for _, w := range fo.Hunks {
			var got []string
			for _, l := range w.Lines {
				if l.Kind == '+' {
					got = append(got, l.Text)
				}
			}
			if !reflect.DeepEqual(got, wantPlus[w.HunkID]) {
				t.Fatalf("window %q '+' texts = %q, want %q", w.HunkID, got, wantPlus[w.HunkID])
			}
		}
	})

	// drop_second_of_merged_shows_each_correctly drops h2 of the merged
	// same-section pair: the window still splits per owner, the h1 run is
	// live, and the h2 run is Dropped with its own content — so Review can
	// show (and undrop) exactly the hunk it displays.
	t.Run("drop_second_of_merged_shows_each_correctly", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("drop second of merged", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
		if !ok {
			t.Fatal("fixture missing wiki/concepts/kv-cache.md")
		}
		hunks := []Hunk{
			{ID: "h1", Path: page.Path, Section: "## Example", Add: []string{"h1 note line.", ""}},
			{ID: "h2", Path: page.Path, Section: "## Example", Add: []string{"h2 note line.", ""}},
		}
		id, err := e.Append(Op{
			Kind:    OpPatchPage,
			Path:    page.Path,
			Section: "## Example",
			Before:  page.SHA256(),
			Content: applyHunks([]byte(page.Serialize()), hunks),
			Hunks:   hunks,
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
		if len(fo.Hunks) != 2 {
			t.Fatalf("windows = %d, want 2 (the merged window splits per owner even after a drop): %+v", len(fo.Hunks), fo.Hunks)
		}
		if fo.Hunks[0].HunkID != "h1" || fo.Hunks[1].HunkID != "h2" {
			t.Fatalf("HunkIDs = [%q, %q], want [h1 h2]", fo.Hunks[0].HunkID, fo.Hunks[1].HunkID)
		}
		if fo.Hunks[0].Dropped {
			t.Fatal("h1's run must be live")
		}
		if !fo.Hunks[1].Dropped {
			t.Fatal("h2's run must carry h2's Dropped flag")
		}
		assertPlusTexts(t, fo.Hunks[0], "h1 note line.", "")
		assertPlusTexts(t, fo.Hunks[1], "h2 note line.", "")
	})

	// every_hunk_covered pins note 4's coverage rule over a deterministic
	// generated op (fixed seed, 1-4 hunks): every Hunk.ID appears in at
	// least one DisplayHunk, and every owned '+'/'-' line in a window
	// belongs to the window's own hunk.
	t.Run("every_hunk_covered", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		fixtureRel := filepath.Join("wiki", "concepts", "generated-hunks-fixture.md")
		if err := os.WriteFile(filepath.Join(dir, fixtureRel), []byte(generatedHunksPage()), 0o644); err != nil {
			t.Fatalf("write generated-hunks-fixture.md: %v", err)
		}

		e, err := OpenEngine(dir)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		t.Cleanup(func() { e.Close() })
		e.now = testutil.FixedClock()

		if _, err := e.OpenChangeset("generated hunks", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		path := filepath.ToSlash(fixtureRel)
		page, ok := e.Vault().Page(path)
		if !ok {
			t.Fatalf("fixture page %s not loaded", path)
		}

		hunks := generateHunks(rand.New(rand.NewSource(1503)), path)
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
		covered := map[string]int{}
		for _, w := range fo.Hunks {
			if w.HunkID == "" {
				t.Fatal("a patch_page with owned hunks produced an ownerless window")
			}
			covered[w.HunkID]++
			var h *Hunk
			for i := range hunks {
				if hunks[i].ID == w.HunkID {
					h = &hunks[i]
					break
				}
			}
			if h == nil {
				t.Fatalf("window attributed to unknown hunk %q", w.HunkID)
			}
			for _, l := range w.Lines {
				switch l.Kind {
				case '+':
					if !lineIn(h.Add, l.Text) {
						t.Fatalf("window %s carries a '+' line hunk %s never added: %q", w.HunkID, h.ID, l.Text)
					}
				case '-':
					if !lineIn(h.Del, l.Text) {
						t.Fatalf("window %s carries a '-' line hunk %s never deleted: %q", w.HunkID, h.ID, l.Text)
					}
				}
			}
		}
		for _, h := range hunks {
			if covered[h.ID] == 0 {
				t.Fatalf("hunk %s appears in no DisplayHunk (covered: %v)", h.ID, covered)
			}
		}
	})

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
