package testutil

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// contextLines is how many matching lines of context Golden shows on each
// side of the first difference.
const contextLines = 3

// updateFlag backs UpdateEnabled. Registered exactly once here; every
// package under test shares this single "-update" flag.
var updateFlag = flag.Bool("update", false, "rewrite golden files instead of comparing against them")

// UpdateEnabled reports whether the test binary was invoked with -update.
func UpdateEnabled() bool {
	return *updateFlag
}

// Golden compares got against the file at path. When -update is set it
// rewrites that file (creating parent directories as needed) instead of
// comparing, and never fails. Otherwise a mismatch fails t with a
// line-level diff.
func Golden(t *testing.T, path string, got []byte) {
	t.Helper()

	if UpdateEnabled() {
		if err := writeGolden(path, got); err != nil {
			t.Fatalf("testutil: update golden %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("testutil: read golden %s: %v (run with -update to create it)", path, err)
	}

	if diff, ok := compareGolden(want, got); !ok {
		t.Fatalf("testutil: golden mismatch %s:\n%s", path, diff)
	}
}

// GoldenString is Golden for a string payload.
func GoldenString(t *testing.T, path string, got string) {
	t.Helper()
	Golden(t, path, []byte(got))
}

// writeGolden creates path's parent directories and writes got, normalized
// to end with exactly one trailing newline, mode 0o644.
func writeGolden(path string, got []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, withTrailingNewline(got), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// withTrailingNewline returns b with any trailing newlines trimmed and
// exactly one added back, per 00-conventions.md §3.
func withTrailingNewline(b []byte) []byte {
	trimmed := bytes.TrimRight(b, "\n")
	out := make([]byte, 0, len(trimmed)+1)
	out = append(out, trimmed...)
	out = append(out, '\n')
	return out
}

// compareGolden reports whether want and got are byte-identical. When they
// are not, diff is a human-readable, line-level report of the first
// difference: a few lines of matching context, then the differing lines
// marked "-want:"/"+got:", then trailing context where the two sides
// realign. Kept separate from Golden so both outcomes are directly
// testable without needing to observe a *testing.T failure.
func compareGolden(want, got []byte) (diff string, ok bool) {
	if bytes.Equal(want, got) {
		return "", true
	}

	wantLines := strings.Split(string(want), "\n")
	gotLines := strings.Split(string(got), "\n")

	first := 0
	for first < len(wantLines) && first < len(gotLines) && wantLines[first] == gotLines[first] {
		first++
	}

	var b strings.Builder
	fmt.Fprintf(&b, "first difference at line %d\n", first+1)

	start := first - contextLines
	if start < 0 {
		start = 0
	}
	for i := start; i < first; i++ {
		fmt.Fprintf(&b, "  %s\n", lineAt(wantLines, i))
	}

	wantEnd := min(first+contextLines, len(wantLines))
	for i := first; i < wantEnd; i++ {
		fmt.Fprintf(&b, "-want: %s\n", lineAt(wantLines, i))
	}

	gotEnd := min(first+contextLines, len(gotLines))
	for i := first; i < gotEnd; i++ {
		fmt.Fprintf(&b, "+got:  %s\n", lineAt(gotLines, i))
	}

	// Trailing context: only meaningful once both sides realign again.
	after := first + contextLines
	afterEnd := after + contextLines
	for i := after; i < afterEnd && i < len(wantLines) && i < len(gotLines) && wantLines[i] == gotLines[i]; i++ {
		fmt.Fprintf(&b, "  %s\n", wantLines[i])
	}

	return b.String(), false
}

// lineAt returns lines[i], or "<EOF>" when i is past the end — one side of
// a diff can run out of lines before the other.
func lineAt(lines []string, i int) string {
	if i < 0 || i >= len(lines) {
		return "<EOF>"
	}
	return lines[i]
}
