package tools

// page_hygiene_test.go freezes 050's contract over the two ways an ingest
// turn left a page lint-dirty: the model echoing a section's own heading as
// the first line of append_section / replace_section content (23 new
// duplicate-section warns on the 049 baseline), and a body that cites a raw
// source its sources: list never gained (23 new cite-source warns) because
// patch_page cannot edit frontmatter. Every test builds a real vault
// fixture — the minimal one, whose kv-cache page has a "## Related" section
// and a sources: list — and drives the registry the way the model does, so a
// handler that stops stripping, or stops syncing, goes red here.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

const (
	hygienePage = "wiki/concepts/kv-cache.md"

	// hygieneEchoNote* are the frozen dropped-heading notes, one per op.
	hygieneEchoNoteAppend  = `note: dropped the leading "## Related" from content; append_section content is the section body without its heading.`
	hygieneEchoNoteReplace = `note: dropped the leading "## Related" from content; replace_section content is the section body without its heading.`

	// hygieneNewNote is the frozen sources note for one added source.
	hygieneNewNote = `note: added raw/articles/new.md to sources: (the body cites them).`
)

// hygieneExtractor is a uri-keyed fake extractor: unlike fakeExtractor it can
// stage several DIFFERENT sources in one changeset, which the create_page
// test needs (one listed source and one only the body cites).
type hygieneExtractor map[string]*extract.Doc

func (h hygieneExtractor) CanHandle(uri string) bool { _, ok := h[uri]; return ok }
func (h hygieneExtractor) Extract(_ context.Context, uri string) (*extract.Doc, error) {
	return h[uri], nil
}

// hygieneDoc is a distinct-bodied article, so the engine's sha dedupe never
// takes two of them for one source.
func hygieneDoc(title, body string) *extract.Doc {
	return &extract.Doc{Title: title, SourceURL: "https://example.test/" + strings.ToLower(title), Markdown: "# " + title + "\n\n" + body + "\n", Kind: "article", Extractor: "test"}
}

// hygieneSetup returns a registry over the minimal fixture with a changeset
// already open and three stageable sources: new.md -> raw/articles/new.md,
// a.md -> raw/articles/a.md, b.md -> raw/articles/b.md. Nothing is staged
// yet; a test stages what it needs with hygieneIngest.
func hygieneSetup(t *testing.T) (*Registry, *stage.Engine) {
	t.Helper()
	reg, e, _ := engineRegistry(t, hygieneExtractor{
		"new.md": hygieneDoc("New", "A fresh source about cache eviction."),
		"a.md":   hygieneDoc("A", "The first source, listed by the page."),
		"b.md":   hygieneDoc("B", "The second source, cited only in the body."),
	})
	if r, err := reg.Call(context.Background(), "stage.open", json.RawMessage(`{"intent":"page hygiene"}`)); err != nil || r.IsError {
		t.Fatalf("stage.open: %+v %v", r, err)
	}
	return reg, e
}

// hygieneIngest stages uri through stage.ingest_source and returns the raw
// path it landed at.
func hygieneIngest(t *testing.T, reg *Registry, uri string) string {
	t.Helper()
	r, err := reg.Call(context.Background(), "stage.ingest_source", json.RawMessage(`{"uri":"`+uri+`","kind":"article"}`))
	if err != nil || r.IsError {
		t.Fatalf("stage.ingest_source %s: %+v %v", uri, r, err)
	}
	return "raw/articles/" + strings.TrimSuffix(uri, ".md") + ".md"
}

// hygieneCall calls tool with args marshalled from a map, so a content
// string with newlines and quotes is never hand-escaped.
func hygieneCall(t *testing.T, reg *Registry, tool string, args map[string]any) Result {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	r, err := reg.Call(context.Background(), tool, b)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return r
}

// hygienePatch patches the kv-cache page's section with op and content.
func hygienePatch(t *testing.T, reg *Registry, section, op, content string) Result {
	t.Helper()
	return hygieneCall(t, reg, "stage.patch_page", map[string]any{
		"path": hygienePage, "section": section, "op": op, "content": content, "rationale": "test",
	})
}

// hygieneStaged parses the page the open changeset currently stages at path.
func hygieneStaged(t *testing.T, e *stage.Engine, path string) *vault.Page {
	t.Helper()
	b, ok, err := e.StagedFile(path)
	if err != nil || !ok {
		t.Fatalf("StagedFile(%s) = ok %v, err %v", path, ok, err)
	}
	p, err := vault.ParsePage(path, b)
	if err != nil {
		t.Fatalf("ParsePage(%s): %v", path, err)
	}
	return p
}

