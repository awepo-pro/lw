package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/awepo-pro/lw/internal/cards"
	"github.com/awepo-pro/lw/internal/safewrite"
	"github.com/awepo-pro/lw/internal/vault"
)

// stringSlice implements flag.Value for repeatable string flags.
type stringSlice []string

func (s *stringSlice) String() string {
	return strings.Join(*s, ",")
}

func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// cmdCards exports spaced-repetition flashcards from wiki page sections.
//
// Contract: extracts cards deterministically in anki, md, or json format.
// Re-exporting preserves stable card IDs. Writing into the vault root is refused.
func cmdCards(args []string) error {
	fs := flag.NewFlagSet("cards", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	format := fs.String("format", "anki", "export format: anki|md|json")
	deck := fs.String("deck", "", "Anki deck name (default: lw::<vault dir basename>)")
	typeFlag := fs.String("type", "", "filter pages by page type")
	var tagFlags stringSlice
	fs.Var(&tagFlags, "tag", "filter pages by tag (repeatable, AND filter)")
	outFile := fs.String("o", "", "output file path (default: stdout)")

	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return &exitError{code: 2}
	}
	if len(positionals) > 0 {
		fmt.Fprintf(os.Stderr, "lw cards: unexpected argument %q\n", positionals[0])
		return &exitError{code: 2}
	}

	switch *format {
	case "anki", "md", "json":
	default:
		fmt.Fprintf(os.Stderr, "lw cards: unknown format %q (want anki|md|json)\n", *format)
		return &exitError{code: 2}
	}

	root, err := findVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	attachLoggingAt(root)

	if *outFile != "" {
		if err := verifyOutputOutsideVault(root, *outFile); err != nil {
			fmt.Fprintf(os.Stderr, "lw cards: %v\n", err)
			return &exitError{code: 1}
		}
	}

	v, err := vault.Open(root)
	if err != nil {
		return fmt.Errorf("open vault %s: %w", root, err)
	}

	opts := cards.Options{
		Type: *typeFlag,
		Tags: tagFlags,
	}

	cardList := cards.Extract(v, opts)
	if len(cardList) == 0 {
		fmt.Fprintln(os.Stderr, "no cards (check --type/--tag)")
		return &exitError{code: 1}
	}

	deckName := *deck
	if deckName == "" {
		deckName = "lw::" + filepath.Base(root)
	}

	var w io.Writer = os.Stdout
	var buf *bytes.Buffer
	if *outFile != "" {
		buf = new(bytes.Buffer)
		w = buf
	}

	switch *format {
	case "anki":
		err = cards.WriteAnkiTSV(w, cardList, deckName)
	case "md":
		err = cards.WriteMarkdown(w, cardList)
	case "json":
		err = cards.WriteJSON(w, cardList)
	}
	if err != nil {
		return fmt.Errorf("write cards: %w", err)
	}

	if *outFile != "" {
		guard, err := safewrite.NewGuard(root)
		if err != nil {
			return fmt.Errorf("safewrite guard: %w", err)
		}
		if err := guard.WriteFile(*outFile, buf.Bytes()); err != nil {
			fmt.Fprintf(os.Stderr, "lw cards: %v\n", err)
			return &exitError{code: 1}
		}
		pagesSet := make(map[string]bool)
		for _, c := range cardList {
			pagesSet[c.Path] = true
		}
		fmt.Fprintf(os.Stderr, "wrote %d cards from %d pages to %s\n", len(cardList), len(pagesSet), *outFile)
		if *format == "anki" {
			fmt.Fprintln(os.Stderr, "import in Anki: File > Import, keep \"Update existing notes\"")
		}
	}

	return nil
}

// verifyOutputOutsideVault returns an error if outFile resolves inside root.
func verifyOutputOutsideVault(root, outFile string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve vault root: %w", err)
	}
	absOut, err := filepath.Abs(outFile)
	if err != nil {
		return fmt.Errorf("resolve output path: %w", err)
	}

	evalRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		evalRoot = absRoot
	}

	evalOutDir, err := filepath.EvalSymlinks(filepath.Dir(absOut))
	if err == nil {
		absOut = filepath.Join(evalOutDir, filepath.Base(absOut))
	}

	rel, err := filepath.Rel(evalRoot, absOut)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("refusing to write inside vault root: %s", outFile)
	}
	return nil
}
