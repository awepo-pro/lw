package tools

// grounding_test.go freezes 036's contract: a tool result must carry the
// facts the tool already holds, so the model stops guessing. Live sessions
// showed ~11% of tool calls failing because a tool withheld one — the
// closest existing paths after a wrong guess (D1), the valid headings after
// a wrong section (D2), the resolved page path after a bare-name read (D3).
// Every message below is pinned byte for byte, so the arrow, the em dash and
// the quotes are part of the contract, and a Contains-style loosening would
// let a reworded clause pass.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// rawNotFoundBase is rawNotFoundMessage's text before any 036 clause: the
// pre-036 message up to, not including, its "; call raw.list …" pointer.
const rawNotFoundBase = `raw source %q was not found; provide the exact vault-relative path under raw/ — check a citing page's ^[raw/...] provenance marker`

// rawListPointer is the clause every raw not-found message ends its fixed
// part with; 036 inserts the closest-sources clause just before it.
const rawListPointer = `; call raw.list to see every raw source`

// frontmatterClause is D2's clause, copied byte for byte from the frozen
// block: it follows the valid-headings list when the model asked for the
// frontmatter or the text above the first heading as if it were a section.
const frontmatterClause = `. Frontmatter and the text above the first heading are not sections: to change text under the page title use op replace_text on the title heading (e.g. "# Title"); frontmatter changes go through stage.create_page on a new page or the user`

// resolvedLine is D3's line for arg resolving to path, copied byte for byte
// from the frozen block (arrow included), blank-line-terminated.
func resolvedLine(arg, path string) string {
	return `resolved "` + arg + `" → ` + path + " (use this path in stage.* calls)\n\n"
}

func rawNotFound(source string) string {
	return strings.Replace(rawNotFoundBase, "%q", `"`+source+`"`, 1)
}

// patchArgs builds a stage.patch_page call whose only varying parts are the
// page and the section: the op and content never matter, since every call
// here is refused before an op is built.
func patchArgs(t *testing.T, path, section string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{
		"path": path, "section": section, "op": "append_section", "content": "- x", "rationale": "test",
	})
	if err != nil {
		t.Fatalf("marshal patch args: %v", err)
	}
	return string(b)
}

// fixtureHeadings reads the committed fixture page's headings straight from
// the parsed page — the list wiki.get prints — so the expected text cannot
// drift from the fixture the way a typed-out list would.
func fixtureHeadings(t *testing.T, path string) string {
	t.Helper()
	v, err := vault.Open(testutil.CopyFixture(t, "minimal"))
	if err != nil {
		t.Fatalf("vault.Open(minimal): %v", err)
	}
	p, ok := v.Page(path)
	if !ok {
		t.Fatalf("fixture page %s not found", path)
	}
	heads := make([]string, 0, len(p.Sections))
	for _, s := range p.Sections {
		heads = append(heads, s.Heading)
	}
	return strings.Join(heads, ", ")
}

// TestRawGetNotFoundClosest pins D1 for raw.get: a guess one hyphen off the
// real path names the real path in the same result, between the provenance
// advice and the raw.list pointer; a guess nothing resembles keeps the
// pre-036 message byte for byte.
func TestRawGetNotFoundClosest(t *testing.T) {
	reg := minimalRegistry(t)

	t.Run("a near miss names the closest raw source", func(t *testing.T) {
		r := callTool(t, reg, "raw.get", `{"source":"raw/papers/leviathan2023.md"}`)
		if !r.IsError {
			t.Fatalf("raw.get on a missing source must be IsError, got:\n%s", r.Content)
		}
		want := `raw source "raw/papers/leviathan2023.md" was not found; provide the exact vault-relative path under raw/ — check a citing page's ^[raw/...] provenance marker; closest raw sources: raw/papers/leviathan-2023.md; call raw.list to see every raw source`
		if r.Content != want {
			t.Errorf("raw.get not-found =\n%s\nwant\n%s", r.Content, want)
		}
	})

	t.Run("no candidate keeps the old message byte for byte", func(t *testing.T) {
		r := callTool(t, reg, "raw.get", `{"source":"raw/zz.md"}`)
		if !r.IsError {
			t.Fatalf("raw.get on a missing source must be IsError, got:\n%s", r.Content)
		}
		want := `raw source "raw/zz.md" was not found; provide the exact vault-relative path under raw/ — check a citing page's ^[raw/...] provenance marker; call raw.list to see every raw source`
		if r.Content != want {
			t.Errorf("raw.get not-found =\n%s\nwant\n%s", r.Content, want)
		}
	})

	t.Run("the candidate is looked up by the trimmed argument", func(t *testing.T) {
		// handler trims the source before resolving it, so the message must
		// echo and match the trimmed text, not the padded one.
		r := callTool(t, reg, "raw.get", `{"source":"  raw/papers/leviathan2023.md  "}`)
		want := rawNotFound("raw/papers/leviathan2023.md") + "; closest raw sources: raw/papers/leviathan-2023.md" + rawListPointer
		if r.Content != want {
			t.Errorf("raw.get not-found =\n%s\nwant\n%s", r.Content, want)
		}
	})
}

