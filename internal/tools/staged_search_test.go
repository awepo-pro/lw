package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/trace"
	"github.com/awepo-pro/lw/internal/vault"
)

// TestWikiSearchNoChangesetByteIdentical: no open changeset → output == the
// pre-067 formatting of d.Index.Search for 3 queries (golden built inside the
// test from the index directly).
func TestWikiSearchNoChangesetByteIdentical(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	v, err := vault.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ix := index.Build(v)
	deps := Deps{Vault: v, Index: ix}
	reg := NewRegistry(deps)

	formatExpected := func(hits []index.Hit, q string) string {
		if len(hits) == 0 {
			return fmt.Sprintf("no results for %q", q)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d result(s) for %q:\n", len(hits), q)
		for i, h := range hits {
			fmt.Fprintf(&b, "%d. %s — %s\n   %s\n", i+1, h.Path, h.Title, h.Snippet)
		}
		return strings.TrimRight(b.String(), "\n")
	}

	for _, q := range []string{"cache", "attention", "nonexistentquery"} {
		res, err := reg.Call(context.Background(), "wiki.search", json.RawMessage(fmt.Sprintf(`{"q":%q}`, q)))
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		want := formatExpected(ix.Search(q, index.Options{}), q)
		if res.Content != want {
			t.Errorf("search %q:\ngot : %q\nwant: %q", q, res.Content, want)
		}
	}
}

// TestWikiSearchFindsStagedOnlyPage: committed vault without HTB pages; open
// changeset with create_page wiki/entities/hack-the-box.md (title Hack The Box,
// body mentions HTB and penetration testing). Queries hack the box, HTB,
// penetration each return that path as hit 1, followed by exactly
//
//	(staged in <id>, unreviewed). Red on base (no results).
func TestWikiSearchFindsStagedOnlyPage(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	e, err := stage.OpenEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	author := stage.Author{Kind: "agent", Model: "test"}
	reg := NewRegistry(Deps{Vault: e.Vault(), Index: e.Index(), Engine: e, Author: author, Extract: extract.NewFile()})

	cs, err := e.OpenChangeset("htb test", author)
	if err != nil {
		t.Fatal(err)
	}

	body := "# Hack The Box\n\nHTB is an online platform for cybersecurity and penetration testing training.\n\n## Related\n\n- [[kv-cache]]\n- [[flash-attention]]\n"
	args := map[string]any{
		"path":       "wiki/entities/hack-the-box.md",
		"title":      "Hack The Box",
		"type":       "entity",
		"tags":       []string{"inference"},
		"sources":    []string{"raw/articles/kv-cache-explained.md"},
		"confidence": "high",
		"contested":  false,
		"body":       body,
		"rationale":  "add htb entity",
	}
	argBytes, _ := json.Marshal(args)
	if res, err := reg.Call(context.Background(), "stage.create_page", argBytes); err != nil || res.IsError {
		t.Fatalf("stage.create_page: %v, content=%s", err, res.Content)
	}

	askCtx := trace.WithVerb(context.Background(), trace.VerbAsk)
	wantTag := fmt.Sprintf("   (staged in %s, unreviewed)", cs.ID)
	for _, q := range []string{"hack the box", "HTB", "penetration"} {
		res, err := reg.Call(askCtx, "wiki.search", json.RawMessage(fmt.Sprintf(`{"q":%q}`, q)))
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		lines := strings.Split(res.Content, "\n")
		if len(lines) < 3 {
			t.Fatalf("search %q returned too few lines:\n%s", q, res.Content)
		}
		if !strings.HasPrefix(lines[1], "1. wiki/entities/hack-the-box.md — Hack The Box") {
			t.Errorf("search %q: hit 1 = %q, want path wiki/entities/hack-the-box.md", q, lines[1])
		}
		foundTag := false
		for _, l := range lines {
			if l == wantTag {
				foundTag = true
				break
			}
		}
		if !foundTag {
			t.Errorf("search %q: missing tag %q in output:\n%s", q, wantTag, res.Content)
		}
	}
}

// TestWikiSearchStagedPatchBody: patch_page on a committed page adds RTT timeout
// text → query RTT hits it with the tag line; a committed page that also matches
// has no tag line.
func TestWikiSearchStagedPatchBody(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")

	// Add a committed page that also mentions RTT
	committedPath := filepath.Join(dir, "wiki", "concepts", "committed-rtt.md")
	committedContent := `---
title: Committed RTT
created: 2026-08-30
updated: 2026-08-30
type: concept
tags: [inference]
confidence: high
---

# Committed RTT

This committed page explains network RTT measurement in detail.

## Related

- [[kv-cache]]
- [[flash-attention]]
`
	if err := os.WriteFile(committedPath, []byte(committedContent), 0o644); err != nil {
		t.Fatal(err)
	}

	e, err := stage.OpenEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	author := stage.Author{Kind: "agent", Model: "test"}
	reg := NewRegistry(Deps{Vault: e.Vault(), Index: e.Index(), Engine: e, Author: author, Extract: extract.NewFile()})

	cs, err := e.OpenChangeset("patch rtt test", author)
	if err != nil {
		t.Fatal(err)
	}

	// Patch kv-cache.md to include "RTT timeout" text
	patchArgs := map[string]any{
		"path":      "wiki/concepts/kv-cache.md",
		"op":        "replace_section",
		"section":   "## Why it matters",
		"content":   "Without caching, generating token n would repeat O(n) work. Caching manages RTT timeout mechanisms.",
		"rationale": "mention RTT timeout",
	}
	pBytes, _ := json.Marshal(patchArgs)
	if res, err := reg.Call(context.Background(), "stage.patch_page", pBytes); err != nil || res.IsError {
		t.Fatalf("stage.patch_page: %v, content=%s", err, res.Content)
	}

	askCtx := trace.WithVerb(context.Background(), trace.VerbAsk)
	res, err := reg.Call(askCtx, "wiki.search", json.RawMessage(`{"q":"RTT"}`))
	if err != nil {
		t.Fatalf("search RTT: %v", err)
	}

	stagedTag := fmt.Sprintf("   (staged in %s, unreviewed)", cs.ID)
	if !strings.Contains(res.Content, "wiki/concepts/kv-cache.md") {
		t.Fatalf("search RTT did not hit patched kv-cache.md:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "wiki/concepts/committed-rtt.md") {
		t.Fatalf("search RTT did not hit committed-rtt.md:\n%s", res.Content)
	}

	// Check line-by-line: kv-cache hit has stagedTag, committed-rtt hit does not
	lines := strings.Split(res.Content, "\n")
	for i, l := range lines {
		if strings.Contains(l, "wiki/concepts/kv-cache.md") {
			if i+2 >= len(lines) || lines[i+2] != stagedTag {
				t.Errorf("kv-cache hit missing tag line:\n%s", res.Content)
			}
		}
		if strings.Contains(l, "wiki/concepts/committed-rtt.md") {
			if i+2 < len(lines) && lines[i+2] == stagedTag {
				t.Errorf("committed-rtt hit unexpectedly has staged tag line:\n%s", res.Content)
			}
		}
	}
}

// TestWikiSearchDroppedOpInvisible: the create op dropped (Engine.DropOp) →
// the page is not a hit; undrop → hit + tag again.
func TestWikiSearchDroppedOpInvisible(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	e, err := stage.OpenEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	author := stage.Author{Kind: "agent", Model: "test"}
	reg := NewRegistry(Deps{Vault: e.Vault(), Index: e.Index(), Engine: e, Author: author, Extract: extract.NewFile()})

	cs, err := e.OpenChangeset("drop test", author)
	if err != nil {
		t.Fatal(err)
	}

	body := "# Dropped Page\n\nUniqueKeywordForDropOp is only in this page.\n\n## Related\n\n- [[kv-cache]]\n- [[flash-attention]]\n"
	args := map[string]any{
		"path":       "wiki/concepts/dropped-page.md",
		"title":      "Dropped Page",
		"type":       "concept",
		"tags":       []string{"inference"},
		"sources":    []string{"raw/articles/kv-cache-explained.md"},
		"confidence": "high",
		"contested":  false,
		"body":       body,
		"rationale":  "add page to drop",
	}
	argBytes, _ := json.Marshal(args)
	if res, err := reg.Call(context.Background(), "stage.create_page", argBytes); err != nil || res.IsError {
		t.Fatalf("stage.create_page: %v, content=%s", err, res.Content)
	}

	askCtx := trace.WithVerb(context.Background(), trace.VerbAsk)

	// 1. Page is a hit before drop
	res1, err := reg.Call(askCtx, "wiki.search", json.RawMessage(`{"q":"UniqueKeywordForDropOp"}`))
	if err != nil || !strings.Contains(res1.Content, "wiki/concepts/dropped-page.md") {
		t.Fatalf("search before drop did not find page: %v, content=%s", err, res1.Content)
	}

	// 2. Drop the op
	csCur, err := e.Current()
	if err != nil || len(csCur.Live()) == 0 {
		t.Fatalf("Current cs has no live ops")
	}
	opID := csCur.Live()[0].ID
	if err := e.DropOp(opID); err != nil {
		t.Fatalf("DropOp: %v", err)
	}

	// 3. Page is not a hit after drop
	res2, err := reg.Call(askCtx, "wiki.search", json.RawMessage(`{"q":"UniqueKeywordForDropOp"}`))
	if err != nil {
		t.Fatalf("search after drop: %v", err)
	}
	if strings.Contains(res2.Content, "wiki/concepts/dropped-page.md") {
		t.Fatalf("dropped page still appeared in search results:\n%s", res2.Content)
	}

	// 4. Undrop (restore) the op
	if err := e.RestoreOps([]string{opID}); err != nil {
		t.Fatalf("RestoreOps: %v", err)
	}

	// 5. Page is hit + tag again
	res3, err := reg.Call(askCtx, "wiki.search", json.RawMessage(`{"q":"UniqueKeywordForDropOp"}`))
	if err != nil || !strings.Contains(res3.Content, "wiki/concepts/dropped-page.md") {
		t.Fatalf("search after restore did not find page: %v, content=%s", err, res3.Content)
	}
	wantTag := fmt.Sprintf("   (staged in %s, unreviewed)", cs.ID)
	if !strings.Contains(res3.Content, wantTag) {
		t.Fatalf("search after restore missing tag %q:\n%s", wantTag, res3.Content)
	}
}

// TestProjectedSearchCache: 5 searches at one key → 1 projection build (count
// via an unexported test hook); Append → key changes → rebuilt once; commit →
// OpenChangesetKey()=="" and results == committed index.
func TestProjectedSearchCache(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	e, err := stage.OpenEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	author := stage.Author{Kind: "agent", Model: "test"}
	reg := NewRegistry(Deps{Vault: e.Vault(), Index: e.Index(), Engine: e, Author: author, Extract: extract.NewFile()})

	_, err = e.OpenChangeset("cache test", author)
	if err != nil {
		t.Fatal(err)
	}

	// Stage a page
	body := "# Cache Target\n\nSearchCacheKeyword is in this page.\n\n## Related\n\n- [[kv-cache]]\n- [[flash-attention]]\n"
	args := map[string]any{
		"path":       "wiki/concepts/cache-target.md",
		"title":      "Cache Target",
		"type":       "concept",
		"tags":       []string{"inference"},
		"sources":    []string{"raw/articles/kv-cache-explained.md"},
		"confidence": "high",
		"contested":  false,
		"body":       body,
		"rationale":  "cache test",
	}
	argBytes, _ := json.Marshal(args)
	if res, err := reg.Call(context.Background(), "stage.create_page", argBytes); err != nil || res.IsError {
		t.Fatalf("stage.create_page: %v, content=%s", err, res.Content)
	}

	buildCount := 0
	testProjectedBuildHook = func() {
		buildCount++
	}
	defer func() { testProjectedBuildHook = nil }()

	askCtx := trace.WithVerb(context.Background(), trace.VerbAsk)

	// 5 searches at one key → 1 projection build
	for i := 0; i < 5; i++ {
		_, err := reg.Call(askCtx, "wiki.search", json.RawMessage(`{"q":"SearchCacheKeyword"}`))
		if err != nil {
			t.Fatalf("search %d: %v", i+1, err)
		}
	}
	if buildCount != 1 {
		t.Fatalf("5 searches triggered %d builds, want exactly 1", buildCount)
	}

	// Append → key changes → rebuilt once
	body2 := "# Second Target\n\nSecondCacheKeyword is in this second page.\n\n## Related\n\n- [[kv-cache]]\n- [[flash-attention]]\n"
	args2 := map[string]any{
		"path":       "wiki/concepts/second-target.md",
		"title":      "Second Target",
		"type":       "concept",
		"tags":       []string{"inference"},
		"sources":    []string{"raw/articles/kv-cache-explained.md"},
		"confidence": "high",
		"contested":  false,
		"body":       body2,
		"rationale":  "second op",
	}
	arg2Bytes, _ := json.Marshal(args2)
	if res, err := reg.Call(context.Background(), "stage.create_page", arg2Bytes); err != nil || res.IsError {
		t.Fatalf("stage.create_page 2: %v, content=%s", err, res.Content)
	}

	_, err = reg.Call(askCtx, "wiki.search", json.RawMessage(`{"q":"SecondCacheKeyword"}`))
	if err != nil {
		t.Fatal(err)
	}
	if buildCount != 2 {
		t.Fatalf("after append, search triggered build count = %d, want 2", buildCount)
	}

	// Commit → OpenChangesetKey() == "" and results == committed index
	if _, err := e.Commit("commit ops"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if key := reg.OpenChangesetKey(); key != "" {
		t.Fatalf("after commit OpenChangesetKey() = %q, want empty string", key)
	}

	resCommitted, err := reg.Call(askCtx, "wiki.search", json.RawMessage(`{"q":"SearchCacheKeyword"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resCommitted.Content, "wiki/concepts/cache-target.md") {
		t.Fatalf("search after commit missing committed hit:\n%s", resCommitted.Content)
	}
	if strings.Contains(resCommitted.Content, "(staged in") {
		t.Fatalf("search after commit unexpectedly contains staged tag:\n%s", resCommitted.Content)
	}
}

// TestOpenChangesetKey: "" with none open; stable across calls; changes on
// Append, DropOp, hunk drop, undrop; changes when the committed vault is
// reloaded (the identity S1 picked).
func TestOpenChangesetKey(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	e, err := stage.OpenEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	author := stage.Author{Kind: "agent", Model: "test"}
	reg := NewRegistry(Deps{Vault: e.Vault(), Index: e.Index(), Engine: e, Author: author, Extract: extract.NewFile()})

	// 1. "" with none open
	if key := reg.OpenChangesetKey(); key != "" {
		t.Fatalf("OpenChangesetKey() with none open = %q, want empty string", key)
	}

	// 2. Open changeset
	cs, err := e.OpenChangeset("key transitions", author)
	if err != nil {
		t.Fatal(err)
	}
	kOpen := reg.OpenChangesetKey()
	if kOpen == "" {
		t.Fatal("OpenChangesetKey() is empty with changeset open")
	}

	// 3. Stable across calls
	if k2 := reg.OpenChangesetKey(); k2 != kOpen {
		t.Fatalf("OpenChangesetKey() unstable: %q != %q", k2, kOpen)
	}

	// 4. Changes on Append
	body := "# Page One\n\nText for page one.\n\n## Related\n\n- [[kv-cache]]\n- [[flash-attention]]\n"
	args := map[string]any{
		"path":       "wiki/concepts/p1.md",
		"title":      "P1",
		"type":       "concept",
		"tags":       []string{"inference"},
		"sources":    []string{"raw/articles/kv-cache-explained.md"},
		"confidence": "high",
		"contested":  false,
		"body":       body,
		"rationale":  "p1",
	}
	argBytes, _ := json.Marshal(args)
	if res, err := reg.Call(context.Background(), "stage.create_page", argBytes); err != nil || res.IsError {
		t.Fatalf("stage.create_page: %v, content=%s", err, res.Content)
	}
	kAppend := reg.OpenChangesetKey()
	if kAppend == kOpen {
		t.Fatal("OpenChangesetKey() did not change on Append")
	}

	// Also append a patch page so we have hunks to drop/undrop
	patchArgs := map[string]any{
		"path":      "wiki/concepts/kv-cache.md",
		"op":        "replace_section",
		"section":   "## Why it matters",
		"content":   "Patched why it matters text.",
		"rationale": "patch why it matters",
	}
	patchBytes, _ := json.Marshal(patchArgs)
	if res, err := reg.Call(context.Background(), "stage.patch_page", patchBytes); err != nil || res.IsError {
		t.Fatalf("stage.patch_page: %v, content=%s", err, res.Content)
	}
	kPatch := reg.OpenChangesetKey()
	if kPatch == kAppend {
		t.Fatal("OpenChangesetKey() did not change on second Append")
	}

	// 5. Changes on DropOp
	csCur, err := e.Current()
	if err != nil || len(csCur.Live()) < 2 {
		t.Fatalf("want at least 2 live ops")
	}
	opP1 := csCur.Live()[0]
	opPatch := csCur.Live()[1]
	if err := e.DropOp(opP1.ID); err != nil {
		t.Fatalf("DropOp: %v", err)
	}
	kDropOp := reg.OpenChangesetKey()
	if kDropOp == kPatch {
		t.Fatal("OpenChangesetKey() did not change on DropOp")
	}

	// 6. Changes on hunk drop
	if len(opPatch.Hunks) == 0 {
		t.Fatal("patch op has no hunks")
	}
	hunkID := opPatch.Hunks[0].ID
	if err := e.DropHunk(opPatch.ID, hunkID); err != nil {
		t.Fatalf("DropHunk: %v", err)
	}
	kHunkDrop := reg.OpenChangesetKey()
	if kHunkDrop == kDropOp {
		t.Fatal("OpenChangesetKey() did not change on DropHunk")
	}

	// 7. Changes on undrop
	if err := e.UndropHunk(opPatch.ID, hunkID); err != nil {
		t.Fatalf("UndropHunk: %v", err)
	}
	kUndrop := reg.OpenChangesetKey()
	if kUndrop == kHunkDrop {
		t.Fatal("OpenChangesetKey() did not change on UndropHunk")
	}

	// 8. Changes when the committed vault is reloaded
	if err := e.ReloadVault(); err != nil {
		t.Fatalf("ReloadVault: %v", err)
	}
	kReload := reg.OpenChangesetKey()
	if kReload == kUndrop {
		t.Fatal("OpenChangesetKey() did not change on ReloadVault")
	}
	_ = cs
}

// TestIngestSearchStaysCommittedOnly (A-067-6, inverts the S1 frozen
// TestSearchSeesOwnStagedPagesInIngestRegistry after FIX-W3): an ingest-verb
// registry (ingestToolDeps shape) over a changeset its own stage.create_page
// just staged does NOT find that page by search in an ingest turn — the eval
// showed projected search costs ingest REAL rounds and tokens — while the same
// registry under an ask verb does, tagged.
func TestIngestSearchStaysCommittedOnly(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	e, err := stage.OpenEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	author := stage.Author{Kind: "agent", Model: "test"}
	// Ingest-verb registry shape: Deps with Vault, Index, Engine, Extract, Author, Recompile
	deps := Deps{
		Vault:     e.Vault(),
		Index:     e.Index(),
		Engine:    e,
		Extract:   extract.NewFile(),
		Author:    author,
		Recompile: []string{"raw/articles/kv-cache-explained.md"},
	}
	reg := NewRegistry(deps)

	_, err = e.OpenChangeset("ingest search test", author)
	if err != nil {
		t.Fatal(err)
	}

	body := "# Ingest Staged Concept\n\nIngestSearchKeyword appears only in this staged concept.\n\n## Related\n\n- [[kv-cache]]\n- [[flash-attention]]\n"
	args := map[string]any{
		"path":       "wiki/concepts/ingest-staged.md",
		"title":      "Ingest Staged Concept",
		"type":       "concept",
		"tags":       []string{"inference"},
		"sources":    []string{"raw/articles/kv-cache-explained.md"},
		"confidence": "high",
		"contested":  false,
		"body":       body,
		"rationale":  "ingest created page",
	}
	argBytes, _ := json.Marshal(args)
	if res, err := reg.Call(context.Background(), "stage.create_page", argBytes); err != nil || res.IsError {
		t.Fatalf("stage.create_page: %v, content=%s", err, res.Content)
	}

	ingestCtx := trace.WithVerb(context.Background(), trace.VerbIngest)
	res, err := reg.Call(ingestCtx, "wiki.search", json.RawMessage(`{"q":"IngestSearchKeyword"}`))
	if err != nil {
		t.Fatalf("search error: %v", err)
	}
	if res.Content != `no results for "IngestSearchKeyword"` {
		t.Fatalf("wiki.search in an ingest turn must stay committed-only, got:\n%s", res.Content)
	}

	askCtx := trace.WithVerb(context.Background(), trace.VerbAsk)
	res, err = reg.Call(askCtx, "wiki.search", json.RawMessage(`{"q":"IngestSearchKeyword"}`))
	if err != nil {
		t.Fatalf("search error: %v", err)
	}
	if !strings.Contains(res.Content, "wiki/concepts/ingest-staged.md") || !strings.Contains(res.Content, "   (staged in cs-") {
		t.Fatalf("wiki.search under an ask verb must find the staged page, tagged:\n%s", res.Content)
	}
}

// TestOrientNoChangesetByteIdentical: output byte-identical to the pre-067
// handler on 2 fixture vaults.
func TestOrientNoChangesetByteIdentical(t *testing.T) {
	pre067Orient := func(v *vault.Vault) (string, error) {
		schemaBytes, err := v.Read("SCHEMA.md")
		if err != nil {
			return "", err
		}
		indexText, indexTruncated := firstNLines(readOptional(v, "index.md"), orientIndexMaxLines)
		memoryText := readOptional(v, "curator-memory.md")
		logText := lastNLines(readOptional(v, "log.md"), orientLogMaxLines)

		var b strings.Builder
		b.WriteString("# Orientation\n\n")

		b.WriteString("## SCHEMA.md\n\n")
		writeSectionBody(&b, string(schemaBytes))

		b.WriteString("\n## index.md")
		if indexTruncated {
			fmt.Fprintf(&b, " (truncated to %d lines)", orientIndexMaxLines)
		}
		b.WriteString("\n\n")
		writeSectionBody(&b, indexText)

		b.WriteString("\n## curator-memory.md\n\n")
		writeSectionBody(&b, memoryText)

		fmt.Fprintf(&b, "\n## log.md (last %d lines)\n\n", orientLogMaxLines)
		writeSectionBody(&b, logText)

		return strings.TrimRight(b.String(), "\n") + "\n", nil
	}

	fixtures := []string{"minimal", "minimal"}
	for i, f := range fixtures {
		dir := testutil.CopyFixture(t, f)
		v, err := vault.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		ix := index.Build(v)
		reg := NewRegistry(Deps{Vault: v, Index: ix})

		res, err := reg.Call(context.Background(), "vault.orient", json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("vault %d: Call error = %v", i+1, err)
		}
		want, err := pre067Orient(v)
		if err != nil {
			t.Fatalf("vault %d: pre067Orient = %v", i+1, err)
		}
		if res.Content != want {
			t.Errorf("vault %d: orient byte mismatch:\n got: %q\nwant: %q", i+1, res.Content, want)
		}
	}
}

// TestOrientOpenChangesetSection: 4 ops (create A, patch B twice, ingest_source raw R) → exactly
// ## Open changeset (unreviewed)
//
// <id> · 4 ops · opened 2026-10-19 · not committed: readable, but nothing in it has been reviewed
// wiki/entities/a.md — A Title
// wiki/concepts/b.md — B Title
// raw/articles/r.md — R Title
// appended after the log section (OpenedAt fixed to 2026-10-19T17:37:00Z). A 1-op variant prints 1 op.
// 32 distinct paths → 30 path lines + (+2 more).
func TestOrientOpenChangesetSection(t *testing.T) {
	fixedTime := time.Date(2026, 10, 19, 17, 37, 0, 0, time.UTC)

	t.Run("4_ops", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")

		// Write committed page B before opening the engine
		bContent := `---
title: B Title
created: 2026-08-30
updated: 2026-08-30
type: concept
tags: [inference]
confidence: high
---

# B Title

Initial body of B.

## Section One

Content one.

## Related

- [[kv-cache]]
- [[flash-attention]]
`
		if err := os.WriteFile(filepath.Join(dir, "wiki", "concepts", "b.md"), []byte(bContent), 0o644); err != nil {
			t.Fatal(err)
		}

		e, err := stage.OpenEngine(dir, stage.WithClock(func() time.Time { return fixedTime }))
		if err != nil {
			t.Fatal(err)
		}
		defer e.Close()

		author := stage.Author{Kind: "human"}
		reg := NewRegistry(Deps{Vault: e.Vault(), Index: e.Index(), Engine: e, Author: author, Extract: extract.NewFile()})

		cs, err := e.OpenChangeset("4 ops test", author)
		if err != nil {
			t.Fatal(err)
		}

		// Op 1: create A: wiki/entities/a.md (title "A Title")
		bodyA := "# A Title\n\nContent for A.\n\n## Related\n\n- [[kv-cache]]\n- [[flash-attention]]\n"
		argsA := map[string]any{
			"path":       "wiki/entities/a.md",
			"title":      "A Title",
			"type":       "entity",
			"tags":       []string{"inference"},
			"sources":    []string{"raw/articles/kv-cache-explained.md"},
			"confidence": "high",
			"contested":  false,
			"body":       bodyA,
			"rationale":  "create a",
		}
		argBytesA, _ := json.Marshal(argsA)
		if res, err := reg.Call(context.Background(), "stage.create_page", argBytesA); err != nil || res.IsError {
			t.Fatalf("create A: %v, %s", err, res.Content)
		}

		// Op 2: patch B (first patch)
		patchArgs1 := map[string]any{
			"path":      "wiki/concepts/b.md",
			"op":        "replace_section",
			"section":   "## Section One",
			"content":   "Updated content one in B.",
			"rationale": "patch b once",
		}
		pBytes1, _ := json.Marshal(patchArgs1)
		if res, err := reg.Call(context.Background(), "stage.patch_page", pBytes1); err != nil || res.IsError {
			t.Fatalf("patch B 1: %v, %s", err, res.Content)
		}

		// Op 3: patch B (second patch)
		patchArgs2 := map[string]any{
			"path":      "wiki/concepts/b.md",
			"op":        "insert_before",
			"section":   "## Related",
			"content":   "## Section Two\n\nAdded section two in B.\n",
			"rationale": "patch b twice",
		}
		pBytes2, _ := json.Marshal(patchArgs2)
		if res, err := reg.Call(context.Background(), "stage.patch_page", pBytes2); err != nil || res.IsError {
			t.Fatalf("patch B 2: %v, %s", err, res.Content)
		}

		// Op 4: ingest_source raw R (title "R Title")
		rContent := "---\nsource_url: file://r.md\ningested: 2026-10-19\nsha256: e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\n---\n\n# R Title\n\nBody of raw R source.\n"
		if _, err := e.Append(stage.Op{
			Kind:      stage.OpIngestSource,
			Path:      "raw/articles/r.md",
			Extractor: "test",
			Content:   []byte(rContent),
		}); err != nil {
			t.Fatalf("append ingest R: %v", err)
		}

		askCtx := trace.WithVerb(context.Background(), trace.VerbAsk)
		res, err := reg.Call(askCtx, "vault.orient", json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("vault.orient: %v", err)
		}

		wantSection := fmt.Sprintf("\n## Open changeset (unreviewed)\n\n%s · 4 ops · opened 2026-10-19 · not committed: readable, but nothing in it has been reviewed\nwiki/entities/a.md — A Title\nwiki/concepts/b.md — B Title\nraw/articles/r.md — R Title\n", cs.ID)
		if !strings.HasSuffix(res.Content, wantSection) {
			t.Fatalf("vault.orient output does not end with expected open changeset section.\nGot:\n%s\nWant suffix:\n%s", res.Content, wantSection)
		}
	})

	t.Run("1_op", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		e, err := stage.OpenEngine(dir, stage.WithClock(func() time.Time { return fixedTime }))
		if err != nil {
			t.Fatal(err)
		}
		defer e.Close()

		author := stage.Author{Kind: "human"}
		reg := NewRegistry(Deps{Vault: e.Vault(), Index: e.Index(), Engine: e, Author: author, Extract: extract.NewFile()})

		cs, err := e.OpenChangeset("1 op test", author)
		if err != nil {
			t.Fatal(err)
		}

		// Op 1: create A: wiki/entities/a.md (title "A Title")
		bodyA := "# A Title\n\nContent for A.\n\n## Related\n\n- [[kv-cache]]\n- [[flash-attention]]\n"
		argsA := map[string]any{
			"path":       "wiki/entities/a.md",
			"title":      "A Title",
			"type":       "entity",
			"tags":       []string{"inference"},
			"sources":    []string{"raw/articles/kv-cache-explained.md"},
			"confidence": "high",
			"contested":  false,
			"body":       bodyA,
			"rationale":  "create a",
		}
		argBytesA, _ := json.Marshal(argsA)
		if res, err := reg.Call(context.Background(), "stage.create_page", argBytesA); err != nil || res.IsError {
			t.Fatalf("create A: %v, %s", err, res.Content)
		}

		askCtx := trace.WithVerb(context.Background(), trace.VerbAsk)
		res, err := reg.Call(askCtx, "vault.orient", json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("vault.orient: %v", err)
		}

		wantSection := fmt.Sprintf("\n## Open changeset (unreviewed)\n\n%s · 1 op · opened 2026-10-19 · not committed: readable, but nothing in it has been reviewed\nwiki/entities/a.md — A Title\n", cs.ID)
		if !strings.HasSuffix(res.Content, wantSection) {
			t.Fatalf("vault.orient 1 op output mismatch.\nGot:\n%s\nWant suffix:\n%s", res.Content, wantSection)
		}
	})

	t.Run("32_distinct_paths", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		e, err := stage.OpenEngine(dir, stage.WithClock(func() time.Time { return fixedTime }))
		if err != nil {
			t.Fatal(err)
		}
		defer e.Close()

		author := stage.Author{Kind: "human"}
		reg := NewRegistry(Deps{Vault: e.Vault(), Index: e.Index(), Engine: e, Author: author, Extract: extract.NewFile()})

		cs, err := e.OpenChangeset("32 paths test", author)
		if err != nil {
			t.Fatal(err)
		}

		for i := 1; i <= 32; i++ {
			p := fmt.Sprintf("wiki/concepts/path-%02d.md", i)
			title := fmt.Sprintf("Path Title %02d", i)
			body := fmt.Sprintf("# %s\n\nBody %02d.\n\n## Related\n\n- [[kv-cache]]\n- [[flash-attention]]\n", title, i)
			args := map[string]any{
				"path":       p,
				"title":      title,
				"type":       "concept",
				"tags":       []string{"inference"},
				"sources":    []string{"raw/articles/kv-cache-explained.md"},
				"confidence": "high",
				"contested":  false,
				"body":       body,
				"rationale":  fmt.Sprintf("create %02d", i),
			}
			b, _ := json.Marshal(args)
			if res, err := reg.Call(context.Background(), "stage.create_page", b); err != nil || res.IsError {
				t.Fatalf("create %d: %v, %s", i, err, res.Content)
			}
		}

		askCtx := trace.WithVerb(context.Background(), trace.VerbAsk)
		res, err := reg.Call(askCtx, "vault.orient", json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("vault.orient: %v", err)
		}

		var b strings.Builder
		fmt.Fprintf(&b, "\n## Open changeset (unreviewed)\n\n%s · 32 ops · opened 2026-10-19 · not committed: readable, but nothing in it has been reviewed\n", cs.ID)
		for i := 1; i <= 30; i++ {
			fmt.Fprintf(&b, "wiki/concepts/path-%02d.md — Path Title %02d\n", i, i)
		}
		b.WriteString("(+2 more)\n")

		if !strings.HasSuffix(res.Content, b.String()) {
			t.Fatalf("vault.orient 32 paths output mismatch.\nGot:\n%s\nWant suffix:\n%s", res.Content, b.String())
		}
	})
}

// TestOrientCommittedOnlyOmitsSection tests that with an open changeset and
// live ops, OrientCommittedOnly: true suppresses the "## Open changeset (unreviewed)"
// section, returning output byte-identical to the no-changeset output (067 D2).
func TestOrientCommittedOnlyOmitsSection(t *testing.T) {
	fixedTime := time.Date(2026, 10, 19, 17, 37, 0, 0, time.UTC)
	dir := testutil.CopyFixture(t, "minimal")

	// Write committed page B before opening the engine
	bContent := `---
title: B Title
created: 2026-08-30
updated: 2026-08-30
type: concept
tags: [inference]
confidence: high
---

# B Title

Initial body of B.

## Section One

Content one.

## Related

- [[kv-cache]]
- [[flash-attention]]
`
	if err := os.WriteFile(filepath.Join(dir, "wiki", "concepts", "b.md"), []byte(bContent), 0o644); err != nil {
		t.Fatal(err)
	}

	e, err := stage.OpenEngine(dir, stage.WithClock(func() time.Time { return fixedTime }))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	author := stage.Author{Kind: "human"}
	regOpen := NewRegistry(Deps{Vault: e.Vault(), Index: e.Index(), Engine: e, Author: author, Extract: extract.NewFile()})

	_, err = e.OpenChangeset("4 ops test", author)
	if err != nil {
		t.Fatal(err)
	}

	// Op 1: create A: wiki/entities/a.md (title "A Title")
	bodyA := "# A Title\n\nContent for A.\n\n## Related\n\n- [[kv-cache]]\n- [[flash-attention]]\n"
	argsA := map[string]any{
		"path":       "wiki/entities/a.md",
		"title":      "A Title",
		"type":       "entity",
		"tags":       []string{"inference"},
		"sources":    []string{"raw/articles/kv-cache-explained.md"},
		"confidence": "high",
		"contested":  false,
		"body":       bodyA,
		"rationale":  "create a",
	}
	argBytesA, _ := json.Marshal(argsA)
	if res, err := regOpen.Call(context.Background(), "stage.create_page", argBytesA); err != nil || res.IsError {
		t.Fatalf("create A: %v, %s", err, res.Content)
	}

	// Op 2: patch B (first patch)
	patchArgs1 := map[string]any{
		"path":      "wiki/concepts/b.md",
		"op":        "replace_section",
		"section":   "## Section One",
		"content":   "Updated content one in B.",
		"rationale": "patch b once",
	}
	pBytes1, _ := json.Marshal(patchArgs1)
	if res, err := regOpen.Call(context.Background(), "stage.patch_page", pBytes1); err != nil || res.IsError {
		t.Fatalf("patch B 1: %v, %s", err, res.Content)
	}

	// Op 3: patch B (second patch)
	patchArgs2 := map[string]any{
		"path":      "wiki/concepts/b.md",
		"op":        "insert_before",
		"section":   "## Related",
		"content":   "## Section Two\n\nAdded section two in B.\n",
		"rationale": "patch b twice",
	}
	pBytes2, _ := json.Marshal(patchArgs2)
	if res, err := regOpen.Call(context.Background(), "stage.patch_page", pBytes2); err != nil || res.IsError {
		t.Fatalf("patch B 2: %v, %s", err, res.Content)
	}

	// Op 4: ingest_source raw R (title "R Title")
	rContent := "---\nsource_url: file://r.md\ningested: 2026-10-19\nsha256: e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\n---\n\n# R Title\n\nBody of raw R source.\n"
	if _, err := e.Append(stage.Op{
		Kind:      stage.OpIngestSource,
		Path:      "raw/articles/r.md",
		Extractor: "test",
		Content:   []byte(rContent),
	}); err != nil {
		t.Fatalf("append ingest R: %v", err)
	}

	// Registry with OrientCommittedOnly: true
	regCommittedOnly := NewRegistry(Deps{
		Vault:               e.Vault(),
		Index:               e.Index(),
		Engine:              e,
		Author:              author,
		Extract:             extract.NewFile(),
		OrientCommittedOnly: true,
	})

	resCommittedOnly, err := regCommittedOnly.Call(context.Background(), "vault.orient", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("vault.orient with OrientCommittedOnly: %v", err)
	}

	// No-changeset baseline: registry over committed vault with no engine
	regNoCS := NewRegistry(Deps{Vault: e.Vault(), Index: e.Index()})
	resNoCS, err := regNoCS.Call(context.Background(), "vault.orient", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("vault.orient no-changeset baseline: %v", err)
	}

	if resCommittedOnly.Content != resNoCS.Content {
		t.Fatalf("OrientCommittedOnly output mismatch with no-changeset output.\nGot:\n%s\nWant:\n%s", resCommittedOnly.Content, resNoCS.Content)
	}
	if strings.Contains(resCommittedOnly.Content, "Open changeset") {
		t.Fatalf("OrientCommittedOnly output unexpectedly contains open-changeset section:\n%s", resCommittedOnly.Content)
	}
}

// TestOrientSectionWithoutExtractor tests that when an open changeset is present,
// Extract: nil and OrientCommittedOnly: false still includes the "## Open changeset (unreviewed)"
// section (F1 fix).
func TestOrientSectionWithoutExtractor(t *testing.T) {
	fixedTime := time.Date(2026, 10, 19, 17, 37, 0, 0, time.UTC)
	dir := testutil.CopyFixture(t, "minimal")

	// Write committed page B before opening the engine
	bContent := `---
title: B Title
created: 2026-08-30
updated: 2026-08-30
type: concept
tags: [inference]
confidence: high
---

# B Title

Initial body of B.

## Section One

Content one.

## Related

- [[kv-cache]]
- [[flash-attention]]
`
	if err := os.WriteFile(filepath.Join(dir, "wiki", "concepts", "b.md"), []byte(bContent), 0o644); err != nil {
		t.Fatal(err)
	}

	e, err := stage.OpenEngine(dir, stage.WithClock(func() time.Time { return fixedTime }))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	author := stage.Author{Kind: "human"}
	regSetup := NewRegistry(Deps{Vault: e.Vault(), Index: e.Index(), Engine: e, Author: author, Extract: extract.NewFile()})

	cs, err := e.OpenChangeset("4 ops test", author)
	if err != nil {
		t.Fatal(err)
	}

	// Op 1: create A: wiki/entities/a.md (title "A Title")
	bodyA := "# A Title\n\nContent for A.\n\n## Related\n\n- [[kv-cache]]\n- [[flash-attention]]\n"
	argsA := map[string]any{
		"path":       "wiki/entities/a.md",
		"title":      "A Title",
		"type":       "entity",
		"tags":       []string{"inference"},
		"sources":    []string{"raw/articles/kv-cache-explained.md"},
		"confidence": "high",
		"contested":  false,
		"body":       bodyA,
		"rationale":  "create a",
	}
	argBytesA, _ := json.Marshal(argsA)
	if res, err := regSetup.Call(context.Background(), "stage.create_page", argBytesA); err != nil || res.IsError {
		t.Fatalf("create A: %v, %s", err, res.Content)
	}

	// Op 2: patch B (first patch)
	patchArgs1 := map[string]any{
		"path":      "wiki/concepts/b.md",
		"op":        "replace_section",
		"section":   "## Section One",
		"content":   "Updated content one in B.",
		"rationale": "patch b once",
	}
	pBytes1, _ := json.Marshal(patchArgs1)
	if res, err := regSetup.Call(context.Background(), "stage.patch_page", pBytes1); err != nil || res.IsError {
		t.Fatalf("patch B 1: %v, %s", err, res.Content)
	}

	// Op 3: patch B (second patch)
	patchArgs2 := map[string]any{
		"path":      "wiki/concepts/b.md",
		"op":        "insert_before",
		"section":   "## Related",
		"content":   "## Section Two\n\nAdded section two in B.\n",
		"rationale": "patch b twice",
	}
	pBytes2, _ := json.Marshal(patchArgs2)
	if res, err := regSetup.Call(context.Background(), "stage.patch_page", pBytes2); err != nil || res.IsError {
		t.Fatalf("patch B 2: %v, %s", err, res.Content)
	}

	// Op 4: ingest_source raw R (title "R Title")
	rContent := "---\nsource_url: file://r.md\ningested: 2026-10-19\nsha256: e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\n---\n\n# R Title\n\nBody of raw R source.\n"
	if _, err := e.Append(stage.Op{
		Kind:      stage.OpIngestSource,
		Path:      "raw/articles/r.md",
		Extractor: "test",
		Content:   []byte(rContent),
	}); err != nil {
		t.Fatalf("append ingest R: %v", err)
	}

	// Registry with Extract: nil, OrientCommittedOnly: false
	regNoExtract := NewRegistry(Deps{
		Vault:               e.Vault(),
		Index:               e.Index(),
		Engine:              e,
		Author:              author,
		Extract:             nil,
		OrientCommittedOnly: false,
	})

	askCtx := trace.WithVerb(context.Background(), trace.VerbAsk)
	res, err := regNoExtract.Call(askCtx, "vault.orient", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("vault.orient: %v", err)
	}

	wantSection := fmt.Sprintf("\n## Open changeset (unreviewed)\n\n%s · 4 ops · opened 2026-10-19 · not committed: readable, but nothing in it has been reviewed\nwiki/entities/a.md — A Title\nwiki/concepts/b.md — B Title\nraw/articles/r.md — R Title\n", cs.ID)
	if !strings.HasSuffix(res.Content, wantSection) {
		t.Fatalf("vault.orient output with Extract: nil does not end with expected open changeset section.\nGot:\n%s\nWant suffix:\n%s", res.Content, wantSection)
	}
}

// TestStagedReadPredicate: table: wiki.get/raw.get + marker → true; wire names
// wiki_get/raw_get → true; wiki.search with a tag line → true; wiki.search
// without → false; raw.list staged in cs-… row → false; any tool with the
// marker text not at the start → false.
func TestStagedReadPredicate(t *testing.T) {
	marker := "(staged in the open changeset, not yet committed)\n# Title\nBody"
	tagLine := "1 result(s) for \"query\":\n1. wiki/concepts/a.md — A Title\n   snippet\n   (staged in cs-857cb0bbcd3427ae, unreviewed)"
	searchNormal := "1 result(s) for \"query\":\n1. wiki/concepts/a.md — A Title\n   snippet"
	rawListStaged := "1 source(s):\n- raw/articles/a.md — A Title (staged in cs-857cb0bbcd3427ae)"
	markerNotAtStart := "Prefix line before marker\n(staged in the open changeset, not yet committed)\nbody"

	tests := []struct {
		name    string
		tool    string
		content string
		want    bool
	}{
		{"wiki.get with marker", "wiki.get", marker, true},
		{"raw.get with marker", "raw.get", marker, true},
		{"wiki.get with citable marker", "wiki.get", stagedCitableMarker + "# Title\nBody", true},
		{"raw.get with citable marker", "raw.get", stagedCitableMarker + "chunk 1 of 1\n\nBody", true},
		{"wiki_get wire name with marker", "wiki_get", marker, true},
		{"raw_get wire name with marker", "raw_get", marker, true},
		{"wiki_get wire name with citable marker", "wiki_get", stagedCitableMarker + "# Title\nBody", true},
		{"raw_get wire name with citable marker", "raw_get", stagedCitableMarker + "chunk 1 of 1\n\nBody", true},
		{"wiki.search with tag line", "wiki.search", tagLine, true},
		{"wiki_search with tag line", "wiki_search", tagLine, true},
		{"wiki.search without tag line", "wiki.search", searchNormal, false},
		{"wiki_search without tag line", "wiki_search", searchNormal, false},
		{"raw.list staged row does not count", "raw.list", rawListStaged, false},
		{"raw_list wire name staged row does not count", "raw_list", rawListStaged, false},
		{"wiki.get marker not at start", "wiki.get", markerNotAtStart, false},
		{"raw.get marker not at start", "raw.get", markerNotAtStart, false},
		{"unrelated tool with marker", "stage.create_page", marker, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StagedRead(tt.tool, tt.content)
			if got != tt.want {
				t.Errorf("StagedRead(%q, %q) = %v, want %v", tt.tool, tt.content, got, tt.want)
			}
		})
	}
}

// TestNoChangesetWithEngineByteIdentical pins the production shape of "no
// changeset open" (067 S1 review m1): an Engine is present but nothing is
// staged, so wiki.search and vault.orient answer exactly what a registry with
// no Engine at all answers — the pre-067 output.
func TestNoChangesetWithEngineByteIdentical(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	e, err := stage.OpenEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, err := e.Current(); err != stage.ErrNoChangeset {
		t.Fatalf("fixture has an open changeset: %v", err)
	}
	withEngine := NewRegistry(Deps{Vault: e.Vault(), Index: e.Index(), Engine: e})
	bare := NewRegistry(Deps{Vault: e.Vault(), Index: e.Index()})
	if k := withEngine.OpenChangesetKey(); k != "" {
		t.Fatalf("OpenChangesetKey = %q, want empty", k)
	}

	calls := []struct{ tool, args string }{
		{"wiki.search", `{"q":"cache"}`},
		{"wiki.search", `{"q":"attention"}`},
		{"wiki.search", `{"q":"nonexistentquery"}`},
		{"vault.orient", `{}`},
	}
	for _, c := range calls {
		got, err := withEngine.Call(context.Background(), c.tool, json.RawMessage(c.args))
		if err != nil {
			t.Fatalf("%s %s with engine: %v", c.tool, c.args, err)
		}
		want, err := bare.Call(context.Background(), c.tool, json.RawMessage(c.args))
		if err != nil {
			t.Fatalf("%s %s bare: %v", c.tool, c.args, err)
		}
		if got != want {
			t.Errorf("%s %s:\nwith engine: %q\nbare:        %q", c.tool, c.args, got.Content, want.Content)
		}
	}
}

// TestProjectedSearchOnlyInReadOnlyVerbs pins 067 FIX-W3 (W3-1):
// open changeset with a staged-only page; ctx verbs ask, query, file → hit + tag line;
// verbs ingest, lint, "" → output == the committed index's ("no results for …").
func TestProjectedSearchOnlyInReadOnlyVerbs(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	e, err := stage.OpenEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	author := stage.Author{Kind: "agent", Model: "test"}
	reg := NewRegistry(Deps{
		Vault:   e.Vault(),
		Index:   e.Index(),
		Engine:  e,
		Author:  author,
		Extract: extract.NewFile(),
	})

	cs, err := e.OpenChangeset("w3-1 search test", author)
	if err != nil {
		t.Fatal(err)
	}

	body := "# Staged Only Concept\n\nStagedSearchOnlyTerm appears only in this staged-only page.\n\n## Related\n\n- [[kv-cache]]\n- [[flash-attention]]\n"
	args := map[string]any{
		"path":       "wiki/concepts/staged-only-page.md",
		"title":      "Staged Only Concept",
		"type":       "concept",
		"tags":       []string{"inference"},
		"sources":    []string{"raw/articles/kv-cache-explained.md"},
		"confidence": "high",
		"contested":  false,
		"body":       body,
		"rationale":  "create staged-only page",
	}
	argBytes, _ := json.Marshal(args)
	if res, err := reg.Call(context.Background(), "stage.create_page", argBytes); err != nil || res.IsError {
		t.Fatalf("stage.create_page: %v, content=%s", err, res.Content)
	}

	query := "StagedSearchOnlyTerm"
	searchArg := json.RawMessage(fmt.Sprintf(`{"q":%q}`, query))
	wantCommitted := fmt.Sprintf("no results for %q", query)
	wantTag := fmt.Sprintf("   (staged in %s, unreviewed)", cs.ID)

	// Read-only verbs: ask, query, file → hit + tag line
	readOnlyVerbs := []string{trace.VerbAsk, trace.VerbQuery, trace.VerbFile}
	for _, verb := range readOnlyVerbs {
		t.Run("read_only_"+verb, func(t *testing.T) {
			ctx := trace.WithVerb(context.Background(), verb)
			res, err := reg.Call(ctx, "wiki.search", searchArg)
			if err != nil {
				t.Fatalf("search %s error: %v", verb, err)
			}
			if !strings.Contains(res.Content, "wiki/concepts/staged-only-page.md") {
				t.Fatalf("verb %s did not find staged-only page:\n%s", verb, res.Content)
			}
			if !strings.Contains(res.Content, wantTag) {
				t.Fatalf("verb %s missing tag %q:\n%s", verb, wantTag, res.Content)
			}
		})
	}

	// Non-read-only verbs: ingest, lint, "" → output == the committed index's (no results for ...)
	nonReadOnlyVerbs := []string{trace.VerbIngest, trace.VerbLint, ""}
	for _, verb := range nonReadOnlyVerbs {
		t.Run("non_read_only_"+verb, func(t *testing.T) {
			ctx := context.Background()
			if verb != "" {
				ctx = trace.WithVerb(ctx, verb)
			}
			res, err := reg.Call(ctx, "wiki.search", searchArg)
			if err != nil {
				t.Fatalf("search %q error: %v", verb, err)
			}
			if res.Content != wantCommitted {
				t.Fatalf("verb %q output = %q, want committed index output %q", verb, res.Content, wantCommitted)
			}
		})
	}
}

// TestStagedPrefixCitableInReadOnlyVerbs pins 067 FIX-W3 (W3-2):
// wiki.get + raw.get of staged content under ask → the W3-2 prefix exactly;
// under ingest and "" → stagedSourceMarker exactly; StagedRead true for both.
func TestStagedPrefixCitableInReadOnlyVerbs(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	e, err := stage.OpenEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	author := stage.Author{Kind: "agent", Model: "test"}
	reg := NewRegistry(Deps{
		Vault:   e.Vault(),
		Index:   e.Index(),
		Engine:  e,
		Author:  author,
		Extract: extract.NewFile(),
	})

	_, err = e.OpenChangeset("w3-2 prefix test", author)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Stage a page via stage.create_page
	pageBody := "# Staged Citable Page\n\nStaged page body content.\n\n## Related\n\n- [[kv-cache]]\n- [[flash-attention]]\n"
	pageArgs := map[string]any{
		"path":       "wiki/concepts/staged-citable.md",
		"title":      "Staged Citable Page",
		"type":       "concept",
		"tags":       []string{"inference"},
		"sources":    []string{"raw/articles/kv-cache-explained.md"},
		"confidence": "high",
		"contested":  false,
		"body":       pageBody,
		"rationale":  "create staged citable page",
	}
	pBytes, _ := json.Marshal(pageArgs)
	if res, err := reg.Call(context.Background(), "stage.create_page", pBytes); err != nil || res.IsError {
		t.Fatalf("stage.create_page: %v, content=%s", err, res.Content)
	}

	// 2. Stage a raw source via stage.ingest_source (or engine.Append)
	rawContent := "---\nsource_url: file://staged-raw.md\ningested: 2026-10-19\nsha256: e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\n---\n\n# Staged Raw\n\nStaged raw body content.\n"
	if _, err := e.Append(stage.Op{
		Kind:      stage.OpIngestSource,
		Path:      "raw/articles/staged-raw.md",
		Extractor: "test",
		Content:   []byte(rawContent),
	}); err != nil {
		t.Fatalf("append ingest source: %v", err)
	}

	wikiGetArgs := json.RawMessage(`{"page":"wiki/concepts/staged-citable.md"}`)
	rawGetArgs := json.RawMessage(`{"source":"raw/articles/staged-raw.md"}`)

	// A. Under ask: exactly stagedCitableMarker
	askCtx := trace.WithVerb(context.Background(), trace.VerbAsk)

	resWikiAsk, err := reg.Call(askCtx, "wiki.get", wikiGetArgs)
	if err != nil || resWikiAsk.IsError {
		t.Fatalf("wiki.get under ask: %v, content=%s", err, resWikiAsk.Content)
	}
	if !strings.HasPrefix(resWikiAsk.Content, stagedCitableMarker) {
		t.Fatalf("wiki.get under ask prefix mismatch.\nGot: %q\nWant prefix: %q", resWikiAsk.Content, stagedCitableMarker)
	}
	if !StagedRead("wiki.get", resWikiAsk.Content) {
		t.Fatalf("StagedRead(wiki.get, citable content) = false, want true")
	}

	resRawAsk, err := reg.Call(askCtx, "raw.get", rawGetArgs)
	if err != nil || resRawAsk.IsError {
		t.Fatalf("raw.get under ask: %v, content=%s", err, resRawAsk.Content)
	}
	if !strings.HasPrefix(resRawAsk.Content, stagedCitableMarker) {
		t.Fatalf("raw.get under ask prefix mismatch.\nGot: %q\nWant prefix: %q", resRawAsk.Content, stagedCitableMarker)
	}
	if !StagedRead("raw.get", resRawAsk.Content) {
		t.Fatalf("StagedRead(raw.get, citable content) = false, want true")
	}

	// B. Under ingest: exactly stagedSourceMarker
	ingestCtx := trace.WithVerb(context.Background(), trace.VerbIngest)

	resWikiIngest, err := reg.Call(ingestCtx, "wiki.get", wikiGetArgs)
	if err != nil || resWikiIngest.IsError {
		t.Fatalf("wiki.get under ingest: %v, content=%s", err, resWikiIngest.Content)
	}
	if !strings.HasPrefix(resWikiIngest.Content, stagedSourceMarker) {
		t.Fatalf("wiki.get under ingest prefix mismatch.\nGot: %q\nWant prefix: %q", resWikiIngest.Content, stagedSourceMarker)
	}
	if strings.HasPrefix(resWikiIngest.Content, stagedCitableMarker) {
		t.Fatalf("wiki.get under ingest unexpectedly has citable prefix")
	}
	if !StagedRead("wiki.get", resWikiIngest.Content) {
		t.Fatalf("StagedRead(wiki.get, ingest content) = false, want true")
	}

	resRawIngest, err := reg.Call(ingestCtx, "raw.get", rawGetArgs)
	if err != nil || resRawIngest.IsError {
		t.Fatalf("raw.get under ingest: %v, content=%s", err, resRawIngest.Content)
	}
	if !strings.HasPrefix(resRawIngest.Content, stagedSourceMarker) {
		t.Fatalf("raw.get under ingest prefix mismatch.\nGot: %q\nWant prefix: %q", resRawIngest.Content, stagedSourceMarker)
	}
	if strings.HasPrefix(resRawIngest.Content, stagedCitableMarker) {
		t.Fatalf("raw.get under ingest unexpectedly has citable prefix")
	}
	if !StagedRead("raw.get", resRawIngest.Content) {
		t.Fatalf("StagedRead(raw.get, ingest content) = false, want true")
	}

	// C. Under "": exactly stagedSourceMarker
	resWikiEmpty, err := reg.Call(context.Background(), "wiki.get", wikiGetArgs)
	if err != nil || resWikiEmpty.IsError {
		t.Fatalf("wiki.get under \"\": %v, content=%s", err, resWikiEmpty.Content)
	}
	if !strings.HasPrefix(resWikiEmpty.Content, stagedSourceMarker) {
		t.Fatalf("wiki.get under \"\" prefix mismatch.\nGot: %q\nWant prefix: %q", resWikiEmpty.Content, stagedSourceMarker)
	}
	if strings.HasPrefix(resWikiEmpty.Content, stagedCitableMarker) {
		t.Fatalf("wiki.get under \"\" unexpectedly has citable prefix")
	}
	if !StagedRead("wiki.get", resWikiEmpty.Content) {
		t.Fatalf("StagedRead(wiki.get, \"\" content) = false, want true")
	}

	resRawEmpty, err := reg.Call(context.Background(), "raw.get", rawGetArgs)
	if err != nil || resRawEmpty.IsError {
		t.Fatalf("raw.get under \"\": %v, content=%s", err, resRawEmpty.Content)
	}
	if !strings.HasPrefix(resRawEmpty.Content, stagedSourceMarker) {
		t.Fatalf("raw.get under \"\" prefix mismatch.\nGot: %q\nWant prefix: %q", resRawEmpty.Content, stagedSourceMarker)
	}
	if strings.HasPrefix(resRawEmpty.Content, stagedCitableMarker) {
		t.Fatalf("raw.get under \"\" unexpectedly has citable prefix")
	}
	if !StagedRead("raw.get", resRawEmpty.Content) {
		t.Fatalf("StagedRead(raw.get, \"\" content) = false, want true")
	}
}
