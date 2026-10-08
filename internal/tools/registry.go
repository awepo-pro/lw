package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/vault"
	"github.com/awepo-pro/lw/internal/web"
)

// Tool is one callable the agent loop and the MCP transport both expose —
// one definition, two consumers (backbone §6).
type Tool struct {
	Name        string          // canonical dotted name, e.g. "wiki.search"
	Description string          // what the model reads; say when to use it
	Schema      json.RawMessage // JSON Schema for the arguments object
	ReadOnly    bool
	Handler     func(ctx context.Context, args json.RawMessage) (Result, error)
}

// Result is what a Tool.Handler returns.
//
// Contract (backbone §6): a validation failure — bad or missing arguments —
// returns Result{IsError:true, Content:"<what was wrong and how to fix
// it>"} with a nil error, so the model can self-correct. A non-nil error is
// reserved for engine failures and aborts the agent loop.
type Result struct {
	Content string // what the model sees — prose or compact JSON, never a page dump
	IsError bool
	Data    any // structured payload for in-process consumers (the TUI); not sent to the model
}

// Deps is everything a Tool's Handler needs. A handler never touches the
// filesystem itself: reads go through Vault, writes through Engine
// (00-conventions.md §5).
type Deps struct {
	Vault   *vault.Vault
	Index   *index.Index
	Engine  *stage.Engine
	Extract extract.Extractor
	Author  stage.Author
	// Search is the web search provider behind web.search (010 contract
	// §3). nil — no provider configured — means the verb is not offered at
	// all, never offered-and-failing.
	Search web.SearchProvider
	// Recompile is the vault paths of the committed raw sources this turn
	// compiles into wiki pages (055: `lw ingest --recompile`). Such a source
	// reaches the turn without a stage.ingest_source, so the 040 read log
	// would never hear of it and stage.close would let a skimmed one through;
	// NewRegistry seeds the log with each path and stage.close treats them as
	// live. Only the recompile turn sets it — every other registry (TUI, MCP,
	// query, lint) leaves it nil and behaves exactly as before.
	Recompile []string
	// reads is the 040 read log shared by raw.get, stage.ingest_source and
	// stage.close. It is not caller-supplied: NewRegistry makes a fresh one
	// on its own copy of Deps, so a registry's reads belong to that registry
	// and a Deps value handed to two registries gives each its own log. nil
	// (a Deps used without NewRegistry) disables the guard — every method of
	// a nil *readLog is a no-op.
	reads *readLog
}

// ErrUnknownTool is returned by Registry.Call for a name with no
// registered Tool.
var ErrUnknownTool = errors.New("tools: unknown tool")

// Registry holds the fixed tool set constructed over one Deps.
type Registry struct {
	deps  Deps
	tools map[string]Tool
}

// NewRegistry builds the tool registry over d. This subtask (S3-T1)
// registers the 7 read-only tools; S3-T2 added the 10 stage.* tools,
// bringing the total to the backbone's 17; 008 adds the read-only
// discovery tool raw.list as the 18th; 010 adds web.search as the 19th,
// registered only when d.Search is non-nil.
//
// 040: NewRegistry also makes the registry's one read log and puts it on
// its copy of d before any tool is built, so raw.get, stage.ingest_source
// and stage.close — which close over d — all see the same log. Each call
// makes a fresh one: a second registry over the same engine (another
// process, in 019's join) must not inherit what this one has read.
func NewRegistry(d Deps) *Registry {
	d.reads = newReadLog()
	// 055: the recompile turn's committed raws are read-log sources from the
	// first round. The slice is copied so the guard answers to the paths the
	// registry was built with, not to a caller's later edit of its own.
	d.Recompile = append([]string(nil), d.Recompile...)
	d.reads.seedRecompile(d.Vault, d.Recompile)
	r := &Registry{
		deps:  d,
		tools: make(map[string]Tool),
	}
	for _, t := range readTools(d) {
		r.tools[t.Name] = t
	}
	for _, t := range stageTools(d) {
		r.tools[t.Name] = t
	}
	return r
}

// readTools returns the read-only tools: backbone §6's rows 1-7, plus 008's
// raw.list, registered directly after raw.get (008 contract §4.2), plus
// 010's web.search — present only when a search provider is wired, since
// an unbacked verb is not offered, not offered-and-failing (010 contract
// §3).
func readTools(d Deps) []Tool {
	tools := []Tool{
		vaultOrientTool(d),
		wikiSearchTool(d),
		wikiGetTool(d),
		wikiNeighborsTool(d),
		wikiBacklinksTool(d),
		rawGetTool(d),
		rawListTool(d),
		wikiLintTool(d),
	}
	if d.Search != nil {
		tools = append(tools, webSearchTool(d))
	}
	return tools
}

