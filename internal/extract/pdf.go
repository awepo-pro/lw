package extract

// pdf.go implements 007 T1's NewPDF: the sidecar leg of `lw ingest`,
// backed by Docling. The backend deliberately links no PDF library —
// Docling is a Python program with its own model weights — and instead
// shells out to the `docling` CLI, reads the two files it leaves in a
// private temp dir, and refuses the result unless it clears a confidence
// floor (F.P5). Everything at the boundary mimics the measured sidecar
// (Docling 2.130.0, 2026-09-23): stdout is empty, progress goes to stderr,
// and a scanned PDF still exits 0 — so the scanned-PDF refusal is this
// code's job, never the sidecar's.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// PDFConfig configures NewPDF and PDFVersion.
type PDFConfig struct {
	// Command is the sidecar's argv prefix: the executable and any leading
	// arguments, e.g. {"docling"} or {"uvx", "--from", "docling==2.130.0", "docling"}.
	// Empty means {"docling"}.
	Command []string
	// Timeout bounds one conversion; <= 0 means DefaultPDFTimeout.
	Timeout time.Duration
}

// DefaultPDFTimeout is the conversion budget when PDFConfig.Timeout is
// unset: at the probe's measured ~0.7 s/page plus up to 30 s cold model
// load, 300 s covers a ~380-page book — 120 s would not (007 correction
// log #4: the config key is a duration string, so callers can widen it).
const DefaultPDFTimeout = 300 * time.Second

// MinPDFCharsPerPage is the confidence floor: an extraction averaging fewer
// non-space characters per page is refused as scanned or image-only.
const MinPDFCharsPerPage = 100

// PDFExtractorID is the Doc.Extractor value, and the cache's extractor id.
const PDFExtractorID = "sidecar/docling"

var (
	ErrSidecarMissing = errors.New("PDF sidecar not found")
	ErrTooLittleText  = errors.New("too little text")
)

// installHint is the second half of the ErrSidecarMissing message (F.P3):
// the fix is part of the error because "not found" alone is not actionable
// — the tool lives outside Go's module system, under a version pin.
const installHint = `is not on PATH — install it with "uv tool install docling==2.130.0", or set extract.command (lw doctor shows what is missing)`

// pdfTitleRe is F.P6's title rule, verbatim: the first ATX heading of any
// level — Docling titles a paper with "## ", never "# ", so firstATXH1's
// level-1-only scan would leave every PDF untitled — with any closing run
// of hashes and its preceding spaces stripped, then trimmed. (?m) makes ^
// and $ line anchors, so the scan is "first heading line in the document".
var pdfTitleRe = regexp.MustCompile(`(?m)^#{1,6} +(.*?) *#*$`)

// stderrTailBytes is how much of the sidecar's stderr an error keeps
// (F.P2: last 2 048 bytes) — the tail carries the final Python traceback,
// which is the part that says why the conversion died.
const stderrTailBytes = 2048

// pdfExtractor is the Extractor NewPDF returns.
type pdfExtractor struct {
	command []string
	timeout time.Duration
}

// NewPDF returns an Extractor for local .pdf files, converted by the
// Docling sidecar described by cfg.
func NewPDF(cfg PDFConfig) Extractor {
	return &pdfExtractor{
		command: commandOf(cfg),
		timeout: timeoutOf(cfg),
	}
}

// commandOf returns cfg's argv prefix, defaulting to {"docling"} when
// empty (PDFConfig.Command).
func commandOf(cfg PDFConfig) []string {
	if len(cfg.Command) == 0 {
		return []string{"docling"}
	}
	return cfg.Command
}

// timeoutOf returns cfg's conversion budget, defaulting to
// DefaultPDFTimeout when unset or non-positive (PDFConfig.Timeout).
func timeoutOf(cfg PDFConfig) time.Duration {
	if cfg.Timeout <= 0 {
		return DefaultPDFTimeout
	}
	return cfg.Timeout
}

// sidecarMissing builds the ErrSidecarMissing error shared by Extract and
// PDFVersion (F.P3/F.P7): LookPath failed for the sidecar's executable.
func sidecarMissing(command []string) error {
	return fmt.Errorf("%w: %s %s", ErrSidecarMissing, command[0], installHint)
}

