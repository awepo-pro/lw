package agent

// tee_test.go is 057 FIX2 R2's frozen block: the audit trail of a turn that
// stages is written WHILE the turn runs, not copied in when it ends. S1
// adopted the turn at its end (AdoptTurn), so a crash, a quit, or a Review
// commit in the middle of a turn left a changeset with live ops and no
// session.ndjson — design D9, "a session is bound to a changeset and travels
// with it", broken exactly when the trail matters. The ask store now TEEs: from
// the turn's first successful staging call every record appended to the
// in-memory conversation is also appended to the changeset's file session.
// Permanent regression tests (D-10C); real engine, real stores, scripted model.

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/logging"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/trace"
)

// sessionAt decodes <state>/<id>/session.ndjson; ok is false when it does not
// exist.
func (f *lazyFixture) sessionAt(t *testing.T, state, id string) (recs []Record, ok bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.root, ".llmwiki", "changesets", state, id, "session.ndjson"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false
		}
		t.Fatalf("read session: %v", err)
	}
	recs, err = decodeRecords(b)
	if err != nil {
		t.Fatalf("decode session: %v", err)
	}
	return recs, true
}

// sendMsg runs one turn of lazySession with an explicit user message.
func (f *lazyFixture) sendMsg(t *testing.T, ctx context.Context, verb, msg string) error {
	t.Helper()
	out := make(chan Event, 64)
	err := f.loop.Send(trace.WithVerb(ctx, verb), lazySession, msg, out)
	drain(out)
	return err
}

// withHook swaps f's loop for one that calls before(n) at the start of the n'th
// Stream call, across every turn of the fixture.
func (f *lazyFixture) withHook(before func(call int)) {
	f.loop = newLoop(&hookStreamer{inner: f.fake, before: before}, f.reg, f.store, f.engine, f.loop.cfg)
}

// openChangeset returns the id of the one open changeset.
func (f *lazyFixture) openID(t *testing.T) string {
	t.Helper()
	cs, err := f.engine.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	return cs.ID
}

// TestTeeMidTurnCommitKeepsTheFullTrail: Review commits the changeset after the
// turn's first staged op, while the model is still working. The turn's records
// written after the commit land in committed/<id>/session.ndjson too — the
// trail is the whole turn, ahead of it the conversation's earlier prose.
func TestTeeMidTurnCommitKeepsTheFullTrail(t *testing.T) {
	f := newLazyFixture(t, [][]llm.Chunk{
		{{Text: "A1 ^[raw/papers/leviathan-2023.md]"}, {Finish: "stop"}}, // turn 1, read-only
		{toolCallChunk("call-1", "stage_create_page", wireCreatePageArgs), {Finish: "tool_calls"}},
		{{Text: "Filed."}, {Finish: "stop"}}, // turn 2, round 2: after the commit
	})
	var csID string
	f.withHook(func(call int) {
		if call == 3 {
			csID = f.openID(t)
			if _, err := f.engine.Commit("review commit mid-turn"); err != nil {
				t.Errorf("Commit: %v", err)
			}
		}
	})
	if err := f.sendMsg(t, WithOpenIntent(context.Background(), "ask: q1"), trace.VerbAsk, "q1"); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if err := f.sendMsg(t, WithOpenIntent(context.Background(), "ask: file it"), trace.VerbFile, "file it"); err != nil {
		t.Fatalf("turn 2: %v", err)
	}

	if csID == "" {
		t.Fatal("the hook never ran")
	}
	if _, ok := f.sessionAt(t, "open", csID); ok {
		t.Fatalf("open/%s still exists after the commit", csID)
	}
	recs, ok := f.sessionAt(t, "committed", csID)
	if !ok {
		t.Fatalf("committed/%s has no session.ndjson: the mid-turn commit lost the trail", csID)
	}
	want := []brief{
		{Role: "user", Content: "q1", Carried: true},
		{Role: "assistant", Content: "A1 ^[raw/papers/leviathan-2023.md]", Carried: true},
		{Role: "user", Content: "file it"},
		{Role: "tool", Tool: "stage.create_page", Staged: true},
		{Role: "assistant", Content: "Filed."}, // written AFTER the commit
	}
	if got := briefs(recs); !reflect.DeepEqual(got, want) {
		t.Fatalf("committed session:\n got  %+v\n want %+v", got, want)
	}
}

