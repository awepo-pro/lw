package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxInstructionRunes    = 2000
	maxInstructionTagRunes = 60
)

// normalizeInstruction validates and normalizes a user instruction for
// lw ingest --prepare. It normalizes CRLF to LF, trims surrounding whitespace,
// checks that the text is not empty, contains no NUL or control characters
// (except \t and \n), and does not exceed 2000 runes.
func normalizeInstruction(s string) (string, error) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("instruction cannot be empty")
	}
	for _, r := range s {
		if (unicode.IsControl(r) && r != '\t' && r != '\n') || r == 0 {
			return "", fmt.Errorf("instruction contains control character %q", r)
		}
	}
	if n := utf8.RuneCountInString(s); n > maxInstructionRunes {
		return "", fmt.Errorf("instruction exceeds %d runes (got %d)", maxInstructionRunes, n)
	}
	return s, nil
}

// readInstructionFile reads the user instruction from path, or from stdin when
// path is "-".
func readInstructionFile(path string) (string, error) {
	if path == "-" {
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return string(b), nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// cutInstructionTag cuts firstLine to at most 60 runes, appending "..." if cut.
func cutInstructionTag(firstLine string) string {
	r := []rune(firstLine)
	if len(r) <= maxInstructionTagRunes {
		return firstLine
	}
	return string(r[:maxInstructionTagRunes]) + "..."
}

// instructionIntentTag returns the intent suffix for an instruction, or empty if
// the instruction is empty.
func instructionIntentTag(instruction string) string {
	if instruction == "" {
		return ""
	}
	firstLine := strings.Split(instruction, "\n")[0]
	return " \u2014 note: " + cutInstructionTag(firstLine)
}
