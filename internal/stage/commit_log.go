// commit_log.go renders the log.md entry line Commit's step 8 appends (008
// contract §3). With live ingest_source ops the line names every raw path
// in op order behind a U+2192 arrow; without one it is byte-identical to
// the pre-008 format, because existing log.md lines are never rewritten.
package stage

import (
	"fmt"
	"strings"
	"time"
)

// rawIngestPaths returns the vault-relative path of every live ingest_source
// op in live, in op order — the arrow clause's operands (008 contract §3).
// Dropped and rejected ops never reach here: callers pass c.Live().
func rawIngestPaths(live []Op) []string {
	var out []string
	for _, op := range live {
		if op.Kind == OpIngestSource {
			out = append(out, op.Path)
		}
	}
	return out
}

// commitLogLine renders one log.md entry line: "- YYYY-MM-DD HH:MM <commit>
// <intent> → <raw path>[, <raw path>…] (+N pages, ~M edits)" when raws is
// non-empty, and today's "- YYYY-MM-DD HH:MM <commit> <intent> (+N pages,
// ~M edits)" when it is not. The arrow is U+2192 with one space on each
// side.
func commitLogLine(now time.Time, commitID, intent string, raws []string, creates, edits int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- %s %s %s", now.Format("2006-01-02 15:04"), commitID, intent)
	if len(raws) > 0 {
		b.WriteString(" → ")
		b.WriteString(strings.Join(raws, ", "))
	}
	fmt.Fprintf(&b, " (+%d pages, ~%d edits)", creates, edits)
	return b.String()
}
