package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/trace"
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
	root, err := writableVaultRoot(vaultPath)
	if err != nil {
		return err
	}
	initLoggingAt(root)

	// 042: take the newest vault before the engine opens.
	auto := loadAutoSync(root)
	auto.pull()

	e, err := openVaultEngine(root, auto)
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

	// 019: open — or, when a changeset is already open, JOIN it, saying so
	// on stderr before the first round. A failed round's partial ops stay
	// live (020 FIX-3b, unchanged below): --fix has no rejectAndReturn
	// path, so there is no rollback to scope — the ops remain for review,
	// and the failure lines say which page staged them.
	cs, joined, err := e.OpenOrJoin(lintFixIntent(report), stage.Author{Kind: "agent", Model: cfg.LLM.Model})
	if err != nil {
		return fmt.Errorf("open changeset: %w", err)
	}
	if joined {
		fmt.Fprintf(os.Stderr, "joined open changeset %s (%d op(s) already staged; they will be reviewed and committed together)\n",
			cs.ID, len(cs.Live()))
	}
	sess, err := verbSession(sessions, cs.ID, joined)
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
		// 038: the turn's verb rides the ctx (038 C-3) — lint here, the same
		// one-word tag ingest and query set around their own Send.
		if err := runAgentTurn(trace.WithVerb(context.Background(), trace.VerbLint), ag, sess.ID, msg, os.Stdout); err != nil {
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
	printChangesetSummary(os.Stdout, final, joined)

	if len(failures) > 0 {
		// U1 still applies per failure: agentErrorHint names the output
		// budget on a truncated turn. The C-808 query/lint form applies —
		// no rejection sentence, which is ingest's alone. Each line also
		// carries lintFixPartialOpsClause: a failed round's partial ops
		// stay live in the changeset, and the reviewer must know the
		// failure did not take them back out (020 FIX-3b, G3 review
		// finding 6).
		fmt.Printf("\n%d page round(s) failed:\n", len(failures))
		for _, f := range failures {
			fmt.Printf("  %s: %v%s\n", f.page, agentErrorHint(f.err, cfg.LLM.MaxTokens, false), lintFixPartialOpsClause(final, f.page))
		}
		return &exitError{code: 1}
	}
	return nil
}

// lintFixPartialOpsClause is the parenthetical appended to a failed
// round's failure line: containment is prompt-only, so a round whose Send
// fails after some Appends leaves those ops live, and the failure line
// must say so (020 FIX-3b, G3 review finding 6). When the page is known
// the clause counts the final changeset's live ops touching it — path,
// rename/merge endpoints, split products — a cheap read-back of state the
// command already holds; no engine surface is added. The count is an
// upper bound on the failed round's own partials, never an attribution:
// nothing scopes an op to the round that proposed it, so the number may
// include ops another round staged for the same page. A vault-level round
// (label "(vault-level)", no single path) and a page with no matching ops
// get the clause without a count.
func lintFixPartialOpsClause(cs *stage.Changeset, page string) string {
	const clause = "any repairs it staged before failing remain in the changeset for review"
	if cs == nil {
		return " (" + clause + ")"
	}
	n := 0
	for _, op := range cs.Live() {
		if op.Path == page || op.From == page || op.To == page {
			n++
			continue
		}
		for _, src := range op.Sources {
			if src == page {
				n++
				break
			}
		}
	}
	if n == 0 {
		return " (" + clause + ")"
	}
	return fmt.Sprintf(" (%d live op(s) touch this page; %s)", n, clause)
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

// remoteVaultForm matches the ssh-style host:path an explicit --vault must
// not be (047 S2, 042 gap #1): a host name — letters, digits, dot, dash and
// underscore, starting with a letter or digit — then a colon. The class after
// the first character is "+", not "*", so a host is two or more characters:
// a single-letter prefix is a Windows drive (C:\x), which is a local path
// and must stay one.
var remoteVaultForm = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]+:`)

// findVaultRoot resolves the vault root every verb works on (042 D1), in this
// order:
//
//  1. an explicit --vault value, used as given;
//  2. $LW_VAULT, likewise;
//  3. the nearest ancestor of the working directory that holds a SCHEMA.md;
//  4. the config's [vault] path, which must hold a SCHEMA.md;
//  5. otherwise the error that has always been returned.
//
// The one explicit value it refuses is host:path that names nothing on this
// machine (047 S2): lw has no remote vaults, so `--vault home:~/ai-vault`
// would otherwise fail verbs later on a "home:~" directory that does not
// exist. It answers with the command that does work. A colon in a path that
// exists is just a colon — the refusal needs os.Stat to fail. The same check
// guards $LW_VAULT, and its message is unchanged.
//
// Whatever root comes out is then checked against the vault format (042
// A-042-2): a vault written by a newer lw is refused here, with the
// *stage.FormatError text, by EVERY verb that resolves a vault — lint and
// status read it before any engine exists, status turns an engine error into a
// status line, and note, session and trace never open the engine at all, so
// leaving the check to stage.OpenEngine would let a dozen verbs misread a
// newer vault. The check reads one small file and writes nothing.
func findVaultRoot(explicit string) (string, error) {
	root, err := discoverVaultRoot(explicit)
	if err != nil {
		return "", err
	}
	if err := checkVaultFormat(root); err != nil {
		return "", err
	}
	return root, nil
}

// discoverVaultRoot is findVaultRoot's five-step search, without the format
// check.
func discoverVaultRoot(explicit string) (string, error) {
	if explicit != "" {
		return explicitVaultRoot(explicit)
	}
	if env := os.Getenv("LW_VAULT"); env != "" {
		return explicitVaultRoot(env)
	}

	wd, wdErr := os.Getwd()
	if wdErr == nil {
		dir := wd
		for {
			if info, err := os.Stat(filepath.Join(dir, "SCHEMA.md")); err == nil && !info.IsDir() {
				return dir, nil
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	// The config is read only now, when nothing nearer named a vault, and a
	// config that cannot be read is not this function's error to report: the
	// verbs that load it say so, and falling through keeps today's message.
	if cfg, err := config.Load(); err == nil && cfg.Vault.Path != "" {
		path := cfg.Vault.ResolvedPath()
		if info, err := os.Stat(filepath.Join(path, "SCHEMA.md")); err != nil || info.IsDir() {
			return "", fmt.Errorf("[vault] path %s: no SCHEMA.md there", path)
		}
		return path, nil
	}

	if wdErr != nil {
		return "", fmt.Errorf("getwd: %w", wdErr)
	}
	return "", fmt.Errorf("no SCHEMA.md found in %s or any parent directory; pass --vault", wd)
}

// explicitVaultRoot returns a --vault or $LW_VAULT value as given, refusing
// the host:path form that names nothing on this machine (047 S2).
func explicitVaultRoot(explicit string) (string, error) {
	if remoteVaultForm.MatchString(explicit) {
		if _, err := os.Stat(explicit); err != nil {
			host, path, _ := strings.Cut(explicit, ":")
			return "", fmt.Errorf("--vault %q looks like host:path, but lw has no remote vaults; run lw on that host instead: ssh %s -t lw tui --vault %s", explicit, host, path)
		}
	}
	return explicit, nil
}
