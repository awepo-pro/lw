package tools

import (
	"fmt"
	"testing"
)

// TestReach_FE_1_RemoveSectionKeepsTextRefusal pins workflow 081, FE.1 (A4 / C-2):
// remove_section on a heading with 2+ copies: when the targeted copy's text
// is non-empty and another copy's text is empty, refuse (nothing staged).
// Page "# T\n\n## What it is\n## What it is\n\nPara text.\n\n## Next\n\nx\n", occurrence 2
// -> refusal with k=2, n=10, j=1, asserted byte for byte, no op staged.
func TestReach_FE_1_RemoveSectionKeepsTextRefusal(t *testing.T) {
	const body = "# T\n\n## What it is\n## What it is\n\nPara text.\n\n## Next\n\nx\n"
	reg, e, _ := setupReachVault(t, body)

	args := `{"path":"` + reachW + `","section":"## What it is","occurrence":2,"op":"remove_section","rationale":"dedupe heading"}`
	res := callTool(t, reg, "stage.patch_page", args)
	if !res.IsError {
		t.Fatalf("expected error result, got: %+v", res)
	}
	want := fmt.Sprintf("remove_section refused: occurrence %d of section %q on %s holds %d bytes of text and occurrence %d is empty, so removing occurrence %d would delete that text and keep the empty copy. To drop the duplicated heading, remove_section occurrence %d.",
		2, "## What it is", reachW, 10, 1, 2, 1)
	if res.Content != want {
		t.Fatalf("content =\n%q\nwant\n%q", res.Content, want)
	}

	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	liveOps := cs.Live()
	if len(liveOps) != 0 {
		t.Fatalf("live ops = %d, want 0", len(liveOps))
	}
}

// TestReach_FE_2_RemoveSectionEmptyCopyStages pins workflow 081, FE.2:
// same page, remove_section occurrence 1 -> stages; staged body is
// "# T\n\n## What it is\n\nPara text.\n\n## Next\n\nx\n" (only the duplicate heading line gone, "Para text." kept).
func TestReach_FE_2_RemoveSectionEmptyCopyStages(t *testing.T) {
	const body = "# T\n\n## What it is\n## What it is\n\nPara text.\n\n## Next\n\nx\n"
	reg, e, _ := setupReachVault(t, body)

	args := `{"path":"` + reachW + `","section":"## What it is","occurrence":1,"op":"remove_section","rationale":"drop empty duplicate"}`
	res := callTool(t, reg, "stage.patch_page", args)
	const wantBody = "# T\n\n## What it is\n\nPara text.\n\n## Next\n\nx\n"
	assertReachStaged(t, e, res, wantBody)
}

// TestReach_FE_3_BothCopiesTextOrBothEmpty pins workflow 081, FE.3:
// both copies hold text ("## A\n\none\n\n## A\n\ntwo\n"): occurrence 2 still stages (merge path unchanged);
// both copies empty: occurrence 2 still stages.
func TestReach_FE_3_BothCopiesTextOrBothEmpty(t *testing.T) {
	t.Run("both_hold_text", func(t *testing.T) {
		const body = "## A\n\none\n\n## A\n\ntwo\n"
		reg, e, _ := setupReachVault(t, body)

		args := `{"path":"` + reachW + `","section":"## A","occurrence":2,"op":"remove_section","rationale":"remove second copy"}`
		res := callTool(t, reg, "stage.patch_page", args)
		const wantBody = "## A\n\none\n"
		assertReachStaged(t, e, res, wantBody)
	})

	t.Run("both_empty", func(t *testing.T) {
		const body = "## A\n## A\n\n## B\n\nx\n"
		reg, e, _ := setupReachVault(t, body)

		args := `{"path":"` + reachW + `","section":"## A","occurrence":2,"op":"remove_section","rationale":"remove second copy"}`
		res := callTool(t, reg, "stage.patch_page", args)
		const wantBody = "## A\n\n## B\n\nx\n"
		assertReachStaged(t, e, res, wantBody)
	})
}
