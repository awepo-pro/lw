package stage

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/vault"
)

// TestAttachOriginalValidateAndCommit pins S2 D8 attach lifecycle:
// committed raw without original -> Append attach OpIngestSource (Before = current SHA,
// Content adds original: + original_sha256:, OriginalPath/Original/OriginalContent set)
// -> Diff() shows the 2-line frontmatter addition, OpDiff() carries OriginalLine,
// RawOnly() is false, ProjectedReport() is clean (src-integrity passes)
// -> Commit() writes the original file and updates the raw .md in place.
func TestAttachOriginalValidateAndCommit(t *testing.T) {
	e, dir := newTestEngine(t)
	rawPath := "raw/articles/kv-cache-explained.md"
	raw, ok := e.vault.RawSource(rawPath)
	if !ok {
		t.Fatalf("missing %s in test fixture", rawPath)
	}
	if raw.Original != "" {
		t.Fatalf("%s already has an original", rawPath)
	}

	beforeSHA, ok := canonicalSHA(e.vault, rawPath)
	if !ok {
		t.Fatalf("canonicalSHA for %s not found", rawPath)
	}

	origBytes := []byte("<!DOCTYPE html><html><body><h1>KV Cache, Explained</h1></body></html>\n")
	origPath := "raw/articles/kv-cache-explained.html"
	origSHA := blobSHA(origBytes)

	updated := *raw
	updated.Original = origPath
	updated.OriginalSHA256 = origSHA
	content := updated.Serialize()

	if _, err := e.OpenChangeset("attach original to kv-cache-explained", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	opID, err := e.Append(Op{
		Kind:            OpIngestSource,
		Path:            rawPath,
		Before:          beforeSHA,
		Content:         content,
		Extractor:       "go/html",
		OriginalPath:    origPath,
		Original:        origSHA,
		OriginalContent: origBytes,
	})
	if err != nil {
		t.Fatalf("Append attach op: %v", err)
	}

	// 1. Diff() shows the 2-line frontmatter addition.
	diffRes, err := e.Diff()
	if err != nil {
		t.Fatalf("Diff(): %v", err)
	}
	if len(diffRes.Files) != 1 {
		t.Fatalf("Diff() returned %d files, want 1", len(diffRes.Files))
	}
	fd := diffRes.Files[0]
	if fd.Path != rawPath {
		t.Errorf("Diff path = %s, want %s", fd.Path, rawPath)
	}
	if !strings.Contains(fd.New, "original: "+origPath) || !strings.Contains(fd.New, "original_sha256: "+origSHA) {
		t.Errorf("Diff New does not contain original lines:\n%s", fd.New)
	}
	if strings.Contains(fd.Old, "original:") {
		t.Errorf("Diff Old should not contain original lines:\n%s", fd.Old)
	}

	// 2. OpDiff() carries OriginalLine.
	opDiffs, err := e.OpDiff(opID)
	if err != nil {
		t.Fatalf("OpDiff(): %v", err)
	}
	if len(opDiffs) == 0 {
		t.Fatalf("OpDiff() returned 0 diffs")
	}
	if opDiffs[0].OriginalLine == "" {
		t.Errorf("OpDiff().OriginalLine is empty")
	}
	if !strings.Contains(opDiffs[0].OriginalLine, origPath) {
		t.Errorf("OpDiff().OriginalLine = %q, want path %s", opDiffs[0].OriginalLine, origPath)
	}

	// 3. RawOnly() is false.
	cs, err := e.Current()
	if err != nil {
		t.Fatalf("Current(): %v", err)
	}
	raws, isRawOnly := cs.RawOnly()
	if isRawOnly {
		t.Errorf("RawOnly() = true, want false for attach op")
	}
	if len(raws) != 0 {
		t.Errorf("RawOnly() raws = %v, want empty", raws)
	}

	// 4. ProjectedReport() is clean (src-integrity passes).
	rep, err := e.ProjectedReport()
	if err != nil {
		t.Fatalf("ProjectedReport(): %v", err)
	}
	if rep.Errors != 0 || rep.Warns != 0 {
		t.Errorf("ProjectedReport() has errors=%d, warns=%d; want clean (findings: %+v)", rep.Errors, rep.Warns, rep.Findings)
	}

	// 5. Commit() writes the original file and updates the raw .md in place.
	commitID, err := e.Commit("attach kv-cache original")
	if err != nil {
		t.Fatalf("Commit(): %v", err)
	}
	if commitID == "" {
		t.Fatal("Commit returned empty commit ID")
	}

	diskOrig, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(origPath)))
	if err != nil {
		t.Fatalf("original file not written on disk: %v", err)
	}
	if !bytes.Equal(diskOrig, origBytes) {
		t.Errorf("original file bytes mismatch")
	}

	diskMD, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rawPath)))
	if err != nil {
		t.Fatalf("raw md not on disk: %v", err)
	}
	if !bytes.Equal(diskMD, content) {
		t.Errorf("raw md bytes on disk != op content:\ngot:\n%s\nwant:\n%s", diskMD, content)
	}
}

