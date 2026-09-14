// bench_test.go benchmarks the staging engine's three hot paths — Append,
// ProjectedReport and Commit — over the scale vaults task 1's
// testutil.NewScaleVault generates. It exists because the S2 audit's D-AD
// question ("what does a full reparse per Append actually cost?") was raised
// and never measured: projectedTree walks the whole working tree from disk
// and re-parses every page on every call, and Append calls it once per op
// through recomputeChecks — so a changeset of k ops costs k whole-vault
// reparses. These numbers put a per-op price on that.
//
// Method, identical in all three benchmarks: build ONE pristine tier vault
// per tier (the template, outside every timer), then per iteration copy it
// with os.CopyFS into a fresh b.TempDir() under b.StopTimer() and open a
// fresh Engine on the copy. No iteration therefore inherits another's
// changeset, CAS, journal or index — each measures the cold-start path a
// real `lw stage` process takes, and the timed window holds only the calls
// named in the benchmark.
package stage

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// benchAuthor is who every benchmark proposes as — a human reviewer, the
// shape changeset.go's Author comment gives as the non-agent half of the
// enum. No Model or Session: those name an agent run, and a benchmark has
// neither.
var benchAuthor = Author{Kind: "human"}

// benchDate is the created/updated date stamped on every benchmark page's
// frontmatter. It matches testutil's own scaleDate, so a bench page and the
// notes around it agree on "now" and neither reads the real clock
// (00-conventions.md §3).
const benchDate = "2026-08-29"

// benchProjectionOps is the number of create_page ops the changeset carries
// when ProjectedReport is measured — enough for the projection to do real op
// work (including the index.md derivation liveCreatePages triggers) without
// the setup dominating the tier.
const benchProjectionOps = 5

// benchAppendOps is the number of create_page ops BenchmarkStageAppend
// appends per iteration — and one iteration is one op of the benchmark's
// b.N, so the reported ns/op, B/op and allocs/op are benchAppendOps times
// the per-Append price (the ns/append column the benchmark reports is the
// per-Append number itself). A fixed count, deliberately NOT the tier: the
// tier names the vault's size (as it does in the other two benchmarks), and
// the per-Append price it measures is the D-AD cost of one Append at that
// size. Twenty ops sample that mean well while keeping the whole suite inside
// its few-minute budget — one op per note would re-multiply the per-op cost
// by the tier and push the 1000 tier alone past ten minutes.
const benchAppendOps = 20

// benchCommitOps is the number of create_page ops Commit applies. Twenty, not
// two: Commit's step 2 Refresh re-projects the tree once per live op
// (cascadeBase over liveBefore, OR-13) on top of the projection
// buildCommitMaterialization shares with them, and a two-op changeset would
// hide that term entirely.
const benchCommitOps = 20

// benchTemplate builds one pristine n-note vault under a fresh temporary
// directory and returns its path. The template is generated once per tier and
// never opened by an Engine, so every iteration copies a vault with no
// .llmwiki/ in it — OpenEngine creates that layout itself, exactly as it does
// on a real first run.
func benchTemplate(b *testing.B, n int) string {
	b.Helper()

	tmpl := filepath.Join(b.TempDir(), "template")
	testutil.NewScaleVault(b, tmpl, n)
	return tmpl
}

// benchVault copies the template vault into a fresh temporary directory and
// returns the copy's root. os.CopyFS (Go 1.23+) copies the template's
// contents into the empty root, so root is the vault root OpenEngine expects.
// The caller stops the timer around it: the copy is per-iteration
// scaffolding, not the thing being measured.
func benchVault(b *testing.B, tmpl string) string {
	b.Helper()

	root := b.TempDir()
	if err := os.CopyFS(root, os.DirFS(tmpl)); err != nil {
		b.Fatalf("stage bench: copy template %s -> %s: %v", tmpl, root, err)
	}
	return root
}

