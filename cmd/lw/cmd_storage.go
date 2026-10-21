package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/awepo-pro/lw/internal/storage"
)

// cmdStorage dispatches lw storage subcommands (062 S1).
func cmdStorage(args []string) error {
	return storageCLIError(cmdStorageDispatch(args))
}

// prefixedError is err shown with a prefix its own text repeated, without the
// repetition; it still unwraps to err.
type prefixedError struct {
	err error
	msg string
}

func (e *prefixedError) Error() string { return e.msg }
func (e *prefixedError) Unwrap() error { return e.err }

// storageCLIError drops the "storage: " that internal/storage puts at the front
// of its errors: dispatch already prints "lw: storage: " before every error of
// this verb, and `lw: storage: storage: entry "x" not found` said it twice (066
// X-15, C-066-9). An *exitError is the verb's own exit code and passes through.
func storageCLIError(err error) error {
	var ee *exitError
	if err == nil || errors.As(err, &ee) {
		return err
	}
	if msg := err.Error(); strings.HasPrefix(msg, "storage: ") {
		return &prefixedError{err: err, msg: strings.TrimPrefix(msg, "storage: ")}
	}
	return err
}

func cmdStorageDispatch(args []string) error {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "lw storage: a subcommand is required")
		storageUsage(os.Stderr)
		return &exitError{code: 2}
	}
	switch sub, rest := args[0], args[1:]; sub {
	case "add":
		return cmdStorageAdd(rest)
	case "list":
		return cmdStorageList(rest)
	case "show", "info":
		return cmdStorageShow(rest)
	case "get":
		return cmdStorageGet(rest)
	case "rm", "remove":
		return cmdStorageRemove(rest)
	case "-h", "--help", "help":
		storageUsage(os.Stdout)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "lw storage: unknown subcommand %q\n", sub)
		storageUsage(os.Stderr)
		return &exitError{code: 2}
	}
}

func storageUsage(w io.Writer) {
	fmt.Fprint(w, `usage: lw storage add <file> [-m "<note>"] [--name <name>] [--force] [--vault P]
       lw storage list [-v] [--json] [--vault P]
       lw storage show <name> [--vault P]
       lw storage get <name> [-o <outpath>] [--vault P]
       lw storage rm <name> [--vault P]

Stored files live in storage/ and are listed in .llmwiki/storage.json, which git
carries; the bytes travel beside git, in <remote>.storage/objects/, and lw sync
moves them. lw storage rm removes the file and its catalog entry on every PC that
syncs; the object stays on the remote until a future lw storage gc.
`)
}

func parseStorageFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	current := args
	for len(current) > 0 {
		if err := fs.Parse(current); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		current = rest[1:]
	}
	return positional, nil
}

func formatStorageSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1048576:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	case n < 1073741824:
		return fmt.Sprintf("%.1f MB", float64(n)/1048576)
	default:
		return fmt.Sprintf("%.1f GB", float64(n)/1073741824)
	}
}

// storageVerifiedWord is what `lw storage list -v` and `lw storage show` print
// for a file whose bytes equal the catalog's. 062 printed "present" for exactly
// this (it hashed the file), TestStorageAddAndList and TestStorageShow pin that
// word, and 066 D4 asks for "verified"; the pinned word wins until those two
// assertions are amended. The other states are internal/storage's own words.
const storageVerifiedWord = "present"

// storageStateWord is the word a listing prints for a catalog entry's state
// (066 D4): pending is "not arrived yet", never "missing", and corrupt is
// reserved for bytes that something other than lw put there (B6). A pending entry
// says why when it is more than late: its file is an older version (X-12) or the
// object on the remote is damaged (X-4).
func storageStateWord(es storage.EntryStatus) string {
	switch {
	case es.State == storage.StateVerified:
		return storageVerifiedWord
	case es.State == storage.StatePending && es.Stale:
		return "pending (replaced on another PC)"
	case es.State == storage.StatePending && es.Damaged:
		return "pending (remote object is damaged)"
	}
	return es.State.String()
}

// storageEntryWord is the state word of one entry, read from its file alone.
func storageEntryWord(st *storage.Storage, e storage.Entry) (string, error) {
	es, err := st.Verify(e.Name)
	if err != nil {
		return "", err
	}
	return storageStateWord(es), nil
}

