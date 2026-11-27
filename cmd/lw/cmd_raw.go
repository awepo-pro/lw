package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/awepo-pro/lw/internal/stage"
)

// cmdRaw dispatches `lw raw <subcommand>`. Its one subcommand, mv, stages a
// move_raw op: a committed raw source moves to another raw/ folder, its
// original moves with it, and every wiki page citing it is rewritten — all
// in one changeset the user reviews and commits like any other.
func cmdRaw(args []string) error {
	if len(args) == 0 {
		rawUsage(os.Stderr)
		return &exitError{code: 2}
	}
	switch args[0] {
	case "mv", "move":
		return cmdRawMove(args[1:])
	case "-h", "--help", "help":
		rawUsage(os.Stdout)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "lw raw: unknown subcommand %q\n", args[0])
		rawUsage(os.Stderr)
		return &exitError{code: 2}
	}
}

func rawUsage(w io.Writer) {
	fmt.Fprint(w, `usage: lw raw mv <raw/from.md> <raw/dir/ | raw/dir/name.md> [-m "<why>"] [--vault P]

Stages a move of a committed raw source to another raw/ folder (articles, notes,
papers, transcripts). The text moves unchanged; its original (x.md.orig, x.pdf)
moves beside it; every wiki page that cites it (^[raw/...], links, sources:) is
rewritten. Nothing changes until you review and commit the changeset
(lw diff, lw tui -> Review, lw commit). A destination ending in / keeps the name.
lw revert does not undo a move; move it back with lw raw mv.
`)
}

// resolveRawMoveTarget turns a destination that names a folder ("raw/articles"
// or "raw/articles/") into a file path keeping from's name.
func resolveRawMoveTarget(from, to string) string {
	to = strings.TrimPrefix(to, "./")
	if strings.HasSuffix(to, "/") || path.Ext(to) != ".md" {
		return path.Join(to, path.Base(from))
	}
	return to
}

func cmdRawMove(args []string) error {
	fs := flag.NewFlagSet("raw mv", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	why := fs.String("m", "", "why the source moves; shown to the reviewer")
	pos, err := parseStorageFlags(fs, args)
	if err != nil {
		return &exitError{code: 2}
	}
	if len(pos) != 2 {
		rawUsage(os.Stderr)
		return &exitError{code: 2}
	}
	from := strings.TrimPrefix(pos[0], "./")
	to := resolveRawMoveTarget(from, pos[1])

	root, err := writableVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	initLoggingAt(root)

	auto := loadAutoSync(root)
	auto.pull()

	e, err := openVaultEngine(root, auto)
	if err != nil {
		return fmt.Errorf("open engine: %w", err)
	}
	defer e.Close()

	rationale := *why
	if rationale == "" {
		rationale = fmt.Sprintf("move %s to %s", from, to)
	}
	cs, joined, err := e.OpenOrJoin(fmt.Sprintf("raw mv %s -> %s", from, to), stage.Author{Kind: "human"})
	if err != nil {
		return fmt.Errorf("open changeset: %w", err)
	}
	opID, err := e.Append(stage.Op{Kind: stage.OpMoveRaw, From: from, To: to, Rationale: rationale})
	if err != nil {
		if !joined {
			// Nothing else is in a changeset this command opened.
			_ = e.Reject("lw raw mv failed: " + err.Error())
		}
		return err
	}

	cur, err := e.Current()
	if err != nil {
		return err
	}
	if joined {
		fmt.Printf("joined open changeset %s\n", cs.ID)
	} else {
		fmt.Printf("opened %s\n", cs.ID)
	}
	if op, ok := cur.Op(opID); ok {
		fmt.Printf("  %s move_raw %s -> %s\n", op.ID, op.From, op.To)
		if op.OriginalPath != "" {
			fmt.Printf("     original -> %s\n", op.OriginalPath)
		}
		for _, sub := range op.Cascade {
			fmt.Printf("  %s patch_page %s (citations)\n", sub.ID, sub.Path)
		}
	}
	c := cur.Checks
	fmt.Printf("checks: schema=%s lint=%s orphans=%d broken_links=%d\n", c.Schema, c.Lint, c.Orphans, c.BrokenLinks)
	fmt.Println("review with lw diff or lw tui, then lw commit -m \"...\"")
	return nil
}
