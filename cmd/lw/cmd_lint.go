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
	// (/docs/design.md §9.4) — it returns here rather than falling into the
	// read-only report built below.
	if *fix {
		return runLintFix(*vaultPath, *checksFlag)
	}

	root, err := findVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	attachLoggingAt(root) // the report path is read-only: join, never create

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
// unless the vault is already clean — hands the findings to the agent ONE
// PAGE AT A TIME (020 T-D): the report's findings are bucketed by path,
// and each bucket drives one agent round through the same stage.* tools
// any other agent turn uses, inside the single changeset and session the
// command opened. The model never computes lint results itself
// (00-conventions.md §5); it only reads what lint.Run already reported.
// Per-page rounds keep ops dependent-safe (each round validates against
// the changeset's own staged state), contain a failure to one page, and
// leave the reviewer one page's hunks at a time. A failed round is
// recorded and the loop continues; at the end every failed page is named
// with its error and the command exits non-zero. The result still stages:
// even an automated repair goes through hunk-level human review before it
// lands (/docs/design.md §9.4), so this leaves an open changeset and
// commits nothing, exactly like `lw ingest`.
func runLintFix(vaultPath, checksFlag string) error {
	root, err := findVaultRoot(vaultPath)
	if err != nil {
		return err
	}
	initLoggingAt(root)

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

	rounds := planLintFixRounds(report)

	// One agent round per page, first-appearance order. A round whose
	// Send fails is recorded and the loop continues: one broken page must
	// not cost the others their repairs.
	var failures []lintFixFailure
	for i, r := range rounds {
		fmt.Printf("%s (%d/%d)\n", lintFixRoundLabel(r), i+1, len(rounds))
		msg := buildLintFixRoundMessage(r, i, len(rounds))
		if err := runAgentTurn(context.Background(), ag, sess.ID, msg, os.Stdout); err != nil {
			failures = append(failures, lintFixFailure{page: lintFixRoundLabel(r), err: err})
		}
	}

	final, curErr := e.Current()
	if curErr != nil {
		if len(failures) > 0 {
			return fmt.Errorf("%w (and reading back the changeset failed: %v)", agentErrorHint(failures[0].err, cfg.LLM.MaxTokens, false), curErr)
		}
		return fmt.Errorf("read back changeset %s: %w", cs.ID, curErr)
	}

	fmt.Println()
	printChangesetSummary(os.Stdout, final)

	if len(failures) > 0 {
		// U1 still applies per failure: agentErrorHint names the output
		// budget on a truncated turn. The C-808 query/lint form applies —
		// no rejection sentence, which is ingest's alone.
		fmt.Printf("\n%d page round(s) failed:\n", len(failures))
		for _, f := range failures {
			fmt.Printf("  %s: %v\n", f.page, agentErrorHint(f.err, cfg.LLM.MaxTokens, false))
		}
		return &exitError{code: 1}
	}
	return nil
}

// lintFixIntent builds a changeset intent line describing what --fix is
// repairing.
func lintFixIntent(report lint.Report) string {
	return fmt.Sprintf("lint --fix: repair %d finding(s)", len(report.Findings))
}

// lintFixRound is one agent round of --fix: every finding the report
// carries for a single page, or — when page is "" — the vault-level
// findings no page owns.
type lintFixRound struct {
	page     string
	findings []lint.Finding
}

// planLintFixRounds buckets report.Findings by Path, preserving report
// order (already sorted Path, Line, Check — never re-sorted here): every
// distinct path is one round, in first-appearance order, and a finding
// with an empty path is its own bucket.
func planLintFixRounds(report lint.Report) []lintFixRound {
	var rounds []lintFixRound
	idx := map[string]int{}
	for _, f := range report.Findings {
		i, ok := idx[f.Path]
		if !ok {
			i = len(rounds)
			idx[f.Path] = i
			rounds = append(rounds, lintFixRound{page: f.Path})
		}
		rounds[i].findings = append(rounds[i].findings, f)
	}
	return rounds
}

// buildLintFixRoundMessage writes the message for round i of n: it names
// only this round's page (or says "vault-level" when the round has no
// page) and asks for repairs to that page alone. Only the final round's
// message asks for stage.close — an agent that closes early is contained
// by review, so nothing guards against it.
func buildLintFixRoundMessage(r lintFixRound, i, n int) string {
	var b strings.Builder
	if r.page == "" {
		fmt.Fprintf(&b, "The vault's lint check reported %d vault-level finding(s) (round %d of %d). They are not tied to a single page. Propose a repair for each one using the stage.* tools (a patch, a rename, a link, whatever fits), stating your rationale, and touch only what these findings name — no other page may be modified this round.\n\n", len(r.findings), i+1, n)
	} else {
		fmt.Fprintf(&b, "The vault's lint check reported findings for page %s; this round covers that page only (round %d of %d). Propose a repair for each finding below using the stage.* tools (a patch, a rename, a link, whatever fits), stating your rationale. Touch this page only — no other page may be modified this round.\n\n", r.page, i+1, n)
	}
	if i < n-1 {
		b.WriteString("Do not close the changeset: more pages follow in later rounds.\n\n")
	} else {
		b.WriteString("This is the last round. When you are done, call stage.close to summarize the proposed changeset.\n\n")
	}
	for _, f := range r.findings {
		fmt.Fprintf(&b, "- %s:%d: %s: %s (%s)\n", f.Path, f.Line, f.Severity, f.Message, f.Check)
	}
	return b.String()
}

// lintFixRoundMessages returns the per-round messages for report: one per
// distinct path, in first-appearance order, with only the last asking for
// stage.close.
func lintFixRoundMessages(report lint.Report) []string {
	rounds := planLintFixRounds(report)
	msgs := make([]string, len(rounds))
	for i, r := range rounds {
		msgs[i] = buildLintFixRoundMessage(r, i, len(rounds))
	}
	return msgs
}

// lintFixFailure is a page round whose Send failed: the page it was
// repairing and the error, named together in the output after the loop
// and counted into the non-zero exit.
type lintFixFailure struct {
	page string
	err  error
}

// lintFixRoundLabel is the name a round goes by in the progress line and
// the failure list: its page path, or "(vault-level)" when the round
// carries the findings no single page owns.
func lintFixRoundLabel(r lintFixRound) string {
	if r.page == "" {
		return "(vault-level)"
	}
	return r.page
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
