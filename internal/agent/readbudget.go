package agent

// readbudget.go is 048's engine-enforced read budget for an ingest turn.
//
// The measured failure: a real ingest of three small articles made 51 tool
// calls and staged nothing — it called wiki.get on all 37 wiki pages in index
// order and hit max_rounds — and every one of 18 eval ingest runs read 14-40
// wiki pages before staging its first. 040's prompt line ("read only the pages
// wiki.search shows are related") and its round-budget nudge did not move it:
// one trace read 20 pages in a row, three of them after the nudge. On this
// model prompt text does not bound reading, so the loop does: an ingest turn
// that has read ingestReadBudget wiki pages since it last staged a page change
// has its next read refused until it stages one.

import (
	"fmt"
	"strings"
)

// ingestReadBudget is how many wiki page reads an ingest turn may make between
// page changes (048). The pages related to one source are what wiki.search
// returns — 5 and 3 results in the measured trace — so six is above every
// relevant set seen and far below the 14-40 the crawl makes. A patch flow (read
// a page, patch it, read the next) never reaches it: each patch resets the
// count.
const ingestReadBudget = 6

// readBudgetRefusalFmt is the refusal a read over the budget gets, with the
// canonical tool name and the budget as its two verbs. It tells the model what
// to do next — stage from what it has read — and that the refusal is not
// permanent, because a model told only "no" kept calling the same tool.
// The wording is a frozen contract: the bytes ride the wire and are pinned by
// readbudget_test.go.
const readBudgetRefusalFmt = "%s refused: this ingest has read %d wiki pages since it last staged a change. Stage the pages for the source now (stage.create_page / stage.patch_page) from what you have read; wiki reads are allowed again after a change is staged."

// budgetedReads are the tools that read a wiki page and so spend the budget,
// by canonical name. wiki.search returns snippets (it is how the model finds
// the related pages), raw.* reads the source being ingested, vault.orient is
// the digest, and the stage tools write: none is a page read.
var budgetedReads = map[string]bool{
	"wiki.get":       true,
	"wiki.neighbors": true,
	"wiki.backlinks": true,
}

// notPageChange are the stage tools that, even when they succeed, put no page
// change in the changeset: ingest_source stages the raw source (the thing
// being read from, not written), open starts or joins a changeset and close
// only summarizes it. Every other stage.* call is a page change, so a stage
// tool added later spends and resets the budget like its siblings unless it is
// listed here.
var notPageChange = map[string]bool{
	"stage.ingest_source": true,
	"stage.open":          true,
	"stage.close":         true,
}

// isPageChange reports whether canonical, when it succeeds, is a change to a
// page — the event that resets the read count.
func isPageChange(canonical string) bool {
	return strings.HasPrefix(canonical, "stage.") && !notPageChange[canonical]
}

// readBudget is one Send's count of wiki page reads — and, since 053, of
// wiki.search calls (searchbudget.go) — since the turn's last page change. It
// lives in the turn, like 041's staged map, and never in the Loop or the
// registry, so a TUI Loop reused across turns starts every turn at zero.
// Dispatch is sequential — dispatchToolCall has one caller, runRound's stream
// loop — so a plain struct passed by pointer needs no mutex.
//
// A nil *readBudget is the budget of every turn that has none (every verb but
// ingest) and every method is a no-op on it, the way a nil trace Recorder is:
// the call sites stay unconditional and those turns run byte for byte as they
// did before 048.
type readBudget struct {
	reads    int
	searches int // 053: wiki.search calls dispatched; reads and searches are separate counts
}

// newReadBudget returns the budget plan calls for: a fresh one for an ingest
// turn, nil for every other.
func newReadBudget(plan turnPlan) *readBudget {
	if !plan.readBudget {
		return nil
	}
	return &readBudget{}
}

// refusal reports whether canonical is a read the budget no longer allows, and
// the text to answer it with. A refused read is not counted: the count stays
// at the budget until a page change resets it.
func (b *readBudget) refusal(canonical string) (string, bool) {
	if b == nil || !budgetedReads[canonical] || b.reads < ingestReadBudget {
		return "", false
	}
	return fmt.Sprintf(readBudgetRefusalFmt, canonical, ingestReadBudget), true
}

// noteRead counts a read that reached the registry. It is the call itself that
// is counted, not what it returned: a guessed page name that came back "not
// found" still cost the model a round, and the crawl is the thing being bound.
func (b *readBudget) noteRead(canonical string) {
	if b != nil && budgetedReads[canonical] {
		b.reads++
	}
}

// notePageChange resets both counts when canonical, a stage.* call that came
// back without IsError, was a page change.
func (b *readBudget) notePageChange(canonical string) {
	if b != nil && isPageChange(canonical) {
		b.reads = 0
		b.searches = 0
	}
}
