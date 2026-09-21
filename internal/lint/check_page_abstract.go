package lint

// pageAbstractCheck is check 15, page-abstract (014).
type pageAbstractCheck struct{}

func newPageAbstract() Check { return pageAbstractCheck{} }

func (pageAbstractCheck) ID() string { return "page-abstract" }
func (pageAbstractCheck) Describe() string {
	return "a wiki/ page carries no ## Abstract section"
}
func (pageAbstractCheck) Severity() Severity { return SevWarn }

// Run reports every wiki page whose parsed sections have none with Slug
// "abstract". The slug is the normalized heading (internal/vault/section.go),
// so "## Abstract", "## abstract" and "## ABSTRACT" all match. Placement is
// deliberately not enforced here — the prompt rule (TS-14A) asks for the
// abstract first; lint only asks that one exists. Raw sources load as
// RawSources, never Pages, so raw/** is structurally out of reach.
func (pageAbstractCheck) Run(ctx *Context) []Finding {
	var findings []Finding
	for _, p := range ctx.Vault.Pages() {
		hasAbstract := false
		for _, sec := range p.Sections {
			if sec.Slug == "abstract" {
				hasAbstract = true
				break
			}
		}
		if hasAbstract {
			continue
		}
		findings = append(findings, Finding{
			Check:    "page-abstract",
			Path:     p.Path,
			Severity: SevWarn,
			Message:  "no ## Abstract section; open the page with a 2-4 sentence summary",
		})
	}
	return findings
}
