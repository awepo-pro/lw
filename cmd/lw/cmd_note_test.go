// cmd_note_test.go covers `lw note` (047 S1, 044 phase 1): the -m capture,
// the editor path, the list, the unknown-subcommand refusal, the help row,
// and the isolation promise — a notes/ directory is invisible to lint,
// status, doctor, the agent's read tools and commit history. Every clock
// the verb reads goes through the noteNow seam, so no test reads the wall
// clock for anything it asserts exactly (00-conventions.md §3); the one
// real-clock subtest pins only a pattern.
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/tools"
)

// noteZone is the "local" zone the fixed test clocks carry, so a created:
// line's offset is pinned without depending on the machine's TZ.
var noteZone = time.FixedZone("UTC+8", 8*3600)

// setNoteClock pins the verb's clock to at for the rest of the test.
func setNoteClock(t *testing.T, at time.Time) {
	t.Helper()
	orig := noteNow
	noteNow = func() time.Time { return at }
	t.Cleanup(func() { noteNow = orig })
}

// noteBareVault is a vault root with a SCHEMA.md and nothing else — no
// .llmwiki, no notes — so a test can prove what `lw note` does and does not
// create.
func noteBareVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "SCHEMA.md"), []byte(testSchema), 0o644); err != nil {
		t.Fatalf("write SCHEMA.md: %v", err)
	}
	return root
}

// noteEditorScript writes an executable POSIX sh script that runs body with
// $last bound to its final argument — the buffer file lw note hands the
// editor — and returns its absolute path.
func noteEditorScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "editor.sh")
	src := "#!/bin/sh\nfor last; do :; done\n" + body + "\n"
	if err := os.WriteFile(path, []byte(src), 0o755); err != nil {
		t.Fatalf("write editor script: %v", err)
	}
	return path
}

// noteEditorEnv points $VISUAL and $EDITOR at the given values for one
// test; either may be "" to model it being unset.
func noteEditorEnv(t *testing.T, visual, editor string) {
	t.Helper()
	t.Setenv("VISUAL", visual)
	t.Setenv("EDITOR", editor)
}

// noteFiles lists the file names under <root>/notes, sorted by ReadDir; a
// missing directory is an empty list.
func noteFiles(t *testing.T, root string) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(root, "notes"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read notes dir: %v", err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

// readNote returns the bytes of <root>/notes/<name>.
func readNote(t *testing.T, root, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "notes", name))
	if err != nil {
		t.Fatalf("read note %s: %v", name, err)
	}
	return string(b)
}

// noteEditBuffer is the editor's scratch file under a vault root.
func noteEditBuffer(root string) string {
	return filepath.Join(root, ".llmwiki", "tmp", "NOTE_EDITMSG")
}

