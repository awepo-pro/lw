package main

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
)

// isConflictCopyableNote reports whether path is a note path eligible for
// conflict copy resolution during a rebase divergence (083 S1b).
// It accepts paths under notes/, under ledger/ (except ledger/receipts/),
// and files directly at the vault root (except SCHEMA.md, index.md, log.md,
// curator-memory.md, and .gitignore). Everything else returns false.
func isConflictCopyableNote(path string) bool {
	p := filepath.ToSlash(filepath.Clean(path))
	p = strings.TrimPrefix(p, "/")
	if p == "" || p == "." {
		return false
	}
	if strings.HasPrefix(p, "notes/") {
		return true
	}
	if strings.HasPrefix(p, "ledger/") {
		if strings.HasPrefix(p, "ledger/receipts/") || p == "ledger/receipts" {
			return false
		}
		return true
	}
	if !strings.Contains(p, "/") {
		switch p {
		case "SCHEMA.md", "index.md", "log.md", "curator-memory.md", ".gitignore":
			return false
		default:
			return true
		}
	}
	return false
}

// printConflictCopiesKept prints, for each copy in copies, a line:
// "<prefix>kept both versions of N note(s): this PC's edit is in <path>"
func printConflictCopiesKept(w io.Writer, prefix string, copies []string) {
	if len(copies) == 0 {
		return
	}
	sorted := append([]string(nil), copies...)
	sort.Strings(sorted)
	n := len(sorted)
	for _, c := range sorted {
		fmt.Fprintf(w, "%skept both versions of %d note(s): this PC's edit is in %s\n", prefix, n, c)
	}
}
