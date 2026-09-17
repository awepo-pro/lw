package main

// cmd_init_gitignore.go is init's one publish-leak defence (005 contract
// §7): the vault's .gitignore carries .llmwiki/, so session.ndjson
// transcripts — whole file dumps and raw provider output — never commit by
// accident. A fresh vault gets a fresh .gitignore; an existing one gains at
// most the single entry line, appended only when absent, never rewritten or
// reordered. init never edits anything else about the user's repository.

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	// gitignoreName is the vault-root file init writes. Unlike the four
	// markdown files it is special-cased rather than tabled: a table entry
	// could only skip an existing file, while an existing .gitignore must
	// gain the entry when it lacks it.
	gitignoreName = ".gitignore"

	// gitignoreEntry is the single line .llmwiki/ must be ignored by. It is
	// built from stateDirName so it cannot drift from the layout doctor
	// inspects.
	gitignoreEntry = stateDirName + "/"
)

// ensureGitignore leaves the vault's .gitignore carrying gitignoreEntry and
// reports whether it WROTE it — a fresh file or a one-line append — so
// init's report can name it alongside its other files. A file already
// covering the entry is left byte-for-byte alone and reported as skipped by
// the caller.
func ensureGitignore(dir string) (wrote bool, err error) {
	full := filepath.Join(dir, gitignoreName)
	data, err := os.ReadFile(full)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.WriteFile(full, []byte(gitignoreEntry+"\n"), 0o644); err != nil {
			return false, fmt.Errorf("write %s: %w", gitignoreName, err)
		}
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", gitignoreName, err)
	}
	if ignoreEntryPresent(string(data)) {
		return false, nil
	}

	// Append exactly one entry line, matching the file's own line ending.
	// A file with no trailing newline first gets that newline — the
	// original bytes stay a prefix of the result, and its last line is
	// not glued to the entry.
	eol := "\n"
	if bytes.HasSuffix(data, []byte("\r\n")) {
		eol = "\r\n"
	}
	var out bytes.Buffer
	out.Write(data)
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		out.WriteString(eol)
	}
	out.WriteString(gitignoreEntry)
	out.WriteString(eol)
	if err := os.WriteFile(full, out.Bytes(), 0o644); err != nil {
		return false, fmt.Errorf("append %s: %w", gitignoreName, err)
	}
	return true, nil
}

// ignoreEntryPresent reports whether an existing .gitignore already ignores
// .llmwiki/. The test is on the ignore ENTRY, not a substring: the entry in
// any of its spellings — `.llmwiki/`, `/.llmwiki/`, `.llmwiki` — counts,
// while the word inside some other path (`sub/.llmwiki/`) or inside a
// comment does not.
func ignoreEntryPresent(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		line = strings.Trim(strings.TrimSpace(line), "/")
		if line == stateDirName {
			return true
		}
	}
	return false
}
