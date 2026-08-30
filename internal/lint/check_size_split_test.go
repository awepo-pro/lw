package lint_test

import (
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/lint"
)

// TestSizeSplitDirty isolates size-split's one row on spec/fixtures/dirty.
func TestSizeSplitDirty(t *testing.T) {
	ctx := openFixtureContext(t, "dirty")
	report := lint.Run(ctx, []string{"size-split"})

	if len(report.Findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(report.Findings), report.Findings)
	}
	f := report.Findings[0]
	if f.Path != "wiki/concepts/long-page.md" {
		t.Errorf("Path = %q, want wiki/concepts/long-page.md", f.Path)
	}
	want := "body exceeds 200 lines; consider splitting into smaller pages"
	if f.Message != want {
		t.Errorf("Message = %q, want %q", f.Message, want)
	}
}

// pageWithBodyLines builds a page whose body is exactly n lines of "x\n",
// for exercising size-split's ">200, not >=200" boundary (S1 correction
// C-8).
func pageWithBodyLines(n int) string {
	return `---
title: Sized Page
created: 2026-01-01
updated: 2026-01-02
type: concept
---

` + strings.Repeat("x\n", n)
}

// TestSizeSplitBoundary proves the threshold is strictly greater than 200
// lines: exactly 200 must not fire, 201 must.
func TestSizeSplitBoundary(t *testing.T) {
	tests := []struct {
		name  string
		lines int
		want  int
	}{
		{"exactly 200 lines does not fire", 200, 0},
		{"201 lines fires", 201, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := buildVault(t, map[string]string{
				"wiki/concepts/sized.md": pageWithBodyLines(tc.lines),
			})
			report := lint.Run(ctx, []string{"size-split"})
			if len(report.Findings) != tc.want {
				t.Fatalf("got %d findings, want %d: %+v", len(report.Findings), tc.want, report.Findings)
			}
		})
	}
}
