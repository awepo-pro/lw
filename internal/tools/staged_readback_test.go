package tools

// staged_readback_test.go freezes 060: wiki.get reads back a page that exists
// only as a staged stage.create_page, and the graph tools name it instead of
// calling it missing.
//
// vault.Resolve walks the COMMITTED index, so a page only the open changeset
// holds fell to the not-found message — while the wiki.get description and
// 040's history placeholder (internal/agent/budget.go) both promise the
// staged text comes back. On 2026-10-08 a joined `lw ingest --recompile`
// (turn 757c) asked wiki.get for the two pages the first call had staged six
// times, got "not found" every time and ran out of rounds (max_rounds
// 24/24). The eval never ran a joined second turn, so 037/049 could not see
// it. The committed lookup still runs first and answers exactly as before.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/stage"
)

// readbackLinks gives a created page the two outbound wikilinks
// stage.create_page requires (the frozen body in the 060 spec has none, so
// as written it is refused with "fewer than 2 outbound wikilinks"). They sit
// in the first section, never in the one the section reads assert on.
const readbackLinks = "See [[kv-cache]] and [[gpt-4]]."

const (
	pagedAttentionPath = "wiki/concepts/paged-attention.md"
	pagedAttentionBody = "## Abstract\n\nPaged attention caches sequences in blocks. " + readbackLinks + "\n\n## How it works\n\nBlocks map logical to physical memory."
)

// readbackCreate drives stage.create_page for a concept page the way
// TestAddLinkErrorNamesStagedOnlyEndpoint does (same tags, source and
// confidence), varying only path, title, type and body.
func readbackCreate(t *testing.T, reg *Registry, path, title, typ, body string) {
	t.Helper()
	args, err := json.Marshal(map[string]any{
		"path":       path,
		"title":      title,
		"type":       typ,
		"tags":       []string{"inference", "memory"},
		"sources":    []string{"raw/papers/leviathan-2023.md"},
		"confidence": "high",
		"contested":  false,
		"body":       body,
		"rationale":  "new page",
	})
	if err != nil {
		t.Fatal(err)
	}
	if r := callTool(t, reg, "stage.create_page", string(args)); r.IsError {
		t.Fatalf("create %s: %s", path, r.Content)
	}
}

func readbackCreatePagedAttention(t *testing.T, reg *Registry) {
	t.Helper()
	if r := callTool(t, reg, "stage.open", `{"intent":"stage a page, then read it back"}`); r.IsError {
		t.Fatal(r.Content)
	}
	readbackCreate(t, reg, pagedAttentionPath, "Paged Attention", "concept", pagedAttentionBody)
}

// readbackNotFound is wiki.get's not-found text for page, written out so a
// change to it is a visible diff here and not a silent one.
func readbackNotFound(page string) string {
	return `page "` + page + `" was not found; pass the vault-relative path or the filename basename (e.g. "kv-cache"), not the page title — wiki.search lists each result's path before the em dash`
}

// readbackGraphText is 060 D3's text for the graph tools: byte-exact, the em
// dash included.
func readbackGraphText(p, tool string) string {
	return `page "` + p + `" exists only in the open changeset (staged by stage.create_page, not committed); ` + tool + ` reports the committed link graph, which does not hold it yet — read it with wiki.get`
}

// readbackLead is what a wiki.get result for arg starts with when the page
// is staged: 036 D3's resolved-path line when arg is not the resolved path
// byte for byte, then the staged marker. An exact-path call is the marker
// alone.
func readbackLead(arg, resolved string) string {
	if arg == resolved {
		return stagedSourceMarker
	}
	return resolvedLine(arg, resolved) + stagedSourceMarker
}

