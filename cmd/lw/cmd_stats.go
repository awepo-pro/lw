package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/awepo-pro/lw/internal/vault"
	"github.com/awepo-pro/lw/internal/vaultstats"
)

// cmdStats reports the overall health, shape, and coverage of a vault.
//
// Usage: lw stats [--vault V] [--json] [--stale-days N]
// Exit 0 on success, 1 on vault errors, 2 on usage errors.
func cmdStats(args []string) error {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	jsonOut := fs.Bool("json", false, "emit the report as indented JSON")
	staleDays := fs.Int("stale-days", 90, "threshold in days for considering a page stale")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return &exitError{code: 2}
	}
	if len(positionals) > 0 {
		fmt.Fprintf(os.Stderr, "lw stats: unexpected argument %q\n", positionals[0])
		return &exitError{code: 2}
	}
	if *staleDays < 1 {
		fmt.Fprintln(os.Stderr, "lw stats: --stale-days must be >= 1")
		return &exitError{code: 2}
	}

	root, err := findVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	attachLoggingAt(root)

	v, err := vault.Open(root)
	if err != nil {
		return fmt.Errorf("open vault %s: %w", root, err)
	}

	journalPath := filepath.Join(root, ".llmwiki", "journal.ndjson")
	s := vaultstats.Compute(v, journalPath, time.Now(), *staleDays)

	if *jsonOut {
		b, err := json.MarshalIndent(s, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal stats: %w", err)
		}
		fmt.Println(string(b))
		return nil
	}

	if err := vaultstats.WriteText(os.Stdout, s); err != nil {
		return err
	}

	// Notes line in text mode (cmd_note.go noteDirName is "notes")
	ents, err := os.ReadDir(filepath.Join(root, "notes"))
	if err == nil {
		var stems []string
		for _, e := range ents {
			if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".md") {
				stems = append(stems, strings.TrimSuffix(e.Name(), ".md"))
			}
		}
		tags := noteDoneTags(root, stems)
		pending := 0
		for _, stem := range stems {
			if tags[stem] == "" {
				pending++
			}
		}
		fmt.Printf("\nnotes: %d total, %d pending\n", len(stems), pending)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read notes: %w", err)
	}

	return nil
}
