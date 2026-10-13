package score

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/awepo-pro/lw/internal/trace"
)

// pageNotFoundFmt is wiki.get's answer for a page it cannot resolve, with the
// page still a verb. It is a COPY of the message internal/tools builds in
// resolvePagePath — "%q" of the trimmed page argument, then the hint — and
// TestPageNotFoundTracksTool runs the real tool and fails when the wording or the
// quoting moves (060). Only its LENGTH is used, and only for a call whose text was
// never recovered (GetNotFound).
const pageNotFoundFmt = "page %q was not found; pass the vault-relative path or the filename basename " +
	"(e.g. \"kv-cache\"), not the page title — wiki.search lists each result's " +
	"path before the em dash"

// pageNotFoundText is the not-found answer for page, which the tool has already
// trimmed.
func pageNotFoundText(page string) string { return fmt.Sprintf(pageNotFoundFmt, page) }

// pageNotFoundSize is the byte length of the not-found answer the tool would
// give for a wiki.get with these JSON arguments, or -1 when it would give
// another answer: arguments that do not parse (the tool answers "arguments are
// not valid JSON") and a blank page ("page is required"), neither of which is a
// not-found.
func pageNotFoundSize(args string) int {
	var a struct {
		Page string `json:"page"`
	}
	if json.Unmarshal([]byte(args), &a) != nil {
		return -1
	}
	page := strings.TrimSpace(a.Page)
	if page == "" {
		return -1
	}
	return len(pageNotFoundText(page))
}

// IsPageNotFound reports whether a tool result's text is wiki.get's refusal of
// a page that does not resolve: it starts `page "` and says `" was not found`.
// The other texts a page read answers with — a section that is missing
// ("section … was not found on …"), a blank page, bad JSON, 048's refusal — do not
// start so. The same text is the answer of wiki.neighbors and wiki.backlinks,
// and a stage tool has a page-not-found of its own with the same head; the tool
// name decides which of those counts (GetNotFound). ToolErrors keeps only the
// first 200 runes of a result, and the phrase sits right after the page argument,
// so a cut text matches unless the model asked for a page name over some 150
// runes long.
func IsPageNotFound(text string) bool {
	return strings.HasPrefix(text, `page "`) && strings.Contains(text, `" was not found`)
}

// GetNotFound counts the wiki.get calls of turn t whose result was the not-found
// refusal: the failed calls to wiki.get whose result IsPageNotFound. The result
// text is recovered by ToolErrors from the next round's request, and a call that
// ended its turn — max_rounds hit on the very round it was made, how the 10-08
// failure ended, a joined turn that asked for its own staged pages six times —
// has no next request, so its Text is "". For that case, and only that case, the
// tool event's result_bytes decides, as it does for ReadRefusals (A-049-5), but
// the refusal's length depends on the page that was asked for: the page is read
// from the same call's arguments in the response and the length of the not-found
// text for it is compared with result_bytes. A call whose text WAS recovered is
// judged by the text alone, however many bytes it returned.
//
// It counts calls, not distinct pages: asking again for a page that was not
// found is the loop the metric exists to show. wiki.neighbors and wiki.backlinks
// answer a missing page with the same words and are not counted — they are not
// the tool a model uses to read a page back. dir and id name the turn on disk, as
// for ToolErrors. (060.)
func GetNotFound(dir, id string, t *trace.Turn) (int, error) {
	failed, err := failedCalls(dir, id, t)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, f := range failed {
		if canonicalTool(f.Name) != "wiki.get" {
			continue
		}
		switch {
		case f.Text != "":
			if IsPageNotFound(f.Text) {
				n++
			}
		case f.ResultBytes == pageNotFoundSize(f.Args):
			n++
		}
	}
	return n, nil
}