// TestWikiGetReadsStagedOnlyPage is the headline: every spelling a model
// uses for a page it just staged — full path, path without ".md", basename,
// a mis-cased basename with ".md" — reads it back, marked as staged. A
// section read returns only that section.
func TestWikiGetReadsStagedOnlyPage(t *testing.T) {
	reg, _, _ := engineRegistry(t, nil)
	readbackCreatePagedAttention(t, reg)

	for _, arg := range []string{
		"wiki/concepts/paged-attention.md",
		"wiki/concepts/paged-attention",
		"paged-attention",
		"Paged-Attention.md",
	} {
		t.Run(arg, func(t *testing.T) {
			r := callTool(t, reg, "wiki.get", `{"page":`+quoteJSON(t, arg)+`}`)
			if r.IsError {
				t.Fatalf("wiki.get %q is an error: %s", arg, r.Content)
			}
			// The exact-path call starts with the staged marker itself; a
			// bare name or a miscased one gets 036 D3's resolved-path line
			// first, as it does for a staged COMMITTED page.
			if lead := readbackLead(arg, pagedAttentionPath); !strings.HasPrefix(r.Content, lead) {
				t.Fatalf("wiki.get %q does not start with %q:\n%.300s", arg, lead, r.Content)
			}
			for _, want := range []string{"title: Paged Attention", "caches sequences in blocks"} {
				if !strings.Contains(r.Content, want) {
					t.Fatalf("wiki.get %q lost %q:\n%s", arg, want, r.Content)
				}
			}
		})
	}

	for _, arg := range []string{pagedAttentionPath, "paged-attention"} {
		r := callTool(t, reg, "wiki.get", `{"page":`+quoteJSON(t, arg)+`,"section":"## How it works"}`)
		if r.IsError {
			t.Fatalf("section read of %q is an error: %s", arg, r.Content)
		}
		if lead := readbackLead(arg, pagedAttentionPath); !strings.HasPrefix(r.Content, lead) {
			t.Fatalf("section read of %q does not start with %q:\n%.300s", arg, lead, r.Content)
		}
		if !strings.Contains(r.Content, "Blocks map logical to physical memory.") {
			t.Fatalf("section read of %q lost its section text:\n%s", arg, r.Content)
		}
		if strings.Contains(r.Content, "caches sequences") {
			t.Fatalf("section read of %q returned the other section too:\n%s", arg, r.Content)
		}
	}

	if r := callTool(t, reg, "wiki.get", `{"page":"paged-attention","section":"## Nope"}`); !r.IsError ||
		!strings.Contains(r.Content, `section "## Nope" was not found on `+pagedAttentionPath+`; valid headings are: `) {
		t.Fatalf("a missing section of a staged-only page: IsError = %v, content:\n%s", r.IsError, r.Content)
	}
}

// quoteJSON renders s as a JSON string literal for an args payload.
func quoteJSON(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestWikiGetStagedOnlyTruncationKeepsMarker: a staged-only page over the
// 16000-rune cap is cut like any wiki.get result and the marker stays a
// prefix that does not count toward the cap — the same contract
// TestWikiGetStagedTruncationKeepsMarkerPrefix pins for a staged committed
// page.
func TestWikiGetStagedOnlyTruncationKeepsMarker(t *testing.T) {
	reg, _, _ := engineRegistry(t, nil)
	if r := callTool(t, reg, "stage.open", `{"intent":"stage a long page"}`); r.IsError {
		t.Fatal(r.Content)
	}
	readbackCreate(t, reg, pagedAttentionPath, "Paged Attention", "concept",
		"## Abstract\n\n"+strings.Repeat("量é", 12000)+"\n\n## Tail\n\nend "+readbackLinks)
	r := callTool(t, reg, "wiki.get", `{"page":"`+pagedAttentionPath+`"}`)
	if r.IsError {
		t.Fatalf("wiki.get: %s", r.Content)
	}
	if !strings.HasPrefix(r.Content, stagedSourceMarker) {
		t.Fatalf("truncated staged-only result lost the marker prefix:\n%.200s", r.Content)
	}
	if !strings.Contains(r.Content, "[truncated: "+pagedAttentionPath+" is ") {
		t.Fatalf("a >16000-rune staged-only page was not truncated:\n%.200s", r.Content)
	}
	if strings.Contains(r.Content, "## Tail\n\nend") {
		t.Fatal("the truncated result still holds the tail of the page")
	}
}

// TestWikiGetStagedOnlyAcrossProcesses is the shape of the 10-08 failure: the
// first `lw ingest` process stages the create and exits; the second process
// opens a NEW engine on the same vault, joins the open changeset (019) and
// reads the page back.
func TestWikiGetStagedOnlyAcrossProcesses(t *testing.T) {
	reg, e, dir := engineRegistry(t, nil)
	readbackCreatePagedAttention(t, reg)
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	e2, err := stage.OpenEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e2.Close() })
	v2 := e2.Vault()
	reg2 := NewRegistry(Deps{Vault: v2, Index: index.Build(v2), Engine: e2, Author: stage.Author{Kind: "agent", Model: "test"}})

	r := callTool(t, reg2, "wiki.get", `{"page":"paged-attention"}`)
	if r.IsError {
		t.Fatalf("wiki.get on the joined engine: %s", r.Content)
	}
	if lead := readbackLead("paged-attention", pagedAttentionPath); !strings.HasPrefix(r.Content, lead) {
		t.Fatalf("joined wiki.get does not start with %q:\n%.300s", lead, r.Content)
	}
	if !strings.Contains(r.Content, "caches sequences in blocks") {
		t.Fatalf("joined wiki.get lost the staged text:\n%s", r.Content)
	}
}

