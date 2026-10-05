package main

// cmd_note.go implements `lw note` (047 S1, 044 phase 1): a cheap inbox for
// raw thoughts that belong in no wiki page yet. A note is one markdown file,
// <vault>/notes/<yyyymmdd-hhmmss>-<slug>.md, written straight to disk — no
// LLM call, no changeset, no review. That is deliberately unlike everything
// else in lw, and it is safe for one reason: notes/ is outside wiki/ and
// raw/, the only two trees the vault loader, the index, lint, doctor and
// the agent's read tools (raw.list, raw.get, wiki.search) ever walk, so a
// note cannot reach an answer or a citation. TestNotesInvisible pins that.
// One walker did see it: stage's whole-vault walk, which seeds the
// projection and every commit's snapshot, so a note taken between two
// commits surfaced in `lw revert` as "skipped: notes/…". That walk now
// skips notes/ explicitly (TestNotesInvisibleToHistory); if another walker
// ever sees it, the fix is the same explicit skip there, never a change to
// this verb. The agent gets no note.* tool (invariant 1: it has no
// filesystem verb), and nothing here is ever shown to it.
//
// The shape copies `git commit`: `-m "<text>"` takes the text inline, and
// no -m opens $VISUAL, else $EDITOR, else vi, on a scratch buffer. Free text
// therefore only ever travels via -m, which is what lets every positional
// word be a subcommand (`list` now; 044 phase 2 adds summarize and done)
// without ever being ambiguous: `lw note remember the milk` is an error,
// not a note.

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/awepo-pro/lw/internal/slug"
)

const (
	// noteDirName is the inbox, directly under the vault root.
	noteDirName = "notes"

	// noteEditBufferName is the editor's scratch file, under .llmwiki/tmp.
	// The name is fixed (git's COMMIT_EDITMSG idiom), so it is truncated on
	// every run and removed after it: two concurrent `lw note` editors in one
	// vault would share it, the same as two concurrent git commits do.
	noteEditBufferName = "NOTE_EDITMSG"

	// noteStampLayout is the file-name time prefix, in local time.
	noteStampLayout = "20060102-150405"

	// noteSlugWords is how many words of the first line name the file.
	noteSlugWords = 6

	// noteSlugMax caps the slug in bytes. Six words are usually short, but a
	// URL is one word, and a name past the filesystem's 255-byte limit would
	// fail the write and lose the note — the one thing this verb must never do.
	noteSlugMax = 60

	// noteListRunes is the width of the list's text column, ellipsis included.
	noteListRunes = 60

	// noteMaxCollisions bounds the -2, -3, … search for a free file name.
	noteMaxCollisions = 1000
)

// noteNow is the verb's one clock: the file-name stamp, the created: line
// and the list's ages all read it, so a test pins them by swapping this var
// instead of racing the wall clock (the newAgent / probeProvider seam shape).
// time.Now() is in the local zone, which is what the file name and the
// created: line are specified in.
var noteNow = time.Now

