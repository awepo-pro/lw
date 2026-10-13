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
// The shape copies `git commit`: `-m "<text>"` takes the text inline, `-F
// <file>` (or `-F -` for stdin) reads it from a file, and neither opens
// $VISUAL, else $EDITOR, else vi, on a scratch buffer. Free text therefore
// only ever travels via -m or -F, which is what lets every positional word be
// a subcommand (`list` now; 044 phase 2 adds summarize and done) without ever
// being ambiguous: `lw note remember the milk` is an error, not a note.
//
// 059: the editor path never loses what the user typed. EDITOR=subl hands the
// file to a running Sublime and exits 0 at once, so the buffer was read empty
// and deleted while the user's 1410 B draft landed in a file nobody read. The
// buffer is now the draft: it is reopened, not truncated, when it holds text;
// an editor that returned at once without touching it is refused by name; and
// it is removed only on a save (or when it holds nothing).

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	// The name is fixed (git's COMMIT_EDITMSG idiom), so two concurrent `lw
	// note` editors in one vault would share it, the same as two concurrent git
	// commits do. It is removed on a save and whenever it holds no text; a
	// buffer that holds text survives, and the next run reopens it (059).
	noteEditBufferName = "NOTE_EDITMSG"

	// noteEditBufferRel is the buffer as the messages spell it: relative to
	// the vault, slash-separated on every OS.
	noteEditBufferRel = stateDirName + "/tmp/" + noteEditBufferName

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

	// noteMaxBytes is the largest note, in bytes of input text (059 D3). A
	// capture verb has no business holding a document; that is lw ingest's job,
	// and the cap also bounds what -F reads from a pipe.
	noteMaxBytes = 64 << 10
)

// noteBOM is the UTF-8 byte-order mark some Windows editors put in front of a
// file. It is not text: vetNoteText drops one for every input path, and
// readNoteSource does not count it against the cap.
const noteBOM = "\xef\xbb\xbf"

// noteKeyLine matches the first line of a top-level YAML entry, the way the
// front-matter merge reads it: not indented, not a comment, not a list item.
// Anything else in the block (indented lines, "- " items) belongs to the
// entry above it.
var noteKeyLine = regexp.MustCompile(`^[^\s#-][^:]*:`)

// noteEditorQuick is how fast an editor must exit 0, leaving the buffer as lw
// wrote it, to be taken for one that does not wait (059). It is a var so a
// test that models a slow editor with a do-nothing script can set it to 0;
// the elapsed time is measured with the monotonic clock, not noteNow, which
// tests pin.
var noteEditorQuick = time.Second

// noteNow is the verb's one clock: the file-name stamp, the created: line
// and the list's ages all read it, so a test pins them by swapping this var
// instead of racing the wall clock (the newAgent / probeProvider seam shape).
// time.Now() is in the local zone, which is what the file name and the
// created: line are specified in.
var noteNow = time.Now

