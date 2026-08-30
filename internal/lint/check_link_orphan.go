package lint

import "strings"

// linkOrphanCheck is check 7, link-orphan.
type linkOrphanCheck struct{}

func newLinkOrphan() Check { return linkOrphanCheck{} }

func (linkOrphanCheck) ID() string         { return "link-orphan" }
func (linkOrphanCheck) Describe() string   { return "no inbound links" }
func (linkOrphanCheck) Severity() Severity { return SevWarn }

// linkOrphanExempt lists the vault-wide files that are never a page (they
// are not under wiki/, so BuildGraph never sees them per backbone §2.9
// correction C-7) but that a human might still expect this check to leave
// alone if they ever were.
var linkOrphanExempt = map[string]bool{
	"index.md":          true,
	"SCHEMA.md":         true,
	"log.md":            true,
	"curator-memory.md": true,
}

// Run reports every page with no inbound wikilink, per Context.Graph.
// Orphans, excluding the vault-wide files and every page under
// wiki/queries/. Because BuildGraph walks Vault.Pages() only (backbone
// §2.9, correction C-7), a page merely listed in index.md is still an
// orphan if no *page* links to it — index.md's own [[links]] contribute no
// graph edges.
func (linkOrphanCheck) Run(ctx *Context) []Finding {
	var findings []Finding
	for _, page := range ctx.Graph.Orphans() {
		if isLinkOrphanExempt(page) {
			continue
		}
		findings = append(findings, Finding{
			Check:    "link-orphan",
			Path:     page,
			Severity: SevWarn,
			Message:  "no inbound links; link to it from a related page or retract it",
		})
	}
	return findings
}

// isLinkOrphanExempt reports whether page is exempt from link-orphan: one
// of the fixed vault-wide files, or anything under wiki/queries/.
func isLinkOrphanExempt(page string) bool {
	if linkOrphanExempt[page] {
		return true
	}
	return strings.HasPrefix(page, "wiki/queries/")
}
