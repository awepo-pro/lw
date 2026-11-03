package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// setupLintFixSyncFixture creates a test vault from minimal fixture where
// kv-cache links to the new fixture pages to avoid link-orphan warnings.
func setupLintFixSyncFixture(t *testing.T, pages ...string) string {
	t.Helper()
	root := testutil.CopyFixture(t, "minimal")

	// Append links to kv-cache.md so new pages are not orphaned
	kvPath := filepath.Join(root, "wiki", "concepts", "kv-cache.md")
	b, err := os.ReadFile(kvPath)
	if err != nil {
		t.Fatal(err)
	}
	var addLinks strings.Builder
	addLinks.WriteString("\n\n## Fixture Links\n\n")
	for _, p := range pages {
		fmt.Fprintf(&addLinks, "- [[%s]] \u2014 test page link.\n", p)
	}
	if err := os.WriteFile(kvPath, append(b, []byte(addLinks.String())...), 0o644); err != nil {
		t.Fatal(err)
	}

	// Add index entries
	for _, p := range pages {
		addIndexEntry(t, root, p)
	}
	return root
}

// addIndexEntry appends a wikilink to index.md so index-sync passes for that page.
func addIndexEntry(t *testing.T, root, slug string) {
	t.Helper()
	idx := filepath.Join(root, "index.md")
	b, err := os.ReadFile(idx)
	if err != nil {
		t.Fatal(err)
	}
	entry := fmt.Sprintf("- [[%s]] \u2014 fixture concept page.\n", slug)
	if err := os.WriteFile(idx, append(b, []byte(entry)...), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLintFixSyncsCiteSourceBeforeAgentRounds (workflow 081, FB.8) pins that
// lw lint --fix deterministically syncs sources: for a page whose only finding
// is cite-source before any agent round, stages the op, prints the sync line,
// leaves intent intact, and gives the agent a round only for page Q.
func TestLintFixSyncsCiteSourceBeforeAgentRounds(t *testing.T) {
	maxTokens8192Env(t)
	root := setupLintFixSyncFixture(t, "p-cite", "q-link")

	// Page P: only cite-source finding (cites raw/papers/leviathan-2023.md which is not in sources:)
	pageP := `---
title: Page P
created: 2026-08-01
updated: 2026-08-01
type: concept
tags: [inference]
sources: [raw/articles/kv-cache-explained.md]
confidence: high
---

# Page P

## Abstract

Text citing ^[raw/articles/kv-cache-explained.md] and ^[raw/papers/leviathan-2023.md].

## Related

- [[kv-cache]] \u2014 related concept.
- [[flash-attention]] \u2014 another related concept.
`
	if err := os.WriteFile(filepath.Join(root, "wiki", "concepts", "p-cite.md"), []byte(pageP), 0o644); err != nil {
		t.Fatal(err)
	}

	// Page Q: only link-min-out warn (only 1 outbound link)
	pageQ := `---
title: Page Q
created: 2026-08-01
updated: 2026-08-01
type: concept
tags: [inference]
sources: []
confidence: high
---

# Page Q

## Abstract

Text for page Q without extra cites.

## Related

- [[kv-cache]] \u2014 single outbound link.
`
	if err := os.WriteFile(filepath.Join(root, "wiki", "concepts", "q-link.md"), []byte(pageQ), 0o644); err != nil {
		t.Fatal(err)
	}

	var rec *roundRecordingAgent
	withFakeAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
		rec = &roundRecordingAgent{e: e, sessions: sessions}
		return rec, nil
	})

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"lint", "--fix", "--vault", root})
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}

	wantSyncLine := "synced sources: on wiki/concepts/p-cite.md (cite-source; no model)"
	if !strings.Contains(stdout, wantSyncLine) {
		t.Errorf("stdout does not contain %q; stdout:\n%s", wantSyncLine, stdout)
	}

	// Exactly one round: page Q only
	if len(rec.msgs) != 1 {
		t.Fatalf("agent received %d round(s), want exactly 1; msgs:\n%q", len(rec.msgs), rec.msgs)
	}
	if !strings.Contains(rec.msgs[0], "wiki/concepts/q-link.md") {
		t.Errorf("round 1 message does not name q-link.md:\n%s", rec.msgs[0])
	}
	if strings.Contains(rec.msgs[0], "wiki/concepts/p-cite.md") {
		t.Errorf("round 1 message unexpectedly names p-cite.md:\n%s", rec.msgs[0])
	}

	// Verify the changeset holds the sync op for P and the repair op for Q,
	// and intent counts all 2 warn/error findings.
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	wantIntent := "lint --fix: repair 2 finding(s)"
	if cs.Intent != wantIntent {
		t.Errorf("cs.Intent = %q, want %q", cs.Intent, wantIntent)
	}

	b, ok, err := e.StagedFile("wiki/concepts/p-cite.md")
	if err != nil || !ok {
		t.Fatalf("StagedFile for p-cite ok=%v err=%v", ok, err)
	}
	page, err := vault.ParsePage("wiki/concepts/p-cite.md", b)
	if err != nil {
		t.Fatal(err)
	}
	wantSources := []string{"raw/articles/kv-cache-explained.md", "raw/papers/leviathan-2023.md"}
	if len(page.FM.Sources) != 2 || page.FM.Sources[0] != wantSources[0] || page.FM.Sources[1] != wantSources[1] {
		t.Errorf("p-cite staged sources = %v, want %v", page.FM.Sources, wantSources)
	}
}

