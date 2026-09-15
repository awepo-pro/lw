// frozen_test.go is the shared harness every T03 golden test in this
// package uses to compare against the artifacts copied verbatim into
// testdata/frozen/ (00-conventions.md §5, contract's frozen block): a
// plain read, never testutil.Golden, and the comparison is always
// ansi.Strip(render) + "\n".
package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// readFrozen returns the verbatim content of testdata/frozen/name.
func readFrozen(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "frozen", name))
	if err != nil {
		t.Fatalf("read frozen %s: %v", name, err)
	}
	return string(b)
}

// assertPlainMatchesFrozen strips got of ANSI, appends the trailing
// newline every frozen artifact ends with, and compares it byte for byte
// against testdata/frozen/name.
func assertPlainMatchesFrozen(t *testing.T, got string, name string) {
	t.Helper()
	want := readFrozen(t, name)
	plain := ansi.Strip(got) + "\n"
	if plain != want {
		t.Fatalf("%s mismatch:\n--- got ---\n%s\n--- want ---\n%s", name, plain, want)
	}
}

// joinLines is strings.Join(lines, "\n") — named for readability at call
// sites that build a view from Panel/tooSmallView's []string return.
func joinLines(lines []string) string {
	return strings.Join(lines, "\n")
}
