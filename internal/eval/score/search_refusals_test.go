package score

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// srOK is what a wiki.search that succeeded returns, as far as these fixtures
// care: some text that is not an error.
const srOK = "3 results for \"agent memory\": wiki/concepts/kv-cache.md, wiki/entities/tilelang.md, wiki/concepts/tls.md"

// srRepeat is 051's refusal of an identical wiki.search, the other refusal
// that begins "wiki.search refused: " and must not be mistaken for 053's.
const srRepeat = "wiki.search refused: this exact call already ran in round 3 of this turn and its result is still above, unchanged. Use that result, or call with different arguments."

// TestIsSearchRefusal pins the needle: 053's own text is a refusal — whole, and
// as cut to the 200 runes ToolErrors keeps — and the other texts a wiki.search
// or a wiki read can answer with are not (053).
func TestIsSearchRefusal(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"the exact 053 text", searchRefusalText, true},
		{"the text a 200-rune ToolErrors cut leaves", string([]rune(searchRefusalText)[:200]), true},
		{"051's refusal of an identical repeat", srRepeat, false},
		{"048's refusal of a read", refusalText, false},
		{"a search that found nothing", "no results for \"obsidian\"", false},
		{"an ordinary search error", "wiki.search: q is required", false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSearchRefusal(tc.text); got != tc.want {
				t.Errorf("IsSearchRefusal(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// TestSearchRefusals pins how a refused search is recognised, the way
// TestReadRefusals pins 048's. The text is the proof when ToolErrors can
// recover it; when the refused call was its turn's last round there is no next
// request, the text is "", and the only trace of it is the tool event's
// result_bytes — the length of 053's refusal. The first row is the case the
// 053 acceptance bar is read from: one refused search and one that was served
// make search_refusals 1 (and search_calls 2).
func TestSearchRefusals(t *testing.T) {
	size := len(searchRefusalText)
	tests := []struct {
		name     string
		withNext bool
		calls    []refCall
		want     int
	}{
		{"one refused and one ok", true, []refCall{
			{"wiki.search", false, len(srOK), srOK},
			{"wiki.search", true, size, searchRefusalText},
		}, 1},
		{"two refused, one ok between", true, []refCall{
			{"wiki.search", true, size, searchRefusalText},
			{"wiki.search", false, len(srOK), srOK},
			{"wiki.search", true, size, searchRefusalText},
		}, 2},
		{"no refusal at all", true, []refCall{
			{"wiki.search", false, len(srOK), srOK},
			{"wiki.search", true, 28, "wiki.search: q is required"},
		}, 0},
		{"051's repeat refusal is not 053's", true, []refCall{
			{"wiki.search", true, len(srRepeat), srRepeat},
		}, 0},
		{"a successful search quoting the refusal", true, []refCall{
			{"wiki.search", false, size, searchRefusalText},
		}, 0},
		{"the refusal text on another tool", true, []refCall{
			{"wiki.get", true, size, searchRefusalText},
		}, 0},
		{"048's read refusal on wiki.get", true, []refCall{
			{"wiki.get", true, len(refusalText), refusalText},
		}, 0},
		{"text known and not a refusal outranks the bytes", true, []refCall{
			{"wiki.search", true, size, "wiki.search: q is required"},
		}, 0},
		{"no next request, bytes are the refusal's", false, []refCall{{"wiki.search", true, size, ""}}, 1},
		{"no next request, bytes are something else", false, []refCall{{"wiki.search", true, 17, ""}}, 0},
		{"no next request, a wire-spelled name", false, []refCall{{"wiki_search", true, size, ""}}, 1},
		{"no next request, the call did not fail", false, []refCall{{"wiki.search", false, size, ""}}, 0},
		{"no next request, the refusal's length on another tool", false, []refCall{
			{"wiki.get", true, size, ""},
			{"web.search", true, size, ""},
		}, 0},
		{"no calls", false, nil, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			turn := writeRefusalTurn(t, dir, tc.withNext, tc.calls...)
			got, err := SearchRefusals(dir, turnID(1), turn)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("SearchRefusals = %d, want %d", got, tc.want)
			}
		})
	}

	t.Run("a nil turn", func(t *testing.T) {
		if got, err := SearchRefusals(t.TempDir(), turnID(1), nil); err != nil || got != 0 {
			t.Errorf("SearchRefusals(nil) = %d, %v, want 0, nil", got, err)
		}
	})
}

// TestSearchRefusalTracksAgentSource is the drift guard for the two copies in
// ingest.go: it reads internal/agent/searchbudget.go as TEXT (no import),
// formats the real refusal with the real budget and requires the needle and
// the whole-text copy to match it. If 053's wording or its budget is ever
// changed, search_refusals would silently count zero; this fails instead.
func TestSearchRefusalTracksAgentSource(t *testing.T) {
	src, err := os.ReadFile("../../agent/searchbudget.go")
	if err != nil {
		t.Fatal(err)
	}
	fm := regexp.MustCompile(`const searchBudgetRefusalFmt = (".*")\n`).FindSubmatch(src)
	bm := regexp.MustCompile(`const ingestSearchBudget = (\d+)\n`).FindSubmatch(src)
	if fm == nil || bm == nil {
		t.Fatal("searchBudgetRefusalFmt or ingestSearchBudget not found in internal/agent/searchbudget.go; update this guard with the refactor")
	}
	format, err := strconv.Unquote(string(fm[1]))
	if err != nil {
		t.Fatal(err)
	}
	budget, err := strconv.Atoi(string(bm[1]))
	if err != nil {
		t.Fatal(err)
	}
	real := fmt.Sprintf(format, budget)
	if !IsSearchRefusal(real) {
		t.Errorf("IsSearchRefusal does not recognise the agent's refusal %q", real)
	}
	if real != searchRefusalText {
		t.Errorf("searchRefusalText drifted from internal/agent/searchbudget.go\n got  %q\n want %q", searchRefusalText, real)
	}
	if !strings.HasPrefix(real, searchRefusalNeedle) {
		t.Errorf("searchRefusalNeedle %q is not the head of the agent's refusal %q", searchRefusalNeedle, real)
	}
}
