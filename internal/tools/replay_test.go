package tools

// replay_test.go is 063 S1's frozen block for Registry.Replay. `lw tool` runs
// one OS process per tool call, so a registry that lives for one call has no
// memory of the 040 read log or the 043 shrink arms a long-lived one would hold
// after the calls before it. Replay rebuilds both from the changeset's own
// session — the live raw sources the changeset stages, and the tool records so
// far — so each test runs real calls through registry A, keeps its records the
// way session.ndjson does, builds a FRESH registry B over the same engine
// (another process), replays, and asserts B answers the next call exactly as A
// would have. Permanent regression tests (D-10C).

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/vault"
)

// rpRig drives registry A the way the in-process agent does and keeps each
// call as a PastCall, oldest first — what a session.ndjson tool record holds.
type rpRig struct {
	t    *testing.T
	reg  *Registry
	e    *stage.Engine
	past []PastCall
}

func (r *rpRig) call(name, args string) Result {
	r.t.Helper()
	res := callTool(r.t, r.reg, name, args)
	r.past = append(r.past, PastCall{Tool: name, Args: args, Result: res.Content})
	return res
}

// rpOpen opens the changeset directly on the engine: `lw ingest --prepare` has
// already opened it before any `lw tool` call, so stage.open is not part of the
// replayed history.
func rpOpen(t *testing.T, e *stage.Engine) {
	t.Helper()
	if _, _, err := e.OpenOrJoin("ingest sources", stage.Author{Kind: "agent", Model: "test"}); err != nil {
		t.Fatalf("OpenOrJoin: %v", err)
	}
}

// ingest stages uri through the rig and returns its path and chunk count, read
// from the result as the agent would.
func (r *rpRig) ingest(uri string) (string, int) {
	r.t.Helper()
	res := r.call("stage.ingest_source", `{"uri":"`+uri+`"}`)
	if res.IsError {
		r.t.Fatalf("stage.ingest_source %s: %s", uri, res.Content)
	}
	m := ingestResultRe.FindStringSubmatch(res.Content)
	if m == nil {
		r.t.Fatalf("ingest result names no path and chunk count: %q", res.Content)
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		r.t.Fatalf("chunk count %q: %v", m[2], err)
	}
	return m[1], n
}

// rpGuardRig is a rig over the minimal fixture with a changeset open and an
// extractor serving docs.
func rpGuardRig(t *testing.T, docs map[string]*extract.Doc) *rpRig {
	t.Helper()
	reg, e := guardRegistry(t, docs)
	rpOpen(t, e)
	return &rpRig{t: t, reg: reg, e: e}
}

// rpFresh is registry B: another process over the same engine, built the way A
// was and knowing nothing A knows.
func rpFresh(e *stage.Engine, ex extract.Extractor) *Registry {
	v := e.Vault()
	return NewRegistry(Deps{Vault: v, Index: index.Build(v), Engine: e, Extract: ex, Author: stage.Author{Kind: "agent", Model: "test"}})
}

// rpLive is the live list `lw tool` derives from the open changeset: every live
// ingest_source op's path and the chunk count raw.get serves for its staged body.
func rpLive(t *testing.T, e *stage.Engine) []LiveSource {
	t.Helper()
	cs, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	var out []LiveSource
	for _, op := range cs.Live() {
		if op.Kind != stage.OpIngestSource {
			continue
		}
		b, staged, err := e.StagedFile(op.Path)
		if err != nil || !staged {
			t.Fatalf("StagedFile(%s) = staged %v, err %v", op.Path, staged, err)
		}
		src, err := vault.ParseRawSource(op.Path, b)
		if err != nil {
			t.Fatalf("ParseRawSource(%s): %v", op.Path, err)
		}
		out = append(out, LiveSource{Path: op.Path, Chunks: RawChunkCount(src.Body)})
	}
	return out
}

// rpRead is the arguments of a raw.get for one chunk of path.
func rpRead(path string, chunk int) string {
	return fmt.Sprintf(`{"source":%q,"chunk":%d}`, path, chunk)
}

// --- 040: the read log ---------------------------------------------------