// benchEngine opens an Engine over root, failing the benchmark on error — a
// vault the tier generated but the engine cannot open is a broken benchmark,
// not a slow one.
func benchEngine(b *testing.B, root string) *Engine {
	b.Helper()

	e, err := OpenEngine(root)
	if err != nil {
		b.Fatalf("stage bench: OpenEngine(%s): %v", root, err)
	}
	return e
}

// benchChangeset opens the iteration's one changeset, failing the benchmark
// on error. changesets/open/ is empty on a fresh copy, so ErrOpenChangeset
// here would mean the copy was not fresh.
func benchChangeset(b *testing.B, e *Engine, intent string) {
	b.Helper()

	if _, err := e.OpenChangeset(intent, benchAuthor); err != nil {
		b.Fatalf("stage bench: OpenChangeset(%q): %v", intent, err)
	}
}

// benchFill appends the first k benchmark ops to the open changeset, then
// checks the count against e.open — the in-memory changeset Append just
// extended (currentOpen returns the e.open pointer OpenChangeset set, and
// nothing reloads it from disk), so it is the changeset the timed region
// actually works on. A mismatch means the setup half-failed and the tier
// measures nothing.
//
// That per-op Append error check sits inside BenchmarkStageAppend's timed
// region deliberately: ~1 ns against a ≥167 ms op, and the only guard
// against silently benchmarking rejected ops.
func benchFill(b *testing.B, e *Engine, k, n int) {
	b.Helper()

	for i := 0; i < k; i++ {
		id, err := e.Append(benchCreateOp(i))
		if err != nil {
			b.Fatalf("stage bench: append op %d into a %d-note vault: %v", i, n, err)
		}
		if i == 0 && id == "" {
			b.Fatalf("stage bench: first Append returned an empty op id")
		}
	}
	if got := len(e.open.Ops); got != k {
		b.Fatalf("stage bench: changeset carries %d ops, want %d", got, k)
	}
}

// benchPageBody renders the post-image of a benchmark create_page:
// frontmatter valid against the scale vault's SCHEMA.md taxonomy (the key set
// testutil's own generator emits) and two outbound wikilinks to generated
// notes, which is the shape validateCreatePage demands of every create_page.
// The links target note-0000 and note-0001, which every tier of n >= 2
// contains, so they resolve rather than manufacture a broken-link finding.
func benchPageBody(title string) []byte {
	return []byte("---\n" +
		"title: " + title + "\n" +
		"created: " + benchDate + "\n" +
		"updated: " + benchDate + "\n" +
		"type: concept\n" +
		"tags: [inference, decoding]\n" +
		"confidence: high\n" +
		"---\n" +
		"\n" +
		"# " + title + "\n" +
		"\n" +
		"See [[note-0000]] and [[note-0001]] for the neighbouring pages.\n")
}

// benchCreateOp builds the i-th create_page op of a benchmark changeset: a
// NEW path under wiki/concepts/ that cannot collide with a generated
// note-XXXX.md, the rationale and provenance the schema's per-kind required
// set demands (MASTER §9 D-BH), and the post-image in Op.Content — the only
// channel raw bytes reach the engine through (MASTER §9 D-AY).
func benchCreateOp(i int) Op {
	return Op{
		Kind:       OpCreatePage,
		Path:       fmt.Sprintf("wiki/concepts/bench-%04d.md", i),
		Rationale:  "bench: one more page, to price the staging engine's per-op work",
		Provenance: []string{fmt.Sprintf("bench/%04d", i)},
		State:      StateProposed,
		Content:    benchPageBody(fmt.Sprintf("Bench %04d", i)),
	}
}

