package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/extract/cache"
	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/slug"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/web"
)

// agent_deps.go holds everything cmd/lw needs to build and drive an
// agent.Agent: the constructors (newIngestAgent, newAgent, agentExtractors,
// ingestExtractors, webSearchProvider, agentToolDeps), the preExtracted
// chain link cmdIngest stages scratch docs into, the scratch-name slug
// (U6), and the one error hint every verb applies to a failed turn (U1).
// Split out of cmd_ingest.go by 008 — which also added agentExtractors
// (U7), so query, lint --fix and the TUI stop handing their agents a bare
// extract.NewFile() that could not fetch a page or read a saved one. 007
// F.W1 made ingestExtractors the one constructor (correction #1: cmdIngest
// used to hand-roll a second copy of the chain) and F.W3 put the per-source
// byte cap on the agent path.

// ingestExtractors returns the extractor chain every `lw ingest` invocation
// extracts sources over, and — wrapped by the F.W3 cap in agentExtractors —
// every agent's tool registry is built over: remote HTML through
// extract.NewHTTPClient(httpTimeout) — the house client (010 contract §1),
// which sends the lw User-Agent, caps the body and validates every redirect
// hop — local files, saved .html pages and .md/.txt sources alike through
// extract.NewFile, and local PDFs through the Docling sidecar (007 T1)
// behind the extraction cache (007 T2) under
// <root>/.llmwiki/cache/extract. root == "" skips the cache (cache.New
// returns the backend unwrapped), and cfg == nil means config.Default()'s
// values — newAgent(nil, nil, nil) in the tests relies on both. cmdIngest,
// newAgent and mcpDeps all derive from this one constructor, so the CLI and
// the model extract the same sources the same way no matter which verb is
// driving.
func ingestExtractors(root string, cfg *config.Config) extract.Extractor {
	if cfg == nil {
		cfg = config.Default()
	}
	cacheDir := ""
	if root != "" {
		cacheDir = filepath.Join(root, stateDirName, "cache", "extract")
	}
	pdfCfg := extract.PDFConfig{Command: cfg.Extract.Argv(), Timeout: cfg.Extract.TimeoutDuration()}
	return extract.Chain(
		extract.NewHTML(extract.NewHTTPClient(httpTimeout)),
		extract.NewFile(),
		cache.New(extract.NewPDF(pdfCfg), cacheDir, extract.PDFExtractorID, pdfVersionOnce(pdfCfg)),
	)
}

// pdfVersionProbeTimeout bounds the cache's one-time sidecar --version
// probe. lw doctor bounds the same probe at doctorProbeTimeout because a
// hung endpoint or sidecar must not hang a health check; an ingest hangs
// all the same when the probe is unbounded, so the cache gives up here,
// caches the error for the process, and extraction proceeds uncached.
var pdfVersionProbeTimeout = 20 * time.Second

// pdfVersionOnce is the cache's version func (007 F.W1): PDFVersion of the
// configured sidecar. cache.New memoizes it — the probe shells out to the
// sidecar and costs ~4 s, so a cache hit must not re-pay it.
func pdfVersionOnce(pdfCfg extract.PDFConfig) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, pdfVersionProbeTimeout)
		defer cancel()
		return extract.PDFVersion(ctx, pdfCfg)
	}
}

// agentExtractors returns the chain every agent's tool registry is built
// over (U7): ingestExtractors wrapped by the per-source byte cap (007
// F.W3). The CLI's own limit check (F.W2) fires before an ingest opens
// anything, but the agent path — stage.ingest_source called by the model
// mid-turn, over MCP, or from query, lint --fix and the TUI — has no such
// gate ahead of it, so a single extracted Doc over the budget must be
// refused here, never staged.
func agentExtractors(root string, cfg *config.Config) extract.Extractor {
	if cfg == nil {
		cfg = config.Default()
	}
	return perSourceCap{
		inner:    ingestExtractors(root, cfg),
		capBytes: ingestCapBytes(cfg.Limits.ContextTokens),
		tokens:   cfg.Limits.ContextTokens,
	}
}

// perSourceCap is F.W3's agent-path wrapper. Its error text is frozen:
// `extract: <uri>: extracted <B> KB, over the <C> KB per-source limit
// (llm.limits.context_tokens <T> × 4 × 75%)` — stage.ingest_source turns it
// into an IsError result the model can read (stage_source.go), so the KB
// rounding is ingestKB's, the same unit the CLI's limit message quotes.
type perSourceCap struct {
	inner    extract.Extractor
	capBytes int
	tokens   int
}

// CanHandle delegates: the cap changes what may be staged, never which URIs
// the chain accepts.
func (c perSourceCap) CanHandle(uri string) bool { return c.inner.CanHandle(uri) }

// Extract delegates and holds the result to the per-source byte cap — the
// agent-path sibling of cmdIngest's F.W2 check, which runs before anything
// is opened but cannot see what the model extracts later in the turn.
func (c perSourceCap) Extract(ctx context.Context, uri string) (*extract.Doc, error) {
	doc, err := c.inner.Extract(ctx, uri)
	if err != nil {
		return nil, err
	}
	if n := len(doc.Markdown); n > c.capBytes {
		return nil, fmt.Errorf("extract: %s: extracted %d KB, over the %d KB per-source limit (llm.limits.context_tokens %d × 4 × 75%%)",
			uri, ingestKB(n), ingestKB(c.capBytes), c.tokens)
	}
	return doc, nil
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
	client := llm.New(ingestLLMConfig(cfg, apiKey))
	reg := tools.NewRegistry(agentToolDeps(e, cfg, ex))
	loopCfg := agent.LoopConfig{
		MaxToolRounds: cfg.Limits.MaxToolRounds,
		ContextTokens: cfg.Limits.ContextTokens,
	}
	return agent.NewLoop(client, reg, sessions, e, loopCfg), nil
}

// ingestLLMConfig maps the [llm] config onto the llm.Config the agent client
// is built over — the seam newIngestAgent hands llm.New, split out so the
// mapping (including 026 T3's stall bound, F.K4) is testable without a
// network. StallTimeout goes through cfg.LLM.StallTimeoutDuration, which
// applies DefaultStallTimeout when the key is absent; cmd_doctor.go's probe
// keeps its own doctorProbeTimeout and is deliberately not wired to this.
func ingestLLMConfig(cfg *config.Config, apiKey string) llm.Config {
	return llm.Config{
		BaseURL:      cfg.LLM.BaseURL,
		Model:        cfg.LLM.Model,
		APIKey:       apiKey,
		Temperature:  cfg.LLM.Temperature,
		MaxTokens:    cfg.LLM.MaxTokens,
		Thinking:     cfg.LLM.Thinking,
		StallTimeout: cfg.LLM.StallTimeoutDuration(),
	}
}

// newAgent is the seam cmd_query.go, cmd_lint.go and cmd_tui.go call and
// swap in their own tests. Since 008's U7 it hands the agent the full
// shared chain instead of the bare extract.NewFile() it built inline
// before; since 007's F.W1/F.W3 that chain is agentExtractors over the
// engine's vault root — the extraction cache and the sidecar live under it
// — and nil for the engine (as the tests call it) means root "", which
// skips the cache. The signature is unchanged, so none of those callers or
// tests are affected.
var newAgent = func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
	root := ""
	if e != nil {
		root = e.Vault().Root()
	}
	return newIngestAgent(e, cfg, sessions, agentExtractors(root, cfg))
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