// List returns every registered Tool, sorted by Name.
func (r *Registry) List() []Tool {
	names := make([]string, 0, len(r.tools))
	for n := range r.tools {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make([]Tool, 0, len(names))
	for _, n := range names {
		out = append(out, r.tools[n])
	}
	return out
}

// Get returns the Tool registered under name, and whether one was found.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Call dispatches to the named Tool's Handler.
//
// Contract (backbone §6): a name with no registered Tool returns
// ErrUnknownTool — checkable with errors.Is, since the returned error wraps
// it with the offending name for a human reading a log.
func (r *Registry) Call(ctx context.Context, name string, args json.RawMessage) (Result, error) {
	t, ok := r.tools[name]
	if !ok {
		return Result{}, fmt.Errorf("tools: unknown tool %q: %w", name, ErrUnknownTool)
	}
	return t.Handler(ctx, args)
}

// Definitions converts every registered Tool (sorted by Name, via List) into
// the wire shape a chat-completions request advertises.
//
// Contract (backbone §6/§7's amendment, D-CY/C-112): the outbound Name is
// WireName(t.Name), not the canonical dotted spelling — every
// OpenAI-compatible endpoint rejects a dot in tools[*].function.name. The
// registry's own map keys, List and Call all keep the canonical name;
// only this outbound translation changes.
func (r *Registry) Definitions() []llm.ToolDef {
	list := r.List()
	out := make([]llm.ToolDef, 0, len(list))
	for _, t := range list {
		out = append(out, toolDef(t))
	}
	return out
}

// DefinitionsOf is Definitions restricted to the tools named in names —
// canonical dotted spellings, the registry's own keys (039).
//
// Why it exists: the model reads every schema it is offered on every round,
// and a read-only question — `lw query`, a TUI ask turn — has no use for the
// ten stage.* schemas a curator turn needs. Advertising only the tools a turn
// may use shrinks the request and takes the stage.* verbs out of the model's
// sight, which the system prompt alone cannot do: a prompt asks, a definitions
// list decides what the model can name. This is a filter over the one
// registry, not a second registry — nothing is removed from it, and Call still
// dispatches every registered tool; deciding which a turn may call is the
// agent loop's job.
//
// The result keeps Definitions' order (sorted by canonical Name, via List)
// and its wire spelling and schemas — the entries are the very values
// Definitions returns — regardless of the order names arrives in. A name
// listed twice yields one entry; a name no tool is registered under is
// skipped, so an unbacked verb such as web.search without a provider is
// never offered. A nil or empty names yields no definitions, never all of
// them: "everything" is Definitions, and a filter that silently widened on an
// empty argument would offer the stage.* verbs to the very turn that asked to
// have them withheld. names is not modified.
func (r *Registry) DefinitionsOf(names []string) []llm.ToolDef {
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	out := make([]llm.ToolDef, 0, len(want))
	for _, t := range r.List() {
		if want[t.Name] {
			out = append(out, toolDef(t))
		}
	}
	return out
}

// toolDef is t's wire shape: the one conversion Definitions and DefinitionsOf
// share, so a tool is advertised identically whichever of them lists it.
func toolDef(t Tool) llm.ToolDef {
	return llm.ToolDef{
		Name:        WireName(t.Name),
		Description: t.Description,
		Parameters:  t.Schema,
	}
}

// decodeArgs unmarshals args into out, treating a missing/empty args
// payload as "no arguments given" rather than a JSON error — a tool whose
// schema has no required properties (e.g. vault.orient) must accept a call
// with no arguments at all.
func decodeArgs(args json.RawMessage, out any) error {
	if len(args) == 0 {
		return nil
	}
	return json.Unmarshal(args, out)
}

// decodeArgsStrict is decodeArgs with the tool schema's
// "additionalProperties": false enforced: an argument object carrying a
// field the args struct does not declare is an error, so the handler
// refuses it with the ordinary bad-args result instead of silently
// dropping it (009 idea 011 added the optional name argument to
// stage.ingest_source, and a misspelled hint must not vanish).
func decodeArgsStrict(args json.RawMessage, out any) error {
	if len(args) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	return dec.Decode(out)
}