// CanHandle reports whether uri is a local path (not http/https) with a
// .pdf extension, case-insensitively. It is deliberately a string test and
// never reads or stats the file (F.P1): Walk calls it once per file, and
// whether the bytes really are a PDF surfaces when Docling itself fails on
// them, with its stderr in the error.
func (p *pdfExtractor) CanHandle(uri string) bool {
	if isRemoteURL(uri) {
		return false
	}
	return strings.ToLower(filepath.Ext(uri)) == ".pdf"
}

// Extract converts uri with the sidecar and returns it as a Doc. The
// conversion's output is files in a fresh temp dir (the sidecar writes
// nothing to stdout — 007 correction log #5), so the happy path is: run,
// find the two files, stream the page count out of the JSON, hold the
// markdown to the F.P5 floor, shape the Doc.
func (p *pdfExtractor) Extract(ctx context.Context, uri string) (*Doc, error) {
	// F.P2: LookPath first, so a missing sidecar is reported before any
	// temp dir is created, with the install hint (PDF4).
	if _, err := exec.LookPath(p.command[0]); err != nil {
		return nil, fmt.Errorf("extract: %s: %w", uri, sidecarMissing(p.command))
	}

	// F.P2: the conversion lands in a private temp dir, removed before
	// Extract returns on every path below — a book's JSON is hundreds of
	// MB, and leaving one behind per refused PDF would fill the disk.
	tmp, err := os.MkdirTemp("", "lw-pdf-*")
	if err != nil {
		return nil, fmt.Errorf("extract: %s: create temp dir: %w", uri, err)
	}
	defer os.RemoveAll(tmp)

	// F.P2: the conversion runs under context.WithTimeout(ctx, Timeout).
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	stderr := &tailBuffer{cap: stderrTailBytes}
	argv := append(append([]string{}, p.command...),
		"convert", uri,
		"--to", "md",
		"--to", "json",
		// Probe §2.2 #4: without --image-export-mode placeholder a 100 KB
		// paper becomes 2.1 MB of embedded images; without --no-ocr a
		// scanned book is silently OCR'd — slow, lossy, and exactly what
		// F.P5 exists to refuse instead.
		"--no-ocr",
		"--image-export-mode", "placeholder",
		"--output", tmp,
	)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// Stdin empty and stdout discarded (the sidecar writes files, not
	// stdout); stderr is kept for the error messages below.
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	cmd.Stderr = stderr
	// The sidecar's children (a model loader, a sleep in a test fake) can
	// outlive it holding our stderr pipe, and Wait would block on them
	// past the deadline; WaitDelay bounds that to a grace period.
	cmd.WaitDelay = time.Second

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("extract: %s: start docling: %w", uri, err)
	}
	if err := cmd.Wait(); err != nil {
		// F.P3: a deadline is reported as a timeout, ahead of whatever
		// exit status the killed process left behind.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("extract: %s: docling timed out after %s", uri, p.timeout)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("extract: %s: docling failed (exit %d): %s",
				uri, exitErr.ExitCode(), strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("extract: %s: run docling: %w", uri, err)
	}

	mds, err := filepath.Glob(filepath.Join(tmp, "*.md"))
	if err != nil {
		return nil, fmt.Errorf("extract: %s: list docling output: %w", uri, err)
	}
	jsons, err := filepath.Glob(filepath.Join(tmp, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("extract: %s: list docling output: %w", uri, err)
	}
	// Exactly one of each: a second would mean the stem collided, and
	// picking one would make the Doc depend on glob order.
	if len(mds) != 1 {
		return nil, fmt.Errorf("extract: %s: docling wrote no markdown", uri)
	}
	if len(jsons) != 1 {
		return nil, fmt.Errorf("extract: %s: docling wrote no json", uri)
	}

	b, err := os.ReadFile(mds[0])
	if err != nil {
		return nil, fmt.Errorf("extract: %s: read docling markdown: %w", uri, err)
	}
	// The fileExtractor rule (F.P6): normalize line endings, trim trailing
	// newlines, append exactly one.
	body := normalizeNewlines(string(b))
	body = strings.TrimRight(body, "\n")
	if body != "" {
		body += "\n"
	}

	pages, err := countJSONFile(jsons[0])
	if err != nil {
		return nil, fmt.Errorf("extract: %s: read docling json: %w", uri, err)
	}
	if pages == 0 {
		return nil, fmt.Errorf("extract: %s: docling reported no pages", uri)
	}

	// F.P5, the confidence gate: a scanned or image-only PDF still exits 0
	// with a near-empty .md, so the refusal can only happen here. The
	// probe measured 1 non-space character per page on a scanned book
	// against 1 843–4 038 on real papers; 100 sits far below the latter.
	n := 0
	for _, r := range body {
		if !unicode.IsSpace(r) {
			n++
		}
	}
	if n < MinPDFCharsPerPage*pages {
		return nil, fmt.Errorf("extract: %s: %w: %d characters over %d pages (floor %d per page) — likely a scanned or image-only PDF; OCR is not supported",
			uri, ErrTooLittleText, n, pages, MinPDFCharsPerPage)
	}

	title := ""
	if m := pdfTitleRe.FindStringSubmatch(body); m != nil {
		title = strings.TrimSpace(m[1])
	}

	return &Doc{
		Title:     title,
		SourceURL: uri,
		Markdown:  body,
		Kind:      "paper",
		Extractor: PDFExtractorID,
	}, nil
}

