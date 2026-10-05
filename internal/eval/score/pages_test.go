package score

import (
	"slices"
	"testing"
)

// TestSlugTokensAndNearDuplicate pins the duplicate-page rule: a slug is its
// file's base name, split on "-" into lowercase tokens with the stop words
// dropped and each all-ASCII-letter token stemmed; two slugs are
// near-duplicates when the Jaccard index of their token sets is at least 0.5.
// The rows are page names the ingest actually produced next to the page
// that already covered them (049).
func TestSlugTokensAndNearDuplicate(t *testing.T) {
	t.Run("tokens", func(t *testing.T) {
		tests := []struct {
			slug string
			want []string
		}{
			{"the-HTTP-versions-vs-quic", []string{"http", "quic", "version"}},
			{"http-http-HTTP", []string{"http"}},
			{"the-of-vs", nil},
			{"", nil},
			{"量子-计算", []string{"计算", "量子"}}, // sorted by byte order, not kept as written
		}
		for _, tc := range tests {
			if got := SlugTokens(tc.slug); !slices.Equal(got, tc.want) {
				t.Errorf("SlugTokens(%q) = %q, want %q", tc.slug, got, tc.want)
			}
		}
		// Empty tokens between and around hyphens are dropped; the stem of
		// "cache" is the library's to spell, so compare with the clean form.
		if got, want := SlugTokens("kv--cache-"), SlugTokens("kv-cache"); len(want) != 2 || !slices.Equal(got, want) {
			t.Errorf(`SlugTokens("kv--cache-") = %q, want %q (two tokens)`, got, want)
		}
	})

	t.Run("near-duplicates", func(t *testing.T) {
		tests := []struct {
			a, b string
			want bool
		}{
			{"glm-5-2-on-m3-ultra", "glm-5-2", true},        // 3 of 5
			{"quaternion", "quaternion-3d-rotation", false}, // 1 of 3
			{"http-versions", "http-version", true},         // stemming
			{"tls", "http", false},
			{"量子-计算", "量子-计算", true},                               // non-ASCII kept as is
			{"kv-cache-eviction", "kv-cache-policy", true},         // 2 of 4: exactly the threshold
			{"kv-cache-eviction", "kv-cache-policy-tuning", false}, // 2 of 5
			{"HTTP-Versions", "http-version", true},
			{"the-cache", "cache", true}, // a stop word is not evidence
			{"the", "of", false},         // no tokens each: nothing to compare
			{"", "", false},
			{"the", "tls", false},
		}
		for _, tc := range tests {
			if got := NearDuplicate(tc.a, tc.b); got != tc.want {
				t.Errorf("NearDuplicate(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
			if got := NearDuplicate(tc.b, tc.a); got != tc.want {
				t.Errorf("NearDuplicate(%q, %q) = %v, want %v (symmetry)", tc.b, tc.a, got, tc.want)
			}
		}
	})
}

// TestLinesKept pins what "a patch lost nothing" means for a page body: every
// non-blank line of the old body is still in the new one, as a multiset
// (a line that was there twice must be there twice), compared after the
// trailing spaces and tabs are trimmed. New lines, moved lines and blank-line
// changes do not matter; a dropped or shortened line does (049).
func TestLinesKept(t *testing.T) {
	tests := []struct {
		name          string
		before, after string
		want          bool
	}{
		{"a line added between", "a\nb\n", "a\nX\nb\n", true},
		{"a repeated line lost one copy", "a\na\nb", "a\nb", false},
		{"trailing space and a blank line", "a  \n\nb", "a\nb", true},
		{"the last line dropped", "a\nb", "a\n", false},
		{"nothing before", "", "anything\n", true},
		{"only blank lines before", "\n  \n\t\n", "", true},
		{"lines reordered", "a\nb", "b\na", true},
		{"a repeated line kept twice and one more", "a", "a\na", true},
		{"a repeated line with enough copies", "a\na\nb", "b\na\nx\na", true},
		{"leading whitespace is part of the line", "  a", "a", false},
		{"trailing tab trimmed", "a\t\nb", "a\nb", true},
		{"a line edited", "alpha beta", "alpha  beta", false},
		{"no trailing newline either side", "a\nb", "a\nb", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := LinesKept(tc.before, tc.after); got != tc.want {
				t.Errorf("LinesKept(%q, %q) = %v, want %v", tc.before, tc.after, got, tc.want)
			}
		})
	}
}