// hygieneHeadings counts the staged page's sections whose heading line is
// exactly heading.
func hygieneHeadings(p *vault.Page, heading string) int {
	n := 0
	for _, s := range p.Sections {
		if s.Heading == heading {
			n++
		}
	}
	return n
}

// hygieneLastLine is the final line of a tool result.
func hygieneLastLine(content string) string {
	lines := strings.Split(content, "\n")
	return lines[len(lines)-1]
}

// hygieneHasNote reports whether any line of a tool result is a note.
func hygieneHasNote(content string) bool {
	for _, l := range strings.Split(content, "\n") {
		if strings.HasPrefix(l, "note:") {
			return true
		}
	}
	return false
}

// hygieneOldSources is kv-cache's committed sources: list.
var hygieneOldSources = []string{"raw/articles/kv-cache-explained.md"}

func hygieneSourcesEqual(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("sources = %q, want %q", got, want)
	}
}

// TestPatchAppendDropsEchoedHeading: append_section whose content opens with
// the section's own heading appends only the body — one "## Related"
// heading, the new link last — and says so on the result's last line.
func TestPatchAppendDropsEchoedHeading(t *testing.T) {
	reg, e := hygieneSetup(t)
	r := hygienePatch(t, reg, "## Related", "append_section", "## Related\n\n- [[b]]")
	if r.IsError {
		t.Fatalf("patch refused: %s", r.Content)
	}
	p := hygieneStaged(t, e, hygienePage)
	if n := hygieneHeadings(p, "## Related"); n != 1 {
		t.Errorf("staged page has %d \"## Related\" headings, want 1:\n%s", n, p.Body)
	}
	if !strings.HasSuffix(strings.TrimRight(p.Body, "\n"), "- [[b]]") {
		t.Errorf("staged body does not end with the appended link:\n%s", p.Body)
	}
	if !strings.Contains(p.Body, "- [[flash-attention]]") {
		t.Errorf("the section's existing links were lost:\n%s", p.Body)
	}
	if got := hygieneLastLine(r.Content); got != hygieneEchoNoteAppend {
		t.Errorf("last line = %q, want %q", got, hygieneEchoNoteAppend)
	}
}

// TestPatchReplaceDropsEchoedHeading: the same for replace_section — the
// section body becomes the content minus its echoed heading.
func TestPatchReplaceDropsEchoedHeading(t *testing.T) {
	reg, e := hygieneSetup(t)
	r := hygienePatch(t, reg, "## Related", "replace_section", "## Related\n\n- [[b]]")
	if r.IsError {
		t.Fatalf("patch refused: %s", r.Content)
	}
	p := hygieneStaged(t, e, hygienePage)
	if n := hygieneHeadings(p, "## Related"); n != 1 {
		t.Errorf("staged page has %d \"## Related\" headings, want 1:\n%s", n, p.Body)
	}
	sec, ok := p.Section("## Related")
	if !ok {
		t.Fatal("staged page lost its ## Related section")
	}
	if got := strings.TrimSpace(p.Body[sec.Body:sec.End]); got != "- [[b]]" {
		t.Errorf("section body = %q, want %q", got, "- [[b]]")
	}
	if got := hygieneLastLine(r.Content); got != hygieneEchoNoteReplace {
		t.Errorf("last line = %q, want %q", got, hygieneEchoNoteReplace)
	}
}

// TestPatchEchoCaseAndSpace: the heading comparison is TrimSpace plus case
// folding, and the marker's inner spacing is free — "##  related " echoes
// "## Related".
func TestPatchEchoCaseAndSpace(t *testing.T) {
	reg, e := hygieneSetup(t)
	r := hygienePatch(t, reg, "## Related", "append_section", "##  related \n- [[b]]")
	if r.IsError {
		t.Fatalf("patch refused: %s", r.Content)
	}
	p := hygieneStaged(t, e, hygienePage)
	for _, s := range p.Sections {
		if strings.EqualFold(s.Title, "related") && s.Heading != "## Related" {
			t.Errorf("the echoed heading %q was appended as a section", s.Heading)
		}
	}
	if n := hygieneHeadings(p, "## Related"); n != 1 {
		t.Errorf("staged page has %d \"## Related\" headings, want 1", n)
	}
	if strings.Contains(p.Body, "##  related") {
		t.Errorf("the echoed line is still in the body:\n%s", p.Body)
	}
	if !strings.HasSuffix(strings.TrimRight(p.Body, "\n"), "- [[b]]") {
		t.Errorf("staged body does not end with the appended link:\n%s", p.Body)
	}
	if got := hygieneLastLine(r.Content); !strings.HasPrefix(got, "note: dropped the leading ") {
		t.Errorf("last line = %q, want the dropped-heading note", got)
	}
}