// TestReplayUnreadRefuseThenPass is the heart of the cross-process 040 guard.
// A ingests a two-chunk source, reads chunk 1 and is refused at the close. B,
// a different process, replays A's three calls and closes: A's second close
// would have gone through over the same unread set, saying so on its last line —
// so must B's, byte for byte. Without the refusal key B would refuse AGAIN, and
// the external agent's "close anyway" could never succeed across processes.
func TestReplayUnreadRefuseThenPass(t *testing.T) {
	docs := map[string]*extract.Doc{"long.md": guardDoc("Long Source", 20000)}
	rig := rpGuardRig(t, docs)
	path, n := rig.ingest("long.md")
	if n != 2 {
		t.Fatalf("source has %d chunk(s), want 2", n)
	}
	if r := rig.call("raw.get", rpRead(path, 1)); r.IsError {
		t.Fatalf("raw.get: %s", r.Content)
	}
	first := rig.call("stage.close", `{}`)
	if want := closeRefusalHead + path + " chunks 2 of 2 unread" + closeRefusalTail; !first.IsError || first.Content != want {
		t.Fatalf("A's first close = %+v, want the refusal %q", first, want)
	}
	if len(rig.past) != 3 { // ingest, raw.get, close: stage.open was the engine's, not a recorded call
		t.Fatalf("recorded %d calls, want 3", len(rig.past))
	}

	b := rpFresh(rig.e, guardExtractor{docs: docs})
	b.Replay(rpLive(t, rig.e), rig.past)
	got := callTool(t, b, "stage.close", `{}`)
	if got.IsError {
		t.Fatalf("B's close was refused although A had already refused this unread set: %s", got.Content)
	}
	if suffix := "\nunread chunks: " + path + " 2"; !strings.HasSuffix(got.Content, suffix) {
		t.Errorf("B's close does not end with %q:\n%s", suffix, got.Content)
	}
	// What A's own second close says is what B said.
	if again := rig.call("stage.close", `{}`); again.IsError || again.Content != got.Content {
		t.Errorf("A's second close = %+v\nB's close = %q", again, got.Content)
	}

	// Controls. Without the refusal in the history B reads the same chunks and
	// refuses afresh; and a registry that replayed nothing never heard of the
	// source, so it closes with no unread line at all.
	c := rpFresh(rig.e, guardExtractor{docs: docs})
	c.Replay(rpLive(t, rig.e), rig.past[:2])
	if r := callTool(t, c, "stage.close", `{}`); !r.IsError || r.Content != first.Content {
		t.Errorf("close after replaying the reads only = %+v, want the refusal %q", r, first.Content)
	}
	blind := rpFresh(rig.e, guardExtractor{docs: docs})
	if r := callTool(t, blind, "stage.close", `{}`); r.IsError || strings.Contains(r.Content, "unread chunks") {
		t.Errorf("a registry that replayed nothing closed with %+v", r)
	}
}

// TestReplayPassedCloseOverUnreadKeepsRefusal: the refusal is once per set, not
// once ever. A's second close went through over the refused set and left the
// refusal in the log (closeVerdict keeps the key when it lets a repeat by), so
// A's THIRD close goes through too (040: "a third close is still let through").
// B replays both closes and must say the same — clearing the key on a close that
// carried an unread line would refuse the agent a third time.
func TestReplayPassedCloseOverUnreadKeepsRefusal(t *testing.T) {
	docs := map[string]*extract.Doc{"long.md": guardDoc("Long Source", 20000)}
	rig := rpGuardRig(t, docs)
	path, _ := rig.ingest("long.md")
	rig.call("raw.get", rpRead(path, 1))
	if r := rig.call("stage.close", `{}`); !r.IsError {
		t.Fatalf("first close not refused: %s", r.Content)
	}
	if r := rig.call("stage.close", `{}`); r.IsError || !strings.Contains(r.Content, "\nunread chunks: ") {
		t.Fatalf("second close = %+v, want it to go through over the unread set", r)
	}
	third := rig.call("stage.close", `{}`) // A, long-lived: still let through
	if third.IsError {
		t.Fatalf("A's third close refused: %s", third.Content)
	}

	b := rpFresh(rig.e, guardExtractor{docs: docs})
	b.Replay(rpLive(t, rig.e), rig.past[:4]) // ingest, raw.get, refused close, passed close
	if got := callTool(t, b, "stage.close", `{}`); got.IsError || got.Content != third.Content {
		t.Errorf("B's close = %+v, want what A's third close said: %q", got, third.Content)
	}
}

