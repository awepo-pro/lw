package tools

// patch_guard_test.go freezes 043 T1's contract over the real bytes that
// forced it: in session 36 a live model twice sent one paragraph as the
// whole new body of stage.patch_page's replace_section when it only meant
// to add a link, silently erasing the rest of wiki/entities/tilelang.md's
// "## GPU programming model". testdata/section-edit/ holds that session's
// ground — the page as op3 left it, the page op8 actually produced, and
// op8's exact tool arguments — and these tests replay op8 against them.
// replace_section that would keep under half of a 400+-byte section, or
// drop a subsection heading, is refused with the deleted text named and the
// replace_text escape hatch pointed at; allow_shrink restores the old
// byte-for-byte behaviour for rewrites that mean it.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// op8DeletedLines are the three deleted-paragraph entries both shrink
// refusals must name: the first 80 runes of the section's first three
// paragraphs, each cut mid-word and closed with "…".
var op8DeletedLines = []string{
	"- \"TileLang treats tiles — shaped chunks of data owned by a warp or thread block — …\"",
	"- \"Dataflow is expressed with tile operators (`T.copy`, `T.gemm`, `T.reduce`, `T.at…\"",
	"- \"Two compiler abstractions carry the thread mapping: a composable **Layout** (bui…\"",
}

// tilelangRegistry is engineRegistry with the session-36 ground page added:
// tilelang-after-op3.md is placed at wiki/entities/tilelang.md before the
// engine opens the fixture copy, so stage.patch_page composes against the
// real bytes op8 patched. The op3 file bytes come back for the tests that
// build their expected results from them.
func tilelangRegistry(t *testing.T) (*Registry, *stage.Engine, []byte) {
	t.Helper()
	dir := testutil.CopyFixture(t, "minimal")
	op3, err := os.ReadFile("testdata/section-edit/tilelang-after-op3.md")
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "wiki", "entities", "tilelang.md")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, op3, 0o644); err != nil {
		t.Fatal(err)
	}
	// The ground page carries the live vault's tags (tools, reference);
	// the minimal fixture's taxonomy does not know them, and Append
	// validates a staged op's post-image frontmatter against it — so the
	// copy gains the two tags before the engine opens.
	schemaPath := filepath.Join(dir, "SCHEMA.md")
	sb, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	extended := strings.Replace(string(sb), "\n## Conventions",
		"\n- `tools` — software the vault tracks.\n- `reference` — reference material about them.\n\n## Conventions", 1)
	if extended == string(sb) {
		t.Fatal("fixture SCHEMA.md has no ## Conventions section to extend")
	}
	if err := os.WriteFile(schemaPath, []byte(extended), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := stage.OpenEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	v := e.Vault()
	reg := NewRegistry(Deps{Vault: v, Index: index.Build(v), Engine: e, Author: stage.Author{Kind: "agent", Model: "test"}})
	return reg, e, op3
}

// op8Args reads op8-args.json and returns its arguments verbatim, as the
// JSON body a registry call takes. withAllowShrink adds the override field
// the refusal message tells the model to repeat the call with.
func op8Args(t *testing.T, withAllowShrink bool) string {
	t.Helper()
	b, err := os.ReadFile("testdata/section-edit/op8-args.json")
	if err != nil {
		t.Fatal(err)
	}
	var args map[string]any
	if err := json.Unmarshal(b, &args); err != nil {
		t.Fatal(err)
	}
	args["allow_shrink"] = withAllowShrink
	out, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// assertNothingStaged fails the test unless the open changeset is empty —
// a refused patch_page must leave no op behind.
func assertNothingStaged(t *testing.T, e *stage.Engine) {
	t.Helper()
	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Ops) != 0 {
		t.Fatalf("refused patch left %d op(s) staged:\n%s", len(cs.Ops), cs.Ops[0].Rationale)
	}
}

