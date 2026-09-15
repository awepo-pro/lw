package stage

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// opDiffWindowSubtests is TestOpDiff's display-window half, moved here whole
// (a pure move, file-size split): the subtests from dropped_op_still_displayed
// through read_only.
func opDiffWindowSubtests(t *testing.T) {
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
