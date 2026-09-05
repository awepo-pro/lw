package tools

import (
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
// registers the 7 read-only tools; S3-T2 adds the 10 stage.* tools to the
// same package, bringing the total to the backbone's 17.
func NewRegistry(d Deps) *Registry {
	r := &Registry{
		deps:  d,
		tools: make(map[string]Tool),
	}
	for _, t := range readTools(d) {
		r.tools[t.Name] = t
	}
	return r
}

// readTools returns the 7 read-only tools (backbone §6, rows 1-7).
func readTools(d Deps) []Tool {
	return []Tool{
		vaultOrientTool(d),
		wikiSearchTool(d),
		wikiGetTool(d),
		wikiNeighborsTool(d),
		wikiBacklinksTool(d),
		rawGetTool(d),
		wikiLintTool(d),
	}
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
func (r *Registry) Definitions() []llm.ToolDef {
	list := r.List()
	out := make([]llm.ToolDef, 0, len(list))
	for _, t := range list {
		out = append(out, llm.ToolDef{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Schema,
		})
	}
	return out
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
