package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// G1 robustness - a vault with an unparseable page under wiki/ AND an
// unparseable raw AND a valid matching raw: the note row is tagged.
func TestNotePending_G1_Robustness(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, noteZone)
	root := testutil.CopyFixture(t, "minimal")

	stem := "20261013-121933-max-combination"
	writeTestNote(t, root, stem, "max combination body", now.Add(-2*time.Hour))

	validRaw := &vault.RawSource{
		Path:      "raw/articles/max-combination.md",
		SourceURL: filepath.Join(root, vault.NotesDir, stem+".md"),
		Ingested:  vault.Date{Time: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)},
		SHA256:    vault.BodySHA256("# Max Combination\n\nBody.\n"),
		Body:      "# Max Combination\n\nBody.\n",
	}
	writeTestRaw(t, root, validRaw)

	// Unparseable raw under raw/
	brokenRawPath := filepath.Join(root, "raw", "articles", "broken.md")
	if err := os.WriteFile(brokenRawPath, []byte("this has no frontmatter delimiters at all\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Unparseable page under wiki/
	brokenWikiPath := filepath.Join(root, "wiki", "entities", "broken.md")
	if err := os.WriteFile(brokenWikiPath, []byte("this has no frontmatter delimiters at all\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := writeNoteList(&buf, root, now, 20); err != nil {
		t.Fatalf("writeNoteList: %v", err)
	}
	want := "20261013-121933-max-combination  2h ago  max combination body  \u2713 max-combination\n"
	if buf.String() != want {
		t.Errorf("stdout =\n%q\nwant\n%q", buf.String(), want)
	}

	tags := noteDoneTags(root, []string{stem})
	if got := tags[stem]; got != "max-combination" {
		t.Errorf("tags[%q] = %q, want %q", stem, got, "max-combination")
	}
}

// G2 filter - notes N1 (newest, distilled), N2, N3 (undone): pending prints N2,
// N3 in newest-first order, no tags; with n=1 prints N2 only (n counts pending rows).
func TestNotePending_G2_Filter(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, noteZone)
	root := testutil.CopyFixture(t, "minimal")

	// N1 (newest, distilled)
	stemN1 := "20261015-110000-n1"
	writeTestNote(t, root, stemN1, "n1 body", now.Add(-1*time.Hour))
	rawN1 := &vault.RawSource{
		Path:      "raw/articles/n1.md",
		SourceURL: filepath.Join(root, vault.NotesDir, stemN1+".md"),
		Ingested:  vault.Date{Time: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)},
		SHA256:    vault.BodySHA256("# N1\n\nBody.\n"),
		Body:      "# N1\n\nBody.\n",
	}
	writeTestRaw(t, root, rawN1)

	// N2 (middle, undone)
	stemN2 := "20261015-100000-n2"
	writeTestNote(t, root, stemN2, "n2 body", now.Add(-2*time.Hour))

	// N3 (oldest, undone)
	stemN3 := "20261015-090000-n3"
	writeTestNote(t, root, stemN3, "n3 body", now.Add(-3*time.Hour))

	t.Run("pending all", func(t *testing.T) {
		var buf bytes.Buffer
		if err := writeNoteListOpts(&buf, root, now, 20, true); err != nil {
			t.Fatalf("writeNoteListOpts: %v", err)
		}
		want := "20261015-100000-n2  2h ago  n2 body\n" +
			"20261015-090000-n3  3h ago  n3 body\n"
		if buf.String() != want {
			t.Errorf("stdout =\n%q\nwant\n%q", buf.String(), want)
		}
	})

	t.Run("pending n=1", func(t *testing.T) {
		var buf bytes.Buffer
		if err := writeNoteListOpts(&buf, root, now, 1, true); err != nil {
			t.Fatalf("writeNoteListOpts: %v", err)
		}
		want := "20261015-100000-n2  2h ago  n2 body\n"
		if buf.String() != want {
			t.Errorf("stdout =\n%q\nwant\n%q", buf.String(), want)
		}
	})
}

