package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/awepo-pro/lw/internal/htmlexport"
	"github.com/awepo-pro/lw/internal/vault"
)

// cmdExport exports the vault into a static offline HTML site.
//
// Usage: lw export html [--vault V] [--force] [--title T] <outdir>
func cmdExport(args []string) error {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: lw export html [--vault V] [--force] [--title T] <outdir>")
		return &exitError{code: 2}
	}
	if args[0] != "html" {
		fmt.Fprintf(os.Stderr, "lw: export: unknown format %q (supported: html)\n", args[0])
		return &exitError{code: 2}
	}

	fs := flag.NewFlagSet("export html", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	force := fs.Bool("force", false, "overwrite existing non-empty output directory")
	title := fs.String("title", "", "site title (default \"lw wiki\")")

	if err := fs.Parse(args[1:]); err != nil {
		return &exitError{code: 2}
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "lw export html: missing output directory")
		return &exitError{code: 2}
	}
	if fs.NArg() > 1 {
		fmt.Fprintf(os.Stderr, "lw export html: unexpected argument %q\n", fs.Arg(1))
		return &exitError{code: 2}
	}
	outDir := fs.Arg(0)

	root, err := findVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	attachLoggingAt(root)

	v, err := vault.Open(root)
	if err != nil {
		return fmt.Errorf("open vault %s: %w", root, err)
	}

	res, err := htmlexport.Export(v, outDir, htmlexport.Options{
		Force: *force,
		Title: *title,
	})
	if err != nil {
		return err
	}

	fmt.Printf("exported %d pages, %d raw sources, %d assets to %s\n", res.Pages, res.Raws, res.Assets, res.Dir)
	fmt.Printf("open %s/index.html\n", res.Dir)
	return nil
}
