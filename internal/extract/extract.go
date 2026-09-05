package extract

import "context"

// Doc is one source turned into deterministic markdown, ready for
// stage.ingest_source to write under raw/ (backbone §10).
type Doc struct {
	Title     string
	SourceURL string
	Markdown  string
	Kind      string // "article" | "paper" | "transcript"
	Extractor string // "go/html" | "passthrough"
}

// Extractor turns a URI into a Doc. Implementations must be deterministic:
// the same input bytes always produce the same markdown, or the sha256
// dedupe in stage.ingest_source is meaningless (backbone §10).
//
// Only the interface and Doc are declared here. NewHTML, NewFile, Chain and
// SuggestPath belong to S5-T4 (backbone §6 correction C-72) — writing them
// now would hand that subtask a file it does not own. internal/tools.Deps
// needs this interface to compile (backbone §6's Deps.Extract field) even
// though no S3 subtask implements a concrete Extractor.
type Extractor interface {
	CanHandle(uri string) bool
	Extract(ctx context.Context, uri string) (*Doc, error)
}
