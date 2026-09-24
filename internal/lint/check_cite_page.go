package lint

import (
	"fmt"

	"github.com/awepo-pro/lw/internal/cite"
)

// citePageCheck is check 17, cite-page (034 T2).
type citePageCheck struct{}

func newCitePage() Check { return citePageCheck{} }

func (citePageCheck) ID() string { return "cite-page" }
func (citePageCheck) Describe() string {
	return "a paged ^[raw/... p.N] marker is malformed or names a page the raw source has no anchor for"
}
func (citePageCheck) Severity() Severity { return SevWarn }

// Run reports every defect a paged provenance marker can carry: a page
// number the cite grammar rejects, a raw source missing from the vault, a
// source whose body has no `<!-- page N -->` anchors, and a page past the
// last anchor the source actually has. The grammar and the anchor reading
// both come from internal/cite, so lint splits a marker exactly the way
// ask display and stage do (034 T2).
//
// Unpaged markers are out of scope: 034 only ever ADDS the page suffix, so
// a bare ^[raw/x.md] written before it must not start firing (legacy
// scope). One marker yields at most one finding — the first defect in the
// chain above is the reason the marker says nothing, and the later ones
// would only restate it.
func (citePageCheck) Run(ctx *Context) []Finding {
	var findings []Finding
	// pages memoizes cite.Pages per source: one wiki page routinely cites
	// the same raw a dozen times, and splitting a 300 KB extracted PDF once
	// per cite instead of once per Run is the difference between linting a
	// vault of ~100 ingested papers in milliseconds and re-splitting all of
	// them for every marker that names them (034 T2 review).
	anchored := map[string][]int{}
	for _, p := range ctx.Vault.Pages() {
		for _, c := range cite.Scan(p.Body) {
			f := Finding{
				Check:    "cite-page",
				Path:     p.Path,
				Line:     sectionLine(p.Body, c.Offset),
				Severity: SevWarn,
			}
			if c.Err != "" {
				f.Message = c.Raw + ": " + c.Err
				findings = append(findings, f)
				continue
			}
			if c.From == 0 {
				continue // the legacy unpaged form — never retro-flagged
			}
			rs, ok := ctx.Vault.RawSource(c.Source)
			if !ok {
				f.Message = fmt.Sprintf("%s: %s does not exist", c.Raw, c.Source)
				findings = append(findings, f)
				continue
			}
			pages, ok := anchored[c.Source]
			if !ok {
				pages = cite.Pages(rs.Body)
				anchored[c.Source] = pages
			}
			if len(pages) == 0 {
				f.Message = fmt.Sprintf(
					"%s: %s has no page anchors; cite it without a page", c.Raw, c.Source)
				findings = append(findings, f)
				continue
			}
			max := pages[0]
			for _, n := range pages[1:] {
				if n > max {
					max = n
				}
			}
			if c.To > max {
				f.Message = fmt.Sprintf("%s: %s has pages 1-%d", c.Raw, c.Source, max)
				findings = append(findings, f)
			}
		}
	}
	return findings
}
