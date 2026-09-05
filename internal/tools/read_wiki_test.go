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
