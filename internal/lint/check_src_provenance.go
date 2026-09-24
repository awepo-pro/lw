package lint

import (
	"fmt"
	"sort"

	"github.com/awepo-pro/lw/internal/cite"
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
// carries the matching provenance marker. 034 T2: detection goes through
// cite.Scan, so a paged marker — ^[raw/x.md p.12], the form 034's PDF
// ingest makes worth writing — marks its source, while a marker inside a
// code fence or span stops counting (an example, not a claim's
// provenance). The old substring match did the first job wrong and could
// not do the second at all. A page can cite several sources; each missing
// marker is its own finding, naming the exact source and the exact marker
// text to add.
func (srcProvenanceCheck) Run(ctx *Context) []Finding {
	var findings []Finding
	for _, p := range ctx.Vault.Pages() {
		marked := map[string]bool{}
		for _, c := range cite.Scan(p.Body) {
			marked[c.Source] = true
		}
		for _, src := range p.FM.Sources {
			if marked[src] {
				continue
			}
			marker := "^[" + src + "]"
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