func TestNoteM(t *testing.T) {
	at := time.Date(2026, 10, 5, 14, 3, 9, 0, noteZone)

	t.Run("exact_file_and_stdout", func(t *testing.T) {
		setNoteClock(t, at)
		root := noteBareVault(t)

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"note", "-m", "Reply to NVIDIA support at 7 PM today  \n\n", "--vault", root})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		if want := "noted notes/20261005-140309-reply-to-nvidia-support-at-7.md\n"; stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
		if stderr != "" {
			t.Errorf("stderr = %q, want empty", stderr)
		}
		want := "---\ncreated: 2026-10-05T14:03:09+08:00\nsummarized: null\n---\n\nReply to NVIDIA support at 7 PM today\n"
		if got := readNote(t, root, "20261005-140309-reply-to-nvidia-support-at-7.md"); got != want {
			t.Errorf("note = %q, want %q", got, want)
		}
		if _, err := os.Stat(filepath.Join(root, ".llmwiki")); !os.IsNotExist(err) {
			t.Errorf("-m created .llmwiki (stat err = %v); only the editor path needs state", err)
		}
	})

	t.Run("slug_is_the_first_six_words", func(t *testing.T) {
		setNoteClock(t, at)
		root := noteBareVault(t)
		_, stderr, code := captureRun(t, func() int {
			return run([]string{"note", "--vault", root, "-m", "one two three four five six seven eight"})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		if got, want := noteFiles(t, root), []string{"20261005-140309-one-two-three-four-five-six.md"}; len(got) != 1 || got[0] != want[0] {
			t.Fatalf("notes = %v, want %v", got, want)
		}
	})

	t.Run("empty_slug_falls_back_to_note", func(t *testing.T) {
		for _, text := range []string{"日本語のメモ", "!!! ???"} {
			setNoteClock(t, at)
			root := noteBareVault(t)
			_, stderr, code := captureRun(t, func() int {
				return run([]string{"note", "-m", text, "--vault", root})
			})
			if code != 0 {
				t.Fatalf("%q: exit code = %d, want 0; stderr=%q", text, code, stderr)
			}
			if got := noteFiles(t, root); len(got) != 1 || got[0] != "20261005-140309-note.md" {
				t.Errorf("%q: notes = %v, want [20261005-140309-note.md]", text, got)
			}
		}
	})

	t.Run("collision_gets_a_numeric_suffix", func(t *testing.T) {
		setNoteClock(t, at)
		root := noteBareVault(t)
		texts := []string{"a b c d e f 1", "a b c d e f 2", "a b c d e f 3"}
		stems := []string{
			"20261005-140309-a-b-c-d-e-f.md",
			"20261005-140309-a-b-c-d-e-f-2.md",
			"20261005-140309-a-b-c-d-e-f-3.md",
		}
		for i, text := range texts {
			stdout, stderr, code := captureRun(t, func() int {
				return run([]string{"note", "-m", text, "--vault", root})
			})
			if code != 0 {
				t.Fatalf("note %d: exit code = %d, want 0; stderr=%q", i, code, stderr)
			}
			if want := "noted notes/" + stems[i] + "\n"; stdout != want {
				t.Errorf("note %d: stdout = %q, want %q", i, stdout, want)
			}
		}
		// Every note survives with its own text: a collision never overwrites.
		for i, name := range stems {
			if got := readNote(t, root, name); !strings.HasSuffix(got, "\n\n"+texts[i]+"\n") {
				t.Errorf("%s = %q, want it to end with the text %q", name, got, texts[i])
			}
		}
	})

	t.Run("real_clock_pattern", func(t *testing.T) {
		root := noteBareVault(t)
		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"note", "-m", "pattern check", "--vault", root})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		m := regexp.MustCompile(`^noted notes/(\d{8}-\d{6})-pattern-check\.md\n$`).FindStringSubmatch(stdout)
		if m == nil {
			t.Fatalf("stdout = %q, want noted notes/<yyyymmdd-hhmmss>-pattern-check.md", stdout)
		}
		got := readNote(t, root, m[1]+"-pattern-check.md")
		cm := regexp.MustCompile(`^---\ncreated: (\S+)\nsummarized: null\n---\n\npattern check\n$`).FindStringSubmatch(got)
		if cm == nil {
			t.Fatalf("note = %q, want the frozen frontmatter and body", got)
		}
		created, err := time.Parse(time.RFC3339, cm[1])
		if err != nil {
			t.Fatalf("created %q is not RFC 3339: %v", cm[1], err)
		}
		// The name and the created: line are one instant in one zone.
		if stamp := created.Format("20060102-150405"); stamp != m[1] {
			t.Errorf("created %q formats as %q, but the file name carries %q", cm[1], stamp, m[1])
		}
	})

	t.Run("a_huge_word_cannot_overflow_the_file_name", func(t *testing.T) {
		setNoteClock(t, at)
		root := noteBareVault(t)
		text := strings.Repeat("a", 400)
		_, stderr, code := captureRun(t, func() int {
			return run([]string{"note", "-m", text, "--vault", root})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		names := noteFiles(t, root)
		if len(names) != 1 {
			t.Fatalf("notes = %v, want exactly one", names)
		}
		if len(names[0]) > 255 {
			t.Errorf("file name is %d bytes, want <= 255 (a name past the filesystem limit loses the note)", len(names[0]))
		}
		if got := readNote(t, root, names[0]); !strings.HasSuffix(got, "\n\n"+text+"\n") {
			t.Errorf("the note body was cut: %q", got)
		}
	})

	t.Run("blank_message_saves_nothing", func(t *testing.T) {
		setNoteClock(t, at)
		root := noteBareVault(t)
		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"note", "-m", "  \n\t", "--vault", root})
		})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
		if stdout != "" || stderr != "empty note; nothing saved\n" {
			t.Errorf("stdout = %q, stderr = %q, want empty and %q", stdout, stderr, "empty note; nothing saved\n")
		}
		if _, err := os.Stat(filepath.Join(root, "notes")); !os.IsNotExist(err) {
			t.Errorf("a blank -m created notes/ (stat err = %v)", err)
		}
	})

	t.Run("a_directory_that_is_not_a_vault_is_refused", func(t *testing.T) {
		setNoteClock(t, at)
		dir := t.TempDir()
		_, stderr, code := captureRun(t, func() int {
			return run([]string{"note", "-m", "hello", "--vault", dir})
		})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1; stderr=%q", code, stderr)
		}
		if !strings.Contains(stderr, "SCHEMA.md") {
			t.Errorf("stderr = %q, want it to name the missing SCHEMA.md", stderr)
		}
		if _, err := os.Stat(filepath.Join(dir, "notes")); !os.IsNotExist(err) {
			t.Errorf("a typo'd --vault grew a notes/ directory (stat err = %v)", err)
		}
	})
}

