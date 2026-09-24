package tools

// read_raw_page_test.go is 034 T4's pin: a raw body that carries PDF page
// anchors reports the chunk's physical pages in its header, so a model
// reading chunk k can cite "^[raw/x.md p.N]" without guessing which anchor
// its claim sits under. A body without anchors keeps the pre-034 header
// byte for byte — only paged sources change shape.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// pagedBody builds the frozen fixture: four PDF pages, each an anchor line,
// a blank line, 10000 'a' runes and a newline — 40072 runes in all, so raw
// chunking splits it into 3 chunks (16000, 16000, 8072).
func pagedBody() string {
	var b strings.Builder
	for p := 1; p <= 4; p++ {
		fmt.Fprintf(&b, "<!-- page %d -->\n\n%s\n", p, strings.Repeat("a", 10000))
	}
	return b.String()
}

func TestRawGetPageHeader(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	body := pagedBody()
	writeRawSource(t, dir, "raw/papers/x.md", body)
	// The unpaged twin is written before the registry opens the vault: a
	// file appearing afterwards is invisible to the in-memory index.
	writeRawSource(t, dir, "raw/papers/plain.md", strings.Repeat("a", 40072))

	reg := NewRegistry(newTestDeps(t, dir))
	ctx := context.Background()

	// Each chunk's header names the pages its bytes sit on: chunk 1 starts
	// on page 1 and ends on page 2; chunk 2 spans the page 2→4 anchors;
	// chunk 3 lies wholly on page 4.
	want := []string{
		"chunk 1 of 3 · pages 1-2",
		"chunk 2 of 3 · pages 2-4",
		"chunk 3 of 3 · page 4",
	}
	for c := 1; c <= 3; c++ {
		res, err := reg.Call(ctx, "raw.get", json.RawMessage(fmt.Sprintf(`{"source": "raw/papers/x.md", "chunk": %d}`, c)))
		if err != nil || res.IsError {
			t.Fatalf("Call(raw.get, chunk %d) = %+v, err = %v", c, res, err)
		}
		if !strings.HasPrefix(res.Content, want[c-1]+"\n\n") {
			t.Fatalf("chunk %d header = %q, want prefix %q", c, res.Content, want[c-1]+"\n\n")
		}
	}

	// The same body without anchors keeps today's header byte for byte.
	res, err := reg.Call(ctx, "raw.get", json.RawMessage(`{"source": "raw/papers/plain.md"}`))
	if err != nil || res.IsError {
		t.Fatalf("Call(raw.get, unpaged) = %+v, err = %v", res, err)
	}
	if !strings.HasPrefix(res.Content, "chunk 1 of 3\n\n") {
		t.Fatalf("unpaged header = %q, want prefix \"chunk 1 of 3\\n\\n\"", res.Content)
	}
}

// TestRawGetPageHeaderStaged pins that the staged-source marker still comes
// first when the header names pages: the notice tells the model the bytes
// are uncommitted before it reads any page number off them.
func TestRawGetPageHeaderStaged(t *testing.T) {
	doc := ingestDoc{
		Title:     "Paged",
		SourceURL: "https://example.test/paged",
		Markdown:  pagedBody(),
		Kind:      "paper",
	}
	reg, _ := ingestRegistry(t, doc)
	ctx := context.Background()

	ingestRes := openAndIngest(t, reg, "paged.md", "paper")
	if ingestRes.IsError {
		t.Fatalf("stage.ingest_source rejected: %s", ingestRes.Content)
	}
	res, err := reg.Call(ctx, "raw.get", json.RawMessage(`{"source": "raw/papers/paged.md"}`))
	if err != nil || res.IsError {
		t.Fatalf("Call(raw.get, staged) = %+v, err = %v", res, err)
	}
	if !strings.HasPrefix(res.Content, stagedSourceMarker+"chunk 1 of 3 · pages 1-2\n\n") {
		t.Fatalf("staged paged header = %q, want the staged notice before the page header", res.Content)
	}
}

// TestRawGetPageHeaderMultibyte pins the byte-offset arithmetic under
// multi-byte text: chunkText counts RUNES while cite.PageAt counts BYTES,
// so the header's page ends are right only if the chunk start is summed as
// BYTES of the preceding chunks. Three pages of 6000 '字' runes (3 bytes
// each) put chunk 1's rune boundary (16000) deep inside page 3's bytes —
// a rune-count offset would land in page 2 and report the wrong range.
func TestRawGetPageHeaderMultibyte(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	var b strings.Builder
	for p := 1; p <= 3; p++ {
		fmt.Fprintf(&b, "<!-- page %d -->\n\n%s\n", p, strings.Repeat("字", 6000))
	}
	writeRawSource(t, dir, "raw/papers/cjk.md", b.String())

	reg := NewRegistry(newTestDeps(t, dir))
	ctx := context.Background()
	want := []string{"chunk 1 of 2 · pages 1-3", "chunk 2 of 2 · page 3"}
	for c := 1; c <= 2; c++ {
		res, err := reg.Call(ctx, "raw.get", json.RawMessage(fmt.Sprintf(`{"source": "raw/papers/cjk.md", "chunk": %d}`, c)))
		if err != nil || res.IsError {
			t.Fatalf("Call(raw.get, chunk %d) = %+v, err = %v", c, res, err)
		}
		if !strings.HasPrefix(res.Content, want[c-1]+"\n\n") {
			t.Fatalf("chunk %d header = %q, want prefix %q", c, res.Content, want[c-1]+"\n\n")
		}
	}
}