// TestPatchReplaceSectionRefusesRealOp8 replays the exact tool arguments
// op8 sent live, which replaced a four-paragraph section with its own last
// paragraph (19% of the bytes kept). The refusal must name the shrink, list
// the deleted paragraphs and point at replace_text / allow_shrink — a
// reason the model can act on, instead of the silent erase — and stage
// nothing.
func TestPatchReplaceSectionRefusesRealOp8(t *testing.T) {
	reg, e, _ := tilelangRegistry(t)
	if r := callTool(t, reg, "stage.open", `{"intent":"add an nvitop link"}`); r.IsError {
		t.Fatal(r.Content)
	}
	r := callTool(t, reg, "stage.patch_page", op8Args(t, false))
	if !r.IsError {
		t.Fatalf("op8 accepted without allow_shrink:\n%s", r.Content)
	}
	want := strings.Join([]string{
		`replace_section "## GPU programming model" keeps 618 of 3161 bytes (19%) of the section; it would delete:`,
		op8DeletedLines[0],
		op8DeletedLines[1],
		op8DeletedLines[2],
		`For a small edit (a link, a sentence) use op replace_text. If deleting this text is intended, repeat the call with "allow_shrink": true.`,
	}, "\n")
	if r.Content != want {
		t.Fatalf("refusal =\n%s\nwant =\n%s", r.Content, want)
	}
	assertNothingStaged(t, e)
}

// TestPatchReplaceSectionAllowShrink replays op8 with the override the
// refusal asks for: the staged op's content must be byte-identical to the
// tilelang-after-op8.md ground — exactly what lw produced live — proving
// allow_shrink restores the pre-043 behaviour rather than a normalized
// variant of it.
func TestPatchReplaceSectionAllowShrink(t *testing.T) {
	reg, e, _ := tilelangRegistry(t)
	if r := callTool(t, reg, "stage.open", `{"intent":"add an nvitop link"}`); r.IsError {
		t.Fatal(r.Content)
	}
	r := callTool(t, reg, "stage.patch_page", op8Args(t, true))
	if r.IsError {
		t.Fatalf("op8 refused with allow_shrink: %s", r.Content)
	}
	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Ops) != 1 {
		t.Fatalf("len(cs.Ops) = %d, want 1", len(cs.Ops))
	}
	want, err := os.ReadFile("testdata/section-edit/tilelang-after-op8.md")
	if err != nil {
		t.Fatal(err)
	}
	// Append clears Op.Content after storing the bytes (MASTER §9 D-AY);
	// the staged projection is where the post-image lives now, and it is
	// the bytes the next chained patch would compose against.
	got, staged, err := e.StagedFile("wiki/entities/tilelang.md")
	if err != nil {
		t.Fatal(err)
	}
	if !staged {
		t.Fatal("op8 page is not staged after acceptance")
	}
	if string(got) != string(want) {
		t.Fatalf("staged op content differs from the live op8 output")
	}
}

// TestPatchReplaceTextKeepsSection does what op8 meant, done right: a
// replace_text that quotes only the sentence gaining the link. The staged
// content must equal the page bytes with exactly that one replacement —
// every byte op8 erased stays — and the op and its hunks carry the section
// argument like every other op.
func TestPatchReplaceTextKeepsSection(t *testing.T) {
	reg, e, op3 := tilelangRegistry(t)
	const find = "recur in [[batch-invariant-deterministic-kernels]]."
	const content = find + " Tools like [[nvitop]] make the resulting utilization observable."
	if n := strings.Count(string(op3), find); n != 1 {
		t.Fatalf("ground page: find occurs %d times, want 1", n)
	}
	if r := callTool(t, reg, "stage.open", `{"intent":"add an nvitop link"}`); r.IsError {
		t.Fatal(r.Content)
	}
	r := callTool(t, reg, "stage.patch_page", `{"path":"wiki/entities/tilelang.md","section":"## GPU programming model","op":"replace_text","find":"`+find+`","content":"`+content+`","rationale":"link nvitop where utilization is watched"}`)
	if r.IsError {
		t.Fatalf("replace_text refused: %s", r.Content)
	}
	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Ops) != 1 {
		t.Fatalf("len(cs.Ops) = %d, want 1", len(cs.Ops))
	}
	op := cs.Ops[0]
	want := strings.Replace(string(op3), find, content, 1)
	// Op.Content is cleared by Append (MASTER §9 D-AY); the staged
	// projection carries the post-image.
	got, staged, err := e.StagedFile("wiki/entities/tilelang.md")
	if err != nil {
		t.Fatal(err)
	}
	if !staged {
		t.Fatal("tilelang.md is not staged after replace_text")
	}
	if string(got) != want {
		t.Fatalf("staged content is not the page with exactly the quoted replacement")
	}
	if op.Section != "## GPU programming model" {
		t.Fatalf("op Section = %q, want the section argument", op.Section)
	}
	if len(op.Hunks) == 0 {
		t.Fatal("op carries no hunks")
	}
	for _, h := range op.Hunks {
		if h.Path != "wiki/entities/tilelang.md" || h.Section != "## GPU programming model" {
			t.Fatalf("hunk = %s/%s, want the page path and section argument", h.Path, h.Section)
		}
	}
}

