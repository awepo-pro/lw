package main

// ingest_pdf_test.go pins 007 T4's wiring: the PDF sidecar reachable end to
// end through `lw ingest` (W1), the extended soft skips for walker-selected
// files (F.W4, W2–W4), the extraction cache behind the CLI chain (W5, red
// if the cache is not wired in), and the agent-path per-source cap (F.W3,
// W8). W6/W7 — the inverted 004 limit pins — live in ingest_dir_test.go's
// amended tests. Every PDF here runs against T1's hermetic fake sidecar
// (testdata/fake-docling, a POSIX sh script — skipped on Windows): a test
// that needs "not found" points extract.command at a nonexistent absolute
// path, so nothing depends on the real docling this machine happens to
// carry.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/testutil"
)

// fakeDoclingCommand returns the absolute path to T1's fake sidecar,
// skipping the test on Windows (contract, pins preamble — same rule as
// internal/extract/pdf_test.go's fakeDocling).
func fakeDoclingCommand(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("testdata/fake-docling is a POSIX sh script")
	}
	path, err := filepath.Abs("../../internal/extract/testdata/fake-docling")
	if err != nil {
		t.Fatalf("resolve testdata/fake-docling: %v", err)
	}
	return path
}

// pdfIngestEnv points config.Load at a scratch XDG dir whose [extract]
// command is command — the fake sidecar for the good cases, a nonexistent
// absolute path where the pin is "the sidecar is missing" — and, when
// contextTokens > 0, pins [llm.limits] context_tokens, the only knob both
// caps read (F.W2/F.W3). Never the user's real config.toml.
func pdfIngestEnv(t *testing.T, contextTokens int, command string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "xdg-config")
	t.Setenv("XDG_CONFIG_HOME", dir)
	toml := "[llm]\nmax_tokens = 8192\n\n[extract]\ncommand = \"" + command + "\"\n"
	if contextTokens > 0 {
		toml += fmt.Sprintf("\n[llm.limits]\ncontext_tokens = %d\n", contextTokens)
	}
	writeConfigFile(t, dir, toml)
}

// pdfSource writes a fake "PDF": content's leading directive lines
// (PAGES=, EXIT=, …) drive the fake sidecar, the rest is the markdown it
// converts — the same fixture shape internal/extract/pdf_test.go drives
// NewPDF with.
func pdfSource(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// paperFixture is a good-paper fixture: pages pages and 200 non-space
// characters per page — twice the F.P5 floor of 100, so the gate passes
// with room to spare.
func paperFixture(pages int) string {
	return fmt.Sprintf("PAGES=%d\n## A Paper Title\n\n%s\n", pages, strings.Repeat("x", 200*pages))
}

// TestIngestPDFBecomesOneChangeset pins W1: `lw ingest paper.pdf` converts
// through the sidecar wired into the CLI chain and lands as one changeset
// whose agent message lists the paper under Kind paper — NewPDF's own kind,
// untouched by any --kind override here.
func TestIngestPDFBecomesOneChangeset(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	pdfIngestEnv(t, 0, fakeDoclingCommand(t))
	paper := pdfSource(t, t.TempDir(), "paper.pdf", paperFixture(1))
	rec := withRecordingIngestAgent(t, oneIngestOp(), nil)

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--vault", root, paper})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	wantSourcesEqual(t, originalSources(t, rec.gotMsg), []string{paper})
	wantSourcesEqual(t, originalKinds(t, rec.gotMsg), []string{"paper"})
	if got := countChangesets(t, root, "open"); got != 1 {
		t.Errorf("open changesets = %d, want 1", got)
	}
}

// TestIngestFolderPDFTooLittleTextSkipped pins F.W4's newest arm (007
// correction #6): a walker-selected PDF refused by the F.P5 confidence gate
// is a soft skip with a reason — a folder of notes with one scanned PDF in
// it must still ingest the notes. a.md and the good paper p.pdf go through;
// scan.pdf (10 pages, ~50 characters) prints the exact skip line and is
// dropped.
func TestIngestFolderPDFTooLittleTextSkipped(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	pdfIngestEnv(t, 0, fakeDoclingCommand(t))
	dir := t.TempDir()
	a := dirFile(t, dir, "a.md", "# A\n\nAlpha.\n")
	p := pdfSource(t, dir, "p.pdf", paperFixture(1))
	scan := pdfSource(t, dir, "scan.pdf", "PAGES=10\n"+strings.Repeat("x", 50)+"\n")
	rec := withRecordingIngestAgent(t, oneIngestOp(), nil)

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--vault", root, dir})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	want := "skipped " + scan + ": too little text (scanned or image-only PDF?)"
	if !strings.Contains(stdout, want) {
		t.Fatalf("stdout = %q, want it to contain %q", stdout, want)
	}
	wantSourcesEqual(t, originalSources(t, rec.gotMsg), []string{a, p})
	if got := countChangesets(t, root, "open"); got != 1 {
		t.Errorf("open changesets = %d, want 1", got)
	}
}

// TestIngestExplicitScannedPDFFails pins F.W4's hard half for the new arms:
// the same scanned PDF handed over as an EXPLICIT argument fails the
// command with the gate's verdict — silence would hide why the command did
// nothing — and nothing is opened.
func TestIngestExplicitScannedPDFFails(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	pdfIngestEnv(t, 0, fakeDoclingCommand(t))
	scan := pdfSource(t, t.TempDir(), "scan.pdf", "PAGES=10\n"+strings.Repeat("x", 50)+"\n")
	noAgentEver(t)

	_, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--vault", root, scan})
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "too little text") {
		t.Fatalf("stderr = %q, want it to carry the too-little-text verdict", stderr)
	}
	if !strings.Contains(stderr, scan) {
		t.Fatalf("stderr = %q, want it to name %q", stderr, scan)
	}
	e := openEngine(t, root)
	if _, err := e.Current(); err == nil {
		t.Errorf("Current = nil error, want ErrNoChangeset (nothing opened)")
	}
}

