package tools

import (
	"context"
	"strings"

	"github.com/awepo-pro/lw/internal/trace"
)

// readOnlyVerb reports whether ctx was started with a read-only verb
// (ask, query, file) for which projected search, the orient open changeset
// section, and the citable staged marker apply (067 FIX-W3).
func readOnlyVerb(ctx context.Context) bool {
	switch trace.VerbFrom(ctx) {
	case trace.VerbAsk, trace.VerbQuery, trace.VerbFile:
		return true
	default:
		return false
	}
}

// StagedRead is the D4 predicate: reports whether tool's execution read
// unreviewed staged content from the open changeset.
// tool may be canonical ("wiki.get") or wire ("wiki_get").
func StagedRead(tool, content string) bool {
	switch CanonicalName(tool) {
	case "wiki.get", "raw.get":
		return strings.HasPrefix(content, stagedSourceMarker) ||
			strings.HasPrefix(content, stagedCitableMarker) ||
			strings.HasPrefix(content, "(staged in the open changeset, not yet committed)")
	case "wiki.search":
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimRight(line, "\r")
			if strings.HasPrefix(line, "   (staged in cs-") && strings.HasSuffix(line, ", unreviewed)") {
				return true
			}
		}
		return false
	default:
		return false
	}
}
