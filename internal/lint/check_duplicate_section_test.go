package lint_test

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/lint"
)

// chimeraPage is the F2 damage shape (020 T-C, from 014 §9 A10): a bulk
// `lw lint --fix` computed each patch_page from the committed page, so the
// old abstract was never removed and the page ended up with two ## Abstract
// sections — the correctly-placed first one and a second mid-page. Duplicate
// headings were invisible to every check that existed, which is why the run
// went QUIETER (18→3 warns) while committing 16 broken pages.
func chimeraPage() string {
	return `---
title: Chimera Page
created: 2026-09-01
updated: 2026-09-02
type: concept
tags: [test]
---

# Chimera Page

A lead paragraph before the abstract, the ordinary fixture shape.

## Abstract

The first abstract is the page's real one, correctly placed.

## Details

The body of the page lives here.

## Abstract

The second abstract is the chimera: a bulk fix appended it because the
patch was computed from the committed page.
`
}

// bodyLinesOf returns the 1-based line numbers in body of lines that start
// with needle — the same body-relative convention Wikilink.Line uses — so a
// test can pin a message's line numbers without hand-counting the fixture.
func bodyLinesOf(body, needle string) []int {
	var lines []int
	for i, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, needle) {
			lines = append(lines, i+1)
		}
	}
	return lines
}

// TestDuplicateSectionWarnsOnChimera asserts the F2 fixture earns exactly one
// duplicate-section warn, naming the slug and both occurrences' body line
// numbers (020 T-C, semantics 1).
func TestDuplicateSectionWarnsOnChimera(t *testing.T) {
	ctx := buildVault(t, map[string]string{
		"wiki/concepts/chimera.md": chimeraPage(),
	})
	report := lint.Run(ctx, []string{"duplicate-section"})

	if len(report.Findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(report.Findings), report.Findings)
	}
	f := report.Findings[0]
	if f.Check != "duplicate-section" {
		t.Errorf("Check = %q, want duplicate-section", f.Check)
	}
	if f.Severity != lint.SevWarn {
		t.Errorf("Severity = %q, want warn", f.Severity)
	}
	if f.Path != "wiki/concepts/chimera.md" {
		t.Errorf("Path = %q, want wiki/concepts/chimera.md", f.Path)
	}
	if !f.Fixable {
		t.Errorf("Fixable = false, want true")
	}
	var body string
	for _, p := range ctx.Vault.Pages() {
		if p.Path == "wiki/concepts/chimera.md" {
			body = p.Body
		}
	}
	if body == "" {
		t.Fatalf("chimera page did not load as a Page")
	}
	abstracts := bodyLinesOf(body, "## Abstract")
	if len(abstracts) != 2 {
		t.Fatalf("fixture carries %d ## Abstract lines, want 2", len(abstracts))
	}
	if !strings.Contains(f.Message, `"abstract"`) {
		t.Errorf("Message = %q, want it to name the slug \"abstract\"", f.Message)
	}
	for _, line := range abstracts {
		if !strings.Contains(f.Message, fmt.Sprintf("line %d", line)) {
			t.Errorf("Message = %q, want it to name line %d", f.Message, line)
		}
	}
}

// TestDuplicateSectionSlugNormalization asserts the match is on the normalized
// Section.Slug, so "## Abstract" and "## abstract" on one page collide
// (020 T-C, semantics 2).
func TestDuplicateSectionSlugNormalization(t *testing.T) {
	ctx := buildVault(t, map[string]string{
		"wiki/concepts/cased-dup.md": `---
title: Cased Duplicate
created: 2026-09-01
updated: 2026-09-02
type: concept
tags: [test]
---

# Cased Duplicate

## Abstract

Lowercase or not, the slug is the same.

## abstract

The heading's case is the slug's problem, not the page's.
`,
	})
	report := lint.Run(ctx, []string{"duplicate-section"})
	if len(report.Findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(report.Findings), report.Findings)
	}
	if !strings.Contains(report.Findings[0].Message, `"abstract"`) {
		t.Errorf("Message = %q, want it to name the slug \"abstract\"", report.Findings[0].Message)
	}
}

// TestDuplicateSectionCleanShapesPass asserts the check leaves three shapes
// alone: a page whose sections are all distinct, a page whose "# Notes" sits
// above "## Notes" (level-1 headings are excluded — unusual-but-legal
// old-note shape, not the chimera class), and raw sources, which load as
// RawSources, never Pages (020 T-C, semantics 3).
func TestDuplicateSectionCleanShapesPass(t *testing.T) {
	ctx := buildVault(t, map[string]string{
		"wiki/concepts/distinct.md": `---
title: Distinct
created: 2026-09-01
updated: 2026-09-02
type: concept
tags: [test]
---

# Distinct

## Abstract

Every section here is unique.

## Details

So nothing is flagged.
`,
		"wiki/concepts/notes-shape.md": `---
title: Notes Shape
created: 2026-09-01
updated: 2026-09-02
type: concept
tags: [test]
---

# Notes Shape

# Notes

A level-1 Notes above a level-2 Notes is legal old-note shape.

## Notes

The slug collides, but the level does not, so no finding.
`,
	})
	report := lint.Run(ctx, []string{"duplicate-section"})
	if len(report.Findings) != 0 {
		t.Fatalf("got %d findings over distinct/level-1 shapes, want 0: %+v", len(report.Findings), report.Findings)
	}

	body := "# Raw Note\n\n## Dup\n\nOne.\n\n## Dup\n\nTwo.\n"
	src := fmt.Sprintf(`---
source_url: https://example.org/raw-note
ingested: 2026-09-01
sha256: %x
---

%s`, sha256.Sum256([]byte(body)), body)
	rawCtx := buildVault(t, map[string]string{
		"raw/notes/raw-note.md": src,
	})
	if len(rawCtx.Vault.RawSources()) != 1 {
		t.Fatalf("raw source did not load as a RawSource (%d loaded)", len(rawCtx.Vault.RawSources()))
	}
	rawReport := lint.Run(rawCtx, []string{"duplicate-section"})
	if len(rawReport.Findings) != 0 {
		t.Fatalf("got %d findings over a raw-only vault, want 0: %+v", len(rawReport.Findings), rawReport.Findings)
	}
}

// TestDuplicateSectionRegistered asserts the check runs both in Run's default
// set and via only=["duplicate-section"] (020 T-C, semantics 4).
func TestDuplicateSectionRegistered(t *testing.T) {
	ctx := buildVault(t, map[string]string{
		"wiki/concepts/chimera.md": chimeraPage(),
	})
	for name, only := range map[string][]string{
		"default-set": nil,
		"only":        {"duplicate-section"},
	} {
		t.Run(name, func(t *testing.T) {
			report := lint.Run(ctx, only)
			var n int
			for _, f := range report.Findings {
				if f.Check == "duplicate-section" {
					n++
				}
			}
			if n != 1 {
				t.Fatalf("got %d duplicate-section findings, want 1: %+v", n, report.Findings)
			}
		})
	}
}
