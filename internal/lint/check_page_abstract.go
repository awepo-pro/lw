package lint

import "fmt"

// pageAbstractCheck is check 15, page-abstract (014).
type pageAbstractCheck struct{}

func newPageAbstract() Check { return pageAbstractCheck{} }

func (pageAbstractCheck) ID() string { return "page-abstract" }
func (pageAbstractCheck) Describe() string {
	return "a wiki/ page carries no ## Abstract section, or does not open with one"
}
func (pageAbstractCheck) Severity() Severity { return SevWarn }

// Run reports two mutually exclusive findings per wiki page: a page whose
// parsed sections have none with Slug "abstract" earns the missing finding;
// a page that has one but does not open its body with it earns the placement
// finding (v2.5.1 fix wave, §9 A9 — placement became lint's concern once real
// vaults carried mid-page abstracts that --fix never repaired; still warn,
// the error flip stays queued). The slug is the normalized heading
// (internal/vault/section.go), so "## Abstract", "## abstract" and
// "## ABSTRACT" all match. Raw sources load as RawSources, never Pages, so
// raw/** is structurally out of reach.
//
// Placement compares against the first section of Level >= 2, the page's
// first body section. Level-1 headings — the page's "# Title", and any other
// single-# heading — and preamble prose before the abstract are legal: 014
// §5 asks for the abstract "before any other section", and the fixtures'
// shape is "# Title" + a lead paragraph + "## Abstract". A page whose
// sections are all Level 1 has no body section to name, so it cannot earn
// the placement finding.
func (pageAbstractCheck) Run(ctx *Context) []Finding {
	var findings []Finding
	for _, p := range ctx.Vault.Pages() {
		abstractAt := -1 // index of the abstract section, if any
		for i, sec := range p.Sections {
			if sec.Slug == "abstract" {
				abstractAt = i
				break
			}
		}
		if abstractAt == -1 {
			findings = append(findings, Finding{
				Check:    "page-abstract",
				Path:     p.Path,
				Severity: SevWarn,
				Message:  "no ## Abstract section; open the page with a 2-4 sentence summary",
				Fixable:  true, // an agent can draft the summary; parity with path-convention
			})
			continue
		}
		first := -1 // index of the first Level >= 2 section, the first body section
		for i, sec := range p.Sections {
			if sec.Level >= 2 {
				first = i
				break
			}
		}
		if first != -1 && first != abstractAt {
			findings = append(findings, Finding{
				Check:    "page-abstract",
				Path:     p.Path,
				Severity: SevWarn,
				Message:  fmt.Sprintf(`## Abstract must be the page's first section; move it above %q`, p.Sections[first].Heading),
				Fixable:  true, // insert_before + remove_section move it; the prompt rule names the ops
			})
		}
	}
	return findings
}
