package lint

import "fmt"

// linkMinOutCheck is check 6, link-min-out.
type linkMinOutCheck struct{}

func newLinkMinOut() Check { return linkMinOutCheck{} }

func (linkMinOutCheck) ID() string         { return "link-min-out" }
func (linkMinOutCheck) Describe() string   { return "fewer than 2 outbound wikilinks" }
func (linkMinOutCheck) Severity() Severity { return SevWarn }

// Run reports every page with fewer than two outbound wikilinks
// (/PLAN.md §6). Context.Graph.Outbound counts every wikilink a page
// carries, resolved or not, matching what a human reading the page would
// call its "outbound wikilinks".
func (linkMinOutCheck) Run(ctx *Context) []Finding {
	var findings []Finding
	for _, p := range ctx.Vault.Pages() {
		n := len(ctx.Graph.Outbound(p.Path))
		if n >= 2 {
			continue
		}
		plural := "s"
		if n == 1 {
			plural = ""
		}
		findings = append(findings, Finding{
			Check:    "link-min-out",
			Path:     p.Path,
			Severity: SevWarn,
			Message:  fmt.Sprintf("only %d outbound wikilink%s; add at least one more", n, plural),
		})
	}
	return findings
}