// TestWikiGetStagedOnlyPatchedReadsLatest: a staged-only page that a later
// op rewrote reads back as the rewrite, not as the create.
func TestWikiGetStagedOnlyPatchedReadsLatest(t *testing.T) {
	reg, _, _ := engineRegistry(t, nil)
	readbackCreatePagedAttention(t, reg)
	if r := callTool(t, reg, "stage.patch_page", `{"path":"`+pagedAttentionPath+`","section":"## How it works","op":"replace_section","content":"Pages are fixed-size blocks.","rationale":"restate the mechanism"}`); r.IsError {
		t.Fatalf("patch: %s", r.Content)
	}
	r := callTool(t, reg, "wiki.get", `{"page":"paged-attention"}`)
	if r.IsError {
		t.Fatalf("wiki.get: %s", r.Content)
	}
	if !strings.Contains(r.Content, "Pages are fixed-size blocks.") {
		t.Fatalf("wiki.get does not show the patched text:\n%s", r.Content)
	}
	if strings.Contains(r.Content, "Blocks map logical to physical memory.") {
		t.Fatalf("wiki.get still shows the created text the patch replaced:\n%s", r.Content)
	}
}

// readbackStageFooBars leaves two LIVE staged-only creates that share a
// basename. stage.create_page refuses the second ("collides with the
// existing wiki/concepts/foo-bar.md ... Give the page a distinct name"), so
// the state is built the way a reviewer's restore builds it: drop the first
// create, stage the second while the name is free, then restore the first.
// Nothing re-checks the collision on restore.
func readbackStageFooBars(t *testing.T, reg *Registry, e *stage.Engine) {
	t.Helper()
	if r := callTool(t, reg, "stage.open", `{"intent":"stage two pages sharing a basename"}`); r.IsError {
		t.Fatal(r.Content)
	}
	readbackCreate(t, reg, "wiki/concepts/foo-bar.md", "Foo Bar Concept", "concept", "## Abstract\n\nThe concept page. "+readbackLinks)
	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	first := cs.Ops[0].ID
	if err := e.DropOp(first); err != nil {
		t.Fatal(err)
	}
	readbackCreate(t, reg, "wiki/entities/foo-bar.md", "Foo Bar Entity", "entity", "## Abstract\n\nThe entity page. "+readbackLinks)
	if err := e.RestoreOps([]string{first}); err != nil {
		t.Fatal(err)
	}
	if cs, err = e.Current(); err != nil {
		t.Fatal(err)
	}
	if n := len(cs.Live()); n != 2 {
		t.Fatalf("len(cs.Live()) = %d, want 2 live creates", n)
	}
}

// TestWikiGetStagedOnlyAmbiguousNotFound: two staged-only pages that share a
// basename make the bare name ambiguous — a miss, never a guess — while each
// full path reads its own page.
func TestWikiGetStagedOnlyAmbiguousNotFound(t *testing.T) {
	reg, e, _ := engineRegistry(t, nil)
	readbackStageFooBars(t, reg, e)

	r := callTool(t, reg, "wiki.get", `{"page":"foo-bar"}`)
	if !r.IsError || r.Content != readbackNotFound("foo-bar") {
		t.Fatalf("ambiguous staged-only name: IsError = %v, content:\n%s", r.IsError, r.Content)
	}
	for _, tc := range []struct{ path, title string }{
		{"wiki/concepts/foo-bar.md", "title: Foo Bar Concept"},
		{"wiki/entities/foo-bar.md", "title: Foo Bar Entity"},
	} {
		r := callTool(t, reg, "wiki.get", `{"page":"`+tc.path+`"}`)
		if r.IsError || !strings.HasPrefix(r.Content, stagedSourceMarker) || !strings.Contains(r.Content, tc.title) {
			t.Fatalf("full path %s: IsError = %v, content:\n%s", tc.path, r.IsError, r.Content)
		}
	}
}

// TestWikiGetStagedOnlyDroppedSiblingUnambiguates: Live() decides what is
// staged. With one of two same-basename creates dropped, the bare name
// names the survivor — and a lookup over every op, dropped ones included,
// would still call it ambiguous.
func TestWikiGetStagedOnlyDroppedSiblingUnambiguates(t *testing.T) {
	reg, e, _ := engineRegistry(t, nil)
	readbackStageFooBars(t, reg, e)
	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.DropOp(cs.Ops[0].ID); err != nil {
		t.Fatal(err)
	}
	r := callTool(t, reg, "wiki.get", `{"page":"foo-bar"}`)
	if r.IsError || !strings.Contains(r.Content, "title: Foo Bar Entity") {
		t.Fatalf("after dropping the concept page, foo-bar should read the entity page: IsError = %v, content:\n%s", r.IsError, r.Content)
	}
}

