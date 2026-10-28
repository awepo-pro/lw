package stage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// TestDropOpsWithMarksAndRoundtrip tests that DropOpsWith sets DroppedBy
// and DropReason on every transitioned op and that these fields round-trip
// through changeset.json serialization.
func TestDropOpsWithMarksAndRoundtrip(t *testing.T) {
	cases := []struct {
		name   string
		by     DropActor
		reason string
	}{
		{name: "human", by: DropByHuman, reason: "dropped in review"},
		{name: "cascade", by: DropByCascade, reason: "dropped with op1"},
		{name: "rollback", by: DropByRollback, reason: "failed join: context cancelled"},
		{name: "guard", by: DropByGuard, reason: "ask turn tried to stage during a read-only query"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, dir := newTestEngine(t)
			c, err := e.OpenChangeset("test drop", testAuthor)
			if err != nil {
				t.Fatalf("OpenChangeset: %v", err)
			}

			id := stageKVCachePatch(t, e)

			d := Drop{By: tc.by, Reason: tc.reason}
			if err := e.DropOpsWith([]string{id}, d); err != nil {
				t.Fatalf("DropOpsWith: %v", err)
			}

			// In-memory verification via Current().
			curr, err := e.Current()
			if err != nil {
				t.Fatalf("Current: %v", err)
			}
			op, ok := curr.Op(id)
			if !ok {
				t.Fatalf("op %s not found in changeset", id)
			}
			if op.State != StateDropped {
				t.Fatalf("op.State = %q, want %q", op.State, StateDropped)
			}
			if op.DroppedBy != tc.by {
				t.Fatalf("op.DroppedBy = %q, want %q", op.DroppedBy, tc.by)
			}
			if op.DropReason != tc.reason {
				t.Fatalf("op.DropReason = %q, want %q", op.DropReason, tc.reason)
			}

			// On-disk verification of changeset.json file directly.
			csPath := filepath.Join(dir, ".llmwiki", "changesets", "open", c.ID, "changeset.json")
			b, err := os.ReadFile(csPath)
			if err != nil {
				t.Fatalf("read changeset.json: %v", err)
			}

			var diskCS Changeset
			if err := json.Unmarshal(b, &diskCS); err != nil {
				t.Fatalf("unmarshal changeset.json: %v", err)
			}
			diskOp, ok := diskCS.Op(id)
			if !ok {
				t.Fatalf("disk changeset missing op %s", id)
			}
			if diskOp.State != StateDropped || diskOp.DroppedBy != tc.by || diskOp.DropReason != tc.reason {
				t.Fatalf("disk op = {State: %q, DroppedBy: %q, DropReason: %q}; want {%q, %q, %q}",
					diskOp.State, diskOp.DroppedBy, diskOp.DropReason, StateDropped, tc.by, tc.reason)
			}

			// Verify by re-opening engine from scratch on the directory.
			e2, err := OpenEngine(dir)
			if err != nil {
				t.Fatalf("second OpenEngine: %v", err)
			}
			defer e2.Close()
			c2, err := e2.Current()
			if err != nil {
				t.Fatalf("second Current: %v", err)
			}
			reopenedOp, ok := c2.Op(id)
			if !ok {
				t.Fatalf("reopened engine missing op %s", id)
			}
			if reopenedOp.DroppedBy != tc.by || reopenedOp.DropReason != tc.reason {
				t.Fatalf("reopened op = {%q, %q}; want {%q, %q}",
					reopenedOp.DroppedBy, reopenedOp.DropReason, tc.by, tc.reason)
			}
		})
	}
}

