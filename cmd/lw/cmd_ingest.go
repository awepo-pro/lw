package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/tools"
)

// httpTimeout bounds every fetch NewHTML's Extract makes on ingest's
// behalf — a hung remote server must not hang the whole command.
const httpTimeout = 30 * time.Second

// newIngestAgent constructs the agent.Agent used by ingest, query and
// lint --fix (cmd_ingest.go, cmd_query.go, cmd_lint.go): the single
// package-level seam this subtask's brief asks for, since agent.Agent is
// a small interface but internal/agent's own fake-client seam (backbone
// §9, D-CS) is unexported and lives in another package. A test in package
// main swaps this var for a function returning a fake agent.Agent —
// stage.Engine.Append is enough for a fake to propose real ops with no
// network and no LLM at all.
//
// Deps are assembled exactly as cmd_mcp.go's cmdMCP does — the same
// findVaultRoot -> stage.OpenEngine -> tools.NewRegistry(tools.Deps{...})
// sequence (backbone §9's C-104 pinned instruction) — with one addition:
// Extract, which cmd_mcp.go's Deps leaves unset because S3 shipped before
// this package existed. Sessions are the caller's choice: ingest and
// lint --fix pass agent.NewFileSessions(root) so the session travels with
// the real changeset (backbone §9, C-102); query passes its own ephemeral,
// in-process store (cmd_query.go) so no changeset is ever touched.
//
// ex is the extract.Extractor the tools' Deps.Extract is built over — the
// ingest-local seam C-123's fix needs (backbone §6, §10): cmdIngest calls
// this directly with a Chain that resolves each scratch path it wrote back
// to the already-extracted Doc, corrected SourceURL and all (see
// preExtracted below), rather than letting stage.ingest_source re-extract
// the scratch file with a bare extract.NewFile() and record the scratch
// path itself as the raw source's provenance. newAgent (below) keeps its
// existing three-argument signature — query, lint --fix and the TUI all
// swap it directly in their own tests — by delegating to this with
// extract.NewFile(), exactly what it built inline before.
var newIngestAgent = func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
	apiKey, err := cfg.ResolveAPIKey()
	if err != nil {
		return nil, fmt.Errorf("resolve api key: %w", err)
	}
	client := llm.New(llm.Config{
		BaseURL:     cfg.LLM.BaseURL,
		Model:       cfg.LLM.Model,
		APIKey:      apiKey,
		Temperature: cfg.LLM.Temperature,
		MaxTokens:   cfg.LLM.MaxTokens,
	})
	reg := tools.NewRegistry(tools.Deps{
		Vault:   e.Vault(),
		Index:   e.Index(),
		Engine:  e,
		Extract: ex,
		Author:  stage.Author{Kind: "agent", Model: cfg.LLM.Model},
	})
	loopCfg := agent.LoopConfig{
		MaxToolRounds: cfg.Limits.MaxToolRounds,
		ContextTokens: cfg.Limits.ContextTokens,
	}
	return agent.NewLoop(client, reg, sessions, e, loopCfg), nil
}

// newAgent is the seam cmd_query.go, cmd_lint.go and cmd_tui.go call and
// swap in their own tests. It is unchanged in signature and behaviour —
// still extract.NewFile() — so none of those callers or tests are
// affected by C-123's fix, which only changes what cmdIngest itself calls.
var newAgent = func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
	return newIngestAgent(e, cfg, sessions, extract.NewFile())
}

// preExtracted is the extract.Extractor cmdIngest hands the agent's tools
// for one ingest (C-123). stage.ingest_source only ever accepts a local
// path, so the agent is always given a scratch file cmdIngest already
// wrote — but re-extracting that scratch file with a bare extract.NewFile()
// sets Doc.SourceURL to the scratch path itself, and that path is removed
// (os.RemoveAll on a deferred temp dir) the moment lw ingest exits: every
// committed raw file then records a provenance that no longer exists. This
// type closes that gap by remembering, per scratch path, the Doc cmdIngest
// already extracted from the ORIGINAL source — SourceURL corrected back to
// that original argument before the doc is ever staged (stage below) — and
// handing back a copy of exactly that Doc when the tool re-extracts the
// path. CanHandle is true only for paths this ingest staged; every other
// uri is left to the next extractor in the chain, extract.NewFile().
type preExtracted struct {
	docs map[string]extract.Doc
}

// newPreExtracted returns an empty preExtracted, ready for stage.
func newPreExtracted() *preExtracted {
	return &preExtracted{docs: make(map[string]extract.Doc)}
}

