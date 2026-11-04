package lint_test

import (
	"testing"

	"github.com/awepo-pro/lw/internal/lint"
)

func rawWithSupersedes(url, date, supersedes string) string {
	res := "---\nsource_url: " + url + "\ningested: " + date + "\nsha256: 0000000000000000000000000000000000000000000000000000000000000000\n"
	if supersedes != "" {
		res += "supersedes: " + supersedes + "\n"
	}
	res += "---\n\n# Source\n\nBody text.\n"
	return res
}

// TestCiteSuperseded_L1 tests L1 from workflow 070:
// a page citing ^[raw/articles/x.md] or listing it in sources:, with
// raw/articles/x-2.md supersedes: raw/articles/x.md:
// one finding, check cite-superseded, severity warn, path = the page, message
// cites raw/articles/x.md in <sources: | the body>, superseded by raw/articles/x-2.md; re-point it to the new raw.
// A page citing both old and new raw: still one finding (the old one).
// A page citing only the new raw: none.
func TestCiteSuperseded_L1(t *testing.T) {
	rawOld := rawWithSupersedes("https://example.org/x", "2026-01-01", "")
	rawNew := rawWithSupersedes("https://example.org/x", "2026-02-01", "raw/articles/x.md")

	// A-070-6: a sources: entry alone earns nothing. stage.patch_page only ever adds
	// to sources:, so the agent could never clear it; src-provenance owns that case.
	t.Run("page listing superseded raw only in sources earns no warn", func(t *testing.T) {
		ctx := buildVault(t, map[string]string{
			"raw/articles/x.md":   rawOld,
			"raw/articles/x-2.md": rawNew,
			"wiki/concepts/page.md": citePageBody([]string{"raw/articles/x.md"},
				"# Page\n\nBody text without explicit body cite.\n"),
		})
		report := lint.Run(ctx, []string{"cite-superseded"})
		if len(report.Findings) != 0 {
			t.Fatalf("got %d findings, want 0: %+v", len(report.Findings), report.Findings)
		}
	})

	t.Run("page citing superseded raw in body earns warn", func(t *testing.T) {
		ctx := buildVault(t, map[string]string{
			"raw/articles/x.md":   rawOld,
			"raw/articles/x-2.md": rawNew,
			"wiki/concepts/page.md": citePageBody(nil,
				"# Page\n\nClaim one.^[raw/articles/x.md]\n"),
		})

		report := lint.Run(ctx, []string{"cite-superseded"})
		if len(report.Findings) != 1 {
			t.Fatalf("got %d findings, want 1: %+v", len(report.Findings), report.Findings)
		}
		f := report.Findings[0]
		if f.Check != "cite-superseded" {
			t.Errorf("Check = %q, want cite-superseded", f.Check)
		}
		if f.Path != "wiki/concepts/page.md" {
			t.Errorf("Path = %q, want wiki/concepts/page.md", f.Path)
		}
		if f.Severity != lint.SevWarn {
			t.Errorf("Severity = %v, want SevWarn", f.Severity)
		}
		wantMsg := "cites raw/articles/x.md in the body, superseded by raw/articles/x-2.md; re-point it to the new raw"
		if f.Message != wantMsg {
			t.Errorf("Message = %q, want %q", f.Message, wantMsg)
		}
	})

	t.Run("page citing both old and new raw earns one finding for old raw", func(t *testing.T) {
		ctx := buildVault(t, map[string]string{
			"raw/articles/x.md":   rawOld,
			"raw/articles/x-2.md": rawNew,
			"wiki/concepts/page.md": citePageBody([]string{"raw/articles/x.md", "raw/articles/x-2.md"},
				"# Page\n\nOld claim.^[raw/articles/x.md]\n\nNew claim.^[raw/articles/x-2.md]\n"),
		})

		report := lint.Run(ctx, []string{"cite-superseded"})
		if len(report.Findings) != 1 {
			t.Fatalf("got %d findings, want 1: %+v", len(report.Findings), report.Findings)
		}
		f := report.Findings[0]
		if f.Path != "wiki/concepts/page.md" {
			t.Errorf("Path = %q, want wiki/concepts/page.md", f.Path)
		}
		wantMsg := "cites raw/articles/x.md in the body, superseded by raw/articles/x-2.md; re-point it to the new raw"
		if f.Message != wantMsg {
			t.Errorf("Message = %q, want %q", f.Message, wantMsg)
		}
	})

	t.Run("page citing only new raw earns no findings", func(t *testing.T) {
		ctx := buildVault(t, map[string]string{
			"raw/articles/x.md":   rawOld,
			"raw/articles/x-2.md": rawNew,
			"wiki/concepts/page.md": citePageBody([]string{"raw/articles/x-2.md"},
				"# Page\n\nNew claim.^[raw/articles/x-2.md]\n"),
		})

		report := lint.Run(ctx, []string{"cite-superseded"})
		if len(report.Findings) != 0 {
			t.Fatalf("got %d findings, want 0: %+v", len(report.Findings), report.Findings)
		}
	})

	t.Run("page citing superseded raw multiple times earns only one finding", func(t *testing.T) {
		ctx := buildVault(t, map[string]string{
			"raw/articles/x.md":   rawOld,
			"raw/articles/x-2.md": rawNew,
			"wiki/concepts/page.md": citePageBody([]string{"raw/articles/x.md"},
				"# Page\n\nClaim 1.^[raw/articles/x.md]\n\nClaim 2.^[raw/articles/x.md]\n"),
		})

		report := lint.Run(ctx, []string{"cite-superseded"})
		if len(report.Findings) != 1 {
			t.Fatalf("got %d findings, want 1: %+v", len(report.Findings), report.Findings)
		}
	})
}

