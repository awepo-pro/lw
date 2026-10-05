// cascade_hunk_position_test.go is 052 S1b's frozen suite: the hunks a
// rename or merge cascade builds for a backlink rewrite carry their position
// (At + Lines) like every ComputeHunks hunk does, so a Review drop or undrop
// of one rewrite touches exactly its own line. Before, buildCascadeHunks
// wrote only Del/Add, and applying a hunk found its Del at the first textual
// match — with the same link line twice in a page, dropping the first
// rewrite rewrote the second line instead (TD-15, probed after 052 S1).
package stage

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// TestCascadeHunkPositioned is the probe that found the gap: identical lines
// "x [[a]]\nmid\nx [[a]]\n" rewritten to [[b]]. Each cascade hunk must carry
// the 1-based line it rewrites and a two-line window, and dropping one must
// leave the OTHER rewrite in the other line.
func TestCascadeHunkPositioned(t *testing.T) {
	const (
		old = "x [[a]]\nmid\nx [[a]]\n"
		new = "x [[b]]\nmid\nx [[b]]\n"
	)
	hunks := buildCascadeHunks("wiki/p.md", old, new)
	if len(hunks) != 2 {
		t.Fatalf("buildCascadeHunks gave %d hunks, want 2", len(hunks))
	}
	for i, wantAt := range []int{1, 3} {
		h := hunks[i]
		if h.At != wantAt {
			t.Fatalf("hunk %s: At = %d, want %d", h.ID, h.At, wantAt)
		}
		if want := []string{"-x [[a]]", "+x [[b]]"}; !reflect.DeepEqual(h.Lines, want) {
			t.Fatalf("hunk %s: Lines = %q, want %q", h.ID, h.Lines, want)
		}
		// The display and legacy fields are exactly what they were.
		if h.Path != "wiki/p.md" || !reflect.DeepEqual(h.Del, []string{"x [[a]]"}) || !reflect.DeepEqual(h.Add, []string{"x [[b]]"}) {
			t.Fatalf("hunk %s: Path/Del/Add changed: %+v", h.ID, h)
		}
	}

	cases := []struct {
		name    string
		dropped [2]bool
		want    string
	}{
		{"both live", [2]bool{false, false}, new},
		{"h1 dropped, h2 live rewrites the second line", [2]bool{true, false}, "x [[a]]\nmid\nx [[b]]\n"},
		{"h2 dropped, h1 live rewrites the first line", [2]bool{false, true}, "x [[b]]\nmid\nx [[a]]\n"},
		{"both dropped", [2]bool{true, true}, old},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hs := make([]Hunk, len(hunks))
			copy(hs, hunks)
			hs[0].Dropped, hs[1].Dropped = tc.dropped[0], tc.dropped[1]
			if got := string(applyHunks([]byte(old), hs)); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// cascadeFixturePath is a page that links to kv-cache on two identical
// lines, so renaming kv-cache rewrites both and the two rewrites are
// textually indistinguishable.
const cascadeFixturePath = "wiki/concepts/cascade-fixture.md"

const cascadeFixtureBefore = "---\n" +
	"title: Cascade Fixture\n" +
	"created: 2026-08-29\n" +
	"updated: 2026-08-29\n" +
	"type: concept\n" +
	"tags: [inference]\n" +
	"confidence: medium\n" +
	"---\n" +
	"\n" +
	"# Cascade Fixture\n" +
	"\n" +
	"See [[kv-cache]] for background.\n" +
	"\n" +
	"A middle paragraph.\n" +
	"\n" +
	"See [[kv-cache]] for background.\n" +
	"\n" +
	"Closing paragraph linking to [[gpt-4]].\n"

// cascadeEngine opens an Engine over a private copy of the "minimal" fixture
// with cascadeFixtureBefore written in as a page that links to kv-cache.
func cascadeEngine(t *testing.T) *Engine {
	t.Helper()
	dir := testutil.CopyFixture(t, "minimal")
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(cascadeFixturePath)), []byte(cascadeFixtureBefore), 0o644); err != nil {
		t.Fatalf("write %s: %v", cascadeFixturePath, err)
	}
	e, err := OpenEngine(dir)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	e.now = testutil.FixedClock()
	page, ok := e.Vault().Page(cascadeFixturePath)
	if !ok {
		t.Fatalf("fixture page %s not loaded", cascadeFixturePath)
	}
	if got := string(page.Serialize()); got != cascadeFixtureBefore {
		t.Fatalf("cascadeFixtureBefore is not canonical:\n%s", got)
	}
	return e
}

// cascadeSubOps returns op's Cascade tree flattened, depth first.
func cascadeSubOps(op Op) []Op {
	var out []Op
	for _, sub := range op.Cascade {
		out = append(out, sub)
		out = append(out, cascadeSubOps(sub)...)
	}
	return out
}

