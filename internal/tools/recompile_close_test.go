package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// 055: `lw ingest --recompile` writes pages from a raw source the vault
// already holds, so no stage.ingest_source runs and the 040 read log never
// hears of the source — a recompile turn could skim a committed raw and
// stage.close would summarize in silence, the exact failure 040 exists to
// stop. Deps.Recompile names the committed raws a turn compiles; the registry
// seeds its read log with them and stage.close holds them to the same rule.

// recompileRawPath is where recompileFixture commits its raw source.
const recompileRawPath = "raw/articles/recompiled-long.md"

// recompileFixture copies the minimal vault, commits one raw source of the
// given body size at recompileRawPath (a file on disk, which is what
// "committed" means to the vault), and opens an engine over it.
func recompileFixture(t *testing.T, runes int) (*stage.Engine, *vault.Vault) {
	t.Helper()
	dir := testutil.CopyFixture(t, "minimal")
	body := guardBody(runes)
	ingested, err := vault.ParseDate("2026-10-08")
	if err != nil {
		t.Fatal(err)
	}
	content := (&vault.RawSource{
		SourceURL: "https://example.test/recompiled-long",
		Ingested:  ingested,
		SHA256:    vault.BodySHA256(body),
		Body:      body,
	}).Serialize()
	full := filepath.Join(dir, filepath.FromSlash(recompileRawPath))
	if err := os.WriteFile(full, content, 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := stage.OpenEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e, e.Vault()
}

// recompileRegistry builds a registry over the fixture's engine with the given
// Deps.Recompile — a fresh registry, so a second call starts with a blind log.
func recompileRegistry(e *stage.Engine, recompile ...string) *Registry {
	v := e.Vault()
	return NewRegistry(Deps{
		Vault: v, Index: index.Build(v), Engine: e,
		Author:    stage.Author{Kind: "agent", Model: "test"},
		Recompile: recompile,
	})
}

var rawGetHeaderRe = regexp.MustCompile(`chunk (\d+) of (\d+)`)

// TestRecompileSeedMatchesRawGet: the chunk count the registry seeds for a
// recompile target is the total raw.get itself reports for that committed
// raw — for one, two and three chunk bodies — so the close guard counts reads
// against the denominator the model sees, not a re-derived one.
func TestRecompileSeedMatchesRawGet(t *testing.T) {
	for _, runes := range []int{100, 20000, 40000} {
		t.Run(fmt.Sprintf("%d_runes", runes), func(t *testing.T) {
			e, _ := recompileFixture(t, runes)
			reg := recompileRegistry(e, recompileRawPath)

			r := guardCall(t, reg, "raw.get", fmt.Sprintf(`{"source":%q,"chunk":1}`, recompileRawPath))
			if r.IsError {
				t.Fatalf("raw.get: %s", r.Content)
			}
			m := rawGetHeaderRe.FindStringSubmatch(r.Content)
			if m == nil {
				t.Fatalf("raw.get result carries no chunk header: %q", r.Content)
			}
			reported, _ := strconv.Atoi(m[2])
			seeded, ok := reg.deps.reads.ingested[recompileRawPath]
			if !ok {
				t.Fatalf("the read log was not seeded for %s; ingested = %v", recompileRawPath, reg.deps.reads.ingested)
			}
			if seeded != reported {
				t.Errorf("seeded chunk count = %d, raw.get reports %d", seeded, reported)
			}
			if want := RawChunkCount(guardBody(runes)); seeded != want {
				t.Errorf("seeded chunk count = %d, RawChunkCount = %d", seeded, want)
			}
		})
	}
}

// TestRecompileCloseGuardCoversRaw is the heart of 055's tool half: a
// committed raw named in Deps.Recompile is held to 040's rule — the first
// close over unread chunks is refused with the 040 text, a repeat of the same
// set goes through and says so, and a registry that read all three chunks
// closes with the plain summary. The changeset holds no ingest_source op at
// all: the guard must not depend on one.
func TestRecompileCloseGuardCoversRaw(t *testing.T) {
	e, _ := recompileFixture(t, 40000)
	reg := recompileRegistry(e, recompileRawPath)
	guardOpen(t, reg)
	if n := RawChunkCount(guardBody(40000)); n != 3 {
		t.Fatalf("fixture body has %d chunks, want 3", n)
	}
	guardRead(t, reg, recompileRawPath, 1)

	first := guardCall(t, reg, "stage.close", `{}`)
	want := closeRefusalHead + recompileRawPath + " chunks 2, 3 of 3 unread" + closeRefusalTail
	if !first.IsError || first.Content != want {
		t.Fatalf("first close = %+v, want the refusal\n%q", first, want)
	}
	if exact := unreadRefusal([]unreadSource{{Path: recompileRawPath, Chunks: []int{2, 3}, N: 3}}); first.Content != exact {
		t.Errorf("refusal = %q, want unreadRefusal's %q", first.Content, exact)
	}

	second := guardCall(t, reg, "stage.close", `{}`)
	if second.IsError {
		t.Fatalf("a second identical close was refused: %s", second.Content)
	}
	if suffix := "\nunread chunks: " + recompileRawPath + " 2, 3"; !strings.HasSuffix(second.Content, suffix) {
		t.Errorf("second close does not end with %q:\n%s", suffix, second.Content)
	}
	if base := currentSummary(t, e).Content; !strings.HasPrefix(second.Content, base) {
		t.Errorf("second close does not start with the plain summary:\n%s\nwant prefix\n%s", second.Content, base)
	}

	// A registry that read every chunk closes plainly. It is a fresh one: its
	// log is seeded again, so the reads below are the only reads it knows.
	fresh := recompileRegistry(e, recompileRawPath)
	for c := 1; c <= 3; c++ {
		guardRead(t, fresh, recompileRawPath, c)
	}
	got := guardCall(t, fresh, "stage.close", `{}`)
	if got.IsError {
		t.Fatalf("close after a full read refused: %s", got.Content)
	}
	if strings.Contains(got.Content, "unread chunks") {
		t.Errorf("close after a full read carries an unread line:\n%s", got.Content)
	}
	if want := currentSummary(t, e).Content; got.Content != want {
		t.Errorf("close content =\n%q\nwant the plain summary\n%q", got.Content, want)
	}
}

// TestNoRecompileCloseUnchanged: without Deps.Recompile a committed raw read
// partially is nobody's obligation — raw.get drops the read, stage.close
// summarizes as it always did. Every registry but the recompile turn's (the
// TUI, MCP, query, lint) is built this way.
func TestNoRecompileCloseUnchanged(t *testing.T) {
	e, _ := recompileFixture(t, 40000)
	reg := recompileRegistry(e)
	guardOpen(t, reg)
	guardRead(t, reg, recompileRawPath, 1)

	got := guardCall(t, reg, "stage.close", `{}`)
	if got.IsError {
		t.Fatalf("close refused for a committed raw nobody asked to recompile: %s", got.Content)
	}
	if want := currentSummary(t, e).Content; got.Content != want {
		t.Errorf("close content =\n%q\nwant the plain summary\n%q", got.Content, want)
	}
	if n := len(reg.deps.reads.ingested); n != 0 {
		t.Errorf("a registry without Recompile tracks %d source(s), want none", n)
	}
}

// TestRecompileSeedIgnoresUnknownPath: a path the vault does not hold has no
// body to count, so nothing is seeded for it and the close is not held to
// chunks that cannot exist; a Deps with no Vault at all builds all the same.
func TestRecompileSeedIgnoresUnknownPath(t *testing.T) {
	e, _ := recompileFixture(t, 100)
	reg := recompileRegistry(e, "raw/articles/not-committed.md")
	guardOpen(t, reg)
	if r := guardCall(t, reg, "stage.close", `{}`); r.IsError {
		t.Fatalf("close refused over a path the vault does not hold: %s", r.Content)
	}
	if n := len(reg.deps.reads.ingested); n != 0 {
		t.Errorf("seeded %d source(s) for an unknown path, want none", n)
	}

	blind := NewRegistry(Deps{Recompile: []string{recompileRawPath}}) // must not panic
	if n := len(blind.deps.reads.ingested); n != 0 {
		t.Errorf("a registry with no Vault seeded %d source(s), want none", n)
	}
}

// TestRecompileCloseKeepsTargetAcrossCloses: a recompile target is live for
// the whole turn, not only until the first close. closeVerdict prunes every
// logged source the live set does not hold, so the target must be in that set
// on every close — a close over a refused set goes through, a later read
// changes the set, and the raw is still there to be refused afresh.
func TestRecompileCloseKeepsTargetAcrossCloses(t *testing.T) {
	e, _ := recompileFixture(t, 40000)
	reg := recompileRegistry(e, recompileRawPath)
	guardOpen(t, reg)
	guardRead(t, reg, recompileRawPath, 2)

	first := guardCall(t, reg, "stage.close", `{}`)
	want := closeRefusalHead + recompileRawPath + " chunks 1, 3 of 3 unread" + closeRefusalTail
	if !first.IsError || first.Content != want {
		t.Fatalf("close = %+v, want the {1, 3} refusal", first)
	}
	// The refusal is remembered per set: the identical close goes through, and
	// the raw is still in the log afterwards (a close does not prune it).
	if second := guardCall(t, reg, "stage.close", `{}`); second.IsError {
		t.Fatalf("second close refused: %s", second.Content)
	}
	if _, ok := reg.deps.reads.ingested[recompileRawPath]; !ok {
		t.Error("the close pruned the recompile target from the read log")
	}
	args, _ := json.Marshal(map[string]any{"source": recompileRawPath, "chunk": 1})
	if r, err := reg.Call(context.Background(), "raw.get", args); err != nil || r.IsError {
		t.Fatalf("raw.get chunk 1: %+v, %v", r, err)
	}
	third := guardCall(t, reg, "stage.close", `{}`)
	wantThird := closeRefusalHead + recompileRawPath + " chunks 3 of 3 unread" + closeRefusalTail
	if !third.IsError || third.Content != wantThird {
		t.Errorf("close after reading chunk 1 = %+v, want the {3} refusal", third)
	}
}
