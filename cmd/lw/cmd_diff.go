package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/awepo-pro/lw/internal/stage"
)

// cmdDiff prints the projected unified diff of the open changeset, or a
// per-file summary with --stat, optionally scoped to a single op with
// --op.
func cmdDiff(args []string) error {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	opID := fs.String("op", "", "show only the file(s) touched by this op id")
	stat := fs.Bool("stat", false, "show a per-file summary instead of the full unified diff")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "lw diff: unexpected argument %q\n", fs.Arg(0))
		return &exitError{code: 2}
	}

	root, err := findVaultRoot(*vaultPath)
	if err != nil {
		return err
	}

	e, err := stage.OpenEngine(root)
	if err != nil {
		return fmt.Errorf("open engine: %w", err)
	}
	defer e.Close()

	d, err := e.Diff()
	if err != nil {
		return err
	}

	if *opID != "" {
		d = filterDiffByOp(d, *opID)
	}

	if *stat {
		printDiffStat(os.Stdout, d)
		return nil
	}

	fmt.Print(d.Unified())
	return nil
}

// filterDiffByOp returns a copy of d containing only the Files entries
// whose OpID equals opID, with Added/Removed re-summed over that subset.
// Diff.Files is an exported field and Unified()/printDiffStat read only
// d.Files/d.Added/d.Removed, so building a narrowed Diff value is enough
// to reuse both without internal/stage exposing an op-scoped query itself.
func filterDiffByOp(d stage.Diff, opID string) stage.Diff {
	out := stage.Diff{Changeset: d.Changeset}
	for _, fd := range d.Files {
		if fd.OpID != opID {
			continue
		}
		out.Files = append(out.Files, fd)
		out.Added += fd.Added
		out.Removed += fd.Removed
	}
	return out
}

// printDiffStat writes one "<path> | +<added> -<removed>" line per Files
// entry, in Diff.Files order (already risk-sorted, backbone §5.6), then a
// one-line summary. A changeset with no file-shaped changes (e.g. only an
// add_link marker) prints "no changes".
func printDiffStat(w io.Writer, d stage.Diff) {
	if len(d.Files) == 0 {
		fmt.Fprintln(w, "no changes")
		return
	}
	for _, fd := range d.Files {
		fmt.Fprintf(w, "%s | +%d -%d\n", fd.Path, fd.Added, fd.Removed)
	}
	fmt.Fprintf(w, "%d file(s) changed, %d insertion(s)(+), %d deletion(s)(-)\n", len(d.Files), d.Added, d.Removed)
}