// TestPatchReplaceTextRefusals pins the four bad-input refusals plus the
// section-scoping one: a find present on the page but only in ANOTHER
// section is "not found", because replace_text must never reach past the
// section it was given. Each refusal stages nothing.
func TestPatchReplaceTextRefusals(t *testing.T) {
	reg, e, op3 := tilelangRegistry(t)
	// "## Related"'s first body line, read from the fixture: present once
	// on the page, but in a sibling section. The blank line between a
	// heading and its body is not a body line — skip it.
	s := string(op3)
	ri := strings.Index(s, "## Related\n") + len("## Related\n")
	for s[ri] == '\n' {
		ri++
	}
	otherSectionFind := s[ri : ri+strings.IndexByte(s[ri:], '\n')]
	if otherSectionFind == "" {
		t.Fatal("fixture: no body line found under ## Related")
	}

	cases := []struct{ name, find, content, want string }{
		{
			name:    "empty find",
			find:    "",
			content: "x",
			want:    `replace_text needs find: the exact text to replace, copied from the page`,
		},
		{
			name:    "absent from section",
			find:    "[[dcgm]]",
			content: "x",
			want:    `find text was not found in section "## GPU programming model" of wiki/entities/tilelang.md; copy it exactly from wiki.get (whitespace and punctuation included)`,
		},
		{
			name:    "occurs four times",
			find:    "^[raw/papers/tilelang-a-composable",
			content: "x",
			want:    `find text occurs 4 times in section "## GPU programming model" of wiki/entities/tilelang.md; include more surrounding text so it matches exactly once`,
		},
		{
			name:    "no-op",
			find:    "recur in",
			content: "recur in",
			want:    `find and content are identical; nothing to change`,
		},
		{
			name:    "present only in another section",
			find:    otherSectionFind,
			content: "x",
			want:    `find text was not found in section "## GPU programming model" of wiki/entities/tilelang.md; copy it exactly from wiki.get (whitespace and punctuation included)`,
		},
	}
	if r := callTool(t, reg, "stage.open", `{"intent":"probe replace_text refusals"}`); r.IsError {
		t.Fatal(r.Content)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args, err := json.Marshal(map[string]any{
				"path":      "wiki/entities/tilelang.md",
				"section":   "## GPU programming model",
				"op":        "replace_text",
				"find":      tc.find,
				"content":   tc.content,
				"rationale": "probe",
			})
			if err != nil {
				t.Fatal(err)
			}
			r := callTool(t, reg, "stage.patch_page", string(args))
			if !r.IsError {
				t.Fatalf("accepted:\n%s", r.Content)
			}
			if r.Content != tc.want {
				t.Fatalf("refusal =\n%s\nwant =\n%s", r.Content, tc.want)
			}
			assertNothingStaged(t, e)
		})
	}
}

