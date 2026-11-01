package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// R1 (071 F1): cutInstructionTag ends with Unicode ellipsis U+2026 when cut.
// A 100-rune first line yields a tag of exactly 60 runes plus "\u2026" (61 runes total)
// and a 60-rune first line is not cut.
func TestReviewFixS64_R1_CutInstructionTag(t *testing.T) {
	line100 := strings.Repeat("x", 100)
	got100 := cutInstructionTag(line100)
	want100 := strings.Repeat("x", 60) + "\u2026"
	if got100 != want100 {
		t.Errorf("cutInstructionTag(100 runes) = %q, want %q", got100, want100)
	}
	if n := utf8.RuneCountInString(got100); n != 61 {
		t.Errorf("rune count for 100-rune tag = %d, want 61", n)
	}

	line60 := strings.Repeat("y", 60)
	got60 := cutInstructionTag(line60)
	if got60 != line60 {
		t.Errorf("cutInstructionTag(60 runes) = %q, want %q", got60, line60)
	}
	if n := utf8.RuneCountInString(got60); n != 60 {
		t.Errorf("rune count for 60-rune tag = %d, want 60", n)
	}
}

// R2 (071 F3): --instruction-file with a regular file must read at most 64 KiB, like stdin:
// a file larger than 64 KiB is refused with an error containing "larger than 64 KiB" and the path,
// without reading the whole file. Test with a 70 KiB temp file and a 2 KiB one.
func TestReviewFixS64_R2_InstructionFileLimit(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	tmpDir := t.TempDir()
	path70k := filepath.Join(tmpDir, "large_70k.txt")
	data70k := bytes.Repeat([]byte("a"), 70*1024)
	if err := os.WriteFile(path70k, data70k, 0o644); err != nil {
		t.Fatal(err)
	}

	path2k := filepath.Join(tmpDir, "small_2k.txt")
	data2k := bytes.Repeat([]byte("b"), 2*1024)
	if err := os.WriteFile(path2k, data2k, 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. Direct helper test on 70 KiB file
	_, err70k := readInstructionFile(path70k)
	if err70k == nil {
		t.Fatalf("readInstructionFile(70 KiB) succeeded, want error")
	}
	if !strings.Contains(err70k.Error(), "larger than 64 KiB") {
		t.Errorf("readInstructionFile(70 KiB) error %q does not contain 'larger than 64 KiB'", err70k.Error())
	}
	if !strings.Contains(err70k.Error(), path70k) {
		t.Errorf("readInstructionFile(70 KiB) error %q does not contain path %q", err70k.Error(), path70k)
	}

	// 2. Direct helper test on 2 KiB file
	got2k, err2k := readInstructionFile(path2k)
	if err2k != nil {
		t.Fatalf("readInstructionFile(2 KiB) failed: %v", err2k)
	}
	if got2k != string(data2k) {
		t.Errorf("readInstructionFile(2 KiB) length = %d, want %d", len(got2k), len(data2k))
	}

	// 3. CLI test on 70 KiB file
	root := testutil.CopyFixture(t, "minimal")
	src := filepath.Join(root, "src.md")
	if err := os.WriteFile(src, []byte("# Src\n\nContent\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, stderr70k, code70k := captureRun(t, func() int {
		return run([]string{"ingest", "--prepare", "--vault", root, "--instruction-file", path70k, src})
	})
	if code70k == 0 {
		t.Errorf("CLI --instruction-file with 70 KiB exited 0, want non-zero")
	}
	if !strings.Contains(stderr70k, "larger than 64 KiB") {
		t.Errorf("CLI stderr %q does not contain 'larger than 64 KiB'", stderr70k)
	}
	if !strings.Contains(stderr70k, path70k) {
		t.Errorf("CLI stderr %q does not contain path %q", stderr70k, path70k)
	}
}

// R3 (075 F1): the tie-break between revision tips with the same ingested date must use
// vault.LaterCopy(a, b) instead of plain string comparison (A-070-5).
// One note, two committed raws ingested on the same date from DIFFERENT absolute source_url
// paths that both end in notes/<id>.md (the two-PC case), at raw/articles/max-combination.md
// and raw/articles/max-combination-2.md -> the row ends with "  \u2713 max-combination-2";
// and with -2 and -10 copies -> "-10".
func TestReviewFixS64_R3_NoteDoneTieBreak(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	stem := "20261013-121933-max-combination"
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, noteZone)
	sameDate := vault.Date{Time: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)}

	t.Run("max-combination and max-combination-2 tie-break", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		writeTestNote(t, root, stem, "max combination body", now.Add(-2*time.Hour))

		raw1 := &vault.RawSource{
			Path:      "raw/articles/max-combination.md",
			SourceURL: "/pc1/notes/" + stem + ".md",
			Ingested:  sameDate,
			SHA256:    vault.BodySHA256("# Revision 1\n"),
			Body:      "# Revision 1\n",
		}
		writeTestRaw(t, root, raw1)

		raw2 := &vault.RawSource{
			Path:      "raw/articles/max-combination-2.md",
			SourceURL: "/pc2/notes/" + stem + ".md",
			Ingested:  sameDate,
			SHA256:    vault.BodySHA256("# Revision 2\n"),
			Body:      "# Revision 2\n",
		}
		writeTestRaw(t, root, raw2)

		var buf bytes.Buffer
		if err := writeNoteList(&buf, root, now, 20); err != nil {
			t.Fatalf("writeNoteList: %v", err)
		}
		out := buf.String()
		wantSuffix := "  \u2713 max-combination-2\n"
		if !strings.HasSuffix(out, wantSuffix) {
			t.Errorf("writeNoteList output = %q, want suffix %q", out, wantSuffix)
		}
	})

	t.Run("max-combination-2 and max-combination-10 tie-break", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		writeTestNote(t, root, stem, "max combination body", now.Add(-2*time.Hour))

		raw2 := &vault.RawSource{
			Path:      "raw/articles/max-combination-2.md",
			SourceURL: "/pc2/notes/" + stem + ".md",
			Ingested:  sameDate,
			SHA256:    vault.BodySHA256("# Revision 2\n"),
			Body:      "# Revision 2\n",
		}
		writeTestRaw(t, root, raw2)

		raw10 := &vault.RawSource{
			Path:      "raw/articles/max-combination-10.md",
			SourceURL: "/pc3/notes/" + stem + ".md",
			Ingested:  sameDate,
			SHA256:    vault.BodySHA256("# Revision 10\n"),
			Body:      "# Revision 10\n",
		}
		writeTestRaw(t, root, raw10)

		var buf bytes.Buffer
		if err := writeNoteList(&buf, root, now, 20); err != nil {
			t.Fatalf("writeNoteList: %v", err)
		}
		out := buf.String()
		wantSuffix := "  \u2713 max-combination-10\n"
		if !strings.HasSuffix(out, wantSuffix) {
			t.Errorf("writeNoteList output = %q, want suffix %q", out, wantSuffix)
		}
	})
}

