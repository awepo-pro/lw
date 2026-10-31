package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

func writeTestNote(t *testing.T, root, stem, body string, created time.Time) {
	t.Helper()
	dir := filepath.Join(root, vault.NotesDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf("---\ncreated: %s\n---\n\n%s\n", created.Format(time.RFC3339), body)
	if err := os.WriteFile(filepath.Join(dir, stem+".md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeTestRaw(t *testing.T, root string, raw *vault.RawSource) {
	t.Helper()
	path := filepath.Join(root, raw.Path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw.Serialize(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// F1 undone: a vault with notes and NO raws, or only raws for other sources:
// writeNoteList output equals the output of v2.58.0.
func TestNoteDone_F1_Undone(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, noteZone)

	t.Run("no raws", func(t *testing.T) {
		root := noteBareVault(t)
		writeTestNote(t, root, "20261013-121933-max-combination", "max combination body", now.Add(-2*time.Hour))
		writeTestNote(t, root, "20261014-090000-rtt", "rtt body", now.Add(-1*time.Hour))

		var buf bytes.Buffer
		if err := writeNoteList(&buf, root, now, 20); err != nil {
			t.Fatalf("writeNoteList: %v", err)
		}
		want := "20261014-090000-rtt  1h ago  rtt body\n" +
			"20261013-121933-max-combination  2h ago  max combination body\n"
		if buf.String() != want {
			t.Errorf("stdout =\n%q\nwant\n%q", buf.String(), want)
		}
	})

	t.Run("raws for other sources only", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		writeTestNote(t, root, "20261013-121933-max-combination", "max combination body", now.Add(-2*time.Hour))
		writeTestNote(t, root, "20261014-090000-rtt", "rtt body", now.Add(-1*time.Hour))

		var buf bytes.Buffer
		if err := writeNoteList(&buf, root, now, 20); err != nil {
			t.Fatalf("writeNoteList: %v", err)
		}
		want := "20261014-090000-rtt  1h ago  rtt body\n" +
			"20261013-121933-max-combination  2h ago  max combination body\n"
		if buf.String() != want {
			t.Errorf("stdout =\n%q\nwant\n%q", buf.String(), want)
		}
	})
}

// F2 exact: note notes/20261013-121933-max-combination.md; raw raw/articles/max-combination.md
// with source_url: <root>/notes/20261013-121933-max-combination.md:
// that row is <stem>  <age>  <text>  <U+2713> max-combination; other note unchanged.
func TestNoteDone_F2_Exact(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, noteZone)
	root := testutil.CopyFixture(t, "minimal")

	writeTestNote(t, root, "20261013-121933-max-combination", "max combination body", now.Add(-2*time.Hour))
	writeTestNote(t, root, "20261014-090000-rtt", "rtt body", now.Add(-1*time.Hour))

	raw := &vault.RawSource{
		Path:      "raw/articles/max-combination.md",
		SourceURL: filepath.Join(root, vault.NotesDir, "20261013-121933-max-combination.md"),
		Ingested:  vault.Date{Time: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)},
		SHA256:    vault.BodySHA256("# Max Combination\n\nBody.\n"),
		Body:      "# Max Combination\n\nBody.\n",
	}
	writeTestRaw(t, root, raw)

	var buf bytes.Buffer
	if err := writeNoteList(&buf, root, now, 20); err != nil {
		t.Fatalf("writeNoteList: %v", err)
	}

	want := "20261014-090000-rtt  1h ago  rtt body\n" +
		"20261013-121933-max-combination  2h ago  max combination body  \u2713 max-combination\n"
	if buf.String() != want {
		t.Errorf("stdout =\n%q\nwant\n%q", buf.String(), want)
	}
}

// F3 revision chain: raw max-combination.md (old) and max-combination-2.md with
// supersedes: raw/articles/max-combination.md, same source_url: tag is <U+2713> max-combination-2.
func TestNoteDone_F3_RevisionChain(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, noteZone)
	root := testutil.CopyFixture(t, "minimal")

	writeTestNote(t, root, "20261013-121933-max-combination", "max combination body", now.Add(-2*time.Hour))

	sourceURL := filepath.Join(root, vault.NotesDir, "20261013-121933-max-combination.md")
	raw1 := &vault.RawSource{
		Path:      "raw/articles/max-combination.md",
		SourceURL: sourceURL,
		Ingested:  vault.Date{Time: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)},
		SHA256:    vault.BodySHA256("# Max Combination\n\nFirst revision.\n"),
		Body:      "# Max Combination\n\nFirst revision.\n",
	}
	writeTestRaw(t, root, raw1)

	raw2 := &vault.RawSource{
		Path:       "raw/articles/max-combination-2.md",
		SourceURL:  sourceURL,
		Supersedes: "raw/articles/max-combination.md",
		Ingested:   vault.Date{Time: time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)},
		SHA256:     vault.BodySHA256("# Max Combination v2\n\nSecond revision.\n"),
		Body:       "# Max Combination v2\n\nSecond revision.\n",
	}
	writeTestRaw(t, root, raw2)

	var buf bytes.Buffer
	if err := writeNoteList(&buf, root, now, 20); err != nil {
		t.Fatalf("writeNoteList: %v", err)
	}

	want := "20261013-121933-max-combination  2h ago  max combination body  \u2713 max-combination-2\n"
	if buf.String() != want {
		t.Errorf("stdout =\n%q\nwant\n%q", buf.String(), want)
	}
}

