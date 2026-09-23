package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// synthPage returns a minimal, parseable wiki page under wiki/concepts,
// carrying two valid outbound links and a marker word for search.
func synthPage(title, marker string) string {
	return fmt.Sprintf(`---
title: %s
created: 2026-08-30
updated: 2026-08-30
type: concept
tags: [inference]
confidence: high
---

# %s

This page exists only to make wiki.search return many hits. Marker: %s.

## Related

- [[kv-cache]]
- [[flash-attention]]
`, title, title, marker)
}

// TestWikiSearchCapsHitsAndSnippets builds a vault with 25 pages sharing a
// marker word and asserts wiki.search never returns more than 20 hits, and
// every snippet stays within the 200-rune cap — end to end through the
// tool, not just at the index layer.
func TestWikiSearchCapsHitsAndSnippets(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")

	const marker = "zzzcapmarker"
	for i := 1; i <= 25; i++ {
		path := filepath.Join(dir, "wiki", "concepts", fmt.Sprintf("synthetic-%02d.md", i))
		content := synthPage(fmt.Sprintf("Synthetic %02d", i), marker)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	reg := NewRegistry(newTestDeps(t, dir))
	res, err := reg.Call(context.Background(), "wiki.search", json.RawMessage(fmt.Sprintf(`{"q": %q}`, marker)))
	if err != nil {
		t.Fatalf("Call(wiki.search) error = %v", err)
	}
	if res.IsError {
		t.Fatalf("Call(wiki.search) IsError; Content: %s", res.Content)
	}

	lines := strings.Split(res.Content, "\n")
	var hitCount int
	for i, line := range lines {
		if !isNumberedHitLine(line) {
			continue
		}
		hitCount++
		if i+1 >= len(lines) {
			t.Fatalf("hit line %q has no following snippet line", line)
		}
		snippet := strings.TrimSpace(lines[i+1])
		if n := utf8.RuneCountInString(snippet); n > 200 {
			t.Errorf("snippet has %d runes, want <= 200: %q", n, snippet)
		}
	}
	if hitCount != 20 {
		t.Fatalf("got %d hits, want the default-capped 20 (25 pages match %q)", hitCount, marker)
	}

	// An explicit lower limit must also be honoured end to end.
	res, err = reg.Call(context.Background(), "wiki.search", json.RawMessage(fmt.Sprintf(`{"q": %q, "limit": 3}`, marker)))
	if err != nil {
		t.Fatalf("Call(wiki.search, limit=3) error = %v", err)
	}
	got := countNumberedHitLines(res.Content)
	if got != 3 {
		t.Fatalf("limit=3 returned %d hits, want 3", got)
	}
}

// wikiGetTruncationNoticeHead / wikiGetTruncationNoticeSection build the
// exact truncation notices 004 T0b F.W1 freezes, so the pins below compare
// full byte-for-byte expected results rather than fragile substrings.
func wikiGetTruncationNoticeHead(resolved string, n int, headings []string) string {
	return fmt.Sprintf(
		"\n\n[truncated: %s is %d runes; showing the first 16000. Read the rest by section: %s]",
		resolved, n, strings.Join(headings, ", "),
	)
}

func wikiGetTruncationNoticeSection(heading, resolved string, n int) string {
	return fmt.Sprintf(
		"\n\n[truncated: section %q of %s is %d runes; showing the first 16000.]",
		heading, resolved, n,
	)
}

// firstNRunes cuts s to at most n runes — the rune-safe cut the cap promises.
func firstNRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// writePaddedPage writes wiki/concepts/<name>.md whose canonical
// serialization is exactly target runes long, by parsing a draft and
// padding its body with 'x' runes before the trailing newline. It returns
// the re-parsed page so a test can build the exact expected wiki.get body.
func writePaddedPage(t *testing.T, dir, name string, target int) *vault.Page {
	t.Helper()
	rel := "wiki/concepts/" + name + ".md"
	content := fmt.Sprintf(`---
title: %s
created: 2026-08-30
updated: 2026-08-30
type: concept
tags: [inference]
confidence: high
---

# %s

pad me

## One

one

## Two

two
`, name, name)
	p, err := vault.ParsePage(rel, []byte(content))
	if err != nil {
		t.Fatalf("ParsePage draft: %v", err)
	}
	diff := target - utf8.RuneCountInString(string(p.Serialize()))
	if diff < 0 {
		t.Fatalf("draft already exceeds target: %d > %d runes", target-diff, target)
	}
	body := p.Body[:len(p.Body)-1] + strings.Repeat("x", diff) + "\n"
	content = string(p.FM.Encode()) + "\n" + body
	if err := os.WriteFile(filepath.Join(dir, "wiki", "concepts", name+".md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	p2, err := vault.ParsePage(rel, []byte(content))
	if err != nil {
		t.Fatalf("ParsePage padded: %v", err)
	}
	if got := utf8.RuneCountInString(string(p2.Serialize())); got != target {
		t.Fatalf("padded page is %d runes, want exactly %d", got, target)
	}
	return p2
}

// TestWikiGetExactly16000RunesUntouched pins F.W2: a page whose whole
// serialization is exactly the cap comes back byte-identical, with no
// truncation notice.
func TestWikiGetExactly16000RunesUntouched(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	p := writePaddedPage(t, dir, "padded-exact", 16000)
	reg := NewRegistry(newTestDeps(t, dir))

	res, err := reg.Call(context.Background(), "wiki.get", json.RawMessage(`{"page": "padded-exact"}`))
	if err != nil || res.IsError {
		t.Fatalf("Call(wiki.get) = %+v, err = %v", res, err)
	}
	if want := string(p.Serialize()); res.Content != want {
		t.Fatalf("exactly-16000-rune page was not byte-identical (got %d runes, want %d, notice present: %v)",
			utf8.RuneCountInString(res.Content), utf8.RuneCountInString(want), strings.Contains(res.Content, "[truncated"))
	}
	if strings.Contains(res.Content, "[truncated") {
		t.Fatal("exactly-16000-rune page must not carry a truncation notice")
	}
}

// TestWikiGetTruncatesWholePageAt16001 pins F.W1's whole-page leg: one rune
// over the cap yields the first 16000 runes plus the full notice, naming
// the resolved path, the page's rune count and sectionHeadings(p).
func TestWikiGetTruncatesWholePageAt16001(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	p := writePaddedPage(t, dir, "padded-over", 16001)
	reg := NewRegistry(newTestDeps(t, dir))

	res, err := reg.Call(context.Background(), "wiki.get", json.RawMessage(`{"page": "padded-over"}`))
	if err != nil || res.IsError {
		t.Fatalf("Call(wiki.get) = %+v, err = %v", res, err)
	}
	full := string(p.Serialize())
	want := firstNRunes(full, 16000) + wikiGetTruncationNoticeHead(p.Path, utf8.RuneCountInString(full), sectionHeadings(p))
	if res.Content != want {
		t.Fatalf("truncated whole page = %d runes, want byte-exact %d-rune prefix + notice",
			utf8.RuneCountInString(res.Content), utf8.RuneCountInString(want))
	}
}

// TestWikiGetTruncationCutIsRuneSafe pins the multibyte leg: a page of
// é/量 pairs cut at 16000 runes stays valid UTF-8 and equals the rune-wise
// prefix — a byte-offset cut would corrupt an in-progress rune.
func TestWikiGetTruncationCutIsRuneSafe(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	rel := "wiki/concepts/multibyte-big.md"
	body := strings.Repeat("é量", 9000) // 18000 runes, 36000 bytes
	content := fmt.Sprintf("---\ntitle: Multibyte\ncreated: 2026-08-30\nupdated: 2026-08-30\ntype: concept\ntags: [inference]\nconfidence: high\n---\n\n# Multibyte\n\n%s\n", body)
	if err := os.WriteFile(filepath.Join(dir, "wiki", "concepts", "multibyte-big.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	p, err := vault.ParsePage(rel, []byte(content))
	if err != nil {
		t.Fatalf("ParsePage: %v", err)
	}
	reg := NewRegistry(newTestDeps(t, dir))

	res, err := reg.Call(context.Background(), "wiki.get", json.RawMessage(`{"page": "multibyte-big"}`))
	if err != nil || res.IsError {
		t.Fatalf("Call(wiki.get) = %+v, err = %v", res, err)
	}
	if !utf8.ValidString(res.Content) {
		t.Fatal("truncated result is not valid UTF-8 — the cap split a rune")
	}
	full := string(p.Serialize())
	want := firstNRunes(full, 16000) + wikiGetTruncationNoticeHead(p.Path, utf8.RuneCountInString(full), sectionHeadings(p))
	if res.Content != want {
		t.Fatalf("multibyte cut diverged from the rune-wise prefix")
	}
}

// TestWikiGetTruncatesOversizedSection pins F.W1's section leg: a section
// body over the cap returns its first 16000 runes plus the section notice,
// naming the heading, the resolved path and the section's own rune count.
func TestWikiGetTruncatesOversizedSection(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	rel := "wiki/concepts/big-section.md"
	big := strings.Repeat("a", 20000)
	content := fmt.Sprintf("---\ntitle: Big Section\ncreated: 2026-08-30\nupdated: 2026-08-30\ntype: concept\ntags: [inference]\nconfidence: high\n---\n\n# Big Section\n\n## Big\n\n%s\n\n## Small\n\ntiny\n", big)
	if err := os.WriteFile(filepath.Join(dir, "wiki", "concepts", "big-section.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	p, err := vault.ParsePage(rel, []byte(content))
	if err != nil {
		t.Fatalf("ParsePage: %v", err)
	}
	sec, ok := p.Section("## Big")
	if !ok {
		t.Fatal("fixture page lost its ## Big section")
	}
	reg := NewRegistry(newTestDeps(t, dir))

	res, err := reg.Call(context.Background(), "wiki.get", json.RawMessage(`{"page": "big-section", "section": "## Big"}`))
	if err != nil || res.IsError {
		t.Fatalf("Call(wiki.get, section) = %+v, err = %v", res, err)
	}
	body := p.Body[sec.Start:sec.End]
	want := firstNRunes(body, 16000) + wikiGetTruncationNoticeSection("## Big", p.Path, utf8.RuneCountInString(body))
	if res.Content != want {
		t.Fatalf("truncated section = %d runes, want byte-exact %d-rune prefix + notice",
			utf8.RuneCountInString(res.Content), utf8.RuneCountInString(want))
	}
}

// TestWikiGetStagedTruncationKeepsMarkerPrefix pins F.W1's staged leg: the
// staged-source marker stays a prefix of a truncated result and does not
// count toward the cap — the first 16000 runes after the marker are all
// page.
func TestWikiGetStagedTruncationKeepsMarkerPrefix(t *testing.T) {
	reg, e, _ := engineRegistry(t, nil)
	if r := callTool(t, reg, "stage.open", `{"intent":"grow a section past the wiki.get cap"}`); r.IsError {
		t.Fatal(r.Content)
	}
	big := strings.Repeat("量é", 12000) // 24000 runes
	if r := callTool(t, reg, "stage.patch_page", fmt.Sprintf(
		`{"path":"wiki/concepts/kv-cache.md","section":"## Why it matters","op":"replace_section","content":%q,"rationale":"oversize the section"}`, big,
	)); r.IsError {
		t.Fatalf("patch: %s", r.Content)
	}
	b, has, err := e.StagedFile("wiki/concepts/kv-cache.md")
	if err != nil || !has {
		t.Fatalf("StagedFile has = %v, err = %v", has, err)
	}
	p, err := vault.ParsePage("wiki/concepts/kv-cache.md", b)
	if err != nil {
		t.Fatalf("ParsePage staged: %v", err)
	}

	res := callTool(t, reg, "wiki.get", `{"page":"kv-cache"}`)
	if res.IsError {
		t.Fatalf("staged wiki.get: %s", res.Content)
	}
	if !strings.HasPrefix(res.Content, stagedSourceMarker) {
		t.Fatalf("truncated staged result lost the marker prefix:\n%.120s", res.Content)
	}
	rest := strings.TrimPrefix(res.Content, stagedSourceMarker)
	full := string(p.Serialize())
	want := firstNRunes(full, 16000) + wikiGetTruncationNoticeHead(p.Path, utf8.RuneCountInString(full), sectionHeadings(p))
	if rest != want {
		t.Fatalf("staged truncated body = %d runes, want byte-exact %d-rune prefix + notice (marker must not count toward the cap)",
			utf8.RuneCountInString(rest), utf8.RuneCountInString(want))
	}
}

// TestWikiGetToolDescriptionMentionsTruncation pins F.W3: the description
// carries the one-sentence truncation disclosure so a model reading long
// pages knows to go by section before it hits the cap.
func TestWikiGetToolDescriptionMentionsTruncation(t *testing.T) {
	d := wikiGetTool(Deps{})
	if !strings.Contains(d.Description, "Results over 16000 characters are truncated; read long pages by section.") {
		t.Fatalf("wiki.get description missing the F.W3 sentence: %s", d.Description)
	}
}

func isNumberedHitLine(line string) bool {
	return len(line) > 2 && line[0] >= '1' && line[0] <= '9' && strings.Contains(line, ". ")
}

func countNumberedHitLines(content string) int {
	n := 0
	for _, line := range strings.Split(content, "\n") {
		if isNumberedHitLine(line) {
			n++
		}
	}
	return n
}

func TestWikiSearchNoHits(t *testing.T) {
	reg := minimalRegistry(t)
	res, err := reg.Call(context.Background(), "wiki.search", json.RawMessage(`{"q": "nonexistentgibberishterm"}`))
	if err != nil {
		t.Fatalf("Call error = %v", err)
	}
	if res.IsError {
		t.Fatalf("zero hits must not be IsError; Content: %s", res.Content)
	}
	if res.Content == "" {
		t.Fatal("zero-hit result must still explain there were no matches")
	}
}

func TestWikiGetWholePageAndSection(t *testing.T) {
	reg := minimalRegistry(t)
	ctx := context.Background()

	whole, err := reg.Call(ctx, "wiki.get", json.RawMessage(`{"page": "kv-cache"}`))
	if err != nil || whole.IsError {
		t.Fatalf("Call(wiki.get) = %+v, err = %v", whole, err)
	}
	if !strings.Contains(whole.Content, "title: KV Cache") {
		t.Errorf("whole-page Content missing frontmatter: %s", whole.Content)
	}
	if !strings.Contains(whole.Content, "## Related") {
		t.Errorf("whole-page Content missing body: %s", whole.Content)
	}

	section, err := reg.Call(ctx, "wiki.get", json.RawMessage(`{"page": "kv-cache", "section": "## Related"}`))
	if err != nil || section.IsError {
		t.Fatalf("Call(wiki.get, section) = %+v, err = %v", section, err)
	}
	if !strings.HasPrefix(section.Content, "## Related") {
		t.Errorf("section Content = %q, want it to start with the heading", section.Content)
	}
	if strings.Contains(section.Content, "## Example") {
		t.Errorf("section Content leaked a different section: %q", section.Content)
	}

	badSection, err := reg.Call(ctx, "wiki.get", json.RawMessage(`{"page": "kv-cache", "section": "## Nope"}`))
	if err != nil {
		t.Fatalf("Call error = %v", err)
	}
	if !badSection.IsError {
		t.Fatal("unknown section must be IsError")
	}
}

func TestWikiGetResolvesByBareName(t *testing.T) {
	reg := minimalRegistry(t)
	// "kv-cache" (no path, no .md) must resolve to wiki/concepts/kv-cache.md
	// via vault.Resolve, the same as a fully-qualified path.
	byName, err := reg.Call(context.Background(), "wiki.get", json.RawMessage(`{"page": "kv-cache"}`))
	if err != nil || byName.IsError {
		t.Fatalf("Call(wiki.get, bare name) = %+v, err = %v", byName, err)
	}
	byPath, err := reg.Call(context.Background(), "wiki.get", json.RawMessage(`{"page": "wiki/concepts/kv-cache.md"}`))
	if err != nil || byPath.IsError {
		t.Fatalf("Call(wiki.get, full path) = %+v, err = %v", byPath, err)
	}
	if byName.Content != byPath.Content {
		t.Errorf("bare-name and full-path lookups returned different content")
	}
}

func TestWikiNeighborsClampsDepth(t *testing.T) {
	reg := minimalRegistry(t)
	ctx := context.Background()

	depth0, err := reg.Call(ctx, "wiki.neighbors", json.RawMessage(`{"page": "kv-cache", "depth": 0}`))
	if err != nil || depth0.IsError {
		t.Fatalf("depth=0 call failed: %+v, err=%v", depth0, err)
	}
	depth1, err := reg.Call(ctx, "wiki.neighbors", json.RawMessage(`{"page": "kv-cache", "depth": 1}`))
	if err != nil || depth1.IsError {
		t.Fatalf("depth=1 call failed: %+v, err=%v", depth1, err)
	}
	if depth0.Content != depth1.Content {
		t.Errorf("depth 0 was not clamped to 1: %q vs %q", depth0.Content, depth1.Content)
	}

	depth9, err := reg.Call(ctx, "wiki.neighbors", json.RawMessage(`{"page": "kv-cache", "depth": 9}`))
	if err != nil || depth9.IsError {
		t.Fatalf("depth=9 call failed: %+v, err=%v", depth9, err)
	}
	depth2, err := reg.Call(ctx, "wiki.neighbors", json.RawMessage(`{"page": "kv-cache", "depth": 2}`))
	if err != nil || depth2.IsError {
		t.Fatalf("depth=2 call failed: %+v, err=%v", depth2, err)
	}
	if depth9.Content != depth2.Content {
		t.Errorf("depth 9 was not clamped to 2: %q vs %q", depth9.Content, depth2.Content)
	}
}

func TestWikiBacklinks(t *testing.T) {
	reg := minimalRegistry(t)
	res, err := reg.Call(context.Background(), "wiki.backlinks", json.RawMessage(`{"page": "flash-attention"}`))
	if err != nil || res.IsError {
		t.Fatalf("Call(wiki.backlinks) = %+v, err = %v", res, err)
	}
	if !strings.Contains(res.Content, "wiki/concepts/kv-cache.md") {
		t.Errorf("backlinks Content missing the known linking page kv-cache.md: %s", res.Content)
	}
}

func TestWikiLintOnCleanFixtureAndUnknownCheck(t *testing.T) {
	reg := minimalRegistry(t)
	ctx := context.Background()

	clean, err := reg.Call(ctx, "wiki.lint", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Call(wiki.lint) error = %v", err)
	}
	if clean.IsError {
		t.Fatalf("Call(wiki.lint) on the minimal fixture should not itself be IsError; Content: %s", clean.Content)
	}
	if clean.Content == "" {
		t.Fatal("wiki.lint returned empty Content")
	}

	scoped, err := reg.Call(ctx, "wiki.lint", json.RawMessage(`{"checks": ["link-broken"]}`))
	if err != nil || scoped.IsError {
		t.Fatalf("Call(wiki.lint, checks) = %+v, err = %v", scoped, err)
	}

	bad, err := reg.Call(ctx, "wiki.lint", json.RawMessage(`{"checks": ["not-a-real-check-id"]}`))
	if err != nil {
		t.Fatalf("Call error = %v", err)
	}
	if !bad.IsError {
		t.Fatal("an unknown check id must be IsError")
	}
	if !strings.Contains(bad.Content, "not-a-real-check-id") {
		t.Errorf("IsError Content should name the offending check id: %s", bad.Content)
	}
}

func TestClampDepth(t *testing.T) {
	cases := map[int]int{-5: 1, 0: 1, 1: 1, 2: 2, 3: 2, 100: 2}
	for in, want := range cases {
		if got := clampDepth(in); got != want {
			t.Errorf("clampDepth(%d) = %d, want %d", in, got, want)
		}
	}
}
