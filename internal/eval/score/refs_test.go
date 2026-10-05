package score

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/awepo-pro/lw/internal/cite"
)

// refsText is the frozen TestRefs input: a paged marker, a prose path whose
// sentence-final dot is not part of it, a range, a malformed page, and a
// marker inside a code span.
const refsText = "KV cache grows ^[raw/papers/a.md p.3] and see wiki/concepts/kv-cache.md.\n" +
	"Range ^[raw/papers/a.md p.2-4]. Bad ^[raw/papers/a.md p.0]. `^[raw/x.md]` in code.\n"

// TestRefs pins the exact reference list for refsText, in order of
// appearance: markers come from cite.Scan, prose paths from the frozen
// regexp, and neither a code-span marker nor the path text inside a marker
// is a ref of its own. Valid stays unset — only CheckRefs decides it — but a
// marker cite.Parse already rejected carries that Err as its Reason (037 T2).
func TestRefs(t *testing.T) {
	badErr := cite.Parse("raw/papers/a.md p.0").Err
	if badErr == "" {
		t.Fatal("precondition: cite.Parse must reject p.0")
	}
	want := []Ref{
		{Kind: "marker", Target: "raw/papers/a.md", From: 3, To: 3, Raw: "^[raw/papers/a.md p.3]"},
		{Kind: "path", Target: "wiki/concepts/kv-cache.md", Raw: "wiki/concepts/kv-cache.md"},
		{Kind: "marker", Target: "raw/papers/a.md", From: 2, To: 4, Raw: "^[raw/papers/a.md p.2-4]"},
		{Kind: "marker", Target: "raw/papers/a.md", Raw: "^[raw/papers/a.md p.0]", Valid: false, Reason: badErr},
	}
	got := Refs(refsText)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Refs =\n%#v\nwant\n%#v", got, want)
	}
}