// TestPatchReplaceTextShrinkGuard proves the shrink guard binds replace_text
// too: deleting the section's first three paragraphs is exactly the erase
// replace_text exists to prevent, so the same refusal shape fires — with
// replace_text named in the header — and allow_shrink lets the meant
// deletion through.
func TestPatchReplaceTextShrinkGuard(t *testing.T) {
	reg, e, op3 := tilelangRegistry(t)
	page, err := vault.ParsePage("wiki/entities/tilelang.md", op3)
	if err != nil {
		t.Fatal(err)
	}
	sec, ok := page.Section("## GPU programming model")
	if !ok {
		t.Fatal(`section "## GPU programming model" not found in the ground page`)
	}
	paras := strings.Split(strings.TrimSpace(page.Body[sec.Body:sec.End]), "\n\n")
	if len(paras) != 4 {
		t.Fatalf("ground section has %d paragraphs, want 4", len(paras))
	}
	find := paras[0] + "\n\n" + paras[1] + "\n\n" + paras[2]

	if r := callTool(t, reg, "stage.open", `{"intent":"delete most of a section"}`); r.IsError {
		t.Fatal(r.Content)
	}
	argsMap := map[string]any{
		"path":      "wiki/entities/tilelang.md",
		"section":   "## GPU programming model",
		"op":        "replace_text",
		"find":      find,
		"content":   "",
		"rationale": "drop the first three paragraphs",
	}
	args, err := json.Marshal(argsMap)
	if err != nil {
		t.Fatal(err)
	}
	r := callTool(t, reg, "stage.patch_page", string(args))
	if !r.IsError {
		t.Fatalf("three-paragraph deletion accepted without allow_shrink:\n%s", r.Content)
	}
	if !strings.HasPrefix(r.Content, `replace_text "## GPU programming model" keeps `) {
		t.Fatalf("refusal does not name the op and section:\n%s", r.Content)
	}
	for _, line := range op8DeletedLines {
		if !strings.Contains(r.Content, line) {
			t.Fatalf("refusal missing deleted-paragraph line:\n%s", line)
		}
	}
	assertNothingStaged(t, e)

	argsMap["allow_shrink"] = true
	args, err = json.Marshal(argsMap)
	if err != nil {
		t.Fatal(err)
	}
	if r := callTool(t, reg, "stage.patch_page", string(args)); r.IsError {
		t.Fatalf("three-paragraph deletion refused with allow_shrink: %s", r.Content)
	}
}

// TestPatchReplaceSectionRefusesNestedLoss replays the session-35 H1 shape:
// replace_section on "# TileLang" with the page's intro alone, which would
// take all seven subsections with it. The refusal names every heading it
// would delete, in document order, and allow_shrink does NOT override it —
// deleting a subsection has its own op. Keeping every heading line is
// accepted: the guard blocks lost subsections, not rewrites.
func TestPatchReplaceSectionRefusesNestedLoss(t *testing.T) {
	reg, e, op3 := tilelangRegistry(t)
	if r := callTool(t, reg, "stage.open", `{"intent":"rewrite the intro"}`); r.IsError {
		t.Fatal(r.Content)
	}
	r := callTool(t, reg, "stage.patch_page", `{"path":"wiki/entities/tilelang.md","section":"# TileLang","op":"replace_section","content":"# TileLang","rationale":"tighten the intro"}`)
	if !r.IsError {
		t.Fatalf("intro-only rewrite accepted:\n%s", r.Content)
	}
	want := `replace_section "# TileLang" would delete its subsections "## Abstract", "## Why a DSL", "## Host Codegen", "## SMT-solver-assisted integer analysis", "## Numerical precision and bitwise reproducibility", "## GPU programming model", "## Related"; target the subsection you mean, or keep every subsection heading line in content.`
	if r.Content != want {
		t.Fatalf("refusal =\n%s\nwant =\n%s", r.Content, want)
	}
	assertNothingStaged(t, e)

	if r := callTool(t, reg, "stage.patch_page", `{"path":"wiki/entities/tilelang.md","section":"# TileLang","op":"replace_section","content":"# TileLang","allow_shrink":true,"rationale":"tighten the intro"}`); !r.IsError || r.Content != want {
		t.Fatalf("allow_shrink changed the nested refusal: isErr=%v content=\n%s", r.IsError, r.Content)
	}

	// The same call with every heading line kept is accepted: change one
	// sentence of the current body, nothing else.
	page, err := vault.ParsePage("wiki/entities/tilelang.md", op3)
	if err != nil {
		t.Fatal(err)
	}
	sec, ok := page.Section("# TileLang")
	if !ok {
		t.Fatal(`section "# TileLang" not found in the ground page`)
	}
	body := page.Body[sec.Body:sec.End]
	const sentence = "A Python-embedded domain-specific language for writing high-performance GPU kernels"
	content := strings.Replace(body, sentence, "A Python-embedded DSL for writing high-performance GPU kernels", 1)
	if content == body {
		t.Fatal("sentence to change not found in the ground body")
	}
	args, err := json.Marshal(map[string]any{
		"path":      "wiki/entities/tilelang.md",
		"section":   "# TileLang",
		"op":        "replace_section",
		"content":   content,
		"rationale": "tighten the opening sentence",
	})
	if err != nil {
		t.Fatal(err)
	}
	if r := callTool(t, reg, "stage.patch_page", string(args)); r.IsError {
		t.Fatalf("heading-preserving rewrite refused: %s", r.Content)
	}
}

