package lint

import "fmt"

// fmDatesCheck is check 3, fm-dates.
type fmDatesCheck struct{}

func newFMDates() Check { return fmDatesCheck{} }

func (fmDatesCheck) ID() string         { return "fm-dates" }
func (fmDatesCheck) Describe() string   { return "created/updated malformed, or created > updated" }
func (fmDatesCheck) Severity() Severity { return SevWarn }

// Run reports every page whose created date is after its updated date.
//
// A date string that fails strict "2006-01-02" parsing never reaches here
// as a parsed Date at all — vault.ParsePage fails the whole page and it
// lands in Context.Vault.ParseErrors instead (backbone §2.1's UnmarshalYAML
// contract), which fm-required reports. A page with an absent (zero)
// created or updated is likewise fm-required's concern, not this check's —
// skipping it here avoids the two checks disagreeing about the same page.
func (fmDatesCheck) Run(ctx *Context) []Finding {
	var findings []Finding
	for _, p := range ctx.Vault.Pages() {
		created, updated := p.FM.Created, p.FM.Updated
		if created.IsZero() || updated.IsZero() {
			continue
		}
		if created.Time.After(updated.Time) {
			findings = append(findings, Finding{
				Check:    "fm-dates",
				Path:     p.Path,
				Severity: SevWarn,
				Message:  fmt.Sprintf("created %s is after updated %s; fix one of the dates", created, updated),
				Fixable:  true,
			})
		}
	}
	return findings
}
