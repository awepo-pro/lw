package agent

// searchbudget.go is 053's engine-enforced search budget for an ingest turn,
// 048's sibling.
//
// The measured failure (eval B49 and the 050+051 candidate): agent-memory-pair,
// a vault with no page about agent memory, made 38 and 54 wiki.search calls in
// two of three runs and used 24 rounds, while every other ingest run made 2-10.
// 051 removed the literal repeats and the model dodged it — "agent memory" x 4
// type filters x limit 1..12, about 48 searches over rounds 8-20 — because every
// result was unrelated or empty and it kept hunting for a page that does not
// exist instead of creating one. That is 048's crawl moved into the one wiki
// tool 048 leaves unbudgeted, and the s39 lesson holds: a refusal bounds this
// model and prompt text does not. An ingest turn that has dispatched
// ingestSearchBudget searches since it last staged a page change has its next
// search refused until it stages one.
//
// The count lives on readBudget (readbudget.go) beside the read count, so it is
// per Send, nil for every verb but ingest, and reset by the same page change.

import "fmt"

// ingestSearchBudget is how many wiki.search calls an ingest turn may make
// between page changes (053). It sits above every non-loop run measured — the
// most was 10, in notion-vs-obsidian — and far below the loops, 38-70. A run
// that stages after each few searches is never refused, because each page
// change resets the count.
const ingestSearchBudget = 10

// searchBudgetRefusalFmt is the refusal a search over the budget gets, with the
// budget as its one verb. It tells the model that more searching will not find
// anything closer, that a new page is the right answer when nothing related
// exists — the thing the loop was avoiding — and that the refusal is not
// permanent, because a model told only "no" kept calling the same tool. The
// wording is a frozen contract: the bytes ride the wire and are pinned by
// searchbudget_test.go.
const searchBudgetRefusalFmt = "wiki.search refused: this ingest has searched the wiki %d times since it last staged a change. The wiki has nothing closer than what you have found; stage the pages for the source now (stage.create_page / stage.patch_page) — a new page is right when nothing related exists. Searches are allowed again after a change is staged."

// budgetedSearch is the one tool that spends the search budget, by canonical
// name. web.search is another tool: it reaches the outside web, not this vault,
// and is not the hunt that was measured, so it stays unbudgeted.
const budgetedSearch = "wiki.search"

// searchRefusal reports whether canonical is a search the budget no longer
// allows, and the text to answer it with. A refused search is not counted: the
// count stays at the budget until a page change resets it.
func (b *readBudget) searchRefusal(canonical string) (string, bool) {
	if b == nil || canonical != budgetedSearch || b.searches < ingestSearchBudget {
		return "", false
	}
	return fmt.Sprintf(searchBudgetRefusalFmt, ingestSearchBudget), true
}

// noteSearch counts a search that reached the registry. As with 048's reads it
// is the call itself that is counted, not what it returned: a query that came
// back "no results" or IsError still cost the model a round, and the hunt is the
// thing being bound. A search 051 refused as a repeat never gets here.
func (b *readBudget) noteSearch(canonical string) {
	if b != nil && canonical == budgetedSearch {
		b.searches++
	}
}
