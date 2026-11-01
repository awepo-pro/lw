package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/awepo-pro/lw/internal/digest"
	"github.com/awepo-pro/lw/internal/safewrite"
	"github.com/awepo-pro/lw/internal/vault"
)

// cmdDigest aggregates committed changes in a time window into a short markdown
// or HTML report: new pages with their gist, updated pages, new raw sources,
// reverts, and pending notes.
func cmdDigest(args []string) error {
	fs := flag.NewFlagSet("digest", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	sinceFlag := fs.String("since", "7d", "start of window: Nd (1..3650 days) or YYYY-MM-DD")
	untilFlag := fs.String("until", "", "exclusive end of window: YYYY-MM-DD (default: tomorrow 00:00 UTC)")
	formatFlag := fs.String("format", "md", "output format: md or html")
	oFlag := fs.String("o", "", "output file (default: stdout)")

	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return &exitError{code: 2}
	}
	if len(positionals) > 0 {
		fmt.Fprintf(os.Stderr, "lw digest: unexpected argument %q\n", positionals[0])
		return &exitError{code: 2}
	}

	if *formatFlag != "md" && *formatFlag != "html" {
		fmt.Fprintf(os.Stderr, "lw digest: invalid --format %q: must be md or html\n", *formatFlag)
		return &exitError{code: 2}
	}

	loc := time.Local
	now := time.Now().In(loc)
	since, until, err := digest.ParseWindow(now, *sinceFlag, *untilFlag, loc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lw digest: %v\n", err)
		return &exitError{code: 2}
	}

	root, err := findVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	attachLoggingAt(root)

	if *oFlag != "" {
		inside, err := isInsideVault(root, *oFlag)
		if err != nil {
			return fmt.Errorf("check output path: %w", err)
		}
		if inside {
			fmt.Fprintf(os.Stderr, "lw digest: output file %q resolves inside vault root %q\n", *oFlag, root)
			return &exitError{code: 1}
		}
	}

	v, err := vault.Open(root)
	if err != nil {
		return fmt.Errorf("open vault %s: %w", root, err)
	}

	journalPath := filepath.Join(root, ".llmwiki", "journal.ndjson")
	d, err := digest.Build(v, journalPath, digest.Options{Since: since, Until: until, Loc: loc})
	if err != nil {
		return fmt.Errorf("build digest: %w", err)
	}

	pendingStems, err := findPendingNotes(root)
	if err != nil {
		return fmt.Errorf("find pending notes: %w", err)
	}
	d.PendingNotes = pendingStems

	var w io.Writer = os.Stdout
	var buf *bytes.Buffer
	if *oFlag != "" {
		buf = new(bytes.Buffer)
		w = buf
	}

	if *formatFlag == "html" {
		if err := digest.WriteHTML(w, d); err != nil {
			return fmt.Errorf("write html: %w", err)
		}
	} else {
		if err := digest.WriteMarkdown(w, d); err != nil {
			return fmt.Errorf("write markdown: %w", err)
		}
	}

	if *oFlag != "" {
		guard, err := safewrite.NewGuard(root)
		if err != nil {
			return fmt.Errorf("safewrite guard: %w", err)
		}
		if err := guard.WriteFile(*oFlag, buf.Bytes()); err != nil {
			fmt.Fprintf(os.Stderr, "lw digest: %v\n", err)
			return &exitError{code: 1}
		}
	}

	return nil
}

func isInsideVault(root, file string) (bool, error) {
	absFile, err := filepath.Abs(file)
	if err != nil {
		return false, err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false, err
	}

	rel, err := filepath.Rel(absRoot, absFile)
	if err == nil {
		if rel == "." || rel == "" || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..") {
			return true, nil
		}
	}

	evalRoot, errRoot := filepath.EvalSymlinks(absRoot)
	evalFileDir, errFile := filepath.EvalSymlinks(filepath.Dir(absFile))
	if errRoot == nil && errFile == nil {
		evalFile := filepath.Join(evalFileDir, filepath.Base(absFile))
		relEval, errRel := filepath.Rel(evalRoot, evalFile)
		if errRel == nil {
			if relEval == "." || relEval == "" || (!strings.HasPrefix(relEval, ".."+string(filepath.Separator)) && relEval != "..") {
				return true, nil
			}
		}
	}

	return false, nil
}

type noteItem struct {
	stem    string
	created time.Time
}

func findPendingNotes(root string) ([]string, error) {
	dir := filepath.Join(root, noteDirName)
	ents, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", noteDirName, err)
	}

	var notes []noteItem
	for _, e := range ents {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read note %s: %w", e.Name(), err)
		}
		created, _ := parseNote(string(b))
		if created.IsZero() {
			info, err := os.Stat(p)
			if err != nil {
				return nil, fmt.Errorf("stat note %s: %w", e.Name(), err)
			}
			created = info.ModTime()
		}
		notes = append(notes, noteItem{
			stem:    strings.TrimSuffix(e.Name(), ".md"),
			created: created,
		})
	}

	if len(notes) == 0 {
		return nil, nil
	}

	// Newest first, then stem descending
	sort.Slice(notes, func(i, j int) bool {
		if !notes[i].created.Equal(notes[j].created) {
			return notes[i].created.After(notes[j].created)
		}
		return notes[i].stem > notes[j].stem
	})

	stems := make([]string, len(notes))
	for i, r := range notes {
		stems[i] = r.stem
	}

	tags, err := noteDoneTagsErr(root, stems)
	if err != nil {
		return nil, fmt.Errorf("cannot check pending notes: %w", err)
	}

	var pending []string
	for _, r := range notes {
		if tags[r.stem] == "" {
			pending = append(pending, r.stem)
		}
	}

	return pending, nil
}
