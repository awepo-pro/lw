package extract

// pdf_test.go pins 007 T1's NewPDF to the frozen contract (F.P1–F.P7, pins
// PDF1–PDF11). Every test drives the fake sidecar testdata/fake-docling —
// a POSIX sh script mimicking the measured Docling 2.130.0 boundary — so
// the suite needs no Python, no Docling install and no network, and runs
// hermetically. Skipped on Windows, where the fake cannot run.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode"
)

// fakeDocling returns a PDFConfig pointed at the fake sidecar, skipping
// the test on Windows — the fake is a POSIX sh script (contract, pins
// preamble).
func fakeDocling(t *testing.T) PDFConfig {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("testdata/fake-docling is a POSIX sh script")
	}
	path, err := filepath.Abs("testdata/fake-docling")
	if err != nil {
		t.Fatalf("resolve testdata/fake-docling: %v", err)
	}
	return PDFConfig{Command: []string{path}, Timeout: 10 * time.Second}
}

// pdfFixture writes a fake "PDF": content's leading PAGES=/EXIT=/… lines
// are the fake's directives, the rest is the markdown it converts.
func pdfFixture(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// padTo appends the non-space filler "x" to s until it holds exactly n
// non-space runes — PDF3's 199/200 boundary is only a pin if the character
// count is exact, and the gate counts non-space runes, not bytes.
func padTo(s string, n int) string {
	count := 0
	for _, r := range s {
		if !unicode.IsSpace(r) {
			count++
		}
	}
	if count > n {
		panic(fmt.Sprintf("padTo: %q already has %d non-space runes, want at most %d", s, count, n))
	}
	return s + strings.Repeat("x", n-count)
}

// TestPDFDocShape is pin PDF1: a 2-page fixture of exactly 400 non-space
// characters (over the 100-per-page floor) yields the Doc of F.P6 — Title
// from the first ATX heading of any level (Docling titles are "## ", never
// "# "), the fileExtractor newline rule on Markdown, Kind "paper",
// Extractor sidecar/docling, SourceURL the uri as given. The \r\n and
// lone-\r cases pin the normalization half of F.P6 too.
func TestPDFDocShape(t *testing.T) {
	cfg := fakeDocling(t)
	title := "How Sparse Attention Approximates Exact Attention? Your Attention is Naturally nC -Sparse"

	t.Run("title from a level-2 heading", func(t *testing.T) {
		md := padTo("## "+title+"\n\n", 400) + "\n"
		uri := pdfFixture(t, t.TempDir(), "paper.pdf", "PAGES=2\n"+md)

		doc, err := NewPDF(cfg).Extract(context.Background(), uri)
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		if doc.Title != title {
			t.Errorf("Title = %q, want %q", doc.Title, title)
		}
		if doc.SourceURL != uri {
			t.Errorf("SourceURL = %q, want %q", doc.SourceURL, uri)
		}
		if doc.Kind != "paper" {
			t.Errorf("Kind = %q, want %q", doc.Kind, "paper")
		}
		if doc.Extractor != PDFExtractorID {
			t.Errorf("Extractor = %q, want %q", doc.Extractor, PDFExtractorID)
		}
		// The fileExtractor rule (markdown.go): CRLF/CR collapsed to \n,
		// trailing newlines trimmed, exactly one appended.
		if want := strings.TrimRight(md, "\n") + "\n"; doc.Markdown != want {
			t.Errorf("Markdown = %q, want %q", doc.Markdown, want)
		}
	})

	t.Run("crlf normalized", func(t *testing.T) {
		// padTo counts the heading's own 3 non-space runes, so the body
		// filler is 147 — 150 over the single page's floor of 100.
		uri := pdfFixture(t, t.TempDir(), "crlf.pdf", "PAGES=1\n"+padTo("## T\r\n", 150)+"\r\n")
		doc, err := NewPDF(cfg).Extract(context.Background(), uri)
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		if want := "## T\n" + strings.Repeat("x", 147) + "\n"; doc.Markdown != want {
			t.Errorf("Markdown = %q, want %q", doc.Markdown, want)
		}
	})

	t.Run("lone cr normalized", func(t *testing.T) {
		uri := pdfFixture(t, t.TempDir(), "cr.pdf", "PAGES=1\n"+padTo("## T\r", 150)+"\r")
		doc, err := NewPDF(cfg).Extract(context.Background(), uri)
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		if want := "## T\n" + strings.Repeat("x", 147) + "\n"; doc.Markdown != want {
			t.Errorf("Markdown = %q, want %q", doc.Markdown, want)
		}
	})
}

// TestPDFArgv is pin PDF2: the exact F.P2 argv, with the temp dir matched
// by its lw-pdf- prefix. The RED-if mutations drop --no-ocr or
// --image-export-mode placeholder — the flags that keep a 100 KB paper
// from becoming 2.1 MB of embedded images and keep a scanned book from
// being silently OCR'd (probe §2.2 #4).
func TestPDFArgv(t *testing.T) {
	cfg := fakeDocling(t)
	dir := t.TempDir()
	argvPath := filepath.Join(dir, "argv.log")
	t.Setenv("FAKE_DOCLING_ARGV", argvPath)
	uri := pdfFixture(t, dir, "paper.pdf", "PAGES=1\n"+padTo("", 150)+"\n")

	if _, err := NewPDF(cfg).Extract(context.Background(), uri); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	b, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatalf("read argv log: %v", err)
	}
	args := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	want := []string{
		"convert", uri,
		"--to", "md",
		"--to", "json",
		"--no-ocr",
		"--image-export-mode", "placeholder",
	}
	if len(args) != len(want)+2 {
		t.Fatalf("argv = %q, want %q followed by --output lw-pdf-*", args, want)
	}
	for i, w := range want {
		if args[i] != w {
			t.Errorf("argv[%d] = %q, want %q", i, args[i], w)
		}
	}
	if args[len(want)] != "--output" {
		t.Errorf("argv[%d] = %q, want --output", len(want), args[len(want)])
	}
	if tmp := args[len(want)+1]; !strings.HasPrefix(filepath.Base(tmp), "lw-pdf-") {
		t.Errorf("output dir = %q, want an lw-pdf- prefix", tmp)
	}
}

