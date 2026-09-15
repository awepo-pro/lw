package stage

import (
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// TestOpDiffAttribution is the amended contract §1 note 4's suite (joint
// T01+T15 review C1, MASTER §8 ORCH-7): hunk ownership is carried through
// the traced reconstruction and correlated to line indices, never inferred
// from text — so merged windows split per owner, identical-text hunks stay
// distinct, every hunk is covered by at least one of its windows, and the
// traced bytes are exactly applyHunks'.
func TestOpDiffAttribution(t *testing.T) {
	// two_insert_only_hunks_same_section_split is C1's case 1: two
	// insert-only hunks naming the SAME section land adjacent
	// (insertAtSectionEnd puts each at the section's end, the second right
	// after the first's own lines), so hunkWindows merges all four '+' ops
	// into ONE window. The trace must split it into one DisplayHunk per
	// hunk, each carrying exactly its own '+' lines — text attribution
	// labelled the whole merged window h1, and y/n then acted on h1 while
	// both hunks were shown.
	t.Run("two_insert_only_hunks_same_section_split", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("same section", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
		if !ok {
			t.Fatal("fixture missing wiki/concepts/kv-cache.md")
		}
		hunks := []Hunk{
			{ID: "h1", Path: page.Path, Section: "## Example", Add: []string{"h1 note line.", ""}},
			{ID: "h2", Path: page.Path, Section: "## Example", Add: []string{"h2 note line.", ""}},
		}
		id, err := e.Append(Op{
			Kind:    OpPatchPage,
			Path:    page.Path,
			Section: "## Example",
			Before:  page.SHA256(),
			Content: applyHunks([]byte(page.Serialize()), hunks),
			Hunks:   hunks,
		})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}

		fods, err := e.OpDiff(id)
		if err != nil {
			t.Fatalf("OpDiff: %v", err)
		}
		fo := opDiffFileByPath(t, fods, page.Path)
		if len(fo.Hunks) != 2 {
			t.Fatalf("windows = %d, want 2 (one per hunk after the merged window is split by owner): %+v", len(fo.Hunks), fo.Hunks)
		}
		if fo.Hunks[0].HunkID != "h1" || fo.Hunks[1].HunkID != "h2" {
			t.Fatalf("HunkIDs = [%q, %q], want [h1 h2] in body order", fo.Hunks[0].HunkID, fo.Hunks[1].HunkID)
		}
		if fo.Hunks[0].Dropped || fo.Hunks[1].Dropped {
			t.Fatal("nothing was dropped; no window may be marked Dropped")
		}
		assertPlusTexts(t, fo.Hunks[0], "h1 note line.", "")
		assertPlusTexts(t, fo.Hunks[1], "h2 note line.", "")
	})

	// identical_text_hunks_attributed_by_position is C1's case 2: the SAME
	// Add text in two DIFFERENT sections. Text attribution returned the
	// first hunk whose Add contained the line, so BOTH windows were
	// labelled h1 and a y/n on the second mutated the first. The positional
	// trace labels each window with its own hunk, and each window's header
	// position matches its own hunk's section (h1's "## Why it matters"
	// anchor precedes h2's end-of-body one).
	t.Run("identical_text_hunks_attributed_by_position", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("identical text", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
		if !ok {
			t.Fatal("fixture missing wiki/concepts/kv-cache.md")
		}
		hunks := []Hunk{
			{ID: "h1", Path: page.Path, Section: "## Why it matters", Add: []string{"Shared added line.", ""}},
			{ID: "h2", Path: page.Path, Section: "## Related", Add: []string{"Shared added line.", ""}},
		}
		id, err := e.Append(Op{
			Kind:    OpPatchPage,
			Path:    page.Path,
			Section: "## Why it matters",
			Before:  page.SHA256(),
			Content: applyHunks([]byte(page.Serialize()), hunks),
			Hunks:   hunks,
		})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}

		fods, err := e.OpDiff(id)
		if err != nil {
			t.Fatalf("OpDiff: %v", err)
		}
		fo := opDiffFileByPath(t, fods, page.Path)
		// THREE windows: h1's insert, then an OWNERLESS one, then h2's.
		// h2 anchors at end of body — after the page's own trailing
		// newline — so that pre-existing blank becomes a visible '+' line
		// of its own. The trace proves it is inherited (both hunks insert
		// byte-identical blanks, so its owner is provably unnameable), and
		// it must carry NO id: folding it into h2's window would put a
		// line h2 does not own under h2's id, and y/n acts on that id.
		if len(fo.Hunks) != 3 {
			t.Fatalf("windows = %d, want 3 (h1, the ownerless trailing blank, h2): %+v", len(fo.Hunks), fo.Hunks)
		}
		if fo.Hunks[0].HunkID != "h1" || fo.Hunks[1].HunkID != "" || fo.Hunks[2].HunkID != "h2" {
			t.Fatalf("HunkIDs = [%q, %q, %q], want [h1 \"\" h2]: identical text must not swap the attribution, and the unowned blank must stay ownerless", fo.Hunks[0].HunkID, fo.Hunks[1].HunkID, fo.Hunks[2].HunkID)
		}
		_, _, newStart1, _ := parseHeader(t, fo.Hunks[0].Header)
		_, _, newStart2, _ := parseHeader(t, fo.Hunks[1].Header)
		_, _, newStart3, _ := parseHeader(t, fo.Hunks[2].Header)
		if !(newStart1 < newStart2 && newStart2 < newStart3) {
			t.Fatalf("windows must sit in body order (new starts %d, %d, %d): each window's header must match its own hunk's position", newStart1, newStart2, newStart3)
		}
		for _, w := range fo.Hunks {
			if w.Dropped {
				t.Fatalf("window %q must not be marked Dropped; nothing was dropped", w.HunkID)
			}
		}
		// Each window carries exactly its own lines: each owned window
		// exactly its own hunk's two added lines, the ownerless middle one
		// only the inherited blank.
		wantPlus := map[string][]string{
			"h1": {"Shared added line.", ""},
			"":   {""},
			"h2": {"Shared added line."},
		}
		for _, w := range fo.Hunks {
			var got []string
			for _, l := range w.Lines {
				if l.Kind == '+' {
					got = append(got, l.Text)
				}
			}
			if !reflect.DeepEqual(got, wantPlus[w.HunkID]) {
				t.Fatalf("window %q '+' texts = %q, want %q", w.HunkID, got, wantPlus[w.HunkID])
			}
		}
	})

	// drop_second_of_merged_shows_each_correctly drops h2 of the merged
	// same-section pair: the window still splits per owner, the h1 run is
	// live, and the h2 run is Dropped with its own content — so Review can
	// show (and undrop) exactly the hunk it displays.
	t.Run("drop_second_of_merged_shows_each_correctly", func(t *testing.T) {
		e, _ := newTestEngine(t)
		if _, err := e.OpenChangeset("drop second of merged", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
		if !ok {
			t.Fatal("fixture missing wiki/concepts/kv-cache.md")
		}
		hunks := []Hunk{
			{ID: "h1", Path: page.Path, Section: "## Example", Add: []string{"h1 note line.", ""}},
			{ID: "h2", Path: page.Path, Section: "## Example", Add: []string{"h2 note line.", ""}},
		}
		id, err := e.Append(Op{
			Kind:    OpPatchPage,
			Path:    page.Path,
			Section: "## Example",
			Before:  page.SHA256(),
			Content: applyHunks([]byte(page.Serialize()), hunks),
			Hunks:   hunks,
		})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		if err := e.DropHunk(id, "h2"); err != nil {
			t.Fatalf("DropHunk h2: %v", err)
		}

		fods, err := e.OpDiff(id)
		if err != nil {
			t.Fatalf("OpDiff: %v", err)
		}
		fo := opDiffFileByPath(t, fods, page.Path)
		if len(fo.Hunks) != 2 {
			t.Fatalf("windows = %d, want 2 (the merged window splits per owner even after a drop): %+v", len(fo.Hunks), fo.Hunks)
		}
		if fo.Hunks[0].HunkID != "h1" || fo.Hunks[1].HunkID != "h2" {
			t.Fatalf("HunkIDs = [%q, %q], want [h1 h2]", fo.Hunks[0].HunkID, fo.Hunks[1].HunkID)
		}
		if fo.Hunks[0].Dropped {
			t.Fatal("h1's run must be live")
		}
		if !fo.Hunks[1].Dropped {
			t.Fatal("h2's run must carry h2's Dropped flag")
		}
		assertPlusTexts(t, fo.Hunks[0], "h1 note line.", "")
		assertPlusTexts(t, fo.Hunks[1], "h2 note line.", "")
	})

	// every_hunk_covered pins note 4's coverage rule over a deterministic
	// generated op (fixed seed, 1-4 hunks): every Hunk.ID appears in at
	// least one DisplayHunk, and every owned '+'/'-' line in a window
	// belongs to the window's own hunk.
	t.Run("every_hunk_covered", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		fixtureRel := filepath.Join("wiki", "concepts", "generated-hunks-fixture.md")
		if err := os.WriteFile(filepath.Join(dir, fixtureRel), []byte(generatedHunksPage()), 0o644); err != nil {
			t.Fatalf("write generated-hunks-fixture.md: %v", err)
		}

		e, err := OpenEngine(dir)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		t.Cleanup(func() { e.Close() })
		e.now = testutil.FixedClock()

		if _, err := e.OpenChangeset("generated hunks", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		path := filepath.ToSlash(fixtureRel)
		page, ok := e.Vault().Page(path)
		if !ok {
			t.Fatalf("fixture page %s not loaded", path)
		}

		hunks := generateHunks(rand.New(rand.NewSource(1503)), path)
		id, err := e.Append(Op{
			Kind:    OpPatchPage,
			Path:    path,
			Section: "## Section 1",
			Before:  page.SHA256(),
			Content: applyHunks([]byte(page.Serialize()), hunks),
			Hunks:   hunks,
		})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}

		fods, err := e.OpDiff(id)
		if err != nil {
			t.Fatalf("OpDiff: %v", err)
		}
		fo := opDiffFileByPath(t, fods, path)
		covered := map[string]int{}
		for _, w := range fo.Hunks {
			if w.HunkID == "" {
				t.Fatal("a patch_page with owned hunks produced an ownerless window")
			}
			covered[w.HunkID]++
			var h *Hunk
			for i := range hunks {
				if hunks[i].ID == w.HunkID {
					h = &hunks[i]
					break
				}
			}
			if h == nil {
				t.Fatalf("window attributed to unknown hunk %q", w.HunkID)
			}
			for _, l := range w.Lines {
				switch l.Kind {
				case '+':
					if !lineIn(h.Add, l.Text) {
						t.Fatalf("window %s carries a '+' line hunk %s never added: %q", w.HunkID, h.ID, l.Text)
					}
				case '-':
					if !lineIn(h.Del, l.Text) {
						t.Fatalf("window %s carries a '-' line hunk %s never deleted: %q", w.HunkID, h.ID, l.Text)
					}
				}
			}
		}
		for _, h := range hunks {
			if covered[h.ID] == 0 {
				t.Fatalf("hunk %s appears in no DisplayHunk (covered: %v)", h.ID, covered)
			}
		}
	})

	attributionTraceSubtests(t)
	attributionOwnerSubtests(t)
}
