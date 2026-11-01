package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

func TestNote_F1_Resolver(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	notesDir := filepath.Join(root, vault.NotesDir)
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	p1 := filepath.Join(notesDir, "20261013-121933-max-combination.md")
	if err := os.WriteFile(p1, []byte("# Max Combination\n\nContent.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p2 := filepath.Join(notesDir, "20261013-130000-rtt.md")
	if err := os.WriteFile(p2, []byte("# RTT\n\nContent.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p3 := filepath.Join(notesDir, "20261014-090000-rtt-two.md")
	if err := os.WriteFile(p3, []byte("# RTT Two\n\nContent.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(notesDir, "20261015-sub.md")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notesDir, "x.txt"), []byte("non md\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.md")
	if err := os.WriteFile(outsideFile, []byte("outside content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(notesDir, "evil.md")); err != nil {
		t.Fatal(err)
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	expectedP1 := filepath.Join(absRoot, vault.NotesDir, "20261013-121933-max-combination.md")

	t.Run("exact stem", func(t *testing.T) {
		got, err := resolveNoteID(root, "20261013-121933-max-combination")
		if err != nil {
			t.Fatalf("resolve exact stem: %v", err)
		}
		if got != expectedP1 {
			t.Errorf("got %q, want %q", got, expectedP1)
		}
	})

	t.Run("trailing .md accepted", func(t *testing.T) {
		got, err := resolveNoteID(root, "20261013-121933-max-combination.md")
		if err != nil {
			t.Fatalf("resolve trailing .md: %v", err)
		}
		if got != expectedP1 {
			t.Errorf("got %q, want %q", got, expectedP1)
		}
	})

	t.Run("unique prefix", func(t *testing.T) {
		got, err := resolveNoteID(root, "20261013-1219")
		if err != nil {
			t.Fatalf("resolve unique prefix: %v", err)
		}
		if got != expectedP1 {
			t.Errorf("got %q, want %q", got, expectedP1)
		}
	})

	t.Run("ambiguous prefix", func(t *testing.T) {
		_, err := resolveNoteID(root, "20261013")
		if err == nil {
			t.Fatal("expected error for ambiguous prefix 20261013, got nil")
		}
		errMsg := err.Error()
		if !strings.Contains(errMsg, "20261013-121933-max-combination") || !strings.Contains(errMsg, "20261013-130000-rtt") {
			t.Errorf("error %q does not contain both matching stems", errMsg)
		}
	})

	t.Run("ambiguous prefix with 7 matches", func(t *testing.T) {
		vault7 := testutil.CopyFixture(t, "minimal")
		n7Dir := filepath.Join(vault7, vault.NotesDir)
		if err := os.MkdirAll(n7Dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for i := 1; i <= 7; i++ {
			fn := filepath.Join(n7Dir, fmt.Sprintf("20269999-0%d-stem%d.md", i, i))
			if err := os.WriteFile(fn, []byte("test\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		_, err := resolveNoteID(vault7, "20269999")
		if err == nil {
			t.Fatal("expected error for 7 matches, got nil")
		}
		errMsg := err.Error()
		if !strings.Contains(errMsg, "and 2 more") {
			t.Errorf("expected 'and 2 more' in error, got: %q", errMsg)
		}
		for i := 1; i <= 5; i++ {
			stem := fmt.Sprintf("20269999-0%d-stem%d", i, i)
			if !strings.Contains(errMsg, stem) {
				t.Errorf("expected first 5 stem %q in error: %q", stem, errMsg)
			}
		}
		if strings.Contains(errMsg, "20269999-06-stem6") || strings.Contains(errMsg, "20269999-07-stem7") {
			t.Errorf("stems past 5 should not appear in error: %q", errMsg)
		}
	})

	t.Run("substring not prefix", func(t *testing.T) {
		_, err := resolveNoteID(root, "rtt")
		if err == nil {
			t.Fatal("expected error for substring rtt, got nil")
		}
		if !strings.Contains(err.Error(), "notes/") || !strings.Contains(err.Error(), "lw note list") {
			t.Errorf("error %q should contain 'notes/' and 'lw note list'", err.Error())
		}
	})

	t.Run("directory not found", func(t *testing.T) {
		_, err := resolveNoteID(root, "20261015-sub")
		if err == nil {
			t.Fatal("expected error for directory stem, got nil")
		}
		if !strings.Contains(err.Error(), "notes/") || !strings.Contains(err.Error(), "lw note list") {
			t.Errorf("error %q should contain 'notes/' and 'lw note list'", err.Error())
		}
	})

	t.Run("non-md not found", func(t *testing.T) {
		_, err := resolveNoteID(root, "x")
		if err == nil {
			t.Fatal("expected error for non-md stem, got nil")
		}
		if !strings.Contains(err.Error(), "notes/") || !strings.Contains(err.Error(), "lw note list") {
			t.Errorf("error %q should contain 'notes/' and 'lw note list'", err.Error())
		}
	})

	t.Run("no match", func(t *testing.T) {
		_, err := resolveNoteID(root, "nonexistent-note")
		if err == nil {
			t.Fatal("expected error for nonexistent note, got nil")
		}
		if !strings.Contains(err.Error(), "notes/") || !strings.Contains(err.Error(), "lw note list") {
			t.Errorf("error %q should contain 'notes/' and 'lw note list'", err.Error())
		}
	})

	t.Run("refused bad ids", func(t *testing.T) {
		badIDs := []string{"", "../x", "a/b", "a\\b", "..", "/abs", ".md"}
		for _, bad := range badIDs {
			_, err := resolveNoteID(root, bad)
			if err == nil {
				t.Errorf("expected error for bad id %q, got nil", bad)
				continue
			}
			if !strings.Contains(err.Error(), "invalid note id") {
				t.Errorf("error for bad id %q = %q, want containing 'invalid note id'", bad, err.Error())
			}
		}
	})

	t.Run("vault with no notes dir", func(t *testing.T) {
		emptyVault := testutil.CopyFixture(t, "minimal")
		_ = os.RemoveAll(filepath.Join(emptyVault, vault.NotesDir))
		_, err := resolveNoteID(emptyVault, "20261013-121933-max-combination")
		if err == nil {
			t.Fatal("expected error for vault with no notes/ dir, got nil")
		}
		if !strings.Contains(err.Error(), "notes/") || !strings.Contains(err.Error(), "lw note list") {
			t.Errorf("error %q should contain 'notes/' and 'lw note list'", err.Error())
		}
	})

	t.Run("symlink outside vault not matched", func(t *testing.T) {
		_, err := resolveNoteID(root, "evil")
		if err == nil {
			t.Fatal("expected error for symlink evil, got nil")
		}
		if !strings.Contains(err.Error(), "notes/") || !strings.Contains(err.Error(), "lw note list") {
			t.Errorf("error %q should contain 'notes/' and 'lw note list'", err.Error())
		}
	})
}

func TestNote_F2_FlagCLI(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	notesDir := filepath.Join(root, vault.NotesDir)
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	noteA := filepath.Join(notesDir, "note-a.md")
	if err := os.WriteFile(noteA, []byte("# Note A\n\nContent A.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	noteB := filepath.Join(notesDir, "note-b.md")
	if err := os.WriteFile(noteB, []byte("# Note B\n\nContent B.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	absNoteA := filepath.Join(absRoot, vault.NotesDir, "note-a.md")
	absNoteB := filepath.Join(absRoot, vault.NotesDir, "note-b.md")

	t.Run("dry run from another cwd", func(t *testing.T) {
		otherDir := t.TempDir()
		t.Chdir(otherDir)

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, "--note", "note-a", "--dry-run"})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		want := "would ingest " + absNoteA
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout does not contain %q\nstdout:\n%s", want, stdout)
		}
	})

	t.Run("repeatable flag order after positional sources", func(t *testing.T) {
		posPath := writtenSource(t, "pos.md", "# Positional\n\nPositional content.\n")
		absPos, err := filepath.Abs(posPath)
		if err != nil {
			t.Fatal(err)
		}

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, posPath, "--note", "note-a", "--note", "note-b", "--dry-run"})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		prepIndexesInOrder(t, stdout, "would ingest "+absPos, "would ingest "+absNoteA, "would ingest "+absNoteB)
	})

	t.Run("note with no positional source valid and neither usage", func(t *testing.T) {
		// Valid with --note and no positional source
		_, _, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, "--note", "note-a", "--dry-run"})
		})
		if code != 0 {
			t.Fatalf("exit code with --note = %d, want 0", code)
		}

		// Exit 2 with neither positional source nor --note
		_, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root})
		})
		if code != 2 {
			t.Fatalf("exit code without sources or notes = %d, want 2", code)
		}
		if !strings.Contains(stderr, "usage: lw ingest") {
			t.Errorf("stderr missing usage text:\n%s", stderr)
		}
	})

	t.Run("note with recompile exits 2", func(t *testing.T) {
		_, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, "--note", "note-a", "--recompile"})
		})
		if code != 2 {
			t.Fatalf("exit code = %d, want 2", code)
		}
		wantMsg := "lw: ingest: cannot use --note with --recompile"
		if !strings.Contains(stderr, wantMsg) {
			t.Errorf("stderr missing %q:\n%s", wantMsg, stderr)
		}
	})

	t.Run("note with attach exits 2", func(t *testing.T) {
		_, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, "--note", "note-a", "--attach"})
		})
		if code != 2 {
			t.Fatalf("exit code = %d, want 2", code)
		}
		wantMsg := "lw: ingest: cannot use --note with --attach"
		if !strings.Contains(stderr, wantMsg) {
			t.Errorf("stderr missing %q:\n%s", wantMsg, stderr)
		}
	})

	t.Run("note with prepare", func(t *testing.T) {
		prepHermetic(t)
		prepNoAgent(t)

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--prepare", "--vault", root, "--note", "note-a"})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		if stderr != "" {
			t.Errorf("stderr = %q, want empty", stderr)
		}
		wantBrief := "original source: " + absNoteA
		if !strings.Contains(stdout, wantBrief) {
			t.Errorf("brief missing %q\nstdout:\n%s", wantBrief, stdout)
		}

		e, err := stage.OpenEngine(root)
		if err != nil {
			t.Fatal(err)
		}
		defer e.Close()
		cs, err := e.Current()
		if err != nil {
			t.Fatal(err)
		}
		op := findIngestOp(t, cs)
		d, err := e.Diff()
		if err != nil {
			t.Fatal(err)
		}
		newContent := findFileDiffNew(t, d, op.Path)
		wantURL := "source_url: " + absNoteA
		if !strings.Contains(newContent, wantURL) {
			t.Errorf("staged raw missing %q\nnew content:\n%s", wantURL, newContent)
		}
	})

	t.Run("unknown id exit 1 message of F1", func(t *testing.T) {
		_, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, "--note", "nonexistent-id", "--dry-run"})
		})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
		if !strings.Contains(stderr, "notes/") || !strings.Contains(stderr, "lw note list") {
			t.Errorf("stderr missing F1 message:\n%s", stderr)
		}
	})
}

