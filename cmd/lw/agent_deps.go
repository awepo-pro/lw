package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/slug"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/web"
)

// agent_deps.go holds everything cmd/lw needs to build and drive an
// agent.Agent: the constructors (newIngestAgent, newAgent, agentExtractors,
// webSearchProvider, agentToolDeps), the preExtracted chain link cmdIngest
// stages scratch docs into, the scratch-name slug (U6), and the one error
// hint every verb applies to a failed turn (U1). Split out of cmd_ingest.go
// by 008 — which also added agentExtractors (U7), so query, lint --fix and
// the TUI stop handing their agents a bare extract.NewFile() that could not
// fetch a page or read a saved one.

// agentExtractors returns the extractor chain every agent's tool registry
// is built over (U7): remote HTML through extract.NewHTTPClient(httpTimeout)
// — the house client (010 contract §1), which sends the lw User-Agent,
// caps the body and validates every redirect hop — and local files, saved
// .html pages and .md/.txt sources alike, through extract.NewFile. newAgent,
// mcpDeps and cmdIngest's tool chain all share it, so the model can fetch or
// read the same sources no matter which verb is driving it.
func agentExtractors() extract.Extractor {
	return extract.Chain(extract.NewHTML(extract.NewHTTPClient(httpTimeout)), extract.NewFile())
}

// webSearchProvider builds the provider behind Deps.Search (010 contract
// §4): a *web.Tavily over the house HTTP client when [web].api_key is
// configured and resolves, nil — meaning web.search is simply not offered,
// never denied — when the provider is not the built-in one, the key is
// empty, or the key cannot be resolved (an unset environment variable, an
// unsupported keyring reference). The provider guard keeps the wiring in
// step with doctor's unknown-provider warn (A-10-5): an unknown provider
// with a key offers no verb, and `lw doctor` explains why. Resolution goes
// through config.ResolveAPIKey itself: the web key follows the llm.api_key
// reference rules, so it is resolved by lending the reference to a copy's
// llm.api_key rather than by a second copy of the env:/keyring: logic.
func webSearchProvider(cfg *config.Config) web.SearchProvider {
	if cfg.Web.Provider != "tavily" {
		return nil
	}
	if cfg.Web.APIKey == "" {
		return nil
	}
	swap := *cfg
	swap.LLM.APIKey = cfg.Web.APIKey
	key, err := swap.ResolveAPIKey()
	if err != nil || key == "" {
		return nil
	}
	return web.NewTavily(key, extract.NewHTTPClient(httpTimeout))
}

// agentToolDeps assembles the tools.Deps every CLI agent's registry is built
// over: the engine's vault, index and staging engine, the extractor chain ex,
// the web search provider when one is configured, and the agent authorship.
// Split from newIngestAgent so the Search wiring — the one field 010 adds —
// is testable without building a loop over a real LLM client.
func agentToolDeps(e *stage.Engine, cfg *config.Config, ex extract.Extractor) tools.Deps {
	return tools.Deps{
		Vault:   e.Vault(),
		Index:   e.Index(),
		Engine:  e,
		Extract: ex,
		Search:  webSearchProvider(cfg),
		Author:  stage.Author{Kind: "agent", Model: cfg.LLM.Model},
	}
}

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
// the scratch file and record the scratch path itself as the raw source's
// provenance. Callers without a staged chain — query, lint --fix and the
// TUI — pass agentExtractors() (U7) via newAgent below.
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
		Thinking:    cfg.LLM.Thinking,
	})
	reg := tools.NewRegistry(agentToolDeps(e, cfg, ex))
	loopCfg := agent.LoopConfig{
		MaxToolRounds: cfg.Limits.MaxToolRounds,
		ContextTokens: cfg.Limits.ContextTokens,
	}
	return agent.NewLoop(client, reg, sessions, e, loopCfg), nil
}

// newAgent is the seam cmd_query.go, cmd_lint.go and cmd_tui.go call and
// swap in their own tests. Since 008's U7 it hands the agent the full
// agentExtractors() chain — remote HTML and local files — instead of the
// bare extract.NewFile() it built inline before, matching what mcpDeps and
// cmdIngest's own tool chain already offered; the signature is unchanged,
// so none of those callers or tests are affected.
var newAgent = func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
	return newIngestAgent(e, cfg, sessions, agentExtractors())
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
// uri is left to the next extractor in the chain.
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

// scratchSlug derives the scratch file-name fragment cmdIngest writes one
// source's markdown under (U6): a local path contributes its base name
// without its extension, a URL its last non-empty path segment likewise.
// The fragment runs through slugFile; "" when nothing survives, which
// cmdIngest answers with extract.SuggestPath's base name as before.
func scratchSlug(src string) string {
	base := src
	if isURLSource(src) {
		u, err := url.Parse(src)
		if err != nil {
			return ""
		}
		base = ""
		for _, seg := range strings.Split(u.Path, "/") {
			if seg != "" {
				base = seg // the LAST non-empty segment wins
			}
		}
		if base == "" {
			return ""
		}
	} else {
		base = filepath.Base(src)
	}
	return slugFile(strings.TrimSuffix(base, filepath.Ext(base)))
}

// slugFile names the scratch file fragment cmdIngest writes one source
// under (U6): internal/slug.Make, the one slug rule the whole tree shares
// (A-805) — a lowercase ASCII name quoted back to the model that survives
// as a plain local file name. "" when nothing survives, in which case
// cmdIngest answers with extract.SuggestPath's base name as before. This
// used to be an ASCII-only copy of extract's slug, kept because
// suggestSlug was unexported and Unicode-aware; internal/slug replaces
// both the copy and the rule that kept non-ASCII letters in paths.
func slugFile(s string) string {
	return slug.Make(s)
}

// agentErrorHint turns a failed agent turn into the error ingest, query
// and lint --fix return (U1): a truncated turn (agent.ErrTruncated — the
// provider stopped at the output-token cap) names the configured budget
// and the fix, and the ingest form alone adds the rejection sentence,
// because ingest rolls its changeset back while query and lint --fix have
// no changeset of their own to reject (008 contract §6, C-808). Any other
// Send error keeps the pre-008 "agent turn: ..." wording untouched.
func agentErrorHint(sendErr error, maxTokens int, rejected bool) error {
	if !errors.Is(sendErr, agent.ErrTruncated) {
		return fmt.Errorf("agent turn: %w", sendErr)
	}
	err := fmt.Errorf("agent turn: %w: the output limit (llm.max_tokens = %d) was reached before the agent finished", sendErr, maxTokens)
	if rejected {
		err = fmt.Errorf("%w; the changeset was rejected", err)
	}
	return fmt.Errorf("%w. Raise it with: lw config set llm.max_tokens %d", err, config.Default().LLM.MaxTokens)
}
