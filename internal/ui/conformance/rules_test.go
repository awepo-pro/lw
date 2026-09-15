package conformance

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// moreNoteRe matches the overflow note a panel's bottom border may carry.
var moreNoteRe = regexp.MustCompile(`↓ [0-9]+ more`)

// checkMaskRules reports every violation of the masked-cell rules on the
// actual render (contract §9 note 5). Views without a mask check nothing.
func checkMaskRules(t *testing.T, name string, grid, actual []string) {
	t.Helper()

	mask := locateMask(t, name, grid)
	for _, problem := range maskRuleProblems(name, grid, actual, mask) {
		t.Error(problem)
	}
}

// maskRuleProblems runs the rules (a)-(f) over the masked cells of actual
// and returns one problem per violation. The mask keeps cells out of the
// byte comparison, never out of the renderer's contract: the gutter, the
// display forms and the overflow note must still hold where the exact
// bytes are not pinned.
//
// "Content cells" throughout are the two columns a panel's content starts
// at — one in from the border, past the cursor-gutter column.
func maskRuleProblems(name string, grid, actual []string, mask *maskRegion) []string {
	if mask == nil {
		return nil
	}
	var probs []string
	add := func(format string, args ...any) {
		probs = append(probs, fmt.Sprintf(format, args...))
	}

	// (a) Every masked content row's first two content cells are `▎ ` (the
	// changed-block gutter) or two spaces.
	for r := mask.rows[0]; r <= mask.rows[1]; r++ {
		a1, a2 := cellAt(actual[r], mask.x+2), cellAt(actual[r], mask.x+3)
		if (a1 != '▎' || a2 != ' ') && (a1 != ' ' || a2 != ' ') {
			add("(a) row %d: first two content cells are %q, want `▎ ` or two spaces",
				r+1, string([]rune{a1, a2}))
		}
	}

	// (b) No masked row's trimmed content starts with `#`: headings render
	// without their mark.
	for r := mask.rows[0]; r <= mask.rows[1]; r++ {
		inner := innerSegment(actual[r], mask)
		if strings.HasPrefix(strings.TrimLeft(inner, " ▎▌"), "#") {
			add("(b) row %d: trimmed content starts with `#`", r+1)
		}
	}

	// (c) No `^[` and no `[[` anywhere in the masked region, content rows
	// and the bottom border row alike: provenance and wikilinks render in
	// their display form.
	for r := mask.rows[0]; r <= mask.rows[1]+1; r++ {
		inner := innerSegment(actual[r], mask)
		for _, marker := range [...]struct{ name, text string }{
			{"provenance marker `^[`", "^["},
			{"wikilink marker `[[`", "[["},
		} {
			if i := strings.Index(inner, marker.text); i >= 0 {
				add("(c) row %d: %s at inner column %d", r+1, marker.name,
					len([]rune(inner[:i]))+1)
			}
		}
	}

	// (d) In review-preview-*, the changed-block gutter appears in the
	// masked region exactly when the frozen grid shows it. A renderer that
	// drops `▎` must fail where the mockup has marked rows, and one that
	// marks unchanged blocks must fail where it has none. The grid is the
	// reference because whether a changed block is in view depends on the
	// page's shape and the size, which the mask excludes from comparison.
	if strings.HasPrefix(name, "review-preview-") {
		gridMarks, actualMarks := countMarks(grid, mask), countMarks(actual, mask)
		if (gridMarks > 0) != (actualMarks > 0) {
			add("(d) masked region shows the `▎` gutter %dx, the frozen grid %dx",
				actualMarks, gridMarks)
		}
	}

	// (e) The bottom border's note matches `↓ <n> more`, or is absent (all
	// dashes and spaces).
	bottom := innerSegment(actual[mask.bottomRow], mask)
	if !moreNoteRe.MatchString(bottom) && !allRunesIn(bottom, "─ ") {
		add("(e) bottom border note is neither `↓ <n> more` nor absent: %q", bottom)
	}

	// (f) No content outside the panel's inner width: the masked rows stay
	// closed on both border columns.
	for r := mask.rows[0]; r <= mask.rows[1]; r++ {
		if cellAt(actual[r], mask.x) != '│' || cellAt(actual[r], mask.x+mask.w-1) != '│' {
			add("(f) row %d: the panel's border columns are not closed with `│`", r+1)
		}
	}

	return probs
}

// innerSegment returns row's cells between the mask's borders: columns
// x+1 through x+w-2 inclusive.
func innerSegment(row string, mask *maskRegion) string {
	var b strings.Builder
	for c := mask.x + 1; c <= mask.x+mask.w-2; c++ {
		b.WriteRune(cellAt(row, c))
	}
	return b.String()
}

// countMarks counts the masked rows whose content starts with the `▎`
// gutter.
func countMarks(lines []string, mask *maskRegion) int {
	n := 0
	for r := mask.rows[0]; r <= mask.rows[1]; r++ {
		if cellAt(lines[r], mask.x+2) == '▎' {
			n++
		}
	}
	return n
}

// allRunesIn reports whether every rune of s is in set.
func allRunesIn(s, set string) bool {
	for _, r := range s {
		if !strings.ContainsRune(set, r) {
			return false
		}
	}
	return true
}