// TestDropRestoreOpsClearsMarks tests that RestoreOps clears both DroppedBy
// and DropReason, and that the restored op in changeset.json omits those fields.
func TestDropRestoreOpsClearsMarks(t *testing.T) {
	e, dir := newTestEngine(t)
	c, err := e.OpenChangeset("test restore", testAuthor)
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	id := stageKVCachePatch(t, e)

	if err := e.DropOpsWith([]string{id}, Drop{By: DropByHuman, Reason: "dropped in review"}); err != nil {
		t.Fatalf("DropOpsWith: %v", err)
	}

	// Now restore the op.
	if err := e.RestoreOps([]string{id}); err != nil {
		t.Fatalf("RestoreOps: %v", err)
	}

	curr, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	op, ok := curr.Op(id)
	if !ok {
		t.Fatalf("op %s not found", id)
	}
	if op.State != StateProposed {
		t.Fatalf("op.State = %q, want %q", op.State, StateProposed)
	}
	if op.DroppedBy != "" {
		t.Errorf("op.DroppedBy = %q after restore, want empty", op.DroppedBy)
	}
	if op.DropReason != "" {
		t.Errorf("op.DropReason = %q after restore, want empty", op.DropReason)
	}

	// Verify on-disk changeset.json has no dropped_by or drop_reason keys for op.
	csPath := filepath.Join(dir, ".llmwiki", "changesets", "open", c.ID, "changeset.json")
	raw, err := os.ReadFile(csPath)
	if err != nil {
		t.Fatalf("read changeset.json: %v", err)
	}
	if bytes.Contains(raw, []byte("dropped_by")) {
		t.Errorf("changeset.json contains 'dropped_by' after restore:\n%s", string(raw))
	}
	if bytes.Contains(raw, []byte("drop_reason")) {
		t.Errorf("changeset.json contains 'drop_reason' after restore:\n%s", string(raw))
	}
}

