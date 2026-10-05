package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/stage"
)

// 040: the stage.close coverage guard. A live ingest read 1-2 chunks per
// round, skimmed near-cap sources and wrote pages from chunks it had never
// opened; the guard makes "every chunk of an ingested source was read" a
// mechanical fact the close checks, not a hope the prompt states.

// The two halves of the refusal text, byte for byte (040 frozen block). The
// em dash is part of the contract: the model and the trace both quote it.
const (
	closeRefusalHead = "stage.close: not every chunk of the sources ingested in this changeset was read — "
	closeRefusalTail = ". Read them with raw.get (request several chunks in one round), or call stage.close again to close anyway."
)

// guardExtractor serves a different document per uri, so one changeset can
// ingest several distinct sources (fakeExtractor serves one doc for all).
type guardExtractor struct{ docs map[string]*extract.Doc }

func (g guardExtractor) CanHandle(uri string) bool { return g.docs[uri] != nil }
func (g guardExtractor) Extract(_ context.Context, uri string) (*extract.Doc, error) {
	return g.docs[uri], nil
}

// guardBody is a markdown body of exactly n runes (n is a multiple of 10):
// rawChunkRunes is 16000, so 40000 runes are 3 chunks and 20000 are 2.
func guardBody(n int) string { return strings.Repeat("abcdefghi\n", n/10) }

// guardDoc is a distinct article document of the given body size.
func guardDoc(title string, runes int) *extract.Doc {
	return &extract.Doc{
		Title: title, SourceURL: "https://example.test/" + strings.ToLower(strings.ReplaceAll(title, " ", "-")),
		Markdown: guardBody(runes), Kind: "article", Extractor: "test",
	}
}

var ingestResultRe = regexp.MustCompile(`at (raw/\S+\.md) — (\d+) chunk\(s\)`)

// guardRegistry returns a registry (and its engine) over the minimal
// fixture whose extractor serves docs by uri.
func guardRegistry(t *testing.T, docs map[string]*extract.Doc) (*Registry, *stage.Engine) {
	t.Helper()
	reg, e, _ := engineRegistry(t, guardExtractor{docs: docs})
	return reg, e
}

func guardCall(t *testing.T, reg *Registry, name, args string) Result {
	t.Helper()
	r, err := reg.Call(context.Background(), name, json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s %s: %v", name, args, err)
	}
	return r
}

// guardOpen opens the changeset the way `lw ingest` already has.
func guardOpen(t *testing.T, reg *Registry) {
	t.Helper()
	if r := guardCall(t, reg, "stage.open", `{"intent":"ingest sources"}`); r.IsError {
		t.Fatalf("stage.open: %s", r.Content)
	}
}

// guardIngest runs stage.ingest_source over uri and returns the staged path
// and chunk count read out of the result — the slug is derived by the tool,
// so the test reads it rather than re-deriving it.
func guardIngest(t *testing.T, reg *Registry, uri string) (string, int) {
	t.Helper()
	r := guardCall(t, reg, "stage.ingest_source", `{"uri":"`+uri+`"}`)
	if r.IsError {
		t.Fatalf("stage.ingest_source %s: %s", uri, r.Content)
	}
	m := ingestResultRe.FindStringSubmatch(r.Content)
	if m == nil {
		t.Fatalf("ingest result names no path and chunk count: %q", r.Content)
	}
	n, _ := strconv.Atoi(m[2])
	return m[1], n
}

func guardRead(t *testing.T, reg *Registry, path string, chunk int) {
	t.Helper()
	r := guardCall(t, reg, "raw.get", fmt.Sprintf(`{"source":%q,"chunk":%d}`, path, chunk))
	if r.IsError {
		t.Fatalf("raw.get %s chunk %d: %s", path, chunk, r.Content)
	}
}

// currentSummary is what stage.close printed before 040 — stageSummary over
// the open changeset — the byte-exact baseline a fully read ingest must keep.
func currentSummary(t *testing.T, e *stage.Engine) Result {
	t.Helper()
	cs, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	return stageSummary(cs)
}