// TestPDFTooLittleTextGate is pin PDF3: the F.P5 floor is
// MinPDFCharsPerPage × pages, with the exact refusal message. 199
// non-space characters over 2 pages is one short of 2 × 100 and must be
// refused; 200 must extract. RED if the comparison is <= or if pages are
// not multiplied in — either way the 199 case silently extracts.
func TestPDFTooLittleTextGate(t *testing.T) {
	cfg := fakeDocling(t)
	dir := t.TempDir()

	scanned := pdfFixture(t, dir, "scanned.pdf", "PAGES=2\n"+strings.Repeat("x", 199)+"\n")
	_, err := NewPDF(cfg).Extract(context.Background(), scanned)
	if err == nil {
		t.Fatal("199 chars over 2 pages: Extract error = nil, want ErrTooLittleText")
	}
	if !errors.Is(err, ErrTooLittleText) {
		t.Fatalf("error %v does not wrap ErrTooLittleText", err)
	}
	want := fmt.Sprintf("extract: %s: too little text: 199 characters over 2 pages (floor 100 per page) — likely a scanned or image-only PDF; OCR is not supported", scanned)
	if err.Error() != want {
		t.Errorf("error =\n%q\nwant\n%q", err.Error(), want)
	}

	atFloor := pdfFixture(t, dir, "at-floor.pdf", "PAGES=2\n"+strings.Repeat("x", 200)+"\n")
	if _, err := NewPDF(cfg).Extract(context.Background(), atFloor); err != nil {
		t.Errorf("200 chars over 2 pages: Extract error = %v, want a Doc", err)
	}
}

