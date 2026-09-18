package extract

import (
	"context"
	"errors"
	"testing"
)

// stubExtractor is a minimal Extractor for exercising Chain without
// touching the filesystem or the network.
type stubExtractor struct {
	handles string // CanHandle returns true only for this exact uri
	doc     *Doc
	err     error
}

func (s stubExtractor) CanHandle(uri string) bool { return uri == s.handles }

func (s stubExtractor) Extract(ctx context.Context, uri string) (*Doc, error) {
	return s.doc, s.err
}

func TestChainDelegatesToFirstMatch(t *testing.T) {
	first := stubExtractor{handles: "a", doc: &Doc{Title: "A"}}
	second := stubExtractor{handles: "b", doc: &Doc{Title: "B"}}
	c := Chain(first, second)

	if !c.CanHandle("a") || !c.CanHandle("b") {
		t.Fatalf("CanHandle should be true for both member URIs")
	}
	if c.CanHandle("c") {
		t.Fatalf("CanHandle(%q) = true, want false", "c")
	}

	doc, err := c.Extract(context.Background(), "b")
	if err != nil {
		t.Fatalf("Extract error: %v", err)
	}
	if doc.Title != "B" {
		t.Errorf("Title = %q, want %q", doc.Title, "B")
	}
}

func TestChainNoMatch(t *testing.T) {
	c := Chain(stubExtractor{handles: "a"})
	if _, err := c.Extract(context.Background(), "z"); err == nil {
		t.Fatal("Extract() error = nil, want a non-nil error when nothing can handle the uri")
	}
}

func TestChainPrefersEarlierExtractor(t *testing.T) {
	// Both extractors can handle "x"; Chain must always pick the first one,
	// in argument order, never the second — determinism (backbone §10).
	first := stubExtractor{handles: "x", doc: &Doc{Title: "first"}}
	second := stubExtractor{handles: "x", doc: &Doc{Title: "second"}}
	c := Chain(first, second)

	for i := 0; i < 5; i++ {
		doc, err := c.Extract(context.Background(), "x")
		if err != nil {
			t.Fatalf("Extract error: %v", err)
		}
		if doc.Title != "first" {
			t.Fatalf("Extract()#%d.Title = %q, want %q", i, doc.Title, "first")
		}
	}
}

func TestChainPropagatesError(t *testing.T) {
	wantErr := errors.New("boom")
	c := Chain(stubExtractor{handles: "a", err: wantErr})
	_, err := c.Extract(context.Background(), "a")
	if !errors.Is(err, wantErr) {
		t.Fatalf("Extract() error = %v, want %v", err, wantErr)
	}
}

func TestSuggestPath(t *testing.T) {
	cases := []struct {
		name string
		doc  *Doc
		want string
	}{
		{
			name: "article",
			doc:  &Doc{Title: "KV Cache, Explained", Kind: "article"},
			want: "raw/articles/kv-cache-explained.md",
		},
		{
			name: "paper",
			doc:  &Doc{Title: "Attention Is All You Need", Kind: "paper"},
			want: "raw/papers/attention-is-all-you-need.md",
		},
		{
			name: "transcript",
			doc:  &Doc{Title: "Panel: Scaling Inference", Kind: "transcript"},
			want: "raw/transcripts/panel-scaling-inference.md",
		},
		{
			name: "unknown kind defaults to articles",
			doc:  &Doc{Title: "Something", Kind: "video"},
			want: "raw/articles/something.md",
		},
		{
			name: "empty title falls back to untitled",
			doc:  &Doc{Title: "", Kind: "article"},
			want: "raw/articles/untitled.md",
		},
		{
			name: "title with only punctuation falls back to untitled",
			doc:  &Doc{Title: "!!!", Kind: "article"},
			want: "raw/articles/untitled.md",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SuggestPath(tc.doc); got != tc.want {
				t.Errorf("SuggestPath() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSuggestPathDeterministic(t *testing.T) {
	d := &Doc{Title: "Repeatable Title", Kind: "article"}
	first := SuggestPath(d)
	for i := 0; i < 5; i++ {
		if got := SuggestPath(d); got != first {
			t.Fatalf("SuggestPath()#%d = %q, want %q", i, got, first)
		}
	}
}

// TestSuggestPathIsValid pins A-805: every SuggestPath result is a path
// the staging validator accepts, whatever script the title is written in.
// The non-Latin part of a mixed title drops out of the FILE NAME (the
// title itself is kept in the document), and a title with nothing
// slugifiable at all falls back to untitled.
func TestSuggestPathIsValid(t *testing.T) {
	cases := []struct {
		title string
		kind  string
		want  string
	}{
		{"Quaternion 四元數簡介", "article", "raw/articles/quaternion.md"},
		{"四元數簡介", "article", "raw/articles/untitled.md"},
		{"Café Déjà Vu", "paper", "raw/papers/cafe-deja-vu.md"},
	}
	for _, tc := range cases {
		if got := SuggestPath(&Doc{Title: tc.title, Kind: tc.kind}); got != tc.want {
			t.Errorf("SuggestPath(%q, %q) = %q, want %q", tc.title, tc.kind, got, tc.want)
		}
	}
}
