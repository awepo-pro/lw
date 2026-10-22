package tools

import (
	"strings"
)

// StagedRead is the D4 predicate: reports whether tool's execution read
// unreviewed staged content from the open changeset.
// tool may be canonical ("wiki.get") or wire ("wiki_get").
func StagedRead(tool, content string) bool {
	switch CanonicalName(tool) {
	case "wiki.get", "raw.get":
		return strings.HasPrefix(content, "(staged in the open changeset, not yet committed)")
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