// TestLintFixSyncsCiteSourceAndKeepsOtherWarnRound (workflow 081, FB.9) pins
// that a page with both cite-source and another warn has its sources synced,
// and still receives an agent round with cite-source findings omitted.
func TestLintFixSyncsCiteSourceAndKeepsOtherWarnRound(t *testing.T) {
	maxTokens8192Env(t)
	root := setupLintFixSyncFixture(t, "p-multi", "q-link")

	// Page P: cite-source finding AND link-min-out finding (only 1 outbound link)
	pageP := `---
title: Page P Multi
created: 2026-08-01
updated: 2026-08-01
type: concept
tags: [inference]
sources: [raw/articles/kv-cache-explained.md]
confidence: high
---

# Page P Multi

## Abstract

Text citing ^[raw/articles/kv-cache-explained.md] and ^[raw/papers/leviathan-2023.md].

## Related

- [[kv-cache]] \u2014 single outbound link.
`
	if err := os.WriteFile(filepath.Join(root, "wiki", "concepts", "p-multi.md"), []byte(pageP), 0o644); err != nil {
		t.Fatal(err)
	}

	// Page Q: only link-min-out warn
	pageQ := `---
title: Page Q
created: 2026-08-01
updated: 2026-08-01
type: concept
tags: [inference]
sources: []
confidence: high
---

# Page Q

## Abstract

Text for page Q.

## Related

- [[kv-cache]] \u2014 single outbound link.
`
	if err := os.WriteFile(filepath.Join(root, "wiki", "concepts", "q-link.md"), []byte(pageQ), 0o644); err != nil {
		t.Fatal(err)
	}

	var rec *roundRecordingAgent
	withFakeAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
		rec = &roundRecordingAgent{e: e, sessions: sessions}
		return rec, nil
	})

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"lint", "--fix", "--vault", root})
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}

	wantSyncLine := "synced sources: on wiki/concepts/p-multi.md (cite-source; no model)"
	if !strings.Contains(stdout, wantSyncLine) {
		t.Errorf("stdout does not contain %q; stdout:\n%s", wantSyncLine, stdout)
	}

	// Page P must still get an agent round for link-min-out, with cite-source omitted
	// Plus page Q gets its round: total 2 rounds
	if len(rec.msgs) != 2 {
		t.Fatalf("agent received %d round(s), want exactly 2; msgs:\n%q", len(rec.msgs), rec.msgs)
	}
	msgP := rec.msgs[0]
	if !strings.Contains(msgP, "wiki/concepts/p-multi.md") {
		t.Errorf("round 1 does not name p-multi.md:\n%s", msgP)
	}
	if strings.Contains(msgP, "cite-source") {
		t.Errorf("round 1 unexpectedly contains cite-source finding:\n%s", msgP)
	}
	if !strings.Contains(msgP, "link-min-out") {
		t.Errorf("round 1 missing link-min-out finding:\n%s", msgP)
	}

	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	b, ok, err := e.StagedFile("wiki/concepts/p-multi.md")
	if err != nil || !ok {
		t.Fatalf("StagedFile for p-multi ok=%v err=%v", ok, err)
	}
	page, err := vault.ParsePage("wiki/concepts/p-multi.md", b)
	if err != nil {
		t.Fatal(err)
	}
	wantSources := []string{"raw/articles/kv-cache-explained.md", "raw/papers/leviathan-2023.md"}
	if len(page.FM.Sources) != 2 || page.FM.Sources[0] != wantSources[0] || page.FM.Sources[1] != wantSources[1] {
		t.Errorf("p-multi staged sources = %v, want %v", page.FM.Sources, wantSources)
	}
}

// TestLintFixSyncRefusalFallsBackToAgentRound (workflow 081, FB.6/FB.8) pins
// that when sync_sources is refused (e.g. page has no sources: key), the refusal
// is printed as "could not sync sources: on P: <refusal>" and the cite-source
// finding goes to the agent round instead.
func TestLintFixSyncRefusalFallsBackToAgentRound(t *testing.T) {
	maxTokens8192Env(t)
	root := setupLintFixSyncFixture(t, "p-nosources")

	// Page P has NO sources: key in frontmatter, but cites raw/papers/leviathan-2023.md
	pageP := `---
title: Page P No Sources
created: 2026-08-01
updated: 2026-08-01
type: concept
tags: [inference]
confidence: high
---

# Page P No Sources

## Abstract

Text citing ^[raw/papers/leviathan-2023.md].

## Related

- [[kv-cache]] \u2014 link 1.
- [[flash-attention]] \u2014 link 2.
`
	if err := os.WriteFile(filepath.Join(root, "wiki", "concepts", "p-nosources.md"), []byte(pageP), 0o644); err != nil {
		t.Fatal(err)
	}

	var rec *roundRecordingAgent
	withFakeAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
		rec = &roundRecordingAgent{e: e, sessions: sessions}
		return rec, nil
	})

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"lint", "--fix", "--vault", root})
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}

	wantCouldNotSync := "could not sync sources: on wiki/concepts/p-nosources.md: sync_sources needs a sources: key; this page has none, so the reviewer adds it by hand"
	if !strings.Contains(stdout, wantCouldNotSync) {
		t.Errorf("stdout does not contain %q; stdout:\n%s", wantCouldNotSync, stdout)
	}

	// Page P should now be handed to the agent round with cite-source
	if len(rec.msgs) != 1 {
		t.Fatalf("agent received %d round(s), want exactly 1; msgs:\n%q", len(rec.msgs), rec.msgs)
	}
	msg := rec.msgs[0]
	if !strings.Contains(msg, "wiki/concepts/p-nosources.md") {
		t.Errorf("round 1 does not name p-nosources.md:\n%s", msg)
	}
	if !strings.Contains(msg, "cite-source") {
		t.Errorf("round 1 missing cite-source finding:\n%s", msg)
	}
}