func TestNote_F3_NotFoundHint(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	notesDir := filepath.Join(root, vault.NotesDir)
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	noteFile := filepath.Join(notesDir, "20261013-121933-max-combination.md")
	if err := os.WriteFile(noteFile, []byte("# Max Combination\n\nContent.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cwd := t.TempDir()
	t.Chdir(cwd)

	t.Run("relative path existing under vault notes suggests note", func(t *testing.T) {
		_, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, "notes/20261013-121933-max-combination.md"})
		})
		if code == 0 {
			t.Fatal("expected failure for missing file at cwd, got 0")
		}
		if !strings.Contains(stderr, cwd) {
			t.Errorf("stderr missing cwd %q:\n%s", cwd, stderr)
		}
		wantNote := "--note 20261013-121933-max-combination"
		if !strings.Contains(stderr, wantNote) {
			t.Errorf("stderr missing suggestion %q:\n%s", wantNote, stderr)
		}
	})

	t.Run("relative path existing nowhere has no note suggestion", func(t *testing.T) {
		_, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, "nosuch.md"})
		})
		if code == 0 {
			t.Fatal("expected failure for nosuch.md, got 0")
		}
		if !strings.Contains(stderr, cwd) {
			t.Errorf("stderr missing cwd %q:\n%s", cwd, stderr)
		}
		if strings.Contains(stderr, "--note") {
			t.Errorf("stderr should NOT suggest --note for nosuch.md:\n%s", stderr)
		}
	})

	t.Run("absolute missing path unchanged from base", func(t *testing.T) {
		absMissing := "/nonexistent/path/for/test/abs.md"
		_, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, absMissing})
		})
		if code == 0 {
			t.Fatal("expected failure for absolute missing path, got 0")
		}
		if strings.Contains(stderr, cwd) {
			t.Errorf("stderr should NOT contain cwd for absolute missing path:\n%s", stderr)
		}
		if strings.Contains(stderr, "--note") {
			t.Errorf("stderr should NOT contain --note for absolute missing path:\n%s", stderr)
		}
		if !strings.Contains(stderr, "extract: read "+absMissing) {
			t.Errorf("stderr missing standard extract error message:\n%s", stderr)
		}
	})

	t.Run("non not-exist extraction error unchanged", func(t *testing.T) {
		emptyDir := t.TempDir()
		_, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, emptyDir})
		})
		if code == 0 {
			t.Fatal("expected failure for empty dir, got 0")
		}
		want := "no ingestible files in " + emptyDir
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q:\n%s", want, stderr)
		}
		if strings.Contains(stderr, "--note") {
			t.Errorf("stderr should not contain --note for dir failure:\n%s", stderr)
		}
	})
}

