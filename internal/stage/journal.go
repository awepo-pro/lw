// journal.go, at this wave, holds only what engine.go needs to compile:
// the Journal type and OpenJournal. Engine (D-AO) holds a `journal
// *Journal` field, so the type name must exist in the package before
// engine.go can build — but §5.7's Event/Filter/EventKind and Journal's
// Append/Query/Last belong to S2-T3 (backbone §5's "where the shared
// declarations live" Contract, MASTER §9 D-AQ, correction C-20).
//
// S2-T1 creates this file with a real, working minimum — not a stub:
// OpenJournal records the path and ensures the file exists. Ownership
// transfers to S2-T3 from wave 2, which adds Append, Query, Last and the
// Event/Filter/EventKind declarations, and may restructure this file
// freely.
package stage

import (
	"fmt"
	"os"
)

// Journal is the vault's append-only event log at .llmwiki/journal.ndjson.
//
// Contract (backbone §5.7, MASTER §9 D-AS "no persistent handle"): a
// *Journal holds the path, not an open *os.File. Append (S2-T3) opens
// O_APPEND|O_CREATE|O_WRONLY, writes one line, fsyncs and closes, on every
// call — which is why Journal has no Close method and Engine.Close has no
// journal step.
type Journal struct {
	path string
}

// OpenJournal opens the journal at path (.llmwiki/journal.ndjson),
// creating it if it does not already exist. It does not keep the file
// open.
func OpenJournal(path string) (*Journal, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("stage: open journal %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("stage: open journal %s: %w", path, err)
	}
	return &Journal{path: path}, nil
}
