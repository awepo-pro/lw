package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/vault"
)

// stringSliceFlag implements flag.Value for repeatable string flags like --tag.
type stringSliceFlag []string

func (s *stringSliceFlag) String() string {
	if s == nil {
		return ""
	}
	return strings.Join(*s, ",")
}

func (s *stringSliceFlag) Set(val string) error {
	*s = append(*s, val)
	return nil
}

// searchHitJSON represents one hit in the --json array.
type searchHitJSON struct {
	Path    string  `json:"path"`
	Title   string  `json:"title"`
	Score   float64 `json:"score"`
	Snippet string  `json:"snippet"`
}

// cmdSearch performs word-search over committed pages only (the open
// changeset is not projected), without using an LLM or an API key.
// It applies BM25 scoring over title, abstract, aliases, body, and tags.
func cmdSearch(args []string) error {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	typeFlag := fs.String("type", "", "filter hits by page type")
	var tags stringSliceFlag
	fs.Var(&tags, "tag", "filter hits by tag (repeatable; AND across tags)")
	nFlag := fs.Int("n", 10, "maximum number of hits to return (must be >= 1)")
	jsonFlag := fs.Bool("json", false, "emit hits as indented JSON array")

	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: lw search [--vault V] [--type T] [--tag t]... [-n N] [--json] <words>")
		return &exitError{code: 2}
	}
	if *nFlag < 1 {
		fmt.Fprintln(os.Stderr, "lw search: -n must be >= 1")
		return &exitError{code: 2}
	}

	q := strings.Join(fs.Args(), " ")

	root, err := findVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	attachLoggingAt(root)

	v, err := vault.Open(root)
	if err != nil {
		return fmt.Errorf("open vault %s: %w", root, err)
	}

	ix := index.Build(v)
	hits := ix.Search(q, index.Options{
		Type:  *typeFlag,
		Tags:  tags,
		Limit: *nFlag,
	})

	if len(hits) == 0 {
		if *jsonFlag {
			fmt.Println("[]")
		} else {
			fmt.Fprintf(os.Stderr, "no pages match %q\n", q)
		}
		return &exitError{code: 1}
	}

	if *jsonFlag {
		out := make([]searchHitJSON, len(hits))
		for i, h := range hits {
			out[i] = searchHitJSON{
				Path:    h.Path,
				Title:   h.Title,
				Score:   h.Score,
				Snippet: h.Snippet,
			}
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal json: %w", err)
		}
		fmt.Println(string(b))
		return nil
	}

	for _, h := range hits {
		fmt.Printf("%s  (%s)  %.2f\n", h.Path, h.Title, h.Score)
		if h.Snippet != "" {
			fmt.Printf("    %s\n", h.Snippet)
		}
	}
	return nil
}
