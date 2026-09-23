package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/tools"
)

// httpTimeout bounds every fetch the CLI's HTML extractor makes —
// cmdIngest's own source chain below and agent_deps.go's agentExtractors
// alike — so a hung remote server must not hang the whole command.
const httpTimeout = 30 * time.Second

// isURLSource reports whether src parses as an http or https URL — the
// same test stage.ingest_source itself applies (internal/tools/
// stage_source.go) — as opposed to a local file path given on the command
// line.
func isURLSource(src string) bool {
	u, err := url.Parse(src)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https")
}

// runAgentTurn drives one agent.Agent turn to completion: it starts
// ag.Send in a goroutine (the caller-runs-a-goroutine-and-ranges-over-out
// shape backbone §9's "who owns out" contract, C-105, requires of every
// Send caller), streams every TextDelta to w as it arrives, and returns
// once Send has both closed the channel and returned. Shared by ingest,
// query and lint --fix.
//
// S6-C130: text from two different agent rounds is separated by a blank
// line, so successive rounds no longer run together on one line — a live
// URL ingest printed "…orientation ritual.The vault is empty…" because the
// old version dropped tool events entirely and never marked a round
// boundary. A round boundary is "a ToolCallEv or ToolResEv arrived since
// the text last written", checked only when the next non-empty TextDelta
// arrives; deltas within one round (no tool event between them) are still
// written byte-identical to before.
func runAgentTurn(ctx context.Context, ag agent.Agent, sessionID, msg string, w io.Writer) error {
	out := make(chan agent.Event)
	done := make(chan struct{})
	var sendErr error
	go func() {
		sendErr = ag.Send(ctx, sessionID, msg, out)
		close(done)
	}()

	var wrote bool     // some TextDelta has already been written this turn
	var toolSince bool // a tool event arrived since the last TextDelta write
	var trailingNL int // trailing '\n' run at the end of w, capped at 2
	for ev := range out {
		switch e := ev.(type) {
		case agent.TextDelta:
			if e.Text == "" {
				continue
			}
			if wrote && toolSince {
				switch trailingNL {
				case 0:
					fmt.Fprint(w, "\n\n")
					trailingNL = 2
				case 1:
					fmt.Fprint(w, "\n")
					trailingNL = 2
				}
				// trailingNL == 2: the round's text already ended on a
				// blank line — no separator needed, nothing to write.
			}
			fmt.Fprint(w, e.Text)
			trailingNL = trailingNewlineRun(trailingNL, e.Text)
			wrote = true
			toolSince = false
		case agent.ToolCallEv, agent.ToolResEv:
			toolSince = true
		}
	}
	<-done
	return sendErr
}

// trailingNewlineRun returns, capped at 2, the number of consecutive '\n'
// bytes ending the text written so far: prev is that same count before s
// was written, and s is non-empty. If s itself contains a non-'\n' byte,
// the run resets to whatever trails s alone; if s is entirely '\n', the
// run carries over from prev.
func trailingNewlineRun(prev int, s string) int {
	n := 0
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] != '\n' {
			return n
		}
		n++
		if n >= 2 {
			return 2
		}
	}
	return min(prev+n, 2)
}

// ingestMaxFiles is the file half of the F.I4 folder-ingest limit: a
// directory invocation carrying more kept sources than this is refused
// before anything is opened. The byte half is derived from
// cfg.Limits.ContextTokens (see the limit check below), not a constant —
// only the file count is.
const ingestMaxFiles = 10

// ingestKB renders a byte count as whole KB, rounded up, the unit both the
// F.I4 error and the F.I5 verdict line quote — a partial KB still occupies
// a whole one as far as the user is concerned.
func ingestKB(n int) int {
	return (n + 1023) / 1024
}

// parseIngestSources parses args into fs's flag values and the returned
// source arguments, accepting flags anywhere among the sources. The frozen
// usage `lw ingest <url|path|dir>... [--kind K] [--dry-run]` promises
// `lw ingest notes/ --dry-run` works, but flag.FlagSet.Parse stops at the
// first positional, so parsing runs a chunk at a time: parse a flag run,
// take the one positional Parse stopped at, re-parse the rest — until
// every argument is consumed. "--" still ends flag parsing: everything
// after it is returned verbatim, so a file literally named "--x" stays
// expressible however far into the argument list it sits. An unparseable
// flag run is reported through fs (its error output) and returned.
func parseIngestSources(fs *flag.FlagSet, args []string) ([]string, error) {
	var sources []string
	for len(args) > 0 {
		if args[0] == "--" {
			return append(sources, args[1:]...), nil
		}
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if rest := fs.Args(); len(rest) == len(args) {
			// Parse consumed nothing: it stopped on this leading
			// argument, which is a source, not a flag.
			sources = append(sources, rest[0])
			args = rest[1:]
		} else {
			args = rest
		}
	}
	return sources, nil
}