func TestNoteEditor(t *testing.T) {
	at := time.Date(2026, 10, 5, 14, 3, 9, 0, noteZone)

	t.Run("saved_with_slug_from_the_first_line", func(t *testing.T) {
		setNoteClock(t, at)
		root := noteBareVault(t)
		noteEditorEnv(t, "", noteEditorScript(t, `printf 'hello\nworld' > "$last"`))

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"note", "--vault", root})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		if want := "noted notes/20261005-140309-hello.md\n"; stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
		want := "---\ncreated: 2026-10-05T14:03:09+08:00\nsummarized: null\n---\n\nhello\nworld\n"
		if got := readNote(t, root, "20261005-140309-hello.md"); got != want {
			t.Errorf("note = %q, want %q", got, want)
		}
		if _, err := os.Stat(noteEditBuffer(root)); !os.IsNotExist(err) {
			t.Errorf("the edit buffer was left behind (stat err = %v)", err)
		}
	})

	t.Run("whitespace_only_buffer_aborts", func(t *testing.T) {
		setNoteClock(t, at)
		root := noteBareVault(t)
		noteEditorEnv(t, "", noteEditorScript(t, `printf '  \n' > "$last"`))

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"note", "--vault", root})
		})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
		if stdout != "" || stderr != "empty note; nothing saved\n" {
			t.Errorf("stdout = %q, stderr = %q, want empty and %q", stdout, stderr, "empty note; nothing saved\n")
		}
		if got := noteFiles(t, root); len(got) != 0 {
			t.Errorf("notes = %v, want none", got)
		}
		if _, err := os.Stat(noteEditBuffer(root)); !os.IsNotExist(err) {
			t.Errorf("the edit buffer was left behind (stat err = %v)", err)
		}
	})

	t.Run("editor_failure_aborts", func(t *testing.T) {
		setNoteClock(t, at)
		root := noteBareVault(t)
		// The editor wrote a perfectly good buffer, then died: nothing is saved.
		noteEditorEnv(t, "", noteEditorScript(t, `printf 'hello' > "$last"; exit 3`))

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"note", "--vault", root})
		})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
		if want := "editor failed: exit status 3; nothing saved\n"; stdout != "" || stderr != want {
			t.Errorf("stdout = %q, stderr = %q, want empty and %q", stdout, stderr, want)
		}
		if got := noteFiles(t, root); len(got) != 0 {
			t.Errorf("notes = %v, want none", got)
		}
		if _, err := os.Stat(noteEditBuffer(root)); !os.IsNotExist(err) {
			t.Errorf("the edit buffer was left behind (stat err = %v)", err)
		}
	})

	t.Run("editor_that_cannot_start_aborts", func(t *testing.T) {
		setNoteClock(t, at)
		root := noteBareVault(t)
		noteEditorEnv(t, "", filepath.Join(t.TempDir(), "no-such-editor"))

		_, stderr, code := captureRun(t, func() int {
			return run([]string{"note", "--vault", root})
		})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
		if !strings.HasPrefix(stderr, "editor failed: ") || !strings.HasSuffix(stderr, "; nothing saved\n") {
			t.Errorf("stderr = %q, want editor failed: <err>; nothing saved", stderr)
		}
		if _, err := os.Stat(noteEditBuffer(root)); !os.IsNotExist(err) {
			t.Errorf("the edit buffer was left behind (stat err = %v)", err)
		}
	})

	t.Run("editor_string_is_split_and_the_empty_buffer_is_the_last_arg", func(t *testing.T) {
		setNoteClock(t, at)
		root := noteBareVault(t)
		record := filepath.Join(t.TempDir(), "record")
		script := noteEditorScript(t, `{ printf '%s\n' "$#"; for a; do printf '%s\n' "$a"; done; wc -c < "$last"; } > `+record+`
printf 'x' > "$last"`)
		// Two flags before the file, split on whitespace the way git splits.
		noteEditorEnv(t, "", script+" -w --flag")

		_, stderr, code := captureRun(t, func() int {
			return run([]string{"note", "--vault", root})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		b, err := os.ReadFile(record)
		if err != nil {
			t.Fatalf("read record: %v", err)
		}
		lines := strings.Fields(string(b))
		want := []string{"3", "-w", "--flag", noteEditBuffer(root), "0"}
		if strings.Join(lines, "|") != strings.Join(want, "|") {
			t.Errorf("editor saw %q, want %q (argc, args…, buffer size)", lines, want)
		}
	})

	t.Run("vi_is_the_default", func(t *testing.T) {
		setNoteClock(t, at)
		root := noteBareVault(t)
		noteEditorEnv(t, "", "")
		// A fake vi first on a PATH of its own: the real one is never run.
		bin := t.TempDir()
		vi := filepath.Join(bin, "vi")
		if err := os.WriteFile(vi, []byte("#!/bin/sh\nfor last; do :; done\nprintf 'from vi' > \"$last\"\n"), 0o755); err != nil {
			t.Fatalf("write fake vi: %v", err)
		}
		t.Setenv("PATH", bin)

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"note", "--vault", root})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		if want := "noted notes/20261005-140309-from-vi.md\n"; stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	})
}

