package agent

// asksessions_test.go is 057 D1/D4's frozen block: the TUI's ask conversation
// lives in memory (NewAskSessions), its history is exactly the prose 009's
// SeedSession used to copy, and a turn that staged is adopted into its
// changeset's session.ndjson without ever fabricating a changeset directory.
// Every file-backed store here is a real NewFileSessions over a temp vault, and
// the adopt tests run a real stage.Engine — no fake of either (D-10C: permanent
// regression tests).

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

// treeSnapshot lists every file and directory under root, slash-separated and
// sorted, with each file's size: two snapshots are equal exactly when nothing
// under root was created, removed or resized between them.
func treeSnapshot(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		entry := filepath.ToSlash(rel)
		if d.IsDir() {
			entry += "/"
		} else if info, ierr := d.Info(); ierr == nil {
			entry += " " + strconv.FormatInt(info.Size(), 10)
		}
		out = append(out, entry)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(out)
	return out
}

// askRecords is one ask conversation turn as the loop writes it: a question, a
// round that called a tool (the split shape: an assistant tool record, then its
// result), the answer, and a reasoning-only assistant record.
func askRecords(ts time.Time) []Record {
	return []Record{
		{TS: ts, Role: "user", Content: "what is a kv cache?", Turn: "t1"},
		{TS: ts.Add(1 * time.Second), Role: "assistant", Tool: "wiki.search", Args: `{"query":"kv cache"}`, Turn: "t1"},
		{TS: ts.Add(2 * time.Second), Role: "tool", Tool: "wiki.search", Args: `{"query":"kv cache"}`, Result: "kv-cache: a memoization technique", Turn: "t1"},
		{TS: ts.Add(3 * time.Second), Role: "assistant", Content: "It memoizes attention keys and values.", Reasoning: "short answer", Turn: "t1"},
		{TS: ts.Add(4 * time.Second), Role: "assistant", Reasoning: "thinking with nothing to say", Turn: "t1"},
	}
}

func TestAskSessionsMemoryOnly(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { e.Close() })

	store := NewAskSessions(NewFileSessions(root))
	state := filepath.Join(root, ".llmwiki")
	before := treeSnapshot(t, state)

	if _, err := store.Create("ask-x"); err != nil {
		t.Fatalf("Create(ask-x): %v", err)
	}
	if _, err := store.Create("ask-x"); !errors.Is(err, errSessionExists) {
		t.Fatalf("Create(ask-x) twice = %v, want errSessionExists", err)
	}
	for _, r := range askRecords(time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)) {
		if err := store.Append("ask-x", r); err != nil {
			t.Fatalf("Append(ask-x): %v", err)
		}
	}
	if _, err := store.Get("ask-x"); err != nil {
		t.Fatalf("Get(ask-x): %v", err)
	}
	if err := store.Append("ask-missing", Record{Role: "user", Content: "q"}); !errors.Is(err, errSessionNotFound) {
		t.Fatalf("Append(ask-missing) = %v, want errSessionNotFound", err)
	}
	if _, err := store.Get("ask-missing"); !errors.Is(err, errSessionNotFound) {
		t.Fatalf("Get(ask-missing) = %v, want errSessionNotFound", err)
	}

	if after := treeSnapshot(t, state); !reflect.DeepEqual(before, after) {
		t.Fatalf("an ask- session touched the disk:\n before %v\n after  %v", before, after)
	}

	// An ask conversation is not a changeset session: lw session is a
	// file-store CLI and must never list one.
	ids, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("List = %v, want no ask- id", ids)
	}

	// Every other id goes to the file store, as it always has: the session
	// appears beside its changeset.
	cs, err := e.OpenChangeset("a real changeset", stage.Author{Kind: "agent"})
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if _, err := store.Create(cs.ID); err != nil {
		t.Fatalf("Create(%s): %v", cs.ID, err)
	}
	path := filepath.Join(state, "changesets", "open", cs.ID, "session.ndjson")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a changeset session was not written beside its changeset: %v", err)
	}
	if err := store.Append(cs.ID, Record{TS: time.Now().UTC(), Role: "user", Content: "q"}); err != nil {
		t.Fatalf("Append(%s): %v", cs.ID, err)
	}
	onDisk, err := NewFileSessions(root).Get(cs.ID)
	if err != nil || len(onDisk.Records) != 1 || onDisk.Records[0].Content != "q" {
		t.Fatalf("the file store reads back %+v, %v; want the one appended record", onDisk, err)
	}
	if ids, _ := store.List(); !reflect.DeepEqual(ids, []string{cs.ID}) {
		t.Fatalf("List = %v, want exactly the changeset session [%s]", ids, cs.ID)
	}

	// Close drops an ask conversation from memory and nothing else.
	if err := store.Close("ask-x"); err != nil {
		t.Fatalf("Close(ask-x): %v", err)
	}
	if _, err := store.Get("ask-x"); !errors.Is(err, errSessionNotFound) {
		t.Fatalf("Get after Close = %v, want errSessionNotFound", err)
	}
}

