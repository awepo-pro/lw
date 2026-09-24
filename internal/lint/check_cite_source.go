package lint

import (
	"fmt"
	"strings"

	"github.com/awepo-pro/lw/internal/cite"
)

// citeSourceCheck is check 18, cite-source (034 T2). Closes the 021 item,
// seen live twice: a body cite of a raw source the frontmatter never lists
// was invisible to every check — src-provenance walks sources:, so it only
// catches the listed-but-never-cited direction.
type citeSourceCheck struct{}

func newCiteSource() Check { return citeSourceCheck{} }

func (citeSourceCheck) ID() string { return "cite-source" }
func (citeSourceCheck) Describe() string {
	return "a wiki page's body cites a raw/ source its sources: list does not include"
}
func (citeSourceCheck) Severity() Severity { return SevWarn }

// Run reports one finding per distinct raw/ source cited in a page's body
// but missing from its sources: — the mirror image of src-provenance. wiki/
// cites are excluded: pointing at another page is a wikilink's job and
// carries no provenance. A source cited three times earns one finding, at
// its first marker's line, because the fix is one frontmatter edit, not one
// per claim. (034 T2.)
func (citeSourceCheck) Run(ctx *Context) []Finding {
	var findings []Finding
	for _, p := range ctx.Vault.Pages() {
		listed := make(map[string]bool, len(p.FM.Sources))
		for _, src := range p.FM.Sources {
			listed[src] = true
		}

		firstLine := map[string]int{} // unlisted source -> its first marker's body line
		var order []string            // distinct unlisted sources, document order
		for _, c := range cite.Scan(p.Body) {
			if !strings.HasPrefix(c.Source, "raw/") || listed[c.Source] {
				continue
			}
			if _, seen := firstLine[c.Source]; !seen {
				firstLine[c.Source] = sectionLine(p.Body, c.Offset)
				order = append(order, c.Source)
			}
		}

		for _, src := range order {
			findings = append(findings, Finding{
				Check:    "cite-source",
				Path:     p.Path,
				Line:     firstLine[src],
				Severity: SevWarn,
				Message: fmt.Sprintf(
					"cites %s in the body but sources: does not list it; add it to sources:", src),
			})
		}
	}
	return findings
}
