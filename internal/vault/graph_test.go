package vault

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// TestGraphNeighborsMinimal proves Neighbors against a known expected set
// derived from spec/fixtures/minimal's link graph, which forms a 4-cycle:
// flash-attention <-> kv-cache <-> speculative-decoding <-> gpt-4 <->
// flash-attention, with no direct edge on either diagonal.
func TestGraphNeighborsMinimal(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	v, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	g := v.Graph()

	tests := []struct {
		page  string
		depth int
		want  []string
	}{
		{
			page:  "wiki/concepts/kv-cache.md",
			depth: 1,
			want: []string{
				"wiki/concepts/flash-attention.md",
				"wiki/concepts/speculative-decoding.md",
			},
		},
		{
			page:  "wiki/concepts/kv-cache.md",
			depth: 2,
			want: []string{
				"wiki/concepts/flash-attention.md",
				"wiki/concepts/speculative-decoding.md",
				"wiki/entities/gpt-4.md",
			},
		},
		{
			page:  "wiki/entities/gpt-4.md",
			depth: 1,
			want: []string{
				"wiki/concepts/flash-attention.md",
				"wiki/concepts/speculative-decoding.md",
			},
		},
		{
			page:  "wiki/entities/gpt-4.md",
			depth: 0,
			want:  nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.page, func(t *testing.T) {
			got := g.Neighbors(tc.page, tc.depth)
			// Compare by length and contents, not reflect.DeepEqual directly:
			// a zero-neighbor result and a nil "want" differ in nil-ness
			// (empty non-nil slice vs. nil), a distinction Neighbors' own
			// contract does not make.
			if len(got) != len(tc.want) || (len(got) > 0 && !reflect.DeepEqual(got, tc.want)) {
				t.Errorf("Neighbors(%q, %d) = %v, want %v", tc.page, tc.depth, got, tc.want)
			}
			for _, p := range got {
				if p == tc.page {
					t.Errorf("Neighbors(%q, %d) includes the page itself", tc.page, tc.depth)
				}
			}
		})
	}
}

// TestGraphBacklinksAndOutbound proves Outbound and Backlinks report the
// resolved edges of the minimal fixture's fully-reciprocal 4-cycle.
func TestGraphBacklinksAndOutbound(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	v, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	g := v.Graph()

	out := g.Outbound("wiki/concepts/kv-cache.md")
	if len(out) != 2 {
		t.Fatalf("len(Outbound(kv-cache.md)) = %d, want 2: %+v", len(out), out)
	}
	for _, ref := range out {
		if ref.From != "wiki/concepts/kv-cache.md" {
			t.Errorf("ref.From = %q, want kv-cache.md", ref.From)
		}
		if ref.To == "" {
			t.Errorf("ref.To is empty for a resolvable link: %+v", ref)
		}
	}

	back := g.Backlinks("wiki/concepts/kv-cache.md")
	if len(back) != 2 {
		t.Fatalf("len(Backlinks(kv-cache.md)) = %d, want 2: %+v", len(back), back)
	}
	for _, ref := range back {
		if ref.To != "wiki/concepts/kv-cache.md" {
			t.Errorf("ref.To = %q, want kv-cache.md", ref.To)
		}
	}
}

// TestResolve exercises Resolve's exact four-step order (backbone §2.9): an
// exact vault-relative path; "<t>.md"; a unique case-insensitive basename
// match; "<dir>/<t>.md" per wiki/* subdirectory. Ambiguous basenames must
// resolve to false, not to an arbitrary pick.
func TestResolve(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	v, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	tests := []struct {
		name   string
		target string
		want   string
		wantOK bool
	}{
		{
			name:   "exact vault-relative path",
			target: "wiki/concepts/kv-cache.md",
			want:   "wiki/concepts/kv-cache.md",
			wantOK: true,
		},
		{
			name:   "exact path without extension",
			target: "wiki/concepts/kv-cache",
			want:   "wiki/concepts/kv-cache.md",
			wantOK: true,
		},
		{
			name:   "basename, exact case",
			target: "kv-cache",
			want:   "wiki/concepts/kv-cache.md",
			wantOK: true,
		},
		{
			name:   "basename, case-insensitive",
			target: "GPT-4",
			want:   "wiki/entities/gpt-4.md",
			wantOK: true,
		},
		{
			name:   "basename, lowercase target against mixed-case file",
			target: "gpt-4",
			want:   "wiki/entities/gpt-4.md",
			wantOK: true,
		},
		{
			name:   "unresolvable",
			target: "nonexistent-target",
			want:   "",
			wantOK: false,
		},
		{
			name:   "empty target",
			target: "",
			want:   "",
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Resolve(v, tc.target)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("Resolve(%q) = (%q, %v), want (%q, %v)", tc.target, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestResolveAmbiguousBasename proves that when two pages share a basename,
// Resolve refuses to guess.
func TestResolveAmbiguousBasename(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	v, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Introduce a second page with the same basename as an existing one, in
	// a different directory, by writing directly into the copied fixture's
	// temp tree (never the repo's own spec/fixtures).
	dup := filepath.Join(dir, "wiki", "entities", "kv-cache.md")
	content := "---\ntitle: KV Cache Duplicate\ncreated: 2026-01-01\nupdated: 2026-01-01\ntype: entity\n---\n\n# KV Cache Duplicate\n\n- [[flash-attention]]\n- [[gpt-4]]\n"
	if err := writeTestFile(t, dup, content); err != nil {
		t.Fatalf("write duplicate fixture: %v", err)
	}
	if err := v.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if _, ok := Resolve(v, "kv-cache"); ok {
		t.Errorf("Resolve(kv-cache) resolved despite two pages sharing that basename")
	}
}

// TestResolveCaseInsensitiveMisCasedFilename proves the dirty fixture's own
// deliberate case-mismatch resolves: missing-raw.md links [[kv_cache]] to
// KV_Cache.md.
func TestResolveCaseInsensitiveMisCasedFilename(t *testing.T) {
	dir := testutil.CopyFixture(t, "dirty")
	v, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	got, ok := Resolve(v, "kv_cache")
	want := "wiki/concepts/KV_Cache.md"
	if !ok || got != want {
		t.Errorf("Resolve(kv_cache) = (%q, %v), want (%q, true)", got, ok, want)
	}
}
