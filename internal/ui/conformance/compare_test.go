package conformance

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// compareGrid is the gate's comparator (contract §9 notes 4 and 7): it
// holds grid — the frozen file's lines — against actual — the render's
// lines — cell for cell outside the mask, and on failure reports a unified
// diff of the two with every masked cell replaced by `░` on both sides,
// headed by the subtest name.
func compareGrid(t *testing.T, name string, grid, actual []string) {
	t.Helper()

	mask := locateMask(t, name, grid)
	if gridEqual(grid, actual, mask) {
		return
	}
	t.Errorf("%s\n%s", name, maskedDiff(name, grid, actual, mask))
}

// gridEqual reports whether actual matches grid cell for cell outside the
// mask. Cells are runes: every rune the frames draw is one cell wide
// (masks_test.go's cellAt), so rune index is cell index on both sides.
func gridEqual(grid, actual []string, mask *maskRegion) bool {
	if len(grid) != len(actual) {
		return false
	}
	for r, gline := range grid {
		gr, ar := []rune(gline), []rune(actual[r])
		if len(gr) != len(ar) {
			return false
		}
		for c, g := range gr {
			if mask.masked(r, c) {
				continue
			}
			if ar[c] != g {
				return false
			}
		}
	}
	return true
}

// maskedDiff renders the failure diff: both sides masked, then a unified
// line diff headed by the two sides' names.
func maskedDiff(name string, grid, actual []string, mask *maskRegion) string {
	var b strings.Builder
	fmt.Fprintf(&b, "--- %s (frozen grid)\n", name)
	fmt.Fprintf(&b, "+++ %s (actual render)\n", name)
	for _, line := range unifiedDiff(maskLines(grid, mask), maskLines(actual, mask)) {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// maskLines copies lines with every masked cell replaced by `░`.
func maskLines(lines []string, mask *maskRegion) []string {
	if mask == nil {
		return lines
	}
	out := make([]string, len(lines))
	for r, line := range lines {
		if !mask.maskedRow(r) {
			out[r] = line
			continue
		}
		runes := []rune(line)
		for c := range runes {
			if mask.masked(r, c) {
				runes[c] = '░'
			}
		}
		out[r] = string(runes)
	}
	return out
}

// diffOp is one unified-diff operation: ' ' context, '-' frozen-grid line,
// '+' actual-render line.
type diffOp struct {
	kind byte
	line string
}

// diffContext is how many unchanged lines surround each change run.
const diffContext = 3

// unifiedDiff renders the standard unified diff of a and b: `@@ -o,c +o,c
// @@` headers over context and change runs, runs of changes closer than
// 2*diffContext merged into one hunk.
func unifiedDiff(a, b []string) []string {
	ops := diffOps(a, b)

	changes := []int{}
	for i, op := range ops {
		if op.kind != ' ' {
			changes = append(changes, i)
		}
	}
	if len(changes) == 0 {
		return nil
	}
	groups := [][2]int{{changes[0], changes[0]}}
	for _, c := range changes[1:] {
		last := &groups[len(groups)-1]
		if c-last[1] > 2*diffContext {
			groups = append(groups, [2]int{c, c})
			continue
		}
		last[1] = c
	}

	// aBefore/bBefore count each side's lines up to op i, so a hunk's
	// header numbers come straight from its first op's index.
	aBefore, bBefore := make([]int, len(ops)+1), make([]int, len(ops)+1)
	for i, op := range ops {
		aBefore[i+1], bBefore[i+1] = aBefore[i], bBefore[i]
		if op.kind != '+' {
			aBefore[i+1]++
		}
		if op.kind != '-' {
			bBefore[i+1]++
		}
	}

	var out []string
	for _, g := range groups {
		k1 := g[0] - diffContext
		if k1 < 0 {
			k1 = 0
		}
		k2 := g[1] + diffContext
		if k2 >= len(ops) {
			k2 = len(ops) - 1
		}
		aLen, bLen := 0, 0
		for i := k1; i <= k2; i++ {
			if ops[i].kind != '+' {
				aLen++
			}
			if ops[i].kind != '-' {
				bLen++
			}
		}
		out = append(out, fmt.Sprintf("@@ -%s +%s @@",
			rangeSpec(aBefore[k1]+1, aLen), rangeSpec(bBefore[k1]+1, bLen)))
		for i := k1; i <= k2; i++ {
			out = append(out, string(ops[i].kind)+ops[i].line)
		}
	}
	return out
}

// rangeSpec formats a unified-diff range: one line is just its number,
// zero lines are `start,0`, more are `start,count`.
func rangeSpec(start, count int) string {
	switch {
	case count == 0:
		return strconv.Itoa(start-1) + ",0"
	case count == 1:
		return strconv.Itoa(start)
	default:
		return strconv.Itoa(start) + "," + strconv.Itoa(count)
	}
}

// diffOps walks the LCS of a and b, emitting context and change operations
// in order.
func diffOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case a[i] == b[j]:
				lcs[i][j] = lcs[i+1][j+1] + 1
			case lcs[i+1][j] >= lcs[i][j+1]:
				lcs[i][j] = lcs[i+1][j]
			default:
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	var out []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, diffOp{' ', a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, diffOp{'-', a[i]})
			i++
		default:
			out = append(out, diffOp{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, diffOp{'-', a[i]})
	}
	for ; j < m; j++ {
		out = append(out, diffOp{'+', b[j]})
	}
	return out
}

// firstLineDiff names the first difference between two plain renders, for
// the light-polarity identity check's report.
func firstLineDiff(a, b []string) string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return fmt.Sprintf("row %d: %q vs %q", i+1, a[i], b[i])
		}
	}
	return fmt.Sprintf("row count %d vs %d", len(a), len(b))
}

// cloneLines deep-copies a grid's lines.
func cloneLines(grid []string) []string {
	out := make([]string, len(grid))
	copy(out, grid)
	return out
}