// TestPatchNoStripOtherHeading: a first line that is a heading of the same
// level but a different text is content, not an echo — it stays and no note
// is added.
func TestPatchNoStripOtherHeading(t *testing.T) {
	reg, e := hygieneSetup(t)
	r := hygienePatch(t, reg, "## Related", "append_section", "## See also\n- [[b]]")
	if r.IsError {
		t.Fatalf("patch refused: %s", r.Content)
	}
	p := hygieneStaged(t, e, hygienePage)
	if n := hygieneHeadings(p, "## See also"); n != 1 {
		t.Errorf("\"## See also\" appears %d times, want 1 (not stripped):\n%s", n, p.Body)
	}
	if hygieneHasNote(r.Content) {
		t.Errorf("a call that stripped nothing carries a note: %q", r.Content)
	}
}

// TestPatchNoStripDeeperLevel: "### Related" under "## Related" is a
// subsection heading, a different level — not an echo.
func TestPatchNoStripDeeperLevel(t *testing.T) {
	reg, e := hygieneSetup(t)
	r := hygienePatch(t, reg, "## Related", "append_section", "### Related\n- [[b]]")
	if r.IsError {
		t.Fatalf("patch refused: %s", r.Content)
	}
	p := hygieneStaged(t, e, hygienePage)
	if n := hygieneHeadings(p, "### Related"); n != 1 {
		t.Errorf("\"### Related\" appears %d times, want 1 (not stripped):\n%s", n, p.Body)
	}
	if n := hygieneHeadings(p, "## Related"); n != 1 {
		t.Errorf("\"## Related\" appears %d times, want 1", n)
	}
	if hygieneHasNote(r.Content) {
		t.Errorf("a call that stripped nothing carries a note: %q", r.Content)
	}
}

// TestPatchInsertAfterUntouched: insert_after's content is a NEW section and
// must carry its heading, so a heading equal to the target's is not an echo
// there — it stays, and no note is added.
func TestPatchInsertAfterUntouched(t *testing.T) {
	reg, e := hygieneSetup(t)
	r := hygienePatch(t, reg, "## Related", "insert_after", "## Related\n- x")
	if r.IsError {
		t.Fatalf("patch refused: %s", r.Content)
	}
	p := hygieneStaged(t, e, hygienePage)
	if n := hygieneHeadings(p, "## Related"); n != 2 {
		t.Errorf("staged page has %d \"## Related\" headings, want 2 (insert_after keeps its heading):\n%s", n, p.Body)
	}
	if hygieneHasNote(r.Content) {
		t.Errorf("insert_after carries a note: %q", r.Content)
	}
}

// TestPatchAddsCitedSource: a patch whose content cites a raw source staged
// in the open changeset leaves the page's sources: as the old list plus
// that source — the model cannot edit frontmatter itself — and the result
// names it. The change is a frontmatter hunk of the op, so Review shows it;
// updated: is not moved, and the committed page is not mutated.
func TestPatchAddsCitedSource(t *testing.T) {
	reg, e := hygieneSetup(t)
	hygieneIngest(t, reg, "new.md")
	r := hygienePatch(t, reg, "## Related", "append_section", "- [[flash-attention]] — eviction policy.^[raw/articles/new.md]")
	if r.IsError {
		t.Fatalf("patch refused: %s", r.Content)
	}
	p := hygieneStaged(t, e, hygienePage)
	hygieneSourcesEqual(t, p.FM.Sources, []string{"raw/articles/kv-cache-explained.md", "raw/articles/new.md"})
	if got := hygieneLastLine(r.Content); got != hygieneNewNote {
		t.Errorf("last line = %q, want %q", got, hygieneNewNote)
	}
	committed, _ := e.Vault().Page(hygienePage)
	if p.FM.Updated != committed.FM.Updated {
		t.Errorf("updated: moved from %s to %s; the sources sync must not touch it", committed.FM.Updated, p.FM.Updated)
	}
	hygieneSourcesEqual(t, committed.FM.Sources, hygieneOldSources)

	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	var op *stage.Op
	for _, o := range cs.Live() {
		if o.Kind == stage.OpPatchPage {
			op = &o
		}
	}
	if op == nil {
		t.Fatal("no patch_page op staged")
	}
	seen := false
	for _, h := range op.Hunks {
		for _, l := range h.Add {
			if strings.HasPrefix(l, "sources:") && strings.Contains(l, "raw/articles/new.md") {
				seen = true
			}
		}
	}
	if !seen {
		t.Errorf("no hunk carries the new sources: line, so Review would not show the frontmatter change:\n%+v", op.Hunks)
	}
}

