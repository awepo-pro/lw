// ingest_dedupe.go is the body-hash half of validateIngestSource's dedupe
// (008 contract §3). Since TD-5 the ingest tool proposes the WHOLE
// serialized raw file — frontmatter included — so hashing op.Content whole
// could never match a committed RawSource.SHA256, which records the sha of
// the body alone. The check had gone dead for real tool output; this
// restores it without breaking the bare-body shape the validator's own
// tests propose.
package stage

import "github.com/awepo-pro/lw/internal/vault"

// ingestBodySHA returns the sha validateIngestSource compares against every
// committed RawSource.SHA256: the sha of the body parsed out of op.Content
// when op.Content parses as a raw source, and the sha of op.Content whole
// otherwise — the pre-008 behaviour. The path argument to ParseRawSource
// feeds its error text only, which this comparison discards.
func ingestBodySHA(content []byte) string {
	if src, err := vault.ParseRawSource("raw/proposed/proposal.md", content); err == nil {
		return vault.BodySHA256(src.Body)
	}
	return vault.BodySHA256(string(content))
}
