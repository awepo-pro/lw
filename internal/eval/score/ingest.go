package score

import (
	"fmt"
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

// readRefusalFmt is the whole refusal with the budget filled in and the tool
// name still a verb — a COPY of internal/agent.readBudgetRefusalFmt, pinned
// byte for byte by the same test as the needle. Only its LENGTH is used: a
// refusal whose text was never recovered is recognised by its tool event's
// result_bytes (ReadRefusals, A-049-5).
const readRefusalFmt = "%s refused: this ingest has read 6 wiki pages since it last staged a change. Stage the pages for the source now (stage.create_page / stage.patch_page) from what you have read; wiki reads are allowed again after a change is staged."

// searchRefusalNeedle is the start of 053's search-budget refusal, which no
// other tool error can begin with: 051's refusal of an identical repeat is also
// "wiki.search refused: " but goes on "this exact call", and 048's is about
// reads. It is a COPY of the head of internal/agent.searchBudgetRefusalFmt;
// TestSearchRefusalTracksAgentSource reads that file as text and fails when the
// wording moves (053).
const searchRefusalNeedle = "wiki.search refused: this ingest has searched the wiki"

// searchRefusalText is the whole refusal with the budget, 10, filled in — a
// COPY of internal/agent.searchBudgetRefusalFmt, pinned byte for byte by the
// same test. Unlike 048's it names one tool and has no verb to fill, so a
// fixed length is all a refusal whose text was never recovered needs to be
// recognised by (SearchRefusals, as ReadRefusals does for 048).
const searchRefusalText = "wiki.search refused: this ingest has searched the wiki 10 times since it last staged a change. The wiki has nothing closer than what you have found; stage the pages for the source now (stage.create_page / stage.patch_page) — a new page is right when nothing related exists. Searches are allowed again after a change is staged."

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

// ReadRefusals counts the wiki reads of turn t that 048's budget refused: the
// failed calls to a read tool whose result is the refusal. The result text is
// recovered by ToolErrors from the next round's request, and a refusal that
// ended the turn — max_rounds hit on the very round that was refused, the
// commonest way a crawl ends — has no next request, so its Text is "". For
// that case, and only that case, the tool event's result_bytes decides: it is
// len() of the refusal formatted for the call's own tool name (the name is in
// the text, and wiki.get's refusal is shorter than wiki.neighbors'). A call
// whose text WAS recovered is judged by the text alone, however many bytes it
// returned. dir and id name the turn on disk, as for ToolErrors. (049, A-049-5.)
func ReadRefusals(dir, id string, t *trace.Turn) (int, error) {
	failed, err := failedCalls(dir, id, t)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, f := range failed {
		name := canonicalTool(f.Name)
		switch {
		case !IsReadTool(name):
		case f.Text != "":
			if IsReadRefusal(f.Text) {
				n++
			}
		case f.ResultBytes == len(fmt.Sprintf(readRefusalFmt, name)):
			n++
		}
	}
	return n, nil
}

// IsSearchRefusal reports whether a tool result's text is 053's refusal of a
// wiki.search over the ingest search budget. As with IsReadRefusal, ToolErrors
// keeps only the first 200 runes and the needle sits in the first 55, so the
// cut text still matches.
func IsSearchRefusal(text string) bool {
	return strings.Contains(text, searchRefusalNeedle)
}

// SearchRefusals counts the wiki.search calls of turn t that 053's budget
// refused: the failed calls to wiki.search whose result is the refusal. It
// reads the trace as ReadRefusals does, and for the same reason falls back to
// the tool event's result_bytes — the length of the refusal — when the call was
// its turn's last round, no next request exists and the text is "": a crawl
// that ends on the refusal is the commonest way one ends. A call whose text WAS
// recovered is judged by the text alone. search_calls counts every call, the
// refused ones too; this is the part of it the engine said no to. (053.)
func SearchRefusals(dir, id string, t *trace.Turn) (int, error) {
	failed, err := failedCalls(dir, id, t)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, f := range failed {
		if canonicalTool(f.Name) != "wiki.search" {
			continue
		}
		switch {
		case f.Text != "":
			if IsSearchRefusal(f.Text) {
				n++
			}
		case f.ResultBytes == len(searchRefusalText):
			n++
		}
	}
	return n, nil
}