// TestPatchCitedSourceCommitted: the same, with a source that is already
// committed in the vault rather than staged.
func TestPatchCitedSourceCommitted(t *testing.T) {
	reg, e := hygieneSetup(t)
	r := hygienePatch(t, reg, "## Related", "append_section", "- [[flash-attention]] — speculative sampling.^[raw/papers/leviathan-2023.md]")
	if r.IsError {
		t.Fatalf("patch refused: %s", r.Content)
	}
	p := hygieneStaged(t, e, hygienePage)
	hygieneSourcesEqual(t, p.FM.Sources, []string{"raw/articles/kv-cache-explained.md", "raw/papers/leviathan-2023.md"})
	want := `note: added raw/papers/leviathan-2023.md to sources: (the body cites them).`
	if got := hygieneLastLine(r.Content); got != want {
		t.Errorf("last line = %q, want %q", got, want)
	}
}

// TestPatchUnresolvedCiteNotAdded: a cite that resolves nowhere — not in the
// vault, not staged — is left to the existing checks and lint: sources: is
// unchanged, there is no note, and the patch lands as it does today.
func TestPatchUnresolvedCiteNotAdded(t *testing.T) {
	reg, e := hygieneSetup(t)
	r := hygienePatch(t, reg, "## Related", "append_section", "- [[flash-attention]] — a claim.^[raw/articles/nope.md]")
	if r.IsError {
		t.Fatalf("patch refused: %s", r.Content)
	}
	p := hygieneStaged(t, e, hygienePage)
	hygieneSourcesEqual(t, p.FM.Sources, hygieneOldSources)
	if hygieneHasNote(r.Content) {
		t.Errorf("an unresolved cite produced a note: %q", r.Content)
	}
	if !strings.Contains(p.Body, "^[raw/articles/nope.md]") {
		t.Errorf("the patch's own content was not applied:\n%s", p.Body)
	}
}

// TestCreatePageAddsCitedSource: create_page lists a.md, its body cites
// a.md and the staged b.md — sources: becomes [a, b] and the op's
// Provenance is that final list, not the list the model sent.
func TestCreatePageAddsCitedSource(t *testing.T) {
	reg, e := hygieneSetup(t)
	a := hygieneIngest(t, reg, "a.md")
	b := hygieneIngest(t, reg, "b.md")
	r := hygieneCall(t, reg, "stage.create_page", map[string]any{
		"path": "wiki/concepts/eviction.md", "title": "Eviction", "type": "concept",
		"tags": []string{"inference"}, "sources": []string{a},
		"confidence": "medium", "contested": false,
		"body":      "Eviction drops old entries.^[" + a + "] It trades recall for memory.^[" + b + "] See [[kv-cache]] and [[gpt-4]].",
		"rationale": "test",
	})
	if r.IsError {
		t.Fatalf("create refused: %s", r.Content)
	}
	p := hygieneStaged(t, e, "wiki/concepts/eviction.md")
	hygieneSourcesEqual(t, p.FM.Sources, []string{a, b})
	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	var op *stage.Op
	for _, o := range cs.Live() {
		if o.Kind == stage.OpCreatePage {
			op = &o
		}
	}
	if op == nil {
		t.Fatal("no create_page op staged")
	}
	hygieneSourcesEqual(t, op.Provenance, []string{a, b})
	want := "note: added " + b + " to sources: (the body cites them)."
	if got := hygieneLastLine(r.Content); got != want {
		t.Errorf("last line = %q, want %q", got, want)
	}
}