// TestPatchPageNotFoundClosest pins D1 for stage.patch_page: the wanted
// page's name exists under another directory — the live tilelang case, where
// the model wrote wiki/concepts/ and the page lives under wiki/entities/ —
// and the refusal says where.
func TestPatchPageNotFoundClosest(t *testing.T) {
	reg := minimalRegistry(t)

	t.Run("right name, wrong directory", func(t *testing.T) {
		r := callTool(t, reg, "stage.patch_page", patchArgs(t, "wiki/entities/kv-cache.md", "## Related"))
		if !r.IsError {
			t.Fatalf("patch on a missing page must be IsError, got:\n%s", r.Content)
		}
		want := `page "wiki/entities/kv-cache.md" was not found; closest pages: wiki/concepts/kv-cache.md`
		if r.Content != want {
			t.Errorf("stage.patch_page not-found =\n%s\nwant\n%s", r.Content, want)
		}
	})

	t.Run("no candidate keeps the old message byte for byte", func(t *testing.T) {
		r := callTool(t, reg, "stage.patch_page", patchArgs(t, "wiki/zz.md", "## Related"))
		want := `page "wiki/zz.md" was not found`
		if !r.IsError || r.Content != want {
			t.Errorf("stage.patch_page not-found = %+v, want IsError with %q", r, want)
		}
	})
}

