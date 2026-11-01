package main

// cmd_ledger.go implements `lw ledger` (074 S2).
//
// Bookkeeping notes and receipts live in <vault>/ledger/ (074). Receipts
// delivered by Syncthing to ledger/receipts/ are immutable local files
// until lw ledger import copies them into storage/ as receipts/<path>, where
// lw sync carries them to other machines. The Syncthing copy is never deleted
// or modified (074 I7).
//
// S2 implements `import` only. S4 adds `check` and `sum`.

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/awepo-pro/lw/internal/storage"
)

// cmdLedger dispatches lw ledger subcommands (074 S2).
func cmdLedger(args []string) error {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "lw ledger: a subcommand is required")
		ledgerUsage(os.Stderr)
		return &exitError{code: 2}
	}
	switch sub, rest := args[0], args[1:]; sub {
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
	fmt.Fprint(w, `usage: lw ledger import [--dry-run] [--vault P]

Receipts delivered to ledger/receipts/ are copied into storage/ as
receipts/<path>, where lw sync carries them to other PCs.
`)
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

	receiptsDir := filepath.Join(root, "ledger", "receipts")
	if _, err := os.Stat(receiptsDir); os.IsNotExist(err) {
		fmt.Println("0 stored, 0 unchanged, 0 conflict(s), 0 error(s)")
		return nil
	}

	var items []receiptFile
	err = filepath.WalkDir(receiptsDir, func(path string, d fs.DirEntry, err error) error {
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
			fmt.Printf("error %s: %v\n", item.name, err)
			errorsCount++
			continue
		}

		existing, found, err := st.Show(item.name)
		if err != nil {
			fmt.Printf("error %s: %v\n", item.name, err)
			errorsCount++
			continue
		}

		if found {
			if existing.SHA256 == sum {
				fmt.Printf("unchanged %s\n", item.name)
				unchanged++
			} else {
				fmt.Printf("conflict %s: storage holds different bytes (lw storage show %s)\n", item.name, item.name)
				conflicts++
			}
			continue
		}

		if *dryRun {
			fmt.Printf("would store %s\n", item.name)
			stored++
		} else {
			_, err := st.Add(item.srcPath, item.name, "ledger receipt", false)
			if err != nil {
				fmt.Printf("error %s: %v\n", item.name, err)
				errorsCount++
				continue
			}
			fmt.Printf("stored %s\n", item.name)
			stored++
		}
	}

	fmt.Printf("%d stored, %d unchanged, %d conflict(s), %d error(s)\n", stored, unchanged, conflicts, errorsCount)
	if stored > 0 && !*dryRun {
		fmt.Println("run lw sync to carry them to your other PCs")
	}

	if conflicts > 0 || errorsCount > 0 {
		return &exitError{code: 1}
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