// cmdNote dispatches lw note: -m saves its text, -F reads it from a file (or
// stdin), neither composes it in the editor, and the one positional word
// `list` lists. Every other positional word is refused with exit 2 — the rule
// that keeps subcommands unambiguous. The flag package stops at the first
// positional, so `lw note --vault P list -n 5` reaches list with --vault
// already parsed here and -n parsed by list.
func cmdNote(args []string) error {
	fs := flag.NewFlagSet("note", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `usage: lw note -m "<text>" [--vault P]   save a note
       lw note -F <file|-> [--vault P]   save a note read from a file (- is stdin)
       lw note [--vault P]              compose one in $VISUAL, else $EDITOR, else vi
       lw note list [-n N] [--vault P]  list notes, newest first
`)
	}
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	var texts, files []string
	fs.Func("m", "the note text (omit to compose it in an editor)", func(s string) error {
		texts = append(texts, s)
		return nil
	})
	fs.Func("F", "read the note text from a file, or from stdin when it is -", func(s string) error {
		files = append(files, s)
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	// A second -m or -F is refused rather than last-one-wins: dropping text the
	// user typed is the failure a capture tool cannot afford. The two sources
	// cannot be combined for the same reason — one of them would be dropped.
	if len(texts) > 1 {
		fmt.Fprintln(os.Stderr, "lw note: -m given more than once")
		return &exitError{code: 2}
	}
	if len(files) > 1 {
		fmt.Fprintln(os.Stderr, "lw note: -F given more than once")
		return &exitError{code: 2}
	}
	if len(texts) == 1 && len(files) == 1 {
		fmt.Fprintln(os.Stderr, "lw note: -m and -F cannot be combined")
		return &exitError{code: 2}
	}

	if rest := fs.Args(); len(rest) > 0 {
		if rest[0] == "list" {
			if len(texts) > 0 {
				fmt.Fprintln(os.Stderr, `lw note: -m cannot be combined with "list"`)
				return &exitError{code: 2}
			}
			if len(files) > 0 {
				fmt.Fprintln(os.Stderr, `lw note: -F cannot be combined with "list"`)
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
	// 042: a note is a tracked file in a synced vault, so it is refused while a
	// checkout collision is unresolved, taken after the newest vault has been
	// pulled (before an editor opens, so the pull does not wait on one), and
	// pushed once it is on disk.
	if err := collisionRefusal(root); err != nil {
		return err
	}
	// Only the editor path has a buffer to look after (D1.5): -m and -F never
	// read, write or remove it.
	fromEditor := len(texts) == 0 && len(files) == 0

	// 059: -m and -F put the whole text in hand before the logger and the
	// pull, so a source that cannot be read, or text lw will refuse, leaves the
	// vault untouched — no fetch, no push, no stray state. -F comes after the
	// collision refusal so a refused verb has not consumed stdin: a pipe read
	// once is gone. The editor path can only be judged after the editor has
	// run, which is after the pull (the pull must not wait on an editor).
	var text string
	switch {
	case len(texts) == 1:
		text = texts[0]
	case len(files) == 1:
		if text, err = readNoteSource(files[0]); err != nil {
			// The path is named once, as the user spelled it.
			fmt.Fprintf(os.Stderr, "lw note: read %s: %v; nothing saved\n", files[0], noteCause(err))
			return &exitError{code: 1}
		}
	}
	if !fromEditor {
		var refusal string
		if text, refusal = vetNoteText(text); refusal != "" {
			fmt.Fprintln(os.Stderr, refusal)
			return &exitError{code: 1}
		}
	}

	attachLoggingAt(root) // notes/ is not lw state: join the trail, never create it
	auto := loadAutoSync(root)
	auto.pull()

	if fromEditor {
		text, err = composeNote(root)
		if err != nil {
			var quick *noteQuickError
			if errors.As(err, &quick) {
				fmt.Fprintln(os.Stderr, quick)
			} else {
				fmt.Fprintf(os.Stderr, "editor failed: %v; nothing saved\n", err)
			}
			keepNoteDraft(root)
			return &exitError{code: 1}
		}
		var refusal string
		if text, refusal = vetNoteText(text); refusal != "" {
			fmt.Fprintln(os.Stderr, refusal)
			keepNoteDraft(root)
			return &exitError{code: 1}
		}
	}

	rel, err := saveNote(root, text, noteNow())
	if err != nil {
		if fromEditor {
			keepNoteDraft(root)
		}
		return err
	}
	if fromEditor {
		os.Remove(noteBufferPath(root)) // saved: the draft is the note now
	}
	fmt.Println("noted " + rel)
	auto.afterNote()
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
	if err := requireSchema(root); err != nil {
		return "", err
	}
	return root, nil
}

// noteBufferPath is the editor's scratch file in the vault at root.
func noteBufferPath(root string) string {
	return filepath.Join(root, stateDirName, "tmp", noteEditBufferName)
}

// noteQuickError is composeNote's refusal of an editor that returned at once
// and left the buffer as lw wrote it: the shape of `subl`, `code`, `zed`, which
// hand the file to a running instance and exit 0 before the user has typed a
// word (059). Its text is the three frozen lines of D1.2.
type noteQuickError struct {
	name   string // the editor's program name, filepath.Base(argv[0])
	envVar string // VISUAL if it chose the editor, else EDITOR
	value  string // that variable's value (or vi), trimmed
}

func (e *noteQuickError) Error() string {
	return "lw note: " + e.name + " returned at once and left the note unchanged — it does not wait for you to finish\n" +
		"  make it wait, e.g. export " + e.envVar + "=\"" + e.value + " -w\", or use lw note -m \"…\" / lw note -F <file>\n" +
		"  anything you save in that editor now lands in " + noteEditBufferRel + "; the next lw note reopens it"
}

// composeNote opens the user's editor on the scratch buffer under
// <vault>/.llmwiki/tmp and returns what is in it afterwards. The editor string
// splits on whitespace — no shell quoting — and the buffer is appended as the
// last argument, so EDITOR="code -w" works and EDITOR="sh -c '…'" does not.
// $VISUAL beats $EDITOR beats vi, and an unset, empty or blank variable
// counts as unset — git's order.
//
// A buffer that already holds text is a draft an earlier session lost track
// of (the editor exited early, crashed, or saved late — EDITOR=subl orphaned
// a real 1410 B note exactly so), so it is reopened as it is, with a line on
// stderr saying so; only a buffer with no text is (re)created empty. Whatever
// happens next, composeNote never deletes the buffer: cmdNote settles it,
// because only the caller knows whether the text was saved.
//
// An editor that cannot start or exits non-zero returns its error: the caller
// saves nothing, because a half-written buffer from a crashed editor is not a
// note the user chose to keep — it is kept as a draft instead. An editor that
// exits 0 in under noteEditorQuick with the buffer byte-for-byte as lw left it
// returns a *noteQuickError.
func composeNote(root string) (string, error) {
	argv, envVar, value := noteEditorArgv(os.Getenv("VISUAL"), os.Getenv("EDITOR"))

	buf := noteBufferPath(root)
	if err := os.MkdirAll(filepath.Dir(buf), 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", filepath.Dir(buf), noteCause(err))
	}
	seed, err := os.ReadFile(buf)
	switch {
	case err == nil && noteHasText(seed):
		fmt.Fprintln(os.Stderr, "lw note: reopening the unsaved draft in "+noteEditBufferRel)
	case err == nil || errors.Is(err, os.ErrNotExist):
		seed = nil
		if err := os.WriteFile(buf, nil, 0o600); err != nil {
			return "", fmt.Errorf("create %s: %w", buf, noteCause(err))
		}
	default:
		return "", fmt.Errorf("read %s: %w", buf, noteCause(err))
	}

	cmd := exec.Command(argv[0], append(argv[1:], buf)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	start := time.Now()
	if err := cmd.Run(); err != nil {
		return "", err
	}
	elapsed := time.Since(start)
	b, err := os.ReadFile(buf)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", buf, noteCause(err))
	}
	if elapsed < noteEditorQuick && bytes.Equal(b, seed) {
		return "", &noteQuickError{name: filepath.Base(argv[0]), envVar: envVar, value: value}
	}
	return string(b), nil
}

// keepNoteDraft is the one rule for every editor-path outcome that saves
// nothing (059 D1.3): the buffer is kept iff it holds non-whitespace text, and
// then stderr says where, so the words the user typed are never lost to an
// error message about something else. A buffer with no text is removed.
func keepNoteDraft(root string) {
	buf := noteBufferPath(root)
	b, err := os.ReadFile(buf)
	if err != nil {
		return // gone, or unreadable: nothing to say about it
	}
	if !noteHasText(b) {
		os.Remove(buf)
		return
	}
	fmt.Fprintln(os.Stderr, "draft kept in "+noteEditBufferRel+"; the next lw note reopens it")
}

// noteHasText reports whether b holds anything but whitespace and a leading
// BOM — the test for "is this buffer a draft".
func noteHasText(b []byte) bool {
	return len(bytes.TrimSpace(bytes.TrimPrefix(b, []byte(noteBOM)))) > 0
}

// noteCause is err without the *os.PathError wrapper that names the path
// again, for messages that already name it: "read X: is a directory", not
// "read X: read X: is a directory". The cause is still the syscall error, so
// errors.Is keeps working on what is wrapped around it.
func noteCause(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// noteEditorArgv picks the editor command line: visual, else editor, else
// vi, each split on whitespace. A value with no words is skipped, so
// VISUAL="" never shadows a real $EDITOR. It also returns the variable that
// chose the editor and its value, trimmed, for the message that tells the user
// how to make it wait; vi, chosen by neither, is reported as EDITOR.
func noteEditorArgv(visual, editor string) (argv []string, envVar, value string) {
	for _, c := range []struct{ name, val string }{{"VISUAL", visual}, {"EDITOR", editor}} {
		if f := strings.Fields(c.val); len(f) > 0 {
			return f, c.name, strings.TrimSpace(c.val)
		}
	}
	return []string{"vi"}, "EDITOR", "vi"
}

// readNoteSource reads the text for -F: the file at path, or stdin when path
// is "-". The source is only read, never modified. At most noteMaxBytes+1 bytes
// of text are kept — one past the cap is enough to know it was exceeded, and a
// huge stdin cannot exhaust memory — so the size the cap check reports for -F
// is the bytes read, 65537 when the source is longer, not the source's size.
// A leading UTF-8 BOM is returned with the text (vetNoteText drops it, once,
// for every path) but does not count against the cap: it is not text, and
// counting it would let a BOM-led source slip past the cap with its tail cut.
func readNoteSource(path string) (string, error) {
	r := io.Reader(os.Stdin)
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, noteMaxBytes+1+int64(len(noteBOM))))
	if err != nil {
		return "", err
	}
	keep := noteMaxBytes + 1
	if bytes.HasPrefix(b, []byte(noteBOM)) {
		keep += len(noteBOM)
	}
	if len(b) > keep {
		b = b[:keep]
	}
	return string(b), nil
}

// vetNoteText is the one place every input path's text goes through (059):
// -m, the editor and -F. It drops one leading UTF-8 BOM — the checks and the
// front-matter test must see the text, not U+FEFF — and applies the content
// checks, returning the text to save or the refusal to print. The order is
// fixed: size first (so a huge input is refused without being scanned), then
// whether it is text at all, then whether anything is left once front matter
// is removed. The size is the bytes after the BOM, so a BOM never costs a note
// its last bytes under the cap.
func vetNoteText(text string) (clean, refusal string) {
	text = strings.TrimPrefix(text, noteBOM)
	if len(text) > noteMaxBytes {
		return text, fmt.Sprintf("lw note: the note is %d bytes; the limit is 64 KiB — a document this size belongs in lw ingest; nothing saved", len(text))
	}
	if strings.IndexByte(text, 0) >= 0 || !utf8.ValidString(text) {
		return text, "lw note: the note is not UTF-8 text; nothing saved"
	}
	if _, body, _ := splitNoteFrontMatter(text); strings.TrimSpace(body) == "" {
		return text, "empty note; nothing saved"
	}
	return text, ""
}

// splitNoteFrontMatter separates a leading ---/--- block from the text. The
// block is the lines between the fences, verbatim; body is everything after
// the closing fence; ok says there was a block at all. A text has front
// matter iff its first line is exactly "---" and a later line is exactly
// "---" — the rule parseNote reads with, so an unclosed block is not a block
// and the whole text is the body. A CR at the end of a fence line (CRLF text)
// is not part of the fence.
func splitNoteFrontMatter(text string) (block []string, body string, ok bool) {
	lines := strings.Split(text, "\n")
	if strings.TrimSuffix(lines[0], "\r") != "---" {
		return nil, text, false
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSuffix(lines[i], "\r") == "---" {
			return lines[1:i], strings.Join(lines[i+1:], "\n"), true
		}
	}
	return nil, text, false
}

// mergeNoteBlock drops, from their front-matter lines, the entries lw writes
// itself (created and summarized) so the saved file never carries a key twice.
// An entry starts at a noteKeyLine and runs to the next one, indented and "- "
// lines included; comment and blank lines are always kept. Nothing is parsed
// or re-serialised (C-134: re-serialising flattened lists): the surviving lines
// are copied as they are.
func mergeNoteBlock(block []string) []string {
	var kept []string
	dropping := false
	for _, l := range block {
		if m := noteKeyLine.FindString(l); m != "" {
			key := strings.TrimSpace(strings.TrimSuffix(m, ":"))
			dropping = key == "created" || key == "summarized"
		} else if strings.TrimSpace(l) == "" || strings.HasPrefix(l, "#") {
			kept = append(kept, l)
			continue
		}
		if !dropping {
			kept = append(kept, l)
		}
	}
	return kept
}

// saveNote writes text as a new note stamped at now and returns its
// vault-relative, slash-separated path. The file is created O_EXCL, so two
// notes in the same second with the same slug can never overwrite each other:
// the later one takes -2, -3, … until a name is free. Trailing whitespace is
// trimmed; leading whitespace is the user's own.
//
// Text that opens with its own front matter (an Obsidian file, a template) is
// merged, never doubled (059 D4): lw's created: and summarized: lines come
// first, then their block minus any created:/summarized: entries, then the
// body after their closing fence with its leading blank lines dropped. The
// slug and the list's text come from that body, not from the fence.
func saveNote(root, text string, now time.Time) (string, error) {
	block, body, merged := splitNoteFrontMatter(text)
	var extra string
	if merged {
		for _, l := range mergeNoteBlock(block) {
			extra += l + "\n"
		}
		lines := strings.Split(body, "\n")
		for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
			lines = lines[1:]
		}
		body = strings.Join(lines, "\n")
	}
	text = strings.TrimRightFunc(body, unicode.IsSpace)
	dir := filepath.Join(root, noteDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}

	content := "---\ncreated: " + now.Format(time.RFC3339) + "\nsummarized: null\n" + extra + "---\n\n" + text + "\n"
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

// noteDraftLine is the extra first line of the list while the editor buffer
// holds text (059 D1.4): the surface a user looks at when a note they typed
// seems to have vanished. It reads like a row — NOTE_EDITMSG for the stem, the
// buffer's age, its first non-blank line cut to the column — plus a tail that
// says what it is. A missing, empty or unreadable buffer is no draft: the list
// is a read-only view and does not fail over it.
func noteDraftLine(root string, now time.Time) string {
	buf := noteBufferPath(root)
	b, err := os.ReadFile(buf)
	if err != nil || !noteHasText(b) {
		return ""
	}
	info, err := os.Stat(buf)
	if err != nil {
		return ""
	}
	return noteEditBufferName + "  " + noteAge(now.Sub(info.ModTime())) + "  " +
		noteFirstLine(strings.Split(strings.TrimPrefix(string(b), noteBOM), "\n")) + "  (unsaved draft — lw note reopens it)"
}

// writeNoteList prints the newest n notes, one `<stem>  <age>  <text>` line
// each, or the one-line hint when there are none. A waiting draft's line
// (noteDraftLine) comes first in either case and is not a note, so n does not
// count it. Only regular .md files directly under notes/ are notes: the dir
// belongs to the user, who may drop a scratch.txt or a sub.md/ folder in it,
// and neither is a row. -n is a count of rows, so a negative is clamped to 0,
// the way `lw trace -n` reads it.
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

	if draft := noteDraftLine(root, now); draft != "" {
		fmt.Fprintln(w, draft)
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
	return created, noteFirstLine(body)
}

// noteFirstLine is the first non-blank line of lines, trimmed and cut to the
// list's column; "" when every line is blank.
func noteFirstLine(lines []string) string {
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			return cutNoteLine(l)
		}
	}
	return ""
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