// cmdNote dispatches lw note: -m saves its text, no -m composes in the
// editor, and the one positional word `list` lists. Every other positional
// word is refused with exit 2 — the rule that keeps subcommands unambiguous.
// The flag package stops at the first positional, so `lw note --vault P list
// -n 5` reaches list with --vault already parsed here and -n parsed by list.
func cmdNote(args []string) error {
	fs := flag.NewFlagSet("note", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `usage: lw note -m "<text>" [--vault P]   save a note
       lw note [--vault P]              compose one in $VISUAL, else $EDITOR, else vi
       lw note list [-n N] [--vault P]  list notes, newest first
`)
	}
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	var texts []string
	fs.Func("m", "the note text (omit to compose it in an editor)", func(s string) error {
		texts = append(texts, s)
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	// A second -m is refused rather than last-one-wins: dropping text the user
	// typed is the failure a capture tool cannot afford.
	if len(texts) > 1 {
		fmt.Fprintln(os.Stderr, "lw note: -m given more than once")
		return &exitError{code: 2}
	}

	if rest := fs.Args(); len(rest) > 0 {
		if rest[0] == "list" {
			if len(texts) > 0 {
				fmt.Fprintln(os.Stderr, `lw note: -m cannot be combined with "list"`)
				return &exitError{code: 2}
			}
			return cmdNoteList(*vaultPath, rest[1:])
		}
		fmt.Fprintf(os.Stderr, "lw note: unknown subcommand %q (free text goes in -m)\n", rest[0])
		return &exitError{code: 2}
	}

	root, err := noteVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	attachLoggingAt(root) // notes/ is not lw state: join the trail, never create it

	var text string
	if len(texts) == 1 {
		text = texts[0]
	} else {
		text, err = composeNote(root)
		if err != nil {
			fmt.Fprintf(os.Stderr, "editor failed: %v; nothing saved\n", err)
			return &exitError{code: 1}
		}
	}
	if strings.TrimSpace(text) == "" {
		fmt.Fprintln(os.Stderr, "empty note; nothing saved")
		return &exitError{code: 1}
	}

	rel, err := saveNote(root, text, noteNow())
	if err != nil {
		return err
	}
	fmt.Println("noted " + rel)
	return nil
}

// noteVaultRoot resolves the vault like every other verb and then insists it
// is one. findVaultRoot trusts an explicit --vault as given, which is fine for
// a verb that goes on to open the vault and fail there; this verb writes a
// directory, so a typo'd --vault must not grow a stray notes/ in the wrong
// place. SCHEMA.md is the marker findVaultRoot itself walks up for.
func noteVaultRoot(explicit string) (string, error) {
	root, err := findVaultRoot(explicit)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(filepath.Join(root, "SCHEMA.md")); err != nil || info.IsDir() {
		return "", fmt.Errorf("%s is not a vault (no SCHEMA.md); pass --vault", root)
	}
	return root, nil
}

// composeNote opens the user's editor on an empty scratch buffer under
// <vault>/.llmwiki/tmp and returns what they wrote. The editor string splits
// on whitespace — no shell quoting — and the buffer is appended as the last
// argument, so EDITOR="code -w" works and EDITOR="sh -c '…'" does not.
// $VISUAL beats $EDITOR beats vi, and an unset, empty or blank variable
// counts as unset — git's order. The buffer is removed on every path.
// An editor that cannot start or exits non-zero returns its error: the caller
// saves nothing, because a half-written buffer from a crashed editor is not a
// note the user chose to keep.
func composeNote(root string) (string, error) {
	argv := noteEditorArgv(os.Getenv("VISUAL"), os.Getenv("EDITOR"))

	dir := filepath.Join(root, stateDirName, "tmp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	buf := filepath.Join(dir, noteEditBufferName)
	if err := os.WriteFile(buf, nil, 0o600); err != nil {
		return "", fmt.Errorf("create %s: %w", buf, err)
	}
	defer os.Remove(buf)

	cmd := exec.Command(argv[0], append(argv[1:], buf)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	b, err := os.ReadFile(buf)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", buf, err)
	}
	return string(b), nil
}

// noteEditorArgv picks the editor command line: visual, else editor, else
// vi, each split on whitespace. A value with no words is skipped, so
// VISUAL="" never shadows a real $EDITOR.
func noteEditorArgv(visual, editor string) []string {
	for _, v := range []string{visual, editor} {
		if f := strings.Fields(v); len(f) > 0 {
			return f
		}
	}
	return []string{"vi"}
}

// saveNote writes text as a new note stamped at now and returns its
// vault-relative, slash-separated path. The file is created O_EXCL, so two
// notes in the same second with the same slug can never overwrite each other:
// the later one takes -2, -3, … until a name is free. Trailing whitespace is
// trimmed; leading whitespace is the user's own.
func saveNote(root, text string, now time.Time) (string, error) {
	text = strings.TrimRightFunc(text, unicode.IsSpace)
	dir := filepath.Join(root, noteDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}

	content := "---\ncreated: " + now.Format(time.RFC3339) + "\nsummarized: null\n---\n\n" + text + "\n"
	base := now.Format(noteStampLayout) + "-" + noteSlug(text)
	for n := 1; n <= noteMaxCollisions; n++ {
		name := base + ".md"
		if n > 1 {
			name = fmt.Sprintf("%s-%d.md", base, n)
		}
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("create note: %w", err)
		}
		_, werr := f.WriteString(content)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			os.Remove(f.Name()) // never leave a truncated note behind
			return "", fmt.Errorf("write note: %w", werr)
		}
		return noteDirName + "/" + name, nil
	}
	return "", fmt.Errorf("create note: %d notes already share the name %s", noteMaxCollisions, base)
}

