package agent

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

func rec(ts time.Time, role, content string) Record {
	return Record{TS: ts, Role: role, Content: content}
}

// TestSessionPersistsBesideChangeset is one of the three PASS-by-name tests
// the stage file names. It asserts the file lands at the exact path
// backbone §9's "Contract — where session.ndjson lives" (C-102) promises —
// inside the changeset's own open/<id> directory, beside changeset.json,
// not a fourth top-level directory — and that Close leaves it there, still
// readable, rather than moving or deleting it.
func TestSessionPersistsBesideChangeset(t *testing.T) {
	root := t.TempDir()
	store := NewFileSessions(root)

	const changesetID = "cs-test0001"
	s, err := store.Create(changesetID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if s.ID != changesetID || s.ChangesetID != changesetID {
		t.Fatalf("Create session = %+v, want ID/ChangesetID %q", s, changesetID)
	}

	wantPath := filepath.Join(root, ".llmwiki", "changesets", "open", changesetID, "session.ndjson")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("session.ndjson missing beside the changeset at %s: %v", wantPath, err)
	}

	ts := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	want := []Record{
		rec(ts, "user", "ingest this paper"),
		{TS: ts.Add(time.Second), Role: "tool", Tool: "stage.create_page", Args: `{"path":"wiki/concepts/x.md"}`, Result: "proposed op1", Staged: true},
	}
	for _, r := range want {
		if err := store.Append(changesetID, r); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	if err := store.Close(changesetID); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The file must still exist, at the same path, after Close: archiving
	// means "durably final", not "relocated" (session.go's Close doc).
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("session.ndjson gone after Close: %v", err)
	}

	got, err := store.Get(changesetID)
	if err != nil {
		t.Fatalf("Get after Close: %v", err)
	}
	if !reflect.DeepEqual(got.Records, want) {
		t.Fatalf("Get after Close = %+v, want %+v", got.Records, want)
	}
}

// TestSessionRoundTrip writes a batch of records, reopens the store from
// scratch (a fresh fileSessions over the same root, simulating a new `lw`
// process), and checks every record reads back identical.
func TestSessionRoundTrip(t *testing.T) {
	root := t.TempDir()
	const id = "cs-roundtrip"

	store := NewFileSessions(root)
	if _, err := store.Create(id); err != nil {
		t.Fatalf("Create: %v", err)
	}

	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	want := []Record{
		rec(ts, "user", "hello"),
		rec(ts.Add(time.Minute), "assistant", "hi, how can I help?"),
		{TS: ts.Add(2 * time.Minute), Role: "assistant", Tool: "wiki.search", Args: `{"q":"kv-cache"}`},
		{TS: ts.Add(3 * time.Minute), Role: "tool", Tool: "wiki.search", Result: `[{"path":"wiki/concepts/kv-cache.md"}]`},
	}
	for _, r := range want {
		if err := store.Append(id, r); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	// Fresh store instance over the same root — no shared in-memory state.
	reopened := NewFileSessions(root)
	got, err := reopened.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(got.Records, want) {
		t.Fatalf("round trip mismatch:\n got  %+v\n want %+v", got.Records, want)
	}
	if got.ID != id || got.ChangesetID != id {
		t.Fatalf("Get session ids = %q/%q, want %q/%q", got.ID, got.ChangesetID, id, id)
	}
	if !got.Started.Equal(ts) {
		t.Fatalf("Started = %v, want %v (the earliest record's TS)", got.Started, ts)
	}
}

func TestSessionCreateTwiceFails(t *testing.T) {
	root := t.TempDir()
	store := NewFileSessions(root)
	if _, err := store.Create("cs-dup"); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := store.Create("cs-dup"); !errors.Is(err, errSessionExists) {
		t.Fatalf("second Create err = %v, want errSessionExists", err)
	}
}

func TestSessionUnknownIDErrors(t *testing.T) {
	root := t.TempDir()
	store := NewFileSessions(root)

	if _, err := store.Get("cs-missing"); !errors.Is(err, errSessionNotFound) {
		t.Errorf("Get err = %v, want errSessionNotFound", err)
	}
	if err := store.Append("cs-missing", rec(time.Now(), "user", "x")); !errors.Is(err, errSessionNotFound) {
		t.Errorf("Append err = %v, want errSessionNotFound", err)
	}
	if err := store.Close("cs-missing"); !errors.Is(err, errSessionNotFound) {
		t.Errorf("Close err = %v, want errSessionNotFound", err)
	}
}

func TestSessionList(t *testing.T) {
	root := t.TempDir()
	store := NewFileSessions(root)

	// List on a vault with no .llmwiki/changesets yet: empty, not an error.
	ids, err := store.List()
	if err != nil {
		t.Fatalf("List on empty store: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("List on empty store = %v, want empty", ids)
	}

	for _, id := range []string{"cs-b", "cs-a", "cs-c"} {
		if _, err := store.Create(id); err != nil {
			t.Fatalf("Create(%s): %v", id, err)
		}
	}

	ids, err = store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"cs-a", "cs-b", "cs-c"} // sorted (00-conventions.md §3)
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("List = %v, want %v", ids, want)
	}
}

