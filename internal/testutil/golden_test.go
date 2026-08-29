package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompareGolden(t *testing.T) {
	tests := []struct {
		name      string
		want      []byte
		got       []byte
		wantOK    bool
		wantMarks []string // substrings required in diff when wantOK is false
	}{
		{
			name:   "identical",
			want:   []byte("a\nb\nc\n"),
			got:    []byte("a\nb\nc\n"),
			wantOK: true,
		},
		{
			name:      "single line differs",
			want:      []byte("a\nb\nc\n"),
			got:       []byte("a\nX\nc\n"),
			wantOK:    false,
			wantMarks: []string{"-want:", "+got:", "b", "X"},
		},
		{
			name:      "got has an extra trailing line",
			want:      []byte("a\nb\n"),
			got:       []byte("a\nb\nc\n"),
			wantOK:    false,
			wantMarks: []string{"-want:", "+got:"},
		},
		{
			name:      "empty vs non-empty",
			want:      []byte(""),
			got:       []byte("a\n"),
			wantOK:    false,
			wantMarks: []string{"-want:", "+got:"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diff, ok := compareGolden(tt.want, tt.got)
			if ok != tt.wantOK {
				t.Fatalf("compareGolden() ok = %v, want %v (diff=%q)", ok, tt.wantOK, diff)
			}
			if ok {
				if diff != "" {
					t.Fatalf("compareGolden() diff = %q, want empty on match", diff)
				}
				return
			}
			for _, m := range tt.wantMarks {
				if !strings.Contains(diff, m) {
					t.Fatalf("compareGolden() diff = %q, missing substring %q", diff, m)
				}
			}
		})
	}
}

// TestGolden_FailsOnMismatch proves the comparison logic behind Golden
// reports failure on a mismatch, with a line-level diff carrying clear
// -want/+got markers. It exercises compareGolden directly rather than
// Golden itself, since Golden's failure path calls t.Fatalf and there is no
// stdlib way to observe that without aborting this test too.
func TestGolden_FailsOnMismatch(t *testing.T) {
	diff, ok := compareGolden([]byte("line one\nline two\nline three\n"), []byte("line one\nCHANGED\nline three\n"))
	if ok {
		t.Fatalf("compareGolden() ok = true, want false for a mismatch")
	}
	if !strings.Contains(diff, "-want:") || !strings.Contains(diff, "+got:") {
		t.Fatalf("compareGolden() diff = %q, missing -want/+got markers", diff)
	}
	if !strings.Contains(diff, "line two") || !strings.Contains(diff, "CHANGED") {
		t.Fatalf("compareGolden() diff = %q, missing the differing content", diff)
	}
}

// TestGolden_UpdateRewritesFile proves Golden rewrites the golden file under
// -update instead of failing, including when the file does not exist yet.
func TestGolden_UpdateRewritesFile(t *testing.T) {
	orig := *updateFlag
	t.Cleanup(func() { *updateFlag = orig })
	*updateFlag = true

	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "out.golden")

	Golden(t, path, []byte("hello\n"))

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	if string(got) != "hello\n" {
		t.Fatalf("golden file = %q, want %q", got, "hello\n")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s): %v", path, err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Fatalf("golden file mode = %o, want 0644", perm)
	}

	// Calling Golden again with different content rewrites it, not fails.
	Golden(t, path, []byte("goodbye\n"))
	got2, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	if string(got2) != "goodbye\n" {
		t.Fatalf("golden file after rewrite = %q, want %q", got2, "goodbye\n")
	}
}

func TestGolden_UpdateNormalizesTrailingNewline(t *testing.T) {
	orig := *updateFlag
	t.Cleanup(func() { *updateFlag = orig })
	*updateFlag = true

	dir := t.TempDir()
	path := filepath.Join(dir, "normalize.golden")

	Golden(t, path, []byte("no-newline-here"))

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	if want := "no-newline-here\n"; string(got) != want {
		t.Fatalf("golden file = %q, want %q", got, want)
	}
}

func TestGolden_MatchPasses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "match.golden")
	if err := os.WriteFile(path, []byte("stable\n"), 0o644); err != nil {
		t.Fatalf("seed golden file: %v", err)
	}
	// Must not fail: got matches the file on disk exactly.
	Golden(t, path, []byte("stable\n"))
}

func TestGoldenString(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "str.golden")
	if err := os.WriteFile(path, []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("seed golden file: %v", err)
	}
	GoldenString(t, path, "hi\n")
}

// TestGolden_AgainstCheckedInFixture is a real, checked-in golden test —
// dogfooding FixedClock and Golden together — so `go test ./internal/testutil/...
// -update` has an actual file under internal/testutil/testdata/ to rewrite,
// per this subtask's own verification command.
func TestGolden_AgainstCheckedInFixture(t *testing.T) {
	got := fmt.Sprintf("frozen clock: %s\n", FixedClock()().Format(time.RFC3339))
	Golden(t, filepath.Join("testdata", "clock.golden"), []byte(got))
}

func TestUpdateEnabled(t *testing.T) {
	orig := *updateFlag
	t.Cleanup(func() { *updateFlag = orig })

	*updateFlag = false
	if UpdateEnabled() {
		t.Fatalf("UpdateEnabled() = true, want false")
	}

	*updateFlag = true
	if !UpdateEnabled() {
		t.Fatalf("UpdateEnabled() = false, want true")
	}
}