// F4 suffix, per-PC: raw source_url: /home/other/pc2/vault/notes/20261013-121933-max-combination.md
// (different root): note IS tagged.
// Not tagged: /x/notes/20261013-121933-max-combination2.md, /x/notes/sub/20261013-121933-max-combination.md,
// /x/note/20261013-121933-max-combination.md, 20261013-121933-max-combination.md.
func TestNoteDone_F4_SuffixPerPC(t *testing.T) {
	stem := "20261013-121933-max-combination"
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, noteZone)

	t.Run("suffix match from other PC", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		writeTestNote(t, root, stem, "max combination body", now.Add(-2*time.Hour))

		raw := &vault.RawSource{
			Path:      "raw/articles/max-combination.md",
			SourceURL: "/home/other/pc2/vault/notes/20261013-121933-max-combination.md",
			Ingested:  vault.Date{Time: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)},
			SHA256:    vault.BodySHA256("# Max Combination\n\nOther PC.\n"),
			Body:      "# Max Combination\n\nOther PC.\n",
		}
		writeTestRaw(t, root, raw)

		var buf bytes.Buffer
		if err := writeNoteList(&buf, root, now, 20); err != nil {
			t.Fatalf("writeNoteList: %v", err)
		}
		want := "20261013-121933-max-combination  2h ago  max combination body  \u2713 max-combination\n"
		if buf.String() != want {
			t.Errorf("stdout =\n%q\nwant\n%q", buf.String(), want)
		}
	})

	t.Run("negatives rejected by predicate and writeNoteList", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		writeTestNote(t, root, stem, "max combination body", now.Add(-2*time.Hour))

		negatives := []string{
			"/x/notes/20261013-121933-max-combination2.md",
			"/x/notes/sub/20261013-121933-max-combination.md",
			"/x/note/20261013-121933-max-combination.md",
			"20261013-121933-max-combination.md",
		}

		for _, badURL := range negatives {
			if noteMatchesSource(root, stem, badURL) {
				t.Errorf("noteMatchesSource(%q) = true, want false", badURL)
			}
		}

		// Verify none tags the note in writeNoteList
		for i, badURL := range negatives {
			raw := &vault.RawSource{
				Path:      fmt.Sprintf("raw/articles/neg-%d.md", i),
				SourceURL: badURL,
				Ingested:  vault.Date{Time: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)},
				SHA256:    vault.BodySHA256("# Neg\n\nBody.\n"),
				Body:      "# Neg\n\nBody.\n",
			}
			writeTestRaw(t, root, raw)
		}

		var buf bytes.Buffer
		if err := writeNoteList(&buf, root, now, 20); err != nil {
			t.Fatalf("writeNoteList: %v", err)
		}
		want := "20261013-121933-max-combination  2h ago  max combination body\n"
		if buf.String() != want {
			t.Errorf("stdout =\n%q\nwant\n%q", buf.String(), want)
		}
	})
}