// TestStageCloseRefusesUnreadOnce is the heart of 040: a three-chunk source
// of which one chunk was read is refused at the first close — with the exact
// text — and let through at the second, whose summary names what went
// unread so the trace and the review show it.
func TestStageCloseRefusesUnreadOnce(t *testing.T) {
	reg, e := guardRegistry(t, map[string]*extract.Doc{"long.md": guardDoc("Long Source", 40000)})
	guardOpen(t, reg)
	path, n := guardIngest(t, reg, "long.md")
	if n != 3 {
		t.Fatalf("ingest reports %d chunk(s), want 3", n)
	}
	if !strings.HasPrefix(path, "raw/articles/") {
		t.Fatalf("ingested path = %q, want under raw/articles/", path)
	}
	guardRead(t, reg, path, 1)

	first := guardCall(t, reg, "stage.close", `{}`)
	if !first.IsError {
		t.Fatalf("first close not refused: %s", first.Content)
	}
	want := closeRefusalHead + path + " chunks 2, 3 of 3 unread" + closeRefusalTail
	if first.Content != want {
		t.Errorf("refusal =\n%q\nwant\n%q", first.Content, want)
	}

	second := guardCall(t, reg, "stage.close", `{}`)
	if second.IsError {
		t.Fatalf("second close for the same unread set refused: %s", second.Content)
	}
	if suffix := "\nunread chunks: " + path + " 2, 3"; !strings.HasSuffix(second.Content, suffix) {
		t.Errorf("second close content does not end with %q:\n%s", suffix, second.Content)
	}
	// The line is appended to the pre-040 summary, never replaces any of it.
	if base := currentSummary(t, e).Content; !strings.HasPrefix(second.Content, base) {
		t.Errorf("second close does not start with the pre-040 summary:\n%s\nwant prefix\n%s", second.Content, base)
	}
	// The refusal is once per set: a third close is still let through.
	if third := guardCall(t, reg, "stage.close", `{}`); third.IsError {
		t.Errorf("third close refused: %s", third.Content)
	}
}

// TestStageCloseAfterFullRead: a source read in full — in any order, with a
// duplicate read — closes the first time, and the guard leaves no trace in
// the summary: byte-identical to the pre-040 stageSummary output.
func TestStageCloseAfterFullRead(t *testing.T) {
	reg, e := guardRegistry(t, map[string]*extract.Doc{"long.md": guardDoc("Long Source", 40000)})
	guardOpen(t, reg)
	path, _ := guardIngest(t, reg, "long.md")
	for _, c := range []int{3, 1, 2, 1} {
		guardRead(t, reg, path, c)
	}

	got := guardCall(t, reg, "stage.close", `{}`)
	if got.IsError {
		t.Fatalf("close after a full read refused: %s", got.Content)
	}
	if want := currentSummary(t, e).Content; got.Content != want {
		t.Errorf("close content =\n%q\nwant the pre-040 summary\n%q", got.Content, want)
	}
}

// TestStageCloseIgnoresJoinedSources: a source another registry — an earlier
// process, 019's join — staged in the open changeset was read there, not
// here, so this registry cannot know and does not check it.
func TestStageCloseIgnoresJoinedSources(t *testing.T) {
	docs := map[string]*extract.Doc{"long.md": guardDoc("Long Source", 40000)}
	earlier, e := guardRegistry(t, docs)
	guardOpen(t, earlier)
	guardIngest(t, earlier, "long.md")

	v := e.Vault()
	later := NewRegistry(Deps{Vault: v, Index: index.Build(v), Engine: e, Extract: guardExtractor{docs: docs}, Author: stage.Author{Kind: "agent", Model: "test"}})
	if r := guardCall(t, later, "stage.close", `{}`); r.IsError {
		t.Fatalf("a registry that ingested nothing refused the close: %s", r.Content)
	} else if strings.Contains(r.Content, "unread chunks") {
		t.Errorf("a registry that ingested nothing reported unread chunks: %s", r.Content)
	}
	// Control: the registry that did stage the source still guards it.
	if r := guardCall(t, earlier, "stage.close", `{}`); !r.IsError {
		t.Errorf("the ingesting registry let an unread source through: %s", r.Content)
	}
}

// TestStageCloseRefreshesOnNewSet: the refusal is per unread SET. After a
// read changes the set, the next close is refused afresh for the smaller
// set; only a close that repeats the set just refused goes through.
func TestStageCloseRefreshesOnNewSet(t *testing.T) {
	reg, _ := guardRegistry(t, map[string]*extract.Doc{"long.md": guardDoc("Long Source", 40000)})
	guardOpen(t, reg)
	path, _ := guardIngest(t, reg, "long.md")
	guardRead(t, reg, path, 1)

	first := guardCall(t, reg, "stage.close", `{}`)
	if want := closeRefusalHead + path + " chunks 2, 3 of 3 unread" + closeRefusalTail; !first.IsError || first.Content != want {
		t.Fatalf("first close = %+v, want the {2, 3} refusal", first)
	}

	guardRead(t, reg, path, 2)
	second := guardCall(t, reg, "stage.close", `{}`)
	if want := closeRefusalHead + path + " chunks 3 of 3 unread" + closeRefusalTail; !second.IsError || second.Content != want {
		t.Fatalf("close after reading chunk 2 = %+v, want the {3} refusal", second)
	}

	third := guardCall(t, reg, "stage.close", `{}`)
	if third.IsError {
		t.Fatalf("close repeating the {3} set refused: %s", third.Content)
	}
	if suffix := "\nunread chunks: " + path + " 3"; !strings.HasSuffix(third.Content, suffix) {
		t.Errorf("third close content does not end with %q:\n%s", suffix, third.Content)
	}
}