func TestNoteVisualWins(t *testing.T) {
	setNoteClock(t, time.Date(2026, 10, 5, 14, 3, 9, 0, noteZone))
	root := noteBareVault(t)
	noteEditorEnv(t,
		noteEditorScript(t, `printf 'from visual' > "$last"`),
		noteEditorScript(t, `printf 'from editor' > "$last"`))

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"note", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if want := "noted notes/20261005-140309-from-visual.md\n"; stdout != want {
		t.Errorf("stdout = %q, want %q (VISUAL must win over EDITOR)", stdout, want)
	}
}

func TestNoteList(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, noteZone)

	// capture saves one note as if it were taken at at, in whatever order
	// the test chooses — list must sort by time, not by arrival.
	capture := func(t *testing.T, root string, at time.Time, text string) {
		t.Helper()
		setNoteClock(t, at)
		if _, stderr, code := captureRun(t, func() int {
			return run([]string{"note", "-m", text, "--vault", root})
		}); code != 0 {
			t.Fatalf("note -m %q: exit code = %d; stderr=%q", text, code, stderr)
		}
	}

	t.Run("newest_first", func(t *testing.T) {
		root := noteBareVault(t)
		// Arrival order is deliberately not time order.
		capture(t, root, now.Add(-2*time.Hour), "middle note\nwith a second line")
		capture(t, root, now.Add(-3*24*time.Hour), "oldest note")
		capture(t, root, now.Add(-5*time.Minute), "\n\nnewest note after blank lines")
		setNoteClock(t, now)

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"note", "list", "--vault", root})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		want := "20261005-115500-newest-note-after-blank-lines  5m ago  newest note after blank lines\n" +
			"20261005-100000-middle-note  2h ago  middle note\n" +
			"20261002-120000-oldest-note  3d ago  oldest note\n"
		if stdout != want {
			t.Errorf("stdout =\n%s\nwant\n%s", stdout, want)
		}
	})

	t.Run("n_limits_to_the_newest", func(t *testing.T) {
		root := noteBareVault(t)
		capture(t, root, now.Add(-3*24*time.Hour), "oldest")
		capture(t, root, now.Add(-2*time.Hour), "middle")
		capture(t, root, now.Add(-5*time.Minute), "newest")
		setNoteClock(t, now)

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"note", "list", "-n", "2", "--vault", root})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n"); len(lines) != 2 ||
			!strings.Contains(lines[0], "newest") || !strings.Contains(lines[1], "middle") {
			t.Errorf("-n 2 printed %q, want the newest two", stdout)
		}
	})

	t.Run("long_first_line_is_cut_to_sixty_runes", func(t *testing.T) {
		root := noteBareVault(t)
		capture(t, root, now.Add(-time.Minute), strings.Repeat("é", 100))
		setNoteClock(t, now)

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"note", "list", "--vault", root})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		parts := strings.SplitN(strings.TrimRight(stdout, "\n"), "  ", 3)
		if len(parts) != 3 {
			t.Fatalf("stdout = %q, want <stem>  <age>  <text>", stdout)
		}
		if n := utf8.RuneCountInString(parts[2]); n > 60 {
			t.Errorf("text is %d runes, want <= 60: %q", n, parts[2])
		}
		if !strings.HasSuffix(parts[2], "…") {
			t.Errorf("text %q was cut but carries no ellipsis", parts[2])
		}
	})

	t.Run("a_hand_written_note_ages_by_its_mtime", func(t *testing.T) {
		root := noteBareVault(t)
		dir := filepath.Join(root, "notes")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "scratch.md")
		if err := os.WriteFile(path, []byte("just typed this in vim\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, now.Add(-10*time.Minute), now.Add(-10*time.Minute)); err != nil {
			t.Fatal(err)
		}
		// Not a note: a non-markdown file and a directory are skipped.
		if err := os.WriteFile(filepath.Join(dir, "scratch.txt"), []byte("no"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "sub.md"), 0o755); err != nil {
			t.Fatal(err)
		}
		setNoteClock(t, now)

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"note", "list", "--vault", root})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if want := "scratch  10m ago  just typed this in vim\n"; stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	})

	t.Run("no_notes_says_so", func(t *testing.T) {
		const want = "no notes yet — lw note -m \"…\"\n"
		setNoteClock(t, now)

		// No notes/ at all, then an empty one.
		root := noteBareVault(t)
		for step := 0; step < 2; step++ {
			stdout, stderr, code := captureRun(t, func() int {
				return run([]string{"note", "list", "--vault", root})
			})
			if code != 0 {
				t.Fatalf("step %d: exit code = %d, want 0; stderr=%q", step, code, stderr)
			}
			if stdout != want {
				t.Errorf("step %d: stdout = %q, want %q", step, stdout, want)
			}
			if err := os.MkdirAll(filepath.Join(root, "notes"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	})
}

func TestNoteUnknownSub(t *testing.T) {
	cases := []struct {
		name string
		args []string
		word string
	}{
		{"unknown_word", []string{"summarize"}, "summarize"},
		{"free_text_without_dash_m", []string{"remember the milk"}, "remember the milk"},
		{"stray_word_after_dash_m", []string{"-m", "hello", "extra"}, "extra"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setNoteClock(t, time.Date(2026, 10, 5, 14, 3, 9, 0, noteZone))
			root := noteBareVault(t)
			stdout, stderr, code := captureRun(t, func() int {
				return run(append([]string{"note", "--vault", root}, tc.args...))
			})
			if code != 2 {
				t.Fatalf("exit code = %d, want 2", code)
			}
			want := "lw note: unknown subcommand \"" + tc.word + "\" (free text goes in -m)\n"
			if stdout != "" || stderr != want {
				t.Errorf("stdout = %q, stderr = %q, want empty and %q", stdout, stderr, want)
			}
			if got := noteFiles(t, root); len(got) != 0 {
				t.Errorf("a refused invocation saved %v", got)
			}
		})
	}

	t.Run("dash_m_cannot_be_combined_with_list", func(t *testing.T) {
		root := noteBareVault(t)
		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"note", "--vault", root, "-m", "hello", "list"})
		})
		if code != 2 {
			t.Fatalf("exit code = %d, want 2", code)
		}
		if want := "lw note: -m cannot be combined with \"list\"\n"; stdout != "" || stderr != want {
			t.Errorf("stdout = %q, stderr = %q, want empty and %q", stdout, stderr, want)
		}
		if got := noteFiles(t, root); len(got) != 0 {
			t.Errorf("a refused invocation saved %v", got)
		}
	})

	t.Run("dash_m_twice_is_refused_not_silently_dropped", func(t *testing.T) {
		root := noteBareVault(t)
		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"note", "--vault", root, "-m", "first", "-m", "second"})
		})
		if code != 2 {
			t.Fatalf("exit code = %d, want 2", code)
		}
		if want := "lw note: -m given more than once\n"; stdout != "" || stderr != want {
			t.Errorf("stdout = %q, stderr = %q, want empty and %q", stdout, stderr, want)
		}
		if got := noteFiles(t, root); len(got) != 0 {
			t.Errorf("a refused invocation saved %v", got)
		}
	})
}