// TestReplayReadsSatisfyClose: A read both chunks. B replays them and closes
// with the plain summary — no refusal, no unread line.
func TestReplayReadsSatisfyClose(t *testing.T) {
	docs := map[string]*extract.Doc{"long.md": guardDoc("Long Source", 20000)}
	rig := rpGuardRig(t, docs)
	path, _ := rig.ingest("long.md")
	rig.call("raw.get", rpRead(path, 2))
	rig.call("raw.get", rpRead(path, 1))

	b := rpFresh(rig.e, guardExtractor{docs: docs})
	b.Replay(rpLive(t, rig.e), rig.past)
	got := callTool(t, b, "stage.close", `{}`)
	if got.IsError {
		t.Fatalf("close after replayed full reads refused: %s", got.Content)
	}
	if want := currentSummary(t, rig.e).Content; got.Content != want {
		t.Errorf("close content =\n%q\nwant the plain summary\n%q", got.Content, want)
	}
}

// TestReplayRawGetDefaultChunkIsOne: a raw.get with no chunk argument served
// chunk 1, and the handler recorded it as chunk 1; Replay must do the same. The
// wire spelling of the tool name is accepted as well, for a history written by a
// client that used it.
func TestReplayRawGetDefaultChunkIsOne(t *testing.T) {
	docs := map[string]*extract.Doc{"short.md": guardDoc("Short Source", 100)}
	rig := rpGuardRig(t, docs)
	path, _ := rig.ingest("short.md")
	rig.call("raw.get", `{"source":"`+path+`"}`)

	for _, tool := range []string{"raw.get", "raw_get"} {
		past := append([]PastCall(nil), rig.past...)
		past[1].Tool = tool
		b := rpFresh(rig.e, guardExtractor{docs: docs})
		b.Replay(rpLive(t, rig.e), past)
		if r := callTool(t, b, "stage.close", `{}`); r.IsError || strings.Contains(r.Content, "unread chunks") {
			t.Errorf("%s with no chunk argument not counted as chunk 1: %+v", tool, r)
		}
	}
}

// TestReplayRefusedRawGetNotARead: only a chunk the model was served counts.
// The agent guesses the staged path before it stages the source (refused: not
// found), and later asks for a chunk past the end (refused: out of range).
// Neither is a read — B's close is refused with every chunk still unread. The
// first refusal is the one that bites: its source and chunk are in range, and a
// replay that counted every raw.get would mark chunk 1 of the live source read.
func TestReplayRefusedRawGetNotARead(t *testing.T) {
	docs := map[string]*extract.Doc{"long.md": guardDoc("Long Source", 20000)}
	rig := rpGuardRig(t, docs)
	const guess = "raw/articles/long-source.md"
	if r := rig.call("raw.get", rpRead(guess, 1)); !r.IsError {
		t.Fatalf("raw.get before staging served: %s", r.Content)
	}
	path, _ := rig.ingest("long.md")
	if path != guess {
		t.Fatalf("staged at %q, the test guessed %q", path, guess)
	}
	if r := rig.call("raw.get", rpRead(path, 5)); !r.IsError {
		t.Fatalf("raw.get chunk 5 of 2 served: %s", r.Content)
	}

	b := rpFresh(rig.e, guardExtractor{docs: docs})
	b.Replay(rpLive(t, rig.e), rig.past)
	want := closeRefusalHead + path + " chunks 1, 2 of 2 unread" + closeRefusalTail
	if r := callTool(t, b, "stage.close", `{}`); !r.IsError || r.Content != want {
		t.Errorf("close after replayed refused reads = %+v, want the all-unread refusal %q", r, want)
	}
}