// BenchmarkStageAppend prices Append — validate, CAS put, assignIDs, the
// whole-vault recomputeChecks, the changeset.json rewrite and the journal
// append — over an n-note vault. Each iteration opens a fresh copy and
// appends the same benchAppendOps ops to it, so ONE benchmark iteration is
// benchAppendOps appends: ns/op, B/op and allocs/op are that many times the
// per-Append price, and the ns/append column below is the per-Append number
// itself. The ratio between the two tiers is D-AD's per-op reparse term
// scaling with the vault rather than with the changeset.
func BenchmarkStageAppend(b *testing.B) {
	for _, n := range []int{250, 1000} {
		b.Run(fmt.Sprintf("%d", n), func(b *testing.B) {
			tmpl := benchTemplate(b, n)
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				b.StopTimer()
				root := benchVault(b, tmpl)
				e := benchEngine(b, root)
				benchChangeset(b, e, "bench append")
				b.StartTimer()

				benchFill(b, e, benchAppendOps, n)

				b.StopTimer()
			}

			// One iteration is benchAppendOps appends, so the per-Append price
			// is the measured time over b.N*benchAppendOps appends — reported
			// here so nobody has to divide ns/op by hand. B/op and allocs/op
			// divide by benchAppendOps the same way.
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/(float64(b.N)*float64(benchAppendOps)), "ns/append")
		})
	}
}

// BenchmarkStageProjectedReport prices ProjectedReport on a changeset that
// already carries ops: walkWholeTree from disk, apply every live op in
// memory, open the result as a Vault and run all fourteen lint checks. It is
// read-only — no lock, nothing written — so the tier measures pure analysis:
// the number `lw commit` pays once more than Append's own Checks already
// cost it.
func BenchmarkStageProjectedReport(b *testing.B) {
	for _, n := range []int{250, 1000, 4000} {
		b.Run(fmt.Sprintf("%d", n), func(b *testing.B) {
			tmpl := benchTemplate(b, n)
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				b.StopTimer()
				root := benchVault(b, tmpl)
				e := benchEngine(b, root)
				benchChangeset(b, e, "bench projected report")
				benchFill(b, e, benchProjectionOps, n)
				b.StartTimer()

				// The error check sits inside the timed region deliberately:
				// ~1 ns against a ≥167 ms call, and the only guard against
				// silently benchmarking rejected ops.
				if _, err := e.ProjectedReport(); err != nil {
					b.Fatalf("stage bench: ProjectedReport on a %d-note vault: %v", n, err)
				}
				b.StopTimer()

				if err := e.Close(); err != nil {
					b.Fatalf("stage bench: Close: %v", err)
				}
			}
		})
	}
}

// BenchmarkStageCommit prices one Commit of a benchCommitOps-op changeset:
// the lock, Refresh (a projection per live op), the materialization, the CAS
// puts, the whole-vault snapshots, the reload, the index save and the log
// append. The twenty appends that fill the changeset stay outside the timer —
// they are BenchmarkStageAppend's subject — so ns/op is the cost of landing a
// changeset a reviewer has already approved.
func BenchmarkStageCommit(b *testing.B) {
	for _, n := range []int{250, 1000} {
		b.Run(fmt.Sprintf("%d", n), func(b *testing.B) {
			tmpl := benchTemplate(b, n)
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				b.StopTimer()
				root := benchVault(b, tmpl)
				e := benchEngine(b, root)
				benchChangeset(b, e, "bench commit")
				benchFill(b, e, benchCommitOps, n)
				b.StartTimer()

				id, err := e.Commit("bench")
				if err != nil {
					b.Fatalf("stage bench: Commit on a %d-note vault: %v", n, err)
				}
				b.StopTimer()

				// A fresh template copy has an empty snapshots/, so Commit's
				// id is deterministically "000001" — asserting it turns
				// "Commit probably landed" into an observable.
				if id == "" {
					b.Fatalf("stage bench: Commit returned an empty changeset id")
				}

				// Closed outside the timer, as BenchmarkStageProjectedReport
				// closes its engine: symmetry keeps the two benchmarks'
				// scaffolding identical, and the unlock plus index flush belong
				// to no iteration's measurement.
				if err := e.Close(); err != nil {
					b.Fatalf("stage bench: Close: %v", err)
				}
			}
		})
	}
}
