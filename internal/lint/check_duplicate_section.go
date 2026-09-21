package lint

import (
	"fmt"
	"strings"
)

// duplicateSectionCheck is check 16, duplicate-section (020).
type duplicateSectionCheck struct{}

func newDuplicateSection() Check { return duplicateSectionCheck{} }

func (duplicateSectionCheck) ID() string { return "duplicate-section" }
func (duplicateSectionCheck) Describe() string {
	return "a wiki/ page carries two body sections whose headings normalize to the same slug"
}
func (duplicateSectionCheck) Severity() Severity { return SevWarn }

// Run reports one finding per slug carried by two or more Level >= 2 sections
// of the same wiki page, naming the slug and every occurrence's body line
// number (020 T-C, from 014 §9 A10 — a bulk `lw lint --fix` committed 16
// pages with DUPLICATE ## Abstract sections because each patch_page was
// computed from the committed page, so the old abstract was never removed;
// duplicate headings were invisible to every check that existed, which is
// how the run went QUIETER, 18→3 warns, while landing the damage). The slug
// is the normalized heading (internal/vault/section.go), so "## Abstract"
// and "## abstract" collide; line numbers are 1-based in Page.Body, the
// same convention Wikilink.Line uses. Raw sources load as RawSources,
// never Pages, so raw/** is structurally out of reach.
//
// Level-1 headings are excluded: "# Notes" above "## Notes" is
// unusual-but-legal old-note shape, not the chimera class — only two body
// sections (Level >= 2) sharing a slug are. A page with three copies of one
// slug earns one finding naming all three lines, not three findings.
func (duplicateSectionCheck) Run(ctx *Context) []Finding {
	var findings []Finding
	for _, p := range ctx.Vault.Pages() {
		lines := map[string][]int{} // slug -> heading body lines, document order
		for _, sec := range p.Sections {
			if sec.Level < 2 {
				continue
			}
			lines[sec.Slug] = append(lines[sec.Slug], sectionLine(p.Body, sec.Start))
		}
		reported := map[string]bool{}
		for _, sec := range p.Sections { // document order, so findings are too
			if reported[sec.Slug] || len(lines[sec.Slug]) < 2 {
				continue
			}
			reported[sec.Slug] = true
			at := make([]string, len(lines[sec.Slug]))
			for i, line := range lines[sec.Slug] {
				at[i] = fmt.Sprintf("line %d", line)
			}
			findings = append(findings, Finding{
				Check:    "duplicate-section",
				Path:     p.Path,
				Line:     lines[sec.Slug][1],
				Severity: SevWarn,
				Message: fmt.Sprintf(
					"duplicate section %q: headings at %s; remove or merge the extra copy",
					sec.Slug, strings.Join(at, ", ")),
				Fixable: true, // remove_section / a merge patch deletes the copy
			})
		}
	}
	return findings
}

// sectionLine returns the 1-based line number in body of the line starting
// at byte offset start — the same body-relative convention Wikilink.Line
// uses, computed from Section.Start because Section carries byte offsets,
// not lines.
func sectionLine(body string, start int) int {
	return 1 + strings.Count(body[:start], "\n")
}