// TestNoteHelpRow pins the one --help row 047 S1 adds.
func TestNoteHelpRow(t *testing.T) {
	stdout, _, code := captureRun(t, func() int {
		return run([]string{"--help"})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	const summary = "capture a quick raw note outside the wiki (no LLM, no review)"
	found := false
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "  note ") && strings.HasSuffix(line, summary) {
			found = true
		}
	}
	if !found {
		t.Errorf("--help has no `note` row ending in %q:\n%s", summary, stdout)
	}
}

// noteToolOutputs runs raw.list, wiki.search and vault.orient through the
// registry exactly as the agent's read path would, over a freshly opened
// engine — so each call sees a fresh load of the vault.
func noteToolOutputs(t *testing.T, root string) map[string]string {
	t.Helper()
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("open engine: %v", err)
	}
	defer e.Close()
	reg := tools.NewRegistry(tools.Deps{Vault: e.Vault(), Index: e.Index(), Engine: e})

	calls := []struct{ name, args string }{
		{"raw.list", `{}`},
		{"wiki.search", `{"q": "zebrafishtoken"}`},
		{"wiki.search", `{"q": "attention"}`},
		{"vault.orient", `{}`},
	}
	out := map[string]string{}
	for _, c := range calls {
		res, err := reg.Call(context.Background(), c.name, json.RawMessage(c.args))
		if err != nil {
			t.Fatalf("%s %s: %v", c.name, c.args, err)
		}
		out[c.name+" "+c.args] = res.Content
	}
	return out
}

