package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/vault"
)

// cmdStatus prints the vault's page/raw/tag counts, a lint summary, and the
// open-changeset line.
func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "lw status: unexpected argument %q\n", fs.Arg(0))
		return &exitError{code: 2}
	}

	root, err := findVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	attachLoggingAt(root) // read-only: join the trail, never create it

	v, err := vault.Open(root)
	if err != nil {
		return fmt.Errorf("open vault %s: %w", root, err)
	}

	ctx := &lint.Context{
		Vault: v,
		Index: index.Build(v),
		Graph: v.Graph(),
	}
	report := lint.Run(ctx, nil)
	info := len(report.Findings) - report.Errors - report.Warns

	fmt.Printf("%d pages · %d raw · %d tags\n", len(v.Pages()), len(v.RawSources()), len(v.Schema().Tags))
	fmt.Printf("lint: %d errors, %d warnings, %d info\n", report.Errors, report.Warns, info)
	fmt.Println(changesetStatusLine(root))

	return nil
}

// changesetStatusLine is the single place that produces the open-changeset
// line of `lw status`: the changeset's id, intent, op count, checks, and
// stale-op count when one is open under root, or the literal
// "no open changeset" when none is. root is threaded in (rather than read
// from a package var) so it always names the same vault cmdStatus already
// resolved via --vault/findVaultRoot, including when that differs from the
// process's working directory.
//
// A staging-engine error other than "no changeset open" is reported
// inline rather than failing the whole `lw status` call: the vault
// summary above this line already printed successfully, and a status
// line's job is to describe what it saw, not to abort a command that has
// otherwise succeeded.
func changesetStatusLine(root string) string {
	e, err := stage.OpenEngine(root)
	if err != nil {
		return fmt.Sprintf("changeset: error opening staging engine: %v", err)
	}
	defer e.Close()

	c, err := e.Current()
	if err != nil {
		if errors.Is(err, stage.ErrNoChangeset) {
			return "no open changeset"
		}
		return fmt.Sprintf("changeset: error: %v", err)
	}

	stale := 0
	for _, op := range c.Ops {
		if op.State == stage.StateStale {
			stale++
		}
	}

	return fmt.Sprintf(
		"open changeset %s: %s (%d op(s), %d stale; schema=%s lint=%s orphans=%d broken_links=%d)",
		c.ID, c.Intent, len(c.Ops), stale,
		c.Checks.Schema, c.Checks.Lint, c.Checks.Orphans, c.Checks.BrokenLinks,
	)
}