// TestReplayDroppedSourceNotHeld: a source the changeset no longer stages — its
// op was dropped — is not in the live list, and B does not hold its close to it
// even though the history still holds the stage.ingest_source call that made it.
// A second, live source keeps the close honest: the refusal names that one only.
func TestReplayDroppedSourceNotHeld(t *testing.T) {
	docs := map[string]*extract.Doc{
		"long.md": guardDoc("Long Source", 20000),
		"zeta.md": guardDoc("Zeta Paper", 30000), // another body: the same one would be refused as a duplicate
	}
	rig := rpGuardRig(t, docs)
	dropped, _ := rig.ingest("long.md")
	kept, _ := rig.ingest("zeta.md")
	cs, err := rig.e.Current()
	if err != nil {
		t.Fatal(err)
	}
	var id string
	for _, op := range cs.Live() {
		if op.Path == dropped {
			id = op.ID
		}
	}
	if id == "" {
		t.Fatalf("no live op for %s", dropped)
	}
	if err := rig.e.DropOp(id); err != nil {
		t.Fatalf("DropOp: %v", err)
	}
	live := rpLive(t, rig.e)
	if len(live) != 1 || live[0].Path != kept {
		t.Fatalf("live = %+v, want only %s", live, kept)
	}

	b := rpFresh(rig.e, guardExtractor{docs: docs})
	b.Replay(live, rig.past)
	if _, ok := b.deps.reads.ingested[dropped]; ok {
		t.Errorf("Replay resurrected the dropped source %s from its stage.ingest_source record", dropped)
	}
	want := closeRefusalHead + kept + " chunks 1, 2 of 2 unread" + closeRefusalTail
	if r := callTool(t, b, "stage.close", `{}`); !r.IsError || r.Content != want {
		t.Errorf("close = %+v, want the refusal naming only %s: %q", r, kept, want)
	}

	// With nothing live at all the close is plain.
	solo := rpGuardRig(t, map[string]*extract.Doc{"long.md": guardDoc("Long Source", 20000)})
	solo.ingest("long.md")
	cs, err = solo.e.Current()
	if err != nil {
		t.Fatal(err)
	}
	if err := solo.e.DropOp(cs.Ops[0].ID); err != nil {
		t.Fatal(err)
	}
	c := rpFresh(solo.e, nil)
	c.Replay(rpLive(t, solo.e), solo.past)
	if len(c.deps.reads.ingested) != 0 {
		t.Errorf("Replay tracks %d source(s) with none live", len(c.deps.reads.ingested))
	}
	if r := callTool(t, c, "stage.close", `{}`); r.IsError {
		t.Errorf("close refused with no live source: %s", r.Content)
	}
}

// TestReplayCleanCloseForgetsRefusal: a close that went through over an EMPTY
// unread set clears the remembered refusal (closeVerdict does), so an identical
// unread set that recurs later is fresh again and is refused once more. A, in
// one process: refused with chunk 2 unread, then read it, then closed clean. B
// replays that history in order and must end where A's log is — nothing
// outstanding — not holding the stale key of the earlier refusal.
func TestReplayCleanCloseForgetsRefusal(t *testing.T) {
	docs := map[string]*extract.Doc{"long.md": guardDoc("Long Source", 20000)}
	rig := rpGuardRig(t, docs)
	path, _ := rig.ingest("long.md")
	rig.call("raw.get", rpRead(path, 1))
	if r := rig.call("stage.close", `{}`); !r.IsError { // refused: {2}
		t.Fatalf("first close not refused: %s", r.Content)
	}
	rig.call("raw.get", rpRead(path, 2))
	if r := rig.call("stage.close", `{}`); r.IsError || strings.Contains(r.Content, "unread chunks") {
		t.Fatalf("close after the full read = %+v, want a clean close", r)
	}

	// The same registry A, were it long-lived, would hold refused == "" now.
	b := rpFresh(rig.e, guardExtractor{docs: docs})
	b.Replay(rpLive(t, rig.e), rig.past)
	if got := b.deps.reads.refused; got != "" {
		t.Errorf("refused after a clean close = %q, want it cleared", got)
	}
	if want := rig.reg.deps.reads.refused; want != "" {
		t.Fatalf("fixture drift: A's own log still remembers %q", want)
	}
}