// G3 empty cases - all distilled: exactly one line no pending notes\n;
// no notes: the existing no notes yet ... hint line unchanged;
// a waiting draft line is printed first in both (same text as plain list).
func TestNotePending_G3_EmptyCases(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, noteZone)

	t.Run("all distilled without draft", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		stem := "20261015-100000-n1"
		writeTestNote(t, root, stem, "n1 body", now.Add(-1*time.Hour))
		raw := &vault.RawSource{
			Path:      "raw/articles/n1.md",
			SourceURL: filepath.Join(root, vault.NotesDir, stem+".md"),
			Ingested:  vault.Date{Time: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)},
			SHA256:    vault.BodySHA256("# N1\n\nBody.\n"),
			Body:      "# N1\n\nBody.\n",
		}
		writeTestRaw(t, root, raw)

		var buf bytes.Buffer
		if err := writeNoteListOpts(&buf, root, now, 20, true); err != nil {
			t.Fatalf("writeNoteListOpts: %v", err)
		}
		want := "no pending notes\n"
		if buf.String() != want {
			t.Errorf("stdout = %q, want %q", buf.String(), want)
		}
	})

	t.Run("all distilled with waiting draft", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		stem := "20261015-100000-n1"
		writeTestNote(t, root, stem, "n1 body", now.Add(-1*time.Hour))
		raw := &vault.RawSource{
			Path:      "raw/articles/n1.md",
			SourceURL: filepath.Join(root, vault.NotesDir, stem+".md"),
			Ingested:  vault.Date{Time: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)},
			SHA256:    vault.BodySHA256("# N1\n\nBody.\n"),
			Body:      "# N1\n\nBody.\n",
		}
		writeTestRaw(t, root, raw)

		bufPath := filepath.Join(root, stateDirName, "tmp", noteEditBufferName)
		if err := os.MkdirAll(filepath.Dir(bufPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bufPath, []byte("unsaved draft body\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		var buf bytes.Buffer
		if err := writeNoteListOpts(&buf, root, now, 20, true); err != nil {
			t.Fatalf("writeNoteListOpts: %v", err)
		}
		lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
		if len(lines) != 2 {
			t.Fatalf("got %d lines, want 2: %q", len(lines), buf.String())
		}
		if !strings.Contains(lines[0], noteEditBufferName) || !strings.Contains(lines[0], "unsaved draft") {
			t.Errorf("line 0 = %q, want draft line", lines[0])
		}
		if lines[1] != "no pending notes" {
			t.Errorf("line 1 = %q, want 'no pending notes'", lines[1])
		}
	})

	t.Run("no notes without draft", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")

		var pendingBuf, plainBuf bytes.Buffer
		if err := writeNoteListOpts(&pendingBuf, root, now, 20, true); err != nil {
			t.Fatalf("pending: %v", err)
		}
		if err := writeNoteListOpts(&plainBuf, root, now, 20, false); err != nil {
			t.Fatalf("plain: %v", err)
		}
		if pendingBuf.String() != plainBuf.String() {
			t.Errorf("pending %q != plain %q", pendingBuf.String(), plainBuf.String())
		}
		want := "no notes yet \u2014 lw note -m \"\u2026\"\n"
		if pendingBuf.String() != want {
			t.Errorf("output = %q, want %q", pendingBuf.String(), want)
		}
	})

	t.Run("no notes with waiting draft", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")

		bufPath := filepath.Join(root, stateDirName, "tmp", noteEditBufferName)
		if err := os.MkdirAll(filepath.Dir(bufPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bufPath, []byte("unsaved draft body\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		var pendingBuf, plainBuf bytes.Buffer
		if err := writeNoteListOpts(&pendingBuf, root, now, 20, true); err != nil {
			t.Fatalf("pending: %v", err)
		}
		if err := writeNoteListOpts(&plainBuf, root, now, 20, false); err != nil {
			t.Fatalf("plain: %v", err)
		}
		if pendingBuf.String() != plainBuf.String() {
			t.Errorf("pending %q != plain %q", pendingBuf.String(), plainBuf.String())
		}
		lines := strings.Split(strings.TrimSuffix(pendingBuf.String(), "\n"), "\n")
		if len(lines) != 2 {
			t.Fatalf("got %d lines, want 2: %q", len(lines), pendingBuf.String())
		}
		if !strings.Contains(lines[0], noteEditBufferName) || !strings.Contains(lines[0], "unsaved draft") {
			t.Errorf("line 0 = %q, want draft line", lines[0])
		}
		if lines[1] != "no notes yet \u2014 lw note -m \"\u2026\"" {
			t.Errorf("line 1 = %q, want 'no notes yet' hint", lines[1])
		}
	})
}

// G4 honesty - writeNoteListOpts(..., pending=true) with an opener that fails
// returns a non-nil error whose text starts cannot tell which notes are pending
// and prints nothing to w; pending=false with the same failing opener returns
// nil and the undecorated list.
func TestNotePending_G4_Honesty(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, noteZone)
	root := testutil.CopyFixture(t, "minimal")
	writeTestNote(t, root, "20261013-121933-max-combination", "max combination body", now.Add(-2*time.Hour))

	origOpener := noteOpenVault
	noteOpenVault = func(root string, opts ...vault.Option) (*vault.Vault, error) {
		return nil, errors.New("simulated opener failure")
	}
	defer func() { noteOpenVault = origOpener }()

	t.Run("pending fails and prints nothing", func(t *testing.T) {
		var buf bytes.Buffer
		err := writeNoteListOpts(&buf, root, now, 20, true)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.HasPrefix(err.Error(), "cannot tell which notes are pending") {
			t.Errorf("error %q does not start with %q", err.Error(), "cannot tell which notes are pending")
		}
		if buf.Len() != 0 {
			t.Errorf("buf = %q, want empty", buf.String())
		}
	})

	t.Run("plain list succeeds with undecorated rows", func(t *testing.T) {
		var buf bytes.Buffer
		err := writeNoteListOpts(&buf, root, now, 20, false)
		if err != nil {
			t.Fatalf("expected nil error, got: %v", err)
		}
		want := "20261013-121933-max-combination  2h ago  max combination body\n"
		if buf.String() != want {
			t.Errorf("stdout =\n%q\nwant undecorated %q", buf.String(), want)
		}
	})
}

