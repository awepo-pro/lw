package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/awepo-pro/lw/internal/graphexport"
	"github.com/awepo-pro/lw/internal/vault"
)

// cmdGraph summarizes or exports the vault wikilink graph to standard formats
// (text, dot, json, mermaid).
//
// Example:
//
//	lw graph --format dot | dot -Tsvg > wiki.svg
//
// Flags:
//
//	--vault <path>
//	    vault root directory
//	--format <text|dot|json|mermaid>
//	    export format (default text)
//	--focus <page>
//	    focus page path or title for ego-graph export
//	--depth <1..5>
//	    hop radius when --focus is set (default 1)
//	--type <type>
//	    filter nodes to this page type, plus the focus node
//	--top <N>
//	    limit text summary sections to at most N items (default 10)
func cmdGraph(args []string) error {
	fs := flag.NewFlagSet("graph", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	format := fs.String("format", "text", "output format: text, dot, json, mermaid")
	focus := fs.String("focus", "", "focus page for ego-graph")
	depth := fs.Int("depth", 1, "search depth for ego-graph, 1..5")
	pageType := fs.String("type", "", "filter nodes by type, keeping focus node")
	top := fs.Int("top", 10, "maximum items to display in text summary sections")

	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return &exitError{code: 2}
	}
	if len(positionals) > 0 {
		fmt.Fprintf(os.Stderr, "lw graph: unexpected argument %q\n", positionals[0])
		return &exitError{code: 2}
	}

	switch *format {
	case "text", "dot", "json", "mermaid":
	default:
		fmt.Fprintf(os.Stderr, "lw graph: unknown format %q (must be text, dot, json, or mermaid)\n", *format)
		return &exitError{code: 2}
	}

	if *depth < 1 || *depth > 5 {
		fmt.Fprintf(os.Stderr, "lw graph: depth must be between 1 and 5 (got %d)\n", *depth)
		return &exitError{code: 2}
	}

	if *top < 0 {
		fmt.Fprintf(os.Stderr, "lw graph: top must be non-negative (got %d)\n", *top)
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

	resolvedFocus := ""
	if *focus != "" {
		page, ambiguous := resolvePageArg(v, *focus)
		if len(ambiguous) > 1 {
			fmt.Fprintf(os.Stderr, "lw graph: %q is ambiguous\n", *focus)
			limit := len(ambiguous)
			if limit > 5 {
				limit = 5
			}
			for i := 0; i < limit; i++ {
				fmt.Fprintf(os.Stderr, "  %s  (%s)\n", ambiguous[i].Path, ambiguous[i].FM.Title)
			}
			return &exitError{code: 1}
		}
		if page == nil {
			fmt.Fprintf(os.Stderr, "lw graph: no page %q\n", *focus)
			return &exitError{code: 1}
		}
		resolvedFocus = page.Path
	}

	opts := graphexport.Options{
		Focus: resolvedFocus,
		Depth: *depth,
		Type:  *pageType,
	}
	g := graphexport.Build(v, opts)

	var writeErr error
	switch *format {
	case "text":
		writeErr = graphexport.WriteText(os.Stdout, g, *top)
	case "dot":
		writeErr = graphexport.WriteDOT(os.Stdout, g)
	case "json":
		writeErr = graphexport.WriteJSON(os.Stdout, g)
	case "mermaid":
		writeErr = graphexport.WriteMermaid(os.Stdout, g)
	}

	if writeErr != nil {
		return fmt.Errorf("write graph: %w", writeErr)
	}
	return nil
}