// TestReplayHoldsRecompileTarget: a 055 recompile target is a live source too —
// a committed raw, named by no ingest_source op, that the changeset never lists.
// `lw tool` hands it to Replay as a LiveSource; B must hold the close to it like
// any other, whether or not Deps.Recompile was also set on B.
func TestReplayHoldsRecompileTarget(t *testing.T) {
	e, _ := recompileFixture(t, 40000)
	a := recompileRegistry(e, recompileRawPath)
	rpOpen(t, e)
	rig := &rpRig{t: t, reg: a, e: e}
	rig.call("raw.get", rpRead(recompileRawPath, 1))
	first := rig.call("stage.close", `{}`)
	want := closeRefusalHead + recompileRawPath + " chunks 2, 3 of 3 unread" + closeRefusalTail
	if !first.IsError || first.Content != want {
		t.Fatalf("A's close = %+v, want %q", first, want)
	}
	live := []LiveSource{{Path: recompileRawPath, Chunks: RawChunkCount(guardBody(40000))}}

	for _, tc := range []struct {
		name string
		mk   func() *Registry
	}{
		{"B built with Deps.Recompile", func() *Registry { return recompileRegistry(e, recompileRawPath) }},
		{"B built without it", func() *Registry { return recompileRegistry(e) }},
	} {
		mk := tc.mk
		t.Run(tc.name, func(t *testing.T) {
			// Reads only: B refuses afresh over the target's unread chunks.
			b := mk()
			b.Replay(live, rig.past[:1])
			if r := callTool(t, b, "stage.close", `{}`); !r.IsError || r.Content != want {
				t.Fatalf("close after replaying the read = %+v, want the refusal %q", r, want)
			}
			// Reads and A's refusal: B lets the repeat through and says so.
			c := mk()
			c.Replay(live, rig.past)
			got := callTool(t, c, "stage.close", `{}`)
			if got.IsError {
				t.Fatalf("close after replaying the refusal was refused: %s", got.Content)
			}
			if suffix := "\nunread chunks: " + recompileRawPath + " 2, 3"; !strings.HasSuffix(got.Content, suffix) {
				t.Errorf("close does not end with %q:\n%s", suffix, got.Content)
			}
		})
	}
}

// TestReplayAcceptsWireSpellings: the session records the canonical dotted name,
// but a history written by a client that used the wire spelling ("stage_close")
// is read the same. The canonical names with an underscore in their leaf
// (stage.patch_page) are the ones a careless name mapping breaks, so both the
// close and the patch are driven through the wire spelling.
func TestReplayAcceptsWireSpellings(t *testing.T) {
	docs := map[string]*extract.Doc{"long.md": guardDoc("Long Source", 20000)}
	rig := rpGuardRig(t, docs)
	path, _ := rig.ingest("long.md")
	rig.call("raw.get", rpRead(path, 1))
	rig.call("stage.close", `{}`) // refused: {2}
	wire := append([]PastCall(nil), rig.past...)
	wire[1].Tool, wire[2].Tool = "raw_get", "stage_close"
	b := rpFresh(rig.e, guardExtractor{docs: docs})
	b.Replay(rpLive(t, rig.e), wire)
	if r := callTool(t, b, "stage.close", `{}`); r.IsError || !strings.HasSuffix(r.Content, "\nunread chunks: "+path+" 2") {
		t.Errorf("close after a wire-spelled history = %+v, want it to go through over the refused set", r)
	}

	tl := rpTilelang(t)
	if r := tl.call("stage.patch_page", op8Args(t, false)); !r.IsError {
		t.Fatalf("op8 accepted without allow_shrink:\n%s", r.Content)
	}
	tl.past[0].Tool = "stage_patch_page"
	c := rpFresh(tl.e, nil)
	c.Replay(nil, tl.past)
	if r := callTool(t, c, "stage.patch_page", op8Args(t, true)); r.IsError {
		t.Fatalf("confirmed repeat refused after a wire-spelled history: %s", r.Content)
	}
}

