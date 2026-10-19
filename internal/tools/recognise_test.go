package tools

// recognise_test.go is 063 S1's pin of the three recognisers (D4, plan R5).
// `lw tool` runs one process per call and rebuilds the 040 read log and the
// 043 shrink arms from the changeset's session.ndjson; agent.Record carries no
// IsError, so what a past tool result MEANT can only be read back from its
// text. The recognisers live in this package, next to the producers, so a
// reworded refusal breaks a test here and not a replay in the field — and each
// test feeds the real handlers' real output back in, never a hand-typed string.
// Permanent regression test (D-10C).

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// rcCase is one real tool result and what each recogniser must say about it.
type rcCase struct {
	name   string
	text   string
	key    string // UnreadRefusalKey's key; "" means it must report ok == false
	shrink bool   // ShrinkRefusal
	served bool   // RawGetServed
}

// rcText runs a tool and returns its Content, failing unless the result's
// IsError is want — a case labelled "refusal" that actually staged would make
// the "false on every other refusal" half of the test vacuous.
func rcText(t *testing.T, reg *Registry, name, args string, wantErr bool) string {
	t.Helper()
	r := callTool(t, reg, name, args)
	if r.IsError != wantErr {
		t.Fatalf("%s %s: IsError = %v, want %v:\n%s", name, args, r.IsError, wantErr, r.Content)
	}
	if r.Content == "" {
		t.Fatalf("%s %s: empty content", name, args)
	}
	return r.Content
}

