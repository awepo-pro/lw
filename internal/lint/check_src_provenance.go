package lint

import (
	"fmt"
	"sort"
	"strings"
)

// srcProvenanceCheck is check 9, src-provenance.
type srcProvenanceCheck struct{}

func newSrcProvenance() Check { return srcProvenanceCheck{} }

func (srcProvenanceCheck) ID() string { return "src-provenance" }
func (srcProvenanceCheck) Describe() string {
	return "a page with sources: carries no ^[raw/...] provenance marker"
}
func (srcProvenanceCheck) Severity() Severity { return SevWarn }

// Run reports, for each source a page cites, whether the page's body
// carries the matching "^[<source>]" provenance marker. A page can cite
// several sources; each missing marker is its own finding, naming the
// exact source and the exact marker text to add.
func (srcProvenanceCheck) Run(ctx *Context) []Finding {
	var findings []Finding
	for _, p := range ctx.Vault.Pages() {
		for _, src := range p.FM.Sources {
			marker := "^[" + src + "]"
			if strings.Contains(p.Body, marker) {
				continue
			}
			findings = append(findings, Finding{
				Check:    "src-provenance",
				Path:     p.Path,
				Severity: SevWarn,
				Message: fmt.Sprintf(
					"cites %s but has no %s marker; add one or drop the source", src, marker),
			})
		}
	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].Path < findings[j].Path })
	return findings
}
