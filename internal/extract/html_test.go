package extract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// goldenCases drives both TestHTMLExtractGolden and
// TestHTMLExtractDeterministic: one saved HTML fixture per case, under
// this package's own testdata/ (there is no spec/fixtures/html — see this
// subtask's brief), with the Title and Kind we expect parseHTMLDoc to
// recover.
var goldenCases = []struct {
	name      string
	file      string // under testdata/
	wantTitle string
}{
	{name: "article", file: "article.html", wantTitle: "KV Cache, Explained Again"},
	{name: "paper", file: "paper.html", wantTitle: "Speculative Decoding for Faster Inference"},
	{name: "transcript", file: "transcript.html", wantTitle: "Panel: Scaling Inference"},
}

// TestHTMLExtractGolden extracts each saved HTML fixture and compares the
// resulting markdown against a golden file this package owns
// (testdata/<name>.golden.md) — no network, no spec/fixtures/ involved.
func TestHTMLExtractGolden(t *testing.T) {
	ex := NewHTML(http.DefaultClient)
	for _, tc := range goldenCases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join("testdata", tc.file)
			doc, err := ex.Extract(context.Background(), path)
			if err != nil {
				t.Fatalf("Extract(%s) error: %v", path, err)
			}
			if doc.Title != tc.wantTitle {
				t.Errorf("Title = %q, want %q", doc.Title, tc.wantTitle)
			}
			if doc.Kind != "article" {
				t.Errorf("Kind = %q, want %q", doc.Kind, "article")
			}
			if doc.Extractor != "go/html" {
				t.Errorf("Extractor = %q, want %q", doc.Extractor, "go/html")
			}
			if doc.SourceURL != path {
				t.Errorf("SourceURL = %q, want %q", doc.SourceURL, path)
			}
			testutil.GoldenString(t, filepath.Join("testdata", tc.name+".golden.md"), doc.Markdown)
		})
	}
}

// TestHTMLExtractDeterministic extracts the same saved HTML twice and
// requires an identical sha256 of the resulting markdown both times — the
// property stage.ingest_source's dedupe-by-hash depends on entirely
// (backbone §10's Contract).
func TestHTMLExtractDeterministic(t *testing.T) {
	ex := NewHTML(http.DefaultClient)
	for _, tc := range goldenCases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join("testdata", tc.file)

			doc1, err := ex.Extract(context.Background(), path)
			if err != nil {
				t.Fatalf("Extract #1 error: %v", err)
			}
			doc2, err := ex.Extract(context.Background(), path)
			if err != nil {
				t.Fatalf("Extract #2 error: %v", err)
			}

			sha1 := sha256Hex(doc1.Markdown)
			sha2 := sha256Hex(doc2.Markdown)
			if sha1 != sha2 {
				t.Fatalf("sha256 drifted across two extractions of the same input:\n#1: %s\n#2: %s", sha1, sha2)
			}
			if doc1.Markdown != doc2.Markdown {
				t.Fatalf("markdown drifted across two extractions of the same input, despite equal sha256")
			}
		})
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// TestHTMLExtractOverHTTP proves NewHTML also fetches over http/https,
// using httptest's loopback server rather than the real network — the
// "no network in any test" rule (00-conventions.md §6) bars a real
// endpoint, not a process-local listener.
func TestHTMLExtractOverHTTP(t *testing.T) {
	const page = `<html><body><h1>Served Over HTTP</h1><p>hello</p></body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	ex := NewHTML(srv.Client())
	doc, err := ex.Extract(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Extract(%s) error: %v", srv.URL, err)
	}
	if doc.Title != "Served Over HTTP" {
		t.Errorf("Title = %q, want %q", doc.Title, "Served Over HTTP")
	}
	if doc.SourceURL != srv.URL {
		t.Errorf("SourceURL = %q, want %q", doc.SourceURL, srv.URL)
	}
}

// TestHTMLExtractHTTPStatusError requires a non-200 response to fail
// clearly rather than silently extracting an error page as content.
func TestHTMLExtractHTTPStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	ex := NewHTML(srv.Client())
	if _, err := ex.Extract(context.Background(), srv.URL); err == nil {
		t.Fatal("Extract() error = nil, want a non-nil error for a 404 response")
	}
}

// TestHTMLExtractMissingLocalFile requires a clear, wrapped error rather
// than a panic or a silently-empty Doc.
func TestHTMLExtractMissingLocalFile(t *testing.T) {
	ex := NewHTML(http.DefaultClient)
	_, err := ex.Extract(context.Background(), filepath.Join("testdata", "does-not-exist.html"))
	if err == nil {
		t.Fatal("Extract() error = nil, want a non-nil error for a missing file")
	}
}

func TestHTMLCanHandle(t *testing.T) {
	ex := NewHTML(http.DefaultClient)
	cases := []struct {
		uri  string
		want bool
	}{
		{"https://example.org/page", true},
		{"http://example.org/page", true},
		{"testdata/article.html", true},
		{"testdata/article.HTML", true},
		{"testdata/article.htm", true},
		{"testdata/article.md", false},
		{"testdata/article.txt", false},
		{"ftp://example.org/page", false},
	}
	for _, tc := range cases {
		if got := ex.CanHandle(tc.uri); got != tc.want {
			t.Errorf("CanHandle(%q) = %v, want %v", tc.uri, got, tc.want)
		}
	}
}

func TestNewHTMLNilClientUsesDefault(t *testing.T) {
	ex := NewHTML(nil)
	if ex == nil {
		t.Fatal("NewHTML(nil) = nil")
	}
	// A nil *http.Client would panic the first time it is used to fetch a
	// remote URL; extracting a local file never reaches that path, so this
	// only proves construction itself does not panic or return nil.
	if _, err := ex.Extract(context.Background(), filepath.Join("testdata", "article.html")); err != nil {
		t.Fatalf("Extract with default client error: %v", err)
	}
}

// TestParseHTMLDocEmptyBody covers a document with nothing whitelisted in
// it at all: Markdown must be "", not a stray blank line.
func TestParseHTMLDocEmptyBody(t *testing.T) {
	doc, err := parseHTMLDoc([]byte(`<html><body><script>1</script><nav>skip</nav></body></html>`))
	if err != nil {
		t.Fatalf("parseHTMLDoc error: %v", err)
	}
	if doc.Markdown != "" {
		t.Errorf("Markdown = %q, want empty", doc.Markdown)
	}
	if doc.Title != "" {
		t.Errorf("Title = %q, want empty", doc.Title)
	}
}
