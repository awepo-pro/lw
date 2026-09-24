package stage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/vault"
)

// The 033 T2 lifecycle tests: the original binary travels with an
// ingest_source op from Append through Commit, is written or written-not
// exactly when the op is, and is named by every surface that describes the
// commit — never silently dropped.

const (
	originalRawPath = "raw/papers/attention-is-boring.md"
	originalPDFPath = "raw/papers/attention-is-boring.pdf"
	originalBody    = "# Attention Is Boring\n\nThe extracted body.\n"
)

// originalBlob returns a fake 3 KiB PDF body — big enough that a bug that
// truncates or aliases the CAS bytes cannot pass by accident.
func originalBlob() []byte {
	blob := make([]byte, 3072)
	copy(blob, "%PDF-1.6 fake pdf for the 033 T2 tests\n")
	for i := range blob {
		blob[i] = byte('a' + i%26)
	}
	copy(blob, "%PDF-1.6 fake pdf for the 033 T2 tests\n")
	return blob
}

func blobSHA(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// originalRawFile returns the whole raw/ file bytes an ingest of the blob
// proposes: the 033 five-key frontmatter (original pair included) plus the
// body. frontOriginal/frontOriginalSHA override what the frontmatter
// DECLARES, for the mismatch tests; empty means "leave the key out".
func originalRawFile(blob []byte, frontOriginal, frontOriginalSHA string) []byte {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("source_url: https://example.test/attention-is-boring\n")
	b.WriteString("ingested: 2026-09-24\n")
	b.WriteString("sha256: " + vault.BodySHA256(originalBody) + "\n")
	if frontOriginal != "" {
		b.WriteString("original: " + frontOriginal + "\n")
	}
	if frontOriginalSHA != "" {
		b.WriteString("original_sha256: " + frontOriginalSHA + "\n")
	}
	b.WriteString("---\n\n")
	b.WriteString(originalBody)
	return []byte(b.String())
}

// stageOriginalIngest opens a changeset on e and appends one ingest_source
// carrying the original blob, the way the tool does: OriginalPath beside
// Path, Original the blob's sha, OriginalContent the bytes themselves, and
// a raw file whose frontmatter declares the same pair. It returns the op id.
func stageOriginalIngest(t *testing.T, e *Engine) string {
	t.Helper()
	blob := originalBlob()
	if _, err := e.OpenChangeset("ingest a source with its original", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	id, err := e.Append(Op{
		Kind:            OpIngestSource,
		Path:            originalRawPath,
		Extractor:       "docling/pdf",
		Content:         originalRawFile(blob, originalPDFPath, blobSHA(blob)),
		OriginalPath:    originalPDFPath,
		Original:        blobSHA(blob),
		OriginalContent: blob,
	})
	if err != nil {
		t.Fatalf("Append ingest with original: %v", err)
	}
	return id
}

// TestCommitWritesBoth: Commit materializes the raw md AND the original at
// OriginalPath, each with exactly the bytes the op carried.
func TestCommitWritesBoth(t *testing.T) {
	e, dir := newTestEngine(t)
	blob := originalBlob()
	stageOriginalIngest(t, e)

	commitID, err := e.Commit("ingest a source with its original")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if commitID == "" {
		t.Fatal("Commit returned an empty commit id")
	}

	md, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(originalRawPath)))
	if err != nil {
		t.Fatalf("committed raw md not on disk: %v", err)
	}
	if want := string(originalRawFile(blob, originalPDFPath, blobSHA(blob))); string(md) != want {
		t.Errorf("committed md =\n%q\nwant\n%q", md, want)
	}

	pdf, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(originalPDFPath)))
	if err != nil {
		t.Fatalf("committed original not on disk at %s: %v", originalPDFPath, err)
	}
	if !bytes.Equal(pdf, blob) {
		t.Errorf("committed original is %d bytes, want the op's %d bytes byte-identical", len(pdf), len(blob))
	}
}

