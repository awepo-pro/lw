package vault

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// TestParseRawSourceFixtures proves ParseRawSource extracts exactly
// source_url/ingested/sha256, derives Title from the body's first level-1
// heading, and that Serialize reproduces every fixture byte for byte
// (backbone §2.7).
func TestParseRawSourceFixtures(t *testing.T) {
	root := testutil.FixtureRoot(t)

	tests := []struct {
		path      string
		sourceURL string
		ingested  string
		title     string
	}{
		{
			path:      "minimal/raw/papers/leviathan-2023.md",
			sourceURL: "https://arxiv.org/abs/2211.17192",
			ingested:  "2026-08-25",
			title:     "Fast Inference from Transformers via Speculative Decoding",
		},
		{
			path:      "minimal/raw/articles/kv-cache-explained.md",
			sourceURL: "https://example.org/articles/kv-cache-explained",
			ingested:  "2026-08-20",
			title:     "KV Cache, Explained",
		},
		{
			path:      "dirty/raw/papers/valid-source.md",
			sourceURL: "https://example.org/papers/latency-methodology",
			ingested:  "2026-08-12",
			title:     "Reproducibility Notes for Latency Benchmarks",
		},
		{
			path:      "dirty/raw/papers/drifted-source.md",
			sourceURL: "https://example.org/papers/drifted-source",
			ingested:  "2026-08-16",
			title:     "Drifted Source",
		},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			full := filepath.Join(root, filepath.FromSlash(tc.path))
			b, err := os.ReadFile(full)
			if err != nil {
				t.Fatalf("ReadFile(%s): %v", full, err)
			}

			r, err := ParseRawSource(tc.path, b)
			if err != nil {
				t.Fatalf("ParseRawSource: %v", err)
			}

			if r.Path != tc.path {
				t.Errorf("Path = %q, want %q", r.Path, tc.path)
			}
			if r.SourceURL != tc.sourceURL {
				t.Errorf("SourceURL = %q, want %q", r.SourceURL, tc.sourceURL)
			}
			if r.Ingested.String() != tc.ingested {
				t.Errorf("Ingested = %q, want %q", r.Ingested.String(), tc.ingested)
			}
			if r.Title != tc.title {
				t.Errorf("Title = %q, want %q", r.Title, tc.title)
			}

			got := r.Serialize()
			if !bytes.Equal(got, b) {
				t.Errorf("Serialize() does not reproduce %s byte for byte:\n got=%q\nwant=%q", tc.path, got, b)
			}
			if bytes.Contains(got, []byte("title:")) {
				t.Errorf("Serialize() emitted a title key, which backbone §2.7 forbids: %q", got)
			}
		})
	}
}

// TestBodySHA256Drift is the drift-detection contract itself (S1
// correction C-2): the recorded sha256 of three fixtures matches
// BodySHA256(Body) exactly, and the fourth — deliberately drifted — does
// not.
func TestBodySHA256Drift(t *testing.T) {
	root := testutil.FixtureRoot(t)

	matching := []string{
		"minimal/raw/papers/leviathan-2023.md",
		"minimal/raw/articles/kv-cache-explained.md",
		"dirty/raw/papers/valid-source.md",
	}
	for _, p := range matching {
		t.Run(p, func(t *testing.T) {
			r := mustParseRawSource(t, root, p)
			got := BodySHA256(r.Body)
			if got != r.SHA256 {
				t.Errorf("BodySHA256(Body) = %s, want recorded sha256 %s", got, r.SHA256)
			}
			if len(got) != 64 {
				t.Errorf("BodySHA256 has length %d, want 64: %q", len(got), got)
			}
		})
	}

	t.Run("dirty/raw/papers/drifted-source.md", func(t *testing.T) {
		r := mustParseRawSource(t, root, "dirty/raw/papers/drifted-source.md")
		got := BodySHA256(r.Body)
		if got == r.SHA256 {
			t.Fatalf("BodySHA256(Body) unexpectedly matches the deliberately drifted recorded sha256 %s", r.SHA256)
		}
	})
}

// mustParseRawSource reads and parses spec/fixtures/<relPath> as a
// RawSource, failing t on any error.
func mustParseRawSource(t *testing.T, root, relPath string) *RawSource {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(relPath))
	b, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", full, err)
	}
	r, err := ParseRawSource(relPath, b)
	if err != nil {
		t.Fatalf("ParseRawSource(%s): %v", relPath, err)
	}
	return r
}

// TestParseRawSourceNoHeadingTitle proves Title is "" when the body has no
// level-1 ATX heading.
func TestParseRawSourceNoHeadingTitle(t *testing.T) {
	sha := strings.Repeat("0", 64)
	b := []byte(fmt.Sprintf("---\nsource_url: https://example.org/x\ningested: 2026-01-01\nsha256: %s\n---\n\nJust a paragraph, no heading.\n", sha))
	r, err := ParseRawSource("raw/x.md", b)
	if err != nil {
		t.Fatalf("ParseRawSource: %v", err)
	}
	if r.Title != "" {
		t.Errorf("Title = %q, want \"\"", r.Title)
	}
}

// TestParseRawSourceMalformed proves a raw source with no closing "---"
// line is an error.
func TestParseRawSourceMalformed(t *testing.T) {
	b := []byte("---\nsource_url: https://example.org/x\n")
	if _, err := ParseRawSource("raw/x.md", b); err == nil {
		t.Fatalf("ParseRawSource(malformed): got nil error, want one")
	}
}
