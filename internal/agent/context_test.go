package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/vault"
	"github.com/awepo-pro/lw/internal/web"
)

// newTestVault copies spec/fixtures/minimal into a private temp directory
// and opens it, returning the vault root too so tests can mutate files on
// disk (index.md, log.md) between Build calls.
func newTestVault(t *testing.T) (*vault.Vault, string) {
	t.Helper()
	root := testutil.CopyFixture(t, "minimal")
	v, err := vault.Open(root)
	if err != nil {
		t.Fatalf("vault.Open: %v", err)
	}
	return v, root
}

// TestContextOrder is one of the three PASS-by-name tests the stage file
// names. It asserts Build's five-part order exactly (/docs/design.md §11.3,
// backbone §9): system prompt, curator-memory.md verbatim, orientation
// digest, compacted history (role sequence and content), user message.
func TestContextOrder(t *testing.T) {
	v, _ := newTestVault(t)
	reg := tools.NewRegistry(tools.Deps{Vault: v})
	b := NewContextBuilder(v, reg, 100_000)

	memory, err := v.Read("curator-memory.md")
	if err != nil {
		t.Fatalf("read curator-memory.md: %v", err)
	}
	wantDigest, err := reg.Call(context.Background(), "vault.orient", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("vault.orient: %v", err)
	}

	ts := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	s := &Session{
		ID:          "cs-order",
		ChangesetID: "cs-order",
		Records: []Record{
			rec(ts, "user", "first turn"),
			rec(ts.Add(time.Second), "assistant", "first reply"),
		},
	}

	msgs, err := b.Build(s, "please orient and search")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(msgs) != 6 {
		t.Fatalf("len(msgs) = %d, want 6 (system, memory, orient, 2 history, user); got %+v", len(msgs), msgs)
	}

	// The registry below wires no Search provider, so the prompt must be
	// the without-search assembly (012 contract §1).
	if msgs[0].Role != "system" || msgs[0].Content != systemPromptFor(false) {
		t.Errorf("msgs[0] = %+v, want the without-search system prompt", msgs[0])
	}
	if msgs[1].Role != "system" || msgs[1].Content != string(memory) {
		t.Errorf("msgs[1] = %+v, want curator-memory.md verbatim: %q", msgs[1], string(memory))
	}
	if msgs[2].Role != "system" || msgs[2].Content != wantDigest.Content {
		t.Errorf("msgs[2] = %+v, want the orientation digest: %q", msgs[2], wantDigest.Content)
	}
	if msgs[3].Role != "user" || msgs[3].Content != "first turn" {
		t.Errorf("msgs[3] = %+v, want the first history record", msgs[3])
	}
	if msgs[4].Role != "assistant" || msgs[4].Content != "first reply" {
		t.Errorf("msgs[4] = %+v, want the second history record", msgs[4])
	}
	if msgs[5].Role != "user" || msgs[5].Content != "please orient and search" {
		t.Errorf("msgs[5] = %+v, want the new user message last", msgs[5])
	}
}

// TestOrientDigestCachedOncePerSession checks both halves of backbone §9's
// contract: the digest is NOT refetched on every Build call (a log.md edit,
// which vault.orient also reads, must not appear until something forces a
// refetch), and it IS refetched once index.md changes.
func TestOrientDigestCachedOncePerSession(t *testing.T) {
	v, root := newTestVault(t)
	reg := tools.NewRegistry(tools.Deps{Vault: v})
	b := NewContextBuilder(v, reg, 100_000)

	s := &Session{ID: "cs-cache", ChangesetID: "cs-cache"}

	msgs1, err := b.Build(s, "hello")
	if err != nil {
		t.Fatalf("Build 1: %v", err)
	}
	digest1 := msgs1[2].Content

	// Mutate log.md only. vault.orient's own output includes log.md's tail,
	// so if Build refetched on every call, digest2 would differ from
	// digest1. It must not: the digest is cached per session until index.md
	// changes.
	logPath := filepath.Join(root, "log.md")
	appendLine(t, logPath, "- 2026-09-06T00:00:00Z new log entry that must not appear yet\n")

	msgs2, err := b.Build(s, "hello again")
	if err != nil {
		t.Fatalf("Build 2: %v", err)
	}
	digest2 := msgs2[2].Content
	if digest2 != digest1 {
		t.Fatalf("digest changed after a log.md-only edit; want it cached until index.md changes\n1: %q\n2: %q", digest1, digest2)
	}

	// Now mutate index.md. This must force a refresh — and the refreshed
	// digest also picks up the earlier log.md edit, since a real refetch
	// reads everything vault.orient reads.
	indexPath := filepath.Join(root, "index.md")
	appendLine(t, indexPath, "- [[new-page]] — added mid-session\n")

	msgs3, err := b.Build(s, "hello a third time")
	if err != nil {
		t.Fatalf("Build 3: %v", err)
	}
	digest3 := msgs3[2].Content
	if digest3 == digest1 {
		t.Fatalf("digest did not change after index.md changed")
	}
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		t.Fatalf("append %s: %v", path, err)
	}
}

