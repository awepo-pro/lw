package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui/review"
)

// cmdCommit commits the open changeset to the vault.
//
// Contract (backbone §13): commit refuses when the projected tree's lint
// errors regress against the vault's baseline (Report.Regresses); --force
// overrides that refusal and journals that it was forced. Engine itself
// does no such check — S2-T4's brief makes that explicit ("no
// lint-regression gate on the CLI [...] T7 owns that flag") — cmdCommit
// owns the whole thing: computing it, refusing on it, and recording an
// override. The baseline itself comes from review.LintBaseline (S6-C127,
// closing part of TD-3): the same helper the review screen's `C` key
// calls, which is what keeps `lw commit` and a TUI commit producing
// "exactly the same result" (s4-tui.md S4-T3) rather than two
// hand-maintained copies of the rule.
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

	// review.LintBaseline resolves the most recent commit_end's counts when
	// one exists, or — a vault that has never been committed, S6-C127's
	// live defect — the report of linting the CURRENT committed working
	// tree, so a vault's very first commit is gated exactly like every
	// later one instead of passing unconditionally (backbone §5.7 D-AG,
	// extended by S6-C127; not touched at all for a vault with a prior
	// commit).
	baseline, err := review.LintBaseline(e)
	if err != nil {
		return fmt.Errorf("read lint baseline: %w", err)
	}

	forced := false
	if projected.Regresses(baseline) {
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
