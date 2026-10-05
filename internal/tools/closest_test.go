package tools

// closest_test.go freezes 036 D1's matcher: closestPaths names the paths a
// not-found message should offer when the model guessed one. The rows are
// the contract — the brief's five, then extra rows that pin the rules a
// brief row cannot reach (rune scoring, the empty squash, the threshold
// edge, the wanted path never offering itself) so a mutation of any one of
// them turns a test red rather than passing for want of a row.

import (
	"slices"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// minimalPaths returns the minimal fixture's committed raw-source paths and
// page paths, each sorted — the "minimal's raw sources" and "minimal's
// pages" the frozen table names, read from the fixture rather than typed
// out so the rows cannot drift from it.
func minimalPaths(t *testing.T) (raw, pages []string) {
	t.Helper()
	v, err := vault.Open(testutil.CopyFixture(t, "minimal"))
	if err != nil {
		t.Fatalf("vault.Open(minimal): %v", err)
	}
	for _, r := range v.RawSources() {
		raw = append(raw, r.Path)
	}
	for _, p := range v.Pages() {
		pages = append(pages, p.Path)
	}
	return raw, pages
}

func TestClosestPaths(t *testing.T) {
	minRaw, minPages := minimalPaths(t)

	tests := []struct {
		name string
		want string
		have []string
		got  []string
	}{
		// --- the brief's frozen rows -------------------------------------
		{
			name: "raw: hyphen dropped from the file name (equal squash)",
			want: "raw/papers/leviathan2023.md",
			have: minRaw,
			got:  []string{"raw/papers/leviathan-2023.md"},
		},
		{
			name: "raw: a made-up short name shares a 5-rune run with the real file",
			want: "raw/articles/glm52-summary.html",
			have: []string{"raw/articles/glm-5-2-744b-on-m3-ultra.md", "raw/articles/gemini.md", "raw/articles/llm-wiki.md"},
			got:  []string{"raw/articles/glm-5-2-744b-on-m3-ultra.md"},
		},
		{
			name: "page: right name, wrong directory",
			want: "wiki/entities/kv-cache.md",
			have: minPages,
			got:  []string{"wiki/concepts/kv-cache.md"},
		},
		{
			name: "page: nothing close offers nothing",
			want: "zz.md",
			have: minPages,
			got:  nil,
		},
		{
			name: "equal squash first, then score, then path; capped at three",
			want: "cache",
			have: []string{"a/kv-cache.md", "b/cache-line.md", "c/cachex.md", "d/cache.md"},
			got:  []string{"d/cache.md", "a/kv-cache.md", "b/cache-line.md"},
		},

		// --- extra rows: rules the brief's rows cannot reach -------------
		{
			// Scored by bytes, the 2-rune "日本" is 6 bytes and clears the
			// 4 threshold; scored by runes it is 2 and does not. 036 D1
			// says runes: a CJK file name must not match on a lone word.
			name: "score counts runes, not bytes",
			want: "wiki/a/日本x.md",
			have: []string{"wiki/b/日本y.md"},
			got:  nil,
		},
		{
			name: "four runes clear the threshold (multi-byte)",
			want: "wiki/a/日本語テx.md",
			have: []string{"wiki/b/日本語テy.md"},
			got:  []string{"wiki/b/日本語テy.md"},
		},
		{
			name: "three runes in common stay under the threshold",
			want: "x/abc-q.md",
			have: []string{"y/zabcw.md"},
			got:  nil,
		},
		{
			name: "four runes in common reach the threshold",
			want: "x/abcd-q.md",
			have: []string{"y/zabcdw.md"},
			got:  []string{"y/zabcdw.md"},
		},
		{
			name: "equal squash qualifies below the threshold",
			want: "abc",
			have: []string{"dir/a-b-c.md"},
			got:  []string{"dir/a-b-c.md"},
		},
		{
			name: "case and punctuation do not matter",
			want: "wiki/concepts/KV_Cache.MD",
			have: []string{"wiki/concepts/kv-cache.md"},
			got:  []string{"wiki/concepts/kv-cache.md"},
		},
		{
			name: "the wanted path never offers itself",
			want: "wiki/concepts/kv-cache.md",
			have: []string{"wiki/concepts/kv-cache.md", "wiki/entities/kv-cache.md"},
			got:  []string{"wiki/entities/kv-cache.md"},
		},
		{
			// A name with no letter or digit squashes to "" — and "" equals
			// every other empty squash, which would offer arbitrary files.
			name: "an empty squash offers nothing",
			want: "wiki/---.md",
			have: []string{"wiki/___.md", "wiki/kv-cache.md"},
			got:  nil,
		},
		{
			name: "equal-squash candidates are ordered by path",
			want: "kvcache",
			have: []string{"z/kv-cache.md", "a/kv_cache.md", "m/kvcache.md"},
			got:  []string{"a/kv_cache.md", "m/kvcache.md", "z/kv-cache.md"},
		},
		{
			name: "higher score outranks lower regardless of path order",
			want: "speculative-decoding.md",
			have: []string{"a/decoding.md", "z/speculative-decode.md"},
			got:  []string{"z/speculative-decode.md", "a/decoding.md"},
		},
		{
			name: "no candidates at all",
			want: "wiki/concepts/kv-cache.md",
			have: nil,
			got:  nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// closestPaths must not reorder or mutate the caller's slice:
			// the same list serves the message and, in the changeset case,
			// a freshly built union.
			before := slices.Clone(tc.have)
			got := closestPaths(tc.want, tc.have)
			if !slices.Equal(got, tc.got) {
				t.Errorf("closestPaths(%q, %q) = %q, want %q", tc.want, tc.have, got, tc.got)
			}
			if !slices.Equal(tc.have, before) {
				t.Errorf("closestPaths mutated its input: %q, was %q", tc.have, before)
			}
		})
	}
}

// TestClosestPathsDeterministic: the same inputs in any order give the same
// answer — the determinism the project's whole thesis rests on. Candidates
// arrive from a vault listing and a changeset, so input order is not
// something the matcher may lean on.
func TestClosestPathsDeterministic(t *testing.T) {
	have := []string{"c/cachex.md", "a/kv-cache.md", "d/cache.md", "b/cache-line.md"}
	want := closestPaths("cache", have)
	rev := slices.Clone(have)
	slices.Reverse(rev)
	if got := closestPaths("cache", rev); !slices.Equal(got, want) {
		t.Errorf("reversed input gave %q, forward gave %q", got, want)
	}
}

// TestClosestPathsDedupes: a path offered twice by its sources — a committed
// page the open changeset also stages — must be named once, or the message
// would list one file as two candidates and eat a slot of the three.
func TestClosestPathsDedupes(t *testing.T) {
	have := []string{"a/kv-cache.md", "a/kv-cache.md", "b/kv_cache.md"}
	want := []string{"a/kv-cache.md", "b/kv_cache.md"}
	if got := closestPaths("kvcache", have); !slices.Equal(got, want) {
		t.Errorf("closestPaths(kvcache, %q) = %q, want %q", have, got, want)
	}
}