// --- 043: the shrink arms -------------------------------------------------

// rpTilelang is a rig over the session-36 ground page with the changeset open.
func rpTilelang(t *testing.T) *rpRig {
	t.Helper()
	reg, e, _ := tilelangRegistry(t)
	rpOpen(t, e)
	return &rpRig{t: t, reg: reg, e: e}
}

const rpTilelangPage = "wiki/entities/tilelang.md"

// rpSmallEdit is a replace_text that lands without shrinking the section.
const rpSmallEdit = `{"path":"wiki/entities/tilelang.md","section":"## GPU programming model","op":"replace_text","find":"recur in [[batch-invariant-deterministic-kernels]].","content":"recur in [[batch-invariant-deterministic-kernels]]. Tools like [[nvitop]] make the resulting utilization observable.","rationale":"link nvitop"}`

// rpNotUnique is a replace_text refused because its find occurs four times.
const rpNotUnique = `{"path":"wiki/entities/tilelang.md","section":"## GPU programming model","op":"replace_text","find":"^[raw/papers/tilelang-a-composable","content":"x","rationale":"probe"}`

// rpAppliedOp8 asserts the staged page is byte-identical to the live op8 output.
func rpAppliedOp8(t *testing.T, e *stage.Engine) {
	t.Helper()
	got, staged, err := e.StagedFile(rpTilelangPage)
	if err != nil || !staged {
		t.Fatalf("StagedFile: staged %v, err %v", staged, err)
	}
	want, err := os.ReadFile("testdata/section-edit/tilelang-after-op8.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatal("staged page is not byte-identical to the live op8 output")
	}
}

// TestReplayShrinkArmCrossesProcess: A's op8 call is refused (a >50 % cut of a
// 400+ byte section), which arms the path+section. B, another process, replays
// the refusal; the same call with allow_shrink now stages. Without Replay the
// flag could never succeed across processes: every call would re-refuse.
func TestReplayShrinkArmCrossesProcess(t *testing.T) {
	rig := rpTilelang(t)
	if r := rig.call("stage.patch_page", op8Args(t, false)); !r.IsError || !ShrinkRefusal(r.Content) {
		t.Fatalf("op8 not refused as a shrink: %+v", r)
	}
	b := rpFresh(rig.e, nil)
	b.Replay(nil, rig.past)
	if r := callTool(t, b, "stage.patch_page", op8Args(t, true)); r.IsError {
		t.Fatalf("confirmed repeat refused in the new process: %s", r.Content)
	}
	rpAppliedOp8(t, rig.e)
}

// TestReplayShrinkConfirmLineRefusalArms: the refusal a pre-emptive allow_shrink
// gets (043 T2: the last line says the flag only counts now) arms the key too,
// so the identical repeat applies — in a replayed process as in a long-lived one.
func TestReplayShrinkConfirmLineRefusalArms(t *testing.T) {
	rig := rpTilelang(t)
	r := rig.call("stage.patch_page", op8Args(t, true))
	if !r.IsError {
		t.Fatalf("pre-emptive allow_shrink bypassed the guard:\n%s", r.Content)
	}
	assertLastLine(t, r.Content, shrinkConfirmLastLine)
	b := rpFresh(rig.e, nil)
	b.Replay(nil, rig.past)
	if r := callTool(t, b, "stage.patch_page", op8Args(t, true)); r.IsError {
		t.Fatalf("repeat after the confirm-line refusal refused: %s", r.Content)
	}
	rpAppliedOp8(t, rig.e)
}

