package lint

import (
	"fmt"
	"time"
)

// srcStaleCheck is check 13, src-stale.
type srcStaleCheck struct{}

func newSrcStale() Check { return srcStaleCheck{} }

func (srcStaleCheck) ID() string { return "src-stale" }
func (srcStaleCheck) Describe() string {
	return "a page's updated is more than 90 days earlier than the ingested date of a source it cites"
}
func (srcStaleCheck) Severity() Severity { return SevWarn }

// srcStaleThreshold is the staleness window (backbone §4, Hermes:
// "Pages whose updated date is >90 days older than the most recent source
// that mentions the same entities" — narrowed to the page's own declared
// sources:, since "mentions the same entities" needs judgement and this
// does not).
const srcStaleThreshold = 90 * 24 * time.Hour

// Run reports a page whose updated date is more than 90 days earlier than
// the ingested date of a source it cites. Both dates come from the vault
// — never time.Now() (backbone §4's determinism contract), so the golden
// cannot rot. Attributed to the page, not the source: the source is fine,
// it is the page that has not been revisited. A page can cite several
// sources; the first stale one found, in the page's declared order, is
// reported.
func (srcStaleCheck) Run(ctx *Context) []Finding {
	var findings []Finding
	for _, p := range ctx.Vault.Pages() {
		for _, src := range p.FM.Sources {
			r, ok := ctx.Vault.RawSource(src)
			if !ok {
				continue
			}
			gap := r.Ingested.Time.Sub(p.FM.Updated.Time)
			if gap <= srcStaleThreshold {
				continue
			}
			findings = append(findings, Finding{
				Check:    "src-stale",
				Path:     p.Path,
				Severity: SevWarn,
				Message: fmt.Sprintf(
					"updated %s is more than 90 days before %s was ingested %s; review the page against its source",
					p.FM.Updated.String(), src, r.Ingested.String()),
			})
			break
		}
	}
	return findings
}
