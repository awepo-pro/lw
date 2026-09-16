package stage

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestOpDiffMockupVault asserts the frozen T01 table (MASTER §5) against
// the private mockup vault. Skipped unless LW_MOCKUP_VAULT is set — the
// vault holds third-party text and local paths and must never enter the
// repo, so it is copied to a t.TempDir() and read only from there.
func TestOpDiffMockupVault(t *testing.T) {
	src := os.Getenv("LW_MOCKUP_VAULT")
	if src == "" {
		t.Skip("LW_MOCKUP_VAULT not set: this conformance check runs locally against the private mockup vault")
	}

	dst := filepath.Join(t.TempDir(), "ml-notes")
	if err := copyDirRecursive(src, dst); err != nil {
		t.Fatalf("copy mockup vault: %v", err)
	}

	e, err := OpenEngine(dst)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { e.Close() })

	if err := e.DropHunk("op4", "h1"); err != nil {
		t.Fatalf("DropHunk(op4, h1): %v", err)
	}

	type wantCounts struct{ spaces, plus, minus int } // -1 means "don't check"
	type wantWin struct {
		header  string // "" means "don't check"
		hunkID  string
		dropped bool
		counts  wantCounts
	}
	type wantFile struct {
		path string
		wins []wantWin
	}

	cases := []struct {
		op    string
		files []wantFile
	}{
		{"op1", []wantFile{
			{"raw/articles/claude-in-a-box.md", []wantWin{
				{"@@ -0,0 +1,286 @@", "", false, wantCounts{0, 286, 0}},
			}},
		}},
		{"op2", []wantFile{
			{"wiki/concepts/network-namespace-egress-isolation.md", []wantWin{
				{"@@ -0,0 +1,52 @@", "", false, wantCounts{0, 52, 0}},
			}},
			{"index.md", []wantWin{
				// The frozen table pins only "exactly 1 +" for this window;
				// context ('- '') line counts are unspecified.
				{"", "", false, wantCounts{-1, 1, -1}},
			}},
		}},
		{"op3", []wantFile{
			{"wiki/concepts/private-network-access.md", []wantWin{
				{"@@ -34,6 +34,10 @@", "h1", false, wantCounts{6, 4, 0}},
			}},
		}},
		{"op4", []wantFile{
			{"wiki/entities/claude.md", []wantWin{
				{"@@ -21,6 +21,10 @@", "h1", true, wantCounts{6, 4, 0}},
			}},
		}},
	}

	totalNonDropped, totalDropped := 0, 0
	for _, tc := range cases {
		fods, err := e.OpDiff(tc.op)
		if err != nil {
			t.Fatalf("OpDiff(%s): %v", tc.op, err)
		}
		if len(fods) != len(tc.files) {
			t.Fatalf("OpDiff(%s) returned %d files, want %d: %+v", tc.op, len(fods), len(tc.files), fods)
		}
		for i, wf := range tc.files {
			fo := fods[i]
			if fo.Path != wf.path {
				t.Fatalf("OpDiff(%s) file[%d].Path = %q, want %q", tc.op, i, fo.Path, wf.path)
			}
			if len(fo.Hunks) != len(wf.wins) {
				t.Fatalf("OpDiff(%s) %s has %d windows, want %d: %+v", tc.op, wf.path, len(fo.Hunks), len(wf.wins), fo.Hunks)
			}
			for j, ww := range wf.wins {
				win := fo.Hunks[j]
				if ww.header != "" && win.Header != ww.header {
					t.Fatalf("OpDiff(%s) %s window[%d].Header = %q, want %q", tc.op, wf.path, j, win.Header, ww.header)
				}
				if win.HunkID != ww.hunkID {
					t.Fatalf("OpDiff(%s) %s window[%d].HunkID = %q, want %q", tc.op, wf.path, j, win.HunkID, ww.hunkID)
				}
				if win.Dropped != ww.dropped {
					t.Fatalf("OpDiff(%s) %s window[%d].Dropped = %v, want %v", tc.op, wf.path, j, win.Dropped, ww.dropped)
				}
				var got wantCounts
				for _, l := range win.Lines {
					switch l.Kind {
					case ' ':
						got.spaces++
					case '+':
						got.plus++
					case '-':
						got.minus++
					}
				}
				if ww.counts.spaces != -1 && got.spaces != ww.counts.spaces {
					t.Fatalf("OpDiff(%s) %s window[%d] ' ' count = %d, want %d", tc.op, wf.path, j, got.spaces, ww.counts.spaces)
				}
				if ww.counts.plus != -1 && got.plus != ww.counts.plus {
					t.Fatalf("OpDiff(%s) %s window[%d] '+' count = %d, want %d", tc.op, wf.path, j, got.plus, ww.counts.plus)
				}
				if ww.counts.minus != -1 && got.minus != ww.counts.minus {
					t.Fatalf("OpDiff(%s) %s window[%d] '-' count = %d, want %d", tc.op, wf.path, j, got.minus, ww.counts.minus)
				}
				if win.Dropped {
					totalDropped++
				} else {
					totalNonDropped++
				}
			}
		}
	}

	if totalNonDropped != 4 || totalDropped != 1 {
		t.Fatalf("totals across live ops = %d non-dropped, %d dropped windows; want 4 and 1", totalNonDropped, totalDropped)
	}
}

// copyDirRecursive recursively copies src into dst, creating directories as
// needed (this test file's own copy helper — the mockup vault lives outside
// spec/fixtures/, so testutil.CopyFixture, which only ever reads under
// spec/fixtures/, does not apply).
func copyDirRecursive(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		}

		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}