// TestWikiGetDroppedCreateNotFound: a dropped create is not staged any more
// — Commit will never write it — so it is as missing as a page never created.
func TestWikiGetDroppedCreateNotFound(t *testing.T) {
	reg, e, _ := engineRegistry(t, nil)
	readbackCreatePagedAttention(t, reg)
	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Ops) != 1 {
		t.Fatalf("len(cs.Ops) = %d, want 1", len(cs.Ops))
	}
	if err := e.DropOp(cs.Ops[0].ID); err != nil {
		t.Fatal(err)
	}
	for _, arg := range []string{"paged-attention", pagedAttentionPath} {
		r := callTool(t, reg, "wiki.get", `{"page":"`+arg+`"}`)
		if !r.IsError || r.Content != readbackNotFound(arg) {
			t.Fatalf("wiki.get %q after the create was dropped: IsError = %v, content:\n%s", arg, r.IsError, r.Content)
		}
	}
}

// TestWikiGetNotFoundTextUnchanged freezes the not-found text byte for byte:
// the staged lookup must not touch the refusal when it finds nothing.
func TestWikiGetNotFoundTextUnchanged(t *testing.T) {
	const want = `page "no-such-page" was not found; pass the vault-relative path or the filename basename (e.g. "kv-cache"), not the page title — wiki.search lists each result's path before the em dash`
	reg, _, _ := engineRegistry(t, nil)
	if r := callTool(t, reg, "wiki.get", `{"page":"no-such-page"}`); !r.IsError || r.Content != want {
		t.Fatalf("no changeset: IsError = %v, content:\n%s", r.IsError, r.Content)
	}
	if readbackNotFound("no-such-page") != want {
		t.Fatal("readbackNotFound drifted from the frozen text")
	}

	// With a changeset open and a staged-only page that does not answer to
	// the name, the refusal is the same.
	readbackCreatePagedAttention(t, reg)
	if r := callTool(t, reg, "wiki.get", `{"page":"no-such-page"}`); !r.IsError || r.Content != want {
		t.Fatalf("changeset open: IsError = %v, content:\n%s", r.IsError, r.Content)
	}
	// An empty page is the required-argument error, not a not-found.
	if r := callTool(t, reg, "wiki.get", `{"page":"  "}`); !r.IsError ||
		r.Content != `page is required for wiki.get: provide a page path or filename basename, e.g. {"page": "kv-cache"}` {
		t.Fatalf("blank page: IsError = %v, content:\n%s", r.IsError, r.Content)
	}
}

// TestWikiGetCommittedHitUnchangedByStagedOnlyPage: the committed lookup
// runs first and a committed hit keeps today's bytes — a staged-only page
// elsewhere in the changeset changes nothing about it. (The staged-only page
// cannot share the committed page's basename: stage.create_page refuses the
// collision, so "the committed hit wins a name clash" has no input to test.)
func TestWikiGetCommittedHitUnchangedByStagedOnlyPage(t *testing.T) {
	reg, _, _ := engineRegistry(t, nil)
	before := callTool(t, reg, "wiki.get", `{"page":"kv-cache"}`)
	if before.IsError {
		t.Fatalf("wiki.get kv-cache: %s", before.Content)
	}
	readbackCreatePagedAttention(t, reg)
	after := callTool(t, reg, "wiki.get", `{"page":"kv-cache"}`)
	if after.IsError || after.Content != before.Content {
		t.Fatalf("a staged-only page elsewhere changed a committed read:\n--- before\n%s\n--- after\n%s", before.Content, after.Content)
	}
}