// TestTeeKilledTurnLeavesTheTrail: the turn is killed (the TUI quits, the
// process dies) right after its first staged op. The session.ndjson is already
// in the open changeset, holding the user record and the staging call.
func TestTeeKilledTurnLeavesTheTrail(t *testing.T) {
	f := newLazyFixture(t, [][]llm.Chunk{
		{toolCallChunk("call-1", "stage_create_page", wireCreatePageArgs), {Finish: "tool_calls"}},
		{{Text: "never reached"}, {Finish: "stop"}},
	})
	ctx, cancel := context.WithCancel(WithOpenIntent(context.Background(), "ask: file it"))
	defer cancel()
	f.withHook(func(call int) {
		if call == 2 {
			cancel() // killed after the staging call
		}
	})

	_ = f.sendMsg(t, ctx, trace.VerbFile, "file it")

	csID := f.openID(t)
	recs, ok := f.sessionAt(t, "open", csID)
	if !ok {
		t.Fatalf("open/%s has no session.ndjson after a turn killed past its first staged op", csID)
	}
	want := []brief{
		{Role: "user", Content: "file it"},
		{Role: "tool", Tool: "stage.create_page", Staged: true},
	}
	if got := briefs(recs); !reflect.DeepEqual(got, want) {
		t.Fatalf("session after the kill:\n got  %+v\n want %+v", got, want)
	}
}

