package lint_test

import (
	"strconv"
	"testing"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// benchTiers are the note counts every benchmark in this file runs at: one
// small vault, one mid-sized one, one that is awkward to keep open in an
// editor. They are named in benchmark output as BenchmarkX/<n>.
func benchTiers() []int {
	return []int{250, 1000, 4000}
}

// openScaleContext writes a deterministic testutil scale vault of n notes
// into root and builds the lint.Context over it exactly the way
// cmd/lw/cmd_lint.go does for `lw lint` — vault.Open, then index.Build over
// that vault, then the vault's own wikilink graph — minus the --fix branch,
// which needs an engine and an LLM and is not what a read path benchmark
// measures. It fails tb on any I/O or parse error: a scale vault is
// well-formed by construction, so either would mean the benchmark is not
// measuring what it claims to.
//
// Everything here is setup cost (vault generation is O(n) file writes); the
// callers ResetTimer before their timed loop, so none of it lands in the
// reported ns/op.
func openScaleContext(tb testing.TB, root string, n int) *lint.Context {
	tb.Helper()

	testutil.NewScaleVault(tb, root, n)

	v, err := vault.Open(root)
	if err != nil {
		tb.Fatalf("vault.Open(%s): %v", root, err)
	}
	if got := len(v.Pages()); got != n {
		tb.Fatalf("vault.Pages() = %d pages, want exactly %d", got, n)
	}

	return &lint.Context{
		Vault: v,
		Index: index.Build(v),
		Graph: v.Graph(),
	}
}

// BenchmarkLintRun measures a full lint pass over a scale vault: all 14
// checks (only == nil, the same thing `lw lint` with no --checks flag does),
// the combined sort, and the ByCheck grouping. The Context — vault, index,
// graph — is built once outside the timed loop, so the number reported is
// the cost of re-checking an already-loaded vault, which is what a curator
// pays on every `lw lint` after the vault is read.
//
// A generated vault is dirty by design only at info severity (oversized
// bodies), so the pass is representative work, not a shortcut through an
// empty vault: every check runs and every finding is sorted and grouped.
func BenchmarkLintRun(b *testing.B) {
	for _, n := range benchTiers() {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			ctx := openScaleContext(b, b.TempDir(), n)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = lint.Run(ctx, nil)
			}
		})
	}
}
