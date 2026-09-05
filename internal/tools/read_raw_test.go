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