// countJSONFile opens path and returns countJSONPages over its contents.
func countJSONFile(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return countJSONPages(f)
}

// countJSONPages returns the number of members of the top-level "pages"
// object in the DoclingDocument read from r (F.P4). The file is never
// unmarshalled whole — a 38-page paper's JSON is ~1.3 MB and a book's runs
// to hundreds of MB, dominated by keys (texts) that come before pages — so
// json.Decoder.Token() streams past everything else.
func countJSONPages(r io.Reader) (int, error) {
	dec := json.NewDecoder(r)
	tok, err := dec.Token()
	if err != nil {
		return 0, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return 0, errors.New("not a JSON object")
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return 0, err
		}
		key, _ := keyTok.(string)
		if key != "pages" {
			if err := skipJSONValue(dec); err != nil {
				return 0, err
			}
			continue
		}
		tok, err := dec.Token() // the '{' opening the pages object
		if err != nil {
			return 0, err
		}
		if d, ok := tok.(json.Delim); !ok || d != '{' {
			return 0, errors.New("pages is not an object")
		}
		n := 0
		for dec.More() {
			if _, err := dec.Token(); err != nil { // the member name "1".."N"
				return 0, err
			}
			n++
			if err := skipJSONValue(dec); err != nil {
				return 0, err
			}
		}
		if _, err := dec.Token(); err != nil { // the closing '}'
			return 0, err
		}
		return n, nil
	}
	// No pages key at all: reported as zero pages, which Extract refuses
	// ("docling reported no pages") rather than treating as a passing gate.
	return 0, nil
}

// skipJSONValue streams past the next JSON value in dec without
// materializing it — the point of F.P4. Scalars are consumed by the single
// Token that reads them; composites are tracked by depth so no nested
// object or array is left half-read.
func skipJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if _, ok := tok.(json.Delim); !ok {
		return nil
	}
	depth := 1
	for depth > 0 {
		tok, err = dec.Token()
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}
	return nil
}

// PDFVersion runs `<Command...> --version` and returns the version from the
// first output line, `Docling version: <v>`.
func PDFVersion(ctx context.Context, cfg PDFConfig) (string, error) {
	command := commandOf(cfg)
	if _, err := exec.LookPath(command[0]); err != nil {
		return "", sidecarMissing(command)
	}
	argv := append(append([]string{}, command[1:]...), "--version")
	out, err := exec.CommandContext(ctx, command[0], argv...).Output()
	if err != nil {
		return "", fmt.Errorf("docling --version: %w", err)
	}
	line, _, _ := strings.Cut(string(out), "\n")
	version, found := strings.CutPrefix(line, "Docling version: ")
	if !found {
		return "", fmt.Errorf("docling --version: unexpected first line %q", line)
	}
	return strings.TrimSpace(version), nil
}

// tailBuffer keeps at most the last cap bytes written to it. Docling
// reports progress on stderr — a long book conversion emits thousands of
// progress lines — and only the tail belongs in an error (F.P2), so the
// buffer slides instead of growing without bound.
type tailBuffer struct {
	buf []byte
	cap int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if excess := len(t.buf) - t.cap; excess > 0 {
		t.buf = append(t.buf[:0], t.buf[excess:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.buf) }
