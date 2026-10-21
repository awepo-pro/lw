package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/awepo-pro/lw/internal/storage"
)

// cmdStorage dispatches lw storage subcommands (062 S1).
func cmdStorage(args []string) error {
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

func entryDiskStatus(root string, e storage.Entry) string {
	path := filepath.Join(root, "storage", filepath.FromSlash(e.Name))
	fi, err := os.Stat(path)
	if err != nil {
		return "missing"
	}
	if !fi.Mode().IsRegular() || fi.Size() != e.Size {
		return "corrupt"
	}
	f, err := os.Open(path)
	if err != nil {
		return "corrupt"
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "corrupt"
	}
	if hex.EncodeToString(h.Sum(nil)) != e.SHA256 {
		return "corrupt"
	}
	return "present"
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
	root, err := findVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	attachLoggingAt(root)

	st, err := storage.Open(root)
	if err != nil {
		return err
	}

	entry, err := st.Add(srcFile, *name, *note, *force)
	if err != nil {
		return err
	}

	shaPrefix := entry.SHA256
	if len(shaPrefix) > 8 {
		shaPrefix = shaPrefix[:8]
	}
	fmt.Printf("stored %s (%s, sha256: %s...)\n", entry.Name, formatStorageSize(entry.Size), shaPrefix)
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

	root, err := findVaultRoot(*vaultPath)
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

	if len(entries) == 0 {
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	if *verbose {
		fmt.Fprintln(w, "NAME\tSIZE\tSHA256\tSTATUS\tADDED\tNOTE")
		for _, e := range entries {
			status := entryDiskStatus(root, e)
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				e.Name,
				formatStorageSize(e.Size),
				e.SHA256,
				status,
				e.Added.UTC().Format("2006-01-02 15:04:05"),
				e.Note,
			)
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
	root, err := findVaultRoot(*vaultPath)
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

	status := entryDiskStatus(root, entry)
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
	root, err := findVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	attachLoggingAt(root)

	st, err := storage.Open(root)
	if err != nil {
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
	root, err := findVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	attachLoggingAt(root)

	st, err := storage.Open(root)
	if err != nil {
		return err
	}

	if err := st.Remove(name); err != nil {
		return err
	}
	fmt.Printf("removed %s\n", name)
	return nil
}
