package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
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

// newAgent constructs the agent.Agent used by ingest, query and lint
// --fix (cmd_ingest.go, cmd_query.go, cmd_lint.go): the single
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
var newAgent = func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
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
		Extract: extract.NewFile(),
		Author:  stage.Author{Kind: "agent", Model: cfg.LLM.Model},
	})
	loopCfg := agent.LoopConfig{
		MaxToolRounds: cfg.Limits.MaxToolRounds,
		ContextTokens: cfg.Limits.ContextTokens,
	}
	return agent.NewLoop(client, reg, sessions, e, loopCfg), nil
}

// runAgentTurn drives one agent.Agent turn to completion: it starts
// ag.Send in a goroutine (the caller-runs-a-goroutine-and-ranges-over-out
// shape backbone §9's "who owns out" contract, C-105, requires of every
// Send caller), streams every TextDelta to w as it arrives, and returns
// once Send has both closed the channel and returned. Shared by ingest,
// query and lint --fix.
func runAgentTurn(ctx context.Context, ag agent.Agent, sessionID, msg string, w io.Writer) error {
	out := make(chan agent.Event)
	done := make(chan struct{})
	var sendErr error
	go func() {
		sendErr = ag.Send(ctx, sessionID, msg, out)
		close(done)
	}()
	for ev := range out {
		if td, ok := ev.(agent.TextDelta); ok {
			fmt.Fprint(w, td.Text)
		}
	}
	<-done
	return sendErr
}

// cmdIngest extracts one or more sources (a URL or a local path) into
// deterministic markdown, opens a changeset, and hands it to the agent to
// ingest the raw content and compile wiki pages from it — leaving the
// changeset open for human review (/PLAN.md §9.4; nothing in this verb
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

	items := make([]ingestItem, 0, len(docs))
	for i, doc := range docs {
		name := filepath.Base(extract.SuggestPath(doc))
		tmpPath := filepath.Join(tmpDir, fmt.Sprintf("%02d-%s", i+1, name))
		if err := os.WriteFile(tmpPath, []byte(doc.Markdown), 0o644); err != nil {
			return fmt.Errorf("write scratch file: %w", err)
		}
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
	// clean up by hand.
	sessions := agent.NewFileSessions(e.Vault().Root())
	ag, err := newAgent(e, cfg, sessions)
	if err != nil {
		return fmt.Errorf("construct agent: %w", err)
	}

	cs, err := e.OpenChangeset(ingestIntent(sources), stage.Author{Kind: "agent", Model: cfg.LLM.Model})
	if err != nil {
		return fmt.Errorf("open changeset: %w", err)
	}
	sess, err := sessions.Create(cs.ID)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}

	sendErr := runAgentTurn(ctx, ag, sess.ID, buildIngestMessage(items), os.Stdout)

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
	b.WriteString("New source material has been extracted and saved locally, ready to ingest. For each file below: call stage.ingest_source with its local path (and the given kind, if it does not match), read the resulting raw source, and create or update wiki pages that faithfully reflect it, following the schema and citing the new raw source. When you are done, call stage.close to summarize the proposed changeset.\n\n")
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
