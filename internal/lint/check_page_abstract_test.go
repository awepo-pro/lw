package lint_test

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/awepo-pro/lw/internal/lint"
)

// pageAbstractMsg is the missing-abstract message, byte-exact (014 workflow
// F-B). The placement message (v2.5.1 fix wave, §9 A9) is pinned verbatim in
// TestPageAbstractMisplacedWarns, where the blocking heading is part of the
// expectation.
const pageAbstractMsg = "no ## Abstract section; open the page with a 2-4 sentence summary"

// abstractlessPage is a well-formed wiki page with no ## Abstract section.
func abstractlessPage() string {
	return `---
title: Abstractless
created: 2026-09-01
updated: 2026-09-02
type: concept
tags: [test]
---

# Abstractless

This page opens straight into prose with no abstract section anywhere.

## Why it matters

Because some page has to be the counterexample.
`
}

// TestPageAbstractMissingWarns asserts a wiki page without an abstract
// yields exactly one finding: page-abstract, warn, the frozen message
// (014 workflow F-B, semantics 1).
func TestPageAbstractMissingWarns(t *testing.T) {
	ctx := buildVault(t, map[string]string{
		"wiki/concepts/abstractless.md": abstractlessPage(),
	})
	report := lint.Run(ctx, []string{"page-abstract"})

	if len(report.Findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(report.Findings), report.Findings)
	}
	f := report.Findings[0]
	if f.Check != "page-abstract" {
		t.Errorf("Check = %q, want page-abstract", f.Check)
	}
	if f.Severity != lint.SevWarn {
		t.Errorf("Severity = %q, want warn", f.Severity)
	}
	if f.Path != "wiki/concepts/abstractless.md" {
		t.Errorf("Path = %q, want wiki/concepts/abstractless.md", f.Path)
	}
	if f.Message != pageAbstractMsg {
		t.Errorf("Message = %q, want %q", f.Message, pageAbstractMsg)
	}
}

// TestPageAbstractSlugCaseTolerant asserts the match is on the normalized
// Section.Slug, so any casing of the heading passes (014 workflow F-B,
// semantics 2).
func TestPageAbstractSlugCaseTolerant(t *testing.T) {
	for _, heading := range []string{"## abstract", "## Abstract", "## ABSTRACT"} {
		t.Run(heading, func(t *testing.T) {
			page := `---
title: Cased Abstract
created: 2026-09-01
updated: 2026-09-02
type: concept
tags: [test]
---

# Cased Abstract

` + heading + `

The heading's case is the slug's problem, not the page's.
`
			ctx := buildVault(t, map[string]string{
				"wiki/concepts/cased.md": page,
			})
			report := lint.Run(ctx, []string{"page-abstract"})
			if len(report.Findings) != 0 {
				t.Fatalf("got %d findings, want 0: %+v", len(report.Findings), report.Findings)
			}
		})
	}
}

// TestPageAbstractRawNeverFlagged asserts a raw source with no abstract
// produces no finding: raw/** loads as RawSources, never Pages, so the
// check never sees it (014 workflow F-B, semantics 3).
func TestPageAbstractRawNeverFlagged(t *testing.T) {
	body := "# Raw Note\n\nNo abstract here either — and none is required.\n"
	src := fmt.Sprintf(`---
source_url: https://example.org/raw-note
ingested: 2026-09-01
sha256: %x
---

%s`, sha256.Sum256([]byte(body)), body)

	ctx := buildVault(t, map[string]string{
		"raw/notes/raw-note.md": src,
	})
	if len(ctx.Vault.RawSources()) != 1 {
		t.Fatalf("raw source did not load as a RawSource (%d loaded)", len(ctx.Vault.RawSources()))
	}
	report := lint.Run(ctx, []string{"page-abstract"})
	if len(report.Findings) != 0 {
		t.Fatalf("got %d findings over a raw-only vault, want 0: %+v", len(report.Findings), report.Findings)
	}
}

// TestPageAbstractMisplacedWarns asserts a page whose ## Abstract follows
// another section yields exactly one finding: page-abstract, warn, the
// placement message naming the blocking heading verbatim (v2.5.1 fix wave,
// §9 A9 — placement became lint's concern once real vaults carried mid-page
// abstracts that --fix never repaired; still warn, the error flip stays
// queued).
func TestPageAbstractMisplacedWarns(t *testing.T) {
	ctx := buildVault(t, map[string]string{
		"wiki/concepts/late-abstract.md": `---
title: Late Abstract
created: 2026-09-01
updated: 2026-09-02
type: concept
tags: [test]
---

# Late Abstract

## Why it matters

The abstract below is deliberately not the page's first section.

## Abstract

It exists, but it does not open the body.
`,
	})
	report := lint.Run(ctx, []string{"page-abstract"})

	if len(report.Findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(report.Findings), report.Findings)
	}
	f := report.Findings[0]
	if f.Check != "page-abstract" {
		t.Errorf("Check = %q, want page-abstract", f.Check)
	}
	if f.Severity != lint.SevWarn {
		t.Errorf("Severity = %q, want warn", f.Severity)
	}
	if f.Path != "wiki/concepts/late-abstract.md" {
		t.Errorf("Path = %q, want wiki/concepts/late-abstract.md", f.Path)
	}
	if !f.Fixable {
		t.Errorf("Fixable = false, want true")
	}
	want := `## Abstract must be the page's first section; move it above "## Why it matters"`
	if f.Message != want {
		t.Errorf("Message = %q, want %q", f.Message, want)
	}
}

// TestPageAbstractAfterTitleAndPreamblePasses asserts the minimal fixtures'
// shape — # Title, a lead paragraph, then ## Abstract — stays clean: 014 §5
// asks for the abstract "before any other section", and a preamble paragraph
// is prose, not a section (v2.5.1 fix wave, §9 A9).
func TestPageAbstractAfterTitleAndPreamblePasses(t *testing.T) {
	ctx := buildVault(t, map[string]string{
		"wiki/concepts/preamble-abstract.md": `---
title: Preamble Abstract
created: 2026-09-01
updated: 2026-09-02
type: concept
tags: [test]
---

# Preamble Abstract

A lead paragraph before the abstract is legal prose, not a section.

## Abstract

Two to four self-contained sentences would sit here.

## Details

The abstract opened the body, so nothing is flagged.
`,
	})
	report := lint.Run(ctx, []string{"page-abstract"})
	if len(report.Findings) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(report.Findings), report.Findings)
	}
}
