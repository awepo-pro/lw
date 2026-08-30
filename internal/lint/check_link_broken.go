package lint

import "fmt"

// linkBrokenCheck is check 5, link-broken.
type linkBrokenCheck struct{}

func newLinkBroken() Check { return linkBrokenCheck{} }

func (linkBrokenCheck) ID() string         { return "link-broken" }
func (linkBrokenCheck) Describe() string   { return "a [[wikilink]] resolves to nothing" }
func (linkBrokenCheck) Severity() Severity { return SevError }

// Run reports every wikilink whose target does not resolve to a page.
// Context.Graph.Broken already returns these sorted by the linking page's
// Path, then by line — the one check in the suite with a real line number,
// since Wikilink.Line (backbone §2.5) survives into vault.Ref.Line.
func (linkBrokenCheck) Run(ctx *Context) []Finding {
	var findings []Finding
	for _, ref := range ctx.Graph.Broken() {
		findings = append(findings, Finding{
			Check:    "link-broken",
			Path:     ref.From,
			Line:     ref.Line,
			Severity: SevError,
			Message:  fmt.Sprintf("[[%s]] resolves to nothing; fix the target or create the page", ref.Raw),
		})
	}
	return findings
}
