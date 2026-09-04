package stage

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// TestSnapshotRoundTrip is the stage file's headline snapshot test: a
// round-trip through WriteSnapshot/ReadSnapshot must be byte-identical,
// including a path containing a space and a non-ASCII path (backbone
// §5.8, MASTER §9 D-AW).
func TestSnapshotRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := Snapshot{
		"wiki/concepts/kv-cache.md":     strings.Repeat("a", 64),
		"wiki/notes/with space here.md": strings.Repeat("b", 64),
		"wiki/概念/知识图谱.md":               strings.Repeat("c", 64),
		"raw/source-01.txt":             strings.Repeat("d", 64),
	}

	if err := WriteSnapshot(dir, "000007", s); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}

	got, err := ReadSnapshot(dir, "000007")
	if err != nil {
		t.Fatalf("ReadSnapshot: %v", err)
	}

	if len(got) != len(s) {
		t.Fatalf("ReadSnapshot returned %d entries, want %d", len(got), len(s))
	}
	for p, sha := range s {
		g, ok := got[p]
		if !ok {
			t.Fatalf("path %q missing after round-trip", p)
		}
		if g != sha {
			t.Fatalf("path %q sha = %q, want %q", p, g, sha)
		}
	}

	// Byte-identical: re-derive the exact expected file content — sorted
	// by path, "<sha>  <path>\n" per line — and compare against what
	// WriteSnapshot actually wrote to disk.
	paths := make([]string, 0, len(s))
	for p := range s {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var want strings.Builder
	for _, p := range paths {
		want.WriteString(s[p])
		want.WriteString("  ")
		want.WriteString(p)
		want.WriteByte('\n')
	}

	raw, err := os.ReadFile(filepath.Join(dir, "000007.tree"))
	if err != nil {
		t.Fatalf("read .tree file: %v", err)
	}
	if string(raw) != want.String() {
		t.Fatalf(".tree file bytes =\n%q\nwant\n%q", raw, want.String())
	}
}

// TestSnapshotEmpty proves an empty Snapshot round-trips to a zero-byte
// .tree file and back to an empty map, without error.
func TestSnapshotEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := WriteSnapshot(dir, "000001", Snapshot{}); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "000001.tree"))
	if err != nil {
		t.Fatalf("stat .tree: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("empty snapshot file size = %d, want 0", info.Size())
	}

	got, err := ReadSnapshot(dir, "000001")
	if err != nil {
		t.Fatalf("ReadSnapshot: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ReadSnapshot(empty) = %v, want empty", got)
	}
}

// TestReadSnapshotMissing proves reading a commit id with no .tree file
// returns an error rather than an empty Snapshot — silence there would
// hide a lost commit from Revert.
func TestReadSnapshotMissing(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadSnapshot(dir, "999999"); err == nil {
		t.Fatal("ReadSnapshot of a missing commit id returned no error")
	}
}

// TestSnapshotMalformedLine proves ReadSnapshot treats a malformed .tree
// line as an error — unlike the journal's skip-and-continue — because a
// silently dropped snapshot entry is a file Revert would lose (backbone
// §5.8, MASTER §9 D-AW).
func TestSnapshotMalformedLine(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"line far too short", "abcd  x.md\n"},
		{"one-space separator", strings.Repeat("a", 64) + " x.md\n"},
		{"uppercase hex sha", strings.Repeat("A", 64) + "  x.md\n"},
		{"non-hex sha", strings.Repeat("g", 64) + "  x.md\n"},
		{"empty path", strings.Repeat("a", 64) + "  \n"},
		{"blank line in the middle", strings.Repeat("a", 64) + "  x.md\n\n" + strings.Repeat("b", 64) + "  y.md\n"},
		{"blank line via trailing double newline", strings.Repeat("a", 64) + "  x.md\n\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "000001.tree"), []byte(tt.content), 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			if _, err := ReadSnapshot(dir, "000001"); err == nil {
				t.Fatalf("ReadSnapshot on malformed content %q returned no error", tt.content)
			}
		})
	}
}

