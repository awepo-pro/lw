package main

// cmd_ledger.go implements `lw ledger` (074 S2, S4).
//
// Bookkeeping notes and receipts live in <vault>/ledger/ (074). Receipts
// delivered by Syncthing to ledger/receipts/ are immutable local files
// until lw ledger import copies them into storage/ as receipts/<path>, where
// lw sync carries them to other machines. The Syncthing copy is never deleted
// or modified (074 I7).
//
// S2 implements `import`. S4 adds `check` and `sum`.

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/awepo-pro/lw/internal/ledger"
	"github.com/awepo-pro/lw/internal/storage"
	"github.com/awepo-pro/lw/internal/vault"
)

var monthRegex = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)

// cmdLedger dispatches lw ledger subcommands (074 S2, S4).
func cmdLedger(args []string) error {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "lw ledger: a subcommand is required")
		ledgerUsage(os.Stderr)
		return &exitError{code: 2}
	}
	switch sub, rest := args[0], args[1:]; sub {
	case "check":
		return cmdLedgerCheck(rest)
	case "sum":
		return cmdLedgerSum(rest)
	case "import":
		return cmdLedgerImport(rest)
	case "-h", "--help", "help":
		ledgerUsage(os.Stdout)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "lw ledger: unknown subcommand %q\n", sub)
		ledgerUsage(os.Stderr)
		return &exitError{code: 2}
	}
}

func ledgerUsage(w io.Writer) {
	fmt.Fprintf(w, `usage: lw ledger import [--dry-run] [--vault P]
       lw ledger check [--vault P]
       lw ledger sum [YYYY-MM] [--vault P]

Receipts delivered to %s/receipts/ are copied into storage/ as
receipts/<path>, where lw sync carries them to other PCs.
`, vault.LedgerDir)
}

func cmdLedgerCheck(args []string) error {
	fl := flag.NewFlagSet("ledger check", flag.ContinueOnError)
	fl.SetOutput(os.Stderr)
	vaultPath := fl.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")

	positional, err := parseStorageFlags(fl, args)
	if err != nil {
		return &exitError{code: 2}
	}
	if len(positional) > 0 {
		fmt.Fprintf(os.Stderr, "lw ledger check: unexpected argument %q\n", positional[0])
		ledgerUsage(os.Stderr)
		return &exitError{code: 2}
	}

	root, err := storageVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	attachLoggingAt(root)

	txns, issues, files, err := ledger.Load(root)
	if err != nil {
		return err
	}

	for _, issue := range issues {
		fmt.Printf("%s:%d: %s\n", issue.Path, issue.Line, issue.Msg)
	}

	if len(issues) == 0 {
		fmt.Printf("ok: %d transaction(s) in %d file(s)\n", len(txns), files)
		return nil
	}

	fmt.Printf("%d issue(s); %d valid transaction(s) in %d file(s)\n", len(issues), len(txns), files)
	return &exitError{code: 1}
}