// TestAttachOriginalValidationRefusals verifies all refusal paths of validateIngestAttach:
// target does not exist, already has original, before mismatch, missing original,
// immutable fields modified, non-canonical content, and duplicate attach in same changeset.
func TestAttachOriginalValidationRefusals(t *testing.T) {
	e, _ := newTestEngine(t)
	rawPath := "raw/articles/kv-cache-explained.md"
	raw, ok := e.vault.RawSource(rawPath)
	if !ok {
		t.Fatalf("missing %s in test fixture", rawPath)
	}

	beforeSHA, _ := canonicalSHA(e.vault, rawPath)
	origBytes := []byte("verbatim original content\n")
	origPath := "raw/articles/kv-cache-explained.html"
	origSHA := blobSHA(origBytes)

	validUpdated := *raw
	validUpdated.Original = origPath
	validUpdated.OriginalSHA256 = origSHA
	validContent := validUpdated.Serialize()

	// 1. Target does not exist
	t.Run("target does not exist", func(t *testing.T) {
		op := Op{
			Kind:            OpIngestSource,
			Path:            "raw/articles/nonexistent.md",
			Before:          beforeSHA,
			Content:         validContent,
			Extractor:       "go/html",
			OriginalPath:    "raw/articles/nonexistent.html",
			Original:        origSHA,
			OriginalContent: origBytes,
		}
		err := ValidateOp(op, e.vault, e.vault.Schema())
		if err == nil || !strings.Contains(err.Error(), "attach target raw/articles/nonexistent.md does not exist") {
			t.Errorf("got %v, want attach target does not exist", err)
		}
	})

	// 2. Target already has an original
	t.Run("target already has original", func(t *testing.T) {
		e2, _ := newTestEngine(t)
		if _, err := e2.OpenChangeset("cs1", testAuthor); err != nil {
			t.Fatal(err)
		}
		if _, err := e2.Append(Op{
			Kind:            OpIngestSource,
			Path:            rawPath,
			Before:          beforeSHA,
			Content:         validContent,
			Extractor:       "go/html",
			OriginalPath:    origPath,
			Original:        origSHA,
			OriginalContent: origBytes,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := e2.Commit("attach cs1"); err != nil {
			t.Fatal(err)
		}

		// Now attempt another attach
		newCurSHA, _ := canonicalSHA(e2.vault, rawPath)
		op := Op{
			Kind:            OpIngestSource,
			Path:            rawPath,
			Before:          newCurSHA,
			Content:         validContent,
			Extractor:       "go/html",
			OriginalPath:    "raw/articles/kv-cache-explained.txt",
			Original:        blobSHA([]byte("txt")),
			OriginalContent: []byte("txt"),
		}
		err := ValidateOp(op, e2.vault, e2.vault.Schema())
		if err == nil || !strings.Contains(err.Error(), "already has original") {
			t.Errorf("got %v, want already has original", err)
		}
	})

	// 3. Before does not match current SHA
	t.Run("before mismatch", func(t *testing.T) {
		op := Op{
			Kind:            OpIngestSource,
			Path:            rawPath,
			Before:          "0000000000000000000000000000000000000000000000000000000000000000",
			Content:         validContent,
			Extractor:       "go/html",
			OriginalPath:    origPath,
			Original:        origSHA,
			OriginalContent: origBytes,
		}
		err := ValidateOp(op, e.vault, e.vault.Schema())
		if err == nil || !strings.Contains(err.Error(), "before does not match the current content") {
			t.Errorf("got %v, want before does not match", err)
		}
	})

	// 4. Extractor is required
	t.Run("extractor required", func(t *testing.T) {
		op := Op{
			Kind:            OpIngestSource,
			Path:            rawPath,
			Before:          beforeSHA,
			Content:         validContent,
			Extractor:       "",
			OriginalPath:    origPath,
			Original:        origSHA,
			OriginalContent: origBytes,
		}
		err := ValidateOp(op, e.vault, e.vault.Schema())
		if err == nil || !strings.Contains(err.Error(), "extractor is required") {
			t.Errorf("got %v, want extractor is required", err)
		}
	})

	// 5. Original fields missing
	t.Run("original required", func(t *testing.T) {
		op := Op{
			Kind:      OpIngestSource,
			Path:      rawPath,
			Before:    beforeSHA,
			Content:   validContent,
			Extractor: "go/html",
		}
		err := ValidateOp(op, e.vault, e.vault.Schema())
		if err == nil || !strings.Contains(err.Error(), "requires an original") {
			t.Errorf("got %v, want requires an original", err)
		}
	})

	// 6. Immutable fields modified (source_url, ingested, sha256, body)
	t.Run("immutable fields modified", func(t *testing.T) {
		// Body modified
		tampered := *raw
		tampered.Original = origPath
		tampered.OriginalSHA256 = origSHA
		tampered.Body = tampered.Body + "\ntampered line\n"
		tampered.SHA256 = vault.BodySHA256(tampered.Body)
		op := Op{
			Kind:            OpIngestSource,
			Path:            rawPath,
			Before:          beforeSHA,
			Content:         tampered.Serialize(),
			Extractor:       "go/html",
			OriginalPath:    origPath,
			Original:        origSHA,
			OriginalContent: origBytes,
		}
		err := ValidateOp(op, e.vault, e.vault.Schema())
		if err == nil || !strings.Contains(err.Error(), "source_url, ingested, sha256 and body are immutable") {
			t.Errorf("got %v, want immutable fields error", err)
		}
	})

	// 7. Non-canonical content
	t.Run("content not canonical", func(t *testing.T) {
		nonCanonical := bytes.Replace(validContent, []byte("source_url: "), []byte("source_url:  "), 1)
		op := Op{
			Kind:            OpIngestSource,
			Path:            rawPath,
			Before:          beforeSHA,
			Content:         nonCanonical,
			Extractor:       "go/html",
			OriginalPath:    origPath,
			Original:        origSHA,
			OriginalContent: origBytes,
		}
		err := ValidateOp(op, e.vault, e.vault.Schema())
		if err == nil || !strings.Contains(err.Error(), "content is not canonical") {
			t.Errorf("got %v, want content is not canonical", err)
		}
	})

	// 8. Second attach for the same raw in the same changeset
	t.Run("second attach in same changeset", func(t *testing.T) {
		e3, _ := newTestEngine(t)
		if _, err := e3.OpenChangeset("cs", testAuthor); err != nil {
			t.Fatal(err)
		}
		op1 := Op{
			Kind:            OpIngestSource,
			Path:            rawPath,
			Before:          beforeSHA,
			Content:         validContent,
			Extractor:       "go/html",
			OriginalPath:    origPath,
			Original:        origSHA,
			OriginalContent: origBytes,
		}
		if _, err := e3.Append(op1); err != nil {
			t.Fatalf("first append failed: %v", err)
		}
		// Attempting second attach on the same rawPath
		op2 := op1
		op2.ID = ""
		_, err := e3.Append(op2)
		if err == nil || !strings.Contains(err.Error(), "already has a staged attach in this changeset") {
			t.Errorf("got %v, want already has a staged attach in this changeset", err)
		}
	})
}

// TestAttachOriginalRefreshAndRevert pins Refresh staleness detection and Revert skipped reporting:
// Refresh keeps attach op StateProposed while raw on disk matches Before, flips to StateStale
// if raw is edited or OriginalPath appears on disk; Revert of a committed attach reports both
// the raw .md and its OriginalPath in skipped.
func TestAttachOriginalRefreshAndRevert(t *testing.T) {
	e, dir := newTestEngine(t)
	rawPath := "raw/articles/kv-cache-explained.md"
	raw, ok := e.vault.RawSource(rawPath)
	if !ok {
		t.Fatalf("missing %s in test fixture", rawPath)
	}

	beforeSHA, _ := canonicalSHA(e.vault, rawPath)
	origBytes := []byte("<!DOCTYPE html><html><body>KV Cache</body></html>\n")
	origPath := "raw/articles/kv-cache-explained.html"
	origSHA := blobSHA(origBytes)

	updated := *raw
	updated.Original = origPath
	updated.OriginalSHA256 = origSHA
	content := updated.Serialize()

	if _, err := e.OpenChangeset("cs-refresh-revert", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	opID, err := e.Append(Op{
		Kind:            OpIngestSource,
		Path:            rawPath,
		Before:          beforeSHA,
		Content:         content,
		Extractor:       "go/html",
		OriginalPath:    origPath,
		Original:        origSHA,
		OriginalContent: origBytes,
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Initial Refresh: StateProposed
	if err := e.Refresh(); err != nil {
		t.Fatalf("Refresh(): %v", err)
	}
	cs, _ := e.Current()
	op, _ := cs.Op(opID)
	if op.State != StateProposed {
		t.Errorf("state after fresh Refresh = %v, want StateProposed", op.State)
	}

	rawDiskPath := filepath.Join(dir, filepath.FromSlash(rawPath))
	originalDiskBytes, err := os.ReadFile(rawDiskPath)
	if err != nil {
		t.Fatalf("read raw on disk: %v", err)
	}

	// 1. Edit raw on disk -> StateStale
	if err := os.WriteFile(rawDiskPath, append(originalDiskBytes, []byte("\n# edit\n")...), 0644); err != nil {
		t.Fatal(err)
	}
	if err := e.Refresh(); err != nil {
		t.Fatalf("Refresh() after disk edit: %v", err)
	}
	cs, _ = e.Current()
	op, _ = cs.Op(opID)
	if op.State != StateStale {
		t.Errorf("state after disk edit = %v, want StateStale", op.State)
	}

	// Restore raw on disk -> back to StateProposed
	if err := os.WriteFile(rawDiskPath, originalDiskBytes, 0644); err != nil {
		t.Fatal(err)
	}
	if err := e.Refresh(); err != nil {
		t.Fatalf("Refresh() after restore: %v", err)
	}
	cs, _ = e.Current()
	op, _ = cs.Op(opID)
	if op.State != StateProposed {
		t.Errorf("state after restore = %v, want StateProposed", op.State)
	}

	// 2. OriginalPath appears on disk -> StateStale
	origDiskPath := filepath.Join(dir, filepath.FromSlash(origPath))
	if err := os.WriteFile(origDiskPath, origBytes, 0644); err != nil {
		t.Fatal(err)
	}
	if err := e.Refresh(); err != nil {
		t.Fatalf("Refresh() after original created: %v", err)
	}
	cs, _ = e.Current()
	op, _ = cs.Op(opID)
	if op.State != StateStale {
		t.Errorf("state after original created = %v, want StateStale", op.State)
	}

	// Remove OriginalPath from disk -> back to StateProposed
	if err := os.Remove(origDiskPath); err != nil {
		t.Fatal(err)
	}
	if err := e.Refresh(); err != nil {
		t.Fatalf("Refresh() after original removed: %v", err)
	}
	cs, _ = e.Current()
	op, _ = cs.Op(opID)
	if op.State != StateProposed {
		t.Errorf("state after original removed = %v, want StateProposed", op.State)
	}

	// 3. Commit and Revert: skipped reports both raw .md and OriginalPath
	commitID, err := e.Commit("attach commit")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	revertCS, err := e.Revert(commitID)
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if len(revertCS.Ops) != 0 {
		t.Errorf("Revert produced %d ops, want 0", len(revertCS.Ops))
	}
	skipped := lastRevertedSkipped(t, e)
	hasRaw := false
	hasOrig := false
	for _, p := range skipped {
		if p == rawPath {
			hasRaw = true
		}
		if p == origPath {
			hasOrig = true
		}
	}
	if !hasRaw || !hasOrig {
		t.Errorf("Revert skipped = %v, want both %s and %s", skipped, rawPath, origPath)
	}
}

// TestSchemaConformanceAttach verifies that a changeset carrying an attach OpIngestSource
// (with Before, After, and original fields) validates against spec/changeset.schema.json.
func TestSchemaConformanceAttach(t *testing.T) {
	root, s := loadChangesetSchema(t)
	e, _ := newTestEngine(t)

	rawPath := "raw/articles/kv-cache-explained.md"
	raw, ok := e.vault.RawSource(rawPath)
	if !ok {
		t.Fatalf("missing %s in test fixture", rawPath)
	}
	beforeSHA, _ := canonicalSHA(e.vault, rawPath)
	origBytes := []byte("<!DOCTYPE html><html><body>KV Cache</body></html>\n")
	origPath := "raw/articles/kv-cache-explained.html"
	origSHA := blobSHA(origBytes)

	updated := *raw
	updated.Original = origPath
	updated.OriginalSHA256 = origSHA

	if _, err := e.OpenChangeset("attach schema test", testAuthor); err != nil {
		t.Fatal(err)
	}

	if _, err := e.Append(Op{
		Kind:            OpIngestSource,
		Path:            rawPath,
		Before:          beforeSHA,
		Content:         updated.Serialize(),
		Extractor:       "go/html",
		OriginalPath:    origPath,
		Original:        origSHA,
		OriginalContent: origBytes,
	}); err != nil {
		t.Fatal(err)
	}

	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}

	assertValidatesAgainstSchema(t, root, s, cs)
}