// TestPDFSidecarMissing is pin PDF4: LookPath against a command that
// cannot exist fails with the exact F.P3 install-hint message, wrapping
// ErrSidecarMissing.
func TestPDFSidecarMissing(t *testing.T) {
	uri := pdfFixture(t, t.TempDir(), "paper.pdf", "PAGES=1\nhello\n")
	cfg := PDFConfig{Command: []string{"/nonexistent/docling"}}

	_, err := NewPDF(cfg).Extract(context.Background(), uri)
	if err == nil {
		t.Fatal("Extract with a missing sidecar error = nil, want ErrSidecarMissing")
	}
	if !errors.Is(err, ErrSidecarMissing) {
		t.Fatalf("error %v does not wrap ErrSidecarMissing", err)
	}
	want := fmt.Sprintf("extract: %s: PDF sidecar not found: /nonexistent/docling is not on PATH — install it with %q, or set extract.command (lw doctor shows what is missing)", uri, "uv tool install docling==2.130.0")
	if err.Error() != want {
		t.Errorf("error =\n%q\nwant\n%q", err.Error(), want)
	}
}

// TestPDFExitStatus is pin PDF5: a non-zero sidecar exit reports the exit
// code and the fake's stderr tail — the only place a real failure's cause
// is visible, since stdout is empty and the markdown never materializes.
func TestPDFExitStatus(t *testing.T) {
	cfg := fakeDocling(t)
	uri := pdfFixture(t, t.TempDir(), "paper.pdf", "PAGES=1\nEXIT=3\n")

	doc, err := NewPDF(cfg).Extract(context.Background(), uri)
	if err == nil {
		t.Fatalf("EXIT=3: Extract = %+v, want an error", doc)
	}
	want := fmt.Sprintf("extract: %s: docling failed (exit 3): fake-docling: conversion failed catastrophically", uri)
	if err.Error() != want {
		t.Errorf("error =\n%q\nwant\n%q", err.Error(), want)
	}
}

// TestPDFStderrTail pins F.P2's "last 2 048 bytes" on the buffer that
// delivers it: a real conversion floods stderr with progress lines, so the
// buffer must slide — keep the tail, not the head — or a long run's error
// would quote the earliest chatter and drop the traceback naming the cause.
func TestPDFStderrTail(t *testing.T) {
	b := &tailBuffer{cap: 8}
	b.Write([]byte("12345"))
	b.Write([]byte("67890"))
	if got, want := b.String(), "34567890"; got != want {
		t.Errorf(`"12345"+"67890" into a cap-8 buffer = %q, want %q`, got, want)
	}
	b.Write([]byte("abcdefghij")) // one write larger than the whole buffer
	if got, want := b.String(), "cdefghij"; got != want {
		t.Errorf(`"abcdefghij" into a cap-8 buffer = %q, want %q`, got, want)
	}
}