// TestSourcesNeverRemoved: a patch that deletes the only citation of a
// listed source leaves sources: as it was — the sync only adds.
func TestSourcesNeverRemoved(t *testing.T) {
	reg, e := hygieneSetup(t)
	r := hygieneCall(t, reg, "stage.patch_page", map[string]any{
		"path": hygienePage, "section": "# KV Cache", "op": "replace_text",
		"find": "^[raw/articles/kv-cache-explained.md]", "content": "",
		"rationale": "test",
	})
	if r.IsError {
		t.Fatalf("patch refused: %s", r.Content)
	}
	p := hygieneStaged(t, e, hygienePage)
	if strings.Contains(p.Body, "^[raw/articles/kv-cache-explained.md]") {
		t.Fatalf("the citation was not removed; the test is not exercising removal:\n%s", p.Body)
	}
	hygieneSourcesEqual(t, p.FM.Sources, hygieneOldSources)
	if hygieneHasNote(r.Content) {
		t.Errorf("a removal produced a note: %q", r.Content)
	}
}

// TestPatchEchoGuardRunsOnStrippedContent: the 043 shrink guard measures the
// content AFTER the echoed heading is dropped. The heading line alone (25
// bytes) would not change the verdict on a 3161-byte section, but the refusal
// must count the 16 bytes that would really be written — and a confirmed
// repeat applies, with the note on its result. A refusal itself carries no
// note: nothing was repaired because nothing staged.
func TestPatchEchoGuardRunsOnStrippedContent(t *testing.T) {
	reg, e, _ := tilelangRegistry(t)
	if r := callTool(t, reg, "stage.open", `{"intent":"echo under the shrink guard"}`); r.IsError {
		t.Fatal(r.Content)
	}
	args := map[string]any{
		"path": "wiki/entities/tilelang.md", "section": "## GPU programming model", "op": "replace_section",
		"content": "## GPU programming model\n\nshort paragraph.", "rationale": "test",
	}
	r := hygieneCall(t, reg, "stage.patch_page", args)
	if !r.IsError {
		t.Fatalf("a 16-byte rewrite of a 3161-byte section was accepted:\n%s", r.Content)
	}
	if want := `replace_section "## GPU programming model" keeps 16 of 3161 bytes (0%) of the section; it would delete:`; !strings.HasPrefix(r.Content, want) {
		t.Errorf("refusal = %q, want it to start with %q", r.Content, want)
	}
	if hygieneHasNote(r.Content) {
		t.Errorf("a refusal carries a note: %q", r.Content)
	}
	assertNothingStaged(t, e)

	args["allow_shrink"] = true
	r = hygieneCall(t, reg, "stage.patch_page", args)
	if r.IsError {
		t.Fatalf("confirmed repeat refused: %s", r.Content)
	}
	want := `note: dropped the leading "## GPU programming model" from content; replace_section content is the section body without its heading.`
	if got := hygieneLastLine(r.Content); got != want {
		t.Errorf("last line = %q, want %q", got, want)
	}
}

// TestPatchBothNotes: a call that drops an echo AND adds a source ends with
// both notes, the heading note first — the order the repairs ran in.
func TestPatchBothNotes(t *testing.T) {
	reg, e := hygieneSetup(t)
	hygieneIngest(t, reg, "new.md")
	r := hygienePatch(t, reg, "## Related", "append_section", "## Related\n\n- [[flash-attention]] — eviction.^[raw/articles/new.md]")
	if r.IsError {
		t.Fatalf("patch refused: %s", r.Content)
	}
	lines := strings.Split(r.Content, "\n")
	if len(lines) < 3 || lines[len(lines)-2] != hygieneEchoNoteAppend || lines[len(lines)-1] != hygieneNewNote {
		t.Errorf("result = %q, want the echo note then the sources note as its last two lines", r.Content)
	}
	p := hygieneStaged(t, e, hygienePage)
	hygieneSourcesEqual(t, p.FM.Sources, []string{"raw/articles/kv-cache-explained.md", "raw/articles/new.md"})
}

// TestPatchHygieneLeavesLintClean is the end-to-end claim of 050: the op
// shape measured on the 049 baseline — an echoed "## Related" heading and a
// body cite of a freshly staged source — projects to a vault with neither a
// duplicate-section nor a cite-source finding on the page.
func TestPatchHygieneLeavesLintClean(t *testing.T) {
	reg, e := hygieneSetup(t)
	hygieneIngest(t, reg, "new.md")
	r := hygienePatch(t, reg, "## Related", "append_section", "## Related\n\n- [[flash-attention]] — eviction.^[raw/articles/new.md]")
	if r.IsError {
		t.Fatalf("patch refused: %s", r.Content)
	}
	rep, err := e.ProjectedReport()
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []string{"duplicate-section", "cite-source"} {
		for _, f := range rep.ByCheck[check] {
			if f.Path == hygienePage {
				t.Errorf("%s still fires on %s: %s", check, f.Path, f.Message)
			}
		}
	}
}

