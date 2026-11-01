package htmlexport

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

func createTestVault(t *testing.T) string {
	t.Helper()

	vaultDir := t.TempDir()

	schemaSrc := filepath.Join(testutil.FixtureRoot(t), "minimal", "SCHEMA.md")
	schemaData, err := os.ReadFile(schemaSrc)
	if err != nil {
		t.Fatalf("read minimal SCHEMA.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, "SCHEMA.md"), schemaData, 0644); err != nil {
		t.Fatalf("write SCHEMA.md: %v", err)
	}

	dirs := []string{
		filepath.Join(vaultDir, "wiki", "concepts"),
		filepath.Join(vaultDir, "wiki", "entities"),
		filepath.Join(vaultDir, "raw", "papers"),
		filepath.Join(vaultDir, "raw", "assets"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	pageA := `---
title: Page A
type: concept
tags: [alpha, beta]
sources: [raw/papers/p.md]
---
## Overview

This is page A linking [[other]] and [[missing]].
`
	if err := os.WriteFile(filepath.Join(vaultDir, "wiki", "concepts", "page-a.md"), []byte(pageA), 0644); err != nil {
		t.Fatalf("write page-a: %v", err)
	}

	pageOther := `---
title: Other Page
type: concept
tags: [gamma]
---
## Overview

This is the other page.
`
	if err := os.WriteFile(filepath.Join(vaultDir, "wiki", "concepts", "other.md"), []byte(pageOther), 0644); err != nil {
		t.Fatalf("write other: %v", err)
	}

	pageC := `---
title: <b>&" Page C
type: entity
tags: [delta]
---
## Overview

Citing p here: ^[raw/papers/p.md p.2]

` + "```go\n" + `// Fake citation inside fenced code block:
^[raw/papers/p.md p.1]
` + "```\n\n<script>alert(1)</script>\n"

	if err := os.WriteFile(filepath.Join(vaultDir, "wiki", "entities", "page-c.md"), []byte(pageC), 0644); err != nil {
		t.Fatalf("write page-c: %v", err)
	}

	rawP := `---
source_url: https://example.com/p.pdf
ingested: 2026-10-01
sha256: 1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef
---
# Paper P

<!-- page 1 -->
Page 1 content.

<!-- page 2 -->
Page 2 content.
`
	if err := os.WriteFile(filepath.Join(vaultDir, "raw", "papers", "p.md"), []byte(rawP), 0644); err != nil {
		t.Fatalf("write raw p: %v", err)
	}

	assetData := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x01, 0x02, 0x03}
	if err := os.WriteFile(filepath.Join(vaultDir, "raw", "assets", "img.png"), assetData, 0644); err != nil {
		t.Fatalf("write asset img.png: %v", err)
	}

	return vaultDir
}

func TestHTMLExport(t *testing.T) {
	vaultDir := createTestVault(t)
	v, err := vault.Open(vaultDir)
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}

	outDir := t.TempDir()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	res, err := Export(v, outDir, Options{Now: now, Title: "Test Site"})
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	// H1: files
	if res.Pages != 3 {
		t.Errorf("res.Pages = %d, want 3", res.Pages)
	}
	if res.Raws != 1 {
		t.Errorf("res.Raws = %d, want 1", res.Raws)
	}
	if res.Assets != 1 {
		t.Errorf("res.Assets = %d, want 1", res.Assets)
	}

	expectedFiles := []string{
		"wiki/concepts/page-a.html",
		"wiki/concepts/other.html",
		"wiki/entities/page-c.html",
		"raw/papers/p.html",
		"raw/assets/img.png",
		"index.html",
		"style.css",
		"search.js",
		"app.js",
	}
	for _, ef := range expectedFiles {
		fp := filepath.Join(outDir, ef)
		if _, err := os.Stat(fp); err != nil {
			t.Errorf("expected file missing: %s (%v)", ef, err)
		}
	}

	origAsset, err := os.ReadFile(filepath.Join(vaultDir, "raw", "assets", "img.png"))
	if err != nil {
		t.Fatalf("read orig asset: %v", err)
	}
	exportedAsset, err := os.ReadFile(filepath.Join(outDir, "raw", "assets", "img.png"))
	if err != nil {
		t.Fatalf("read exported asset: %v", err)
	}
	if !bytes.Equal(origAsset, exportedAsset) {
		t.Errorf("exported asset bytes do not match original")
	}

	// H2: wikilinks
	pageAHTML, err := os.ReadFile(filepath.Join(outDir, "wiki", "concepts", "page-a.html"))
	if err != nil {
		t.Fatalf("read page-a.html: %v", err)
	}
	pageAContent := string(pageAHTML)
	if !strings.Contains(pageAContent, `href="other.html"`) {
		t.Errorf("page-a.html missing href=\"other.html\", content:\n%s", pageAContent)
	}
	if !strings.Contains(pageAContent, `class="broken"`) {
		t.Errorf("page-a.html missing class=\"broken\" for missing link, content:\n%s", pageAContent)
	}

	// H3: citation & anchor
	pageCHTML, err := os.ReadFile(filepath.Join(outDir, "wiki", "entities", "page-c.html"))
	if err != nil {
		t.Fatalf("read page-c.html: %v", err)
	}
	pageCContent := string(pageCHTML)
	expectedCiteHref := `href="../../raw/papers/p.html#p2"`
	if !strings.Contains(pageCContent, expectedCiteHref) {
		t.Errorf("page-c.html missing expected cite href %s, got:\n%s", expectedCiteHref, pageCContent)
	}
	if !strings.Contains(pageCContent, `<sup class="cite">`) {
		t.Errorf("page-c.html missing <sup class=\"cite\">, got:\n%s", pageCContent)
	}
	if !strings.Contains(pageCContent, `^[raw/papers/p.md p.1]`) {
		t.Errorf("page-c.html missing literal citation marker inside fenced block, got:\n%s", pageCContent)
	}

	rawPHTML, err := os.ReadFile(filepath.Join(outDir, "raw", "papers", "p.html"))
	if err != nil {
		t.Fatalf("read p.html: %v", err)
	}
	rawPContent := string(rawPHTML)
	if !strings.Contains(rawPContent, `id="p2"`) {
		t.Errorf("raw p.html missing id=\"p2\", got:\n%s", rawPContent)
	}

	// H4: XSS check
	if strings.Contains(pageCContent, `<script>alert(1)</script>`) {
		t.Errorf("page-c.html contains unescaped <script> tag!")
	}
	if strings.Contains(pageCContent, `<h1><b>&" Page C</h1>`) || strings.Contains(pageCContent, `<title><b>&" Page C`) {
		t.Errorf("page-c.html contains unescaped title!")
	}
	if !strings.Contains(pageCContent, `&lt;b&gt;&amp;&#34; Page C`) && !strings.Contains(pageCContent, `&lt;b&gt;&amp;&quot; Page C`) {
		t.Errorf("page-c.html does not contain properly escaped title, got:\n%s", pageCContent)
	}

	// H5: backlinks & cited by
	otherHTML, err := os.ReadFile(filepath.Join(outDir, "wiki", "concepts", "other.html"))
	if err != nil {
		t.Fatalf("read other.html: %v", err)
	}
	otherContent := string(otherHTML)
	if !strings.Contains(otherContent, "Linked from") {
		t.Errorf("other.html missing 'Linked from' section")
	}
	if !strings.Contains(otherContent, "Page A") {
		t.Errorf("other.html missing link to 'Page A' in backlinks")
	}

	if !strings.Contains(rawPContent, "Cited by") {
		t.Errorf("raw p.html missing 'Cited by' section")
	}
	if !strings.Contains(rawPContent, "Page C") {
		t.Errorf("raw p.html missing link to citing page in Cited by")
	}

	// H8: search.js
	searchJSBytes, err := os.ReadFile(filepath.Join(outDir, "search.js"))
	if err != nil {
		t.Fatalf("read search.js: %v", err)
	}
	searchJS := string(searchJSBytes)
	if !strings.HasPrefix(searchJS, "window.LW_INDEX = ") {
		t.Errorf("search.js does not start with 'window.LW_INDEX = ', got:\n%s", searchJS)
	}
	if !strings.Contains(searchJS, "Page A") || !strings.Contains(searchJS, "Other Page") || !strings.Contains(searchJS, "Page C") {
		t.Errorf("search.js missing page titles, got:\n%s", searchJS)
	}
}

func TestHTMLExportRefusals(t *testing.T) {
	// H6: refusals
	vaultDir := createTestVault(t)
	v, err := vault.Open(vaultDir)
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}

	insideVaultDir := filepath.Join(vaultDir, "export-sub")
	if _, err := Export(v, insideVaultDir, Options{}); err == nil {
		t.Errorf("Export into vault subdirectory should fail, got nil err")
	}
	if _, err := os.Stat(insideVaultDir); !os.IsNotExist(err) {
		t.Errorf("refusal should write nothing into vault, but path exists")
	}

	nonEmptyDir := t.TempDir()
	keepFile := filepath.Join(nonEmptyDir, "keep.txt")
	if err := os.WriteFile(keepFile, []byte("preserve me"), 0644); err != nil {
		t.Fatalf("write keep.txt: %v", err)
	}

	if _, err := Export(v, nonEmptyDir, Options{Force: false}); err == nil {
		t.Errorf("Export into non-empty dir without Force should fail, got nil err")
	}

	if _, err := Export(v, nonEmptyDir, Options{Force: true}); err != nil {
		t.Errorf("Export into non-empty dir with Force should succeed, got %v", err)
	}

	data, err := os.ReadFile(keepFile)
	if err != nil {
		t.Errorf("read keep.txt after forced export: %v", err)
	} else if string(data) != "preserve me" {
		t.Errorf("keep.txt modified, got %q", string(data))
	}
}

