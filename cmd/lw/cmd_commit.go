package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/stage"
)

// cmdCommit commits the open changeset to the vault.
//
// Contract (backbone §13): commit refuses when the projected tree's lint
// errors regress against the last commit's baseline (Report.Regresses);
// --force overrides that refusal and journals that it was forced. Engine
// itself does no such check — S2-T4's brief makes that explicit ("no
// lint-regression gate on the CLI [...] T7 owns that flag") — cmdCommit
// owns the whole thing: computing it, refusing on it, and recording an
// override.
func cmdCommit(args []string) error {
	fs := flag.NewFlagSet("commit", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	message := fs.String("m", "", "commit message (required)")
	force := fs.Bool("force", false, "commit even if lint regresses against the last commit")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "lw commit: unexpected argument %q\n", fs.Arg(0))
		return &exitError{code: 2}
	}
	if *message == "" {
		fmt.Fprintln(os.Stderr, `usage: lw commit -m "<message>" [--force]`)
		return &exitError{code: 2}
	}

	root, err := findVaultRoot(*vaultPath)
	if err != nil {
		return err
	}

	e, err := stage.OpenEngine(root)
	if err != nil {
		return fmt.Errorf("open engine: %w", err)
	}
	defer e.Close()

	// Rehydrate the open changeset — ErrNoChangeset here is the "nothing
	// staged" error path, and every step below assumes one is open.
	if _, err := e.Current(); err != nil {
		return err
	}

	// ProjectedReport equals the post-commit report exactly (backbone §5.4
	// D-CC), which is what makes it a sound basis for the refusal decision
	// before a byte is written.
	projected, err := e.ProjectedReport()
	if err != nil {
		return err
	}

	baseline, hasBaseline, err := lastCommitLintBaseline(e)
	if err != nil {
		return fmt.Errorf("read lint baseline: %w", err)
	}

	// A vault with no prior commit_end has no baseline: the check passes
	// (backbone §5.7 D-AG, verbatim) — gate G2's own first commit is
	// exactly this case.
	forced := false
	if hasBaseline && projected.Regresses(baseline) {
		if !*force {
			return fmt.Errorf("lint regressed: %d error(s) projected vs %d in the last commit; review with `lw diff`, fix it, or re-run with --force", projected.Errors, baseline.Errors)
		}
		forced = true
	}

	// The override is recorded on the REAL commit_end, by the Engine that
	// writes it (MASTER §9 D-CD / §10 OR-12). Appending a second
	// commit_end from here instead would make the journal state that one
	// commit ended twice.
	if forced {
		e.ForceNextCommit()
	}

	commitID, err := e.Commit(*message)
	if err != nil {
		if errors.Is(err, stage.ErrStale) {
			return fmt.Errorf("changeset has stale ops (the working tree changed since they were proposed); run `lw diff` to review, then re-stage before committing: %w", err)
		}
		return err
	}

	fmt.Printf("committed %s\n", commitID)
	return nil
}

// commitEndData is the shape of a commit_end event's Data (backbone §5.7
// D-AG): the counts of the report computed for that commit, and, when
// --force overrode a refusal, "forced":true.
type commitEndData struct {
	LintErrors int  `json:"lint_errors"`
	LintWarns  int  `json:"lint_warns"`
	Forced     bool `json:"forced,omitempty"`
}

// lastCommitLintBaseline reads the most recent commit_end event's lint
// counts as the baseline lw commit refuses a regression against.
//
// Contract (backbone §5.7 D-AU, MASTER §9): Filter.Limit selects the most
// recent N events and returns them oldest-first, so Limit:1 must be read
// as evs[0] — a larger Limit read as evs[0] would silently compare every
// future commit against the FIRST commit ever made. hasBaseline is false
// when the journal holds no commit_end at all (a fresh vault's first
// commit), which is the case backbone §5.7 pins as "the check passes".
func lastCommitLintBaseline(e *stage.Engine) (lint.Report, bool, error) {
	evs, err := e.Journal().Query(stage.Filter{
		Kinds: []stage.EventKind{stage.EvCommitEnd},
		Limit: 1,
	})
	if err != nil {
		return lint.Report{}, false, err
	}
	if len(evs) == 0 {
		return lint.Report{}, false, nil
	}

	var data commitEndData
	if err := json.Unmarshal(evs[0].Data, &data); err != nil {
		return lint.Report{}, false, fmt.Errorf("parse commit_end data: %w", err)
	}
	return lint.Report{Errors: data.LintErrors, Warns: data.LintWarns}, true, nil
}