// TestStageCloseListsSourcesSorted: several unread sources are named sorted
// by path and joined by "; ", chunk lists ascending — in the refusal and in
// the line the second close appends.
func TestStageCloseListsSourcesSorted(t *testing.T) {
	reg, _ := guardRegistry(t, map[string]*extract.Doc{
		"zeta.md":  guardDoc("Zeta Paper", 20000),
		"alpha.md": guardDoc("Alpha Note", 40000),
	})
	guardOpen(t, reg)
	// Zeta is ingested first: the order in the message must still be by path.
	zeta, zn := guardIngest(t, reg, "zeta.md")
	alpha, an := guardIngest(t, reg, "alpha.md")
	if zn != 2 || an != 3 {
		t.Fatalf("chunk counts zeta=%d alpha=%d, want 2 and 3", zn, an)
	}
	if !(alpha < zeta) {
		t.Fatalf("fixture paths %q, %q are not in the order the test expects", alpha, zeta)
	}
	guardRead(t, reg, alpha, 2)

	first := guardCall(t, reg, "stage.close", `{}`)
	want := closeRefusalHead + alpha + " chunks 1, 3 of 3 unread; " + zeta + " chunks 1, 2 of 2 unread" + closeRefusalTail
	if !first.IsError || first.Content != want {
		t.Fatalf("refusal =\n%q\nwant\n%q", first.Content, want)
	}
	second := guardCall(t, reg, "stage.close", `{}`)
	if suffix := "\nunread chunks: " + alpha + " 1, 3; " + zeta + " 1, 2"; second.IsError || !strings.HasSuffix(second.Content, suffix) {
		t.Errorf("second close = %+v, want success ending with %q", second, suffix)
	}
}

// TestStageCloseIgnoresDroppedSource: the guard is about the sources in the
// changeset being closed. A registry outlives a changeset (the TUI's does,
// and so does an MCP server's); a source whose op is no longer live there —
// dropped here, committed in a real session — must not hold the next close
// to chunks nobody will ever write pages from.
func TestStageCloseIgnoresDroppedSource(t *testing.T) {
	reg, e := guardRegistry(t, map[string]*extract.Doc{"long.md": guardDoc("Long Source", 40000)})
	guardOpen(t, reg)
	guardIngest(t, reg, "long.md")
	cs, err := e.Current()
	if err != nil || len(cs.Ops) != 1 {
		t.Fatalf("Current = %+v, %v; want one staged op", cs, err)
	}
	if err := e.DropOp(cs.Ops[0].ID); err != nil {
		t.Fatalf("DropOp: %v", err)
	}
	if r := guardCall(t, reg, "stage.close", `{}`); r.IsError {
		t.Fatalf("close refused for a source whose op was dropped: %s", r.Content)
	}
}

// TestStageCloseReingestStartsOver: a re-ingested path is a new body — its
// earlier reads do not count, or a longer re-ingest would look read.
func TestStageCloseReingestStartsOver(t *testing.T) {
	reg, e := guardRegistry(t, map[string]*extract.Doc{"long.md": guardDoc("Long Source", 40000)})
	guardOpen(t, reg)
	path, _ := guardIngest(t, reg, "long.md")
	for c := 1; c <= 3; c++ {
		guardRead(t, reg, path, c)
	}
	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.DropOp(cs.Ops[0].ID); err != nil {
		t.Fatal(err)
	}
	again, _ := guardIngest(t, reg, "long.md")
	if again != path {
		t.Fatalf("re-ingest landed at %q, want the same path %q", again, path)
	}
	want := closeRefusalHead + path + " chunks 1, 2, 3 of 3 unread" + closeRefusalTail
	if r := guardCall(t, reg, "stage.close", `{}`); !r.IsError || r.Content != want {
		t.Errorf("close after a re-ingest = %+v, want the all-unread refusal %q", r, want)
	}
}