// TestWikiGetCommittedHitWinsOverStagedOnly pins the order: the committed
// lookup runs FIRST. stage.create_page refuses a name that clashes with a
// committed page, but a page can land in the working tree after the create
// was staged (a pull, a sync) — then one name answers to both. The committed
// page keeps winning, byte for byte as before 060, and the staged-only page
// stays reachable by its full path.
func TestWikiGetCommittedHitWinsOverStagedOnly(t *testing.T) {
	reg, e, dir := engineRegistry(t, nil)
	readbackCreatePagedAttention(t, reg)

	const landed = "---\ntitle: Paged Attention Landed\ncreated: 2026-01-01\nupdated: 2026-01-01\ntype: entity\n---\n\n# Paged Attention Landed\n\n- [[kv-cache]]\n- [[gpt-4]]\n"
	full := filepath.Join(dir, "wiki", "entities", "paged-attention.md")
	if err := os.WriteFile(full, []byte(landed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.Vault().Reload(); err != nil {
		t.Fatal(err)
	}

	r := callTool(t, reg, "wiki.get", `{"page":"paged-attention"}`)
	if r.IsError || !strings.Contains(r.Content, "title: Paged Attention Landed") || strings.Contains(r.Content, stagedSourceMarker) {
		t.Fatalf("the committed page should answer paged-attention, unmarked: IsError = %v, content:\n%.300s", r.IsError, r.Content)
	}
	if want := resolvedLine("paged-attention", "wiki/entities/paged-attention.md"); !strings.HasPrefix(r.Content, want) {
		t.Fatalf("committed read does not start with the resolved-path line %q:\n%.300s", want, r.Content)
	}
	r = callTool(t, reg, "wiki.get", `{"page":"`+pagedAttentionPath+`"}`)
	if r.IsError || !strings.HasPrefix(r.Content, stagedSourceMarker) || !strings.Contains(r.Content, "title: Paged Attention\n") {
		t.Fatalf("the staged-only page should still read by its full path: IsError = %v, content:\n%.300s", r.IsError, r.Content)
	}
}

// TestWikiGetStagedOnlyTrimsArgument: the staged lookup sees the same
// trimmed argument the committed one does.
func TestWikiGetStagedOnlyTrimsArgument(t *testing.T) {
	reg, _, _ := engineRegistry(t, nil)
	readbackCreatePagedAttention(t, reg)
	r := callTool(t, reg, "wiki.get", `{"page":"  paged-attention\n"}`)
	if lead := readbackLead("paged-attention", pagedAttentionPath); r.IsError || !strings.HasPrefix(r.Content, lead) {
		t.Fatalf("padded argument: IsError = %v, content:\n%.300s", r.IsError, r.Content)
	}
}

// TestGraphToolsNameStagedOnlyPage: wiki.neighbors and wiki.backlinks answer
// from the committed link graph, which cannot hold a staged-only page. Not
// found would send the model hunting for a typo; the refusal names the page
// and the tool that can read it. Committed pages are untouched.
func TestGraphToolsNameStagedOnlyPage(t *testing.T) {
	reg, _, _ := engineRegistry(t, nil)
	committed := map[string]Result{}
	for _, tool := range []string{"wiki.neighbors", "wiki.backlinks"} {
		committed[tool] = callTool(t, reg, tool, `{"page":"kv-cache"}`)
		if committed[tool].IsError {
			t.Fatalf("%s kv-cache: %s", tool, committed[tool].Content)
		}
	}
	readbackCreatePagedAttention(t, reg)

	for _, tool := range []string{"wiki.neighbors", "wiki.backlinks"} {
		for _, arg := range []string{"paged-attention", pagedAttentionPath, "Paged-Attention"} {
			r := callTool(t, reg, tool, `{"page":"`+arg+`"}`)
			if want := readbackGraphText(pagedAttentionPath, tool); !r.IsError || r.Content != want {
				t.Fatalf("%s %q: IsError = %v, content:\n%s\nwant:\n%s", tool, arg, r.IsError, r.Content, want)
			}
		}
		after := callTool(t, reg, tool, `{"page":"kv-cache"}`)
		if after.IsError || after.Content != committed[tool].Content {
			t.Fatalf("%s kv-cache changed with a staged-only page open:\n--- before\n%s\n--- after (IsError %v)\n%s",
				tool, committed[tool].Content, after.IsError, after.Content)
		}
		if r := callTool(t, reg, tool, `{"page":"no-such-page"}`); !r.IsError || r.Content != readbackNotFound("no-such-page") {
			t.Fatalf("%s no-such-page: IsError = %v, content:\n%s", tool, r.IsError, r.Content)
		}
	}
}

// TestGraphToolsDroppedCreateNotFound: a dropped create is not staged, so the
// graph tools call it missing, not "exists only in the open changeset".
func TestGraphToolsDroppedCreateNotFound(t *testing.T) {
	reg, e, _ := engineRegistry(t, nil)
	readbackCreatePagedAttention(t, reg)
	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.DropOp(cs.Ops[0].ID); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"wiki.neighbors", "wiki.backlinks"} {
		r := callTool(t, reg, tool, `{"page":"paged-attention"}`)
		if !r.IsError || r.Content != readbackNotFound("paged-attention") {
			t.Fatalf("%s after the create was dropped: IsError = %v, content:\n%s", tool, r.IsError, r.Content)
		}
	}
}