// TestCiteSuperseded_L2 tests L2 from workflow 070:
// a raw whose supersedes: names a missing/non-raw path: one finding on that RAW
// (supersedes names raw/missing.md, which is not a committed raw), severity warn.
func TestCiteSuperseded_L2(t *testing.T) {
	t.Run("raw naming missing raw earns warn on that raw", func(t *testing.T) {
		rawClaimant := rawWithSupersedes("https://example.org/x", "2026-02-01", "raw/missing.md")
		ctx := buildVault(t, map[string]string{
			"raw/articles/claimant.md": rawClaimant,
		})

		report := lint.Run(ctx, []string{"cite-superseded"})
		if len(report.Findings) != 1 {
			t.Fatalf("got %d findings, want 1: %+v", len(report.Findings), report.Findings)
		}
		f := report.Findings[0]
		if f.Check != "cite-superseded" {
			t.Errorf("Check = %q, want cite-superseded", f.Check)
		}
		if f.Path != "raw/articles/claimant.md" {
			t.Errorf("Path = %q, want raw/articles/claimant.md", f.Path)
		}
		if f.Severity != lint.SevWarn {
			t.Errorf("Severity = %v, want SevWarn", f.Severity)
		}
		wantMsg := "supersedes names raw/missing.md, which is not a committed raw"
		if f.Message != wantMsg {
			t.Errorf("Message = %q, want %q", f.Message, wantMsg)
		}
	})

	t.Run("raw naming non-raw path earns warn on that raw", func(t *testing.T) {
		rawClaimant := rawWithSupersedes("https://example.org/x", "2026-02-01", "wiki/concepts/page.md")
		ctx := buildVault(t, map[string]string{
			"raw/articles/claimant.md": rawClaimant,
			"wiki/concepts/page.md":    citePageBody(nil, "# Page\n"),
		})

		report := lint.Run(ctx, []string{"cite-superseded"})
		if len(report.Findings) != 1 {
			t.Fatalf("got %d findings, want 1: %+v", len(report.Findings), report.Findings)
		}
		f := report.Findings[0]
		if f.Path != "raw/articles/claimant.md" {
			t.Errorf("Path = %q, want raw/articles/claimant.md", f.Path)
		}
		wantMsg := "supersedes names wiki/concepts/page.md, which is not a committed raw"
		if f.Message != wantMsg {
			t.Errorf("Message = %q, want %q", f.Message, wantMsg)
		}
	})

	t.Run("raw naming valid committed raw earns no findings", func(t *testing.T) {
		rawOld := rawWithSupersedes("https://example.org/x", "2026-01-01", "")
		rawNew := rawWithSupersedes("https://example.org/x", "2026-02-01", "raw/articles/old.md")
		ctx := buildVault(t, map[string]string{
			"raw/articles/old.md": rawOld,
			"raw/articles/new.md": rawNew,
		})

		report := lint.Run(ctx, []string{"cite-superseded"})
		if len(report.Findings) != 0 {
			t.Fatalf("got %d findings, want 0: %+v", len(report.Findings), report.Findings)
		}
	})
}