// TestDropWritesNeither: after 030's DropOps drops the ingest, Commit has
// nothing to apply and writes neither the md nor the pdf.
func TestDropWritesNeither(t *testing.T) {
	e, dir := newTestEngine(t)
	id := stageOriginalIngest(t, e)

	if err := e.DropOps([]string{id}); err != nil {
		t.Fatalf("DropOps: %v", err)
	}
	if _, err := e.Commit("nothing left to apply"); !errors.Is(err, ErrNothingToCommit) {
		t.Fatalf("Commit after DropOps = %v, want ErrNothingToCommit", err)
	}
	for _, p := range []string{originalRawPath, originalPDFPath} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); !os.IsNotExist(err) {
			t.Errorf("dropped ingest left a file at %s (stat err = %v)", p, err)
		}
	}
}

// TestRevertRemovesBoth is the 033 contract's third lifecycle bullet, and
// it carries the subtask's ONE deviation, stated here rather than buried:
// the frozen wording is "revert of that commit removes both", but backbone
// §5.8 (the frozen API contract, 01-backbone.md) is explicit that Revert
// "never touches the working tree" (D-BZ) and that an addition under raw/
// is "not invertible — reported, not emitted" (D-BY). Literal removal would
// break the backbone and the shipped pin on it
// (TestRevertReportsSkippedPaths). What CAN be true, and what this test
// pins, is the contract-legal equivalent: the revert of an ingest commit
// names BOTH files — the raw md and its original attachment — in the
// not-invertible record it carries on the reverted journal event and in
// the changeset's Intent, so neither is dropped silently, and it touches
// neither file. The orchestrator owns the call on whether attachment
// removal needs its own stage.
func TestRevertRemovesBoth(t *testing.T) {
	e, dir := newTestEngine(t)
	stageOriginalIngest(t, e)

	commitID, err := e.Commit("ingest a source with its original")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	revertCS, err := e.Revert(commitID)
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}

	skipped := lastRevertedSkipped(t, e)
	if !containsString(skipped, originalRawPath) {
		t.Errorf("revert skipped = %v, want it to name the raw md %s", skipped, originalRawPath)
	}
	if !containsString(skipped, originalPDFPath) {
		t.Errorf("revert skipped = %v, want it to name the original attachment %s", skipped, originalPDFPath)
	}
	for _, p := range allTouchedPaths(revertCS.Ops) {
		if p == originalRawPath || p == originalPDFPath {
			t.Errorf("revert changeset carries an op for %s, which no op kind can invert (D-BY)", p)
		}
	}
	if !strings.Contains(revertCS.Intent, originalPDFPath) {
		t.Errorf("revert Intent = %q, want it to name the original attachment", revertCS.Intent)
	}

	// §5.8 D-BZ: the revert never touched the working tree — both files are
	// exactly where the commit left them.
	for _, p := range []string{originalRawPath, originalPDFPath} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
			t.Errorf("Revert disturbed %s: %v", p, err)
		}
	}
}

