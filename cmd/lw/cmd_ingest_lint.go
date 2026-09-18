package main

import (
	"fmt"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui/review"
)

// warnIfLintRegresses prints the one-line warning A-807 (FINDING-3) adds to
// a successful ingest: when the open changeset projects more lint errors
// than the vault's baseline — by the SAME predicate lw commit refuses on,
// e.ProjectedReport() against review.LintBaseline(e) via Report.Regresses —
// the user hears it at ingest time instead of only at commit. It is a
// warning, never a failure: the exit code stays 0 and the changeset stays
// open for the review the message itself points at (lw diff). A report that
// cannot be computed at all is also only a warning, naming the reason.
func warnIfLintRegresses(e *stage.Engine) {
	projected, err := e.ProjectedReport()
	if err != nil {
		fmt.Printf("warning: could not check lint: %v\n", err)
		return
	}
	baseline, err := review.LintBaseline(e)
	if err != nil {
		fmt.Printf("warning: could not check lint: %v\n", err)
		return
	}
	if projected.Regresses(baseline) {
		fmt.Printf("warning: lint regresses — %d error(s) projected vs %d in the last commit; lw commit will refuse this (review with lw diff)\n",
			projected.Errors, baseline.Errors)
	}
}
