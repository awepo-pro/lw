package score

import (
	"strings"

	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/trace"
)

// readRefusalNeedle is the part of 048's read-budget refusal that no other
// tool error can contain. It is a COPY: the text is
// internal/agent.readBudgetRefusalFmt ("%s refused: this ingest has read %d
// wiki pages since it last staged a change. …") with the budget, 6, filled
// in — this package never imports the agent, so
// TestReadRefusalNeedleTracksAgentSource reads that file as text and fails
// when the wording or the budget moves (049).
const readRefusalNeedle = "refused: this ingest has read 6 wiki pages"

// readTools are the tools that read a wiki page and so spend 048's budget,
// by canonical name (agent.budgetedReads). wiki.search returns snippets and
// raw.* reads the source: neither is a page read.
var readTools = map[string]bool{
	"wiki.get":       true,
	"wiki.neighbors": true,
	"wiki.backlinks": true,
}

// IsReadTool reports whether canonical names a tool that reads a wiki page.
func IsReadTool(canonical string) bool { return readTools[canonical] }

// isPageChange reports whether canonical, when it succeeds, puts a change to
// a page in the changeset: every stage.* tool except ingest_source (stages
// the raw source), open (starts the changeset) and close (summarizes it). It
// is agent.isPageChange's rule, restated here because the scorer must
// recognise the same event the engine reset its read count on (048): a stage
// tool added later is a page change here exactly when it is one there.
func isPageChange(canonical string) bool {
	return strings.HasPrefix(canonical, "stage.") &&
		canonical != "stage.ingest_source" && canonical != "stage.open" && canonical != "stage.close"
}

// canonicalTool is the registry spelling of a tool name read from a trace.
// The agent loop records canonical names ("stage.create_page"), while a
// response's tool_calls echo the wire names the provider saw
// ("stage_create_page"). tools.CanonicalName maps the wire form but is not
// idempotent: given the canonical "stage.ingest_source" it would answer
// "stage.ingest.source" and every comparison below would quietly fail. A
// name that already holds a dot is therefore canonical as it stands — a
// wire name cannot carry one, providers reject it.
func canonicalTool(name string) string {
	if strings.Contains(name, ".") {
		return name
	}
	return tools.CanonicalName(name)
}

// eachCall calls f with the canonical name and failure flag of every tool
// event of turns, in trace order — turns in order, then each turn's attempts,
// then each attempt's calls — and stops when f returns false. A nil turn has
// no calls. The calls are the TOOL EVENTS, which exist for every dispatched
// call, refused ones included; the model's arguments are not in them.
func eachCall(turns []*trace.Turn, f func(name string, failed bool) bool) {
	for _, t := range turns {
		if t == nil {
			continue
		}
		for _, a := range t.Attempts {
			for _, c := range a.Calls {
				if !f(canonicalTool(c.Name), c.IsError) {
					return
				}
			}
		}
	}
}

// ReadsBeforeFirstStage counts the wiki read calls (wiki.get, wiki.neighbors,
// wiki.backlinks) an ingest made before it staged its first page change,
// across turns in trace order. Reads count whether or not 048's budget
// refused them: a refused read is still a round spent crawling, and the
// number is a measure of the crawl, not of what the engine let through. A
// page change is a call that succeeded; a failed stage call changed nothing.
// With no page change at all every read counts. (049.)
func ReadsBeforeFirstStage(turns []*trace.Turn) int {
	reads := 0
	eachCall(turns, func(name string, failed bool) bool {
		if !failed && isPageChange(name) {
			return false
		}
		if readTools[name] {
			reads++
		}
		return true
	})
	return reads
}

// Closed reports whether any turn made a successful stage.close call — the
// model said it was done, rather than running out of rounds. A close that
// came back as an error did not end anything. (049.)
func Closed(turns []*trace.Turn) bool {
	closed := false
	eachCall(turns, func(name string, failed bool) bool {
		if name == "stage.close" && !failed {
			closed = true
			return false
		}
		return true
	})
	return closed
}

// CallCount counts the tool calls named canonical (for example
// "wiki.search"), failed ones included: a search that errored was still a
// search the model chose to make. The name in the trace may be either
// spelling; both match. (049.)
func CallCount(turns []*trace.Turn, canonical string) int {
	n := 0
	eachCall(turns, func(name string, _ bool) bool {
		if name == canonical {
			n++
		}
		return true
	})
	return n
}

// IsReadRefusal reports whether a tool result's text is 048's refusal of a
// wiki read over the ingest budget. ToolErrors keeps only the first 200 runes
// of a result, and the needle sits in the first 80 of the refusal, so the
// cut text still matches.
func IsReadRefusal(text string) bool {
	return strings.Contains(text, readRefusalNeedle)
}
