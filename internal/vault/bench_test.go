package vault

import (
	"strconv"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// benchTiers are the note counts every benchmark in this file runs at: one
// small vault, one mid-sized one, one that is awkward to keep open in an
// editor. They are named in benchmark output as BenchmarkX/<n>.
func benchTiers() []int {
	return []int{250, 1000, 4000}
}

// writeScaleVault writes a deterministic testutil scale vault of n notes into
// root and checks the parts of the load contract the timed loop below leans
// on: the tree parses with zero ParseErrors and holds exactly n pages (the
// root bookkeeping files are not pages, and a scale vault carries no raw
// sources). It fails b otherwise — either would mean the benchmark reports
// the cost of an unexpected vault rather than of scale.
func writeScaleVault(b *testing.B, root string, n int) {
	b.Helper()

	testutil.NewScaleVault(b, root, n)

	v, err := Open(root)
	if err != nil {
		b.Fatalf("Open(%s): %v", root, err)
	}
	if got := len(v.Pages()); got != n {
		b.Fatalf("Pages() = %d pages, want exactly %d", got, n)
	}
	if errs := v.ParseErrors(); len(errs) != 0 {
		b.Fatalf("ParseErrors() = %d entries, want 0: %v", len(errs), errs)
	}
}

// BenchmarkVaultOpen measures a cold Open of a scale vault: reading SCHEMA.md
// and every *.md under wiki/ and raw/ off os.DirFS, parsing each, and building
// the wikilink graph (vault.go's Open → OpenFS(os.DirFS(root)) → Reload). This
// is the cost a curator pays before anything else in lw can run, and it grows
// with the number of notes, so the three tiers are the interesting axis.
//
// Vault generation happens once per tier before ResetTimer, so the reported
// ns/op is pure open cost, not fixture-writing cost.
func BenchmarkVaultOpen(b *testing.B) {
	for _, n := range benchTiers() {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			root := b.TempDir()
			writeScaleVault(b, root, n)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := Open(root); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
