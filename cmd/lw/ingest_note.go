package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/awepo-pro/lw/internal/vault"
)

// noteList implements flag.Value for repeatable --note flags.
type noteList []string

func (n *noteList) String() string {
	if n == nil {
		return ""
	}
	return strings.Join(*n, ",")
}

func (n *noteList) Set(v string) error {
	*n = append(*n, v)
	return nil
}

// resolveNoteID resolves a note id or prefix against <root>/notes/.
// The binding order of rules is:
// 1. reject bad id
// 2. exact stem
// 3. unique stem prefix
// 4. ambiguous prefix (list up to 5 stems then "and N more") / none
func resolveNoteID(root, id string) (string, error) {
	target := strings.TrimSuffix(id, ".md")
	if target == "" || target == "." || target == ".." ||
		strings.Contains(id, "/") || strings.Contains(id, `\`) ||
		strings.Contains(id, "..") || filepath.IsAbs(id) {
		return "", fmt.Errorf("invalid note id %q", id)
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("abs vault root: %w", err)
	}
	notesDir := filepath.Join(absRoot, vault.NotesDir)

	ents, err := os.ReadDir(notesDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read %s: %w", vault.NotesDir, err)
	}

	var stems []string
	for _, e := range ents {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		stem := strings.TrimSuffix(e.Name(), ".md")
		if stem == "" {
			continue
		}
		stems = append(stems, stem)
	}
	sort.Strings(stems)

	// Rule 2: exact stem match
	for _, s := range stems {
		if s == target {
			return filepath.Join(notesDir, s+".md"), nil
		}
	}

	// Rule 3: unique stem prefix
	var matches []string
	for _, s := range stems {
		if strings.HasPrefix(s, target) {
			matches = append(matches, s)
		}
	}

	if len(matches) == 1 {
		return filepath.Join(notesDir, matches[0]+".md"), nil
	}

	if len(matches) > 1 {
		if len(matches) > 5 {
			return "", fmt.Errorf("ambiguous note prefix %q: %s and %d more", target, strings.Join(matches[:5], ", "), len(matches)-5)
		}
		return "", fmt.Errorf("ambiguous note prefix %q: %s", target, strings.Join(matches, ", "))
	}

	return "", fmt.Errorf("no note matching %q in notes/ (see lw note list)", id)
}

// noteHint wraps an extraction error for a relative missing path,
// appending the cwd and a suggestion to use --note if <root>/<src> exists directly under notes/.
func noteHint(root, src string, err error) error {
	base := fmt.Errorf("extract %s: %w", src, err)
	if filepath.IsAbs(src) || !errors.Is(err, os.ErrNotExist) {
		return base
	}
	cwd, _ := os.Getwd()
	candidate := filepath.Join(root, filepath.FromSlash(src))
	if info, statErr := os.Stat(candidate); statErr == nil && info.Mode().IsRegular() {
		rel, relErr := filepath.Rel(filepath.Join(root, vault.NotesDir), candidate)
		if relErr == nil && !strings.Contains(rel, string(filepath.Separator)) && !strings.Contains(rel, "/") && strings.HasSuffix(rel, ".md") {
			stem := strings.TrimSuffix(rel, ".md")
			return fmt.Errorf("%w (cwd %s; did you mean --note %s?)", base, cwd, stem)
		}
	}
	return fmt.Errorf("%w (cwd %s)", base, cwd)
}
