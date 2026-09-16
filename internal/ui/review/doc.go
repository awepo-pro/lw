// Package review is the changeset review screen, rebuilt on the 003 frame
// (workflow 003, s2-screens.md T06): an Ops panel walking the changeset's
// ops, a Changeset panel summarizing it, and one focused Detail panel that
// flips between the op's Diff (Engine.OpDiff windows, attributed per hunk)
// and a Preview of the staged page (the shared markdown renderer). Accept,
// drop, accept-all, reject and commit go through stage.Engine exactly as
// before; the lint-regression commit gate stays here as LintBaseline,
// which cmd/lw imports directly (S6-C127, TD-3).
//
// Layout is the frozen grids in plans/003-mockups/ascii (review-*,
// review-preview-*, keys-*), drawn through ui.Panel; every mutation goes
// through stage.Engine and nothing here writes a vault file directly.
package review
