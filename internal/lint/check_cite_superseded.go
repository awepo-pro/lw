package lint

import (
	"fmt"
	"strings"

	"github.com/awepo-pro/lw/internal/cite"
)

// citeSupersededCheck is check 19, cite-superseded (070 S2).
// Reports wiki pages citing raw sources that have been superseded by newer
// revisions (L1), and raw sources whose supersedes: frontmatter key names
// a path that is not a committed raw in the vault (L2).
type citeSupersededCheck struct{}

func newCiteSuperseded() Check { return citeSupersededCheck{} }

func (citeSupersededCheck) ID() string { return "cite-superseded" }
func (citeSupersededCheck) Describe() string {
	return "a page cites a superseded raw source, or a raw names a missing or non-raw path in supersedes:"
}
func (citeSupersededCheck) Severity() Severity { return SevWarn }

// Run reports:
// L1: one finding per superseded raw cited by a wiki page in sources: or the body,
// advising re-pointing to the successor raw.
// L2: one finding per raw whose supersedes: key names a missing or non-raw path.
func (citeSupersededCheck) Run(ctx *Context) []Finding {
	if ctx == nil || ctx.Vault == nil {
		return nil
	}

	var findings []Finding

	// L1: Check pages citing superseded raws.
	for _, p := range ctx.Vault.Pages() {
		seen := make(map[string]bool)

		// Check sources: list in frontmatter.
		for _, src := range p.FM.Sources {
			if seen[src] {
				continue
			}
			if _, exists := ctx.Vault.RawSource(src); !exists {
				continue
			}
			if successor, ok := ctx.Vault.SupersededBy(src); ok {
				seen[src] = true
				findings = append(findings, Finding{
					Check:    "cite-superseded",
					Path:     p.Path,
					Severity: SevWarn,
					Message: fmt.Sprintf(
						"cites %s in sources:, superseded by %s; re-point it to the new raw",
						src, successor),
				})
			}
		}

		// Check body citations.
		for _, c := range cite.Scan(p.Body) {
			if !strings.HasPrefix(c.Source, "raw/") {
				continue
			}
			if seen[c.Source] {
				continue
			}
			if _, exists := ctx.Vault.RawSource(c.Source); !exists {
				continue
			}
			if successor, ok := ctx.Vault.SupersededBy(c.Source); ok {
				seen[c.Source] = true
				findings = append(findings, Finding{
					Check:    "cite-superseded",
					Path:     p.Path,
					Severity: SevWarn,
					Message: fmt.Sprintf(
						"cites %s in the body, superseded by %s; re-point it to the new raw",
						c.Source, successor),
				})
			}
		}
	}

	// L2: Check raws whose supersedes: names a missing/non-raw path.
	for _, r := range ctx.Vault.RawSources() {
		if r.Supersedes == "" {
			continue
		}
		if _, ok := ctx.Vault.RawSource(r.Supersedes); !ok {
			findings = append(findings, Finding{
				Check:    "cite-superseded",
				Path:     r.Path,
				Severity: SevWarn,
				Message: fmt.Sprintf(
					"supersedes names %s, which is not a committed raw",
					r.Supersedes),
			})
		}
	}

	return findings
}