// TestCascadeHunkRoundTripRename stages a real rename whose cascade rewrites
// every inbound link — pages with frontmatter, vault-root files without — and
// proves, for every cascade sub-op: its hunks sit on the lines of the op's
// real before file (frontmatter included, not body-relative), n then y on any
// hunk is byte-identical, n on one hunk gives exactly the other rewrites, and
// n on all gives the before bytes. The fixture page's two identical link
// lines are the case first-text-match got wrong.
func TestCascadeHunkRoundTripRename(t *testing.T) {
	e := cascadeEngine(t)
	if _, err := e.OpenChangeset("rename with duplicate link lines", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	renameID, err := e.Append(Op{Kind: OpRenamePage, From: "wiki/concepts/kv-cache.md", To: "wiki/concepts/kv-caching.md"})
	if err != nil {
		t.Fatalf("Append rename: %v", err)
	}
	c, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	rename, _ := c.Op(renameID)
	subs := cascadeSubOps(*rename)

	var fixture *Op
	for i := range subs {
		if subs[i].Path == cascadeFixturePath {
			fixture = &subs[i]
		}
	}
	if fixture == nil {
		t.Fatalf("the cascade does not cover %s: %d sub-ops", cascadeFixturePath, len(subs))
	}
	if len(fixture.Hunks) != 2 {
		t.Fatalf("the fixture page's cascade has %d hunks, want 2: %+v", len(fixture.Hunks), fixture.Hunks)
	}
	// The two identical lines are file lines 12 and 16 — body-relative
	// positions (3 and 7) would be the frontmatter-offset bug.
	for i, wantAt := range []int{12, 16} {
		if fixture.Hunks[i].At != wantAt {
			t.Fatalf("fixture hunk %s: At = %d, want %d (a line of the whole file)", fixture.Hunks[i].ID, fixture.Hunks[i].At, wantAt)
		}
	}

	sawRootFile := false
	for _, sub := range subs {
		if sub.Path == "index.md" {
			sawRootFile = true
		}
		t.Run(sub.Path, func(t *testing.T) {
			if len(sub.Hunks) == 0 {
				t.Fatal("a cascade sub-op with no hunks")
			}
			beforeBytes, err := e.store.Get(sub.Before)
			if err != nil {
				t.Fatalf("Store.Get(Before): %v", err)
			}
			before := string(beforeBytes)
			want := string(currentOpAfterBytes(t, e, sub.ID))
			for _, h := range sub.Hunks {
				if h.At < 1 || !reflect.DeepEqual(h.Lines, []string{"-" + h.Del[0], "+" + h.Add[0]}) {
					t.Fatalf("hunk %s is not positioned as a one-line rewrite: At=%d Lines=%q", h.ID, h.At, h.Lines)
				}
			}
			// The positions sit on the real before file, and reproduce the after file.
			if got := hunkPosReassemble(t, before, sub.Hunks, func(int) bool { return true }); got != want {
				t.Fatalf("At+Lines do not reassemble the after file:\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}

			for i, h := range sub.Hunks {
				if err := e.DropHunk(sub.ID, h.ID); err != nil {
					t.Fatalf("DropHunk %s: %v", h.ID, err)
				}
				alone := string(currentOpAfterBytes(t, e, sub.ID))
				if oracle := hunkPosReassemble(t, before, sub.Hunks, func(k int) bool { return k != i }); alone != oracle {
					t.Fatalf("dropping %s alone != the other rewrites:\n--- got ---\n%s\n--- want ---\n%s", h.ID, alone, oracle)
				}
				if err := e.UndropHunk(sub.ID, h.ID); err != nil {
					t.Fatalf("UndropHunk %s: %v", h.ID, err)
				}
				if got := string(currentOpAfterBytes(t, e, sub.ID)); got != want {
					t.Fatalf("drop+undrop of %s changed the op:\n--- got ---\n%s\n--- want ---\n%s", h.ID, got, want)
				}
			}
			for _, h := range sub.Hunks {
				if err := e.DropHunk(sub.ID, h.ID); err != nil {
					t.Fatalf("DropHunk %s: %v", h.ID, err)
				}
			}
			if got := string(currentOpAfterBytes(t, e, sub.ID)); got != before {
				t.Fatalf("dropping every hunk != the before file:\n%s", got)
			}
			for i := len(sub.Hunks) - 1; i >= 0; i-- {
				if err := e.UndropHunk(sub.ID, sub.Hunks[i].ID); err != nil {
					t.Fatalf("UndropHunk %s: %v", sub.Hunks[i].ID, err)
				}
			}
			if got := string(currentOpAfterBytes(t, e, sub.ID)); got != want {
				t.Fatalf("undropping every hunk != the proposal:\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}
		})
	}
	if !sawRootFile {
		t.Fatal("the cascade has no vault-root sub-op (index.md): the raw-file path was not exercised")
	}

	// The fixture's own after file, spelled out: both lines rewritten.
	want := strings.ReplaceAll(cascadeFixtureBefore, "[[kv-cache]]", "[[kv-caching]]")
	if got := string(currentOpAfterBytes(t, e, fixture.ID)); got != want {
		t.Fatalf("fixture page after the round trips:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
