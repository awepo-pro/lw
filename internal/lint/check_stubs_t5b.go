// REPLACED BY S1-T5b — do not extend.
//
// check_stubs_t5b.go is the one sanctioned temporary shim in the build
// (00-conventions.md; MASTER §9 D-V, S1 correction C-4). S1-T5a owns
// scaffolding and checks 1-7; S1-T5b owns checks 8-14 and, once real,
// deletes this file. It exists only so the package compiles between the two
// sequential dispatches — same package, one agent at a time
// (00-conventions.md §1 rule 5a).
//
// Each stub reports its real ID, Describe and Severity so All() stays in
// backbone §4 table order and Run(ctx, only) filtering works against the
// full 14; only Run(*Context) is a no-op, returning nil findings.
package lint

// stubCheck is a Check whose Run never fires. See the file comment above.
type stubCheck struct {
	id       string
	describe string
	severity Severity
}

func (s stubCheck) ID() string             { return s.id }
func (s stubCheck) Describe() string       { return s.describe }
func (s stubCheck) Severity() Severity     { return s.severity }
func (s stubCheck) Run(*Context) []Finding { return nil }

// newSrcIntegrity stubs check 8, src-integrity — owned by S1-T5b.
func newSrcIntegrity() Check {
	return stubCheck{
		id:       "src-integrity",
		describe: "a sources: entry is missing from raw/, or its body sha256 differs from the frontmatter sha256 (drift)",
		severity: SevError,
	}
}

// newSrcProvenance stubs check 9, src-provenance — owned by S1-T5b.
func newSrcProvenance() Check {
	return stubCheck{
		id:       "src-provenance",
		describe: "a page with sources: carries no ^[raw/...] provenance marker",
		severity: SevWarn,
	}
}

// newIndexSync stubs check 10, index-sync — owned by S1-T5b.
func newIndexSync() Check {
	return stubCheck{
		id:       "index-sync",
		describe: "index.md and the wiki/ page set are not 1:1",
		severity: SevError,
	}
}

// newSizeSplit stubs check 11, size-split — owned by S1-T5b.
func newSizeSplit() Check {
	return stubCheck{
		id:       "size-split",
		describe: "body exceeds 200 lines -> split candidate",
		severity: SevInfo,
	}
}

// newFMQuality stubs check 12, fm-quality — owned by S1-T5b.
func newFMQuality() Check {
	return stubCheck{
		id:       "fm-quality",
		describe: "confidence: low, contested: true, or a single source with no confidence set",
		severity: SevInfo,
	}
}

// newSrcStale stubs check 13, src-stale — owned by S1-T5b.
func newSrcStale() Check {
	return stubCheck{
		id:       "src-stale",
		describe: "a page's updated is more than 90 days earlier than the ingested date of a source it cites",
		severity: SevWarn,
	}
}

// newLogRotate stubs check 14, log-rotate — owned by S1-T5b.
func newLogRotate() Check {
	return stubCheck{
		id:       "log-rotate",
		describe: "log.md exceeds 500 entries",
		severity: SevInfo,
	}
}