// stage records doc — with SourceURL already corrected — as the result
// preExtracted returns for a future Extract(ctx, path).
func (p *preExtracted) stage(path string, doc extract.Doc) {
	p.docs[path] = doc
}

// CanHandle reports whether uri is a path preExtracted staged.
func (p *preExtracted) CanHandle(uri string) bool {
	_, ok := p.docs[uri]
	return ok
}

// Extract returns a copy of the Doc staged for uri, so the caller's own
// mutations (e.g. stage.ingest_source trimming Markdown) never alter the
// map entry a second call to the same path would see.
func (p *preExtracted) Extract(ctx context.Context, uri string) (*extract.Doc, error) {
	doc, ok := p.docs[uri]
	if !ok {
		return nil, fmt.Errorf("preExtracted: no document staged for %s", uri)
	}
	out := doc
	return &out, nil
}

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

// cmdIngest extracts one or more sources (a URL or a local path) into
// deterministic markdown, opens a changeset, and hands it to the agent to
// ingest the raw content and compile wiki pages from it — leaving the
// changeset open for human review (/.dev-notes/PLAN-v1.md §9.4; nothing in this verb
// commits).
func cmdIngest(args []string) error {
	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	kindFlag := fs.String("kind", "", "override the detected kind for every source: article|paper|transcript")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	sources := fs.Args()
	if len(sources) == 0 {
		fmt.Fprintln(os.Stderr, "usage: lw ingest <url|path>...")
		return &exitError{code: 2}
	}

	root, err := findVaultRoot(*vaultPath)
	if err != nil {
		return err
	}

	// Extract every source before touching the staging engine at all: a
	// bad source fails the whole command with nothing opened, rather than
	// leaving a changeset with only some of the requested sources in it.
	ex := extract.Chain(extract.NewHTML(&http.Client{Timeout: httpTimeout}), extract.NewFile())
	ctx := context.Background()
	docs := make([]*extract.Doc, 0, len(sources))
	for _, src := range sources {
		doc, err := ex.Extract(ctx, src)
		if err != nil {
			return fmt.Errorf("extract %s: %w", src, err)
		}
		if *kindFlag != "" {
			doc.Kind = *kindFlag
		}
		docs = append(docs, doc)
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
	items := make([]ingestItem, 0, len(docs))
	for i, doc := range docs {
		// doc.SourceURL is already sources[i] verbatim — both NewHTML and
		// NewFile set it to the uri they were handed (backbone §10) — so a
		// URL source needs no change. A local path is instead resolved to
		// absolute + cleaned (00-conventions.md §3: never store a path a
		// caller could have typed relative to a directory that no longer
		// matches by the time a human reviews the committed raw file).
		corrected := *doc
		if !isURLSource(sources[i]) {
			if abs, absErr := filepath.Abs(sources[i]); absErr == nil {
				corrected.SourceURL = abs
			}
		}

		name := filepath.Base(extract.SuggestPath(doc))
		tmpPath := filepath.Join(tmpDir, fmt.Sprintf("%02d-%s", i+1, name))
		if err := os.WriteFile(tmpPath, []byte(doc.Markdown), 0o644); err != nil {
			return fmt.Errorf("write scratch file: %w", err)
		}
		pre.stage(tmpPath, corrected)
		items = append(items, ingestItem{path: tmpPath, kind: doc.Kind, title: doc.Title, source: sources[i]})
	}

	e, err := stage.OpenEngine(root)
	if err != nil {
		return fmt.Errorf("open engine: %w", err)
	}
	defer e.Close()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Construct the agent (which resolves the configured API key) before
	// opening a changeset: a bad or missing key must fail with nothing
	// opened at all, not an empty changeset the caller has to notice and
	// clean up by hand. newIngestAgent (not newAgent) so the tools' Extract
	// is pre chained in front of extract.NewFile() — see preExtracted above.
	sessions := agent.NewFileSessions(e.Vault().Root())
	toolExtract := extract.Chain(pre, extract.NewFile())
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
			return rejectAndReturn(e, fmt.Errorf("agent turn: %w (and reading back the changeset failed: %v)", sendErr, curErr))
		}
		return rejectAndReturn(e, fmt.Errorf("read back changeset %s: %w", cs.ID, curErr))
	}

	if sendErr != nil {
		return rejectAndReturn(e, fmt.Errorf("agent turn: %w", sendErr))
	}

	fmt.Println()
	printChangesetSummary(os.Stdout, final)
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
