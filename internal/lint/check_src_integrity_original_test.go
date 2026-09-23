package lint_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/vault"
)

// originalTestPDF is a fake original binary whose sha is computed, not
// hardcoded, so the test states the relationship rather than a magic hex.
var originalTestPDF = []byte("%PDF-1.6 fake pdf for TestSrcIntegrityOriginal\n" + strings.Repeat("x", 512))

func originalTestPDFSHA() string {
	sum := sha256.Sum256(originalTestPDF)
	return hex.EncodeToString(sum[:])
}

// originalRawFixture renders a raw/ md declaring an original pair. An empty
// sha omits the original_sha256 line, mirroring Serialize's only-when-set.
// The body sha is correct, so the only findings the fixtures can produce
// are the original pair's.
func originalRawFixture(origSHA string) string {
	const body = "# Attached Source\n\nBody.\n"
	s := "---\n" +
		"source_url: https://example.org/attached\n" +
		"ingested: 2026-09-24\n" +
		"sha256: " + vault.BodySHA256(body) + "\n"
	if origSHA != "" {
		s += "original: raw/papers/attached-source.pdf\n" +
			"original_sha256: " + origSHA + "\n"
	}
	s += "---\n\n" + body
	return s
}

// TestSrcIntegrityOriginal covers the 033 extension: a raw whose
// frontmatter sets original/original_sha256 must have that file present
// and hashing to the recorded sha. missing and modified are SevErrors
// attributed to the raw file; intact reports nothing.
func TestSrcIntegrityOriginal(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		ctx := buildVault(t, map[string]string{
			"raw/papers/attached-source.md": originalRawFixture(originalTestPDFSHA()),
		})
		report := lint.Run(ctx, []string{"src-integrity"})
		if len(report.Findings) != 1 {
			t.Fatalf("src-integrity produced %d findings, want 1: %+v", len(report.Findings), report.Findings)
		}
		f := report.Findings[0]
		if f.Path != "raw/papers/attached-source.md" || f.Severity != lint.SevError {
			t.Errorf("finding = %+v, want a SevError on the raw file", f)
		}
		if want := "original raw/papers/attached-source.pdf is missing"; f.Message != want {
			t.Errorf("Message = %q, want %q", f.Message, want)
		}
	})

	t.Run("modified", func(t *testing.T) {
		ctx := buildVault(t, map[string]string{
			"raw/papers/attached-source.md":  originalRawFixture(originalTestPDFSHA()),
			"raw/papers/attached-source.pdf": string(append(append([]byte{}, originalTestPDF[:10]...), []byte("TAMPERED")...)),
		})
		report := lint.Run(ctx, []string{"src-integrity"})
		if len(report.Findings) != 1 {
			t.Fatalf("src-integrity produced %d findings, want 1: %+v", len(report.Findings), report.Findings)
		}
		f := report.Findings[0]
		if f.Path != "raw/papers/attached-source.md" || f.Severity != lint.SevError {
			t.Errorf("finding = %+v, want a SevError on the raw file", f)
		}
		if want := "original raw/papers/attached-source.pdf does not match original_sha256"; f.Message != want {
			t.Errorf("Message = %q, want %q", f.Message, want)
		}
	})

	t.Run("intact", func(t *testing.T) {
		ctx := buildVault(t, map[string]string{
			"raw/papers/attached-source.md":  originalRawFixture(originalTestPDFSHA()),
			"raw/papers/attached-source.pdf": string(originalTestPDF),
		})
		report := lint.Run(ctx, []string{"src-integrity"})
		if len(report.Findings) != 0 {
			t.Errorf("src-integrity reported an intact original: %+v", report.Findings)
		}
	})

	t.Run("no original no findings", func(t *testing.T) {
		ctx := buildVault(t, map[string]string{
			"raw/papers/attached-source.md": originalRawFixture(""),
		})
		report := lint.Run(ctx, []string{"src-integrity"})
		if len(report.Findings) != 0 {
			t.Errorf("src-integrity reported an original-less raw: %+v", report.Findings)
		}
	})
}