// TestSessionTravelsWithChangesetCommit is the regression S5-T2 repair-1
// exists to prevent (backbone §9 C-102): it drives a REAL stage.Engine
// through OpenChangeset, Append and Commit — which renames the whole
// changeset directory from changesets/open/<id> to changesets/committed/<id>
// — and checks that session.ndjson, living inside that directory, moved
// with it and is still readable via Get afterward. Staged records
// (backbone §9's Compact contract: "never dropped") are asserted present
// too, since they are the audit trail the whole layout exists to protect.
func TestSessionTravelsWithChangesetCommit(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("stage.OpenEngine: %v", err)
	}
	defer e.Close()

	cs, err := e.OpenChangeset("add a page", stage.Author{Kind: "agent", Model: "test-model"})
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	store := NewFileSessions(root)
	if _, err := store.Create(cs.ID); err != nil {
		t.Fatalf("Create: %v", err)
	}

	openPath := filepath.Join(root, ".llmwiki", "changesets", "open", cs.ID, "session.ndjson")
	if _, err := os.Stat(openPath); err != nil {
		t.Fatalf("session.ndjson missing under open/ right after Create: %v", err)
	}

	ts := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	userRec := rec(ts, "user", "ingest this paper")
	if err := store.Append(cs.ID, userRec); err != nil {
		t.Fatalf("Append user record: %v", err)
	}

	const path = "wiki/concepts/agent-added.md"
	content := []byte("---\n" +
		"title: Agent Added\n" +
		"created: 2026-09-06\n" +
		"updated: 2026-09-06\n" +
		"type: concept\n" +
		"tags: [inference]\n" +
		"confidence: medium\n" +
		"---\n" +
		"\n" +
		"# Agent Added\n" +
		"\n" +
		"See [[kv-cache]] and [[gpt-4]] for background.\n")

	if _, err := e.Append(stage.Op{
		Kind:       stage.OpCreatePage,
		Path:       path,
		Content:    content,
		Rationale:  "test",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	}); err != nil {
		t.Fatalf("stage.Engine.Append: %v", err)
	}

	stagedRec := Record{
		TS:     ts.Add(time.Second),
		Role:   "tool",
		Tool:   "stage.create_page",
		Args:   `{"path":"wiki/concepts/agent-added.md"}`,
		Result: "proposed op1",
		Staged: true,
	}
	if err := store.Append(cs.ID, stagedRec); err != nil {
		t.Fatalf("Append staged record: %v", err)
	}

	if _, err := e.Commit("add agent-added"); err != nil {
		t.Fatalf("stage.Engine.Commit: %v", err)
	}

	// The property this test exists to pin: the WHOLE changeset directory
	// moved, so session.ndjson must no longer be under open/ ...
	if _, err := os.Stat(openPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("session.ndjson still under open/ after commit (stat err = %v); it should have moved with the changeset", err)
	}
	// ... and must now be under committed/, beside the moved changeset.json.
	committedDir := filepath.Join(root, ".llmwiki", "changesets", "committed", cs.ID)
	if _, err := os.Stat(filepath.Join(committedDir, "changeset.json")); err != nil {
		t.Fatalf("changeset.json not found under committed/ after commit: %v", err)
	}
	committedPath := filepath.Join(committedDir, "session.ndjson")
	if _, err := os.Stat(committedPath); err != nil {
		t.Fatalf("session.ndjson not found under committed/ after commit: %v", err)
	}

	got, err := store.Get(cs.ID)
	if err != nil {
		t.Fatalf("Get after commit: %v", err)
	}
	want := []Record{userRec, stagedRec}
	if !reflect.DeepEqual(got.Records, want) {
		t.Fatalf("Get after commit = %+v, want %+v", got.Records, want)
	}

	if err := store.Close(cs.ID); err != nil {
		t.Fatalf("Close after commit: %v", err)
	}
}
