package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

func TestRawGetReturnsOneChunk(t *testing.T) {
	reg := minimalRegistry(t)
	ctx := context.Background()

	res, err := reg.Call(ctx, "raw.get", json.RawMessage(`{"source": "raw/papers/leviathan-2023.md"}`))
	if err != nil || res.IsError {
		t.Fatalf("Call(raw.get) = %+v, err = %v", res, err)
	}
	if !strings.HasPrefix(res.Content, "chunk 1 of 1\n\n") {
		t.Fatalf("Content = %q, want it to start with the \"chunk i of n\" marker", res.Content)
	}
	if strings.Count(res.Content, "chunk ") != 1 {
		t.Fatalf("raw.get must return exactly one chunk per call, got: %s", res.Content)
	}
}

// buildRawSource writes a raw source file under dir whose body is n
// characters long, all parseable frontmatter.
func writeRawSource(t *testing.T, dir, relPath string, body string) {
	t.Helper()
	sha := vault.BodySHA256(body)
	content := fmt.Sprintf("---\nsource_url: https://example.com/big\ningested: 2026-01-01\nsha256: %s\n---\n\n%s", sha, body)

	full := filepath.Join(dir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

// TestRawGetChunksLongSource builds a raw source over the ~4000-token chunk
// boundary and asserts raw.get paginates it correctly: each call returns
// exactly one chunk, the "i of n" marker is consistent across calls, and
// the chunks concatenate back to the original body.
func TestRawGetChunksLongSource(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")

	var b strings.Builder
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&b, "word%04d ", i) // ~9 chars each, ~27000 chars total
	}
	body := b.String() + "\n"
	writeRawSource(t, dir, "raw/articles/big-source.md", body)

	reg := NewRegistry(newTestDeps(t, dir))
	ctx := context.Background()

	first, err := reg.Call(ctx, "raw.get", json.RawMessage(`{"source": "raw/articles/big-source.md"}`))
	if err != nil || first.IsError {
		t.Fatalf("Call(raw.get, chunk 1) = %+v, err = %v", first, err)
	}
	if !strings.HasPrefix(first.Content, "chunk 1 of ") {
		t.Fatalf("Content = %q, want it to start with \"chunk 1 of N\"", first.Content)
	}

	var n int
	if _, err := fmt.Sscanf(first.Content, "chunk 1 of %d", &n); err != nil {
		t.Fatalf("could not parse chunk count from %q: %v", first.Content, err)
	}
	if n < 2 {
		t.Fatalf("a %d-char body should need more than 1 chunk, got n=%d", len(body), n)
	}

	var reassembled strings.Builder
	for c := 1; c <= n; c++ {
		res, err := reg.Call(ctx, "raw.get", json.RawMessage(fmt.Sprintf(`{"source": "raw/articles/big-source.md", "chunk": %d}`, c)))
		if err != nil || res.IsError {
			t.Fatalf("Call(raw.get, chunk %d) = %+v, err = %v", c, res, err)
		}
		marker := fmt.Sprintf("chunk %d of %d\n\n", c, n)
		if !strings.HasPrefix(res.Content, marker) {
			t.Fatalf("chunk %d Content = %q, want prefix %q", c, res.Content, marker)
		}
		reassembled.WriteString(strings.TrimPrefix(res.Content, marker))
	}
	if reassembled.String() != body {
		t.Fatalf("reassembled chunks (%d chars) do not match original body (%d chars)", reassembled.Len(), len(body))
	}

	outOfRange, err := reg.Call(ctx, "raw.get", json.RawMessage(fmt.Sprintf(`{"source": "raw/articles/big-source.md", "chunk": %d}`, n+1)))
	if err != nil {
		t.Fatalf("Call error = %v", err)
	}
	if !outOfRange.IsError {
		t.Fatal("chunk n+1 must be IsError")
	}
}

func TestRawGetUnknownSource(t *testing.T) {
	reg := minimalRegistry(t)
	res, err := reg.Call(context.Background(), "raw.get", json.RawMessage(`{"source": "raw/does-not-exist.md"}`))
	if err != nil {
		t.Fatalf("Call error = %v", err)
	}
	if !res.IsError {
		t.Fatal("an unknown source must be IsError, not silently empty")
	}
	if res.Content == "" {
		t.Fatal("IsError result must explain what was wrong")
	}
}

func TestChunkTextEmptyBody(t *testing.T) {
	chunks := chunkText("", 100)
	if len(chunks) != 1 || chunks[0] != "" {
		t.Fatalf("chunkText(\"\", 100) = %#v, want one empty chunk", chunks)
	}
}