// TestRawGetFailureDoesNotCountAsRead: only a chunk the model was actually
// served counts. A chunk past the end, or a path that does not exist, must
// not mark anything read.
func TestRawGetFailureDoesNotCountAsRead(t *testing.T) {
	reg, _ := guardRegistry(t, map[string]*extract.Doc{"long.md": guardDoc("Long Source", 40000)})
	guardOpen(t, reg)
	path, _ := guardIngest(t, reg, "long.md")

	if r := guardCall(t, reg, "raw.get", fmt.Sprintf(`{"source":%q,"chunk":9}`, path)); !r.IsError {
		t.Fatalf("raw.get chunk 9 of 3 = %+v, want an error", r)
	}
	if r := guardCall(t, reg, "raw.get", `{"source":"raw/articles/nope.md","chunk":1}`); !r.IsError {
		t.Fatalf("raw.get of a missing source = %+v, want an error", r)
	}
	want := closeRefusalHead + path + " chunks 1, 2, 3 of 3 unread" + closeRefusalTail
	if r := guardCall(t, reg, "stage.close", `{}`); !r.IsError || r.Content != want {
		t.Errorf("close after failed reads = %+v, want the all-unread refusal %q", r, want)
	}
}

// TestRawGetDefaultChunkCountsAsChunkOne: raw.get without a chunk argument
// serves chunk 1, so that is the chunk it records.
func TestRawGetDefaultChunkCountsAsChunkOne(t *testing.T) {
	reg, _ := guardRegistry(t, map[string]*extract.Doc{"long.md": guardDoc("Long Source", 40000)})
	guardOpen(t, reg)
	path, _ := guardIngest(t, reg, "long.md")
	if r := guardCall(t, reg, "raw.get", fmt.Sprintf(`{"source":%q}`, path)); r.IsError {
		t.Fatalf("raw.get: %s", r.Content)
	}
	want := closeRefusalHead + path + " chunks 2, 3 of 3 unread" + closeRefusalTail
	if r := guardCall(t, reg, "stage.close", `{}`); !r.IsError || r.Content != want {
		t.Errorf("close = %+v, want %q", r, want)
	}
}

// TestStageCloseSingleChunkSourceNeedsOneRead: a one-chunk source is the
// common case; it must not be refused once it has been read, and is refused
// (with "chunks 1 of 1") if it never was.
func TestStageCloseSingleChunkSourceNeedsOneRead(t *testing.T) {
	reg, _ := guardRegistry(t, map[string]*extract.Doc{"short.md": guardDoc("Short Source", 100)})
	guardOpen(t, reg)
	path, n := guardIngest(t, reg, "short.md")
	if n != 1 {
		t.Fatalf("chunk count = %d, want 1", n)
	}
	want := closeRefusalHead + path + " chunks 1 of 1 unread" + closeRefusalTail
	if r := guardCall(t, reg, "stage.close", `{}`); !r.IsError || r.Content != want {
		t.Fatalf("close before reading = %+v, want %q", r, want)
	}
	guardRead(t, reg, path, 1)
	if r := guardCall(t, reg, "stage.close", `{}`); r.IsError || strings.Contains(r.Content, "unread chunks") {
		t.Errorf("close after reading the one chunk = %+v, want the plain summary", r)
	}
}

// TestStageCloseAfterConcurrentReads: tools may run concurrently within a
// round, so raw.get calls for different chunks race each other (and a close
// can land beside them). Under -race this proves the log is guarded through
// the real handlers, not just in the unit; once every read returned, the
// close must see all of them.
func TestStageCloseAfterConcurrentReads(t *testing.T) {
	reg, e := guardRegistry(t, map[string]*extract.Doc{"long.md": guardDoc("Long Source", 40000)})
	guardOpen(t, reg)
	path, n := guardIngest(t, reg, "long.md")

	var wg sync.WaitGroup
	for c := 1; c <= n; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := reg.Call(context.Background(), "raw.get", json.RawMessage(fmt.Sprintf(`{"source":%q,"chunk":%d}`, path, c)))
			if err != nil || r.IsError {
				t.Errorf("raw.get chunk %d: %+v, %v", c, r, err)
			}
		}()
	}
	wg.Wait()

	got := guardCall(t, reg, "stage.close", `{}`)
	if got.IsError {
		t.Fatalf("close after every chunk was read concurrently refused: %s", got.Content)
	}
	if want := currentSummary(t, e).Content; got.Content != want {
		t.Errorf("close content = %q, want the plain summary %q", got.Content, want)
	}
}