// TestCiteSuperseded_L3_SrcStaleSkipsSuperseded tests L3 from workflow 070:
// src-stale no longer reports a page whose only stale source is superseded;
// behaviour for every other page is unchanged (existing tests pass).
func TestCiteSuperseded_L3_SrcStaleSkipsSuperseded(t *testing.T) {
	t.Run("page whose only stale source is superseded is clean", func(t *testing.T) {
		// Old source ingested 2026-06-01, page updated 2026-01-01 (> 90d gap, would be stale)
		// but rawOld is superseded by rawNew.
		rawOld := rawWithSupersedes("https://example.org/x", "2026-06-01", "")
		rawNew := rawWithSupersedes("https://example.org/x", "2026-06-02", "raw/papers/old.md")
		page := `---
title: Page
created: 2026-01-01
updated: 2026-01-01
type: concept
sources: [raw/papers/old.md]
---

# Page

Cites old source.^[raw/papers/old.md]
`
		ctx := buildVault(t, map[string]string{
			"raw/papers/old.md":     rawOld,
			"raw/papers/new.md":     rawNew,
			"wiki/concepts/page.md": page,
		})

		report := lint.Run(ctx, []string{"src-stale"})
		if len(report.Findings) != 0 {
			t.Fatalf("got %d findings, want 0: %+v", len(report.Findings), report.Findings)
		}
	})

	t.Run("page with superseded stale source and non-superseded stale source reports non-superseded", func(t *testing.T) {
		rawOld := rawWithSupersedes("https://example.org/x", "2026-06-01", "")
		rawNew := rawWithSupersedes("https://example.org/x", "2026-06-02", "raw/papers/old.md")
		rawStale := rawWithSupersedes("https://example.org/y", "2026-06-01", "")
		page := `---
title: Page
created: 2026-01-01
updated: 2026-01-01
type: concept
sources: [raw/papers/old.md, raw/papers/stale.md]
---

# Page

Cites both sources.^[raw/papers/old.md] ^[raw/papers/stale.md]
`
		ctx := buildVault(t, map[string]string{
			"raw/papers/old.md":     rawOld,
			"raw/papers/new.md":     rawNew,
			"raw/papers/stale.md":   rawStale,
			"wiki/concepts/page.md": page,
		})

		report := lint.Run(ctx, []string{"src-stale"})
		if len(report.Findings) != 1 {
			t.Fatalf("got %d findings, want 1: %+v", len(report.Findings), report.Findings)
		}
		f := report.Findings[0]
		if f.Check != "src-stale" {
			t.Errorf("Check = %q, want src-stale", f.Check)
		}
		wantMsg := "updated 2026-01-01 is more than 90 days before raw/papers/stale.md was ingested 2026-06-01; review the page against its source"
		if f.Message != wantMsg {
			t.Errorf("Message = %q, want %q", f.Message, wantMsg)
		}
	})
}

