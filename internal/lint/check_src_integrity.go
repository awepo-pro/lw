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

// Run reports two distinct src-integrity defects. A sources: entry naming
// a raw/ path that does not exist is attributed to the citing page, since
// the defect is that page's dangling citation. A raw source whose stored
// body no longer hashes to its own frontmatter sha256 is attributed to
// the raw file itself (EXPECTED-LINT.md's attribution note) — the defect
// is a property of that file, independent of who cites it, so it is
// reported once even when several pages cite it. BodySHA256's exact
// definition is backbone §2.7 (S1 correction C-2).
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
		if vault.BodySHA256(r.Body) == r.SHA256 {
			continue
		}
		findings = append(findings, Finding{
			Check:    "src-integrity",
			Path:     r.Path,
			Severity: SevError,
			Message:  "body sha256 does not match frontmatter sha256; re-ingest to refresh the hash",
		})
	}

	sort.Slice(findings, func(i, j int) bool { return findings[i].Path < findings[j].Path })
	return findings
}
