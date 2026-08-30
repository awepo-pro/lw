package lint

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/awepo-pro/lw/internal/vault"
)

// fmRequiredCheck is check 1, fm-required.
type fmRequiredCheck struct{}

func newFMRequired() Check { return fmRequiredCheck{} }

func (fmRequiredCheck) ID() string { return "fm-required" }
func (fmRequiredCheck) Describe() string {
	return "frontmatter missing, unparsable, or a required field absent / wrong type"
}
func (fmRequiredCheck) Severity() Severity { return SevError }

// Run reports two distinct fm-required defects: a file that failed to parse
// at all, surfaced through Context.Vault.ParseErrors (backbone §2.8,
// MASTER §9 D-W — a check never touches the filesystem directly), and a
// page that parsed successfully but is missing a required field or carries
// one with an invalid value.
func (fmRequiredCheck) Run(ctx *Context) []Finding {
	var findings []Finding

	for _, pe := range ctx.Vault.ParseErrors() {
		findings = append(findings, Finding{
			Check:    "fm-required",
			Path:     pe.Path,
			Severity: SevError,
			Message:  parseErrorMessage(pe.Err),
		})
	}

	for _, p := range ctx.Vault.Pages() {
		for _, msg := range requiredFieldIssues(p.FM) {
			findings = append(findings, Finding{
				Check:    "fm-required",
				Path:     p.Path,
				Severity: SevError,
				Message:  msg,
				Fixable:  true,
			})
		}
	}

	// ParseErrors and Pages are each already sorted by Path individually,
	// but interleaving the two loops above does not guarantee the combined
	// slice is; Run's caller re-sorts everything anyway, but this check is
	// independently testable, so it must hold its own ordering contract too.
	sort.Slice(findings, func(i, j int) bool { return findings[i].Path < findings[j].Path })
	return findings
}

// fmRequiredDateFieldErrRE matches the field name and offending value out
// of a wrapped Date.UnmarshalYAML failure. decodeFrontmatter (backbone
// §2.2) wraps every field error as `frontmatter field "<key>": vault:
// parse date "<value>": <cause>` (backbone §2.1's ParseDate), so both are
// recoverable from the error text without hard-coding a field name —
// "updated" works identically to "created".
var fmRequiredDateFieldErrRE = regexp.MustCompile(`frontmatter field "([^"]+)": vault: parse date "([^"]*)"`)

// parseErrorMessage turns the underlying ParsePage failure into a message
// that states the fix, per backbone §4's Message contract. Most cases
// trace back to a specific ParseFrontmatter failure mode (backbone §2.2).
//
// OQ-7 (MASTER §9 D-AB): backbone §4's fm-dates "malformed" clause is
// unreachable — a bad date value fails ParseDate inside
// Date.UnmarshalYAML, so the whole page lands here instead of at
// fm-dates. The generic "fix the YAML" fallback is then wrong twice over:
// the YAML is fine and the date is not, and it does not say what to fix.
// When the wrapped error names the offending field and value, this names
// them instead; every other failure mode (no field identified) keeps the
// existing generic messages, including malformed.md's "no closing" case,
// which must stay byte-identical.
func parseErrorMessage(err error) string {
	msg := err.Error()
	if m := fmRequiredDateFieldErrRE.FindStringSubmatch(msg); m != nil {
		return fmt.Sprintf("%s %q is not a valid YYYY-MM-DD date; fix it", m[1], m[2])
	}
	switch {
	case strings.Contains(msg, "no closing"):
		return "frontmatter block never closes; add the closing --- delimiter or fix the YAML"
	case strings.Contains(msg, "must open with"):
		return "frontmatter block is missing; add an opening --- delimiter and the required fields"
	case strings.Contains(msg, "parse frontmatter yaml"):
		return "frontmatter is not valid YAML; fix the YAML"
	default:
		return "frontmatter could not be parsed; fix the YAML"
	}
}

// requiredFieldIssues reports every required-field defect in fm that is not
// already owned by fm-taxonomy (tag membership) or fm-dates (date
// ordering): an absent title, an absent or invalid type, an absent
// created/updated date, or an invalid confidence value.
func requiredFieldIssues(fm vault.Frontmatter) []string {
	var issues []string

	if fm.Title == "" {
		issues = append(issues, "title is required; add one")
	}
	if !fm.Type.Valid() {
		issues = append(issues, fmt.Sprintf(
			"type %q is invalid; set it to one of entity, concept, comparison, query, summary", string(fm.Type)))
	}
	if fm.Created.IsZero() {
		issues = append(issues, "created date is required; add one")
	}
	if fm.Updated.IsZero() {
		issues = append(issues, "updated date is required; add one")
	}
	if fm.Confidence != "" && !fm.Confidence.Valid() {
		issues = append(issues, fmt.Sprintf(
			"confidence %q is invalid; set it to high, medium or low", string(fm.Confidence)))
	}

	return issues
}
