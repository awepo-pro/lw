// lintbaseline.go is the commit gate's baseline: the lint.Report a
// projected tree is compared against (backbone §5.7 D-AG, S6-C127). It
// lives in its own file because it is exported API shared with cmd/lw,
// not review-screen state.
package review

import (
	"encoding/json"
	"fmt"

	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/stage"
)

// commitEndData is the wire shape of a commit_end event's Data (backbone
// §5.7 D-AG): the counts of the report computed for that commit.
type commitEndData struct {
	LintErrors int `json:"lint_errors"`
	LintWarns  int `json:"lint_warns"`
}

// LintBaseline computes the lint-regression baseline the commit gate
// compares a projected report against (backbone §5.7 D-AG; S6-C127). It is
// exported and lives here — rather than duplicated a second time — because
// cmd/lw already imports this package (cmd_tui.go, to wire the review
// screen into the shell), which is what closes TD-3's "the gate is
// implemented twice" for this half of it: cmd/lw's cmdCommit calls
// review.LintBaseline directly instead of running its own copy of this
// journal query.
//
// When the journal already holds a commit_end event, its counts ARE the
// baseline — the tree exactly as it was committed, decoded from Data
// exactly as Commit wrote it. Filter.Limit selects the most recent N
// events and returns them oldest-first (backbone §5.7 D-AU), so Limit:1
// is read as evs[0]; a larger Limit read as evs[0] would silently compare
// every future commit against the FIRST commit ever made.
//
// When it does not — a vault that has never been committed — the baseline
// becomes the lint.Report of the CURRENT COMMITTED working tree
// (e.Vault(), e.Index() and e.Vault().Graph(), the same {Vault, Index,
// Graph} shape `lw lint` builds), so a vault's first commit is refused
// exactly like every later one and a vault that already carries N lint
// errors is not penalized for them: only a NEW error, added by the
// changeset being committed, regresses.
func LintBaseline(e *stage.Engine) (lint.Report, error) {
	evs, err := e.Journal().Query(stage.Filter{
		Kinds: []stage.EventKind{stage.EvCommitEnd},
		Limit: 1,
	})
	if err != nil {
		return lint.Report{}, err
	}
	if len(evs) > 0 {
		var data commitEndData
		if err := json.Unmarshal(evs[0].Data, &data); err != nil {
			return lint.Report{}, fmt.Errorf("review: parse commit_end data: %w", err)
		}
		return lint.Report{Errors: data.LintErrors, Warns: data.LintWarns}, nil
	}

	ctx := &lint.Context{Vault: e.Vault(), Index: e.Index(), Graph: e.Vault().Graph()}
	return lint.Run(ctx, nil), nil
}