// TestIngestPDFWithoutSidecar pins W4: with extract.command pointing at a
// nonexistent absolute path, a folder's PDFs soft-skip with the doctor
// pointer while the notes ingest (F.W4's ErrSidecarMissing arm), and an
// explicit PDF fails hard with F.P3's install hint.
func TestIngestPDFWithoutSidecar(t *testing.T) {
	t.Run("folder skips the pdf and ingests the notes", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		pdfIngestEnv(t, 0, "/nonexistent/docling")
		dir := t.TempDir()
		a := dirFile(t, dir, "a.md", "# A\n\nAlpha.\n")
		p := pdfSource(t, dir, "p.pdf", paperFixture(1))
		rec := withRecordingIngestAgent(t, oneIngestOp(), nil)

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, dir})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		want := "skipped " + p + ": no PDF sidecar (see lw doctor)"
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, want it to contain %q", stdout, want)
		}
		wantSourcesEqual(t, originalSources(t, rec.gotMsg), []string{a})
		if got := countChangesets(t, root, "open"); got != 1 {
			t.Errorf("open changesets = %d, want 1", got)
		}
	})

	t.Run("explicit pdf fails with the install hint", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		pdfIngestEnv(t, 0, "/nonexistent/docling")
		p := pdfSource(t, t.TempDir(), "p.pdf", paperFixture(1))
		noAgentEver(t)

		_, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, p})
		})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1; stderr=%q", code, stderr)
		}
		if !strings.Contains(stderr, "PDF sidecar not found") {
			t.Fatalf("stderr = %q, want it to carry the missing-sidecar verdict", stderr)
		}
		if !strings.Contains(stderr, "uv tool install docling==2.130.0") {
			t.Fatalf("stderr = %q, want it to carry the F.P3 install hint", stderr)
		}
	})
}