func TestRecognisersRoundTripRealText(t *testing.T) {
	var cases []rcCase

	// --- 040: an unread refusal over two sources -------------------------
	{
		reg, _ := guardRegistry(t, map[string]*extract.Doc{
			"zeta.md":  guardDoc("Zeta Paper", 20000),
			"alpha.md": guardDoc("Alpha Note", 40000),
		})
		guardOpen(t, reg)
		zeta, _ := guardIngest(t, reg, "zeta.md")
		alpha, _ := guardIngest(t, reg, "alpha.md")
		text := rcText(t, reg, "stage.close", `{}`, true)
		wantKey := alpha + " chunks 1, 2, 3 of 3 unread; " + zeta + " chunks 1, 2 of 2 unread"
		cases = append(cases, rcCase{name: "unread refusal, two sources", text: text, key: wantKey})
		if got, want := text, closeRefusalHead+wantKey+closeRefusalTail; got != want {
			t.Fatalf("fixture drift: the close refusal is\n%q\nwant\n%q", got, want)
		}
		// The close that follows a refusal goes through and says what it let by.
		second := rcText(t, reg, "stage.close", `{}`, false)
		if !strings.Contains(second, "\nunread chunks: ") {
			t.Fatalf("second close carries no unread line:\n%s", second)
		}
		cases = append(cases, rcCase{name: "close that went through over unread chunks", text: second})
	}

	// --- a close that went through clean ---------------------------------
	{
		reg, _ := guardRegistry(t, map[string]*extract.Doc{"short.md": guardDoc("Short Source", 100)})
		guardOpen(t, reg)
		path, _ := guardIngest(t, reg, "short.md")
		guardRead(t, reg, path, 1)
		cases = append(cases, rcCase{name: "clean close summary", text: rcText(t, reg, "stage.close", `{}`, false)})
	}

	// --- 043: the shrink refusal, plain and confirm-line forms, and every
	// other stage.patch_page refusal that must NOT read as one ------------
	{
		reg, _, op3 := tilelangRegistry(t)
		if r := callTool(t, reg, "stage.open", `{"intent":"probe refusals"}`); r.IsError {
			t.Fatal(r.Content)
		}
		const page = "wiki/entities/tilelang.md"

		// A pre-emptive allow_shrink: the refusal's last line is swapped for the
		// confirm line (043 T2). The unflagged call that follows is the plain form.
		confirm := rcText(t, reg, "stage.patch_page", op8Args(t, true), true)
		plain := rcText(t, reg, "stage.patch_page", op8Args(t, false), true)
		if !strings.HasSuffix(confirm, shrinkConfirmLine) || strings.HasSuffix(plain, shrinkConfirmLine) {
			t.Fatalf("fixture drift: confirm form:\n%s\nplain form:\n%s", confirm, plain)
		}
		cases = append(cases,
			rcCase{name: "shrink refusal, replace_section, plain form", text: plain, shrink: true},
			rcCase{name: "shrink refusal, replace_section, confirm-line form", text: confirm, shrink: true},
		)

		// replace_text deleting three of four paragraphs: same refusal, other op.
		parsed, err := vault.ParsePage(page, op3)
		if err != nil {
			t.Fatal(err)
		}
		sec, ok := parsed.Section("## GPU programming model")
		if !ok {
			t.Fatal(`section "## GPU programming model" not found in the ground page`)
		}
		paras := strings.Split(strings.TrimSpace(parsed.Body[sec.Body:sec.End]), "\n\n")
		if len(paras) != 4 {
			t.Fatalf("ground section has %d paragraphs, want 4", len(paras))
		}
		textShrink, err := json.Marshal(map[string]any{
			"path": page, "section": "## GPU programming model", "op": "replace_text",
			"find": paras[0] + "\n\n" + paras[1] + "\n\n" + paras[2], "content": "", "rationale": "drop three paragraphs",
		})
		if err != nil {
			t.Fatal(err)
		}
		rt := rcText(t, reg, "stage.patch_page", string(textShrink), true)
		if !strings.HasPrefix(rt, "replace_text ") {
			t.Fatalf("fixture drift: replace_text shrink refusal is\n%s", rt)
		}
		cases = append(cases, rcCase{name: "shrink refusal, replace_text", text: rt, shrink: true})

		cases = append(cases,
			rcCase{name: "nested-loss refusal", text: rcText(t, reg, "stage.patch_page",
				`{"path":"`+page+`","section":"# TileLang","op":"replace_section","content":"# TileLang","rationale":"tighten"}`, true)},
			rcCase{name: "echo-only refusal", text: rcText(t, reg, "stage.patch_page",
				`{"path":"`+page+`","section":"## Related","op":"append_section","content":"## Related","rationale":"echo"}`, true)},
			rcCase{name: "find not unique", text: rcText(t, reg, "stage.patch_page",
				`{"path":"`+page+`","section":"## GPU programming model","op":"replace_text","find":"^[raw/papers/tilelang-a-composable","content":"x","rationale":"probe"}`, true)},
			rcCase{name: "find not found", text: rcText(t, reg, "stage.patch_page",
				`{"path":"`+page+`","section":"## GPU programming model","op":"replace_text","find":"[[dcgm]]","content":"x","rationale":"probe"}`, true)},
			rcCase{name: "section not found", text: rcText(t, reg, "stage.patch_page",
				`{"path":"`+page+`","section":"## Nope","op":"append_section","content":"x","rationale":"probe"}`, true)},
			rcCase{name: "elision placeholder refusal", text: rcText(t, reg, "stage.patch_page",
				`{"path":"`+page+`","section":"## Related","op":"append_section","content":"[elided: 12 bytes of page text sent in this call]","rationale":"probe"}`, true)},
			rcCase{name: "bad arguments", text: rcText(t, reg, "stage.patch_page", `{"path":1}`, true)},
		)
		// A patch that staged is not a refusal at all.
		cases = append(cases, rcCase{name: "patch that staged", text: rcText(t, reg, "stage.patch_page",
			`{"path":"`+page+`","section":"## GPU programming model","op":"replace_text","find":"recur in [[batch-invariant-deterministic-kernels]].","content":"recur in [[batch-invariant-deterministic-kernels]]. Tools like [[nvitop]] make the resulting utilization observable.","rationale":"link nvitop"}`, false)})
	}

	// --- 040: raw.get — served chunks, with and without page anchors, committed
	// and staged; and every raw.get refusal --------------------------------
	{
		dir := testutil.CopyFixture(t, "minimal")
		writeRawSource(t, dir, "raw/papers/paged.md", pagedBody())
		writeRawSource(t, dir, "raw/papers/plain.md", strings.Repeat("a", 40072))
		reg := NewRegistry(newTestDeps(t, dir))
		get := func(source string, chunk int) string {
			return rcText(t, reg, "raw.get", fmt.Sprintf(`{"source":%q,"chunk":%d}`, source, chunk), false)
		}
		plain, lastPlain := get("raw/papers/plain.md", 1), get("raw/papers/plain.md", 3)
		if !strings.HasPrefix(plain, "chunk 1 of 3\n\n") {
			t.Fatalf("fixture drift: committed plain chunk starts %q", plain[:20])
		}
		anchored := get("raw/papers/paged.md", 2)
		if !strings.HasPrefix(anchored, "chunk 2 of 3 · pages 2-4\n\n") {
			t.Fatalf("fixture drift: committed paged chunk starts %q", anchored[:40])
		}
		onePage := get("raw/papers/paged.md", 3)
		if !strings.HasPrefix(onePage, "chunk 3 of 3 · page 4\n\n") {
			t.Fatalf("fixture drift: single-page chunk starts %q", onePage[:40])
		}
		cases = append(cases,
			rcCase{name: "raw.get chunk, committed, no anchors", text: plain, served: true},
			rcCase{name: "raw.get chunk, committed, last chunk", text: lastPlain, served: true},
			rcCase{name: "raw.get chunk, committed, page range", text: anchored, served: true},
			rcCase{name: "raw.get chunk, committed, one page", text: onePage, served: true},
			rcCase{name: "raw.get missing source", text: rcText(t, reg, "raw.get", `{"source":"raw/papers/nope.md"}`, true)},
			rcCase{name: "raw.get chunk out of range", text: rcText(t, reg, "raw.get", `{"source":"raw/papers/plain.md","chunk":9}`, true)},
			rcCase{name: "raw.get negative chunk", text: rcText(t, reg, "raw.get", `{"source":"raw/papers/plain.md","chunk":-1}`, true)},
			rcCase{name: "raw.get empty source", text: rcText(t, reg, "raw.get", `{}`, true)},
			rcCase{name: "raw.get bad arguments", text: rcText(t, reg, "raw.get", `{"source":7}`, true)},
		)

		// A chunk of a source only staged in the changeset carries the staged
		// marker ahead of the header; the anchored form keeps both.
		sreg, _ := guardRegistry(t, map[string]*extract.Doc{
			"plain.md": guardDoc("Staged Plain", 20000),
			"paged.md": {Title: "Staged Paged", SourceURL: "https://example.test/staged-paged", Markdown: pagedBody(), Kind: "paper", Extractor: "test"},
			"third.md": guardDoc("Third Source", 100),
		})
		guardOpen(t, sreg)
		sp, _ := guardIngest(t, sreg, "plain.md")
		sa, _ := guardIngest(t, sreg, "paged.md")
		stagedPlain := rcText(t, sreg, "raw.get", fmt.Sprintf(`{"source":%q,"chunk":1}`, sp), false)
		stagedPaged := rcText(t, sreg, "raw.get", fmt.Sprintf(`{"source":%q,"chunk":1}`, sa), false)
		if !strings.HasPrefix(stagedPlain, stagedSourceMarker+"chunk 1 of 2\n\n") || !strings.HasPrefix(stagedPaged, stagedSourceMarker+"chunk 1 of 3 · pages 1-2\n\n") {
			t.Fatalf("fixture drift: staged chunks start %q / %q", stagedPlain[:80], stagedPaged[:80])
		}
		cases = append(cases,
			rcCase{name: "raw.get chunk, staged, no anchors", text: stagedPlain, served: true},
			rcCase{name: "raw.get chunk, staged, with anchors", text: stagedPaged, served: true},
			rcCase{name: "stage.ingest_source success", text: rcText(t, sreg, "stage.ingest_source", `{"uri":"third.md"}`, false)},
			rcCase{name: "stage.ingest_source duplicate refusal", text: rcText(t, sreg, "stage.ingest_source", `{"uri":"plain.md"}`, true)},
		)
	}

	// --- reads that are neither raw.get nor a stage.* refusal ------------
	{
		reg := minimalRegistry(t)
		cases = append(cases, rcCase{name: "wiki.get page", text: rcText(t, reg, "wiki.get", `{"page":"kv-cache"}`, false)})
	}

	seen := map[string]bool{}
	for _, c := range cases {
		if seen[c.name] {
			t.Fatalf("duplicate case name %q", c.name)
		}
		seen[c.name] = true
		t.Run(c.name, func(t *testing.T) {
			key, ok := UnreadRefusalKey(c.text)
			if ok != (c.key != "") || key != c.key {
				t.Errorf("UnreadRefusalKey = (%q, %v), want (%q, %v)", key, ok, c.key, c.key != "")
			}
			if got := ShrinkRefusal(c.text); got != c.shrink {
				t.Errorf("ShrinkRefusal = %v, want %v", got, c.shrink)
			}
			if got := RawGetServed(c.text); got != c.served {
				t.Errorf("RawGetServed = %v, want %v", got, c.served)
			}
		})
	}
	if !t.Failed() && len(cases) < 20 {
		t.Errorf("only %d cases; the round trip lost coverage", len(cases))
	}
}

