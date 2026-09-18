package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

// TestIngestSourceUnicodeTitles pins A-805 end to end through the real
// stage.ingest_source -> Engine.Append -> validator: a name derived from a
// Unicode title must be a lowercase ASCII segment the validator accepts,
// never a path the ingest dies on. Before A-805 slugSourceName kept every
// unicode.IsLetter rune, so "Quaternion 四元數簡介" staged at
// raw/articles/quaternion-四元數簡介.md and the validator refused the op —
// the user's G5b acceptance run could not ingest that clipping at all.
// Every subtest asserts the tool result is not an error AND the open
// changeset's live op sits at the exact path.
func TestIngestSourceUnicodeTitles(t *testing.T) {
	t.Run("cjk_mixed_title_stages_ascii_path", func(t *testing.T) {
		reg, e := namingRegistry(t, nil, namingDoc("Quaternion 四元數簡介", "# Quaternion 四元數簡介\n\nQuaternion algebra, briefly.\n"))
		nameOpen(t, reg)
		if r := nameIngest(t, reg, "Quaternion 四元數簡介.md"); r.IsError {
			t.Fatalf("ingest refused: %s", r.Content)
		}
		if got := stagedOpPath(t, e, 0); got != "raw/articles/quaternion.md" {
			t.Fatalf("staged at %s, want raw/articles/quaternion.md", got)
		}
	})

	t.Run("cjk_only_title_uses_basename", func(t *testing.T) {
		reg, e := namingRegistry(t, nil, namingDoc("四元數簡介", "# Quaternion 四元數簡介\n\nA CJK-only title contributes nothing, so the file it came from names the source.\n"))
		nameOpen(t, reg)
		if r := nameIngest(t, reg, "/downloads/Quaternion Notes.md"); r.IsError {
			t.Fatalf("ingest refused: %s", r.Content)
		}
		if got := stagedOpPath(t, e, 0); got != "raw/articles/quaternion-notes.md" {
			t.Fatalf("staged at %s, want raw/articles/quaternion-notes.md", got)
		}
	})

	t.Run("cjk_title_and_basename_is_untitled", func(t *testing.T) {
		reg, e := namingRegistry(t, nil, namingDoc("四元數簡介", "No heading, a CJK-only file name, nothing slugifiable at all.\n"))
		nameOpen(t, reg)
		if r := nameIngest(t, reg, "/downloads/四元數.md"); r.IsError {
			t.Fatalf("ingest refused: %s", r.Content)
		}
		if got := stagedOpPath(t, e, 0); got != "raw/articles/untitled.md" {
			t.Fatalf("staged at %s, want raw/articles/untitled.md", got)
		}
	})

	t.Run("accented_and_emoji_titles_fold", func(t *testing.T) {
		docs := []*extract.Doc{
			namingDoc("Café Déjà Vu", "# Café Déjà Vu\n\nAccented Latin folds through NFKD.\n"),
			namingDoc("🚀 Rocket Notes 🚀", "# Rocket Notes\n\nEmoji contributes nothing but a dash.\n"),
		}
		reg, e := namingRegistry(t, nil, docs...)
		nameOpen(t, reg)
		if r := nameIngest(t, reg, "cafe.md"); r.IsError {
			t.Fatalf("first ingest refused: %s", r.Content)
		}
		if got := stagedOpPath(t, e, 0); got != "raw/articles/cafe-deja-vu.md" {
			t.Fatalf("first staged at %s, want raw/articles/cafe-deja-vu.md", got)
		}
		if r := nameIngest(t, reg, "rocket.md"); r.IsError {
			t.Fatalf("second ingest refused: %s", r.Content)
		}
		if got := stagedOpPath(t, e, 1); got != "raw/articles/rocket-notes.md" {
			t.Fatalf("second staged at %s, want raw/articles/rocket-notes.md", got)
		}
	})

	t.Run("folded_collision_gets_suffix_2", func(t *testing.T) {
		pre := []namedFile{{rel: "raw/articles/quaternion.md", body: "# Quaternion\n\nA committed article that merely shares the folded name.\n"}}
		reg, e := namingRegistry(t, pre, namingDoc("Quaternion 四元數", "# Quaternion 四元數\n\nA different body under the same folded slug.\n"))
		nameOpen(t, reg)
		r := nameIngest(t, reg, "Quaternion.md")
		if r.IsError {
			t.Fatalf("ingest refused: %s", r.Content)
		}
		if got := stagedOpPath(t, e, 0); got != "raw/articles/quaternion-2.md" {
			t.Fatalf("staged at %s, want raw/articles/quaternion-2.md", got)
		}
		want := "named raw/articles/quaternion-2.md because raw/articles/quaternion.md already holds a different source."
		if !strings.Contains(r.Content, want) {
			t.Errorf("result = %q, want it to contain %q", r.Content, want)
		}
	})

	t.Run("staged_unicode_sources_commit", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		docs := []*extract.Doc{
			namingDoc("Quaternion 四元數簡介", "---\ntitle: Quaternion 四元數簡介\n---\n\n四元數簡介: quaternions extend the complex numbers with i, j and k.\n"),
			namingDoc("Café Déjà Vu", "---\ntitle: Café Déjà Vu\n---\n\nAn accented title survives the commit verbatim.\n"),
		}
		reg, e := namingRegistryOn(t, dir, docs...)
		nameOpen(t, reg)
		if r := nameIngest(t, reg, "one.md"); r.IsError {
			t.Fatalf("first ingest refused: %s", r.Content)
		}
		if r := nameIngest(t, reg, "two.md"); r.IsError {
			t.Fatalf("second ingest refused: %s", r.Content)
		}
		if got := stagedOpPath(t, e, 0); got != "raw/articles/quaternion.md" {
			t.Fatalf("first op at %s, want raw/articles/quaternion.md", got)
		}
		if got := stagedOpPath(t, e, 1); got != "raw/articles/cafe-deja-vu.md" {
			t.Fatalf("second op at %s, want raw/articles/cafe-deja-vu.md", got)
		}
		if _, err := e.Commit("two unicode-titled sources"); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		for _, check := range []struct{ rel, want string }{
			{"raw/articles/quaternion.md", "四元數簡介"},
			{"raw/articles/cafe-deja-vu.md", "Café Déjà Vu"},
		} {
			b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(check.rel)))
			if err != nil {
				t.Fatalf("read %s: %v", check.rel, err)
			}
			if !strings.Contains(string(b), check.want) {
				t.Errorf("%s lost its original title %q; committed bytes:\n%s", check.rel, check.want, b)
			}
		}
	})
}

// stagedOpPath returns the path of the open changeset's i'th live op —
// where stage.ingest_source actually staged the source.
func stagedOpPath(t *testing.T, e *stage.Engine, i int) string {
	t.Helper()
	cs, err := e.Current()
	if err != nil {
		t.Fatal(err)
	}
	return cs.Ops[i].Path
}