// TestRefsProse covers the path grammar's edges: where a path ends, where it
// may sit, and the places that are not prose at all (037 T2).
func TestRefsProse(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string // the path refs' Targets, in order
	}{
		{"none", "no citations here", nil},
		{"sentence-final dot", "see wiki/a.md.", []string{"wiki/a.md"}},
		{"in parentheses", "(see raw/papers/x.md)", []string{"raw/papers/x.md"}},
		{"inside a wikilink", "see [[wiki/concepts/kv-cache.md]]", []string{"wiki/concepts/kv-cache.md"}},
		{"two on one line, in order", "wiki/b.md and raw/a.md", []string{"wiki/b.md", "raw/a.md"}},
		{"only md paths", "raw/papers/x.pdf and wiki/a.txt", nil},
		{"a bare directory is not a path", "see wiki/concepts/ and raw/", nil},
		{"inline code span", "use `wiki/a.md` here", nil},
		{"double-backtick span", "use ``wiki/a.md`` here", nil},
		{"fenced block", "before\n```\nwiki/a.md\n```\nafter wiki/b.md", []string{"wiki/b.md"}},
		{"tilde fence", "~~~go\nraw/a.md\n~~~\n", nil},
		{"path inside a marker is the marker's", "^[raw/papers/x.md] and ^[wiki/a.md]", nil},
		{"path after a closed marker on the same line", "^[raw/papers/x.md] wiki/a.md", []string{"wiki/a.md"}},
		{"unclosed marker leaves the path as prose", "^[raw/x.md and nothing", []string{"raw/x.md"}},
		{"marker split over two lines", "^[raw/x.md\n p.3] then wiki/a.md", []string{"raw/x.md", "wiki/a.md"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, r := range Refs(tc.text) {
				if r.Kind == "path" {
					got = append(got, r.Target)
					if r.Raw != r.Target || r.From != 0 || r.To != 0 || r.Valid || r.Reason != "" {
						t.Errorf("path ref %+v: want Raw == Target, no pages, Valid unset", r)
					}
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("path refs = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRefsPathLeftBoundary pins A-037-1: a prose path counts only when it
// starts a path of its own. The byte before the match must be start-of-text
// or not one of [A-Za-z0-9./_:-], so the tail of a URL or of a longer
// relative path is not a vault reference (and, scored, would be a bogus
// "no such source"), while a path after opening punctuation, a quote,
// whitespace or a line break is (037 T2).
func TestRefsPathLeftBoundary(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string // the path refs' Targets
	}{
		// the five frozen cases
		{"the tail of a URL", "https://h/x/raw/main/README.md", nil},
		{"a subpath of a longer relative path", "foo/wiki/x.md", nil},
		{"in parentheses", "(raw/a.md)", []string{"raw/a.md"}},
		{"in double quotes", `"wiki/x.md"`, []string{"wiki/x.md"}},
		{"at the start of a line", "intro\nwiki/x.md", []string{"wiki/x.md"}},
		// every byte class the boundary rejects, and the start of the text
		{"at the very start of the text", "wiki/x.md is here", []string{"wiki/x.md"}},
		{"after a letter", "xwiki/a.md", nil},
		{"after a digit", "9raw/a.md", nil},
		{"after a dot", ".wiki/a.md", nil},
		{"after a dot and a slash", "../wiki/a.md", nil},
		{"after a slash", "/raw/a.md", nil},
		{"after an underscore", "_wiki/a.md", nil},
		{"after a colon", "see:wiki/a.md", nil},
		{"after a dash", "x-wiki/a.md", nil},
		// bytes the boundary lets through
		{"after a comma", "a,wiki/a.md", []string{"wiki/a.md"}},
		{"after a bracket", "[[wiki/a.md]]", []string{"wiki/a.md"}},
		{"after a tab", "\twiki/a.md", []string{"wiki/a.md"}},
		{"after a multibyte character", "见wiki/a.md", []string{"wiki/a.md"}},
		// a rejected match must not hide the accepted one that follows it
		{"a rejected path then an accepted one", "https://h/raw/a.md and raw/b.md", []string{"raw/b.md"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, r := range Refs(tc.text) {
				if r.Kind == "path" {
					got = append(got, r.Target)
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Refs(%q) path refs = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

// TestRefsMultibyte checks that byte offsets survive non-ASCII prose: the
// order of appearance and the code exclusion both ride on byte offsets, and
// a rune-vs-byte slip shows up only once a CJK character precedes a ref
// (037 T2).
func TestRefsMultibyte(t *testing.T) {
	text := "语言模型 wiki/a.md 和 `wiki/b.md` 以及 ^[raw/c.md] 再 raw/d.md"
	var got []string
	for _, r := range Refs(text) {
		got = append(got, r.Kind+":"+r.Target+"|"+r.Raw)
	}
	want := []string{
		"path:wiki/a.md|wiki/a.md",
		"marker:raw/c.md|^[raw/c.md]",
		"path:raw/d.md|raw/d.md",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Refs = %q, want %q", got, want)
	}
}

// TestRefsCodeAgreesWithCite is the drift guard for "the same code cite.Scan
// skips": for each context, a marker placed there is found by cite.Scan
// exactly when the same text as a bare path is found by Refs. If cite's idea
// of code ever changes, this fails instead of the two quietly disagreeing
// about which citations exist (037 T2).
func TestRefsCodeAgreesWithCite(t *testing.T) {
	contexts := []string{
		"plain %s prose",
		"`%s`",
		"before `%s` after",
		"``a ` %s`` tail",
		"```\n%s\n```",
		"```go\nx\n%s\n```\nafter",
		"~~~\n%s\n~~~\nafter",
		"  ```\n%s\n  ```",
		"````\n```\n%s\n````",
		"```\nunterminated fence\n%s",
		"`unterminated span %s",
		"line one\n`a`\n%s",
		"`a` %s `b`",
		"> quote %s",
		"- bullet %s\n- other",
		"^[ unclosed %s",
		"^[ closed ^[x] then %s",
		"`^[a` %s ]",
	}
	for _, tpl := range contexts {
		marker := fmt.Sprintf(tpl, "^[raw/x.md]")
		path := fmt.Sprintf(tpl, "raw/x.md")
		viaCite := len(cite.Scan(marker)) > 0
		viaRefs := len(Refs(path)) > 0
		if viaCite != viaRefs {
			t.Errorf("context %q: cite.Scan finds a marker = %v, Refs finds a path = %v", tpl, viaCite, viaRefs)
		}
	}
}

// fakeVault is the resolver the CheckRefs tests run against: two raw
// sources — one with page anchors 1-3, one with none — and one wiki page.
func fakeVault(path string) (string, bool) {
	switch path {
	case "raw/papers/a.md":
		return "<!-- page 1 -->\n\nintro\n\n<!-- page 2 -->\n\nbody\n\n<!-- page 3 -->\n\nend\n", true
	case "raw/papers/c.md":
		return "a source with no anchors\n", true
	case "wiki/concepts/kv-cache.md":
		return "# KV cache\n", true
	}
	return "", false
}

// TestCheckRefs runs the frozen resolution cases through Refs -> CheckRefs
// so the Reason texts are the real, byte-exact ones: a marker and a prose
// path resolve the same way, raw/ against sources and wiki/ against pages,
// and a page must exist as an anchor (037 T2).
func TestCheckRefs(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		valid  bool
		reason string
	}{
		{"page 3 of 3", "^[raw/papers/a.md p.3]", true, ""},
		{"page 1", "^[raw/papers/a.md p.1]", true, ""},
		{"range past the last anchor", "^[raw/papers/a.md p.2-4]", false, "page 4 not in raw/papers/a.md (pages 1-3)"},
		{"single page past the last anchor", "^[raw/papers/a.md p.9]", false, "page 9 not in raw/papers/a.md (pages 1-3)"},
		{"range wholly past the last anchor", "^[raw/papers/a.md p.7-9]", false, "page 7 not in raw/papers/a.md (pages 1-3)"},
		{"missing source", "^[raw/papers/b.md]", false, "no such source raw/papers/b.md"},
		{"missing source with a page", "^[raw/papers/b.md p.2]", false, "no such source raw/papers/b.md"},
		{"missing wiki page as a path", "see wiki/x.md", false, "no such page wiki/x.md"},
		{"missing source as a path", "see raw/papers/zzz.md", false, "no such source raw/papers/zzz.md"},
		{"pageless marker on an existing source", "^[raw/papers/a.md]", true, ""},
		{"paged marker on a source with no anchors", "^[raw/papers/c.md p.2]", false, "raw/papers/c.md has no page anchors"},
		{"pageless marker on a source with no anchors", "^[raw/papers/c.md]", true, ""},
		{"existing wiki page as a path", "see wiki/concepts/kv-cache.md.", true, ""},
		{"existing wiki page as a marker", "^[wiki/concepts/kv-cache.md]", true, ""},
		{"existing raw source as a path", "see raw/papers/a.md.", true, ""},
		{"a marker cite.Parse rejected keeps its reason", "^[raw/papers/a.md p.0]", false, cite.Parse("raw/papers/a.md p.0").Err},
		{"a target that is neither raw/ nor wiki/", "^[https://example.com/x]", false, "https://example.com/x is neither a raw/ source nor a wiki/ page"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			refs := Refs(tc.text)
			if len(refs) != 1 {
				t.Fatalf("Refs(%q) = %d refs, want 1", tc.text, len(refs))
			}
			got := CheckRefs(refs, fakeVault)
			if len(got) != 1 {
				t.Fatalf("CheckRefs returned %d refs, want 1", len(got))
			}
			if got[0].Valid != tc.valid || got[0].Reason != tc.reason {
				t.Errorf("CheckRefs = Valid %v Reason %q, want Valid %v Reason %q", got[0].Valid, got[0].Reason, tc.valid, tc.reason)
			}
			if got[0].Kind != refs[0].Kind || got[0].Target != refs[0].Target || got[0].Raw != refs[0].Raw {
				t.Errorf("CheckRefs changed identity fields: %+v -> %+v", refs[0], got[0])
			}
		})
	}
}

// TestCheckRefsKeepsOrderAndInput checks the bookkeeping around the verdicts:
// the result lines up one-to-one with the input, and the input slice itself
// is left alone (the report keeps the unchecked refs to show what the model
// wrote) (037 T2).
func TestCheckRefsKeepsOrderAndInput(t *testing.T) {
	refs := Refs(refsText)
	before := append([]Ref(nil), refs...)
	got := CheckRefs(refs, fakeVault)
	if len(got) != len(refs) {
		t.Fatalf("len = %d, want %d", len(got), len(refs))
	}
	if !reflect.DeepEqual(refs, before) {
		t.Errorf("CheckRefs mutated its input:\n%#v\nwas\n%#v", refs, before)
	}
	wantValid := []bool{true, true, false, false}
	for i, r := range got {
		if r.Valid != wantValid[i] {
			t.Errorf("ref %d (%s) Valid = %v, want %v (Reason %q)", i, r.Raw, r.Valid, wantValid[i], r.Reason)
		}
		if r.Valid && r.Reason != "" {
			t.Errorf("ref %d is valid but carries Reason %q", i, r.Reason)
		}
	}
	if got := CheckRefs(nil, fakeVault); len(got) != 0 {
		t.Errorf("CheckRefs(nil) = %v, want none", got)
	}
}

// TestAbstained pins the abstention phrases: matched after Normalize (case,
// whitespace, curly apostrophes), anywhere in the answer — the prompt's own
// "Not from your vault:" opener is only the most common wording (037 T2).
func TestAbstained(t *testing.T) {
	tests := []struct {
		name   string
		answer string
		want   bool
	}{
		{"the prompt's opener", "Not from your vault: TPU v7 launched after my training data.", true},
		{"mid-text, lower case", "Well, this is not from your vault, but TPUs are accelerators.", true},
		{"vault has nothing", "Your vault has nothing on TPU v7.", true},
		{"not in your vault", "That topic is Not In Your Vault.", true},
		{"no information in your vault", "There is no information in your vault about it.", true},
		{"vault does not cover", "The vault does not cover this.", true},
		{"vault doesn't cover", "The vault doesn't cover this.", true},
		{"vault doesn't cover, curly apostrophe", "The vault doesn’t cover this.", true},
		{"wrapped across a line break", "This is not from\nyour vault at all.", true},
		{"a real answer", "DeepSeek-V4 uses TileLang to write its fused kernels. ^[raw/papers/a.md p.3]", false},
		{"mentions the vault without abstaining", "Your vault contains three papers on KV caches.", false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Abstained(tc.answer); got != tc.want {
				t.Errorf("Abstained(%q) = %v, want %v", tc.answer, got, tc.want)
			}
		})
	}
}

// TestCitesAny pins what counts as citing a target: any ref — marker or
// path, valid or not — whose Target is one of them. Validity is CheckRefs'
// business; a model that cites the right source with a wrong page still
// cited it, and the invalid page is counted separately (037 T2).
func TestCitesAny(t *testing.T) {
	refs := CheckRefs(Refs(refsText), fakeVault)
	tests := []struct {
		name    string
		refs    []Ref
		targets []string
		want    bool
	}{
		{"a marker's source", refs, []string{"raw/papers/a.md"}, true},
		{"a prose path", refs, []string{"wiki/concepts/kv-cache.md"}, true},
		{"the second of several targets", refs, []string{"raw/none.md", "raw/papers/a.md"}, true},
		{"none of them", refs, []string{"raw/papers/z.md", "wiki/other.md"}, false},
		{"no targets", refs, nil, false},
		{"no refs", nil, []string{"raw/papers/a.md"}, false},
		{"unchecked refs still count", Refs(refsText), []string{"raw/papers/a.md"}, true},
		{"a prefix is not the path", refs, []string{"raw/papers/"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CitesAny(tc.refs, tc.targets); got != tc.want {
				t.Errorf("CitesAny(%v) = %v, want %v", tc.targets, got, tc.want)
			}
		})
	}
}