// TestTeeWritesTheTrailWhileTheTurnRuns: the file exists between the staging
// call and the end of the turn — read at the start of the next round, with the
// model still working.
func TestTeeWritesTheTrailWhileTheTurnRuns(t *testing.T) {
	f := newLazyFixture(t, [][]llm.Chunk{
		{toolCallChunk("call-1", "stage_create_page", wireCreatePageArgs), {Finish: "tool_calls"}},
		{{Text: "Filed."}, {Finish: "stop"}},
	})
	var mid []Record
	var midOK bool
	f.withHook(func(call int) {
		if call == 2 {
			mid, midOK = f.sessionAt(t, "open", f.openID(t))
		}
	})

	if err := f.sendMsg(t, WithOpenIntent(context.Background(), "ask: file it"), trace.VerbFile, "file it"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if !midOK {
		t.Fatal("no session.ndjson while the turn was still running")
	}
	if got := briefs(mid); !reflect.DeepEqual(got, []brief{
		{Role: "user", Content: "file it"},
		{Role: "tool", Tool: "stage.create_page", Staged: true},
	}) {
		t.Fatalf("session while the turn ran: %+v", got)
	}
	// The closing answer joined it afterwards.
	final, _ := f.sessionAt(t, "open", f.openID(t))
	if len(final) != 3 || final[2].Content != "Filed." {
		t.Fatalf("final session: %+v", briefs(final))
	}
}

// TestTeeIntoAPreExistingChangeset: the turn stages into a changeset an `lw
// ingest` opened and gave a session. The turn's records follow the ingest's, as
// they happen, with nothing carried into someone else's transcript.
func TestTeeIntoAPreExistingChangeset(t *testing.T) {
	f := newLazyFixture(t, [][]llm.Chunk{
		{{Text: "A1"}, {Finish: "stop"}}, // turn 1, read-only: earlier prose exists
		{toolCallChunk("call-1", "stage_create_page", wireCreatePageArgs), {Finish: "tool_calls"}},
		{{Text: "Filed."}, {Finish: "stop"}},
	})
	opened, err := f.engine.OpenChangeset("lw ingest a.md", stage.Author{Kind: "agent"})
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	file := NewFileSessions(f.root)
	if _, err := file.Create(opened.ID); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := file.Append(opened.ID, Record{Role: "user", Content: "ingest a.md"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	var mid []Record
	f.withHook(func(call int) {
		if call == 3 {
			mid, _ = f.sessionAt(t, "open", opened.ID)
		}
	})

	if err := f.sendMsg(t, WithOpenIntent(context.Background(), "ask: q1"), trace.VerbAsk, "q1"); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if err := f.sendMsg(t, WithOpenIntent(context.Background(), "ask: file it"), trace.VerbFile, "file it"); err != nil {
		t.Fatalf("turn 2: %v", err)
	}

	if got := briefs(mid); len(got) != 3 || got[2].Tool != "stage.create_page" {
		t.Fatalf("the staging call was not in the session while the turn ran: %+v", got)
	}
	final, _ := f.sessionAt(t, "open", opened.ID)
	want := []brief{
		{Role: "user", Content: "ingest a.md"},
		{Role: "user", Content: "file it"},
		{Role: "tool", Tool: "stage.create_page", Staged: true},
		{Role: "assistant", Content: "Filed."},
	}
	if got := briefs(final); !reflect.DeepEqual(got, want) {
		t.Fatalf("session:\n got  %+v\n want %+v", got, want)
	}
}

// TestTeeEndsWithTheTurn: a later, read-only turn of the same conversation does
// not leak into the changeset the earlier turn staged into.
func TestTeeEndsWithTheTurn(t *testing.T) {
	f := newLazyFixture(t, [][]llm.Chunk{
		{toolCallChunk("call-1", "stage_create_page", wireCreatePageArgs), {Finish: "tool_calls"}},
		{{Text: "Filed."}, {Finish: "stop"}},
		{{Text: "A second answer."}, {Finish: "stop"}}, // turn 2, read-only
	})
	if err := f.sendMsg(t, WithOpenIntent(context.Background(), "ask: file it"), trace.VerbFile, "file it"); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	csID := f.openID(t)
	before, _ := f.sessionAt(t, "open", csID)

	if err := f.sendMsg(t, WithOpenIntent(context.Background(), "ask: and then?"), trace.VerbAsk, "and then?"); err != nil {
		t.Fatalf("turn 2: %v", err)
	}

	after, _ := f.sessionAt(t, "open", csID)
	if !reflect.DeepEqual(briefs(before), briefs(after)) {
		t.Fatalf("a later read-only turn wrote into the changeset's session:\n before %+v\n after  %+v", briefs(before), briefs(after))
	}
}

// TestTeeReadOnlyTurnWritesNoFile: nothing under .llmwiki changes for a turn
// that only reads — the headline of 057 survives the tee.
func TestTeeReadOnlyTurnWritesNoFile(t *testing.T) {
	f := newLazyFixture(t, [][]llm.Chunk{
		{toolCallChunk("call-1", "wiki_search", `{"q":"kv cache"}`), {Finish: "tool_calls"}},
		{{Text: "The kv cache memoizes keys and values."}, {Finish: "stop"}},
	})
	state := filepath.Join(f.root, ".llmwiki")
	before := treeSnapshot(t, state)

	if err := f.sendMsg(t, WithOpenIntent(context.Background(), "ask: what is a kv cache?"), trace.VerbAsk, "what is a kv cache?"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if after := treeSnapshot(t, state); !reflect.DeepEqual(before, after) {
		t.Fatalf("a read-only turn changed the disk:\n before %v\n after  %v", before, after)
	}
}

// TestTeeNotForPlainStores: a loop over a plain file store (`lw ingest`, `lint
// --fix`) has no tee and writes each record once.
func TestTeeNotForPlainStores(t *testing.T) {
	l, fx, _ := newTestLoop(t, [][]llm.Chunk{
		{toolCallChunk("call-1", "stage_create_page", wireCreatePageArgs), {Finish: "tool_calls"}},
		{{Text: "Staged."}, {Finish: "stop"}},
	}, LoopConfig{})
	out := make(chan Event, 64)
	if err := l.Send(WithOpenIntent(context.Background(), "ask: x"), fx.csID, "stage it", out); err != nil {
		t.Fatalf("Send: %v", err)
	}
	drain(out)

	sess, err := fx.store.Get(fx.csID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := []brief{
		{Role: "user", Content: "stage it"},
		{Role: "tool", Tool: "stage.create_page", Staged: true},
		{Role: "assistant", Content: "Staged."},
	}
	if got := briefs(sess.Records); !reflect.DeepEqual(got, want) {
		t.Fatalf("records:\n got  %+v\n want %+v", got, want)
	}
}

// TestAdoptTurnSkippedWhenTeed: the end-of-turn copy is only a fallback for a
// turn nothing teed; for a teed turn it appends nothing, so the trail is not
// written twice.
func TestAdoptTurnSkippedWhenTeed(t *testing.T) {
	f := newLazyFixture(t, [][]llm.Chunk{
		{toolCallChunk("call-1", "stage_create_page", wireCreatePageArgs), {Finish: "tool_calls"}},
		{{Text: "Filed."}, {Finish: "stop"}},
	})
	ctx := logging.WithTurn(WithOpenIntent(context.Background(), "ask: file it"), "turn-tee")
	if err := f.sendMsg(t, ctx, trace.VerbFile, "file it"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	csID := f.openID(t)
	before, _ := f.sessionAt(t, "open", csID)

	n, err := AdoptTurn(f.store, lazySession, csID, "turn-tee")
	if err != nil || n != 0 {
		t.Fatalf("AdoptTurn after a teed turn = %d, %v; want 0, nil", n, err)
	}
	after, _ := f.sessionAt(t, "open", csID)
	if len(after) != len(before) {
		t.Fatalf("AdoptTurn duplicated the trail: %d records -> %d", len(before), len(after))
	}
}

// TestTeeGoneChangesetIsANoOp: a tee asked for a changeset that has left
// changesets/open/ attaches nothing and, like the adopt step, creates no
// directory (C-118).
func TestTeeGoneChangesetIsANoOp(t *testing.T) {
	f := newLazyFixture(t, nil)
	tee, ok := f.store.(turnTee)
	if !ok {
		t.Fatal("the ask store has no tee capability")
	}
	state := filepath.Join(f.root, ".llmwiki")
	before := treeSnapshot(t, state)

	attached, err := tee.beginTee(lazySession, "cs-gone", "turn-x")
	if err != nil || attached {
		t.Fatalf("beginTee on a gone changeset = %v, %v; want false, nil", attached, err)
	}
	if after := treeSnapshot(t, state); !reflect.DeepEqual(before, after) {
		t.Fatalf("beginTee touched the disk:\n before %v\n after  %v", before, after)
	}
	if err := f.store.Append(lazySession, Record{Role: "user", Content: "q", Turn: "turn-x"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if strings.Contains(strings.Join(treeSnapshot(t, state), " "), "cs-gone") {
		t.Fatal("an Append after a failed tee created cs-gone")
	}
}
