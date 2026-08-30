package lint

import "sort"

// Severity is how urgently a Finding should be acted on.
type Severity string

const (
	SevError Severity = "error"
	SevWarn  Severity = "warn"
	SevInfo  Severity = "info"
)

// Finding is one thing a Check noticed about the vault.
type Finding struct {
	Check    string // check ID, e.g. "link-broken"
	Path     string // vault-relative page, or "" for vault-wide
	Line     int    // 0 when not line-specific
	Severity Severity
	Message  string // one line, no trailing period, states the fix
	Fixable  bool   // true if an agent could plausibly repair it
}

// Check is one independently testable lint rule. Implementations never
// touch the filesystem directly and never mutate the Context they are
// handed — everything they need is already on it.
type Check interface {
	ID() string
	Describe() string
	Severity() Severity
	Run(*Context) []Finding
}

// Report is the result of running one or more Checks over a Context.
type Report struct {
	Findings []Finding // sorted by Path, then Line, then Check
	ByCheck  map[string][]Finding
	Errors   int
	Warns    int
}

// All returns the fixed set of 14 checks (backbone §4, MASTER §9 D-V), in
// the order they appear in that table. A fresh instance is constructed on
// every call — checks hold no state, so this costs nothing and keeps the
// package free of mutable package-level vars (00-conventions.md §2).
func All() []Check {
	return []Check{
		newFMRequired(),
		newFMTaxonomy(),
		newFMDates(),
		newPathConvention(),
		newLinkBroken(),
		newLinkMinOut(),
		newLinkOrphan(),
		newSrcIntegrity(),
		newSrcProvenance(),
		newIndexSync(),
		newSizeSplit(),
		newFMQuality(),
		newSrcStale(),
		newLogRotate(),
	}
}

// Run runs the checks named in only against ctx and returns the combined
// Report. A nil or empty only runs every check in All().
//
// Contract (backbone §4): Findings are sorted by Path, then Line, then
// Check — the Check-name tie-break matters whenever two checks report the
// same Path and Line. ByCheck groups the same findings by check ID; Errors
// and Warns count SevError and SevWarn findings respectively.
func Run(ctx *Context, only []string) Report {
	checks := selectChecks(only)

	var findings []Finding
	for _, c := range checks {
		findings = append(findings, c.Run(ctx)...)
	}
	sortFindings(findings)

	byCheck := map[string][]Finding{}
	var errors, warns int
	for _, f := range findings {
		byCheck[f.Check] = append(byCheck[f.Check], f)
		switch f.Severity {
		case SevError:
			errors++
		case SevWarn:
			warns++
		}
	}

	return Report{
		Findings: findings,
		ByCheck:  byCheck,
		Errors:   errors,
		Warns:    warns,
	}
}

// selectChecks returns All() when only is empty, otherwise the subset of
// All() whose ID appears in only, preserving All()'s table order.
func selectChecks(only []string) []Check {
	all := All()
	if len(only) == 0 {
		return all
	}

	want := make(map[string]bool, len(only))
	for _, id := range only {
		want[id] = true
	}

	var out []Check
	for _, c := range all {
		if want[c.ID()] {
			out = append(out, c)
		}
	}
	return out
}

// sortFindings sorts findings in place by Path, then Line, then Check, per
// Report.Findings' ordering contract.
func sortFindings(findings []Finding) {
	sort.Slice(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Check < b.Check
	})
}

// Clean reports whether r has zero error-severity findings.
func (r Report) Clean() bool {
	return r.Errors == 0
}

// Regresses reports whether r has more error-severity findings than prev —
// the signal `lw commit` uses to refuse a regressing changeset.
func (r Report) Regresses(prev Report) bool {
	return r.Errors > prev.Errors
}
