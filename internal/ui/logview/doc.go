// Package logview implements the journal/log screen (backbone §12 Pane,
// §5.7 Journal/Event/Filter, §5.8 Revert): the journal rendered newest-last
// in one focused Events panel (003 contract §5's frame, s2-screens.md T10),
// filterable by five stage.Filter queries cycled with `f`, and `r`
// reverting a commit-bearing event into a new reviewable changeset.
//
// Every mutation goes through stage.Engine; nothing here writes to the
// vault directly (00-conventions.md §5.4).
package logview
