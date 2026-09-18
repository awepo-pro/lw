package extract

import (
	"context"
	"fmt"

	"github.com/awepo-pro/lw/internal/slug"
)

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

// chain is the Extractor Chain returns.
type chain struct {
	extractors []Extractor
}

// Chain returns an Extractor that tries each of es, in order, delegating
// to the first whose CanHandle reports true. Order is significant and
// never reshuffled, so the same argument list always resolves to the same
// extractor (backbone §10's determinism contract).
func Chain(es ...Extractor) Extractor {
	return &chain{extractors: es}
}

// CanHandle reports whether any extractor in c can handle uri.
func (c *chain) CanHandle(uri string) bool {
	for _, e := range c.extractors {
		if e.CanHandle(uri) {
			return true
		}
	}
	return false
}

// Extract delegates to the first extractor in c whose CanHandle reports
// true, or fails with a clear error if none does.
func (c *chain) Extract(ctx context.Context, uri string) (*Doc, error) {
	for _, e := range c.extractors {
		if e.CanHandle(uri) {
			return e.Extract(ctx, uri)
		}
	}
	return nil, fmt.Errorf("extract: no extractor can handle %q", uri)
}

// SuggestPath returns the vault-relative raw/ path a freshly extracted Doc
// should be staged under: "raw/<kind>s/<slugified-title>.md" (backbone
// §10). An empty or unrecognized Kind is treated as "article", and an
// empty or unslugifiable Title falls back to "untitled" — stage.
// ingest_source's own dedupe-by-hash still catches a genuine duplicate;
// this is only ever a suggestion the caller may override.
func SuggestPath(d *Doc) string {
	return "raw/" + kindDir(d.Kind) + "/" + suggestSlug(d.Title) + ".md"
}

// kindDir maps a Doc.Kind to its raw/ subdirectory, defaulting to
// "articles" for "" or anything other than the three the schema
// recognizes (backbone §10: "article" | "paper" | "transcript").
func kindDir(kind string) string {
	switch kind {
	case "paper":
		return "papers"
	case "transcript":
		return "transcripts"
	default:
		return "articles"
	}
}

// suggestSlug turns a title into the filename fragment SuggestPath builds
// with: internal/slug.Make, the one slug rule the whole tree shares
// (A-805), so the suggestion is always a path the validator accepts. It
// falls back to "untitled" when nothing survives — a title with no Latin
// letters or digits (四元數簡介, "!!!") still needs a stable, non-empty
// filename.
func suggestSlug(title string) string {
	if s := slug.Make(title); s != "" {
		return s
	}
	return "untitled"
}