// TestStripEchoedHeading pins the matcher itself: what counts as the echo of
// a "## Related" section, and what the stripped content keeps.
func TestStripEchoedHeading(t *testing.T) {
	sec := vault.Section{Level: 2, Heading: "## Related", Title: "Related"}
	tests := []struct {
		name     string
		content  string
		stripped string
		line     string
		ok       bool
	}{
		{"exact", "## Related\n\n- [[b]]", "- [[b]]", "## Related", true},
		{"no blank after", "## Related\n- [[b]]", "- [[b]]", "## Related", true},
		{"many blanks after", "## Related\n\n\n  \n- [[b]]\n", "- [[b]]\n", "## Related", true},
		{"blank lines before", "\n\n## Related\n- [[b]]", "- [[b]]", "## Related", true},
		{"case and space", "##  related \n- [[b]]", "- [[b]]", "##  related", true},
		{"tab after hashes", "##\tRELATED\n- [[b]]", "- [[b]]", "##\tRELATED", true},
		{"three-space indent", "   ## Related\n- [[b]]", "- [[b]]", "## Related", true},
		{"crlf", "## Related\r\n\r\n- [[b]]", "- [[b]]", "## Related", true},
		{"echo only", "## Related", "", "## Related", true},
		{"only the first line", "## Related\n- [[b]]\n\n## Related\n- [[c]]", "- [[b]]\n\n## Related\n- [[c]]", "## Related", true},
		{"other text", "## See also\n- [[b]]", "", "", false},
		{"deeper level", "### Related\n- [[b]]", "", "", false},
		{"shallower level", "# Related\n- [[b]]", "", "", false},
		{"no space after hashes", "##Related\n- [[b]]", "", "", false},
		{"four-space indent is code", "    ## Related\n- [[b]]", "", "", false},
		{"prose first", "Related pages:\n## Related\n- [[b]]", "", "", false},
		{"substring text", "## Related work\n- [[b]]", "", "", false},
		{"seven hashes", "####### Related\n- [[b]]", "", "", false},
		{"empty", "", "", "", false},
		{"blank only", "\n \n", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stripped, line, ok := stripEchoedHeading(sec, tt.content)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if !ok {
				if stripped != tt.content {
					t.Errorf("an unstripped content changed: %q", stripped)
				}
				return
			}
			if stripped != tt.stripped || line != tt.line {
				t.Errorf("got (%q, %q), want (%q, %q)", stripped, line, tt.stripped, tt.line)
			}
		})
	}
}