// TestAddLinkNotFoundClosest pins D1 for stage.add_link: both endpoints get
// the clause, and the pre-existing staged-state clause — which describes the
// same path — comes after it, so the two read as one sentence of advice.
func TestAddLinkNotFoundClosest(t *testing.T) {
	t.Run("from endpoint", func(t *testing.T) {
		reg := minimalRegistry(t)
		r := callTool(t, reg, "stage.add_link", `{"from":"wiki/entities/kv-cache.md","to":"wiki/entities/gpt-4.md"}`)
		if !r.IsError {
			t.Fatalf("add_link from a missing page must be IsError, got:\n%s", r.Content)
		}
		want := `from page "wiki/entities/kv-cache.md" was not found; closest pages: wiki/concepts/kv-cache.md`
		if !strings.HasPrefix(r.Content, want) {
			t.Errorf("add_link not-found =\n%s\nwant it to start with\n%s", r.Content, want)
		}
		// No changeset is open and nothing is staged, so the whole message is
		// the frozen prefix — no stray suffix.
		if r.Content != want {
			t.Errorf("add_link not-found =\n%s\nwant exactly\n%s", r.Content, want)
		}
	})

	t.Run("to endpoint", func(t *testing.T) {
		reg := minimalRegistry(t)
		r := callTool(t, reg, "stage.add_link", `{"from":"wiki/entities/gpt-4.md","to":"wiki/entities/kv-cache.md"}`)
		want := `to page "wiki/entities/kv-cache.md" was not found; closest pages: wiki/concepts/kv-cache.md`
		if !r.IsError || r.Content != want {
			t.Errorf("add_link not-found = %+v, want IsError with %q", r, want)
		}
	})

	t.Run("no candidate keeps the old message byte for byte", func(t *testing.T) {
		reg := minimalRegistry(t)
		r := callTool(t, reg, "stage.add_link", `{"from":"wiki/zz.md","to":"wiki/entities/gpt-4.md"}`)
		want := `from page "wiki/zz.md" was not found`
		if !r.IsError || r.Content != want {
			t.Errorf("add_link not-found = %+v, want IsError with %q", r, want)
		}
	})

	t.Run("the staged-state clause follows the closest clause", func(t *testing.T) {
		// A rename's cascade rewrites vault-root curator-memory.md, whose
		// staged bytes are not a page: the endpoint is not found as a page
		// yet has staged state, which is the one shape where both clauses
		// apply. A page named curator-memory under wiki/ is its close match.
		dir := testutil.CopyFixture(t, "minimal")
		if err := os.WriteFile(filepath.Join(dir, "curator-memory.md"),
			[]byte("## Naming\n\n- Prefer the hyphenated vendor form: [[gpt-4]], not `gpt4`.\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		kv, err := os.ReadFile(filepath.Join(dir, "wiki", "concepts", "kv-cache.md"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "wiki", "concepts", "curator-memory.md"), kv, 0o644); err != nil {
			t.Fatal(err)
		}
		reg, _ := registryAt(t, dir)
		if r := callTool(t, reg, "stage.open", `{"intent":"rename over a root file"}`); r.IsError {
			t.Fatal(r.Content)
		}
		if r := callTool(t, reg, "stage.rename_page", `{"from":"wiki/entities/gpt-4.md","to":"wiki/entities/gpt-four.md","rationale":"canonical spelling"}`); r.IsError {
			t.Fatalf("rename: %s", r.Content)
		}
		r := callTool(t, reg, "stage.add_link", `{"from":"curator-memory.md","to":"wiki/entities/gpt-four.md"}`)
		want := `from page "curator-memory.md" was not found; closest pages: wiki/concepts/curator-memory.md (curator-memory.md exists only in the open changeset's staged state; an add_link endpoint must be a committed page, so link after this changeset commits)`
		if !r.IsError || r.Content != want {
			t.Errorf("add_link not-found = %+v\nwant IsError with\n%s", r, want)
		}
	})
}

// TestPatchSectionNotFoundHeadings pins D2: a wrong section on stage.patch_page
// lists the page's real headings — the same message wiki.get has always
// printed — and a request for the frontmatter or the preamble, which no
// heading addresses, also says how to change that text. The unspellable
// request is the one the model cannot fix by retrying, so the clause names
// the way out rather than only the list.
func TestPatchSectionNotFoundHeadings(t *testing.T) {
	const page = "wiki/concepts/kv-cache.md"
	heads := fixtureHeadings(t, page)
	if want := "# KV Cache, ## Abstract, ## Why it matters, ## Example, ## Related"; heads != want {
		// The fenced "## this is not a heading" line must not be listed; this
		// guards the fixture the expected text is read from.
		t.Fatalf("fixture headings = %q, want %q", heads, want)
	}
	reg := minimalRegistry(t)

	t.Run("an unknown heading lists the valid ones", func(t *testing.T) {
		r := callTool(t, reg, "stage.patch_page", patchArgs(t, page, "## Nope"))
		want := `section "## Nope" was not found on ` + page + `; valid headings are: ` + heads
		if !r.IsError || r.Content != want {
			t.Errorf("stage.patch_page = %+v\nwant IsError with\n%s", r, want)
		}
		// The same message wiki.get gives for the same mistake.
		g := callTool(t, reg, "wiki.get", `{"page":"`+page+`","section":"## Nope"}`)
		if !g.IsError || g.Content != want {
			t.Errorf("wiki.get = %+v, want the identical message %q", g, want)
		}
	})

	for _, sec := range []string{"frontmatter", "---", "Preamble", "", " FrontMatter ", "PREAMBLE", "\t---\n"} {
		t.Run("frontmatter or preamble "+strconv.Quote(sec), func(t *testing.T) {
			r := callTool(t, reg, "stage.patch_page", patchArgs(t, page, sec))
			want := `section ` + strconv.Quote(sec) + ` was not found on ` + page + `; valid headings are: ` + heads + frontmatterClause
			if !r.IsError || r.Content != want {
				t.Errorf("stage.patch_page(section %q) = %+v\nwant IsError with\n%s", sec, r, want)
			}
		})
	}

	t.Run("near misses do not get the frontmatter clause", func(t *testing.T) {
		for _, sec := range []string{"## preamble", "# Frontmatter", "frontmatter:", "Intro"} {
			r := callTool(t, reg, "stage.patch_page", patchArgs(t, page, sec))
			want := `section ` + strconv.Quote(sec) + ` was not found on ` + page + `; valid headings are: ` + heads
			if !r.IsError || r.Content != want {
				t.Errorf("stage.patch_page(section %q) = %+v\nwant IsError with\n%s", sec, r, want)
			}
		}
	})

	t.Run("a page with no headings says none", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		const body = "---\ntitle: Flat\ncreated: 2026-08-20\nupdated: 2026-08-29\ntype: concept\ntags: [inference]\nsources: [raw/articles/kv-cache-explained.md]\nconfidence: high\n---\n\nJust one paragraph, no headings at all.\n"
		if err := os.WriteFile(filepath.Join(dir, "wiki", "concepts", "flat.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		flat := NewRegistry(newTestDeps(t, dir))
		r := callTool(t, flat, "stage.patch_page", patchArgs(t, "wiki/concepts/flat.md", "## Related"))
		want := `section "## Related" was not found on wiki/concepts/flat.md; valid headings are: (none)`
		if !r.IsError || r.Content != want {
			t.Errorf("stage.patch_page = %+v\nwant IsError with\n%s", r, want)
		}
	})

	t.Run("the list comes from the staged base, not the committed page", func(t *testing.T) {
		reg, _, _ := engineRegistry(t, nil)
		if r := callTool(t, reg, "stage.open", `{"intent":"drop a section, then patch it"}`); r.IsError {
			t.Fatal(r.Content)
		}
		if r := callTool(t, reg, "stage.patch_page", `{"path":"`+page+`","section":"## Example","op":"remove_section","content":"","rationale":"drop the example"}`); r.IsError {
			t.Fatalf("remove: %s", r.Content)
		}
		r := callTool(t, reg, "stage.patch_page", patchArgs(t, page, "## Example"))
		want := `section "## Example" was not found on ` + page + `; valid headings are: # KV Cache, ## Abstract, ## Why it matters, ## Related`
		if !r.IsError || r.Content != want {
			t.Errorf("stage.patch_page = %+v\nwant IsError with\n%s", r, want)
		}
	})
}

// TestWikiGetResolvedPathLine pins D3: reading a page by anything but its
// exact path says where the page really is, in a line the model can copy
// into its next stage.* call — the root cause of the tilelang case, where a
// model that had just read "tilelang" by bare name then patched a path it
// invented. An exact-path read is unchanged, byte for byte.
func TestWikiGetResolvedPathLine(t *testing.T) {
	const path = "wiki/concepts/kv-cache.md"
	reg := minimalRegistry(t)

	exact := callTool(t, reg, "wiki.get", `{"page":"`+path+`"}`)
	if exact.IsError {
		t.Fatalf("exact wiki.get: %s", exact.Content)
	}
	if strings.HasPrefix(exact.Content, "resolved ") {
		t.Fatalf("an exact-path read must carry no resolved line:\n%.200s", exact.Content)
	}
	exactSection := callTool(t, reg, "wiki.get", `{"page":"`+path+`","section":"## Related"}`)
	if exactSection.IsError {
		t.Fatalf("exact wiki.get section: %s", exactSection.Content)
	}

	t.Run("a bare name gets the line, then the unchanged content", func(t *testing.T) {
		r := callTool(t, reg, "wiki.get", `{"page":"kv-cache"}`)
		line := `resolved "kv-cache" → wiki/concepts/kv-cache.md (use this path in stage.* calls)` + "\n\n"
		if !strings.HasPrefix(r.Content, line) {
			t.Fatalf("wiki.get(kv-cache) Content starts:\n%.200s\nwant prefix %q", r.Content, line)
		}
		if rest := strings.TrimPrefix(r.Content, line); rest != exact.Content {
			t.Errorf("content after the line differs from the exact-path read")
		}
	})

	t.Run("the same with section set", func(t *testing.T) {
		r := callTool(t, reg, "wiki.get", `{"page":"kv-cache","section":"## Related"}`)
		line := `resolved "kv-cache" → wiki/concepts/kv-cache.md (use this path in stage.* calls)` + "\n\n"
		if !strings.HasPrefix(r.Content, line) {
			t.Fatalf("wiki.get(kv-cache, section) Content starts:\n%.200s\nwant prefix %q", r.Content, line)
		}
		if rest := strings.TrimPrefix(r.Content, line); rest != exactSection.Content {
			t.Errorf("section content after the line differs from the exact-path read")
		}
	})

	t.Run("every non-exact spelling that resolves gets the line", func(t *testing.T) {
		for _, arg := range []string{"kv-cache.md", "KV-Cache", "wiki/concepts/kv-cache"} {
			r := callTool(t, reg, "wiki.get", `{"page":"`+arg+`"}`)
			if r.IsError {
				t.Errorf("wiki.get(%q): %s", arg, r.Content)
				continue
			}
			if want := resolvedLine(arg, path) + exact.Content; r.Content != want {
				t.Errorf("wiki.get(%q) Content starts:\n%.200s\nwant\n%.200s", arg, r.Content, want)
			}
		}
	})

	t.Run("an exact path with stray whitespace is still exact", func(t *testing.T) {
		r := callTool(t, reg, "wiki.get", `{"page":"  `+path+`\n"}`)
		if r.IsError || r.Content != exact.Content {
			t.Errorf("padded exact path = %+v, want it byte-identical to the exact read", r)
		}
	})

	t.Run("the line is the first thing, before the staged marker", func(t *testing.T) {
		reg, _, _ := engineRegistry(t, nil)
		if r := callTool(t, reg, "stage.open", `{"intent":"stage, then read back by name"}`); r.IsError {
			t.Fatal(r.Content)
		}
		if r := callTool(t, reg, "stage.patch_page", `{"path":"`+path+`","section":"## Example","op":"remove_section","content":"","rationale":"drop the example"}`); r.IsError {
			t.Fatalf("patch: %s", r.Content)
		}
		byPath := callTool(t, reg, "wiki.get", `{"page":"`+path+`"}`)
		if !strings.HasPrefix(byPath.Content, stagedSourceMarker) {
			t.Fatalf("exact read of a staged page lost its marker:\n%.200s", byPath.Content)
		}
		byName := callTool(t, reg, "wiki.get", `{"page":"kv-cache"}`)
		if want := resolvedLine("kv-cache", path) + byPath.Content; byName.Content != want {
			t.Errorf("staged bare-name read starts:\n%.250s\nwant the line, then the marker, then the content", byName.Content)
		}
	})

	t.Run("errors carry no line", func(t *testing.T) {
		// The section error already names the resolved path; a leading line
		// would put a second, redundant statement of it ahead of the cause.
		r := callTool(t, reg, "wiki.get", `{"page":"kv-cache","section":"## Nope"}`)
		if !r.IsError || strings.HasPrefix(r.Content, "resolved ") {
			t.Errorf("section error = %+v, want an IsError without a resolved line", r)
		}
		r = callTool(t, reg, "wiki.get", `{"page":"no-such-page"}`)
		if !r.IsError || strings.HasPrefix(r.Content, "resolved ") {
			t.Errorf("page error = %+v, want an IsError without a resolved line", r)
		}
	})
}

// TestStagedCandidatesCount pins D1's candidate set: committed paths plus
// what the open changeset has staged, each offered once. A model that just
// staged a source or created a page and then mistypes its path is guessing
// at something that exists only in the changeset — exactly where the
// committed vault alone cannot help it.
func TestStagedCandidatesCount(t *testing.T) {
	t.Run("a staged raw source is offered, once, in message order", func(t *testing.T) {
		reg, _ := namingRegistry(t, nil, namingDoc("Gemini", "# Gemini\n\nBody.\n"))
		nameOpen(t, reg)
		if r := nameIngest(t, reg, "gemini.md"); r.IsError || !strings.Contains(r.Content, "raw/articles/gemini.md") {
			t.Fatalf("ingest: %+v", r)
		}
		r := callTool(t, reg, "raw.get", `{"source":"raw/papers/gemini.md"}`)
		want := rawNotFound("raw/papers/gemini.md") +
			"; closest raw sources: raw/articles/gemini.md" + rawListPointer +
			"; staged raw sources in the open changeset: raw/articles/gemini.md"
		if !r.IsError || r.Content != want {
			t.Errorf("raw.get not-found = %+v\nwant IsError with\n%s", r, want)
		}
		if n := strings.Count(r.Content, "closest raw sources: raw/articles/gemini.md"); n != 1 {
			t.Errorf("closest clause appears %d times, want 1", n)
		}
	})

	t.Run("a dropped ingest is no longer a candidate", func(t *testing.T) {
		reg, e := namingRegistry(t, nil, namingDoc("Gemini", "# Gemini\n\nBody.\n"))
		nameOpen(t, reg)
		if r := nameIngest(t, reg, "gemini.md"); r.IsError {
			t.Fatalf("ingest: %s", r.Content)
		}
		cs, err := e.Current()
		if err != nil {
			t.Fatal(err)
		}
		if len(cs.Ops) != 1 {
			t.Fatalf("len(cs.Ops) = %d, want the one ingest op", len(cs.Ops))
		}
		if err := e.DropOp(cs.Ops[0].ID); err != nil {
			t.Fatalf("DropOp: %v", err)
		}
		r := callTool(t, reg, "raw.get", `{"source":"raw/papers/gemini.md"}`)
		if want := rawNotFound("raw/papers/gemini.md") + rawListPointer; !r.IsError || r.Content != want {
			t.Errorf("raw.get not-found = %+v\nwant IsError with\n%s", r, want)
		}
	})

	t.Run("a page created in the changeset is offered, once", func(t *testing.T) {
		reg, _, _ := engineRegistry(t, nil)
		if r := callTool(t, reg, "stage.open", `{"intent":"create a page, then mistype its path"}`); r.IsError {
			t.Fatal(r.Content)
		}
		if r := callTool(t, reg, "stage.create_page", `{"path":"wiki/concepts/tilelang.md","title":"TileLang","type":"concept","tags":["inference","memory"],"sources":["raw/papers/leviathan-2023.md"],"confidence":"high","contested":false,"body":"TileLang is a GPU kernel language. See [[kv-cache]] and [[gpt-4]].","rationale":"new concept"}`); r.IsError {
			t.Fatalf("create: %s", r.Content)
		}
		want := `page "wiki/entities/tilelang.md" was not found; closest pages: wiki/concepts/tilelang.md`
		r := callTool(t, reg, "stage.patch_page", patchArgs(t, "wiki/entities/tilelang.md", "## Related"))
		if !r.IsError || r.Content != want {
			t.Errorf("stage.patch_page not-found = %+v\nwant IsError with\n%s", r, want)
		}
		if n := strings.Count(r.Content, "wiki/concepts/tilelang.md"); n != 1 {
			t.Errorf("staged page named %d times, want 1", n)
		}
		l := callTool(t, reg, "stage.add_link", `{"from":"wiki/entities/tilelang.md","to":"wiki/concepts/kv-cache.md"}`)
		wantLink := `from page "wiki/entities/tilelang.md" was not found; closest pages: wiki/concepts/tilelang.md`
		if !l.IsError || l.Content != wantLink {
			t.Errorf("stage.add_link not-found = %+v\nwant IsError with\n%s", l, wantLink)
		}
	})

	t.Run("a committed page with a staged patch is offered once", func(t *testing.T) {
		reg, _, _ := engineRegistry(t, nil)
		if r := callTool(t, reg, "stage.open", `{"intent":"patch a page, then mistype its path"}`); r.IsError {
			t.Fatal(r.Content)
		}
		if r := callTool(t, reg, "stage.patch_page", `{"path":"wiki/concepts/kv-cache.md","section":"## Example","op":"remove_section","content":"","rationale":"drop the example"}`); r.IsError {
			t.Fatalf("patch: %s", r.Content)
		}
		r := callTool(t, reg, "stage.patch_page", patchArgs(t, "wiki/entities/kv-cache.md", "## Related"))
		want := `page "wiki/entities/kv-cache.md" was not found; closest pages: wiki/concepts/kv-cache.md`
		if !r.IsError || r.Content != want {
			t.Errorf("stage.patch_page not-found = %+v\nwant IsError with\n%s", r, want)
		}
	})
}
