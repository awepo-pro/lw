package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/vault"
)

// --- wiki.search -----------------------------------------------------------

type wikiSearchArgs struct {
	Q     string   `json:"q"`
	Type  string   `json:"type,omitempty"`
	Tags  []string `json:"tags,omitempty"`
	Limit int      `json:"limit,omitempty"`
}

func wikiSearchTool(d Deps) Tool {
	return Tool{
		Name: "wiki.search",
		Description: "Search wiki page titles, tags and body text. Returns " +
			"at most 20 hits of title plus a short snippet — never full page " +
			"bodies. Use wiki.get to read a specific page in full.",
		Schema:   json.RawMessage(wikiSearchSchema),
		ReadOnly: true,
		Handler: func(ctx context.Context, args json.RawMessage) (Result, error) {
			return wikiSearchHandler(ctx, d, args)
		},
	}
}

// wikiSearchHandler formats index.Index.Search's hits. It does not
// re-enforce the 20-hit or 200-rune-snippet caps itself: Options.Limit == 0
// already defaults to 20 (and Search caps its output slice to Limit), and
// Hit.Snippet is already built through a <=200-rune runeWindow — both
// enforced in internal/index/index.go, verified by reading it rather than
// assumed.
func wikiSearchHandler(ctx context.Context, d Deps, args json.RawMessage) (Result, error) {
	var a wikiSearchArgs
	if err := decodeArgs(args, &a); err != nil {
		return badArgs("wiki.search", err, `{"q": "kv cache"}`), nil
	}

	q := strings.TrimSpace(a.Q)
	if q == "" {
		return Result{IsError: true, Content: `q is required: provide a non-empty search query string, e.g. {"q": "kv cache"}`}, nil
	}
	if a.Type != "" && !vault.PageType(a.Type).Valid() {
		return Result{IsError: true, Content: fmt.Sprintf(
			"type %q is not a valid page type; use one of entity, concept, comparison, query, summary, or omit type", a.Type,
		)}, nil
	}

	hits := d.Index.Search(q, index.Options{Type: a.Type, Tags: a.Tags, Limit: a.Limit})
	if len(hits) == 0 {
		return Result{Content: fmt.Sprintf("no results for %q", q)}, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d result(s) for %q:\n", len(hits), q)
	for i, h := range hits {
		fmt.Fprintf(&b, "%d. %s — %s\n   %s\n", i+1, h.Path, h.Title, h.Snippet)
	}
	return Result{Content: strings.TrimRight(b.String(), "\n")}, nil
}

// --- wiki.get ----------------------------------------------------------

type wikiGetArgs struct {
	Page    string `json:"page"`
	Section string `json:"section,omitempty"`
}

func wikiGetTool(d Deps) Tool {
	return Tool{
		Name: "wiki.get",
		Description: "Read one wiki page in full, or a single named section " +
			"of it (section is the exact heading line, e.g. \"## Related\"). " +
			"If the page has staged bytes in the open changeset, the staged " +
			"content is returned, prefixed with a (staged in the open " +
			"changeset, not yet committed) notice.",
		Schema:   json.RawMessage(wikiGetSchema),
		ReadOnly: true,
		Handler: func(ctx context.Context, args json.RawMessage) (Result, error) {
			return wikiGetHandler(ctx, d, args)
		},
	}
}

func wikiGetHandler(ctx context.Context, d Deps, args json.RawMessage) (Result, error) {
	var a wikiGetArgs
	if err := decodeArgs(args, &a); err != nil {
		return badArgs("wiki.get", err, `{"page": "kv-cache"}`), nil
	}

	resolved, p, staged, res, ok, err := resolvePageArg(d, "wiki.get", a.Page)
	if err != nil {
		return Result{}, fmt.Errorf("tools: wiki.get: %w", err)
	}
	if !ok {
		return res, nil
	}

	if a.Section == "" {
		content := string(p.Serialize())
		if staged {
			content = stagedSourceMarker + content
		}
		return Result{Content: content}, nil
	}

	sec, ok := p.Section(a.Section)
	if !ok {
		return Result{IsError: true, Content: fmt.Sprintf(
			"section %q was not found on %s; valid headings are: %s",
			a.Section, resolved, strings.Join(sectionHeadings(p), ", "),
		)}, nil
	}
	body := p.Body[sec.Start:sec.End]
	if staged {
		body = stagedSourceMarker + body
	}
	return Result{Content: body}, nil
}

func sectionHeadings(p *vault.Page) []string {
	out := make([]string, 0, len(p.Sections))
	for _, s := range p.Sections {
		out = append(out, s.Heading)
	}
	return out
}

// --- wiki.neighbors ------------------------------------------------------

type wikiNeighborsArgs struct {
	Page  string `json:"page"`
	Depth int    `json:"depth,omitempty"`
}

func wikiNeighborsTool(d Deps) Tool {
	return Tool{
		Name: "wiki.neighbors",
		Description: "List pages within 1-2 hops of a page, following " +
			"wikilinks in either direction — so an agent can see an existing " +
			"neighboring page before proposing a duplicate.",
		Schema:   json.RawMessage(wikiNeighborsSchema),
		ReadOnly: true,
		Handler: func(ctx context.Context, args json.RawMessage) (Result, error) {
			return wikiNeighborsHandler(ctx, d, args)
		},
	}
}

// wikiNeighborsHandler answers from the committed graph only (see
// resolvePagePath): Neighbors walks d.Vault's index, so a staged rewrite
// of the page's links is invisible here by contract, not by accident.
func wikiNeighborsHandler(ctx context.Context, d Deps, args json.RawMessage) (Result, error) {
	var a wikiNeighborsArgs
	if err := decodeArgs(args, &a); err != nil {
		return badArgs("wiki.neighbors", err, `{"page": "kv-cache"}`), nil
	}

	resolved, res, ok := resolvePagePath(d, "wiki.neighbors", a.Page)
	if !ok {
		return res, nil
	}

	depth := clampDepth(a.Depth)
	neighbors := d.Vault.Graph().Neighbors(resolved, depth)
	if len(neighbors) == 0 {
		return Result{Content: fmt.Sprintf("%s has no neighbors within %d hop(s)", resolved, depth)}, nil
	}
	return Result{Content: fmt.Sprintf(
		"%s neighbors within %d hop(s):\n%s", resolved, depth, strings.Join(neighbors, "\n"),
	)}, nil
}

// clampDepth enforces backbone §6's "depth 1-2" rule: an absent (zero)
// depth defaults to 1, and anything outside [1,2] is clamped into it rather
// than rejected — a depth of 0 or 5 is a harmless request to shrink or
// widen, not a malformed one.
func clampDepth(depth int) int {
	switch {
	case depth < 1:
		return 1
	case depth > 2:
		return 2
	default:
		return depth
	}
}

// --- wiki.backlinks ------------------------------------------------------

type wikiBacklinksArgs struct {
	Page string `json:"page"`
}

func wikiBacklinksTool(d Deps) Tool {
	return Tool{
		Name:        "wiki.backlinks",
		Description: "List every page that links to a given page, with the linking line's text.",
		Schema:      json.RawMessage(wikiBacklinksSchema),
		ReadOnly:    true,
		Handler: func(ctx context.Context, args json.RawMessage) (Result, error) {
			return wikiBacklinksHandler(ctx, d, args)
		},
	}
}

// wikiBacklinksHandler answers from the committed graph only (see
// resolvePagePath): Backlinks walks d.Vault's index, so a staged rewrite
// of inbound links is invisible here by contract, not by accident.
func wikiBacklinksHandler(ctx context.Context, d Deps, args json.RawMessage) (Result, error) {
	var a wikiBacklinksArgs
	if err := decodeArgs(args, &a); err != nil {
		return badArgs("wiki.backlinks", err, `{"page": "kv-cache"}`), nil
	}

	resolved, res, ok := resolvePagePath(d, "wiki.backlinks", a.Page)
	if !ok {
		return res, nil
	}

	refs := d.Vault.Graph().Backlinks(resolved)
	if len(refs) == 0 {
		return Result{Content: fmt.Sprintf("%s has no inbound wikilinks", resolved)}, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d backlink(s) to %s:\n", len(refs), resolved)
	for _, r := range refs {
		fmt.Fprintf(&b, "- %s:%d — %s\n", r.From, r.Line, r.Context)
	}
	return Result{Content: strings.TrimRight(b.String(), "\n")}, nil
}

// --- wiki.lint -----------------------------------------------------------

type wikiLintArgs struct {
	Checks []string `json:"checks,omitempty"`
}

func wikiLintTool(d Deps) Tool {
	return Tool{
		Name: "wiki.lint",
		Description: "Run the vault's lint checks and return their findings. " +
			"The engine computes these; never estimate or guess at a lint " +
			"result yourself.",
		Schema:   json.RawMessage(wikiLintSchema),
		ReadOnly: true,
		Handler: func(ctx context.Context, args json.RawMessage) (Result, error) {
			return wikiLintHandler(ctx, d, args)
		},
	}
}

func wikiLintHandler(ctx context.Context, d Deps, args json.RawMessage) (Result, error) {
	var a wikiLintArgs
	if err := decodeArgs(args, &a); err != nil {
		return badArgs("wiki.lint", err, `{"checks": ["link-broken"]}`), nil
	}

	allIDs := checkIDs()
	if unknown := unknownCheckIDs(a.Checks, allIDs); len(unknown) > 0 {
		return Result{IsError: true, Content: fmt.Sprintf(
			"unknown check id(s) %s; valid ids are: %s",
			strings.Join(unknown, ", "), strings.Join(allIDs, ", "),
		)}, nil
	}

	report := lint.Run(&lint.Context{Vault: d.Vault, Index: d.Index, Graph: d.Vault.Graph()}, a.Checks)
	if len(report.Findings) == 0 {
		return Result{Content: "lint: clean, no findings"}, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d finding(s): %d error(s), %d warn(s):\n", len(report.Findings), report.Errors, report.Warns)
	for _, f := range report.Findings {
		loc := f.Path
		if loc == "" {
			loc = "(vault)"
		}
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", loc, f.Line)
		}
		fmt.Fprintf(&b, "- [%s] %s %s: %s\n", f.Severity, f.Check, loc, f.Message)
	}
	return Result{Content: strings.TrimRight(b.String(), "\n")}, nil
}

func checkIDs() []string {
	all := lint.All()
	out := make([]string, 0, len(all))
	for _, c := range all {
		out = append(out, c.ID())
	}
	return out
}

func unknownCheckIDs(requested, valid []string) []string {
	if len(requested) == 0 {
		return nil
	}
	set := make(map[string]bool, len(valid))
	for _, id := range valid {
		set[id] = true
	}
	var unknown []string
	for _, id := range requested {
		if !set[id] {
			unknown = append(unknown, id)
		}
	}
	return unknown
}

// --- shared helpers --------------------------------------------------------

// badArgs builds the Result{IsError:true} for a args payload that failed to
// decode against a tool's schema — the §6 error convention's "helpful
// content" clause: name the tool, the parse error and a working example.
func badArgs(tool string, err error, example string) Result {
	return Result{IsError: true, Content: fmt.Sprintf(
		"arguments are not valid JSON for %s: %v; expected an object like %s", tool, err, example,
	)}
}

// resolvePageArg validates and resolves a "page" argument for wiki.get:
// trims it, resolves it through vault.Resolve (so a bare filename basename
// works, not just a full path), and returns a ready-to-use Result on any
// failure so the caller can just return it.
//
// After the committed hit, the resolver prefers the open changeset's
// staged bytes for the resolved path (020 T-B) — the agent must be able
// to READ BACK its own staged edit before committing it, and the F2
// blindness ran through reads too. staged reports which body the returned
// page was parsed from: wiki.get marks staged results with
// stagedSourceMarker, the way raw.get marks a staged source (S6-C121).
// The graph tools (wiki.neighbors, wiki.backlinks) do not come through
// here at all: they report the committed graph's link data and carry no
// staged bytes to mark, so they use resolvePagePath and never pay for a
// staged read they would throw away (020 FIX-1).
//
// The staged preference is a read-back, not a discovery channel:
// vault.Resolve walks the committed index, so a page that exists only as
// a staged create_page — no committed counterpart — still falls to the
// not-found message. Reading a path you just staged is the contract;
// staged-only page discovery is out of scope.
//
// Contract (mirrors rawSourceBody): staged bytes parse through
// vault.ParsePage — the same parser a committed page goes through — and a
// staged parse failure is an internal-invariant error, never a silent
// fall back to the committed page, because Append only stages bytes it
// validated. err is non-nil for such a violation and for a genuine
// engine/CAS failure; the caller aborts the turn on it.
//
// vault.Resolve does NOT match a page's frontmatter title (backbone §2.9:
// exact path, then "<target>.md", then basename case-insensitively, then
// "<dir>/<target>.md"). Every message below therefore says "basename",
// never "title" — wiki.search shows the model each hit's title beside its
// path, and a description promising titles resolve sends it round the loop
// search -> title -> not found -> search (MASTER §10 OR-15).
func resolvePageArg(d Deps, tool, page string) (resolved string, p *vault.Page, staged bool, onFail Result, ok bool, err error) {
	resolved, onFail, ok = resolvePagePath(d, tool, page)
	if !ok {
		return "", nil, false, onFail, false, nil
	}

	pg, found := d.Vault.Page(resolved)
	if !found {
		// vault.Resolve only returns a path it has already confirmed
		// exists via Vault.Page, so this is unreachable in practice; kept
		// as a defensive, helpful error rather than a panic.
		return "", nil, false, Result{IsError: true, Content: fmt.Sprintf(
			"page %q resolved to %q, which could not be loaded", page, resolved,
		)}, false, nil
	}

	if d.Engine != nil {
		b, has, serr := d.Engine.StagedFile(resolved)
		if serr != nil {
			return "", nil, false, Result{}, false, serr
		}
		if has {
			sp, perr := vault.ParsePage(resolved, b)
			if perr != nil {
				return "", nil, false, Result{}, false, fmt.Errorf("parse staged page %s: %w", resolved, perr)
			}
			return resolved, sp, true, Result{}, true, nil
		}
	}
	return resolved, pg, false, Result{}, true, nil
}

// resolvePagePath is resolvePageArg's path-only half, shared by both
// resolvers' front matter: trim the argument and resolve it to a
// committed page path through vault.Resolve. It carries no staged read
// and no parse — wiki.neighbors and wiki.backlinks resolve through it
// because their answers are COMMITTED-INDEX ONLY (Graph().Backlinks and
// Neighbors over d.Vault): staged bytes would be parsed and immediately
// discarded, and a staged parse failure would turn a healthy committed
// page's graph query into an invariant error for nothing (020 FIX-1, T-B
// review finding 4). onFail is meaningful only when ok is false.
func resolvePagePath(d Deps, tool, page string) (resolved string, onFail Result, ok bool) {
	page = strings.TrimSpace(page)
	if page == "" {
		return "", Result{IsError: true, Content: fmt.Sprintf(
			`page is required for %s: provide a page path or filename basename, e.g. {"page": "kv-cache"}`, tool,
		)}, false
	}

	resolved, found := vault.Resolve(d.Vault, page)
	if !found {
		return "", Result{IsError: true, Content: fmt.Sprintf(
			"page %q was not found; pass the vault-relative path or the filename basename "+
				"(e.g. \"kv-cache\"), not the page title — wiki.search lists each result's "+
				"path before the em dash", page,
		)}, false
	}
	return resolved, Result{}, true
}
