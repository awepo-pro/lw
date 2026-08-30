package lint

import "github.com/awepo-pro/lw/internal/vault"

// fmQualityCheck is check 12, fm-quality.
type fmQualityCheck struct{}

func newFMQuality() Check { return fmQualityCheck{} }

func (fmQualityCheck) ID() string { return "fm-quality" }
func (fmQualityCheck) Describe() string {
	return "confidence: low, contested: true, or a single source with no confidence set"
}
func (fmQualityCheck) Severity() Severity { return SevInfo }

// Run reports pages whose frontmatter signals a claim that has not
// hardened into accepted fact yet: an explicit confidence: low, an
// explicit contested: true, or a page that cites exactly one source and
// sets no confidence at all (Hermes; backbone §4 check 12). Each page
// gets at most one finding, checked in that order — the three conditions
// describe overlapping shades of the same "review this" signal rather
// than independent defects.
func (fmQualityCheck) Run(ctx *Context) []Finding {
	var findings []Finding
	for _, p := range ctx.Vault.Pages() {
		msg, ok := fmQualityMessage(p.FM)
		if !ok {
			continue
		}
		findings = append(findings, Finding{
			Check:    "fm-quality",
			Path:     p.Path,
			Severity: SevInfo,
			Message:  msg,
		})
	}
	return findings
}

// fmQualityMessage reports the fm-quality message for fm, and whether one
// applies at all.
func fmQualityMessage(fm vault.Frontmatter) (string, bool) {
	switch {
	case fm.Confidence == vault.ConfLow:
		return "confidence is low; corroborate with another source or raise the confidence", true
	case fm.Contested:
		return "page is marked contested; resolve the dispute or drop the contested flag", true
	case len(fm.Sources) == 1 && fm.Confidence == "":
		return "cites a single source with no confidence set; add a confidence level", true
	default:
		return "", false
	}
}
