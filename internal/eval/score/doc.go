// Package score is the eval harness's mechanical judge (037 T2): given what a
// model wrote and the trace of how it got there, it computes the numbers a
// run is compared on — which expected facts the text contains, whether its
// citations resolve, whether it abstained, how much of a source it read, what
// its tool calls cost. Nothing here calls a model or touches the network;
// every function is a pure function of its arguments (or of one trace
// directory), so scoring the same run twice gives the same numbers and any
// difference between two runs is the model's, never the scorer's.
//
// The package is dev-only. cmd/lw and every shipped package must not import
// it: the invariants lw ships on (no filesystem verbs for the agent, hunk
// review before anything lands) are untouched by an accuracy harness, and
// keeping it unlinked keeps it that way.
package score
