package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// TestVaultOrientSingleCallHasAllFourSections pins the "one call" contract:
// SCHEMA.md, index.md, curator-memory.md and log.md must all be present in
// a single Result.Content.
func TestVaultOrientSingleCallHasAllFourSections(t *testing.T) {
	reg := minimalRegistry(t)

	res, err := reg.Call(context.Background(), "vault.orient", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Call(vault.orient) error = %v", err)
	}
	if res.IsError {
		t.Fatalf("Call(vault.orient) IsError; Content: %s", res.Content)
	}

	for _, want := range []string{"## SCHEMA.md", "## index.md", "## curator-memory.md", "## log.md"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("orient Content missing %q section header", want)
		}
	}
	// Content from every fixture file should show up somewhere.
	for _, want := range []string{"ml-systems", "speculative-decoding", "gpt-4", "create_page"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("orient Content missing expected fixture text %q", want)
		}
	}
}

// TestVaultOrientTruncatesIndex builds a vault whose index.md has 250
// lines and asserts vault.orient truncates it to exactly 200 (backbone §6).
func TestVaultOrientTruncatesIndex(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")

	var lines []string
	for i := 1; i <= 250; i++ {
		lines = append(lines, fmt.Sprintf("- item-%04d", i))
	}
	synthetic := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "index.md"), []byte(synthetic), 0o644); err != nil {
		t.Fatalf("write synthetic index.md: %v", err)
	}

	reg := NewRegistry(newTestDeps(t, dir))
	res, err := reg.Call(context.Background(), "vault.orient", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Call(vault.orient) error = %v", err)
	}
	if res.IsError {
		t.Fatalf("Call(vault.orient) IsError; Content: %s", res.Content)
	}

	if !strings.Contains(res.Content, "(truncated to 200 lines)") {
		t.Fatalf("orient Content does not mention the truncation:\n%s", res.Content)
	}

	section := extractSection(t, res.Content, "## index.md", "## curator-memory.md")
	got := strings.Split(strings.TrimRight(section, "\n"), "\n")
	if len(got) != 200 {
		t.Fatalf("index.md section has %d lines, want 200", len(got))
	}
	if got[0] != "- item-0001" {
		t.Errorf("first line = %q, want %q", got[0], "- item-0001")
	}
	if got[199] != "- item-0200" {
		t.Errorf("last line = %q, want %q (index.md must not be silently reordered)", got[199], "- item-0200")
	}
}

// TestVaultOrientHandlesMissingOptionalFiles asserts orient still answers
// (non-empty Content, no error) on a vault with no curator-memory.md and no
// log.md yet — the on-demand orientation tool must not fail on a fresh vault.
func TestVaultOrientHandlesMissingOptionalFiles(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	if err := os.Remove(filepath.Join(dir, "curator-memory.md")); err != nil {
		t.Fatalf("remove curator-memory.md: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "log.md")); err != nil {
		t.Fatalf("remove log.md: %v", err)
	}

	reg := NewRegistry(newTestDeps(t, dir))
	res, err := reg.Call(context.Background(), "vault.orient", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Call(vault.orient) error = %v", err)
	}
	if res.IsError {
		t.Fatalf("Call(vault.orient) IsError; Content: %s", res.Content)
	}
	if res.Content == "" {
		t.Fatal("Call(vault.orient) returned empty Content")
	}
	if !strings.Contains(res.Content, "(empty)") {
		t.Errorf("orient Content should mark the missing sections as empty, got:\n%s", res.Content)
	}
}

// extractSection returns the section body between a "## startMarker" header
// (skipping the rest of that header line, e.g. a "(truncated...)" suffix)
// and the next "## endMarker" header.
func extractSection(t *testing.T, content, startMarker, endMarker string) string {
	t.Helper()
	start := strings.Index(content, startMarker)
	if start < 0 {
		t.Fatalf("marker %q not found in content", startMarker)
	}
	headerEnd := strings.Index(content[start:], "\n\n")
	if headerEnd < 0 {
		t.Fatalf("no blank line after header %q", startMarker)
	}
	bodyStart := start + headerEnd + len("\n\n")

	rest := content[bodyStart:]
	end := strings.Index(rest, endMarker)
	if end < 0 {
		t.Fatalf("marker %q not found after %q", endMarker, startMarker)
	}
	return rest[:end]
}
