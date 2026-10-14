package extract

// original_bytes_test.go pins 058's D1: an extractor that already holds the
// source's bytes hands them over verbatim in Doc.OriginalBytes (with
// OriginalExt), so stage.ingest_source can keep an original beside every raw
// — not only beside a PDF's. The Markdown each extractor produces is pinned
// by its own tests and goldens; here it is only checked to be unchanged.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestHTMLLocalKeepsOriginalBytes: a local saved page's bytes survive the
// extraction untouched — upper-case extension, BOM, CRLF and all — and the
// Markdown stays exactly what the golden recorded before 058.
func TestHTMLLocalKeepsOriginalBytes(t *testing.T) {
	article, err := os.ReadFile(filepath.Join("testdata", "article.html"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "article.golden.md"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}

	cases := []struct {
		name    string
		file    string
		bytes   []byte
		wantExt string
	}{
		{name: "upper-case .HTML", file: "Page.HTML", bytes: article, wantExt: ".html"},
		{name: "lower-case .html", file: "page.html", bytes: article, wantExt: ".html"},
		{name: ".htm keeps .htm", file: "page.htm", bytes: article, wantExt: ".htm"},
		{name: "upper-case .HTM", file: "PAGE.HTM", bytes: article, wantExt: ".htm"},
		{
			name:    "BOM and CRLF kept verbatim",
			file:    "crlf.html",
			bytes:   []byte("\xef\xbb\xbf<html>\r\n<body>\r\n<h1>Kept Verbatim</h1>\r\n<p>caf\xc3\xa9</p>\r\n</body>\r\n</html>\r\n"),
			wantExt: ".html",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tc.file)
			if err := os.WriteFile(path, tc.bytes, 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			doc, err := NewHTML(nil).Extract(context.Background(), path)
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if !bytes.Equal(doc.OriginalBytes, tc.bytes) {
				t.Errorf("OriginalBytes = %q, want the file's %d bytes verbatim", doc.OriginalBytes, len(tc.bytes))
			}
			if doc.OriginalExt != tc.wantExt {
				t.Errorf("OriginalExt = %q, want %q", doc.OriginalExt, tc.wantExt)
			}
			if doc.Original != "" {
				t.Errorf("Original = %q, want \"\" (the PDF backend's path field)", doc.Original)
			}
			if bytes.Equal(tc.bytes, article) && doc.Markdown != string(golden) {
				t.Errorf("Markdown changed from the pre-058 golden:\n%s", doc.Markdown)
			}
		})
	}
}

// TestHTMLRemoteKeepsOriginalBytes: a fetched page keeps the exact bytes the
// server sent, and is named .html whatever the URL's own path ends in.
func TestHTMLRemoteKeepsOriginalBytes(t *testing.T) {
	page := []byte("<html><head><title>Remote</title></head>\r\n<body><h1>Served Remotely</h1><p>caf\xc3\xa9</p></body></html>\r\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(page)
	}))
	defer srv.Close()

	for _, suffix := range []string{"", "/page.htm", "/article.php?id=7", "/notes.md"} {
		t.Run("url"+suffix, func(t *testing.T) {
			doc, err := NewHTML(srv.Client()).Extract(context.Background(), srv.URL+suffix)
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if !bytes.Equal(doc.OriginalBytes, page) {
				t.Errorf("OriginalBytes = %q, want the served %d bytes verbatim", doc.OriginalBytes, len(page))
			}
			if doc.OriginalExt != ".html" {
				t.Errorf("OriginalExt = %q, want \".html\" for a remote URL", doc.OriginalExt)
			}
			if doc.Title != "Served Remotely" {
				t.Errorf("Title = %q, want %q", doc.Title, "Served Remotely")
			}
		})
	}
}

// TestFileKeepsOriginalBytesCRLF: the passthrough extractor normalizes the
// Markdown it returns, but the original is the file as it was on disk —
// CRLF, stray CR, trailing blank lines and all.
func TestFileKeepsOriginalBytesCRLF(t *testing.T) {
	cases := []struct {
		file    string
		wantExt string
	}{
		{"notes.md", ".md"},
		{"notes.txt", ".txt"},
		{"notes.markdown", ".markdown"},
		{"NOTES.MD", ".md"},
		{"Notes.TXT", ".txt"},
		{"README.Markdown", ".markdown"},
	}
	raw := []byte("# Max Combination\r\n\r\nSome body text.\r\nA lone CR\rline.\r\n\r\n\r\n")
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tc.file)
			if err := os.WriteFile(path, raw, 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			doc, err := NewFile().Extract(context.Background(), path)
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if !bytes.Equal(doc.OriginalBytes, raw) {
				t.Errorf("OriginalBytes = %q, want the file's %d bytes with their \\r\\n", doc.OriginalBytes, len(raw))
			}
			if doc.OriginalExt != tc.wantExt {
				t.Errorf("OriginalExt = %q, want %q", doc.OriginalExt, tc.wantExt)
			}
			if want := "# Max Combination\n\nSome body text.\nA lone CR\nline.\n"; doc.Markdown != want {
				t.Errorf("Markdown = %q, want the normalized %q", doc.Markdown, want)
			}
			if doc.Original != "" {
				t.Errorf("Original = %q, want \"\" (the PDF backend's path field)", doc.Original)
			}
		})
	}
}

// TestPDFOriginalUnchanged: the PDF backend still names its file in Original
// and carries no in-memory bytes — the two original mechanisms stay apart.
func TestPDFOriginalUnchanged(t *testing.T) {
	cfg := fakeDocling(t)
	uri := pdfFixture(t, t.TempDir(), "paper.pdf", "PAGES=1\n"+padTo("", 150)+"\n")

	doc, err := NewPDF(cfg).Extract(context.Background(), uri)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if doc.Original != uri {
		t.Errorf("Original = %q, want the absolute input path %q", doc.Original, uri)
	}
	if doc.OriginalBytes != nil {
		t.Errorf("OriginalBytes = %d bytes, want nil for the PDF backend", len(doc.OriginalBytes))
	}
	if doc.OriginalExt != "" {
		t.Errorf("OriginalExt = %q, want \"\" when OriginalBytes is nil", doc.OriginalExt)
	}
}
