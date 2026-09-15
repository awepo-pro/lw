// Package conformance is lw's frozen-grid gate (workflow 003, T13; contract
// §9): a test-only package that drives the real TUI — wired exactly as
// cmd/lw's buildTUIOptions wires it — to each mockup's state on a private
// copy of the mockup vault, and compares the screen cell for cell against
// the frozen ASCII grids in plans/003-mockups/ascii.
//
// The gate is red-first by design: the screens it judges are rewritten in a
// later wave, so at gate G2 exactly the 24 screen grids must fail, each
// with a layout diff and never a harness error, while the two too-small
// notice grids pass. That red state is itself frozen (MASTER §5 T13): a
// subtest that passes when it should fail is a defect, and so is "fixing" a
// screen or loosening the comparator to turn one green. TestGridCompare-
// SelfCheck proves the comparator on the grids alone, so a red subtest
// always indicts the screen, not the gate.
//
// The grids and the vault are read-only private inputs (00-conventions.md
// §1 rule 7): the vault holds third-party text and never enters the repo,
// and the gate reaches both only through the environment — LW_MOCKUP_VAULT
// names the vault, LW_MOCKUP_GRIDS the grids (defaulting to the metadata
// tree's plans/003-mockups/ascii beside it). With the vault unset both
// tests skip, so CI never sees them; the gate is run by implementers and
// the orchestrator.
//
// The package has no non-test files. doc_test.go carries the package
// comment because a test-only package has no other home for one.
package conformance
