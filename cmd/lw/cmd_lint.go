package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/stage"
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
	fix := fs.Bool("fix", false, "hand the findings to the agent and stage its proposed repairs (still requires review)")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "lw lint: unexpected argument %q\n", fs.Arg(0))
		return &exitError{code: 2}
	}

	// --fix hands the engine's findings to the agent and lets it propose
	// repairs; the result still stages, same as any other agent turn
	// (/PLAN.md §9.4) — it returns here rather than falling into the
	// read-only report built below.
	if *fix {
		return runLintFix(*vaultPath, *checksFlag)
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

// runLintFix implements `lw lint --fix`: it runs the lint checks, and —
// unless the vault is already clean — hands every finding to the agent
// and asks it to propose a repair for each through the same stage.* tools
// any other agent turn uses. The model never computes lint results itself
// (00-conventions.md §5); it only reads what lint.Run already reported.
// The result still stages: even an automated repair goes through
// hunk-level human review before it lands (/PLAN.md §9.4), so this leaves
// an open changeset and commits nothing, exactly like `lw ingest`.
func runLintFix(vaultPath, checksFlag string) error {
	root, err := findVaultRoot(vaultPath)
	if err != nil {
		return err
	}

	e, err := stage.OpenEngine(root)
	if err != nil {
		return fmt.Errorf("open engine: %w", err)
	}
	defer e.Close()

	ctx := &lint.Context{
		Vault: e.Vault(),
		Index: e.Index(),
		Graph: e.Vault().Graph(),
	}
	var only []string
	if checksFlag != "" {
		only = strings.Split(checksFlag, ",")
	}
	report := lint.Run(ctx, only)
	if len(report.Findings) == 0 {
		fmt.Println("clean")
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Construct the agent before opening a changeset, same as cmdIngest:
	// a bad or missing API key must fail with nothing opened.
	sessions := agent.NewFileSessions(e.Vault().Root())
	ag, err := newAgent(e, cfg, sessions)
	if err != nil {
		return fmt.Errorf("construct agent: %w", err)
	}

	cs, err := e.OpenChangeset(lintFixIntent(report), stage.Author{Kind: "agent", Model: cfg.LLM.Model})
	if err != nil {
		return fmt.Errorf("open changeset: %w", err)
	}
	sess, err := sessions.Create(cs.ID)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}

	sendErr := runAgentTurn(context.Background(), ag, sess.ID, buildLintFixMessage(report), os.Stdout)

	final, curErr := e.Current()
	if curErr != nil {
		if sendErr != nil {
			return fmt.Errorf("agent turn: %w (and reading back the changeset failed: %v)", sendErr, curErr)
		}
		return fmt.Errorf("read back changeset %s: %w", cs.ID, curErr)
	}

	fmt.Println()
	printChangesetSummary(os.Stdout, final)

	if sendErr != nil {
		return fmt.Errorf("agent turn: %w", sendErr)
	}
	return nil
}

// lintFixIntent builds a changeset intent line describing what --fix is
// repairing.
func lintFixIntent(report lint.Report) string {
	return fmt.Sprintf("lint --fix: repair %d finding(s)", len(report.Findings))
}

// buildLintFixMessage hands the agent every finding lint.Run reported and
// asks it to propose a repair for each through the stage.* tools.
func buildLintFixMessage(report lint.Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The vault's lint check reported %d finding(s). Propose a repair for each one using the stage.* tools (a patch, a rename, a link, whatever fits), stating your rationale. When you are done, call stage.close to summarize the proposed changeset.\n\n", len(report.Findings))
	for _, f := range report.Findings {
		fmt.Fprintf(&b, "- %s:%d: %s: %s (%s)\n", f.Path, f.Line, f.Severity, f.Message, f.Check)
	}
	return b.String()
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