// TestReplayShrinkNotArmedAfterSuccess: 043 T3 across processes. The refusal
// armed the key, then a small edit of the same path+section staged; staging
// disarms, so B's flagged call is refused again with the confirm line.
func TestReplayShrinkNotArmedAfterSuccess(t *testing.T) {
	rig := rpTilelang(t)
	if r := rig.call("stage.patch_page", op8Args(t, false)); !r.IsError {
		t.Fatalf("op8 accepted without allow_shrink:\n%s", r.Content)
	}
	if r := rig.call("stage.patch_page", rpSmallEdit); r.IsError {
		t.Fatalf("small edit refused: %s", r.Content)
	}
	b := rpFresh(rig.e, nil)
	b.Replay(nil, rig.past)
	r := callTool(t, b, "stage.patch_page", op8Args(t, true))
	if !r.IsError {
		t.Fatalf("a stale arm survived a staged edit of the same section:\n%s", r.Content)
	}
	assertLastLine(t, r.Content, shrinkConfirmLastLine)
	// Control: A, long-lived, answers the same call the same way.
	if a := callTool(t, rig.reg, "stage.patch_page", op8Args(t, true)); !a.IsError || a.Content != r.Content {
		t.Errorf("A's answer = %+v, B's = %q", a, r.Content)
	}
}

// TestReplayShrinkOtherRefusalKeepsArm: a refusal that is not a shrink — here a
// find that occurs four times — touches no arm. The shrink refusal before it
// still stands, so B confirms it.
func TestReplayShrinkOtherRefusalKeepsArm(t *testing.T) {
	rig := rpTilelang(t)
	if r := rig.call("stage.patch_page", op8Args(t, false)); !r.IsError {
		t.Fatalf("op8 accepted without allow_shrink:\n%s", r.Content)
	}
	if r := rig.call("stage.patch_page", rpNotUnique); !r.IsError || ShrinkRefusal(r.Content) {
		t.Fatalf("not-unique probe = %+v, want a non-shrink refusal", r)
	}
	b := rpFresh(rig.e, nil)
	b.Replay(nil, rig.past)
	if r := callTool(t, b, "stage.patch_page", op8Args(t, true)); r.IsError {
		t.Fatalf("a later non-shrink refusal disarmed the key: %s", r.Content)
	}
	rpAppliedOp8(t, rig.e)
}

// TestReplayNonShrinkRefusalDoesNotArm: the converse. A history holding only a
// non-shrink refusal arms nothing, so B's pre-emptive allow_shrink is refused
// with the confirm line, as a registry that never saw a shrink refusal would.
func TestReplayNonShrinkRefusalDoesNotArm(t *testing.T) {
	rig := rpTilelang(t)
	if r := rig.call("stage.patch_page", rpNotUnique); !r.IsError {
		t.Fatalf("not-unique probe accepted: %s", r.Content)
	}
	b := rpFresh(rig.e, nil)
	b.Replay(nil, rig.past)
	r := callTool(t, b, "stage.patch_page", op8Args(t, true))
	if !r.IsError {
		t.Fatalf("a non-shrink refusal armed the key:\n%s", r.Content)
	}
	assertLastLine(t, r.Content, shrinkConfirmLastLine)
}

// TestReplayShrinkArmScopedToSection: the arm is path+section, so a refusal of
// "## GPU programming model" does not confirm a shrink of "## Abstract".
func TestReplayShrinkArmScopedToSection(t *testing.T) {
	rig := rpTilelang(t)
	if r := rig.call("stage.patch_page", op8Args(t, false)); !r.IsError {
		t.Fatalf("op8 accepted without allow_shrink:\n%s", r.Content)
	}
	b := rpFresh(rig.e, nil)
	b.Replay(nil, rig.past)
	r := callTool(t, b, "stage.patch_page", `{"path":"wiki/entities/tilelang.md","section":"## Abstract","op":"replace_section","content":"x","allow_shrink":true,"rationale":"condense the abstract"}`)
	if !r.IsError {
		t.Fatalf("a shrink of another section was confirmed by the first section's arm:\n%s", r.Content)
	}
	assertLastLine(t, r.Content, shrinkConfirmLastLine)
}

