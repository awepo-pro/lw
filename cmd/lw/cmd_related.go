package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/awepo-pro/lw/internal/similar"
	"github.com/awepo-pro/lw/internal/vault"
)

// cmdRelated finds and displays the pages most related to <page>
// based on deterministic link, source, tag, title, and body similarity.
func cmdRelated(args []string) error {
	fs := flag.NewFlagSet("related", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	nFlag := fs.Int("n", 10, "maximum number of results")
	jsonFlag := fs.Bool("json", false, "emit results as JSON")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	if *nFlag < 1 {
		fmt.Fprintf(os.Stderr, "lw related: -n must be >= 1\n")
		return &exitError{code: 2}
	}
	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "lw related: missing page argument\n")
		return &exitError{code: 2}
	}
	if fs.NArg() > 1 {
		fmt.Fprintf(os.Stderr, "lw related: unexpected argument %q\n", fs.Arg(1))
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

	target := fs.Arg(0)
	targetPath, ok := vault.Resolve(v, target)
	var targetPage *vault.Page
	if ok {
		targetPage, ok = v.Page(targetPath)
	}
	if !ok {
		for _, p := range v.Pages() {
			if strings.EqualFold(p.FM.Title, target) {
				targetPage = p
				targetPath = p.Path
				ok = true
				break
			}
		}
	}

	if !ok || targetPage == nil {
		fmt.Fprintf(os.Stderr, "lw related: no page %q\n", target)
		return &exitError{code: 1}
	}

	corp := similar.NewCorpus(v)
	matches := corp.Related(targetPath, *nFlag)

	if *jsonFlag {
		b, err := json.MarshalIndent(matches, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal json: %w", err)
		}
		fmt.Println(string(b))
		return nil
	}

	if len(matches) == 0 {
		fmt.Println("no related pages")
		return nil
	}

	fmt.Printf("related to %s (%s)\n", targetPath, targetPage.FM.Title)
	for _, m := range matches {
		fmt.Printf("%.3f  %s  (%s)\n", m.Score, m.Path, m.Title)
		var reasons []string
		if len(m.Shared.Links) > 0 {
			reasons = append(reasons, fmt.Sprintf("links %d shared (%s)", len(m.Shared.Links), strings.Join(m.Shared.Links, ", ")))
		}
		if len(m.Shared.Sources) > 0 {
			reasons = append(reasons, fmt.Sprintf("sources %d", len(m.Shared.Sources)))
		}
		if len(m.Shared.Tags) > 0 {
			reasons = append(reasons, fmt.Sprintf("tags: %s", strings.Join(m.Shared.Tags, ", ")))
		}
		if m.Signals.Title > 0 {
			reasons = append(reasons, fmt.Sprintf("title %.2f", m.Signals.Title))
		}
		if m.Signals.Body > 0 {
			reasons = append(reasons, fmt.Sprintf("body %.2f", m.Signals.Body))
		}
		if m.Signals.Direct {
			reasons = append(reasons, "linked")
		}
		if len(reasons) > 0 {
			fmt.Printf("    %s\n", strings.Join(reasons, " - "))
		}
	}
	return nil
}

// cmdDupes identifies near-duplicate page pairs across the vault based on
// title, body, and source similarity.
func cmdDupes(args []string) error {
	fs := flag.NewFlagSet("dupes", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	thresholdFlag := fs.Float64("threshold", 0.45, "similarity threshold in (0, 1]")
	nFlag := fs.Int("n", 20, "maximum number of results")
	jsonFlag := fs.Bool("json", false, "emit results as JSON")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	if *thresholdFlag <= 0 || *thresholdFlag > 1 {
		fmt.Fprintf(os.Stderr, "lw dupes: --threshold must be in (0, 1]\n")
		return &exitError{code: 2}
	}
	if *nFlag < 1 {
		fmt.Fprintf(os.Stderr, "lw dupes: -n must be >= 1\n")
		return &exitError{code: 2}
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "lw dupes: unexpected argument %q\n", fs.Arg(0))
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

	corp := similar.NewCorpus(v)
	pairs := corp.Dupes(*thresholdFlag, *nFlag)

	if *jsonFlag {
		b, err := json.MarshalIndent(pairs, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal json: %w", err)
		}
		fmt.Println(string(b))
		return nil
	}

	if len(pairs) == 0 {
		fmt.Printf("no duplicate candidates at threshold %g\n", *thresholdFlag)
		return nil
	}

	for _, p := range pairs {
		fmt.Printf("%.3f  %s  <->  %s\n", p.Score, p.A, p.B)
		linkedStr := ""
		if p.Linked {
			linkedStr = " [already linked]"
		}
		fmt.Printf("    %q / %q   title %.2f body %.2f sources %.2f%s\n",
			p.ATitle, p.BTitle, p.Signals.Title, p.Signals.Body, p.Signals.Sources, linkedStr)
	}
	return nil
}