func TestNote_F4_RevisionLink(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	notesDir := filepath.Join(root, vault.NotesDir)
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	noteID := "20261013-121933-revision-note"
	notePath := filepath.Join(notesDir, noteID+".md")
	if err := os.WriteFile(notePath, []byte("# Revision Note\n\nInitial version of note.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	withFakeIngestAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
		return newToolCallingFakeAgent(e, cfg, sessions, ex), nil
	})

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--vault", root, "--note", noteID})
	})
	if code != 0 {
		t.Fatalf("first ingest failed: %d, stderr=%q stdout=%q", code, stderr, stdout)
	}

	commitOut, commitErr, commitCode := captureRun(t, func() int {
		return run([]string{"commit", "--vault", root, "-m", "commit first note revision"})
	})
	if commitCode != 0 {
		t.Fatalf("commit failed: %d, stderr=%q stdout=%q", commitCode, commitErr, commitOut)
	}

	// Verify first raw committed
	e1, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatal(err)
	}
	// A-074-3 (074 S3, D-5): a note from <vault>/notes/ now stages under raw/notes/;
	// 073 F4's subject, the supersedes link below, is unchanged.
	raw1, ok := e1.Vault().RawSource("raw/notes/revision-note.md")
	e1.Close()
	if !ok {
		t.Fatal("expected raw/notes/revision-note.md to be committed in vault")
	}

	// Edit note
	if err := os.WriteFile(notePath, []byte("# Revision Note\n\nUpdated version of note.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	withFakeIngestAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
		return newToolCallingFakeAgent(e, cfg, sessions, ex), nil
	})

	stdout, stderr, code = captureRun(t, func() int {
		return run([]string{"ingest", "--vault", root, "--note", noteID})
	})
	if code != 0 {
		t.Fatalf("second ingest failed: %d, stderr=%q stdout=%q", code, stderr, stdout)
	}

	e2, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatal(err)
	}
	defer e2.Close()
	cs, err := e2.Current()
	if err != nil {
		t.Fatal(err)
	}
	op := findIngestOp(t, cs)
	d, err := e2.Diff()
	if err != nil {
		t.Fatal(err)
	}
	newRawContent := findFileDiffNew(t, d, op.Path)
	wantSupersedes := "supersedes: " + raw1.Path
	if !strings.Contains(newRawContent, wantSupersedes) {
		t.Errorf("new raw missing %q\ncontent:\n%s", wantSupersedes, newRawContent)
	}
}