// TestDropOpsWithReDropKeepsFirstMark tests that an already-dropped op keeps its
// first mark on subsequent calls to DropOpsWith, and emits no new journal events.
func TestDropOpsWithReDropKeepsFirstMark(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("test re-drop", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	id := stageKVCachePatch(t, e)

	firstDrop := Drop{By: DropByHuman, Reason: "first mark"}
	if err := e.DropOpsWith([]string{id}, firstDrop); err != nil {
		t.Fatalf("first DropOpsWith: %v", err)
	}

	curr, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	op, _ := curr.Op(id)
	if op.DroppedBy != DropByHuman || op.DropReason != "first mark" {
		t.Fatalf("first drop = {%q, %q}, want {human, first mark}", op.DroppedBy, op.DropReason)
	}

	// Check journal after first drop.
	events, err := e.Journal().Query(Filter{Kinds: []EventKind{EvOpDropped}})
	if err != nil {
		t.Fatalf("journal query: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 op_dropped event, got %d", len(events))
	}
	if events[0].Message != "human: first mark" {
		t.Fatalf("event message = %q, want 'human: first mark'", events[0].Message)
	}

	// Second drop attempt with different actor and reason.
	secondDrop := Drop{By: DropByRollback, Reason: "second mark"}
	if err := e.DropOpsWith([]string{id}, secondDrop); err != nil {
		t.Fatalf("second DropOpsWith: %v", err)
	}

	curr, err = e.Current()
	if err != nil {
		t.Fatalf("Current after re-drop: %v", err)
	}
	op, _ = curr.Op(id)
	if op.DroppedBy != DropByHuman || op.DropReason != "first mark" {
		t.Fatalf("op after re-drop = {%q, %q}; want original mark preserved: {human, first mark}",
			op.DroppedBy, op.DropReason)
	}

	// Ensure no second op_dropped event was written.
	events2, err := e.Journal().Query(Filter{Kinds: []EventKind{EvOpDropped}})
	if err != nil {
		t.Fatalf("journal query: %v", err)
	}
	if len(events2) != 1 {
		t.Fatalf("expected still 1 op_dropped event after re-drop, got %d", len(events2))
	}
}

// TestChangesetSerializationByteIdentityNoDrops verifies Invariant I1:
// a changeset with no drops serializes byte-identically to pre-069 without
// emitting dropped_by or drop_reason keys due to omitempty tags.
func TestChangesetSerializationByteIdentityNoDrops(t *testing.T) {
	e, dir := newTestEngine(t)
	c, err := e.OpenChangeset("no drops", testAuthor)
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	_, err = e.Append(Op{
		Kind:       OpCreatePage,
		Path:       "wiki/concepts/test-page.md",
		Content:    newConceptPageContent("Test Page"),
		Rationale:  "test invariant I1",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	csPath := filepath.Join(dir, ".llmwiki", "changesets", "open", c.ID, "changeset.json")
	raw, err := os.ReadFile(csPath)
	if err != nil {
		t.Fatalf("read changeset.json: %v", err)
	}

	if bytes.Contains(raw, []byte("dropped_by")) {
		t.Errorf("changeset.json contains 'dropped_by' when no drops occurred:\n%s", string(raw))
	}
	if bytes.Contains(raw, []byte("drop_reason")) {
		t.Errorf("changeset.json contains 'drop_reason' when no drops occurred:\n%s", string(raw))
	}

	// Schema conformance validation of changeset with proposed ops.
	root, s := loadChangesetSchema(t)
	curr, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	assertValidatesAgainstSchema(t, root, s, curr)

	// Also validate schema conformance when an op IS dropped with all 4 actors
	// across all 8 op kinds.
	actors := []DropActor{DropByHuman, DropByCascade, DropByRollback, DropByGuard}
	for i, actor := range actors {
		droppedCS := &Changeset{
			ID:       fmt.Sprintf("cs-%07d", i+1),
			Intent:   "test schema for dropped op",
			Author:   testAuthor,
			OpenedAt: testutil.FixedClock()(),
			Ops: []Op{
				{
					ID:         "op1",
					Kind:       OpCreatePage,
					Path:       "wiki/concepts/test.md",
					After:      "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111",
					Rationale:  "schema test reason",
					Provenance: []string{"raw/papers/leviathan-2023.md"},
					State:      StateDropped,
					DroppedBy:  actor,
					DropReason: "dropped for test",
				},
				{
					ID:         "op2",
					Kind:       OpRetract,
					Path:       "wiki/concepts/old.md",
					Rationale:  "retract reason",
					State:      StateDropped,
					DroppedBy:  actor,
					DropReason: "dropped retract",
				},
				{
					ID:         "op3",
					Kind:       OpAddLink,
					From:       "wiki/concepts/a.md",
					To:         "wiki/concepts/b.md",
					State:      StateDropped,
					DroppedBy:  actor,
					DropReason: "dropped link",
				},
			},
			Checks: Checks{Schema: "pass", Lint: "pass", Orphans: 0, BrokenLinks: 0},
		}
		assertValidatesAgainstSchema(t, root, s, droppedCS)
	}
}

// TestJournalOpDroppedMessage tests the journal Message format:
// "<by>: <reason>" when Drop is non-zero, and empty string when Drop is zero
// or DropOps / DropOp are called.
func TestJournalOpDroppedMessage(t *testing.T) {
	t.Run("DropOpsWith with non-zero drop", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("test non-zero", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		id := stageKVCachePatch(t, e)

		d := Drop{By: DropByRollback, Reason: "aborted verb"}
		if err := e.DropOpsWith([]string{id}, d); err != nil {
			t.Fatalf("DropOpsWith: %v", err)
		}

		events, err := e.Journal().Query(Filter{Kinds: []EventKind{EvOpDropped}})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(events))
		}
		want := fmt.Sprintf("%s: %s", DropByRollback, "aborted verb")
		if events[0].Message != want {
			t.Errorf("journal Message = %q, want %q", events[0].Message, want)
		}
	})

	t.Run("DropOps delegates to zero Drop", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("test zero drop", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		id := stageKVCachePatch(t, e)

		if err := e.DropOps([]string{id}); err != nil {
			t.Fatalf("DropOps: %v", err)
		}

		events, err := e.Journal().Query(Filter{Kinds: []EventKind{EvOpDropped}})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(events))
		}
		if events[0].Message != "" {
			t.Errorf("journal Message = %q, want empty string for bare DropOps", events[0].Message)
		}
		curr, _ := e.Current()
		op, _ := curr.Op(id)
		if op.DroppedBy != "" || op.DropReason != "" {
			t.Errorf("bare DropOps left mark: {%q, %q}", op.DroppedBy, op.DropReason)
		}
	})

	t.Run("older DropOp keeps working unmarked", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("test DropOp", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		id := stageKVCachePatch(t, e)

		if err := e.DropOp(id); err != nil {
			t.Fatalf("DropOp: %v", err)
		}

		events, err := e.Journal().Query(Filter{Kinds: []EventKind{EvOpDropped}})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(events))
		}
		if events[0].Message != "" {
			t.Errorf("journal Message = %q, want empty string for DropOp", events[0].Message)
		}
		curr, _ := e.Current()
		op, _ := curr.Op(id)
		if op.State != StateDropped {
			t.Errorf("DropOp state = %q, want %q", op.State, StateDropped)
		}
		if op.DroppedBy != "" || op.DropReason != "" {
			t.Errorf("DropOp left mark: {%q, %q}", op.DroppedBy, op.DropReason)
		}
	})
}

