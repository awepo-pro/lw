package conformance

import "testing"

// The locator's test rows, copied cell for cell from the frozen grids
// (plans/003-mockups/ascii): review-80x24.txt rows 4–5, review-100x30.txt
// row 5 and browse-120x40.txt rows 8 and 14. Each literal is the panel's
// row — border and gutter included; for the split grids, where the Ops or
// Pages panel shares the terminal line with its neighbour, the literal is
// that panel's border-to-border portion of the line. The grids live in the
// read-only metadata tree, so the rows are pasted here rather than read at
// run time, and this test needs no vault. Every panel shown sits at cell 0,
// which is the x the calls below pass.

// review-80x24.txt, op3 — the target, on the cursor row (`▌` gutter). Its
// basename is unclipped; the dir hint follows two spaces after it.
const review80TargetRow = `│▌● patch private-network-access.md  wiki/concepts/                            │`

// review-80x24.txt, op2 — not the target, so its gutter is a space. Its
// dir hint `wiki/concepts/` is a 14-character prefix of the target's path:
// the false match correction C34 records.
const review80DirHintRow = `│ ● new   network-namespace-egress-isolation.md  wiki/concepts/                │`

// review-100x30.txt, op3 again with the Ops panel narrowed by the split
// layout: the basename is `…`-clipped to `private-network-acce…`.
const review100TargetRow = `│▌● patch private-network-acce… │`

// browse-120x40.txt, the Pages cursor row on the selected comparison page;
// its name is `…`-clipped to fit the tree panel.
const browseTargetRow = `│▌      anthropic-api-vs-vertex… │`

// browse-120x40.txt, another page's row.
const browseOtherRow = `│       claude                   │`

// TestCursorRowIdentification pins the j-script locator (C34): the driver
// identifies its target by the cursor row's name cell — never by a prefix
// of the target found anywhere in the row, which op2's dir hint defeated.
func TestCursorRowIdentification(t *testing.T) {
	t.Run("review_target_row_matches_unclipped", func(t *testing.T) {
		if !rowIdentifies(review80TargetRow, 0, reviewTargetPanel, reviewTargetLabel) {
			t.Errorf("op3's unclipped row does not identify %s", reviewTargetLabel)
		}
	})

	t.Run("review_target_row_matches_clipped", func(t *testing.T) {
		if !rowIdentifies(review100TargetRow, 0, reviewTargetPanel, reviewTargetLabel) {
			t.Errorf("op3's `…`-clipped row does not identify %s", reviewTargetLabel)
		}
	})

	t.Run("review_dir_hint_row_does_not_match", func(t *testing.T) {
		if rowIdentifies(review80DirHintRow, 0, reviewTargetPanel, reviewTargetLabel) {
			t.Error("op2's row identifies the target: its wiki/concepts/ dir hint must not")
		}
	})

	t.Run("browse_target_row_matches", func(t *testing.T) {
		if !rowIdentifies(browseTargetRow, 0, "Pages", browseTargetLabel) {
			t.Errorf("the target page's row does not identify %s", browseTargetLabel)
		}
	})

	t.Run("browse_other_row_does_not_match", func(t *testing.T) {
		if rowIdentifies(browseOtherRow, 0, "Pages", browseTargetLabel) {
			t.Error("claude's row identifies the target")
		}
	})
}
