package lint

import "fmt"

// fmTaxonomyCheck is check 2, fm-taxonomy.
type fmTaxonomyCheck struct{}

func newFMTaxonomy() Check { return fmTaxonomyCheck{} }

func (fmTaxonomyCheck) ID() string         { return "fm-taxonomy" }
func (fmTaxonomyCheck) Describe() string   { return "a tag is not in SCHEMA.md's taxonomy" }
func (fmTaxonomyCheck) Severity() Severity { return SevError }

// Run reports one finding per tag, on any page, that Context.Vault.Schema
// does not recognize. Context.Vault.Pages is already sorted by Path, and a
// page's Tags are walked in their declared order, so the result is
// deterministic without an explicit sort here.
func (fmTaxonomyCheck) Run(ctx *Context) []Finding {
	schema := ctx.Vault.Schema()

	var findings []Finding
	for _, p := range ctx.Vault.Pages() {
		for _, tag := range p.FM.Tags {
			if schema.HasTag(tag) {
				continue
			}
			findings = append(findings, Finding{
				Check:    "fm-taxonomy",
				Path:     p.Path,
				Severity: SevError,
				Message:  fmt.Sprintf("tag `%s` is not in SCHEMA.md; add it to the taxonomy or retag", tag),
				Fixable:  true,
			})
		}
	}
	return findings
}