// TestRecognisersKeyIsTheRemembered040Key: the key UnreadRefusalKey returns is
// byte-for-byte what readLog.closeVerdict remembers as refused — the string
// Replay hands back to the log, so the second close over the same set matches
// it. Compared against the log itself, not against a second copy of the format.
func TestRecognisersKeyIsTheRemembered040Key(t *testing.T) {
	reg, _ := guardRegistry(t, map[string]*extract.Doc{"long.md": guardDoc("Long Source", 40000)})
	guardOpen(t, reg)
	path, _ := guardIngest(t, reg, "long.md")
	guardRead(t, reg, path, 2)
	refusal := guardCall(t, reg, "stage.close", `{}`)
	if !refusal.IsError {
		t.Fatalf("close not refused: %s", refusal.Content)
	}
	key, ok := UnreadRefusalKey(refusal.Content)
	if !ok {
		t.Fatalf("UnreadRefusalKey did not recognise the real refusal:\n%s", refusal.Content)
	}
	if remembered := reg.deps.reads.refused; key != remembered {
		t.Errorf("UnreadRefusalKey = %q, but the log remembers %q", key, remembered)
	}
	want := unreadClauses([]unreadSource{{Path: path, Chunks: []int{1, 3}, N: 3}})
	if key != want {
		t.Errorf("key = %q, want unreadClauses' %q", key, want)
	}
}

