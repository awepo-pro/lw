package lint_test

import (
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/lint"
)

// TestBodyFrontmatterCheckProperties pins FC.2 from workflow 081:
// check ID "body-frontmatter", Severity SevWarn, Describe exact text.
func TestBodyFrontmatterCheckProperties(t *testing.T) {
	var c lint.Check
	for _, check := range lint.All() {
		if check.ID() == "body-frontmatter" {
			c = check
			break
		}
	}
	if c == nil {
		t.Fatal("body-frontmatter check not found in lint.All()")
	}
	if c.ID() != "body-frontmatter" {
		t.Errorf("ID = %q, want \"body-frontmatter\"", c.ID())
	}
	wantDesc := `a wiki/ page body carries a frontmatter block (a "---" line followed by a frontmatter key)`
	if c.Describe() != wantDesc {
		t.Errorf("Describe = %q, want %q", c.Describe(), wantDesc)
	}
	if c.Severity() != lint.SevWarn {
		t.Errorf("Severity = %q, want %q", c.Severity(), lint.SevWarn)
	}
}

// TestBodyFrontmatterWarnsOnPreFixInfinibandShape pins FC.2 and FC.3 from workflow 081:
// the pre-fix infiniband shape (frontmatter, then a body starting with a frontmatter block)
// gives exactly one body-frontmatter finding, with 1-based line number and exact message.
func TestBodyFrontmatterWarnsOnPreFixInfinibandShape(t *testing.T) {
	infinibandContent := `---
title: InfiniBand
created: 2026-09-01
updated: 2026-09-02
type: concept
tags: [test]
---

---
title: X
created: 2026-09-01
---

Lead paragraph.

## Abstract

An abstract section.
`
	ctx := buildVault(t, map[string]string{
		"wiki/concepts/infiniband.md": infinibandContent,
	})
	report := lint.Run(ctx, []string{"body-frontmatter"})

	if len(report.Findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(report.Findings), report.Findings)
	}
	f := report.Findings[0]
	if f.Check != "body-frontmatter" {
		t.Errorf("Check = %q, want body-frontmatter", f.Check)
	}
	if f.Severity != lint.SevWarn {
		t.Errorf("Severity = %q, want warn", f.Severity)
	}
	if f.Path != "wiki/concepts/infiniband.md" {
		t.Errorf("Path = %q, want wiki/concepts/infiniband.md", f.Path)
	}
	if f.Line != 1 {
		t.Errorf("Line = %d, want 1", f.Line)
	}
	if !f.Fixable {
		t.Errorf("Fixable = false, want true")
	}
	wantMsg := `body contains a frontmatter block at line 1 ("---" then "title:"); the page's real frontmatter is the one above, so delete this copy`
	if f.Message != wantMsg {
		t.Errorf("Message = %q, want %q", f.Message, wantMsg)
	}
}

// TestBodyFrontmatterOffsetLine pins FC.2: line number is 1-based in page body.
func TestBodyFrontmatterOffsetLine(t *testing.T) {
	pageContent := `---
title: Offset Test
created: 2026-09-01
updated: 2026-09-02
type: concept
tags: [test]
---

Lead paragraph line 1.
Lead paragraph line 2.

---
sources: [raw/articles/a.md]
---

## Abstract

Summary.
`
	ctx := buildVault(t, map[string]string{
		"wiki/concepts/offset.md": pageContent,
	})
	report := lint.Run(ctx, []string{"body-frontmatter"})

	if len(report.Findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(report.Findings), report.Findings)
	}
	f := report.Findings[0]
	if f.Line != 4 {
		t.Errorf("Line = %d, want 4", f.Line)
	}
	wantMsg := `body contains a frontmatter block at line 4 ("---" then "sources:"); the page's real frontmatter is the one above, so delete this copy`
	if f.Message != wantMsg {
		t.Errorf("Message = %q, want %q", f.Message, wantMsg)
	}
}

// TestBodyFrontmatterIgnoresNonBlocks pins FC.3:
// bodies with "a\n\n---\n\nb" or "---\nnot a key" give 0 findings.
func TestBodyFrontmatterIgnoresNonBlocks(t *testing.T) {
	dashesBetweenProse := `---
title: Prose Dashes
created: 2026-09-01
updated: 2026-09-02
type: concept
tags: [test]
---

Prose paragraph.

---

More prose paragraph.

## Abstract

Summary.
`
	dashesNotAKey := `---
title: Non Key
created: 2026-09-01
updated: 2026-09-02
type: concept
tags: [test]
---

---
not a key
---

## Abstract

Summary.
`
	cleanPage := `---
title: Clean
created: 2026-09-01
updated: 2026-09-02
type: concept
tags: [test]
---

Normal body text.

## Abstract

Summary.
`
	ctx := buildVault(t, map[string]string{
		"wiki/concepts/dashes-prose.md":  dashesBetweenProse,
		"wiki/concepts/dashes-nonkey.md": dashesNotAKey,
		"wiki/concepts/clean.md":         cleanPage,
	})
	report := lint.Run(ctx, []string{"body-frontmatter"})
	if len(report.Findings) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(report.Findings), report.Findings)
	}
}

// TestBodyFrontmatterNeverChecksRaw pins FC.3:
// raw/** files are never checked (raw sources load as RawSources, never Pages).
func TestBodyFrontmatterNeverChecksRaw(t *testing.T) {
	rawWithBodyBlock := `---
title: Raw Article
created: 2026-09-01
updated: 2026-09-01
type: article
sources: []
confidence: high
---

Raw body content.

---
title: Duplicated In Raw
---

More raw body content.
`
	cleanWikiPage := `---
title: Wiki Clean
created: 2026-09-01
updated: 2026-09-02
type: concept
tags: [test]
---

Normal wiki body.

## Abstract

Summary.
`
	ctx := buildVault(t, map[string]string{
		"raw/articles/raw-one.md": rawWithBodyBlock,
		"wiki/concepts/clean.md":  cleanWikiPage,
	})
	report := lint.Run(ctx, []string{"body-frontmatter"})
	for _, f := range report.Findings {
		if strings.HasPrefix(f.Path, "raw/") {
			t.Errorf("finding reported on raw path %q", f.Path)
		}
	}
	if len(report.Findings) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(report.Findings), report.Findings)
	}
}
