package lint_test

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/awepo-pro/lw/internal/lint"
)

// pageAbstractMsg is the one message page-abstract may emit, byte-exact
// (014 workflow F-B).
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

// TestPageAbstractPositionNotEnforced asserts placement is not lint's job:
// an abstract that is not the first ## section still passes (014 workflow
// F-B, semantics 4).
func TestPageAbstractPositionNotEnforced(t *testing.T) {
	ctx := buildVault(t, map[string]string{
		"wiki/concepts/late-abstract.md": `---
title: Late Abstract
created: 2026-09-01
updated: 2026-09-02
type: concept
tags: [test]
---

# Late Abstract

## Overview

The abstract below is deliberately not the first ## section.

## Abstract

It still passes, because placement belongs to the prompt rule, not lint.
`,
	})
	report := lint.Run(ctx, []string{"page-abstract"})
	if len(report.Findings) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(report.Findings), report.Findings)
	}
}
