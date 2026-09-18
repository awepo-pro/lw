package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReliabilityUnicodeTitle is C-815 end to end: the exact shape of the
// user's G5b clipping — a file literally named "Quaternion 四元數簡介.md"
// whose body opens with YAML frontmatter carrying the CJK title and has no
// "# " heading — must ingest, diff and commit, at an ASCII path the
// validator accepts, with the full CJK title kept inside the file. Scripted
// like TestReliabilityUntitledCollision; no ambient provider key.
func TestReliabilityUnicodeTitle(t *testing.T) {
	e := newEnv(t)
	vault := newVault(t)

	src := e.writeSource(t, "Quaternion 四元數簡介.md",
		"---\ntitle: Quaternion 四元數簡介\n---\n\n四元數簡介 — quaternions extend the complex numbers with two more imaginary units; this clipping walks through i, j and k.\n")

	fake := newFakeLLM(t,
		stageIngestScratchSSE("uni1", 1),
		stopSSE("uni1", "Staged the quaternion clipping; no page proposed, so the changeset is raw-only."),
	)
	writeConfig(t, e.config, fake.URL()+"/v1")

	ingest := runLW(t, e, "ingest", "--vault", vault, "--kind", "article", src)

	t.Run("ingest_exits_zero", func(t *testing.T) {
		if ingest.Code != 0 {
			t.Fatalf("lw ingest (unicode-titled source): exit %d, want 0\n%s", ingest.Code, ingest.Output)
		}
		assertNoPanic(t, ingest.Output)
	})

	t.Run("diff_names_the_ascii_path", func(t *testing.T) {
		diff := runLW(t, e, "diff", "--vault", vault, "--stat")
		if diff.Code != 0 {
			t.Fatalf("lw diff --stat: exit %d, want 0\n%s", diff.Code, diff.Output)
		}
		if !strings.Contains(diff.Output, "raw/articles/quaternion.md") {
			t.Errorf("lw diff --stat does not name raw/articles/quaternion.md\n%s", diff.Output)
		}
	})

	t.Run("commit_writes_the_file_with_its_title", func(t *testing.T) {
		commit := runLW(t, e, "commit", "--vault", vault, "-m", "quaternion clipping")
		if commit.Code != 0 {
			t.Fatalf("lw commit: exit %d, want 0\n%s", commit.Code, commit.Output)
		}
		b, err := os.ReadFile(filepath.Join(vault, "raw", "articles", "quaternion.md"))
		if err != nil {
			t.Fatalf("committed raw/articles/quaternion.md is missing: %v\n%s", err, commit.Output)
		}
		if !strings.Contains(string(b), "四元數簡介") {
			t.Errorf("committed raw/articles/quaternion.md no longer carries the CJK title:\n%s", b)
		}
	})
}
