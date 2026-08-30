package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/lint"
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
	fmt.Println(changesetStatusLine())

	return nil
}

// changesetStatusLine is the single place that produces the open-changeset
// line of `lw status`. In v0.1 there is no internal/stage yet, so it is a
// literal "no open changeset" — S2-T7 only has to change the body of this
// function to look up the real changeset once internal/stage exists.
func changesetStatusLine() string {
	return "no open changeset"
}