// TestPDFTimeout is pin PDF6: a stalled sidecar is cut off at
// PDFConfig.Timeout with the exact F.P3 message, in well under the 5 s the
// fake would sleep — a wall-clock bound, because a hung conversion that
// eventually finishes is exactly the failure this pin exists for.
func TestPDFTimeout(t *testing.T) {
	cfg := fakeDocling(t)
	cfg.Timeout = 200 * time.Millisecond
	uri := pdfFixture(t, t.TempDir(), "paper.pdf", "PAGES=1\nSLEEP=5\n")

	start := time.Now()
	doc, err := NewPDF(cfg).Extract(context.Background(), uri)
	if err == nil {
		t.Fatalf("SLEEP=5: Extract = %+v, want a timeout error", doc)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Extract returned after %s, want well under the fake's 5s sleep", elapsed)
	}
	want := fmt.Sprintf("extract: %s: docling timed out after 200ms", uri)
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

// TestPDFNoMarkdown is pin PDF7: NOMD (the sidecar exited 0 but wrote no
// markdown) is refused with the exact F.P3 message. The NOJSON sibling and
// the zero-pages case complete F.P3's error list.
func TestPDFNoMarkdown(t *testing.T) {
	cfg := fakeDocling(t)

	t.Run("no markdown", func(t *testing.T) {
		uri := pdfFixture(t, t.TempDir(), "paper.pdf", "PAGES=1\nNOMD\n")
		doc, err := NewPDF(cfg).Extract(context.Background(), uri)
		if err == nil {
			t.Fatalf("NOMD: Extract = %+v, want an error", doc)
		}
		want := fmt.Sprintf("extract: %s: docling wrote no markdown", uri)
		if err.Error() != want {
			t.Errorf("error = %q, want %q", err.Error(), want)
		}
	})

	t.Run("no json", func(t *testing.T) {
		uri := pdfFixture(t, t.TempDir(), "paper.pdf", "PAGES=1\nNOJSON\n"+padTo("", 150)+"\n")
		doc, err := NewPDF(cfg).Extract(context.Background(), uri)
		if err == nil {
			t.Fatalf("NOJSON: Extract = %+v, want an error", doc)
		}
		want := fmt.Sprintf("extract: %s: docling wrote no json", uri)
		if err.Error() != want {
			t.Errorf("error = %q, want %q", err.Error(), want)
		}
	})

	t.Run("no pages", func(t *testing.T) {
		uri := pdfFixture(t, t.TempDir(), "paper.pdf", "PAGES=0\n"+padTo("", 150)+"\n")
		doc, err := NewPDF(cfg).Extract(context.Background(), uri)
		if err == nil {
			t.Fatalf("PAGES=0: Extract = %+v, want an error", doc)
		}
		want := fmt.Sprintf("extract: %s: docling reported no pages", uri)
		if err.Error() != want {
			t.Errorf("error = %q, want %q", err.Error(), want)
		}
	})
}

// TestPDFPagesStreamed is pin PDF8: the pages object is the last key after
// a 1 MiB texts array — the shape of a real book's DoclingDocument, which
// F.P4 forbids unmarshalling whole. The end-to-end leg can only see the
// count through the gate (750 characters pass a 7-page floor, fail an
// 8-page one), so the count itself is pinned directly on countJSONPages.
func TestPDFPagesStreamed(t *testing.T) {
	cfg := fakeDocling(t)
	uri := pdfFixture(t, t.TempDir(), "book.pdf", "PAGES=7\nTEXTS=1048576\n"+padTo("", 750)+"\n")
	if _, err := NewPDF(cfg).Extract(context.Background(), uri); err != nil {
		t.Fatalf("Extract over a 1 MiB texts array: %v", err)
	}

	// countJSONPages driven directly on the same shape the fake emits:
	// 7 page members after a 1 MiB texts array.
	docJSON := `{"schema_name":"DoclingDocument","texts":[` +
		`{"text":"` + strings.Repeat("x", 1<<20) + `"}],` +
		`"pages":{"1":{},"2":{},"3":{},"4":{},"5":{},"6":{},"7":{}}}`
	n, err := countJSONPages(strings.NewReader(docJSON))
	if err != nil {
		t.Fatalf("countJSONPages: %v", err)
	}
	if n != 7 {
		t.Errorf("countJSONPages = %d, want 7", n)
	}

	// Malformed and truncated sidecar output must surface as an error
	// (wrapped as "read docling json" by Extract), never as a silent
	// count — a half-written JSON that read as 0 pages would masquerade
	// as "docling reported no pages" instead of the real defect.
	t.Run("malformed and truncated json are errors", func(t *testing.T) {
		for _, bad := range []string{"", `not json`, `[1,2]`, `{"pages":null}`, `{"pages":{"1":{`} {
			if n, err := countJSONPages(strings.NewReader(bad)); err == nil {
				t.Errorf("countJSONPages(%q) = %d, nil error, want an error", bad, n)
			}
		}
	})
}

// TestPDFTempDirRemoved is pin PDF9: the F.P2 temp dir is removed on every
// path — success, the F.P5 gate refusal, and a failed exit. TMPDIR is
// pointed at a test-owned directory so lw-pdf-* globs cannot race the
// machine's real temp traffic.
func TestPDFTempDirRemoved(t *testing.T) {
	cfg := fakeDocling(t)
	tmpRoot := t.TempDir()
	t.Setenv("TMPDIR", tmpRoot)
	leftovers := func(when string) {
		t.Helper()
		matches, err := filepath.Glob(filepath.Join(tmpRoot, "lw-pdf-*"))
		if err != nil {
			t.Fatalf("glob lw-pdf-*: %v", err)
		}
		if len(matches) != 0 {
			t.Errorf("%s: lw-pdf-* left behind: %v", when, matches)
		}
	}

	ok := pdfFixture(t, tmpRoot, "ok.pdf", "PAGES=1\n"+padTo("", 150)+"\n")
	if _, err := NewPDF(cfg).Extract(context.Background(), ok); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	leftovers("after success")

	scanned := pdfFixture(t, tmpRoot, "scanned.pdf", "PAGES=2\nscant\n")
	if _, err := NewPDF(cfg).Extract(context.Background(), scanned); !errors.Is(err, ErrTooLittleText) {
		t.Fatalf("scanned Extract error = %v, want ErrTooLittleText", err)
	}
	leftovers("after the gate refusal")

	dead := pdfFixture(t, tmpRoot, "dead.pdf", "PAGES=1\nEXIT=3\n")
	if _, err := NewPDF(cfg).Extract(context.Background(), dead); err == nil {
		t.Fatal("EXIT=3 Extract error = nil, want an error")
	}
	leftovers("after a failed exit")
}

// TestPDFCanHandle is pin PDF10's half of F.P1: exactly the .pdf
// extension, case-insensitive, never a URL — and never a read or stat, so
// a.pdf answers true without existing.
func TestPDFCanHandle(t *testing.T) {
	p := NewPDF(fakeDocling(t))
	for uri, want := range map[string]bool{
		"a.pdf":           true,
		"A.PDF":           true,
		"a.pdf.md":        false,
		"https://x/a.pdf": false,
		"a.txt":           false,
	} {
		if got := p.CanHandle(uri); got != want {
			t.Errorf("CanHandle(%q) = %v, want %v", uri, got, want)
		}
	}
}

// TestPDFVersion is pin PDF10's other half, plus F.P7: the version comes
// from the first `--version` line's "Docling version: " prefix, and a
// missing sidecar surfaces ErrSidecarMissing exactly as Extract's does.
func TestPDFVersion(t *testing.T) {
	v, err := PDFVersion(context.Background(), fakeDocling(t))
	if err != nil {
		t.Fatalf("PDFVersion: %v", err)
	}
	if v != "2.130.0" {
		t.Errorf("PDFVersion = %q, want %q", v, "2.130.0")
	}

	_, err = PDFVersion(context.Background(), PDFConfig{Command: []string{"/nonexistent/docling"}})
	if !errors.Is(err, ErrSidecarMissing) {
		t.Errorf("PDFVersion with a missing sidecar error = %v, want ErrSidecarMissing", err)
	}
}

// TestPDFWalkRegistry is pin PDF11: NewPDF drops into Walk's
// extractor-registry derivation from 004 unchanged — walk.go is stat-only
// and selects by CanHandle, so b.pdf is selected and c.png is skipped with
// the registry's own reason string. Zero changes to walk.go.
func TestPDFWalkRegistry(t *testing.T) {
	cfg := fakeDocling(t)
	dir := t.TempDir()
	pdfFixture(t, dir, "a.md", "# A\n")
	pdfFixture(t, dir, "b.pdf", "PAGES=1\n"+padTo("", 150)+"\n")
	pdfFixture(t, dir, "c.png", "\x89PNG\r\n\x1a\n")

	files, skipped, err := Walk(dir, Chain(NewFile(), NewPDF(cfg)))
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	wantFiles := []string{filepath.Join(dir, "a.md"), filepath.Join(dir, "b.pdf")}
	if len(files) != len(wantFiles) {
		t.Fatalf("files = %v, want %v", files, wantFiles)
	}
	for i := range wantFiles {
		if files[i] != wantFiles[i] {
			t.Errorf("files[%d] = %q, want %q", i, files[i], wantFiles[i])
		}
	}
	wantSkipped := []Skipped{{filepath.Join(dir, "c.png"), "unsupported type"}}
	if len(skipped) != len(wantSkipped) {
		t.Fatalf("skipped = %v, want %v", skipped, wantSkipped)
	}
	for i := range wantSkipped {
		if skipped[i] != wantSkipped[i] {
			t.Errorf("skipped[%d] = %+v, want %+v", i, skipped[i], wantSkipped[i])
		}
	}
}