func TestAskSessionsProseView(t *testing.T) {
	store := NewAskSessions(NewFileSessions(t.TempDir()))
	if _, err := store.Create("ask-p"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	recs := askRecords(time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC))
	for _, r := range recs {
		if err := store.Append("ask-p", r); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got, err := store.Get("ask-p")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := []Record{
		{TS: recs[0].TS, Role: "user", Content: "what is a kv cache?", Carried: true},
		{TS: recs[3].TS, Role: "assistant", Content: "It memoizes attention keys and values.", Carried: true},
	}
	if !reflect.DeepEqual(got.Records, want) {
		t.Fatalf("Get(ask-p).Records:\n got  %+v\n want %+v", got.Records, want)
	}
	if got.ID != "ask-p" || got.ChangesetID != "" {
		t.Fatalf("session = {ID %q ChangesetID %q}, want {ask-p, no changeset}", got.ID, got.ChangesetID)
	}

	// The view is a copy: a caller scribbling on it cannot change history.
	got.Records[0].Content = "scribble"
	again, _ := store.Get("ask-p")
	if again.Records[0].Content != "what is a kv cache?" {
		t.Fatalf("mutating a returned view changed the stored conversation: %+v", again.Records[0])
	}

	// RawRecords is the unfiltered record — tool calls included — which only
	// the adopt step reads.
	raw, err := RawRecords(store, "ask-p")
	if err != nil {
		t.Fatalf("RawRecords: %v", err)
	}
	if !reflect.DeepEqual(raw, recs) {
		t.Fatalf("RawRecords:\n got  %+v\n want %+v", raw, recs)
	}
}

// TestAskSessionsViewEqualsSeed is the 009 parity pin: for the same records,
// the in-memory conversation's history is exactly what SeedSession wrote into a
// fresh session — so a multi-turn ask sends the model the history it has always
// been sent.
func TestAskSessionsViewEqualsSeed(t *testing.T) {
	file := NewFileSessions(t.TempDir())
	ask := NewAskSessions(file)

	ts := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	recs := append(askRecords(ts),
		Record{TS: ts.Add(10 * time.Second), Role: "user", Content: "and flash attention?", Turn: "t2"},
		Record{TS: ts.Add(11 * time.Second), Role: "assistant", Content: "A kernel design.", Turn: "t2", Finish: "stop"},
		Record{TS: ts.Add(12 * time.Second), Role: "assistant", Tool: "stage.create_page", Args: `{}`, Staged: true, Turn: "t2"},
	)

	for _, id := range []string{"cs-from", "cs-to"} {
		if _, err := file.Create(id); err != nil {
			t.Fatalf("Create(%s): %v", id, err)
		}
	}
	if _, err := ask.Create("ask-seed"); err != nil {
		t.Fatalf("Create(ask-seed): %v", err)
	}
	for _, r := range recs {
		if err := ask.Append("ask-seed", r); err != nil {
			t.Fatalf("ask Append: %v", err)
		}
		if err := file.Append("cs-from", r); err != nil {
			t.Fatalf("file Append: %v", err)
		}
	}
	n, err := SeedSession(file, "cs-from", "cs-to")
	if err != nil {
		t.Fatalf("SeedSession: %v", err)
	}
	seeded, err := file.Get("cs-to")
	if err != nil {
		t.Fatalf("Get(cs-to): %v", err)
	}
	view, err := ask.Get("ask-seed")
	if err != nil {
		t.Fatalf("Get(ask-seed): %v", err)
	}
	if n != 4 || len(seeded.Records) != 4 {
		t.Fatalf("SeedSession carried %d records (%d on disk), want the 4 prose records", n, len(seeded.Records))
	}
	if !reflect.DeepEqual(view.Records, seeded.Records) {
		t.Fatalf("the ask view is not SeedSession's transform:\n view   %+v\n seeded %+v", view.Records, seeded.Records)
	}
}

// adoptFixture is a real engine, the file store over its vault and the ask
// store above it: the shape D4's adopt step runs in.
type adoptFixture struct {
	root   string
	engine *stage.Engine
	file   SessionStore
	ask    SessionStore
}

func newAdoptFixture(t *testing.T) *adoptFixture {
	t.Helper()
	root := testutil.CopyFixture(t, "minimal")
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	file := NewFileSessions(root)
	return &adoptFixture{root: root, engine: e, file: file, ask: NewAskSessions(file)}
}

// conversation fills ask-conv with two finished read-only turns (t1 with a tool
// round, t2) and a third, t3, whose records are the ones to adopt.
func (f *adoptFixture) conversation(t *testing.T) {
	t.Helper()
	if _, err := f.ask.Create("ask-conv"); err != nil {
		t.Fatalf("Create(ask-conv): %v", err)
	}
	ts := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	for _, r := range []Record{
		{TS: ts, Role: "user", Content: "q1", Turn: "t1"},
		{TS: ts.Add(1 * time.Second), Role: "assistant", Tool: "wiki.search", Args: `{"query":"q1"}`, Turn: "t1"},
		{TS: ts.Add(2 * time.Second), Role: "tool", Tool: "wiki.search", Args: `{"query":"q1"}`, Result: "hit", Turn: "t1"},
		{TS: ts.Add(3 * time.Second), Role: "assistant", Content: "A1", Turn: "t1"},
		{TS: ts.Add(10 * time.Second), Role: "user", Content: "q2", Turn: "t2"},
		{TS: ts.Add(11 * time.Second), Role: "assistant", Content: "A2", Turn: "t2"},
		{TS: ts.Add(20 * time.Second), Role: "user", Content: "file it", Turn: "t3"},
		{TS: ts.Add(21 * time.Second), Role: "tool", Tool: "stage.create_page", Args: `{"path":"p"}`, Result: "proposed op1", Staged: true, Turn: "t3"},
		{TS: ts.Add(22 * time.Second), Role: "assistant", Content: "Filed.", Turn: "t3"},
	} {
		if err := f.ask.Append("ask-conv", r); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
}

// brief is a record reduced to the fields these tests compare.
type brief struct {
	Role, Content, Tool string
	Carried, Staged     bool
}

func briefs(recs []Record) []brief {
	out := make([]brief, len(recs))
	for i, r := range recs {
		out[i] = brief{r.Role, r.Content, r.Tool, r.Carried, r.Staged}
	}
	return out
}

func TestAdoptTurnCreatesAndAppends(t *testing.T) {
	f := newAdoptFixture(t)
	f.conversation(t)
	cs, err := f.engine.OpenChangeset("ask: file it", stage.Author{Kind: "agent"})
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	// The changeset has no session yet: the lazy open made a changeset and
	// nothing else.
	if _, err := f.file.Get(cs.ID); !errors.Is(err, errSessionNotFound) {
		t.Fatalf("precondition: Get(%s) = %v, want no session yet", cs.ID, err)
	}

	n, err := AdoptTurn(f.ask, "ask-conv", cs.ID, "t3")
	if err != nil {
		t.Fatalf("AdoptTurn: %v", err)
	}

	got, err := f.file.Get(cs.ID)
	if err != nil {
		t.Fatalf("Get(%s): %v", cs.ID, err)
	}
	// The earlier turns' prose rides first, as Carried records (the shape a
	// seeded session had); then every raw record of this turn, tool records
	// included and not Carried.
	want := []brief{
		{Role: "user", Content: "q1", Carried: true},
		{Role: "assistant", Content: "A1", Carried: true},
		{Role: "user", Content: "q2", Carried: true},
		{Role: "assistant", Content: "A2", Carried: true},
		{Role: "user", Content: "file it"},
		{Role: "tool", Tool: "stage.create_page", Staged: true},
		{Role: "assistant", Content: "Filed."},
	}
	if n != len(want) {
		t.Fatalf("AdoptTurn returned %d, want %d", n, len(want))
	}
	if g := briefs(got.Records); !reflect.DeepEqual(g, want) {
		t.Fatalf("the adopted session:\n got  %+v\n want %+v", g, want)
	}
	// The turn's records keep their turn id, so lw log and the trace still join.
	for _, r := range got.Records[4:] {
		if r.Turn != "t3" {
			t.Fatalf("adopted record %+v lost its turn id", r)
		}
	}
	// The conversation itself is untouched.
	if raw, _ := RawRecords(f.ask, "ask-conv"); len(raw) != 9 {
		t.Fatalf("the conversation holds %d records after the adopt, want its 9", len(raw))
	}
}

func TestAdoptTurnExistingSession(t *testing.T) {
	f := newAdoptFixture(t)
	f.conversation(t)
	cs, err := f.engine.OpenChangeset("lw ingest a.md", stage.Author{Kind: "agent"})
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	// `lw ingest` created the changeset's session and wrote its own turn.
	if _, err := f.file.Create(cs.ID); err != nil {
		t.Fatalf("Create(%s): %v", cs.ID, err)
	}
	ingest := Record{TS: time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC), Role: "user", Content: "ingest a.md", Turn: "ti"}
	if err := f.file.Append(cs.ID, ingest); err != nil {
		t.Fatalf("Append: %v", err)
	}

	n, err := AdoptTurn(f.ask, "ask-conv", cs.ID, "t3")
	if err != nil {
		t.Fatalf("AdoptTurn: %v", err)
	}

	// Only this turn's records are appended: the earlier ask turns are not
	// carried into someone else's transcript.
	want := []brief{
		{Role: "user", Content: "ingest a.md"},
		{Role: "user", Content: "file it"},
		{Role: "tool", Tool: "stage.create_page", Staged: true},
		{Role: "assistant", Content: "Filed."},
	}
	if n != 3 {
		t.Fatalf("AdoptTurn returned %d, want the turn's 3 records", n)
	}
	got, _ := f.file.Get(cs.ID)
	if g := briefs(got.Records); !reflect.DeepEqual(g, want) {
		t.Fatalf("the session after the adopt:\n got  %+v\n want %+v", g, want)
	}

	// A second adopted turn of the same conversation appends again, still
	// without carrying anything.
	if err := f.ask.Append("ask-conv", Record{TS: ingest.TS.Add(time.Hour), Role: "user", Content: "one more", Turn: "t4"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if n, err := AdoptTurn(f.ask, "ask-conv", cs.ID, "t4"); err != nil || n != 1 {
		t.Fatalf("second AdoptTurn = %d, %v; want 1, nil", n, err)
	}
	got, _ = f.file.Get(cs.ID)
	if len(got.Records) != 5 || got.Records[4].Content != "one more" || got.Records[4].Carried {
		t.Fatalf("session after the second adopt: %+v", briefs(got.Records))
	}
}

// TestAdoptTurnChangesetGoneNoOp is C-118's rule for the adopt step: a changeset
// that was committed or rejected while the turn ran is not adopted into, and the
// attempt never makes an open/<id> directory — an open/ entry with a
// session.ndjson and no changeset.json is the shape that breaks Engine.Current
// for every verb after it.
func TestAdoptTurnChangesetGoneNoOp(t *testing.T) {
	f := newAdoptFixture(t)
	f.conversation(t)
	cs, err := f.engine.OpenChangeset("ask: file it", stage.Author{Kind: "agent"})
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if err := f.engine.Reject("rejected while the turn ran"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	state := filepath.Join(f.root, ".llmwiki")
	before := treeSnapshot(t, state)

	n, err := AdoptTurn(f.ask, "ask-conv", cs.ID, "t3")
	if err != nil {
		t.Fatalf("AdoptTurn on a gone changeset = %v, want a quiet no-op", err)
	}
	if n != 0 {
		t.Fatalf("AdoptTurn appended %d records to a gone changeset, want 0", n)
	}
	if after := treeSnapshot(t, state); !reflect.DeepEqual(before, after) {
		t.Fatalf("adopting into a gone changeset changed the disk:\n before %v\n after  %v", before, after)
	}
	if _, err := os.Stat(filepath.Join(state, "changesets", "open", cs.ID)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("open/%s exists after the no-op (%v), want it never made", cs.ID, err)
	}
	if _, err := f.engine.Current(); !errors.Is(err, stage.ErrNoChangeset) {
		t.Fatalf("Current after the no-op = %v, want ErrNoChangeset", err)
	}

	// The same holds when the changeset was REJECTED with a session already in
	// it: a closed changeset's transcript is not appended to.
	cs2, err := f.engine.OpenChangeset("another", stage.Author{Kind: "agent"})
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if _, err := f.file.Create(cs2.ID); err != nil {
		t.Fatalf("Create(%s): %v", cs2.ID, err)
	}
	if err := f.engine.Reject("gone too"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if n, err := AdoptTurn(f.ask, "ask-conv", cs2.ID, "t3"); err != nil || n != 0 {
		t.Fatalf("AdoptTurn into a rejected changeset = %d, %v; want 0, nil", n, err)
	}
	if got, _ := f.file.Get(cs2.ID); len(got.Records) != 0 {
		t.Fatalf("a rejected changeset's session grew: %+v", briefs(got.Records))
	}
}