// F5 non-local: source_url: https://example.com/notes/20261013-121933-max-combination.md
// and relative notes/20261013-121933-max-combination.md: NOT tagged.
func TestNoteDone_F5_NonLocal(t *testing.T) {
	stem := "20261013-121933-max-combination"
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, noteZone)
	root := testutil.CopyFixture(t, "minimal")
	writeTestNote(t, root, stem, "max combination body", now.Add(-2*time.Hour))

	nonLocals := []string{
		"https://example.com/notes/20261013-121933-max-combination.md",
		"notes/20261013-121933-max-combination.md",
	}

	for _, url := range nonLocals {
		if noteMatchesSource(root, stem, url) {
			t.Errorf("noteMatchesSource(%q) = true, want false", url)
		}
	}

	for i, url := range nonLocals {
		raw := &vault.RawSource{
			Path:      fmt.Sprintf("raw/articles/nonlocal-%d.md", i),
			SourceURL: url,
			Ingested:  vault.Date{Time: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)},
			SHA256:    vault.BodySHA256("# NonLocal\n\nBody.\n"),
			Body:      "# NonLocal\n\nBody.\n",
		}
		writeTestRaw(t, root, raw)
	}

	var buf bytes.Buffer
	if err := writeNoteList(&buf, root, now, 20); err != nil {
		t.Fatalf("writeNoteList: %v", err)
	}
	want := "20261013-121933-max-combination  2h ago  max combination body\n"
	if buf.String() != want {
		t.Errorf("stdout =\n%q\nwant\n%q", buf.String(), want)
	}
}

// F6 failure is silent: a root with no SCHEMA.md (vault.Open fails) and a root
// whose raw/ holds an unparseable .md: output is exactly the undecorated list,
// writeNoteList returns nil.
func TestNoteDone_F6_FailureIsSilent(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, noteZone)

	t.Run("missing SCHEMA.md", func(t *testing.T) {
		root := t.TempDir()
		writeTestNote(t, root, "20261013-121933-max-combination", "max combination body", now.Add(-2*time.Hour))

		var buf bytes.Buffer
		if err := writeNoteList(&buf, root, now, 20); err != nil {
			t.Fatalf("writeNoteList: %v, want nil", err)
		}
		want := "20261013-121933-max-combination  2h ago  max combination body\n"
		if buf.String() != want {
			t.Errorf("stdout =\n%q\nwant\n%q", buf.String(), want)
		}
	})

	t.Run("unparseable md under raw/", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		writeTestNote(t, root, "20261013-121933-max-combination", "max combination body", now.Add(-2*time.Hour))

		// Also add a valid matching raw to ensure failure silences decoration
		validRaw := &vault.RawSource{
			Path:      "raw/articles/max-combination.md",
			SourceURL: filepath.Join(root, vault.NotesDir, "20261013-121933-max-combination.md"),
			Ingested:  vault.Date{Time: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)},
			SHA256:    vault.BodySHA256("# Max Combination\n\nBody.\n"),
			Body:      "# Max Combination\n\nBody.\n",
		}
		writeTestRaw(t, root, validRaw)

		// Corrupt raw file that fails ParseRawSource
		brokenPath := filepath.Join(root, "raw", "articles", "broken.md")
		if err := os.WriteFile(brokenPath, []byte("this has no frontmatter delimiters at all\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		var buf bytes.Buffer
		if err := writeNoteList(&buf, root, now, 20); err != nil {
			t.Fatalf("writeNoteList: %v, want nil", err)
		}
		want := "20261013-121933-max-combination  2h ago  max combination body\n"
		if buf.String() != want {
			t.Errorf("stdout =\n%q\nwant undecorated %q", buf.String(), want)
		}
	})
}

