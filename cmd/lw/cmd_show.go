package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/vault"
)

// showBacklinkJSON is one backlink entry in showPageJSON.
type showBacklinkJSON struct {
	From    string `json:"from"`
	Line    int    `json:"line"`
	Context string `json:"context"`
}

// showPageJSON represents the page structure emitted by `lw show --json`.
type showPageJSON struct {
	Path      string             `json:"path"`
	Title     string             `json:"title"`
	Type      string             `json:"type"`
	Tags      []string           `json:"tags"`
	Sources   []string           `json:"sources"`
	Outbound  []string           `json:"outbound"`
	Broken    []string           `json:"broken"`
	Backlinks []showBacklinkJSON `json:"backlinks"`
	Body      string             `json:"body"`
}

// cmdShow prints a wiki page by path, basename, title or alias without
// using an LLM or an API key. With --links, it also prints outbound and
// backlink sections; with --json, it emits structured metadata.
func cmdShow(args []string) error {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	linksFlag := fs.Bool("links", false, "print outbound links and backlinks after page content")
	jsonFlag := fs.Bool("json", false, "emit structured page metadata as indented JSON")

	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return &exitError{code: 2}
	}
	if len(positionals) != 1 || positionals[0] == "" {
		fmt.Fprintln(os.Stderr, "usage: lw show [--vault V] [--links] [--json] <page>")
		return &exitError{code: 2}
	}

	arg := positionals[0]

	root, err := findVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	attachLoggingAt(root)

	v, err := vault.Open(root)
	if err != nil {
		return fmt.Errorf("open vault %s: %w", root, err)
	}

	matched, ambiguous := resolvePageArg(v, arg)

	if len(ambiguous) > 1 {
		sort.Slice(ambiguous, func(i, j int) bool {
			return ambiguous[i].Path < ambiguous[j].Path
		})
		fmt.Fprintf(os.Stderr, "lw show: %q is ambiguous\n", arg)
		limit := len(ambiguous)
		if limit > 5 {
			limit = 5
		}
		for i := 0; i < limit; i++ {
			fmt.Fprintf(os.Stderr, "  %s  (%s)\n", ambiguous[i].Path, ambiguous[i].FM.Title)
		}
		return &exitError{code: 1}
	}

	if matched == nil {
		fmt.Fprintf(os.Stderr, "lw show: no page %q\n", arg)
		ix := index.Build(v)
		hits := ix.Search(arg, index.Options{Limit: 5})
		for _, h := range hits {
			fmt.Fprintf(os.Stderr, "  %s  (%s)\n", h.Path, h.Title)
		}
		return &exitError{code: 1}
	}

	g := v.Graph()
	outboundRefs := g.Outbound(matched.Path)
	blRefs := g.Backlinks(matched.Path)

	if *jsonFlag {
		outboundSet := make(map[string]bool)
		brokenSet := make(map[string]bool)
		var outbound []string
		var broken []string

		for _, ref := range outboundRefs {
			if ref.To != "" {
				if !outboundSet[ref.To] {
					outboundSet[ref.To] = true
					outbound = append(outbound, ref.To)
				}
			} else {
				if !brokenSet[ref.Raw] {
					brokenSet[ref.Raw] = true
					broken = append(broken, ref.Raw)
				}
			}
		}
		if outbound == nil {
			outbound = []string{}
		} else {
			sort.Strings(outbound)
		}
		if broken == nil {
			broken = []string{}
		} else {
			sort.Strings(broken)
		}

		backlinks := make([]showBacklinkJSON, 0, len(blRefs))
		for _, bl := range blRefs {
			backlinks = append(backlinks, showBacklinkJSON{
				From:    bl.From,
				Line:    bl.Line,
				Context: bl.Context,
			})
		}

		tags := matched.FM.Tags
		if tags == nil {
			tags = []string{}
		}
		sources := matched.FM.Sources
		if sources == nil {
			sources = []string{}
		}

		out := showPageJSON{
			Path:      matched.Path,
			Title:     matched.FM.Title,
			Type:      string(matched.FM.Type),
			Tags:      tags,
			Sources:   sources,
			Outbound:  outbound,
			Broken:    broken,
			Backlinks: backlinks,
			Body:      matched.Body,
		}

		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal json: %w", err)
		}
		fmt.Println(string(b))
		return nil
	}

	b, err := v.Read(matched.Path)
	if err != nil {
		return fmt.Errorf("read %s: %w", matched.Path, err)
	}
	os.Stdout.Write(b)

	if *linksFlag {
		if bytes.HasSuffix(b, []byte("\n")) {
			fmt.Fprintln(os.Stdout)
		} else {
			fmt.Fprintln(os.Stdout)
			fmt.Fprintln(os.Stdout)
		}

		seenOut := make(map[string]bool)
		var outbound []string
		for _, ref := range outboundRefs {
			var entry string
			if ref.To != "" {
				entry = ref.To
			} else {
				entry = "unresolved: " + ref.Raw
			}
			if !seenOut[entry] {
				seenOut[entry] = true
				outbound = append(outbound, entry)
			}
		}
		sort.Strings(outbound)

		fmt.Fprintf(os.Stdout, "--- links: %s\n", matched.Path)
		fmt.Fprintf(os.Stdout, "outbound (%d):\n", len(outbound))
		for _, ob := range outbound {
			fmt.Fprintf(os.Stdout, "  %s\n", ob)
		}

		fmt.Fprintf(os.Stdout, "backlinks (%d):\n", len(blRefs))
		for _, bl := range blRefs {
			fmt.Fprintf(os.Stdout, "  %s:%d  %s\n", bl.From, bl.Line, bl.Context)
		}
	}

	return nil
}
