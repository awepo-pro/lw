package lint

import (
	"fmt"
	"sort"

	"github.com/awepo-pro/lw/internal/vault"
)

// srcIntegrityCheck is check 8, src-integrity.
type srcIntegrityCheck struct{}

func newSrcIntegrity() Check { return srcIntegrityCheck{} }

func (srcIntegrityCheck) ID() string { return "src-integrity" }
func (srcIntegrityCheck) Describe() string {
	return "a sources: entry is missing from raw/, or its body sha256 differs from the frontmatter sha256 (drift)"
}
func (srcIntegrityCheck) Severity() Severity { return SevError }

// Run reports src-integrity defects. A sources: entry naming a raw/ path
// that does not exist is attributed to the citing page, since the defect
// is that page's dangling citation. The other three are properties of the
// raw file itself (EXPECTED-LINT.md's attribution note) — body drift, a
// missing original, and an original whose bytes no longer hash to the
// recorded original_sha256 (033) — so each is reported once, on the raw
// file, even when several pages cite it. BodySHA256's exact definition is
// backbone §2.7 (S1 correction C-2).
func (srcIntegrityCheck) Run(ctx *Context) []Finding {
	var findings []Finding

	for _, p := range ctx.Vault.Pages() {
		for _, src := range p.FM.Sources {
			if _, ok := ctx.Vault.RawSource(src); ok {
				continue
			}
			findings = append(findings, Finding{
				Check:    "src-integrity",
				Path:     p.Path,
				Severity: SevError,
				Message: fmt.Sprintf(
					"sources entry %s not found under raw/; ingest it or drop the citation", src),
			})
		}
	}

	for _, r := range ctx.Vault.RawSources() {
		if vault.BodySHA256(r.Body) != r.SHA256 {
			findings = append(findings, Finding{
				Check:    "src-integrity",
				Path:     r.Path,
				Severity: SevError,
				Message:  "body sha256 does not match frontmatter sha256; re-ingest to refresh the hash",
			})
		}

		// 033: the original is the ground truth the extracted text is
		// checked against, so its absence or drift is an error on the raw
		// file, not a warning. The hash comes from AttachmentSHA256 — for
		// a disk vault that is the persistent stat cache (033 scaling
		// fix), so lint costs one stat per unchanged original, not one
		// multi-MB read. A file whose hash cannot be resolved is reported
		// as missing — from this check's vantage there is no readable
		// original at the declared path.
		if r.Original == "" {
			continue
		}
		sha, err := ctx.Vault.AttachmentSHA256(r.Original)
		switch {
		case err != nil:
			findings = append(findings, Finding{
				Check:    "src-integrity",
				Path:     r.Path,
				Severity: SevError,
				Message:  fmt.Sprintf("original %s is missing", r.Original),
			})
		case sha != r.OriginalSHA256:
			findings = append(findings, Finding{
				Check:    "src-integrity",
				Path:     r.Path,
				Severity: SevError,
				Message:  fmt.Sprintf("original %s does not match original_sha256", r.Original),
			})
		}
	}

	sort.Slice(findings, func(i, j int) bool { return findings[i].Path < findings[j].Path })
	return findings
}