func cmdStorageAdd(args []string) error {
	fs := flag.NewFlagSet("storage add", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	note := fs.String("m", "", "note describing the stored file")
	name := fs.String("name", "", "custom name under storage/")
	force := fs.Bool("force", false, "force overwrite if entry exists with different sha256")

	positional, err := parseStorageFlags(fs, args)
	if err != nil {
		return &exitError{code: 2}
	}
	if len(positional) == 0 {
		fmt.Fprintln(os.Stderr, "lw storage add: source file path is required")
		storageUsage(os.Stderr)
		return &exitError{code: 2}
	}
	if len(positional) > 1 {
		fmt.Fprintf(os.Stderr, "lw storage add: unexpected argument %q\n", positional[1])
		storageUsage(os.Stderr)
		return &exitError{code: 2}
	}

	srcFile := positional[0]
	root, err := storageVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	// A stored file is a tracked catalog change like a note: refused while a
	// checkout collision is unresolved, before anything is pulled or written
	// (X-15, C-066-9), exactly as lw note does it.
	if err := collisionRefusal(root); err != nil {
		return err
	}
	attachLoggingAt(root)

	st, err := storage.Open(root)
	if err != nil {
		return err
	}

	// 066: a stored file is a catalog change like a note is a file change, so it
	// is pulled-for and pushed the way lw note does it (047, 059): the newest
	// catalog first, so two PCs rarely edit the one storage.json from the same
	// base, and the upload and the push once the verb has said what it did.
	auto := loadAutoSync(root)
	auto.pull()

	entry, err := st.Add(srcFile, *name, *note, *force)
	if err != nil {
		return err
	}

	shaPrefix := entry.SHA256
	if len(shaPrefix) > 8 {
		shaPrefix = shaPrefix[:8]
	}
	fmt.Printf("stored %s (%s, sha256: %s...)\n", entry.Name, formatStorageSize(entry.Size), shaPrefix)
	auto.afterNote()
	return nil
}

func cmdStorageList(args []string) error {
	fs := flag.NewFlagSet("storage list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	verbose := fs.Bool("v", false, "verbose output including sha256 and status")
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON array")

	positional, err := parseStorageFlags(fs, args)
	if err != nil {
		return &exitError{code: 2}
	}
	if len(positional) > 0 {
		fmt.Fprintf(os.Stderr, "lw storage list: unexpected argument %q\n", positional[0])
		storageUsage(os.Stderr)
		return &exitError{code: 2}
	}

	root, err := storageVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	attachLoggingAt(root)

	st, err := storage.Open(root)
	if err != nil {
		return err
	}

	entries, err := st.List()
	if err != nil {
		return err
	}

	if *jsonOut {
		if entries == nil {
			entries = []storage.Entry{}
		}
		data, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal json: %w", err)
		}
		fmt.Println(string(data))
		return nil
	}

	var rep storage.Report
	if *verbose {
		// One pass over the disk: the state of every entry, and the files in
		// storage/ the catalog has never heard of (066 R7).
		if rep, err = st.Status(); err != nil {
			return err
		}
		if len(rep.Entries) == 0 && len(rep.Orphans) == 0 {
			return nil
		}
	} else if len(entries) == 0 {
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	if *verbose {
		fmt.Fprintln(w, "NAME\tSIZE\tSHA256\tSTATUS\tADDED\tNOTE")
		for _, es := range rep.Entries {
			e := es.Entry
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				e.Name,
				formatStorageSize(e.Size),
				e.SHA256,
				storageStateWord(es),
				e.Added.UTC().Format("2006-01-02 15:04:05"),
				e.Note,
			)
		}
		for _, name := range rep.Orphans {
			size := "-"
			if fi, err := os.Stat(st.Path(name)); err == nil {
				size = formatStorageSize(fi.Size())
			}
			fmt.Fprintf(w, "%s\t%s\t-\torphan\t-\t\n", name, size)
		}
	} else {
		fmt.Fprintln(w, "NAME\tSIZE\tADDED\tNOTE")
		for _, e := range entries {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
				e.Name,
				formatStorageSize(e.Size),
				e.Added.UTC().Format("2006-01-02 15:04:05"),
				e.Note,
			)
		}
	}
	return w.Flush()
}

