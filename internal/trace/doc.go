// Package trace records every agent turn to disk (038, turn trace): the
// exact request bytes lw POSTed each round, what the model streamed back,
// the provider's token usage, the timings, and what the loop did between
// rounds. One turn is one directory under <vault>/.llmwiki/traces/, named by
// the turn id that also tags the turn's lw.log records and session records.
//
// Tracing is observation only: nothing here can change what is sent, and a
// trace write failure is logged once and never fails the turn.
package trace
