package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/awepo-pro/lw/internal/vault"
)

// orientIndexMaxLines is how far vault.orient truncates index.md
// (backbone §6).
const orientIndexMaxLines = 200

// orientLogMaxLines is how many trailing lines of log.md vault.orient
// includes (backbone §6).
const orientLogMaxLines = 30

// vaultOrientTool builds "vault.orient": SCHEMA.md, index.md, curator
// memory and the tail of log.md, all in one call. The engine injects the
// same digest per session (022); the tool stays for on-demand re-orientation
// (/docs/design.md §8, §11.3).
func vaultOrientTool(d Deps) Tool {
	return Tool{
		Name: "vault.orient",
		Description: "Read the vault's SCHEMA.md, index.md, curator-memory.md " +
			"and the tail of log.md in a single call — a deliberate, " +
			"on-demand re-orientation. The engine already injects an " +
			"orientation digest into every turn's context, so this tool is " +
			"not part of routine work; choose it when you need a fresh view " +
			"of the vault, such as after stage edits changed the structure " +
			"or when the injected digest seems out of date.",
		Schema:   json.RawMessage(vaultOrientSchema),
		ReadOnly: true,
		Handler: func(ctx context.Context, args json.RawMessage) (Result, error) {
			return orientHandler(ctx, d, args)
		},
	}
}

func orientHandler(ctx context.Context, d Deps, args json.RawMessage) (Result, error) {
	schemaBytes, err := d.Vault.Read("SCHEMA.md")
	if err != nil {
		// SCHEMA.md is required for the vault to have opened at all
		// (backbone §2.8), so a failure here is an engine-level problem,
		// not a bad argument — a real Go error, per the §6 error
		// convention.
		return Result{}, fmt.Errorf("tools: vault.orient: read SCHEMA.md: %w", err)
	}

	indexText, indexTruncated := firstNLines(readOptional(d.Vault, "index.md"), orientIndexMaxLines)
	memoryText := readOptional(d.Vault, "curator-memory.md")
	logText := lastNLines(readOptional(d.Vault, "log.md"), orientLogMaxLines)

	var b strings.Builder
	b.WriteString("# Orientation\n\n")

	b.WriteString("## SCHEMA.md\n\n")
	writeSectionBody(&b, string(schemaBytes))

	b.WriteString("\n## index.md")
	if indexTruncated {
		fmt.Fprintf(&b, " (truncated to %d lines)", orientIndexMaxLines)
	}
	b.WriteString("\n\n")
	writeSectionBody(&b, indexText)

	b.WriteString("\n## curator-memory.md\n\n")
	writeSectionBody(&b, memoryText)

	fmt.Fprintf(&b, "\n## log.md (last %d lines)\n\n", orientLogMaxLines)
	writeSectionBody(&b, logText)

	return Result{Content: strings.TrimRight(b.String(), "\n") + "\n"}, nil
}

// writeSectionBody appends text to b, falling back to a visible "(empty)"
// marker so an orientation digest never silently drops a section.
func writeSectionBody(b *strings.Builder, text string) {
	if text == "" {
		b.WriteString("(empty)\n")
		return
	}
	b.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		b.WriteString("\n")
	}
}

// readOptional reads path from v, returning "" for a file that does not
// exist yet — a fresh vault may have no curator-memory.md or log.md.
func readOptional(v *vault.Vault, path string) string {
	if !v.Exists(path) {
		return ""
	}
	b, err := v.Read(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// splitLines splits s into lines without the trailing empty element that
// strings.Split produces when s ends in "\n" — every vault file does
// (00-conventions.md §3).
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// firstNLines returns the first n lines of s joined by "\n", and whether s
// had more than n lines to begin with.
func firstNLines(s string, n int) (string, bool) {
	lines := splitLines(s)
	if len(lines) <= n {
		return s, false
	}
	return strings.Join(lines[:n], "\n"), true
}

// lastNLines returns the last n lines of s joined by "\n".
func lastNLines(s string, n int) string {
	lines := splitLines(s)
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