func cmdLedgerSum(args []string) error {
	fl := flag.NewFlagSet("ledger sum", flag.ContinueOnError)
	fl.SetOutput(os.Stderr)
	vaultPath := fl.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")

	positional, err := parseStorageFlags(fl, args)
	if err != nil {
		return &exitError{code: 2}
	}

	var month string
	if len(positional) > 1 {
		fmt.Fprintf(os.Stderr, "lw ledger sum: unexpected argument %q\n", positional[1])
		ledgerUsage(os.Stderr)
		return &exitError{code: 2}
	}
	if len(positional) == 1 {
		if !monthRegex.MatchString(positional[0]) {
			fmt.Fprintf(os.Stderr, "lw ledger sum: invalid month %q (want YYYY-MM)\n", positional[0])
			ledgerUsage(os.Stderr)
			return &exitError{code: 2}
		}
		month = positional[0]
	}

	root, err := storageVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	attachLoggingAt(root)

	txns, issues, _, err := ledger.Load(root)
	if err != nil {
		return err
	}

	if len(issues) > 0 {
		fmt.Fprintf(os.Stderr, "lw: ledger: %d issue(s) found; totals may be wrong - run lw ledger check\n", len(issues))
	}

	totals := ledger.Sum(txns, month)
	if len(totals) == 0 {
		fmt.Println("no transactions")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	type curGroup struct {
		currency string
		totals   []ledger.Total
		cents    int64
		count    int
	}

	var groups []curGroup
	var curG *curGroup
	for _, tot := range totals {
		if curG == nil || curG.currency != tot.Currency {
			groups = append(groups, curGroup{currency: tot.Currency})
			curG = &groups[len(groups)-1]
		}
		curG.totals = append(curG.totals, tot)
		curG.cents += tot.Cents
		curG.count += tot.Count
	}

	for _, g := range groups {
		for _, tot := range g.totals {
			fmt.Fprintf(tw, "%s\t%s\t%s\t(%d)\n", tot.Currency, tot.Category, ledger.FormatCents(tot.Cents), tot.Count)
		}
		fmt.Fprintf(tw, "%s\ttotal\t%s\t(%d)\n", g.currency, ledger.FormatCents(g.cents), g.count)
	}

	return tw.Flush()
}

type receiptFile struct {
	srcPath string
	name    string
}

func cmdLedgerImport(args []string) error {
	fl := flag.NewFlagSet("ledger import", flag.ContinueOnError)
	fl.SetOutput(os.Stderr)
	vaultPath := fl.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	dryRun := fl.Bool("dry-run", false, "report what would be imported without writing to storage")

	positional, err := parseStorageFlags(fl, args)
	if err != nil {
		return &exitError{code: 2}
	}
	if len(positional) > 0 {
		fmt.Fprintf(os.Stderr, "lw ledger import: unexpected argument %q\n", positional[0])
		ledgerUsage(os.Stderr)
		return &exitError{code: 2}
	}

	root, err := storageVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	attachLoggingAt(root)

	return importReceipts(os.Stdout, root, *dryRun)
}

// importReceipts imports receipts under ledger/receipts/ into lw storage.
// When w is non-nil, it prints progress and summary lines matching `lw ledger import`.
func importReceipts(w io.Writer, root string, dryRun bool) error {
	receiptsDir := filepath.Join(root, vault.LedgerDir, "receipts")
	if _, err := os.Stat(receiptsDir); os.IsNotExist(err) {
		if w != nil {
			fmt.Fprintln(w, "0 stored, 0 unchanged, 0 conflict(s), 0 error(s)")
		}
		return nil
	}

	var items []receiptFile
	err := filepath.WalkDir(receiptsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == receiptsDir {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "~syncthing~") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		rel, err := filepath.Rel(receiptsDir, path)
		if err != nil {
			return err
		}
		storageName := "receipts/" + filepath.ToSlash(rel)
		items = append(items, receiptFile{
			srcPath: path,
			name:    storageName,
		})
		return nil
	})
	if err != nil {
		return err
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].name < items[j].name
	})

	st, err := storage.Open(root)
	if err != nil {
		return err
	}

	var stored, unchanged, conflicts, errorsCount int
	for _, item := range items {
		sum, err := fileSHA256(item.srcPath)
		if err != nil {
			if w != nil {
				fmt.Fprintf(w, "error %s: %v\n", item.name, err)
			}
			errorsCount++
			continue
		}

		existing, found, err := st.Show(item.name)
		if err != nil {
			if w != nil {
				fmt.Fprintf(w, "error %s: %v\n", item.name, err)
			}
			errorsCount++
			continue
		}

		if found {
			if existing.SHA256 == sum {
				if w != nil {
					fmt.Fprintf(w, "unchanged %s\n", item.name)
				}
				unchanged++
			} else {
				if w != nil {
					fmt.Fprintf(w, "conflict %s: storage holds different bytes (lw storage show %s)\n", item.name, item.name)
				}
				conflicts++
			}
			continue
		}

		if dryRun {
			if w != nil {
				fmt.Fprintf(w, "would store %s\n", item.name)
			}
			stored++
		} else {
			_, err := st.Add(item.srcPath, item.name, "ledger receipt", false)
			if err != nil {
				if w != nil {
					fmt.Fprintf(w, "error %s: %v\n", item.name, err)
				}
				errorsCount++
				continue
			}
			if w != nil {
				fmt.Fprintf(w, "stored %s\n", item.name)
			}
			stored++
		}
	}

	if w != nil {
		fmt.Fprintf(w, "%d stored, %d unchanged, %d conflict(s), %d error(s)\n", stored, unchanged, conflicts, errorsCount)
		if stored > 0 && !dryRun {
			fmt.Fprintln(w, "run lw sync to carry them to your other PCs")
		}
	}

	if conflicts > 0 || errorsCount > 0 {
		if w != nil {
			return &exitError{code: 1}
		}
		if errorsCount > 0 {
			return fmt.Errorf("import receipts had %d error(s)", errorsCount)
		}
		return fmt.Errorf("import receipts had %d conflict(s)", conflicts)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