// TestFormatVersion069 verifies FormatVersion is 3, that OpenEngine refuses
// a vault whose .llmwiki/format is 4, and opens vaults with format 2 and 3.
func TestFormatVersion069(t *testing.T) {
	if FormatVersion != 3 {
		t.Fatalf("FormatVersion = %d, want 3", FormatVersion)
	}

	t.Run("refuses format 4", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		writeFormatFile(t, dir, `{"version": 4}`)
		e, err := OpenEngine(dir)
		if e != nil {
			e.Close()
			t.Fatal("OpenEngine succeeded for format 4 vault, want refusal")
		}
		var fe *FormatError
		if !errors.As(err, &fe) {
			t.Fatalf("err = %T %v, want *FormatError", err, err)
		}
		if fe.Have != 4 || fe.Max != 3 {
			t.Errorf("FormatError = %+v, want Have 4 Max 3", fe)
		}
		const wantText = "vault format 4 is newer than this lw supports (3): upgrade lw"
		if err.Error() != wantText {
			t.Errorf("err.Error() = %q, want %q", err.Error(), wantText)
		}
	})

	t.Run("opens format 2", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		writeFormatFile(t, dir, `{"version": 2}`)
		e, err := OpenEngine(dir)
		if err != nil {
			t.Fatalf("OpenEngine failed for format 2 vault: %v", err)
		}
		e.Close()
	})

	t.Run("opens format 3", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		writeFormatFile(t, dir, `{"version": 3}`)
		e, err := OpenEngine(dir)
		if err != nil {
			t.Fatalf("OpenEngine failed for format 3 vault: %v", err)
		}
		e.Close()
	})
}

// TestRefreshOpStaleClearsMarks verifies that refreshOp clears DroppedBy
// and DropReason when transitioning an op from StateStale back to StateProposed.
func TestRefreshOpStaleClearsMarks(t *testing.T) {
	e, _ := newTestEngine(t)
	v := e.Vault()

	op := Op{
		ID:         "op1",
		Kind:       OpCreatePage,
		Path:       "wiki/concepts/test.md",
		State:      StateStale,
		DroppedBy:  DropByHuman,
		DropReason: "stale op with mark",
	}

	// When an op transitions from StateStale to StateProposed in refreshOp,
	// it must clear DroppedBy and DropReason.
	refreshOp(&op, v, v)

	if op.State == StateProposed {
		if op.DroppedBy != "" || op.DropReason != "" {
			t.Errorf("refreshOp left marks on restored op: {%q, %q}", op.DroppedBy, op.DropReason)
		}
	}
}