func TestBuildCompactsHistoryOverBudget(t *testing.T) {
	v, _ := newTestVault(t)
	reg := tools.NewRegistry(tools.Deps{Vault: v})

	// A tiny budget: barely enough for the fixed preamble, none left for
	// full history, so Build must hand Compact a small remaining budget
	// rather than erroring.
	b := NewContextBuilder(v, reg, 50)

	ts := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	s := &Session{
		ID:          "cs-tiny",
		ChangesetID: "cs-tiny",
		Records: []Record{
			rec(ts, "user", "a very long turn that should be collapsible under a tiny budget indeed"),
			rec(ts.Add(time.Second), "assistant", "another very long turn that should also be collapsible under budget"),
		},
	}

	msgs, err := b.Build(s, "go")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(msgs) < 4 {
		t.Fatalf("Build produced %d messages, want at least system+memory+orient+user", len(msgs))
	}
	if msgs[len(msgs)-1].Content != "go" {
		t.Fatalf("last message = %+v, want the user message", msgs[len(msgs)-1])
	}
}

// stubSearchProvider is the non-nil web.SearchProvider that makes a test
// registry offer web.search; its results are never read — only the prompt's
// reaction to the verb's presence is under test (012 contract §1).
type stubSearchProvider struct{}

func (stubSearchProvider) Search(ctx context.Context, query string, max int) ([]web.SearchHit, error) {
	return []web.SearchHit{{Title: "stub", URL: "https://example.com/stub", Snippet: "stub snippet"}}, nil
}

// TestBuildContextWebSearch pins 012 contract §1's wiring: Build derives
// hasSearch from its own registry — never a new constructor parameter, never
// a stored field — so the first system message carries the web-lookup
// paragraphs only when the registry actually offers web.search.
func TestBuildContextWebSearch(t *testing.T) {
	v, _ := newTestVault(t)
	s := &Session{ID: "cs-websearch", ChangesetID: "cs-websearch"}

	t.Run("without_search", func(t *testing.T) {
		b := NewContextBuilder(v, tools.NewRegistry(tools.Deps{Vault: v}), 100_000)
		msgs, err := b.Build(s, "hello")
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if strings.Contains(msgs[0].Content, "web.search") {
			t.Fatalf("a registry without a Search provider produced a prompt promising web.search:\n%s", msgs[0].Content)
		}
	})

	t.Run("with_search", func(t *testing.T) {
		b := NewContextBuilder(v, tools.NewRegistry(tools.Deps{Vault: v, Search: stubSearchProvider{}}), 100_000)
		msgs, err := b.Build(s, "hello")
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		search := strings.Index(msgs[0].Content, webSearchRule)
		if search < 0 {
			t.Fatalf("a registry with a Search provider produced a prompt without the search rule:\n%s", msgs[0].Content)
		}
		injection := strings.Index(msgs[0].Content, webInjectionRule)
		if injection < 0 {
			t.Fatalf("a registry with a Search provider produced a prompt without the injection rule:\n%s", msgs[0].Content)
		}
		if injection < search {
			t.Fatalf("injection rule (at %d) precedes the search rule (at %d)", injection, search)
		}
	})
}
