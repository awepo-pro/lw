package stage

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
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
		// Two edits far enough apart (>2*diffContext+1 lines) that
		// hunkWindows cannot merge them into one window.
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
		if len(fo.Hunks) != 2 {
			t.Fatalf("Hunks = %d windows, want 2 (the two edits are far apart): %+v", len(fo.Hunks), fo.Hunks)
		}

		var h1win, h2win *DisplayHunk
		for i := range fo.Hunks {
			switch fo.Hunks[i].HunkID {
			case "h1":
				h1win = &fo.Hunks[i]
			case "h2":
				h2win = &fo.Hunks[i]
			}
		}
		if h1win == nil {
			t.Fatalf("no window attributed to h1: %+v", fo.Hunks)
		}
		if h1win.Dropped {
			t.Fatal("h1's window must not be marked Dropped")
		}
		if h2win == nil {
			t.Fatalf("no window attributed to h2: %+v", fo.Hunks)
		}
		if !h2win.Dropped {
			t.Fatal("h2's window must be marked Dropped, even though it is still displayed")
		}
		var hasOld2, hasNew2 bool
		for _, l := range h2win.Lines {
			if l.Kind == '-' && l.Text == old2 {
				hasOld2 = true
			}
			if l.Kind == '+' && l.Text == new2 {
				hasNew2 = true
			}
		}
		if !hasOld2 || !hasNew2 {
			t.Fatalf("dropped hunk's window is missing its original old/new text: %+v", h2win.Lines)
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
