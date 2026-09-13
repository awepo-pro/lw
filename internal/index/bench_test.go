package index

import (
	"fmt"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// benchTiers is the note count every benchmark below runs at: three tiers far
// above the spec/fixtures/minimal fixture the unit tests use, so the index
// hot paths are measured where scaling is visible instead of assumed.
var benchTiers = []int{250, 1000, 4000}

// benchQuery is the query BenchmarkIndexSearch times: three terms, each a word
// of the filler vocabulary NewScaleVault draws every generated body from
// (testutil's unexported scaleVocab, hence spelled out here rather than
// referenced), so the corpus provably matches and Search pays for real BM25
// scoring and snippet extraction instead of returning early on an empty
// result set. Should the generator's vocabulary ever drift, the one-time
// match check in BenchmarkIndexSearch fails before a number is recorded.
const benchQuery = "attention memory throughput"

// openScaleVault generates a deterministic vault of exactly n notes into a
// fresh temp dir and opens it — the setup half of both benchmarks. It fails b
// before any timing if the vault is short of pages, so a fixture-generation
// regression can never be reported as a fast benchmark.
func openScaleVault(b *testing.B, n int) *vault.Vault {
	b.Helper()

	dir := b.TempDir()
	testutil.NewScaleVault(b, dir, n)

	v, err := vault.Open(dir)
	if err != nil {
		b.Fatalf("vault.Open(%s): %v", dir, err)
	}
	if got := len(v.Pages()); got != n {
		b.Fatalf("vault.Pages() = %d, want %d", got, n)
	}
	return v
}

// BenchmarkIndexBuild measures Build over a whole scale vault: tokenizing
// every page's body, title and tags, counting per-field term frequencies and
// keeping the raw body for snippet extraction. Vault generation and parsing
// run once, before the timer, because they are the filesystem's cost — the
// subject here is the indexing work alone.
func BenchmarkIndexBuild(b *testing.B) {
	for _, n := range benchTiers {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			b.ReportAllocs()
			v := openScaleVault(b, n)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = Build(v)
			}
		})
	}
}

// BenchmarkIndexSearch measures Search at the shape wiki.search actually
// issues it: the Options zero value, so the structural filters pass every
// document and the default limit of 20 truncates the result. One Index is
// built outside the timer — its cost is BenchmarkIndexBuild's subject — and
// the query is checked to match once, also outside the timer, since an
// assertion inside the timed loop would measure the check, not the search.
func BenchmarkIndexSearch(b *testing.B) {
	for _, n := range benchTiers {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			b.ReportAllocs()
			ix := Build(openScaleVault(b, n))

			if hits := ix.Search(benchQuery, Options{}); len(hits) == 0 {
				b.Fatalf("Search(%q) over %d pages returned no hits; the query no longer matches the generated corpus", benchQuery, n)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = ix.Search(benchQuery, Options{})
			}
		})
	}
}