// TestNoReplayKeepsRestartRule: A-063-1's other half. A registry that never calls
// Replay — the TUI's, an MCP server's, an in-process ingest's, `lw query`'s — is
// exactly what it was: a fresh one refuses the flagged call, because a shrink
// arm must not survive a restart (043), and the long-lived registry that armed
// it still confirms. Replay is the one door, and it is opt-in.
func TestNoReplayKeepsRestartRule(t *testing.T) {
	rig := rpTilelang(t)
	if r := rig.call("stage.patch_page", op8Args(t, false)); !r.IsError {
		t.Fatalf("op8 accepted without allow_shrink:\n%s", r.Content)
	}
	fresh := rpFresh(rig.e, nil)
	r := callTool(t, fresh, "stage.patch_page", op8Args(t, true))
	if !r.IsError {
		t.Fatalf("a fresh registry confirmed a shrink armed in another one:\n%s", r.Content)
	}
	assertLastLine(t, r.Content, shrinkConfirmLastLine)
	assertNothingStaged(t, rig.e)

	if r := callTool(t, rig.reg, "stage.patch_page", op8Args(t, true)); r.IsError {
		t.Fatalf("the registry that armed the key no longer confirms it: %s", r.Content)
	}
}

// TestReplayStateBelongsToItsRegistry: what Replay restores lands on that
// registry alone. A third registry built afterwards starts blind in both the
// read log and the arms — nothing is shared through package state.
func TestReplayStateBelongsToItsRegistry(t *testing.T) {
	rig := rpTilelang(t)
	if r := rig.call("stage.patch_page", op8Args(t, false)); !r.IsError {
		t.Fatalf("op8 accepted without allow_shrink:\n%s", r.Content)
	}
	b := rpFresh(rig.e, nil)
	b.Replay([]LiveSource{{Path: "raw/articles/x.md", Chunks: 3}}, rig.past)
	c := rpFresh(rig.e, nil)
	if r := callTool(t, c, "stage.patch_page", op8Args(t, true)); !r.IsError {
		t.Fatalf("a registry built after B's Replay inherited its shrink arm: %s", r.Content)
	}
	if len(c.deps.reads.ingested) != 0 {
		t.Errorf("a registry built after B's Replay inherited %d logged source(s)", len(c.deps.reads.ingested))
	}
	if c.deps.arms == b.deps.arms || c.deps.reads == b.deps.reads {
		t.Errorf("two registries share replayed state")
	}
}

// TestReplayWritesNothing: Replay never calls the engine and never writes —
// the vault tree and the whole .llmwiki directory are byte-identical across it,
// and the open changeset is the same. Records it cannot read (malformed
// arguments, an unknown tool, a result of no known shape) are skipped, not
// fatal.
func TestReplayWritesNothing(t *testing.T) {
	rig := rpTilelang(t)
	if r := rig.call("stage.patch_page", op8Args(t, false)); !r.IsError {
		t.Fatalf("op8 accepted without allow_shrink:\n%s", r.Content)
	}
	root := rig.e.Vault().Root()
	before := rpTree(t, root)
	csBefore, err := rig.e.Current()
	if err != nil {
		t.Fatal(err)
	}

	past := append([]PastCall(nil), rig.past...)
	past = append(past,
		PastCall{Tool: "raw.get", Args: `{not json`, Result: "chunk 1 of 1\n\nbody"},
		PastCall{Tool: "stage.patch_page", Args: ``, Result: "whatever"},
		PastCall{Tool: "stage.close", Args: ``, Result: ""},
		PastCall{Tool: "no.such.tool", Args: `{}`, Result: "chunk 1 of 1\n\nbody"},
		PastCall{Tool: "wiki.search", Args: `{"query":"x"}`, Result: "[]"},
	)
	b := rpFresh(rig.e, nil)
	b.Replay([]LiveSource{{Path: "raw/articles/x.md", Chunks: 2}}, past)
	b.Replay(nil, nil) // an empty replay is a no-op

	if after := rpTree(t, root); !reflect.DeepEqual(before, after) {
		t.Errorf("Replay changed the vault tree")
	}
	csAfter, err := rig.e.Current()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(csBefore, csAfter) {
		t.Errorf("Replay changed the open changeset")
	}
}

// rpTree reads every file under root (the .llmwiki directory included) into a
// map keyed by slash-separated path.
func rpTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		out[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
