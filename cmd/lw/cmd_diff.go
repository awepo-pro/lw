package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/term"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/markdown"
)

// cmdDiff prints the projected unified diff of the open changeset, a
// per-file summary with --stat, or each changed page as it will read after
// commit with --render, optionally scoped to a single op with --op.
func cmdDiff(args []string) error {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	opID := fs.String("op", "", "show only the file(s) touched by this op id")
	stat := fs.Bool("stat", false, "show a per-file summary instead of the full unified diff")
	render := fs.Bool("render", false, "render each changed page as it will read after commit")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "lw diff: unexpected argument %q\n", fs.Arg(0))
		return &exitError{code: 2}
	}
	if *render && *stat {
		fmt.Fprintln(os.Stderr, "lw diff: --render and --stat are mutually exclusive")
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

	if *render {
		return renderDiff(os.Stdout, e, d)
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

// stdoutTerm is the injectable seam for the three terminal facts --render
// needs (003 contract §8.4): whether stdout is a TTY (a character device),
// the terminal's width, and its background polarity. It is the one
// deliberately mutable package variable in cmd/lw: tests override it and
// restore it with t.Cleanup to exercise the TTY path deterministically —
// os.Stdout under captureRun is always a pipe.
var stdoutTerm = termProbe{
	isTTY: func() bool {
		info, err := os.Stdout.Stat()
		return err == nil && info.Mode()&os.ModeCharDevice != 0
	},
	width: func() int {
		w, _, err := term.GetSize(os.Stdout.Fd())
		if err != nil || w < 1 {
			return 80
		}
		return w
	},
	dark: func() bool {
		return lipgloss.HasDarkBackground(os.Stdin, os.Stdout)
	},
}

// termProbe bundles the three stdoutTerm functions so a test can replace
// them as one value.
type termProbe struct {
	isTTY func() bool
	width func() int  // terminal width in cells; consulted only when isTTY()
	dark  func() bool // background polarity; consulted only when isTTY()
}

// renderOpts carries the terminal facts one --render pass resolves once,
// before the first file is rendered.
type renderOpts struct {
	tty   bool
	width int
	style markdown.Style
}

// renderDiff prints each file of d as it will read after commit (003
// contract §8): a rule line naming the path, a blank line, the body — the
// markdown renderer's output for a staged .md page, a one-line
// substitute otherwise — and a blank line. Terminal facts come from
// stdoutTerm; a non-TTY run is plain text (no escape sequence anywhere)
// at width 80.
func renderDiff(w io.Writer, e *stage.Engine, d stage.Diff) error {
	o, err := renderOptions()
	if err != nil {
		return err
	}
	r := markdown.NewRenderer()
	seen := make(map[string]bool, len(d.Files)) // §8.2: de-duplicate by path
	for _, fd := range d.Files {
		if seen[fd.Path] {
			continue
		}
		seen[fd.Path] = true
		fmt.Fprintln(w, diffRule(fd.Path, o.width))
		fmt.Fprintln(w)
		lines, err := diffFileBody(e, r, fd, o)
		if err != nil {
			return err
		}
		for _, line := range lines {
			fmt.Fprintln(w, strings.TrimRight(line, " "))
		}
		fmt.Fprintln(w)
	}
	return nil
}

// renderOptions resolves the renderer's Options inputs once: TTY from
// stdoutTerm, width 80 when not a TTY, and the default theme's token
// palette at the terminal's polarity, mapped field by field onto
// markdown.Style (003 contract §3: ui.Palette and markdown.Style share
// field names on purpose). When not a TTY the output is Plain anyway, so
// the adaptive theme's dark default carries no bytes.
func renderOptions() (renderOpts, error) {
	tty := stdoutTerm.isTTY()
	width, dark := 80, true
	if tty {
		width = stdoutTerm.width()
		dark = stdoutTerm.dark()
	}
	theme, err := ui.LoadTheme("")
	if err != nil {
		return renderOpts{}, fmt.Errorf("load theme: %w", err)
	}
	theme = theme.WithDark(dark)
	return renderOpts{
		tty:   tty,
		width: width,
		style: markdown.Style{
			Dark:   theme.IsDark,
			Fg:     theme.Palette.Fg,
			Muted:  theme.Palette.Muted,
			Faint:  theme.Palette.Faint,
			Border: theme.Palette.Border,
			Accent: theme.Palette.Accent,
			Good:   theme.Palette.Good,
			Warn:   theme.Palette.Warn,
			Bad:    theme.Palette.Bad,
		},
	}, nil
}

// diffRule is the per-file rule line (003 contract §8.3): "── <path> "
// followed by "─" repeated max(3, W-len(path)-4) times — W cells in total
// when the path fits.
func diffRule(path string, w int) string {
	n := w - len(path) - 4
	if n < 3 {
		n = 3
	}
	return "── " + path + " " + strings.Repeat("─", n)
}

// diffFileBody renders one file's body for --render: the staged page
// through the markdown renderer, or the contract's one-line substitute
// when the path is not markdown or has no staged content (a path a commit
// will delete).
func diffFileBody(e *stage.Engine, r *markdown.Renderer, fd stage.FileDiff, o renderOpts) ([]string, error) {
	if !strings.HasSuffix(fd.Path, ".md") {
		return []string{"(not markdown: see lw diff)"}, nil
	}
	content, ok, err := e.StagedFile(fd.Path)
	if err != nil {
		return nil, err
	}
	if !ok {
		return []string{"(deleted in this changeset)"}, nil
	}
	changed, err := changedLines(e, fd)
	if err != nil {
		return nil, err
	}
	lines, err := r.Render(content, markdown.Options{
		Width:   o.width,
		Style:   o.style,
		Plain:   !o.tty,
		Changed: changed,
	})
	if err != nil {
		return nil, err
	}
	return lines, nil
}

// changedLines gathers the Changed source lines the renderer marks with
// the ▎ gutter (003 contract §8.3): the '+' line texts of the NON-dropped
// DisplayHunks of the file's own op, for this path.
func changedLines(e *stage.Engine, fd stage.FileDiff) ([]string, error) {
	opd, err := e.OpDiff(fd.OpID)
	if err != nil {
		return nil, err
	}
	var changed []string
	for _, f := range opd {
		if f.Path != fd.Path {
			continue
		}
		for _, h := range f.Hunks {
			if h.Dropped {
				continue
			}
			for _, l := range h.Lines {
				if l.Kind == '+' {
					changed = append(changed, l.Text)
				}
			}
		}
	}
	return changed, nil
}
