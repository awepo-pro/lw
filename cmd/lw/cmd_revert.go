package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/awepo-pro/lw/internal/stage"
)

// cmdRevert opens the inverse of a committed changeset for review.
//
// Contract (backbone §5.8 D-BY): a path Revert could not invert (a raw/
// addition, or a restored page validateCreatePage itself rejects) is
// never dropped silently — it is recorded in the reverted journal event's
// Data and in the changeset's Intent. cmdRevert surfaces that list on
// stdout, one "skipped: <path>" line per entry, so it is visible without
// reading the journal by hand.
func cmdRevert(args []string) error {
	fs := flag.NewFlagSet("revert", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: lw revert <commit-id>")
		return &exitError{code: 2}
	}
	commitID := fs.Arg(0)

	root, err := findVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	initLoggingAt(root)

	e, err := stage.OpenEngine(root)
	if err != nil {
		return fmt.Errorf("open engine: %w", err)
	}
	defer e.Close()

	c, err := e.Revert(commitID)
	if err != nil {
		return err
	}

	fmt.Printf("opened %s: %s\n", c.ID, c.Intent)

	skipped, err := revertedSkippedPaths(e, c.ID)
	if err != nil {
		return err
	}
	for _, p := range skipped {
		fmt.Printf("skipped: %s\n", p)
	}
	return nil
}

// revertedSkippedPaths reads the "skipped" list Revert recorded on the
// reverted event for changesetID, exactly as it journaled it (backbone
// §5.8 D-BY, D-BZ) — never re-derived, since the journal record is the
// one source that cannot be dropped silently.
func revertedSkippedPaths(e *stage.Engine, changesetID string) ([]string, error) {
	evs, err := e.Journal().Query(stage.Filter{
		Kinds:     []stage.EventKind{stage.EvReverted},
		Changeset: changesetID,
		Limit:     1,
	})
	if err != nil {
		return nil, err
	}
	if len(evs) == 0 || len(evs[0].Data) == 0 {
		return nil, nil
	}

	var data struct {
		Skipped []string `json:"skipped"`
	}
	if err := json.Unmarshal(evs[0].Data, &data); err != nil {
		return nil, fmt.Errorf("parse reverted event data: %w", err)
	}
	return data.Skipped, nil
}
