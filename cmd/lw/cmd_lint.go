package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/vault"
)

// cmdLint runs the lint checks over a vault and reports the findings.
//
// Contract (backbone §13, MASTER §9 D-AC): lint's own stdout is the result.
// A clean vault, a dirty vault and a usage error all print to stdout/stderr
// themselves and signal their exit code with *exitError, so dispatch adds
// no "lw: lint: ..." line on top of the findings already printed. Only a
// genuine failure to open the vault returns a bare error, which dispatch
// does prefix.
func cmdLint(args []string) error {
	fs := flag.NewFlagSet("lint", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	checksFlag := fs.String("checks", "", "comma-separated check IDs to run (default: all)")
	jsonOut := fs.Bool("json", false, "emit the report as indented JSON instead of one line per finding")
	fix := fs.Bool("fix", false, "not implemented in v0.1 — accepted so the flag parses, then refused")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "lw lint: unexpected argument %q\n", fs.Arg(0))
		return &exitError{code: 2}
	}

	// --fix is refused bare (not via exitError): the user should see the
	// "lw: lint: " prefix main.go adds, per this subtask's brief.
	if *fix {
		return errors.New("--fix requires the agent (available in v0.1 after M5)")
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

	var only []string
	if *checksFlag != "" {
		only = strings.Split(*checksFlag, ",")
	}

	report := lint.Run(ctx, only)

	if *jsonOut {
		b, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal report: %w", err)
		}
		fmt.Println(string(b))
	} else {
		printLintReport(os.Stdout, report)
	}

	if report.Errors > 0 {
		return &exitError{code: 1}
	}
	return nil
}

// printLintReport writes one line per finding, in report.Findings order
// (already sorted by Path, then Line, then Check — never re-sorted here),
// followed by a summary line. A report with no findings prints "clean"
// instead of an empty summary, per this subtask's Goal.
func printLintReport(w io.Writer, report lint.Report) {
	if len(report.Findings) == 0 {
		fmt.Fprintln(w, "clean")
		return
	}

	for _, f := range report.Findings {
		fmt.Fprintf(w, "%s:%d: %s: %s (%s)\n", f.Path, f.Line, f.Severity, f.Message, f.Check)
	}

	info := len(report.Findings) - report.Errors - report.Warns
	fmt.Fprintf(w, "%d errors, %d warnings, %d info\n", report.Errors, report.Warns, info)
}

// findVaultRoot resolves the vault root shared by lw lint and lw status: an
// explicit --vault value is used as given; otherwise the current directory
// is walked upward until an ancestor containing SCHEMA.md is found.
func findVaultRoot(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}

	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}

	dir := wd
	for {
		if info, err := os.Stat(filepath.Join(dir, "SCHEMA.md")); err == nil && !info.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no SCHEMA.md found in %s or any parent directory; pass --vault", wd)
		}
		dir = parent
	}
}