// openAndIngest opens a changeset and runs stage.ingest_source with an
// explicit kind, unlike proposeIngest (stage_source_test.go), which always
// sends "kind":"paper" regardless of its ingestDoc — this package's read.get
// tests need to control the kind, since that is exactly what determines the
// raw/ subdirectory S6-C122 fixed.
func openAndIngest(t *testing.T, reg *Registry, uri, kind string) Result {
	t.Helper()
	ctx := context.Background()
	if r, err := reg.Call(ctx, "stage.open", json.RawMessage(`{"intent":"ingest a source"}`)); err != nil || r.IsError {
		t.Fatalf("stage.open: %+v %v", r, err)
	}
	r, err := reg.Call(ctx, "stage.ingest_source", json.RawMessage(fmt.Sprintf(`{"uri":%q,"kind":%q}`, uri, kind)))
	if err != nil {
		t.Fatalf("stage.ingest_source: %v", err)
	}
	return r
}

// TestRawGetReadsStagedIngestSource is the S6-C121 regression: immediately
// after stage.ingest_source succeeds — before any commit — raw.get on the
// exact path the result named must return chunk 1 of the staged body, not
// "was not found". This is the failure the live G6 ingest hit: the agent
// staged raw/article/gemini.md and every subsequent raw.get, including the
// exact right guess, came back not-found because raw.get read only the
// committed vault.
func TestRawGetReadsStagedIngestSource(t *testing.T) {
	doc := ingestDoc{
		Title:     "Gemini",
		SourceURL: "https://example.test/gemini",
		Markdown:  "# Gemini\n\nA staged body raw.get must be able to read.\n",
		Kind:      "article",
	}
	reg, _ := ingestRegistry(t, doc)
	ctx := context.Background()

	ingestRes := openAndIngest(t, reg, "gemini.md", "article")
	if ingestRes.IsError {
		t.Fatalf("stage.ingest_source rejected: %s", ingestRes.Content)
	}
	if !strings.Contains(ingestRes.Content, "raw/articles/gemini.md") {
		t.Fatalf("stage.ingest_source result = %q, want it to name raw/articles/gemini.md", ingestRes.Content)
	}

	res, err := reg.Call(ctx, "raw.get", json.RawMessage(`{"source": "raw/articles/gemini.md"}`))
	if err != nil {
		t.Fatalf("raw.get error = %v", err)
	}
	if res.IsError {
		t.Fatalf("raw.get on a just-staged source is IsError: %s", res.Content)
	}
	if !strings.Contains(res.Content, "staged in the open changeset") {
		t.Errorf("raw.get Content = %q, want the staged marker", res.Content)
	}
	if !strings.Contains(res.Content, "chunk 1 of") {
		t.Errorf("raw.get Content = %q, want a \"chunk 1 of N\" marker", res.Content)
	}
	if !strings.Contains(res.Content, "A staged body raw.get must be able to read.") {
		t.Errorf("raw.get Content = %q, want the staged body", res.Content)
	}
}

// TestRawGetWrongGuessListsStagedPaths pins the not-found message's second
// half: when a source is staged in the open changeset, a wrong guess must
// list the real staged path so a model can self-correct in one step,
// instead of guessing blind the way the live G6 session did ~20 times.
func TestRawGetWrongGuessListsStagedPaths(t *testing.T) {
	doc := ingestDoc{
		Title:     "Gemini",
		SourceURL: "https://example.test/gemini",
		Markdown:  "# Gemini\n\nBody.\n",
		Kind:      "article",
	}
	reg, _ := ingestRegistry(t, doc)
	ctx := context.Background()

	if r := openAndIngest(t, reg, "gemini.md", "article"); r.IsError {
		t.Fatalf("stage.ingest_source rejected: %s", r.Content)
	}

	res, err := reg.Call(ctx, "raw.get", json.RawMessage(`{"source": "raw/articles/wrong-guess.md"}`))
	if err != nil {
		t.Fatalf("raw.get error = %v", err)
	}
	if !res.IsError {
		t.Fatal("raw.get on a wrong guess must be IsError")
	}
	if !strings.Contains(res.Content, "raw/articles/gemini.md") {
		t.Errorf("raw.get not-found Content = %q, want it to list the staged path raw/articles/gemini.md", res.Content)
	}
}

// TestRawGetCommittedSourceStillWorks pins that the S6-C121 fix does not
// disturb the plain committed-source path: no marker, chunk 1 of 1, same
// shape as before the fix (TestRawGetReturnsOneChunk covers the read-only
// registry; this exercises the same call through a real Engine, whose
// StagedFile branch must not fire when Deps.Vault already has the source).
func TestRawGetCommittedSourceStillWorks(t *testing.T) {
	reg, _, _ := engineRegistry(t, nil)
	res, err := reg.Call(context.Background(), "raw.get", json.RawMessage(`{"source": "raw/papers/leviathan-2023.md"}`))
	if err != nil || res.IsError {
		t.Fatalf("Call(raw.get) = %+v, err = %v", res, err)
	}
	if strings.Contains(res.Content, "staged in the open changeset") {
		t.Errorf("Content = %q, a committed source must not carry the staged marker", res.Content)
	}
	if !strings.HasPrefix(res.Content, "chunk 1 of 1\n\n") {
		t.Fatalf("Content = %q, want it to start with \"chunk 1 of 1\"", res.Content)
	}
}
