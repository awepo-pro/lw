package index

import (
	"reflect"
	"testing"

	"github.com/awepo-pro/lw/internal/vault"
)

func TestTokenize(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "lowercase and strip punctuation",
			in:   "Hello, World!",
			want: []string{"hello", "world"},
		},
		{
			name: "drops stopwords and length-1 tokens",
			in:   "The Model is a Transformer",
			want: []string{"model", "transformer"},
		},
		{
			name: "all stopwords and single letters yields nothing",
			in:   "a I to be or not",
			want: nil,
		},
		{
			name: "intra-word hyphen kept as one token",
			in:   "well-known-model",
			want: []string{"well-known-model"},
		},
		{
			name: "leading and trailing hyphens are separators, not part of a word",
			in:   "-well known-",
			want: []string{"well", "known"},
		},
		{
			name: "double hyphen breaks the word",
			in:   "a--b",
			want: nil,
		},
		{
			name: "digits are alphanumeric",
			in:   "gpt-4 attention2",
			want: []string{"gpt-4", "attention2"},
		},
		{
			name: "empty string",
			in:   "",
			want: nil,
		},
		{
			name: "mixed whitespace and newlines",
			in:   "kv cache\nspeculative\tdecoding",
			want: []string{"kv", "cache", "speculative", "decoding"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Tokenize(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Tokenize(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

// TestWikilinkTokensKVCache pins the backbone §3 example exactly:
// [[kv-cache]] must contribute "kv-cache", "kv" and "cache" — the whole
// hyphenated token and both its parts.
func TestWikilinkTokensKVCache(t *testing.T) {
	got := wikilinkTokens("kv-cache")
	want := []string{"kv-cache", "kv", "cache"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wikilinkTokens(%q) = %#v, want %#v", "kv-cache", got, want)
	}
}

func TestWikilinkTokens(t *testing.T) {
	tests := []struct {
		name   string
		target string
		want   []string
	}{
		{
			name:   "hyphenated target: whole plus both parts",
			target: "speculative-decoding",
			want:   []string{"speculative-decoding", "speculative", "decoding"},
		},
		{
			name:   "case-insensitive, and a length-1 part is dropped",
			target: "GPT-4",
			want:   []string{"gpt-4", "gpt"},
		},
		{
			name:   "no hyphen: just the whole token",
			target: "flashattention",
			want:   []string{"flashattention"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wikilinkTokens(tt.target)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("wikilinkTokens(%q) = %#v, want %#v", tt.target, got, tt.want)
			}
		})
	}
}

func TestTagTokens(t *testing.T) {
	got := tagTokens([]string{"inference", "decoding"})
	want := []string{"inference", "decoding"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tagTokens = %#v, want %#v", got, want)
	}
}

// mustParsePage is a small helper for building a *vault.Page from raw bytes
// without needing a whole vault on disk — used to test bodyTokens against a
// wikilink alias, which no fixture under spec/fixtures exercises.
func mustParsePage(t *testing.T, body string) *vault.Page {
	t.Helper()
	src := "---\ntitle: Test Page\ncreated: 2026-08-10\nupdated: 2026-08-10\ntype: concept\n---\n\n" + body
	p, err := vault.ParsePage("wiki/concepts/test-page.md", []byte(src))
	if err != nil {
		t.Fatalf("vault.ParsePage: %v", err)
	}
	return p
}

// TestBodyTokensAlias pins backbone §3's amended contract: a wikilink's
// alias is indexed as body prose, in addition to the target's own tokens.
// [[kv-cache|the KV cache]] must contribute "kv-cache", "kv", "cache" from
// the target, and "kv", "cache" again from the alias ("the" is a stopword) —
// the overlap is deliberate, not deduplicated, because a term reachable
// through both the target and the alias is genuinely more evidence.
func TestBodyTokensAlias(t *testing.T) {
	p := mustParsePage(t, "See [[kv-cache|the KV cache]] for more on caching keys and values.\n")

	got := bodyTokens(p)
	want := []string{
		"see",
		"kv-cache", "kv", "cache", // from the target
		"kv", "cache", // from the alias ("the" dropped as a stopword)
		"more", "caching", "keys", "values",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bodyTokens = %#v, want %#v", got, want)
	}
}

// TestBodyTokensNoAlias proves a link with no alias contributes exactly the
// target's tokens and nothing more — the empty-alias case must behave
// identically to before the alias amendment.
func TestBodyTokensNoAlias(t *testing.T) {
	p := mustParsePage(t, "See [[kv-cache]] for details.\n")

	got := bodyTokens(p)
	want := []string{
		"see",
		"kv-cache", "kv", "cache",
		"details",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bodyTokens = %#v, want %#v", got, want)
	}
}