// TestSnapshotDoesNotUseFields proves the D-AW fixed-offset requirement
// directly: a path containing an internal run of spaces must survive
// intact, which strings.Fields (or a split on the first space run) would
// break by treating the run as more separators or truncating the path.
func TestSnapshotDoesNotUseFields(t *testing.T) {
	dir := t.TempDir()
	s := Snapshot{
		"wiki/a   b   c.md": strings.Repeat("e", 64),
	}
	if err := WriteSnapshot(dir, "000002", s); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}
	got, err := ReadSnapshot(dir, "000002")
	if err != nil {
		t.Fatalf("ReadSnapshot: %v", err)
	}
	if sha, ok := got["wiki/a   b   c.md"]; !ok || sha != strings.Repeat("e", 64) {
		t.Fatalf("ReadSnapshot = %v, want the internal-space path preserved whole", got)
	}
}

// TestEngineSnapshot proves Engine.Snapshot is the thin wrapper backbone
// §5.4/§5.8 (MASTER §9 D-AT) describes: ReadSnapshot over
// e.llmwikiDir()/snapshots.
func TestEngineSnapshot(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")

	e, err := OpenEngine(dir)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()

	want := Snapshot{
		"wiki/concepts/kv-cache.md": strings.Repeat("1", 64),
		"raw/example.txt":           strings.Repeat("2", 64),
	}
	snapshotsDir := filepath.Join(dir, ".llmwiki", "snapshots")
	if err := WriteSnapshot(snapshotsDir, "000001", want); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}

	got, err := e.Snapshot("000001")
	if err != nil {
		t.Fatalf("Engine.Snapshot: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("Engine.Snapshot returned %d entries, want %d", len(got), len(want))
	}
	for p, sha := range want {
		if got[p] != sha {
			t.Fatalf("Engine.Snapshot()[%q] = %q, want %q", p, got[p], sha)
		}
	}

	if _, err := e.Snapshot("999999"); err == nil {
		t.Fatal("Engine.Snapshot of a missing commit id returned no error")
	}
}

// TestWriteSnapshotRejectsWhatReadCannotParse locks the write/read symmetry
// backbone §5.8 (D-AW) describes and MASTER §10 OR-4 repaired: WriteSnapshot
// used to accept entries ReadSnapshot then refused, so a malformed sha wrote
// cleanly at Commit time and surfaced only at Revert, blaming the reader.
func TestWriteSnapshotRejectsWhatReadCannotParse(t *testing.T) {
	good := strings.Repeat("a", 64)
	cases := map[string]Snapshot{
		"sha too short":     {"wiki/a.md": strings.Repeat("a", 63)},
		"sha too long":      {"wiki/a.md": strings.Repeat("a", 65)},
		"sha uppercase":     {"wiki/a.md": strings.ToUpper(good)},
		"sha not hex":       {"wiki/a.md": strings.Repeat("z", 64)},
		"sha empty":         {"wiki/a.md": ""},
		"path empty":        {"": good},
		"path with newline": {"wiki/a\nb.md": good},
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := WriteSnapshot(dir, "000001", s); err == nil {
				t.Fatal("WriteSnapshot accepted an entry ReadSnapshot cannot parse")
			}
			if _, err := os.Stat(filepath.Join(dir, "000001.tree")); !os.IsNotExist(err) {
				t.Fatal("a rejected WriteSnapshot still created the .tree file")
			}
			ents, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(ents) != 0 {
				t.Fatalf("a rejected WriteSnapshot left %d files behind", len(ents))
			}
		})
	}

	// And everything it accepts still round-trips.
	dir := t.TempDir()
	ok := Snapshot{"wiki/a b.md": good, "wiki/概念.md": strings.Repeat("f", 64)}
	if err := WriteSnapshot(dir, "000002", ok); err != nil {
		t.Fatalf("WriteSnapshot rejected a valid snapshot: %v", err)
	}
	got, err := ReadSnapshot(dir, "000002")
	if err != nil {
		t.Fatalf("ReadSnapshot: %v", err)
	}
	for p, sha := range ok {
		if got[p] != sha {
			t.Fatalf("round-trip lost %q", p)
		}
	}
}