func TestIngestPlainPathBytesUnchanged(t *testing.T) {
	goldenBytes, err := os.ReadFile("testdata/ingest-plain-prepare.golden")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}

	prepHermetic(t)
	prepNoAgent(t)

	vaultDir := t.TempDir()
	t.Chdir(vaultDir)
	initOut, initErr, initCode := captureRun(t, func() int {
		return run([]string{"init", "--schema", "testing"})
	})
	if initCode != 0 {
		t.Fatalf("init failed: %d, stderr=%q stdout=%q", initCode, initErr, initOut)
	}

	// Normalise log.md to the fixed time stamped in the golden file
	logFile := filepath.Join(vaultDir, "log.md")
	if err := os.WriteFile(logFile, []byte("# Log\n\n- 2026-10-31T09:22:59Z init testing\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	srcDir := t.TempDir()
	plainContent := "# Plain Source\n\n" + strings.Repeat("Alpha beta gamma delta. ", 50) + "\n"
	plainPath := filepath.Join(srcDir, "plain.md")
	if err := os.WriteFile(plainPath, []byte(plainContent), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := prepareRun(t, vaultDir, plainPath)
	if code != 0 {
		t.Fatalf("prepareRun code = %d, want 0; stderr=%q", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}

	reCS := regexp.MustCompile(`cs-[0-9a-f]{16}`)
	normalised := reCS.ReplaceAllString(stdout, "<CS>")
	normalised = strings.ReplaceAll(normalised, plainPath, "<SRC>")

	if normalised != string(goldenBytes) {
		t.Fatalf("stdout differs from golden:\n--- got ---\n%s\n--- want ---\n%s", normalised, string(goldenBytes))
	}
}
