package lint

import (
	"fmt"

	"github.com/awepo-pro/lw/internal/vault"
)

// bodyFrontmatterCheck is check 20, body-frontmatter (081 FIX-C).
type bodyFrontmatterCheck struct{}

func newBodyFrontmatter() Check { return bodyFrontmatterCheck{} }

func (bodyFrontmatterCheck) ID() string { return "body-frontmatter" }

func (bodyFrontmatterCheck) Describe() string {
	return `a wiki/ page body carries a frontmatter block (a "---" line followed by a frontmatter key)`
}

func (bodyFrontmatterCheck) Severity() Severity { return SevWarn }

// Run reports one finding per page whose Body contains a frontmatter block
// (FC.2, FC.3). Line is the 1-based line number of the block's "---" line in
// Page.Body. Raw sources load as RawSources, never Pages, so raw/** is
// structurally never checked.
func (bodyFrontmatterCheck) Run(ctx *Context) []Finding {
	var findings []Finding
	for _, p := range ctx.Vault.Pages() {
		if key, line, ok := vault.FindFrontmatterBlock(p.Body); ok {
			findings = append(findings, Finding{
				Check:    "body-frontmatter",
				Path:     p.Path,
				Line:     line,
				Severity: SevWarn,
				Message: fmt.Sprintf(
					"body contains a frontmatter block at line %d (\"---\" then %q); the page's real frontmatter is the one above, so delete this copy",
					line, key+":"),
				Fixable: true,
			})
		}
	}
	return findings
}