// TestPatchShrinkGuardSmallSection pins the floor: "## Why it matters" is
// ~210 bytes, under the 400-byte threshold, so replacing it whole is small
// enough that Review shows the result anyway — accepted, exactly as the
// existing staged-read tests have always driven it.
func TestPatchShrinkGuardSmallSection(t *testing.T) {
	reg, _, _ := engineRegistry(t, nil)
	if r := callTool(t, reg, "stage.open", `{"intent":"replace a small section"}`); r.IsError {
		t.Fatal(r.Content)
	}
	r := callTool(t, reg, "stage.patch_page", `{"path":"wiki/concepts/kv-cache.md","section":"## Why it matters","op":"replace_section","content":"Staged replacement body.","rationale":"rewrite the motivation"}`)
	if r.IsError {
		t.Fatalf("small-section rewrite refused: %s", r.Content)
	}
}

// TestPatchPageSchemaDescribesOps pins 043 T1's schema texts — they are what
// tells the model which op to use, so the exact wording is the contract:
// the tool description leads with replace_text for small edits, the op enum
// lists it first, every property carries its description, and find /
// allow_shrink are typed.
func TestPatchPageSchemaDescribesOps(t *testing.T) {
	reg, _, _ := engineRegistry(t, nil)
	tool, ok := reg.Get("stage.patch_page")
	if !ok {
		t.Fatal("stage.patch_page not registered")
	}
	wantDescription := `Propose a section-level page patch. For a small edit — add a link, fix a sentence — use op replace_text: it changes only the text you quote. replace_section rewrites the whole section body, subsections included. When the open changeset already stages the page, the patch composes against that staged state, not the committed bytes.`
	if tool.Description != wantDescription {
		t.Fatalf("tool Description =\n%s\nwant =\n%s", tool.Description, wantDescription)
	}

	var schema struct {
		Properties map[string]struct {
			Type        string   `json:"type"`
			Description string   `json:"description"`
			Enum        []string `json:"enum"`
		} `json:"properties"`
		Required             []string `json:"required"`
		AdditionalProperties bool     `json:"additionalProperties"`
	}
	if err := json.Unmarshal(tool.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	wantEnum := []string{"replace_text", "replace_section", "append_section", "insert_after", "insert_before", "remove_section"}
	if !reflect.DeepEqual(schema.Properties["op"].Enum, wantEnum) {
		t.Fatalf("op enum = %#v, want %#v", schema.Properties["op"].Enum, wantEnum)
	}
	wantRequired := []string{"path", "section", "op", "content", "rationale"}
	if !reflect.DeepEqual(schema.Required, wantRequired) {
		t.Fatalf("required = %#v, want %#v (unchanged)", schema.Required, wantRequired)
	}
	if schema.AdditionalProperties {
		t.Fatal("additionalProperties = true, want false")
	}
	wantDescriptions := map[string]string{
		"path":         `Vault-relative page path, e.g. wiki/concepts/kv-cache.md.`,
		"section":      `The exact heading line of the target section, e.g. "## Related".`,
		"op":           `replace_text: replace the find text, which must occur exactly once inside the section. replace_section: replace the WHOLE section body below its heading, subsections included. append_section: add content at the end of the section. insert_after / insert_before: add content as a new section next to this one. remove_section: delete the section.`,
		"find":         `replace_text only: the exact text to replace, copied from the page. It must occur exactly once inside the section.`,
		"content":      `replace_text: the replacement for find. replace_section: the new body WITHOUT the heading line. append_section: the text to add. insert_after / insert_before: the new section, heading line included. remove_section: send "".`,
		"allow_shrink": `Set true only when you mean to delete most of a section. Without it, a replace_section or replace_text that drops more than half of a section of 400 bytes or more is refused.`,
		"rationale":    `One sentence: why this change.`,
	}
	for prop, want := range wantDescriptions {
		p, ok := schema.Properties[prop]
		if !ok {
			t.Fatalf("schema property %q missing", prop)
		}
		if p.Description != want {
			t.Fatalf("description of %q =\n%s\nwant =\n%s", prop, p.Description, want)
		}
	}
	if got := schema.Properties["find"].Type; got != "string" {
		t.Fatalf("find type = %q, want string", got)
	}
	if got := schema.Properties["allow_shrink"].Type; got != "boolean" {
		t.Fatalf("allow_shrink type = %q, want boolean", got)
	}
}

// TestPatchGuardChainedMeasuresStagedBase pins which bytes the shrink
// guard's "old" comes from on a chained patch (043 T1): the second
// patch_page on a staged path composes against the staged bytes
// (stagedPatchBase, 020 T-B), so the guard must measure the staged section
// body — 618 bytes after op8 — not the committed one (3161). Measuring the
// committed base would both quote the wrong ratio and list paragraphs that
// are already gone from the staged state.
func TestPatchGuardChainedMeasuresStagedBase(t *testing.T) {
	reg, e, _ := tilelangRegistry(t)
	if r := callTool(t, reg, "stage.open", `{"intent":"chained condense"}`); r.IsError {
		t.Fatal(r.Content)
	}
	if r := callTool(t, reg, "stage.patch_page", op8Args(t, true)); r.IsError {
		t.Fatalf("op8 with allow_shrink: %s", r.Content)
	}
	r := callTool(t, reg, "stage.patch_page", `{"path":"wiki/entities/tilelang.md","section":"## GPU programming model","op":"replace_section","content":"Utilization is observable via [[nvitop]].","rationale":"condense to one line"}`)
	if !r.IsError {
		t.Fatalf("chained condense accepted without allow_shrink:\n%s", r.Content)
	}
	if !strings.Contains(r.Content, "of 618 bytes") {
		t.Fatalf("shrink guard measured the committed base, not the staged one:\n%s", r.Content)
	}
	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Ops) != 1 {
		t.Fatalf("refused chained patch changed the changeset: %d ops, want 1", len(cs.Ops))
	}
	args := `{"path":"wiki/entities/tilelang.md","section":"## GPU programming model","op":"replace_section","content":"Utilization is observable via [[nvitop]].","allow_shrink":true,"rationale":"condense to one line"}`
	if r := callTool(t, reg, "stage.patch_page", args); r.IsError {
		t.Fatalf("chained condense refused with allow_shrink: %s", r.Content)
	}
	got, staged, err := e.StagedFile("wiki/entities/tilelang.md")
	if err != nil {
		t.Fatal(err)
	}
	if !staged {
		t.Fatal("tilelang.md is not staged after the chained patch")
	}
	if !strings.Contains(string(got), "Utilization is observable via [[nvitop]].") || strings.Contains(string(got), "This fine-grained GPU control") {
		t.Fatal("staged page is not the staged base with the condense composed onto it")
	}
}