// TestRecognisersRejectLookalikes: text that merely resembles a producer's
// output is not it. The refusals open with fixed lead-ins, but a model's reply
// or a page quote could repeat one; each recogniser also checks the shape that
// closes the text, and an empty string is nothing.
func TestRecognisersRejectLookalikes(t *testing.T) {
	for _, s := range []string{
		"",
		"stage.close: not every chunk of the sources ingested in this changeset was read — ",                     // head only
		closeRefusalHead + closeRefusalTail,                                                                      // no clauses
		closeRefusalHead + "raw/a.md chunks 1 of 1 unread",                                                       // no tail
		"note: " + closeRefusalHead + "raw/a.md chunks 1 of 1 unread" + closeRefusalTail,                         // not at the start
		`replace_section "## S" keeps 1 of 400 bytes (0%) of the section; it would delete:`,                      // one line, no closing advice
		`replace_section "## S" keeps 1 of 400 bytes (0%) of the section; it would delete:` + "\nsomething else", // wrong last line
		"it would delete:\n" + shrinkConfirmLine,                                                                 // wrong first line
		"chunk 1 of 2",                                                                                           // header without the blank line and chunk
		"chunk one of 2\n\nbody",                                                                                 // not a number
		"chunk 1 of 2 · pages\n\nbody",                                                                           // malformed anchor suffix
		"(staged in the open changeset, not yet committed)\n\nnot a header\n\nbody",                              // marker, no header
		"chunk 9 is out of range for raw/a.md: it has 2 chunk(s), so chunk must be between 1 and 2",              // the refusal text
		"prefix chunk 1 of 2\n\nbody",                                                                            // header not at the start
	} {
		if k, ok := UnreadRefusalKey(s); ok {
			t.Errorf("UnreadRefusalKey(%q) = (%q, true)", s, k)
		}
		if ShrinkRefusal(s) {
			t.Errorf("ShrinkRefusal(%q) = true", s)
		}
		if RawGetServed(s) {
			t.Errorf("RawGetServed(%q) = true", s)
		}
	}

	// A chunk whose BODY quotes a refusal is still a served chunk, and only that.
	body := "chunk 1 of 1\n\n" + closeRefusalHead + "raw/a.md chunks 1 of 1 unread" + closeRefusalTail
	if !RawGetServed(body) {
		t.Errorf("RawGetServed rejected a chunk whose text quotes a refusal")
	}
	if _, ok := UnreadRefusalKey(body); ok {
		t.Errorf("UnreadRefusalKey recognised a refusal quoted inside a chunk")
	}
	// An empty chunk body is still a served chunk: the header, the blank line, nothing.
	if !RawGetServed("chunk 1 of 1\n\n") {
		t.Errorf("RawGetServed rejected an empty chunk")
	}
}