// TestNotesInvisible pins 047 S1's isolation promise: notes/ sits outside
// wiki/ and raw/, so a vault carrying notes answers lint, status, doctor and
// the agent's read tools byte for byte as it did without them — even when
// the notes hold text that would trip a lint check or match a search if it
// were seen.
func TestNotesInvisible(t *testing.T) {
	doctorTestEnv(t)
	root := testutil.CopyFixture(t, "minimal")

	cmds := map[string][]string{
		"lint":        {"lint", "--vault", root},
		"lint --json": {"lint", "--json", "--vault", root},
		"status":      {"status", "--vault", root},
		"doctor":      {"doctor", "--json", "--vault", root},
	}
	order := []string{"lint", "lint --json", "status", "doctor"}
	snapshot := func() map[string]string {
		out := map[string]string{}
		for _, name := range order {
			stdout, stderr, code := captureRun(t, func() int { return run(cmds[name]) })
			out[name] = stdout + "\n--stderr--\n" + stderr + "\n--code--\n" + strconv.Itoa(code)
		}
		for k, v := range noteToolOutputs(t, root) {
			out[k] = v
		}
		return out
	}

	snapshot() // warm-up: doctor and the engine materialise .llmwiki state on first contact
	before := snapshot()

	// A mix a loader would choke on: a note that would cite a missing raw
	// file and link a missing page, a hand-written note with broken
	// frontmatter, and a nested one.
	setNoteClock(t, time.Date(2026, 10, 5, 14, 3, 9, 0, noteZone))
	if _, stderr, code := captureRun(t, func() int {
		return run([]string{"note", "-m", "zebrafishtoken [[No Such Page]] ^[raw/ghost.md] attention is all you need", "--vault", root})
	}); code != 0 {
		t.Fatalf("note -m: exit code = %d; stderr=%q", code, stderr)
	}
	for rel, body := range map[string]string{
		"notes/broken.md":      "---\ntitle: [unclosed\n---\nzebrafishtoken attention\n",
		"notes/sub/nested.md":  "zebrafishtoken\n",
		"notes/not-a-note.txt": "zebrafishtoken\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	after := snapshot()
	for k, want := range before {
		if got := after[k]; got != want {
			t.Errorf("%s changed once notes/ existed:\n--- without notes\n%s\n--- with notes\n%s", k, want, got)
		}
	}
	for k, v := range after {
		if strings.Contains(v, "notes/") || strings.Contains(v, "zebrafishtoken [[") {
			t.Errorf("%s output mentions a note:\n%s", k, v)
		}
	}
}

// TestNotesInvisibleToHistory pins the half of 047 S1's isolation promise
// that TestNotesInvisible cannot reach: commit history. Every commit's
// snapshot manifest is built by walking the whole vault for *.md, and a
// walk that does not skip notes/ records each note in it — so a note taken
// between two commits shows up as an ADDED path in the next one's delta, and
// `lw revert` reports it as "skipped: notes/…" in a changeset about wiki
// pages (and lw doctor counts it among the snapshot's entries). The fix is
// the explicit notes/ skip in stage's whole-vault walk, not anything in
// `lw note`.
func TestNotesInvisibleToHistory(t *testing.T) {
	setNoteClock(t, time.Date(2026, 10, 5, 14, 3, 9, 0, noteZone))
	root := testutil.CopyFixture(t, "minimal")

	commit := func(id, msg, path, title string) {
		t.Helper()
		openCreatePageChangeset(t, root, path, title)
		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"commit", "--vault", root, "-m", msg})
		})
		if code != 0 || !strings.Contains(stdout, "committed "+id) {
			t.Fatalf("commit %s: exit = %d, stdout=%q stderr=%q", id, code, stdout, stderr)
		}
	}

	commit("000001", "first page", "wiki/concepts/first-page.md", "First Page")
	// The note lands between two commits, so it is the one file the second
	// snapshot has that the first does not.
	if _, stderr, code := captureRun(t, func() int {
		return run([]string{"note", "-m", "remember the milk", "--vault", root})
	}); code != 0 {
		t.Fatalf("note -m: exit code = %d; stderr=%q", code, stderr)
	}
	commit("000002", "second page", "wiki/concepts/second-page.md", "Second Page")

	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	for _, id := range []string{"000000", "000001", "000002"} {
		snap, err := e.Snapshot(id)
		if err != nil {
			t.Fatalf("Snapshot(%s): %v", id, err)
		}
		for p := range snap {
			if strings.HasPrefix(p, "notes/") {
				t.Errorf("snapshot %s records %s; notes/ is outside the vault's history", id, p)
			}
		}
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"revert", "--vault", root, "000002"})
	})
	if code != 0 {
		t.Fatalf("revert: exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if strings.Contains(stdout, "notes/") {
		t.Errorf("revert output mentions a note:\n%s", stdout)
	}
}
