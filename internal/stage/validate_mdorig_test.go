package stage

// validate_mdorig_test.go pins 058's D3: validateIngestOriginal accepts a
// markdown source's original under the one name it can have — x.md.orig,
// since x.md is the raw itself — and keeps every other original-naming
// refusal, text and all, exactly as 033 wrote it.

import (
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/vault"
)

// mdOrigOp is an ingest_source op for raw/articles/x.md whose original sits
// at originalPath, with a raw file whose frontmatter declares that same pair —
// everything validateIngestOriginal checks BEFORE the naming rules agrees,
// so a refusal can only come from the naming rule under test.
func mdOrigOp(originalPath string) Op {
	body := "# X\n\nThe extracted body.\n"
	blob := []byte("# X\r\n\r\nThe extracted body.\r\n")
	sha := blobSHA(blob)
	var b strings.Builder
	b.WriteString("---\nsource_url: /home/user/notes/x.md\ningested: 2026-10-15\n")
	b.WriteString("sha256: " + vault.BodySHA256(body) + "\n")
	b.WriteString("original: " + originalPath + "\n")
	b.WriteString("original_sha256: " + sha + "\n")
	b.WriteString("---\n\n" + body)
	return Op{
		Kind:            OpIngestSource,
		Path:            "raw/articles/x.md",
		Content:         []byte(b.String()),
		Extractor:       "passthrough",
		OriginalPath:    originalPath,
		Original:        sha,
		OriginalContent: blob,
	}
}

// TestValidateMdOrigAccepted: raw/articles/x.md + raw/articles/x.md.orig is a
// valid pair.
func TestValidateMdOrigAccepted(t *testing.T) {
	v := newTestVault(t)
	if err := ValidateOp(mdOrigOp("raw/articles/x.md.orig"), v, v.Schema()); err != nil {
		t.Fatalf("ValidateOp(x.md + x.md.orig) = %v, want it accepted", err)
	}
}

// TestValidateMdOriginalStillRefused: x.md as the original of x.md is the
// collision 033 refused, and the refusal text is the one 033 shipped.
func TestValidateMdOriginalStillRefused(t *testing.T) {
	v := newTestVault(t)
	err := ValidateOp(mdOrigOp("raw/articles/x.md"), v, v.Schema())
	wantValidationError(t, err)
	const want = "stage: op failed validation: ingest_source: original_path raw/articles/x.md must carry a non-.md extension"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

// TestValidateOrigForOtherNameRefused: a .md.orig that belongs to a
// DIFFERENT raw is refused with the base-name text 033 shipped — the
// .md.orig rule matches exactly "<this raw's base name>.orig", not any
// name ending in .orig.
func TestValidateOrigForOtherNameRefused(t *testing.T) {
	v := newTestVault(t)
	for _, other := range []string{
		"raw/articles/y.md.orig",
		"raw/articles/x.md.orig.orig",
		"raw/articles/x.md.ORIG",
		"raw/articles/xx.md.orig",
		"raw/articles/.md.orig",
	} {
		t.Run(other, func(t *testing.T) {
			err := ValidateOp(mdOrigOp(other), v, v.Schema())
			wantValidationError(t, err)
			want := "stage: op failed validation: ingest_source: original_path " + other + " must be x.md's base name with that extension"
			if err.Error() != want {
				t.Errorf("error = %q, want %q", err.Error(), want)
			}
		})
	}

	t.Run("directory rule unchanged", func(t *testing.T) {
		err := ValidateOp(mdOrigOp("raw/papers/x.md.orig"), v, v.Schema())
		wantValidationError(t, err)
		const want = "stage: op failed validation: ingest_source: original_path raw/papers/x.md.orig must sit in raw/articles, beside the raw source"
		if err.Error() != want {
			t.Errorf("error = %q, want %q", err.Error(), want)
		}
	})
}