// TestCiteSuperseded_L4_RegistryAndOrder tests L4 from workflow 070:
// len(All()) == 20 (081 A-081-5); check order: cite-superseded registered after cite-source.
func TestCiteSuperseded_L4_RegistryAndOrder(t *testing.T) {
	checks := lint.All()
	if len(checks) != 20 {
		t.Fatalf("len(All()) = %d, want 20", len(checks))
	}

	citeSourceIdx := -1
	citeSupersededIdx := -1
	for i, c := range checks {
		if c.ID() == "cite-source" {
			citeSourceIdx = i
		}
		if c.ID() == "cite-superseded" {
			citeSupersededIdx = i
		}
	}

	if citeSourceIdx == -1 {
		t.Fatal("cite-source check not found in All()")
	}
	if citeSupersededIdx == -1 {
		t.Fatal("cite-superseded check not found in All()")
	}
	if citeSupersededIdx != citeSourceIdx+1 {
		t.Fatalf("cite-superseded index = %d, want cite-source index %d + 1", citeSupersededIdx, citeSourceIdx)
	}
}

// A-070-6 review follow-ups: a chain names its tip, and a raw that supersedes
// itself is a defect on the raw, not a circular warning on every citing page.
func TestCiteSuperseded_ChainTipAndSelfReference(t *testing.T) {
	r1 := rawWithSupersedes("https://example.org/x", "2026-01-01", "")
	r2 := rawWithSupersedes("https://example.org/x", "2026-02-01", "raw/articles/x.md")
	r3 := rawWithSupersedes("https://example.org/x", "2026-03-01", "raw/articles/x-2.md")

	t.Run("chain names the tip", func(t *testing.T) {
		ctx := buildVault(t, map[string]string{
			"raw/articles/x.md": r1, "raw/articles/x-2.md": r2, "raw/articles/x-3.md": r3,
			"wiki/concepts/page.md": citePageBody(nil, "# Page\n\nClaim.^[raw/articles/x.md]\n"),
		})
		report := lint.Run(ctx, []string{"cite-superseded"})
		if len(report.Findings) != 1 {
			t.Fatalf("got %d findings, want 1: %+v", len(report.Findings), report.Findings)
		}
		want := "cites raw/articles/x.md in the body, superseded by raw/articles/x-3.md; re-point it to the new raw"
		if report.Findings[0].Message != want {
			t.Errorf("Message = %q, want %q", report.Findings[0].Message, want)
		}
	})

	t.Run("self reference is one defect on the raw, none on pages", func(t *testing.T) {
		self := rawWithSupersedes("https://example.org/s", "2026-01-01", "raw/articles/s.md")
		ctx := buildVault(t, map[string]string{
			"raw/articles/s.md":     self,
			"wiki/concepts/page.md": citePageBody(nil, "# Page\n\nClaim.^[raw/articles/s.md]\n"),
		})
		report := lint.Run(ctx, []string{"cite-superseded"})
		if len(report.Findings) != 1 || report.Findings[0].Path != "raw/articles/s.md" || report.Findings[0].Message != "supersedes names itself" {
			t.Fatalf("want one 'supersedes names itself' finding on the raw, got %+v", report.Findings)
		}
	})

	t.Run("cycle terminates", func(t *testing.T) {
		a := rawWithSupersedes("https://example.org/c", "2026-01-01", "raw/articles/c-b.md")
		b := rawWithSupersedes("https://example.org/c", "2026-02-01", "raw/articles/c-a.md")
		ctx := buildVault(t, map[string]string{
			"raw/articles/c-a.md": a, "raw/articles/c-b.md": b,
			"wiki/concepts/page.md": citePageBody(nil, "# Page\n\nClaim.^[raw/articles/c-a.md]\n"),
		})
		_ = lint.Run(ctx, []string{"cite-superseded"}) // must return
	})
}