// R4 (073 F3): the main.go help line 'ingest --note <id>...' must not suggest one flag takes
// several ids. New text: 'ingest --note <id>           ingest a note from notes/ by id or unique prefix; repeat --note for more'.
// Assert that line is present and the old '<id>...' text is gone.
func TestReviewFixS64_R4_HelpLine(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"--help"})
	})
	if code != 0 {
		t.Fatalf("run(--help) exited with %d; stderr: %s", code, stderr)
	}

	wantLine := "ingest --note <id>           ingest a note from notes/ by id or unique prefix; repeat --note for more"
	if !strings.Contains(stdout, wantLine) {
		t.Errorf("--help output missing new line %q\ngot:\n%s", wantLine, stdout)
	}
	if strings.Contains(stdout, "ingest --note <id>...") {
		t.Errorf("--help output still contains old '<id>...' text\ngot:\n%s", stdout)
	}
}

// R5 (073 F4): the usage line printed when lw ingest gets no source and no --note becomes
// exactly: 'usage: lw ingest <url|path|dir>... [--note <id>]... [--kind K] [--dry-run]'.
// Test the exact stderr line and exit 2.
func TestReviewFixS64_R5_IngestUsage(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	root := testutil.CopyFixture(t, "minimal")
	_, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--vault", root})
	})
	if code != 2 {
		t.Fatalf("run(ingest --vault <root>) exit code = %d, want 2", code)
	}
	wantStderr := "usage: lw ingest <url|path|dir>... [--note <id>]... [--kind K] [--dry-run]\n"
	if stderr != wantStderr {
		t.Errorf("stderr = %q, want %q", stderr, wantStderr)
	}
}