// noteSlug names a note after its first non-blank line: the first six words,
// slugged by the one rule the whole tree uses (slug.Make), cut to noteSlugMax
// at a word boundary, and "note" when nothing survives (CJK, emoji or
// punctuation-only text slugs to nothing).
func noteSlug(text string) string {
	var line string
	for _, l := range strings.Split(text, "\n") {
		if strings.TrimSpace(l) != "" {
			line = l
			break
		}
	}
	words := strings.Fields(line)
	if len(words) > noteSlugWords {
		words = words[:noteSlugWords]
	}
	s := slug.Make(strings.Join(words, " "))
	if len(s) > noteSlugMax {
		s = s[:noteSlugMax]
		if i := strings.LastIndexByte(s, '-'); i > 0 {
			s = s[:i]
		}
	}
	if s == "" {
		return "note"
	}
	return s
}

// cmdNoteList implements `lw note list [-n N]`. vaultDefault is the --vault
// value `lw note` parsed before the subcommand word, so both spellings work.
func cmdNoteList(vaultDefault string, args []string) error {
	fs := flag.NewFlagSet("note list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", vaultDefault, "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	n := fs.Int("n", 20, "how many notes to list")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "lw note list: unexpected argument %q\n", fs.Arg(0))
		return &exitError{code: 2}
	}
	root, err := noteVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	attachLoggingAt(root) // read-only: join the trail, never create it
	return writeNoteList(os.Stdout, root, noteNow(), *n)
}

// noteRow is one note as the list shows it.
type noteRow struct {
	stem    string    // file name without .md
	created time.Time // created: from the frontmatter, else the file's mtime
	text    string    // the first non-blank body line, cut for the column
}

// writeNoteList prints the newest n notes, one `<stem>  <age>  <text>` line
// each, or the one-line hint when there are none. Only regular .md files
// directly under notes/ are notes: the dir belongs to the user, who may drop a
// scratch.txt or a sub.md/ folder in it, and neither is a row. -n is a count
// of rows, so a negative is clamped to 0, the way `lw trace -n` reads it.
func writeNoteList(w io.Writer, root string, now time.Time, n int) error {
	ents, err := os.ReadDir(filepath.Join(root, noteDirName))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", noteDirName, err)
	}

	var rows []noteRow
	for _, e := range ents {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(root, noteDirName, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read note %s: %w", e.Name(), err)
		}
		created, text := parseNote(string(b))
		if created.IsZero() {
			info, err := os.Stat(path)
			if err != nil {
				return fmt.Errorf("stat note %s: %w", e.Name(), err)
			}
			created = info.ModTime()
		}
		rows = append(rows, noteRow{stem: strings.TrimSuffix(e.Name(), ".md"), created: created, text: text})
	}

	if len(rows) == 0 {
		fmt.Fprintln(w, `no notes yet — lw note -m "…"`)
		return nil
	}
	// Newest first; the stem breaks a tie so same-second notes (-2 after the
	// bare name) come out in one stable order.
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].created.Equal(rows[j].created) {
			return rows[i].created.After(rows[j].created)
		}
		return rows[i].stem > rows[j].stem
	})
	if n < 0 {
		n = 0
	}
	if len(rows) > n {
		rows = rows[:n]
	}
	for _, r := range rows {
		fmt.Fprintln(w, strings.TrimRight(r.stem+"  "+noteAge(now.Sub(r.created))+"  "+r.text, " "))
	}
	return nil
}

// parseNote reads one note file: the created: instant out of a leading
// ---/--- frontmatter block (zero when there is none, or it does not parse —
// the caller falls back to the file's mtime, since a note is user-editable
// and may have lost its frontmatter) and the first non-blank body line, cut
// to the list's column. A frontmatter block that never closes is not a block,
// so the whole text is the body.
func parseNote(content string) (created time.Time, text string) {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	body := lines
	if len(lines) > 0 && lines[0] == "---" {
		for i := 1; i < len(lines); i++ {
			if lines[i] != "---" {
				continue
			}
			for _, l := range lines[1:i] {
				if v, ok := strings.CutPrefix(l, "created:"); ok {
					if t, err := time.Parse(time.RFC3339, strings.TrimSpace(v)); err == nil {
						created = t
					}
				}
			}
			body = lines[i+1:]
			break
		}
	}
	for _, l := range body {
		if l = strings.TrimSpace(l); l != "" {
			return created, cutNoteLine(l)
		}
	}
	return created, ""
}

// cutNoteLine fits s into the list's text column: at most noteListRunes
// runes, with the last one an ellipsis when it had to cut.
func cutNoteLine(s string) string {
	if utf8.RuneCountInString(s) <= noteListRunes {
		return s
	}
	return string([]rune(s)[:noteListRunes-1]) + "…"
}

// noteAge renders how long ago a note was taken: whole minutes, hours or
// days, whichever is the largest unit that fits. A note from the future (a
// clock that moved back) reads as just now rather than a negative age.
func noteAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	}
}