// TestPatchNoSourcesKeyLeftAlone: a page with no sources: key at all is not
// given one by a patch. Adding the key would stage an insert-only frontmatter
// hunk, which the engine anchors at the end of the patched section — so
// dropping the body hunk in Review would write "sources: [...]" into the body
// (measured while building 050). Such a page keeps its cite-source warn for
// lint to report, exactly as before: no key, no note, no insert-only hunk.
func TestPatchNoSourcesKeyLeftAlone(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	page := "---\ntitle: No Src\ncreated: 2026-08-20\nupdated: 2026-08-29\ntype: concept\ntags: [inference]\nconfidence: high\n---\n\n# No Src\n\nIntro text.\n\n## Related\n\n- [[kv-cache]]\n- [[gpt-4]]\n\n## Tail\n\nTail text.\n"
	if err := os.WriteFile(filepath.Join(dir, "wiki", "concepts", "no-src.md"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := stage.OpenEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	v := e.Vault()
	reg := NewRegistry(Deps{Vault: v, Index: index.Build(v), Engine: e, Author: stage.Author{Kind: "agent", Model: "test"}})
	if r, err := reg.Call(context.Background(), "stage.open", json.RawMessage(`{"intent":"page without sources"}`)); err != nil || r.IsError {
		t.Fatalf("stage.open: %+v %v", r, err)
	}
	r := hygieneCall(t, reg, "stage.patch_page", map[string]any{
		"path": "wiki/concepts/no-src.md", "section": "## Related", "op": "append_section",
		"content": "- [[flash-attention]] — x.^[raw/papers/leviathan-2023.md]", "rationale": "test",
	})
	if r.IsError {
		t.Fatalf("patch refused: %s", r.Content)
	}
	if hygieneHasNote(r.Content) {
		t.Errorf("a page with no sources: key got a note: %q", r.Content)
	}
	p := hygieneStaged(t, e, "wiki/concepts/no-src.md")
	if p.FM.Sources != nil {
		t.Errorf("sources = %q, want the key left absent", p.FM.Sources)
	}
	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range cs.Ops[0].Hunks {
		for _, l := range h.Add {
			if strings.HasPrefix(l, "sources:") {
				t.Errorf("hunk %s adds %q", h.ID, l)
			}
		}
	}
}

// hygieneOnlyOp returns the single live patch_page op of the open changeset.
func hygieneOnlyOp(t *testing.T, e *stage.Engine) stage.Op {
	t.Helper()
	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	var ops []stage.Op
	for _, o := range cs.Live() {
		if o.Kind == stage.OpPatchPage {
			ops = append(ops, o)
		}
	}
	if len(ops) != 1 {
		t.Fatalf("want 1 live patch_page op, have %d", len(ops))
	}
	return ops[0]
}

// hygieneAssertSound fails unless the staged page at hygienePage parses and
// the changeset's lint verdict is not "fail" — the two things a corrupted
// frontmatter breaks first.
func hygieneAssertSound(t *testing.T, e *stage.Engine, step string) []byte {
	t.Helper()
	b, ok, err := e.StagedFile(hygienePage)
	if err != nil || !ok {
		t.Fatalf("%s: StagedFile = ok %v, err %v", step, ok, err)
	}
	if _, err := vault.ParsePage(hygienePage, b); err != nil {
		t.Fatalf("%s: the staged page does not parse: %v\n%s", step, err, b)
	}
	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	if cs.Checks.Lint == "fail" {
		t.Fatalf("%s: Checks.Lint = fail\n%s", step, b)
	}
	return b
}

// hygieneSyncShape stages the review's shape: replace_text on "# KV Cache"
// whose content repeats the find line (the first line of the intro
// paragraph, six lines below the sources: line) and adds a second line that
// cites a freshly staged source. The two changes sit close enough that one
// ComputeHunks over the whole file merges them into a single hunk.
func hygieneSyncShape(t *testing.T) (*stage.Engine, stage.Op) {
	t.Helper()
	reg, e := hygieneSetup(t)
	hygieneIngest(t, reg, "new.md")
	find := "The key/value cache stores per-layer attention projections from previous"
	r := hygieneCall(t, reg, "stage.patch_page", map[string]any{
		"path": hygienePage, "section": "# KV Cache", "op": "replace_text",
		"find": find, "content": find + "\nA second claim from a fresh source.^[raw/articles/new.md]",
		"rationale": "test",
	})
	if r.IsError {
		t.Fatalf("patch refused: %s", r.Content)
	}
	return e, hygieneOnlyOp(t, e)
}

// hygieneHasSourcesLine reports whether h touches the frontmatter's sources:
// line, on either side of the hunk.
func hygieneHasSourcesLine(h stage.Hunk) bool {
	for _, l := range append(append([]string(nil), h.Del...), h.Add...) {
		if strings.HasPrefix(l, "sources:") {
			return true
		}
	}
	return false
}

// TestPatchSyncHunksSurviveDropUndrop: when the sources: sync changes the
// frontmatter, Review's n then y on any hunk of the op keeps the staged page
// intact. 050 S1 diffed the whole file at once, so a body edit within three
// lines of the sources: change merged with it into one hunk (Del the old
// sources: line, Add the new one plus the body line); re-applying that hunk
// pairs its lines, which puts the BODY line after sources: inside the
// frontmatter and leaves a page that does not parse (review M1). The root
// cause is stage's flattened Hunk, which loses the interior context of a
// merged window, so it is fixed there (052, hunks persist ordered diff lines
// and a start position), not by splitting the diff here — and this test
// waits for it.
//
// Each subtest runs the review's shape: replace_text on "# KV Cache" whose
// content repeats the find line (the first line of the intro paragraph, six
// lines below the sources: line) and adds a second line citing a freshly
// staged source.
func TestPatchSyncHunksSurviveDropUndrop(t *testing.T) {
	t.Skip("needs 052 hunk positions (TD-15)")

	t.Run("every_hunk_n_then_y", func(t *testing.T) {
		e, op := hygieneSyncShape(t)
		if len(op.Hunks) == 0 {
			t.Fatal("the patch staged no hunks")
		}
		for _, h := range op.Hunks {
			before := hygieneAssertSound(t, e, "before "+h.ID)
			if err := e.DropHunk(op.ID, h.ID); err != nil {
				t.Fatalf("DropHunk %s: %v", h.ID, err)
			}
			hygieneAssertSound(t, e, "after n on "+h.ID)
			if err := e.UndropHunk(op.ID, h.ID); err != nil {
				t.Fatalf("UndropHunk %s: %v", h.ID, err)
			}
			after := hygieneAssertSound(t, e, "after n then y on "+h.ID)
			if string(after) != string(before) {
				t.Errorf("n then y on %s changed the staged bytes:\n--- before\n%s\n--- after\n%s", h.ID, before, after)
			}
		}
	})

	t.Run("frontmatter_hunk_dropped_alone", func(t *testing.T) {
		e, op := hygieneSyncShape(t)
		var fm []stage.Hunk
		for _, h := range op.Hunks {
			if hygieneHasSourcesLine(h) {
				fm = append(fm, h)
			}
		}
		if len(fm) != 1 {
			t.Fatalf("%d hunks touch sources:, want exactly 1", len(fm))
		}
		for _, l := range append(append([]string(nil), fm[0].Del...), fm[0].Add...) {
			if !strings.HasPrefix(l, "sources:") {
				t.Fatalf("the frontmatter hunk %s also carries a body line %q: dropping it alone is impossible", fm[0].ID, l)
			}
		}
		if err := e.DropHunk(op.ID, fm[0].ID); err != nil {
			t.Fatalf("DropHunk %s: %v", fm[0].ID, err)
		}
		b := hygieneAssertSound(t, e, "after dropping the frontmatter hunk")
		p, err := vault.ParsePage(hygienePage, b)
		if err != nil {
			t.Fatal(err)
		}
		hygieneSourcesEqual(t, p.FM.Sources, hygieneOldSources)
		if !strings.Contains(p.Body, "A second claim from a fresh source.^[raw/articles/new.md]") {
			t.Errorf("the body edit was lost with the frontmatter hunk:\n%s", p.Body)
		}
	})
}

// TestPatchEchoOnlyRefused: content that is nothing but the echoed heading
// leaves an empty body once stripped — an append that adds nothing, or a
// replace_section that would blank the section. Both are refused with the
// frozen text and stage nothing, instead of staging a silent no-op or an
// erase (review L1). remove_section is the op for deleting a section.
func TestPatchEchoOnlyRefused(t *testing.T) {
	want := `stage.patch_page refused: content is only the "## Related" heading; send the section body without its heading, or use op remove_section to delete the section.`
	for _, op := range []string{"append_section", "replace_section"} {
		for _, content := range []string{"## Related", "## Related\n", "\n## Related\n\n  \n"} {
			t.Run(op+"/"+strings.TrimSpace(strings.ReplaceAll(content, "\n", "|")), func(t *testing.T) {
				reg, e := hygieneSetup(t)
				r := hygienePatch(t, reg, "## Related", op, content)
				if !r.IsError {
					t.Fatalf("an echo-only %s was accepted: %q", op, r.Content)
				}
				if r.Content != want {
					t.Errorf("refusal =\n%s\nwant =\n%s", r.Content, want)
				}
				assertNothingStaged(t, e)
			})
		}
	}
}

// TestPatchEchoOnlyBeatsShrink: a bare heading sent to replace_section on a
// section the shrink guard would measure (3161 bytes) gets the echo-only
// refusal, not the shrink refusal — whose "repeat with allow_shrink" advice
// would invite the very erase the model did not mean — whether or not the
// call sets allow_shrink. A section WITH subsections keeps 043's nested-loss
// refusal, pinned by TestPatchReplaceSectionRefusesNestedLoss, which sends
// exactly this bare heading.
func TestPatchEchoOnlyBeatsShrink(t *testing.T) {
	reg, e, _ := tilelangRegistry(t)
	if r := callTool(t, reg, "stage.open", `{"intent":"bare heading under the shrink guard"}`); r.IsError {
		t.Fatal(r.Content)
	}
	want := `stage.patch_page refused: content is only the "## GPU programming model" heading; send the section body without its heading, or use op remove_section to delete the section.`
	for _, flag := range []bool{false, true} {
		r := hygieneCall(t, reg, "stage.patch_page", map[string]any{
			"path": "wiki/entities/tilelang.md", "section": "## GPU programming model", "op": "replace_section",
			"content": "## GPU programming model", "allow_shrink": flag, "rationale": "test",
		})
		if !r.IsError || r.Content != want {
			t.Errorf("allow_shrink=%v: result = %v %q, want the echo-only refusal", flag, r.IsError, r.Content)
		}
		assertNothingStaged(t, e)
	}
}
