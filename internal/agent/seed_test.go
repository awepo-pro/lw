package agent

// seed_test.go is 009 contract §1.2's tests for SeedSession: what is copied
// between sessions (user and assistant text only), what is not (tools,
// reasoning, Finish), and the no-op and error edges. Every store is a real
// NewFileSessions over a temp vault — no fake of the store (MASTER §5, T-A
// assertions).

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSeedSession(t *testing.T) {
	// newSeedStore opens a real file-backed store and pre-creates the named
	// sessions — the way T-C creates the session a turn runs in before
	// seeding it.
	newSeedStore := func(t *testing.T, ids ...string) SessionStore {
		t.Helper()
		store := NewFileSessions(t.TempDir())
		for _, id := range ids {
			if _, err := store.Create(id); err != nil {
				t.Fatalf("Create(%s): %v", id, err)
			}
		}
		return store
	}

	// appendAll writes recs to id, failing the test on the first error.
	appendAll := func(t *testing.T, store SessionStore, id string, recs []Record) {
		t.Helper()
		for _, r := range recs {
			if err := store.Append(id, r); err != nil {
				t.Fatalf("Append to %s: %v", id, err)
			}
		}
	}

	t.Run("copies_user_and_assistant_text", func(t *testing.T) {
		store := newSeedStore(t, "cs-from", "cs-to")
		ts := time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)
		appendAll(t, store, "cs-from", []Record{
			rec(ts, "user", "Q1"),
			{TS: ts.Add(time.Second), Role: "assistant", Reasoning: "how to answer", Content: "A1"},
			{TS: ts.Add(2 * time.Second), Role: "tool", Tool: "wiki.search", Args: `{"q":"kv-cache"}`, Result: "[]"},
			{TS: ts.Add(3 * time.Second), Role: "assistant", Content: "A2"},
		})

		n, err := SeedSession(store, "cs-from", "cs-to")
		if err != nil {
			t.Fatalf("SeedSession: %v", err)
		}
		if n != 3 {
			t.Fatalf("SeedSession appended %d records, want 3", n)
		}

		got, err := store.Get("cs-to")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		// Exactly the three text records, in order, all Carried; the tool
		// record is absent and the reasoning never comes along. DeepEqual
		// against this literal pins every other field to its zero value.
		want := []Record{
			{TS: ts, Role: "user", Content: "Q1", Carried: true},
			{TS: ts.Add(time.Second), Role: "assistant", Content: "A1", Carried: true},
			{TS: ts.Add(3 * time.Second), Role: "assistant", Content: "A2", Carried: true},
		}
		if !reflect.DeepEqual(got.Records, want) {
			t.Fatalf("seeded records:\n got  %+v\n want %+v", got.Records, want)
		}
	})

	t.Run("skips_tools_reasoning_and_empty", func(t *testing.T) {
		store := newSeedStore(t, "cs-from", "cs-to")
		ts := time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)
		appendAll(t, store, "cs-from", []Record{
			rec(ts, "user", "Q1"),
			{TS: ts.Add(time.Second), Role: "tool", Tool: "wiki.search", Args: `{"q":"kv-cache"}`, Result: "[]"},
			{TS: ts.Add(2 * time.Second), Role: "tool", Tool: "stage.create_page", Args: `{"path":"wiki/concepts/x.md"}`, Result: "proposed op1", Staged: true},
			{TS: ts.Add(3 * time.Second), Role: "assistant", Reasoning: "thinking only, no text streamed"},
			{TS: ts.Add(4 * time.Second), Role: "assistant", Finish: "length"},
		})

		n, err := SeedSession(store, "cs-from", "cs-to")
		if err != nil {
			t.Fatalf("SeedSession: %v", err)
		}
		if n != 1 {
			t.Fatalf("SeedSession appended %d records, want 1 (only the user turn has carryable text)", n)
		}

		got, err := store.Get("cs-to")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		// Tool records, reasoning-only records and Finish-only records are
		// not carried; and the one record that is carried has empty
		// Reasoning, Finish, Tool, Args and Result — DeepEqual against this
		// literal pins them all to zero.
		want := []Record{{TS: ts, Role: "user", Content: "Q1", Carried: true}}
		if !reflect.DeepEqual(got.Records, want) {
			t.Fatalf("seeded records:\n got  %+v\n want %+v", got.Records, want)
		}
	})

	t.Run("preserves_order_and_ts", func(t *testing.T) {
		store := newSeedStore(t, "cs-from", "cs-to")
		ts := time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)
		src := []Record{
			rec(ts, "user", "Q1"),
			rec(ts.Add(7*time.Minute+13*time.Second), "assistant", "A1"),
			rec(ts.Add(11*time.Minute+2*time.Second), "user", "Q2"),
			rec(ts.Add(19*time.Minute+58*time.Second), "assistant", "A2"),
		}
		appendAll(t, store, "cs-from", src)

		n, err := SeedSession(store, "cs-from", "cs-to")
		if err != nil {
			t.Fatalf("SeedSession: %v", err)
		}
		if n != len(src) {
			t.Fatalf("SeedSession appended %d records, want %d", n, len(src))
		}

		got, err := store.Get("cs-to")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if len(got.Records) != len(src) {
			t.Fatalf("seeded %d records, want %d", len(got.Records), len(src))
		}
		for i, r := range got.Records {
			if r.TS != src[i].TS {
				t.Errorf("record %d TS = %v, want the source's %v", i, r.TS, src[i].TS)
			}
			if r.Content != src[i].Content || r.Role != src[i].Role {
				t.Errorf("record %d = (%s, %q), want (%s, %q) — order must be preserved", i, r.Role, r.Content, src[i].Role, src[i].Content)
			}
			if !r.Carried {
				t.Errorf("record %d is not Carried", i)
			}
		}
	})

	t.Run("chains_carried_records", func(t *testing.T) {
		store := newSeedStore(t, "cs-a", "cs-b", "cs-c")
		ts := time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)

		appendAll(t, store, "cs-a", []Record{
			rec(ts, "user", "Q-A"),
			rec(ts.Add(time.Second), "assistant", "A-A"),
		})
		if n, err := SeedSession(store, "cs-a", "cs-b"); err != nil || n != 2 {
			t.Fatalf("seed cs-b from cs-a = (%d, %v), want (2, nil)", n, err)
		}

		// B's own turn lands after the records it inherited.
		appendAll(t, store, "cs-b", []Record{
			rec(ts.Add(2*time.Second), "user", "Q-B"),
			rec(ts.Add(3*time.Second), "assistant", "A-B"),
		})
		n, err := SeedSession(store, "cs-b", "cs-c")
		if err != nil {
			t.Fatalf("SeedSession cs-c from cs-b: %v", err)
		}
		if n != 4 {
			t.Fatalf("SeedSession appended %d records, want 4 (A's text carried again, then B's)", n)
		}

		got, err := store.Get("cs-c")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		// C holds A's text followed by B's text, all Carried — chaining is
		// how turn N sees turns 1…N-1 (contract §1.2).
		want := []Record{
			{TS: ts, Role: "user", Content: "Q-A", Carried: true},
			{TS: ts.Add(time.Second), Role: "assistant", Content: "A-A", Carried: true},
			{TS: ts.Add(2 * time.Second), Role: "user", Content: "Q-B", Carried: true},
			{TS: ts.Add(3 * time.Second), Role: "assistant", Content: "A-B", Carried: true},
		}
		if !reflect.DeepEqual(got.Records, want) {
			t.Fatalf("chained records:\n got  %+v\n want %+v", got.Records, want)
		}
	})

	t.Run("empty_from_is_noop", func(t *testing.T) {
		store := newSeedStore(t, "cs-to")
		ts := time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)
		own := []Record{rec(ts, "user", "already here")}
		appendAll(t, store, "cs-to", own)

		n, err := SeedSession(store, "", "cs-to")
		if err != nil {
			t.Fatalf("SeedSession with empty from: %v", err)
		}
		if n != 0 {
			t.Fatalf("SeedSession with empty from appended %d records, want 0", n)
		}

		got, err := store.Get("cs-to")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !reflect.DeepEqual(got.Records, own) {
			t.Fatalf("to changed: got %+v, want %+v", got.Records, own)
		}
	})

	t.Run("same_session_is_noop", func(t *testing.T) {
		store := newSeedStore(t, "cs-same")
		ts := time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)
		own := []Record{rec(ts, "user", "Q1")}
		appendAll(t, store, "cs-same", own)

		n, err := SeedSession(store, "cs-same", "cs-same")
		if err != nil {
			t.Fatalf("SeedSession with from == to: %v", err)
		}
		if n != 0 {
			t.Fatalf("SeedSession with from == to appended %d records, want 0 (not a duplication)", n)
		}

		got, err := store.Get("cs-same")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !reflect.DeepEqual(got.Records, own) {
			t.Fatalf("session changed: got %+v, want %+v", got.Records, own)
		}
	})

	t.Run("get_error_wrapped", func(t *testing.T) {
		store := newSeedStore(t, "cs-to")

		n, err := SeedSession(store, "cs-nowhere", "cs-to")
		if err == nil {
			t.Fatal("SeedSession from an unknown session = nil error, want one")
		}
		if n != 0 {
			t.Fatalf("SeedSession appended %d records on a Get failure, want 0", n)
		}
		const prefix = "agent: seed session cs-to from cs-nowhere: "
		if !strings.HasPrefix(err.Error(), prefix) {
			t.Fatalf("error = %q, want it to start with %q", err.Error(), prefix)
		}
	})
}