// G5 CLI - run([]string{"note","list","--pending","--vault",V}) exit 0;
// lw note list --pending extra exit 2 (existing unexpected-argument message);
// the lw note help/usage text lists --pending.
func TestNotePending_G5_CLI(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, noteZone)
	setNoteClock(t, now)

	t.Run("exit 0 on valid run", func(t *testing.T) {
		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"note", "list", "--pending", "--vault", root})
		})
		if code != 0 {
			t.Fatalf("run failed: code=%d, stderr=%q", code, stderr)
		}
		if !strings.Contains(stdout, "no notes yet") {
			t.Errorf("stdout missing expected output: %q", stdout)
		}
	})

	t.Run("exit 2 on unexpected argument", func(t *testing.T) {
		_, stderr, code := captureRun(t, func() int {
			return run([]string{"note", "list", "--pending", "extra", "--vault", root})
		})
		if code != 2 {
			t.Fatalf("exit code = %d, want 2", code)
		}
		if !strings.Contains(stderr, `lw note list: unexpected argument "extra"`) {
			t.Errorf("stderr missing unexpected argument message: %q", stderr)
		}
	})

	t.Run("help/usage text lists --pending", func(t *testing.T) {
		stdout, stderr, _ := captureRun(t, func() int {
			return run([]string{"note", "-h"})
		})
		combined := stdout + stderr
		if !strings.Contains(combined, "--pending") {
			t.Errorf("help/usage text missing '--pending':\n%s", combined)
		}
	})
}

// G6 laziness - plain list with n=0 or an empty notes dir never calls noteOpenVault;
// --pending with an empty notes dir never calls it either.
func TestNotePending_G6_Laziness(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, noteZone)

	origOpener := noteOpenVault
	var calls int
	noteOpenVault = func(root string, opts ...vault.Option) (*vault.Vault, error) {
		calls++
		return origOpener(root, opts...)
	}
	defer func() { noteOpenVault = origOpener }()

	t.Run("plain list with n=0 never calls opener", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		writeTestNote(t, root, "20261013-100000-note1", "note 1", now.Add(-1*time.Hour))
		calls = 0
		var buf bytes.Buffer
		if err := writeNoteListOpts(&buf, root, now, 0, false); err != nil {
			t.Fatalf("writeNoteListOpts: %v", err)
		}
		if calls != 0 {
			t.Errorf("calls = %d, want 0", calls)
		}
	})

	t.Run("plain list with empty notes dir never calls opener", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		calls = 0
		var buf bytes.Buffer
		if err := writeNoteListOpts(&buf, root, now, 20, false); err != nil {
			t.Fatalf("writeNoteListOpts: %v", err)
		}
		if calls != 0 {
			t.Errorf("calls = %d, want 0", calls)
		}
	})

	t.Run("pending with empty notes dir never calls opener", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		calls = 0
		var buf bytes.Buffer
		if err := writeNoteListOpts(&buf, root, now, 20, true); err != nil {
			t.Fatalf("writeNoteListOpts: %v", err)
		}
		if calls != 0 {
			t.Errorf("calls = %d, want 0", calls)
		}
	})
}