func TestHTMLExportDeterminism(t *testing.T) {
	// H7: determinism
	vaultDir := createTestVault(t)
	v, err := vault.Open(vaultDir)
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}

	dir1 := t.TempDir()
	dir2 := t.TempDir()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

	if _, err := Export(v, dir1, Options{Now: now, Title: "Determinism Test"}); err != nil {
		t.Fatalf("Export dir1: %v", err)
	}
	if _, err := Export(v, dir2, Options{Now: now, Title: "Determinism Test"}); err != nil {
		t.Fatalf("Export dir2: %v", err)
	}

	var files1, files2 []string
	_ = filepath.Walk(dir1, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			rel, _ := filepath.Rel(dir1, p)
			files1 = append(files1, rel)
		}
		return nil
	})
	_ = filepath.Walk(dir2, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			rel, _ := filepath.Rel(dir2, p)
			files2 = append(files2, rel)
		}
		return nil
	})

	if len(files1) != len(files2) {
		t.Fatalf("file count mismatch: %d vs %d", len(files1), len(files2))
	}
	for i := range files1 {
		if files1[i] != files2[i] {
			t.Errorf("file name mismatch at %d: %s vs %s", i, files1[i], files2[i])
		}
		b1, _ := os.ReadFile(filepath.Join(dir1, files1[i]))
		b2, _ := os.ReadFile(filepath.Join(dir2, files2[i]))
		if !bytes.Equal(b1, b2) {
			t.Errorf("file content mismatch for %s", files1[i])
		}
	}
}
