package score

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/trace"
	"github.com/awepo-pro/lw/internal/vault"
)

// gnfOK is what a wiki.get that succeeded returns, as far as these fixtures
// care: some page text that is not an error.
const gnfOK = "---\ntitle: KV Cache\n---\n\n# KV Cache\n\nKeys and values."

// gnfCall is one tool call of a fixture turn: the tool the loop recorded, the
// JSON arguments the model sent (they travel in the response's tool_calls, where
// the scorer finds them for a call whose text was never recovered), whether it
// failed, the result_bytes of its tool event (len(result) when 0) and the
// result itself (round 2's request holds it when the turn has one).
type gnfCall struct {
	name   string
	args   string
	fail   bool
	bytes  int
	result string
}

// gnfPage is the arguments of a wiki.get of page.
func gnfPage(page string) string {
	b, err := json.Marshal(map[string]string{"page": page})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// writeGetTurn records turn 1 under dir: round 1 makes calls, then the turn
// ends at max_rounds — or, with withNext, a second round answers and the
// results ride in its request. It is writeRefusalTurn with the call arguments
// kept, because the bytes fallback of GetNotFound reads them.
func writeGetTurn(t *testing.T, dir string, withNext bool, calls ...gnfCall) *trace.Turn {
	t.Helper()
	rec := startTurn(t, dir, 1)
	user := llm.Message{Role: "user", Content: "ingest"}
	send(rec, 1, 1, request(t, user))
	var asked []llm.ToolCall
	var results []llm.Message
	for i, c := range calls {
		id := fmt.Sprintf("c%d", i+1)
		n := c.bytes
		if n == 0 {
			n = len(c.result)
		}
		rec.Tool(trace.Tool{Round: 1, ID: id, Name: c.name, IsError: c.fail, ResultBytes: n})
		args := c.args
		if args == "" {
			args = `{}`
		}
		asked = append(asked, call(id, tools.WireName(c.name), args))
		results = append(results, llm.Message{Role: "tool", ToolCallID: id, Content: c.result})
	}
	rec.Response(trace.Response{Round: 1, Attempt: 1, Finish: "tool_calls", ToolCalls: toolCalls(asked...)})
	if withNext {
		send(rec, 2, 1, request(t, append([]llm.Message{user, {Role: "assistant", ToolCalls: asked}}, results...)...))
		rec.Response(trace.Response{Round: 2, Attempt: 1, Finish: "stop", Text: "done"})
		rec.Done(trace.Done{Reason: "stop", Rounds: 2})
	} else {
		rec.Done(trace.Done{Reason: "max_rounds", Rounds: 1})
	}
	return loadTurn(t, dir, 1)
}

// TestIsPageNotFound pins the recogniser of wiki.get's not-found text (060): it
// starts `page "` and says `" was not found`. The other texts a wiki read can
// answer with — a section that is missing, an empty page argument, bad JSON, 048's
// refusal — are not it, and neither is a cut to the 200 runes ToolErrors keeps
// once the phrase has been pushed past the cut.
func TestIsPageNotFound(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"the exact tool text", pageNotFoundText("paged-attention"), true},
		{"the text a 200-rune ToolErrors cut leaves", firstRunes(pageNotFoundText("wiki/concepts/"+strings.Repeat("long-name-", 8)+".md"), toolErrorRunes), true},
		{"a path with quotes in it", pageNotFoundText(`wiki/"odd".md`), true},
		{"a staged tool's own page-not-found", `page "wiki/concepts/x.md" was not found; closest: wiki/concepts/y.md`, true},
		{"a section that is missing", `section "## Nope" was not found on wiki/concepts/kv-cache.md; valid headings are: ## Abstract`, false},
		{"a page that resolved but could not be loaded", `page "x" resolved to "wiki/x.md", which could not be loaded`, false},
		{"an empty page argument", `page is required for wiki.get: provide a page path or filename basename, e.g. {"page": "kv-cache"}`, false},
		{"bad arguments", `arguments are not valid JSON for wiki.get: unexpected end of JSON input; expected an object like {"page": "kv-cache"}`, false},
		{"048's refusal of a read", "wiki.get refused: this ingest has read 6 wiki pages since it last staged a change.", false},
		{"the phrase without the page prefix", `the page "x" was not found`, false},
		{"a page text that mentions it later", "# Notes\n\npage \"x\" was not found in the old wiki", false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsPageNotFound(tc.text); got != tc.want {
				t.Errorf("IsPageNotFound(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// TestGetNotFound pins how a not-found wiki.get is counted (060): the FAILED
// calls to wiki.get whose result is the not-found text — nothing else. The text
// is the proof when ToolErrors can recover it from the next round's request;
// when the call was its turn's last round there is no next request, the text is
// "", and the only trace of it is the tool event's result_bytes, which for this
// text depends on the page the model asked for — so the scorer takes the page
// from the same call's arguments in the response and compares the length with the
// text the tool would have returned for it. That last-round case is the one the
// 10-08 failure ended on (a joined recompile turn at max_rounds after six
// not-found reads of its own staged pages), so a scorer blind to it would report
// the bug at its smallest exactly where it did its damage.
func TestGetNotFound(t *testing.T) {
	nf := func(page string) string { return pageNotFoundText(page) }
	tests := []struct {
		name     string
		withNext bool
		calls    []gnfCall
		want     int
	}{
		{"two not found and one served", true, []gnfCall{
			{name: "wiki.get", args: gnfPage("paged-attention"), fail: true, result: nf("paged-attention")},
			{name: "wiki.get", args: gnfPage("kv-cache"), result: gnfOK},
			{name: "wiki.get", args: gnfPage("wiki/comparisons/http-versions-comparison.md"), fail: true, result: nf("wiki/comparisons/http-versions-comparison.md")},
		}, 2},
		{"a wire-spelled name", true, []gnfCall{
			{name: "wiki_get", args: gnfPage("x"), fail: true, result: nf("x")},
		}, 1},
		{"the same text on the graph tools is not a wiki.get", true, []gnfCall{
			{name: "wiki.neighbors", args: gnfPage("x"), fail: true, result: nf("x")},
			{name: "wiki.backlinks", args: gnfPage("x"), fail: true, result: nf("x")},
		}, 0},
		{"a staged tool's page-not-found is not a wiki.get", true, []gnfCall{
			{name: "stage.patch_page", args: `{"path":"x"}`, fail: true, result: `page "x" was not found; closest: wiki/concepts/y.md`},
		}, 0},
		{"other wiki.get failures are not it", true, []gnfCall{
			{name: "wiki.get", args: gnfPage("kv-cache"), fail: true, result: `section "## Nope" was not found on wiki/concepts/kv-cache.md; valid headings are: ## Abstract`},
			{name: "wiki.get", args: `{`, fail: true, result: `arguments are not valid JSON for wiki.get: unexpected end of JSON input`},
			{name: "wiki.get", args: gnfPage(" "), fail: true, result: `page is required for wiki.get: provide a page path`},
			{name: "wiki.get", args: gnfPage("x"), fail: true, result: "wiki.get refused: this ingest has read 6 wiki pages since it last staged a change."},
		}, 0},
		{"a served read quoting the text", true, []gnfCall{
			{name: "wiki.get", args: gnfPage("x"), result: nf("x")},
		}, 0},
		{"text known and not it outranks the bytes", true, []gnfCall{
			{name: "wiki.get", args: gnfPage("x"), fail: true, bytes: len(nf("x")), result: "wiki.get: something else"},
		}, 0},
		{"no next request, bytes are the not-found text for the page asked", false, []gnfCall{
			{name: "wiki.get", args: gnfPage("paged-attention"), fail: true, bytes: len(nf("paged-attention"))},
		}, 1},
		{"no next request, the page argument is trimmed as the tool trims it", false, []gnfCall{
			{name: "wiki.get", args: gnfPage("  paged-attention \n"), fail: true, bytes: len(nf("paged-attention"))},
		}, 1},
		{"no next request, a page that costs more bytes than runes", false, []gnfCall{
			{name: "wiki.get", args: gnfPage("wiki/concepts/注意力—缓存.md"), fail: true, bytes: len(nf("wiki/concepts/注意力—缓存.md"))},
		}, 1},
		{"no next request, bytes of another page's text", false, []gnfCall{
			{name: "wiki.get", args: gnfPage("paged-attention"), fail: true, bytes: len(nf("a-much-longer-page-name-than-asked"))},
		}, 0},
		{"no next request, bytes are something else", false, []gnfCall{
			{name: "wiki.get", args: gnfPage("paged-attention"), fail: true, bytes: 17},
		}, 0},
		{"no next request, arguments that do not parse", false, []gnfCall{
			{name: "wiki.get", args: `{"page":`, fail: true, bytes: len(nf(""))},
			{name: "wiki.get", args: `{}`, fail: true, bytes: len(nf(""))},
		}, 0},
		{"no next request, the call did not fail", false, []gnfCall{
			{name: "wiki.get", args: gnfPage("x"), bytes: len(nf("x"))},
		}, 0},
		{"no next request, the length on another tool", false, []gnfCall{
			{name: "wiki.search", args: gnfPage("x"), fail: true, bytes: len(nf("x"))},
			{name: "wiki.neighbors", args: gnfPage("x"), fail: true, bytes: len(nf("x"))},
		}, 0},
		{"no next request, a wire-spelled name", false, []gnfCall{
			{name: "wiki_get", args: gnfPage("x"), fail: true, bytes: len(nf("x"))},
		}, 1},
		{"no calls", false, nil, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			turn := writeGetTurn(t, dir, tc.withNext, tc.calls...)
			got, err := GetNotFound(dir, turnID(1), turn)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("GetNotFound = %d, want %d", got, tc.want)
			}
		})
	}

	t.Run("a nil turn", func(t *testing.T) {
		if got, err := GetNotFound(t.TempDir(), turnID(1), nil); err != nil || got != 0 {
			t.Errorf("GetNotFound(nil) = %d, %v, want 0, nil", got, err)
		}
	})
}

// TestPageNotFoundTracksTool is the drift guard for the copy in
// get_not_found.go: it asks the REAL wiki.get, through a real registry over the
// minimal fixture vault, for pages that do not exist, and requires the scorer to
// recognise what comes back and to predict its size byte for byte. The text is
// built in internal/tools; if its wording is ever changed, get_not_found would
// silently count zero (or, worse, only the calls whose text was recovered); this
// fails instead. It runs the tool rather than reading its source so a refactor of
// where the message is built cannot hide a change of what it says.
func TestPageNotFoundTracksTool(t *testing.T) {
	v, err := vault.Open(testutil.CopyFixture(t, "minimal"))
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(tools.Deps{Vault: v, Index: index.Build(v)})

	for _, page := range []string{"no-such-page", "wiki/concepts/not-there.md", "Paged Attention", `has "quotes"`, "注意力—缓存", "  padded  "} {
		args, err := json.Marshal(map[string]string{"page": page})
		if err != nil {
			t.Fatal(err)
		}
		res, err := reg.Call(context.Background(), "wiki.get", args)
		if err != nil {
			t.Fatalf("wiki.get %q: %v", page, err)
		}
		if !res.IsError {
			t.Fatalf("wiki.get %q returned a page; the fixture was meant not to hold it:\n%s", page, res.Content)
		}
		if !IsPageNotFound(res.Content) {
			t.Errorf("IsPageNotFound does not recognise the tool's text for %q: %q", page, res.Content)
		}
		if want := pageNotFoundText(strings.TrimSpace(page)); res.Content != want {
			t.Errorf("pageNotFoundText(%q) drifted from internal/tools\n got  %q\n want %q", page, want, res.Content)
		}
		if got := pageNotFoundSize(string(args)); got != len(res.Content) {
			t.Errorf("pageNotFoundSize(%s) = %d, want the tool's %d bytes", args, got, len(res.Content))
		}
	}

	// A page that exists is not an error and is not recognised.
	res, err := reg.Call(context.Background(), "wiki.get", json.RawMessage(`{"page":"kv-cache"}`))
	if err != nil || res.IsError || IsPageNotFound(res.Content) {
		t.Errorf("wiki.get kv-cache = %+v, %v; want a page that is not a not-found", res, err)
	}
}
