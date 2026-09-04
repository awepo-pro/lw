package stage

import "testing"

// TestDroppedOpKeepsItsProposalRecord pins gate G2's pre-approval clause:
// the journal records what was PROPOSED, not only what landed, so an op a
// reviewer dropped still leaves its op_proposed behind.
//
// G2 words this clause as a shell check ("stage two ops, drop one, commit
// — then grep"), which is not executable at S2: dropping is the review
// screen's job (S4-T3) and neither backbone §13's CLI surface nor S2-T7's
// brief defines a `lw drop` verb — `lw drop` is an unknown verb and prints
// usage. Correction C-70. This test is the in-process substitute; the
// shell form becomes runnable at gate G4, where the review screen exists.
func TestDroppedOpKeepsItsProposalRecord(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("two ops, one dropped", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	appendCreate(t, e, "wiki/concepts/keep-me.md", newConceptPageContent("Keep Me"))
	appendCreate(t, e, "wiki/concepts/drop-me.md", newConceptPageContent("Drop Me"))

	if err := e.DropOp("op2"); err != nil {
		t.Fatalf("DropOp: %v", err)
	}
	if _, err := e.Commit("two ops, one dropped"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	proposed, err := e.journal.Query(Filter{Kinds: []EventKind{EvOpProposed}})
	if err != nil {
		t.Fatalf("query op_proposed: %v", err)
	}
	var sawDroppedProposal bool
	for _, ev := range proposed {
		if ev.Op == "op2" {
			sawDroppedProposal = true
		}
	}
	if !sawDroppedProposal {
		t.Error("the dropped op left no op_proposed record — the journal is not a pre-approval record")
	}

	dropped, err := e.journal.Query(Filter{Kinds: []EventKind{EvOpDropped}})
	if err != nil {
		t.Fatalf("query op_dropped: %v", err)
	}
	if len(dropped) != 1 || dropped[0].Op != "op2" {
		t.Errorf("op_dropped events = %+v, want exactly one for op2", dropped)
	}

	// And the drop was honoured: the proposal is recorded, the write is not.
	if e.Vault().Exists("wiki/concepts/drop-me.md") {
		t.Error("the dropped op was applied to the vault anyway")
	}
	if !e.Vault().Exists("wiki/concepts/keep-me.md") {
		t.Error("the kept op was not applied")
	}
}