// F7 laziness/cut: -n 1 with 3 notes: only the newest row is printed and decorated;
// no notes yet path never calls the opener (test via an unexported var noteOpenVault = vault.Open
// seam that counts calls: 0 calls for an empty notes dir and for n=0).
func TestNoteDone_F7_LazinessCut(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, noteZone)

	origOpener := noteOpenVault
	var calls int
	noteOpenVault = func(root string, opts ...vault.Option) (*vault.Vault, error) {
		calls++
		return origOpener(root, opts...)
	}
	defer func() { noteOpenVault = origOpener }()

	t.Run("empty notes dir never calls opener", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		calls = 0
		var buf bytes.Buffer
		if err := writeNoteList(&buf, root, now, 20); err != nil {
			t.Fatalf("writeNoteList: %v", err)
		}
		if calls != 0 {
			t.Errorf("calls = %d, want 0", calls)
		}
		if !strings.Contains(buf.String(), "no notes yet") {
			t.Errorf("output missing 'no notes yet': %q", buf.String())
		}
	})

	t.Run("n=0 never calls opener", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		writeTestNote(t, root, "20261013-100000-note1", "note 1", now.Add(-3*time.Hour))
		writeTestNote(t, root, "20261013-110000-note2", "note 2", now.Add(-2*time.Hour))
		writeTestNote(t, root, "20261013-120000-note3", "note 3", now.Add(-1*time.Hour))

		calls = 0
		var buf bytes.Buffer
		if err := writeNoteList(&buf, root, now, 0); err != nil {
			t.Fatalf("writeNoteList: %v", err)
		}
		if calls != 0 {
			t.Errorf("calls = %d, want 0", calls)
		}
		if buf.String() != "" {
			t.Errorf("buf = %q, want empty", buf.String())
		}
	})

	t.Run("n=1 with 3 notes prints only newest row and decorates it", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		writeTestNote(t, root, "20261013-100000-note1", "note 1", now.Add(-3*time.Hour))
		writeTestNote(t, root, "20261013-110000-note2", "note 2", now.Add(-2*time.Hour))
		writeTestNote(t, root, "20261013-120000-note3", "note 3", now.Add(-1*time.Hour))

		// Raw matches the newest note
		raw := &vault.RawSource{
			Path:      "raw/articles/note3.md",
			SourceURL: filepath.Join(root, vault.NotesDir, "20261013-120000-note3.md"),
			Ingested:  vault.Date{Time: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)},
			SHA256:    vault.BodySHA256("# Note 3\n\nBody.\n"),
			Body:      "# Note 3\n\nBody.\n",
		}
		writeTestRaw(t, root, raw)

		calls = 0
		var buf bytes.Buffer
		if err := writeNoteList(&buf, root, now, 1); err != nil {
			t.Fatalf("writeNoteList: %v", err)
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1", calls)
		}
		want := "20261013-120000-note3  1h ago  note 3  \u2713 note3\n"
		if buf.String() != want {
			t.Errorf("buf =\n%q\nwant\n%q", buf.String(), want)
		}
	})
}

// F8 end to end (cmd/lw run): scratch vault: lw note -m x, lw ingest --prepare --note <id>
// then lw commit; lw note list shows <U+2713>; before the commit it does not
// (the staged raw is not on disk under raw/ yet).
func TestNoteDone_F8_EndToEnd(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	prepHermetic(t)

	now := time.Date(2026, 10, 15, 12, 0, 0, 0, noteZone)
	setNoteClock(t, now)

	// Step 1: lw note -m "f8 test message"
	_, stderr, code := captureRun(t, func() int {
		return run([]string{"note", "-m", "f8 test message", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("note -m failed: %d, stderr=%q", code, stderr)
	}

	ents, err := os.ReadDir(filepath.Join(root, vault.NotesDir))
	if err != nil {
		t.Fatal(err)
	}
	var noteID string
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".md") {
			noteID = strings.TrimSuffix(e.Name(), ".md")
			break
		}
	}
	if noteID == "" {
		t.Fatal("no note created under notes/")
	}

	// Step 2: lw note list before ingest shows no checkmark
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"note", "list", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("note list before ingest failed: %d, stderr=%q", code, stderr)
	}
	if strings.Contains(stdout, "\u2713") {
		t.Fatalf("note list before ingest should not contain \u2713, got:\n%s", stdout)
	}

	// Step 3: lw ingest --prepare --note <id>
	_, stderr, code = captureRun(t, func() int {
		return run([]string{"ingest", "--prepare", "--vault", root, "--note", noteID})
	})
	if code != 0 {
		t.Fatalf("ingest --prepare failed: %d, stderr=%q", code, stderr)
	}

	// Step 4: lw note list before commit shows no checkmark (staged raw not on disk under raw/)
	stdout, stderr, code = captureRun(t, func() int {
		return run([]string{"note", "list", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("note list before commit failed: %d, stderr=%q", code, stderr)
	}
	if strings.Contains(stdout, "\u2713") {
		t.Fatalf("note list before commit should not contain \u2713, got:\n%s", stdout)
	}

	// Step 5: lw commit
	_, stderr, code = captureRun(t, func() int {
		return run([]string{"commit", "--vault", root, "-m", "commit note raw for f8"})
	})
	if code != 0 {
		t.Fatalf("commit failed: %d, stderr=%q", code, stderr)
	}

	// Step 6: lw note list after commit shows <U+2713>
	stdout, stderr, code = captureRun(t, func() int {
		return run([]string{"note", "list", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("note list after commit failed: %d, stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "\u2713") {
		t.Fatalf("note list after commit must contain \u2713, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, noteID) {
		t.Fatalf("note list after commit must contain noteID %s, got:\n%s", noteID, stdout)
	}
}