// TestIngestPDFDryRunCachesForRealRun pins W5: --dry-run runs extraction —
// a PDF is really converted, and the conversion lands in the vault's
// extraction cache — so the real ingest of the same paper right after is a
// cache hit and the sidecar runs its convert exactly ONCE across both
// invocations. RED if the cache is not wired into the CLI chain: without it
// the second run converts again and the log carries two convert lines.
func TestIngestPDFDryRunCachesForRealRun(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	pdfIngestEnv(t, 0, fakeDoclingCommand(t))
	argvPath := filepath.Join(t.TempDir(), "argv.log")
	t.Setenv("FAKE_DOCLING_ARGV", argvPath) // the fake appends its argv there
	paper := pdfSource(t, t.TempDir(), "paper.pdf", paperFixture(1))

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--vault", root, "--dry-run", paper})
	})
	if code != 0 {
		t.Fatalf("dry-run: exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	if want := "would ingest " + paper; !strings.Contains(stdout, want) {
		t.Fatalf("dry-run stdout = %q, want it to contain %q", stdout, want)
	}
	if want := "within limits: 1 files, 1 KB (limit 10 files, 282 KB)"; !strings.Contains(stdout, want) {
		t.Fatalf("dry-run stdout = %q, want it to contain the F.W5 verdict", stdout)
	}
	noChangesetsAnywhere(t, root)

	rec := withRecordingIngestAgent(t, oneIngestOp(), nil)
	stdout, stderr, code = captureRun(t, func() int {
		return run([]string{"ingest", "--vault", root, paper})
	})
	if code != 0 {
		t.Fatalf("ingest: exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	wantSourcesEqual(t, originalSources(t, rec.gotMsg), []string{paper})

	b, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatalf("read argv log: %v", err)
	}
	converts := 0
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if line == "convert" {
			converts++
		}
	}
	if converts != 1 {
		t.Errorf("the sidecar ran convert %d times across dry-run + ingest, want exactly 1 (the second run must be a cache hit)\nargv log:\n%s", converts, b)
	}
}

// TestPDFVersionProbeBounded pins the cache's sidecar --version probe
// bound: a sidecar whose --version wedges (a broken install can hang the
// Python import) must not hang the ingest. The probe gives up, the error
// is cached for the process, and the extraction proceeds uncached — the
// ingest still lands. RED if the bound is dropped: the probe waits out the
// fake's whole sleep and the elapsed assertion fires.
func TestPDFVersionProbeBounded(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	pdfIngestEnv(t, 0, fakeDoclingCommand(t))
	t.Setenv("FAKE_DOCLING_VERSION_SLEEP", "30")
	oldBound := pdfVersionProbeTimeout
	pdfVersionProbeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { pdfVersionProbeTimeout = oldBound })

	paper := pdfSource(t, t.TempDir(), "paper.pdf", paperFixture(1))
	rec := withRecordingIngestAgent(t, oneIngestOp(), nil)

	start := time.Now()
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--vault", root, paper})
	})
	elapsed := time.Since(start)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (the wedged probe must degrade to an uncached extraction); stderr=%q stdout=%q", code, stderr, stdout)
	}
	wantSourcesEqual(t, originalSources(t, rec.gotMsg), []string{paper})
	if elapsed > 10*time.Second {
		t.Errorf("ingest took %s with a wedged --version (probe bound %s); the probe must give up, not hang the command", elapsed, oldBound)
	}
}

// TestAgentPathPerSourceCap pins W8/F.W3: agentExtractors(root, cfg) wraps
// the chain so a single extracted Doc over the per-source byte cap is an
// error — never staged, the text frozen — and exactly-at-cap passes. The
// cap is context_tokens × 4 × 75%: 40 tokens ⇒ 120 bytes. stage.ingest_source
// already turns an extract error into an IsError result (stage_source.go),
// so this one wrapper is the whole agent-path rule.
func TestAgentPathPerSourceCap(t *testing.T) {
	pdfIngestEnv(t, 40, fakeDoclingCommand(t))
	cfg := loadedConfig(t)
	root := t.TempDir() // the cache dir root; entries here are throwaway
	ex := agentExtractors(root, cfg)

	t.Run("over_the_cap_is_an_error", func(t *testing.T) {
		over := writtenSource(t, "over.md", strings.Repeat("a", 120)+"\n") // 121 extracted bytes
		_, err := ex.Extract(context.Background(), over)
		if err == nil {
			t.Fatal("Extract over the cap = nil error, want the F.W3 refusal")
		}
		want := fmt.Sprintf("extract: %s: extracted 1 KB, over the 1 KB per-source limit (llm.limits.context_tokens 40 × 4 × 75%%)", over)
		if err.Error() != want {
			t.Errorf("error =\n%q\nwant\n%q", err.Error(), want)
		}
	})

	t.Run("exactly_at_the_cap_is_a_doc", func(t *testing.T) {
		at := writtenSource(t, "at.md", strings.Repeat("a", 119)+"\n") // 120 extracted bytes
		doc, err := ex.Extract(context.Background(), at)
		if err != nil {
			t.Fatalf("Extract at the cap: %v", err)
		}
		if len(doc.Markdown) != 120 {
			t.Errorf("markdown = %d bytes, want 120 (exactly at the cap)", len(doc.Markdown))
		}
	})
}