// TestMismatchedFrontmatterRefused: Append refuses every way an ingest op's
// original can disagree with itself or with the raw file it proposes —
// so a commit can never write an md whose original/original_sha256
// misdescribes the bytes beside it (the src-integrity error 033 exists to
// prevent, staged instead of caught).
func TestMismatchedFrontmatterRefused(t *testing.T) {
	blob := originalBlob()
	sha := blobSHA(blob)

	open := func(t *testing.T) *Engine {
		t.Helper()
		e, dir := newTestEngine(t)
		t.Cleanup(func() { _ = e.Close() })
		_ = dir
		if _, err := e.OpenChangeset("ingest with original", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		return e
	}

	appendIngest := func(e *Engine, op Op) error {
		t.Helper()
		op.Kind = OpIngestSource
		op.Path = originalRawPath
		op.Extractor = "docling/pdf"
		_, err := e.Append(op)
		return err
	}

	t.Run("frontmatter sha differs from op original", func(t *testing.T) {
		e := open(t)
		err := appendIngest(e, Op{
			Content:         originalRawFile(blob, originalPDFPath, strings.Repeat("cd", 32)),
			OriginalPath:    originalPDFPath,
			Original:        sha,
			OriginalContent: blob,
		})
		if !errors.Is(err, ErrValidation) {
			t.Errorf("Append = %v, want ErrValidation", err)
		}
	})

	t.Run("frontmatter path differs from op original_path", func(t *testing.T) {
		e := open(t)
		err := appendIngest(e, Op{
			Content:         originalRawFile(blob, "raw/papers/elsewhere.pdf", sha),
			OriginalPath:    originalPDFPath,
			Original:        sha,
			OriginalContent: blob,
		})
		if !errors.Is(err, ErrValidation) {
			t.Errorf("Append = %v, want ErrValidation", err)
		}
	})

	t.Run("frontmatter carries no original keys", func(t *testing.T) {
		e := open(t)
		err := appendIngest(e, Op{
			Content:         originalRawFile(blob, "", ""),
			OriginalPath:    originalPDFPath,
			Original:        sha,
			OriginalContent: blob,
		})
		if !errors.Is(err, ErrValidation) {
			t.Errorf("Append = %v, want ErrValidation", err)
		}
	})

	t.Run("content bytes differ from declared sha", func(t *testing.T) {
		e := open(t)
		err := appendIngest(e, Op{
			Content:         originalRawFile(blob, originalPDFPath, sha),
			OriginalPath:    originalPDFPath,
			Original:        sha,
			OriginalContent: append(append([]byte{}, blob[:len(blob)-1]...), 'X'),
		})
		if !errors.Is(err, ErrValidation) {
			t.Errorf("Append = %v, want ErrValidation", err)
		}
	})

	t.Run("original in another directory", func(t *testing.T) {
		e := open(t)
		err := appendIngest(e, Op{
			Content:         originalRawFile(blob, "raw/articles/attention-is-boring.pdf", sha),
			OriginalPath:    "raw/articles/attention-is-boring.pdf",
			Original:        sha,
			OriginalContent: blob,
		})
		if !errors.Is(err, ErrValidation) {
			t.Errorf("Append = %v, want ErrValidation", err)
		}
	})

	t.Run("original is a .md path", func(t *testing.T) {
		e := open(t)
		err := appendIngest(e, Op{
			Content:         originalRawFile(blob, "raw/papers/attention-is-boring.md", sha),
			OriginalPath:    "raw/papers/attention-is-boring.md",
			Original:        sha,
			OriginalContent: blob,
		})
		if !errors.Is(err, ErrValidation) {
			t.Errorf("Append = %v, want ErrValidation", err)
		}
	})

	t.Run("original already exists", func(t *testing.T) {
		e, dir := newTestEngine(t)
		if err := os.MkdirAll(filepath.Join(dir, "raw", "papers"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(originalPDFPath)), blob, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := e.OpenChangeset("ingest with original", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		err := appendIngest(e, Op{
			Content:         originalRawFile(blob, originalPDFPath, sha),
			OriginalPath:    originalPDFPath,
			Original:        sha,
			OriginalContent: blob,
		})
		if !errors.Is(err, ErrValidation) {
			t.Errorf("Append = %v, want ErrValidation", err)
		}
	})

	t.Run("bare ingest without an original still appends", func(t *testing.T) {
		e := open(t)
		err := appendIngest(e, Op{
			Content: originalRawFile(blob, "", ""),
		})
		if err != nil {
			t.Errorf("Append original-less ingest = %v, want it accepted unchanged", err)
		}
	})
}

// TestProjectedLintCleanWithOriginal pins the projection half of the
// feature: from Append through review, a staged ingest with an original
// projects CLEAN under src-integrity — the projected tree carries the
// attachment's bytes, so Checks.Lint cannot report "original missing" for
// a changeset the reviewer is being asked to approve. The same holds for
// a vault that ALREADY committed an attachment: the projection seeds it
// from disk, so no later changeset regresses against a phantom finding.
func TestProjectedLintCleanWithOriginal(t *testing.T) {
	e, dir := newTestEngine(t)
	stageOriginalIngest(t, e)

	report, err := e.ProjectedReport()
	if err != nil {
		t.Fatalf("ProjectedReport: %v", err)
	}
	for _, f := range report.Findings {
		if f.Check == "src-integrity" {
			t.Errorf("projected lint reports src-integrity on a staged ingest with its original: %+v", f)
		}
	}

	if _, err := e.Commit("ingest a source with its original"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// A SECOND changeset over the committed vault must stay clean too.
	if _, err := e.OpenChangeset("a later changeset", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	report, err = e.ProjectedReport()
	if err != nil {
		t.Fatalf("ProjectedReport (second changeset): %v", err)
	}
	for _, f := range report.Findings {
		if f.Check == "src-integrity" {
			t.Errorf("projected lint reports src-integrity for a committed attachment: %+v", f)
		}
	}
	_ = dir
}

// TestOriginalFieldsRoundTripJSON pins the wire format the other lw
// processes depend on: a staged ingest's original_path and original
// serialize into changeset.json (so a fresh Commit process can recover the
// bytes from the CAS), OriginalContent never does, and an original-less
// op's JSON is byte-for-byte what earlier waves wrote — omitempty keeps
// every existing changeset loading and validating.
func TestOriginalFieldsRoundTripJSON(t *testing.T) {
	with := Op{
		ID:              "op1",
		Kind:            OpIngestSource,
		Path:            originalRawPath,
		State:           StateProposed,
		OriginalPath:    originalPDFPath,
		Original:        strings.Repeat("ab", 32),
		OriginalContent: []byte("transient"),
	}
	b, err := json.Marshal(with)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(b), `"original_path":"`+originalPDFPath+`"`) {
		t.Errorf("original_path missing from the wire format:\n%s", b)
	}
	if !strings.Contains(string(b), `"original":"`+strings.Repeat("ab", 32)+`"`) {
		t.Errorf("original missing from the wire format:\n%s", b)
	}
	if strings.Contains(string(b), "transient") {
		t.Errorf("OriginalContent leaked into the wire format:\n%s", b)
	}
	var back Op
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back.OriginalPath != originalPDFPath || back.Original != strings.Repeat("ab", 32) {
		t.Errorf("round-tripped = %q / %q, want the original pair back", back.OriginalPath, back.Original)
	}
	if back.OriginalContent != nil {
		t.Errorf("round-trip materialized OriginalContent %q, want nil", back.OriginalContent)
	}

	without, err := json.Marshal(Op{ID: "op1", Kind: OpIngestSource, Path: originalRawPath, State: StateProposed})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(without), "original") {
		t.Errorf("original-less op grew an original key:\n%s", without)
	}
}

// TestOpDiffShowsOriginal pins the review-display line: OpDiff attaches
// "original (<size>, sha256 <12 hex>): <path>" to the ingest's own
// file entry — display-only, never a hunk — and leaves every other entry
// and every original-less op untouched.
func TestOpDiffShowsOriginal(t *testing.T) {
	e, _ := newTestEngine(t)
	blob := originalBlob()
	stageOriginalIngest(t, e)

	diffs, err := e.OpDiff("op1")
	if err != nil {
		t.Fatalf("OpDiff: %v", err)
	}
	// originalBlob is a fixed 3 KiB: humanSize renders it "3.0 KB"
	// (TestHumanSize pins the rendering itself).
	want := "original (3.0 KB, sha256 " + blobSHA(blob)[:12] + "): " + originalPDFPath

	var found int
	for _, fd := range diffs {
		if fd.Path == originalRawPath {
			found++
			if fd.OriginalLine != want {
				t.Errorf("OriginalLine = %q, want %q", fd.OriginalLine, want)
			}
		} else if fd.OriginalLine != "" {
			t.Errorf("entry %s carries OriginalLine %q, want \"\" on every non-ingest entry", fd.Path, fd.OriginalLine)
		}
	}
	if found != 1 {
		t.Fatalf("OpDiff listed %d entries for %s, want 1", found, originalRawPath)
	}
}

// TestHumanSize pins the size rendering the original line leads with
// (034 T3, A-034-1): binary units — 1 KB is 1024 B, so the number agrees
// with what ls and stat print for the same file — one decimal once the
// byte count stops being readable on its own.
func TestHumanSize(t *testing.T) {
	rows := []struct {
		n    int
		want string
	}{
		{900, "900 B"},
		{1024, "1.0 KB"},
		{3072, "3.0 KB"},
		{1048576, "1.0 MB"},
		{1153434, "1.1 MB"},
	}
	for _, row := range rows {
		if got := humanSize(row.n); got != row.want {
			t.Errorf("humanSize(%d) = %q; want %q", row.n, got, row.want)
		}
	}
}