// cmdIngest extracts one or more sources (a URL, a local path, or a local
// directory — 004) into deterministic markdown, opens a changeset, and
// hands it to the agent to ingest the raw content and compile wiki pages
// from it — leaving the changeset open for human review
// (/docs/design.md §9.4; nothing in this verb commits).
func cmdIngest(args []string) error {
	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	kindFlag := fs.String("kind", "", "override the detected kind for every source: article|paper|transcript")
	dryRun := fs.Bool("dry-run", false, "report what would be ingested and the limit verdict, then exit — opens no changeset, constructs no agent (URL sources are still fetched, to size them)")
	sources, err := parseIngestSources(fs, args)
	if err != nil {
		return &exitError{code: 2}
	}
	if len(sources) == 0 {
		fmt.Fprintln(os.Stderr, "usage: lw ingest <url|path|dir>... [--kind K] [--dry-run]")
		return &exitError{code: 2}
	}

	root, err := findVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	initLoggingAt(root)

	// Extract every source before touching the staging engine at all: a
	// bad source fails the whole command with nothing opened, rather than
	// leaving a changeset with only some of the requested sources in it.
	// The HTML client is extract.NewHTTPClient — the house client (010
	// contract §1) — the same one agent_deps.go builds the agents' chain
	// and the web.search provider over.
	ex := extract.Chain(extract.NewHTML(extract.NewHTTPClient(httpTimeout)), extract.NewFile())
	ctx := context.Background()

	// 004 F.I1: an argument that stats as a directory is replaced, in
	// place, by extract.Walk's eligible files — the same chain extracts
	// them, so the eligible set is exactly what this command could already
	// have handled one file at a time. Walk is stat-only (004 T1); the
	// pass-overs print here, before any extraction, so a folder's skips all
	// land on stdout ahead of the existing flow's output.
	hadDir := false                   // any argument was a directory — gates the F.I4 limits (correction #3)
	expanded := make(map[string]bool) // walker-selected paths, for F.I2's soft ErrNotText skip
	explicit := make(map[string]bool) // arguments named outright — F.I2's hard ErrNotText failure wins over folder expansion
	expandedSrcs := make([]string, 0, len(sources))
	for _, arg := range sources {
		if info, serr := os.Stat(arg); serr == nil && info.IsDir() {
			hadDir = true
			files, skipped, werr := extract.Walk(arg, ex)
			if werr != nil {
				return werr
			}
			for _, sk := range skipped {
				fmt.Printf("skipped %s: %s\n", sk.Path, sk.Reason)
			}
			if len(files) == 0 {
				// Nothing the registry could handle — an empty or
				// all-unsupported directory is a caller mistake worth
				// failing on, not a silent no-op.
				return fmt.Errorf("no ingestible files in %s", arg)
			}
			for _, f := range files {
				expandedSrcs = append(expandedSrcs, f)
				expanded[f] = true
			}
			continue
		}
		expandedSrcs = append(expandedSrcs, arg)
		explicit[arg] = true
	}
	sources = expandedSrcs

	docs := make([]*extract.Doc, 0, len(sources))
	srcs := make([]string, 0, len(sources)) // the arguments docs carries — shorter than sources when F.I2 drops one
	for _, src := range sources {
		doc, err := ex.Extract(ctx, src)
		if err != nil {
			// 004 F.I2: the text sniff is the backend's verdict (F.E3), and
			// for a walker-selected file it is a soft skip — a folder always
			// holds some binary alongside the notes, and one of them must
			// not fail the rest. An EXPLICIT argument keeps today's hard
			// failure: the caller named that exact file, so silence would
			// hide the reason the command did nothing. Naming a file both
			// ways — folder and outright, `lw ingest notes/ notes/nul.md` —
			// is explicit: the exception exists for the caller's own
			// spelling, and it wins over the folder the file also rode in
			// through.
			if expanded[src] && !explicit[src] && errors.Is(err, extract.ErrNotText) {
				fmt.Printf("skipped %s: not text\n", src)
				continue
			}
			return fmt.Errorf("extract %s: %w", src, err)
		}
		if *kindFlag != "" {
			doc.Kind = *kindFlag
		}
		docs = append(docs, doc)
		srcs = append(srcs, src)
	}

	// The engine opens before config.Load and the agent is built (A-807):
	// the duplicate pre-check below reads the committed raw sources, and a
	// vault that already holds everything must exit 0 without ever
	// resolving an API key.
	e, err := stage.OpenEngine(root)
	if err != nil {
		return fmt.Errorf("open engine: %w", err)
	}
	defer e.Close()

	// A-807 (BUG-2): drop every source whose body the vault already holds,
	// or that an earlier source of this command already carried, BEFORE
	// building the agent, opening a changeset, or spending an LLM round —
	// re-ingesting committed content used to burn a whole agent turn only
	// to end in "agent proposed nothing". The hash is tools.SourceBodySHA,
	// the same number stage.ingest_source records, so this check and the
	// tool can never disagree about what the vault holds.
	committed := e.Vault().RawSources()
	seenBody := make(map[string]string) // body sha -> the first source argument carrying it
	keptSrcs := make([]string, 0, len(sources))
	keptDocs := make([]*extract.Doc, 0, len(docs))
	for i, src := range srcs {
		sha := tools.SourceBodySHA(docs[i].Markdown)
		skip := ""
		for _, r := range committed {
			if r.SHA256 == sha {
				skip = fmt.Sprintf("skipped %s: already in the vault at %s", src, r.Path)
				break
			}
		}
		if skip == "" {
			if first, dup := seenBody[sha]; dup {
				skip = fmt.Sprintf("skipped %s: same content as %s", src, first)
			}
		}
		if skip != "" {
			fmt.Println(skip)
			continue
		}
		seenBody[sha] = src
		keptSrcs = append(keptSrcs, src)
		keptDocs = append(keptDocs, docs[i])
	}
	if len(keptSrcs) == 0 {
		fmt.Println("nothing to ingest: every source is already in the vault")
		return nil
	}

	// 004 F.I4 moved config.Load ahead of the scratch staging it used to
	// follow: the limit check below needs Limits.ContextTokens, and the
	// check must run before the agent is built or a changeset opened. Load
	// only reads config files — the API key stays unresolved until
	// newIngestAgent (A-807's "a fully-ingested vault exits 0 without a
	// key" still holds, since that path returns above).
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// 004 F.I4 (correction #3): the per-invocation limits apply only when
	// at least one argument was a directory — a file/URL-only invocation is
	// byte-identical to pre-004 behaviour, limit-free. The counts are of
	// KEPT sources only (F.I3: dedupe-skipped files do not count), and
	// capBytes mirrors the agent's own context budget
	// (llm.limits.context_tokens × 4 bytes/token × 25%): one ingest should
	// never be able to hand the curator more than a quarter of the context
	// it will later be read back into. Over either cap the command fails
	// here — no changeset, no agent, no key resolved.
	ingestBytes := 0
	for _, doc := range keptDocs {
		ingestBytes += len(doc.Markdown)
	}
	capBytes := cfg.Limits.ContextTokens * 4 * 25 / 100
	if hadDir && (len(keptSrcs) > ingestMaxFiles || ingestBytes > capBytes) {
		return fmt.Errorf("%d files (%d KB) to ingest; the limit is %d files and %d KB per ingest (llm.limits.context_tokens %d × 4 × 25%%). Split the folder into smaller ones.",
			len(keptSrcs), ingestKB(ingestBytes), ingestMaxFiles, ingestKB(capBytes), cfg.Limits.ContextTokens)
	}

	// 004 F.I5: --dry-run stops here — expansion, extraction, dedupe and
	// the limit check have all run, but nothing is opened: no scratch, no
	// session, no agent construction, no changeset. It reports the verdict
	// for folder and file-only invocations alike.
	if *dryRun {
		for _, src := range keptSrcs {
			fmt.Println("would ingest " + src)
		}
		fmt.Printf("within limits: %d files, %d KB (limit %d files, %d KB)\n",
			len(keptSrcs), ingestKB(ingestBytes), ingestMaxFiles, ingestKB(capBytes))
		return nil
	}

	// stage.ingest_source only ever accepts a local path (it refuses
	// http/https outright), so every extracted Doc is written to a scratch
	// local file the agent can hand that tool regardless of what the
	// original source argument was.
	tmpDir, err := os.MkdirTemp("", "lw-ingest-*")
	if err != nil {
		return fmt.Errorf("create scratch directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// pre lets the agent's stage.ingest_source tool re-extract each scratch
	// path back to the Doc already produced above, provenance corrected —
	// fixing C-123, where a bare extract.NewFile() re-extraction recorded
	// the scratch path itself (removed by the tmpDir cleanup below the
	// moment this process exits) as the raw source's source_url.
	pre := newPreExtracted()
	items := make([]ingestItem, 0, len(keptSrcs))
	for i, src := range keptSrcs {
		doc := keptDocs[i]
		// doc.SourceURL is already src verbatim — both NewHTML and
		// NewFile set it to the uri they were handed (backbone §10) — so a
		// URL source needs no change. A local path is instead resolved to
		// absolute + cleaned (00-conventions.md §3: never store a path a
		// caller could have typed relative to a directory that no longer
		// matches by the time a human reviews the committed raw file).
		corrected := *doc
		if !isURLSource(src) {
			if abs, absErr := filepath.Abs(src); absErr == nil {
				corrected.SourceURL = abs
			}
		}

		// The scratch name follows the ORIGINAL source (U6) — its base
		// name, slugified, numbered per source — not the extracted title.
		// A-807 (BUG-1): the number is a directory of its own
		// (<tmpDir>/<NN>/<slug>.md, keeping two same-named sources apart),
		// so the file name stage.ingest_source's basename fallback sees is
		// the user's own — an untitled "m3-note.md" lands at
		// raw/articles/m3-note.md, never at a path carrying lw's scratch
		// numbering. A source whose base name slugifies to nothing falls
		// back to SuggestPath's base, as before 008.
		slug := scratchSlug(src)
		if slug == "" {
			slug = strings.TrimSuffix(filepath.Base(extract.SuggestPath(doc)), ".md")
		}
		tmpPath := filepath.Join(tmpDir, fmt.Sprintf("%02d", i+1), slug+".md")
		if err := os.MkdirAll(filepath.Dir(tmpPath), 0o700); err != nil {
			return fmt.Errorf("create scratch directory: %w", err)
		}
		if err := os.WriteFile(tmpPath, []byte(doc.Markdown), 0o644); err != nil {
			return fmt.Errorf("write scratch file: %w", err)
		}
		pre.stage(tmpPath, corrected)
		items = append(items, ingestItem{path: tmpPath, kind: doc.Kind, title: doc.Title, source: src})
	}

	// Construct the agent (which resolves the configured API key) before
	// opening a changeset: a bad or missing key must fail with nothing
	// opened at all, not an empty changeset the caller has to notice and
	// clean up by hand. The tools' Extract is pre chained in front of the
	// shared agentExtractors() chain (U7) — see preExtracted above.
	sessions := agent.NewFileSessions(e.Vault().Root())
	toolExtract := extract.Chain(pre, agentExtractors())
	ag, err := newIngestAgent(e, cfg, sessions, toolExtract)
	if err != nil {
		return fmt.Errorf("construct agent: %w", err)
	}

	cs, err := e.OpenChangeset(ingestIntent(sources), stage.Author{Kind: "agent", Model: cfg.LLM.Model})
	if err != nil {
		return fmt.Errorf("open changeset: %w", err)
	}
	sess, err := sessions.Create(cs.ID)
	if err != nil {
		return rejectAndReturn(e, fmt.Errorf("create session: %w", err))
	}

	sendErr := runAgentTurn(ctx, ag, sess.ID, buildIngestMessage(items), os.Stdout)

	final, curErr := e.Current()
	if curErr != nil {
		if sendErr != nil {
			return rejectAndReturn(e, fmt.Errorf("%w (and reading back the changeset failed: %v)", agentErrorHint(sendErr, cfg.LLM.MaxTokens, true), curErr))
		}
		return rejectAndReturn(e, fmt.Errorf("read back changeset %s: %w", cs.ID, curErr))
	}

	if sendErr != nil {
		return rejectAndReturn(e, agentErrorHint(sendErr, cfg.LLM.MaxTokens, true))
	}

	// A clean turn that staged nothing is not a success (U1's sibling,
	// 000006's zero-page commit): reject the empty changeset with the
	// contract's own message rather than printing a summary of nothing.
	if len(final.Live()) == 0 {
		return rejectAndReturn(e, errors.New("agent proposed nothing for this ingest; the changeset was rejected"))
	}

	fmt.Println()
	printChangesetSummary(os.Stdout, final)

	// A raw-only ingest (live raw sources, no create_page) still succeeds —
	// the changeset stays open for review — but says so, so the absence of
	// pages is visible to the human the changeset waits for.
	if _, rawOnly := ingestOnlyPaths(final); rawOnly {
		fmt.Println("warning: 0 pages proposed — only raw source(s) staged; review before committing")
	}

	// A-807 (FINDING-3): an ingest whose changeset lint-regresses must say
	// so here — not let the user discover it only when lw commit refuses.
	// The warning is informational: exit 0, changeset left open for review.
	warnIfLintRegresses(e)
	return nil
}

// rejectAndReturn rolls back the changeset e currently has open when a
// later step of cmdIngest fails — agent error, a read-back failure, or a
// session-creation failure — so a failed ingest never leaves one stuck in
// changesets/open/ (C-113): Engine.Reject moves it to
// changesets/rejected/, never deletes it, keeping the failed attempt in
// the audit trail. It must not mask origErr: a Reject failure is reported
// alongside it, never in its place, so a human sees the real cause of the
// failure and, separately, that the rollback itself needs attention.
func rejectAndReturn(e *stage.Engine, origErr error) error {
	if rerr := e.Reject(origErr.Error()); rerr != nil {
		return fmt.Errorf("%w (and rejecting the changeset failed: %v)", origErr, rerr)
	}
	return origErr
}

// ingestOnlyPaths reports cs's live ingest_source paths, in live-op order,
// and whether cs is raw-only: at least one live ingest_source op and zero
// live create_page ops (008 contract §6). Shared by `lw ingest`'s
// post-turn warning and `lw commit`'s pre-commit one — the same definition
// review's own raw-only confirmation applies (internal/ui/review).
func ingestOnlyPaths(cs *stage.Changeset) (paths []string, rawOnly bool) {
	creates := 0
	for _, op := range cs.Live() {
		switch op.Kind {
		case stage.OpIngestSource:
			paths = append(paths, op.Path)
		case stage.OpCreatePage:
			creates++
		}
	}
	return paths, len(paths) > 0 && creates == 0
}

// ingestItem is one extracted source, staged locally and described to the
// agent so it can call stage.ingest_source on it.
type ingestItem struct {
	path   string // local scratch path the agent should ingest
	kind   string
	title  string
	source string // the original argument (URL or path) — for the message only
}

// ingestIntent builds a changeset intent line from the sources given on
// the command line.
func ingestIntent(sources []string) string {
	return "ingest " + strings.Join(sources, ", ")
}

// buildIngestMessage is the first user message sent to the agent: it
// names every scratch file and asks the agent to ingest each one, then
// compile or update wiki pages from the newly ingested content.
func buildIngestMessage(items []ingestItem) string {
	var b strings.Builder
	b.WriteString("New source material has been extracted and saved locally, ready to ingest. The changeset for this ingest is already open, so do not call stage.open. For each file below: call stage.ingest_source with its local path (and the given kind, if it does not match) — its result names the exact raw/ path to read next with raw.get — then create or update wiki pages that faithfully reflect it, following the schema and citing the new raw source. When you are done, call stage.close to summarize the proposed changeset.\n\n")
	for _, it := range items {
		fmt.Fprintf(&b, "- path: %s\n  kind: %s\n  title: %s\n  original source: %s\n", it.path, it.kind, it.title, it.source)
	}
	return b.String()
}

// printChangesetSummary writes cs's id, intent, op count and each live
// op's id/kind/path — in cs.Ops order, the order Append assigned them, so
// the summary is exactly what a human sees again in `lw status`/`lw diff`.
func printChangesetSummary(w io.Writer, cs *stage.Changeset) {
	fmt.Fprintf(w, "opened changeset %s: %s (%d op(s))\n", cs.ID, cs.Intent, len(cs.Live()))
	for _, op := range cs.Live() {
		fmt.Fprintf(w, "  %s %s %s\n", op.ID, op.Kind, op.Path)
	}
}