func cmdStorageShow(args []string) error {
	fs := flag.NewFlagSet("storage show", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")

	positional, err := parseStorageFlags(fs, args)
	if err != nil {
		return &exitError{code: 2}
	}
	if len(positional) == 0 {
		fmt.Fprintln(os.Stderr, "lw storage show: entry name is required")
		storageUsage(os.Stderr)
		return &exitError{code: 2}
	}
	if len(positional) > 1 {
		fmt.Fprintf(os.Stderr, "lw storage show: unexpected argument %q\n", positional[1])
		storageUsage(os.Stderr)
		return &exitError{code: 2}
	}

	name := positional[0]
	root, err := storageVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	attachLoggingAt(root)

	st, err := storage.Open(root)
	if err != nil {
		return err
	}

	entry, found, err := st.Show(name)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("storage: entry %q not found", name)
	}

	status, err := storageEntryWord(st, entry)
	if err != nil {
		return err
	}
	relPath := filepath.ToSlash(filepath.Join("storage", filepath.FromSlash(entry.Name)))

	fmt.Printf("Name:    %s\n", entry.Name)
	fmt.Printf("Path:    %s\n", relPath)
	fmt.Printf("Size:    %d (%s)\n", entry.Size, formatStorageSize(entry.Size))
	fmt.Printf("SHA256:  %s\n", entry.SHA256)
	fmt.Printf("Status:  %s\n", status)
	fmt.Printf("Added:   %s\n", entry.Added.UTC().Format(time.RFC3339))
	if entry.Note != "" {
		fmt.Printf("Note:    %s\n", entry.Note)
	}
	return nil
}

func cmdStorageGet(args []string) error {
	fs := flag.NewFlagSet("storage get", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	outPath := fs.String("o", "", "output file path (or - for stdout)")

	positional, err := parseStorageFlags(fs, args)
	if err != nil {
		return &exitError{code: 2}
	}
	if len(positional) == 0 {
		fmt.Fprintln(os.Stderr, "lw storage get: entry name is required")
		storageUsage(os.Stderr)
		return &exitError{code: 2}
	}
	if len(positional) > 1 {
		fmt.Fprintf(os.Stderr, "lw storage get: unexpected argument %q\n", positional[1])
		storageUsage(os.Stderr)
		return &exitError{code: 2}
	}

	name := positional[0]
	root, err := storageVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	attachLoggingAt(root)

	st, err := storage.Open(root)
	if err != nil {
		return err
	}

	// 066 R6: serve only bytes that equal the catalog. A pending entry is
	// fetched now, once, instead of failing with an ENOENT until the user thinks
	// of a lw sync (B6); a corrupt one is refused by name and sha.
	entry, found, err := st.Show(name)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("storage: entry %q not found", name)
	}
	if err := storageEnsureLocal(st, root, entry); err != nil {
		return err
	}

	rc, _, err := st.Get(name)
	if err != nil {
		return err
	}
	defer rc.Close()

	if *outPath == "" || *outPath == "-" {
		_, err = io.Copy(os.Stdout, rc)
		return err
	}

	outDir := filepath.Dir(*outPath)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	f, err := os.Create(*outPath)
	if err != nil {
		return fmt.Errorf("create output file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, rc); err != nil {
		return fmt.Errorf("write output file: %w", err)
	}
	return f.Close()
}

func cmdStorageRemove(args []string) error {
	fs := flag.NewFlagSet("storage rm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")

	positional, err := parseStorageFlags(fs, args)
	if err != nil {
		return &exitError{code: 2}
	}
	if len(positional) == 0 {
		fmt.Fprintln(os.Stderr, "lw storage rm: entry name is required")
		storageUsage(os.Stderr)
		return &exitError{code: 2}
	}
	if len(positional) > 1 {
		fmt.Fprintf(os.Stderr, "lw storage rm: unexpected argument %q\n", positional[1])
		storageUsage(os.Stderr)
		return &exitError{code: 2}
	}

	name := positional[0]
	root, err := storageVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	if err := collisionRefusal(root); err != nil {
		return err
	}
	attachLoggingAt(root)

	st, err := storage.Open(root)
	if err != nil {
		return err
	}

	// The removal travels as a catalog change; the other PCs delete their copy
	// when they pull it (B1). Pulled first and pushed after, as lw storage add.
	auto := loadAutoSync(root)
	auto.pull()

	if err := st.Remove(name); err != nil {
		return err
	}
	fmt.Printf("removed %s\n", name)
	auto.afterNote()
	return nil
}