// TestRefuseNestedLossLineSemantics pins the heading-line comparison behind
// the nested guard (043 T1) at the unit level, where the registry path
// cannot reach: a heading inside a fenced code block is not a section
// (ParseSections walks the AST), so it is never demanded; a heading text
// that occurs only as the substring of a longer content line does not count
// as kept; and a kept heading line may carry trailing spaces or tabs, the
// only normalization buildSection itself applies.
func TestRefuseNestedLossLineSemantics(t *testing.T) {
	body := "---\ntitle: Doc\ntype: concept\ntags: [x]\n---\n\n# Doc\n\nintro paragraph.\n\n## Real\n\n```text\n## Fake\n```\n\ntail paragraph.\n"
	page, err := vault.ParsePage("wiki/x.md", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := page.Section("## Fake"); ok {
		t.Fatal(`"## Fake" inside the fenced block parsed as a section`)
	}
	sec, ok := page.Section("# Doc")
	if !ok {
		t.Fatal(`section "# Doc" not found`)
	}
	res := refuseNestedLoss(page, sec, "intro only")
	if res == nil || !strings.Contains(res.Content, `"## Real"`) || strings.Contains(res.Content, "## Fake") {
		t.Fatalf("fenced pseudo-heading demanded or real heading missed:\n%v", res)
	}
	if res := refuseNestedLoss(page, sec, "intro, and we discuss ## Real below"); res == nil || !strings.Contains(res.Content, `"## Real"`) {
		t.Fatalf("substring occurrence counted as keeping the heading:\n%v", res)
	}
	for _, content := range []string{"## Real\n\nkept body", "## Real  \n\nkept body", "## Real\t\nkept body"} {
		if res := refuseNestedLoss(page, sec, content); res != nil {
			t.Fatalf("exact heading line refused: %q →\n%s", content, res.Content)
		}
	}
}

// TestRefuseShrinkListCaps pins the deleted-text list's presentation
// contract (043 T1) at the unit level: at most 5 paragraph entries (then
// "- … and N more"), the 80-rune cut never splits a UTF-8 rune, "…"
// appears only on paragraphs that were actually cut, and a block with an
// indented first line is compared by its trimmed text — kept text stays out
// of the deletion list.
func TestRefuseShrinkListCaps(t *testing.T) {
	kept := "kept paragraph survives verbatim in the new body."
	old := strings.Join([]string{
		strings.Repeat("a", 79) + "ééééé", // 84 runes; rune 80 is multibyte
		"    " + kept + "\nsecond line",   // indented block: compared trimmed
		"gone paragraph",
		strings.Repeat("x", 100),
		strings.Repeat("y", 100),
		strings.Repeat("z", 100),
	}, "\n\n")
	body := "## S\n" + old
	sec := vault.Section{Level: 2, Heading: "## S", Start: 0, Body: 5, End: len(body)}
	newRegion := "intro\n" + kept + "\nsecond line\nend"
	res := refuseShrink(body, sec, newRegion, stagePatchPageArgs{Op: "replace_section", Section: "## S"})
	if res == nil || !res.IsError {
		t.Fatalf("six-paragraph deletion not refused: %v", res)
	}
	if !strings.HasPrefix(res.Content, `replace_section "## S" keeps `) {
		t.Fatalf("refusal does not name the op and section:\n%s", res.Content)
	}
	// The 80-rune cut lands on "é", not inside it: 79 a's + one intact
	// "é" + "…".
	wantCut := `- "` + strings.Repeat("a", 79) + "é…" + `"`
	if !strings.Contains(res.Content, wantCut) {
		t.Fatalf("multibyte rune cut badly:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, `- "gone paragraph"`) || strings.Contains(res.Content, "gone paragraph…") {
		t.Fatalf("uncut paragraph must not carry …:\n%s", res.Content)
	}
	if n := strings.Count(res.Content, `…"`); n != 4 { // p1 + the three 100-rune paragraphs
		t.Fatalf("… on %d entries, want 4:\n%s", n, res.Content)
	}
	if strings.Contains(res.Content, "- … and") {
		t.Fatalf("… and N more without a sixth deleted paragraph:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "kept paragraph") {
		t.Fatalf("kept indented block listed as deleted:\n%s", res.Content)
	}

	// Past 5 deleted paragraphs the list caps and names the remainder.
	more := strings.Join([]string{
		strings.Repeat("a", 79) + "ééééé",
		"gone paragraph",
		strings.Repeat("x", 100),
		strings.Repeat("y", 100),
		strings.Repeat("z", 100),
		strings.Repeat("w", 100),
		strings.Repeat("v", 100),
	}, "\n\n")
	body = "## S\n" + more
	sec.End = len(body)
	res = refuseShrink(body, sec, "tiny", stagePatchPageArgs{Op: "replace_section", Section: "## S"})
	if res == nil || !strings.Contains(res.Content, "- … and 2 more") {
		t.Fatalf("cap at 5 + remainder not applied:\n%v", res)
	}
	if strings.Contains(res.Content, `"`+strings.Repeat("w", 20)) {
		t.Fatalf("seventh paragraph listed past the cap:\n%s", res.Content)
	}
}
