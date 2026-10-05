package score

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/awepo-pro/lw/internal/cite"
)

// refKinds renders every ref as kind:target|raw, in order, for the tables
// below.
func refKinds(text string) []string {
	var out []string
	for _, r := range Refs(text) {
		out = append(out, r.Kind+":"+r.Target+"|"+r.Raw)
	}
	return out
}

// TestRefsWikilinks pins A-037-4: [[target]] and [[target|alias]], where the
// target is a wiki/ or raw/ path with or without ".md", are refs of Kind
// "wikilink" — Target as written (the alias is not part of it), Raw the whole
// link. A wikilink is one ref, never also a "path" ref for the .md inside it;
// a slug link ([[kv-cache]]) names no path and is no ref; code is skipped as
// for every other ref, and the same left-boundary rule applies (037 T2).
func TestRefsWikilinks(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{"a wiki path without .md", "see [[wiki/queries/x]]", []string{"wikilink:wiki/queries/x|[[wiki/queries/x]]"}},
		{"a wiki path with .md is one ref, not two", "[[wiki/concepts/kv-cache.md]]",
			[]string{"wikilink:wiki/concepts/kv-cache.md|[[wiki/concepts/kv-cache.md]]"}},
		{"an alias is not part of the target", "[[wiki/a|the a page]]", []string{"wikilink:wiki/a|[[wiki/a|the a page]]"}},
		{"an alias after a .md target", "[[raw/papers/x.md|the paper]]", []string{"wikilink:raw/papers/x.md|[[raw/papers/x.md|the paper]]"}},
		{"a raw path", "[[raw/papers/x]]", []string{"wikilink:raw/papers/x|[[raw/papers/x]]"}},
		{"a slug link is not a ref", "see [[kv-cache]] and [[gpt-4]]", nil},
		{"an anchor makes it no path link", "[[wiki/a#heading]]", nil},
		{"not wiki or raw", "[[notes/a]] and [[http://x/wiki/a]]", nil},
		{"a bare directory", "[[wiki/]] and [[raw/]]", nil},
		{"two on one line, in order", "[[wiki/a]] then [[raw/b]]", []string{"wikilink:wiki/a|[[wiki/a]]", "wikilink:raw/b|[[raw/b]]"}},
		{"in order with a marker and a prose path", "^[raw/a.md] and [[wiki/b]] then wiki/c.md",
			[]string{"marker:raw/a.md|^[raw/a.md]", "wikilink:wiki/b|[[wiki/b]]", "path:wiki/c.md|wiki/c.md"}},
		{"inline code span", "use `[[wiki/a]]` here", nil},
		{"fenced block", "```\n[[wiki/a]]\n```\nafter [[wiki/b]]", []string{"wikilink:wiki/b|[[wiki/b]]"}},
		{"after opening punctuation", "([[wiki/a]])", []string{"wikilink:wiki/a|[[wiki/a]]"}},
		{"at the start of a line", "intro\n[[wiki/a]]", []string{"wikilink:wiki/a|[[wiki/a]]"}},
		{"after a multibyte character", "见[[wiki/a]]", []string{"wikilink:wiki/a|[[wiki/a]]"}},
		{"after a letter is the tail of something longer", "foo[[wiki/a]]", nil},
		{"after a letter, its .md is not a path either", "foo[[wiki/a.md]]", nil},
		{"after a colon", "k:[[wiki/a]]", nil},
		{"after a slash", "x/[[wiki/a]]", nil},
		{"an alias may hold spaces and punctuation", "[[wiki/a|why, exactly?]]", []string{"wikilink:wiki/a|[[wiki/a|why, exactly?]]"}},
		{"an unclosed link is no ref", "[[wiki/a and more", nil},
		{"an unclosed link still leaves its .md as a path", "[[wiki/a.md and more", []string{"path:wiki/a.md|wiki/a.md"}},
		{"a link may not span lines", "[[wiki/a|alias\ncontinued]]", nil},
		{"the sentence dot after a link", "see [[wiki/a]].", []string{"wikilink:wiki/a|[[wiki/a]]"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := refKinds(tc.text)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Refs(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

// TestRefsWikilinkCodeAgreesWithCite is TestRefsCodeAgreesWithCite for
// wikilinks: in every context where cite.Scan finds a marker, Refs finds the
// wikilink, and in every context where it does not (code), neither does Refs
// (037 T2, A-037-4).
func TestRefsWikilinkCodeAgreesWithCite(t *testing.T) {
	contexts := []string{
		"plain %s prose",
		"`%s`",
		"before `%s` after",
		"``a ` %s`` tail",
		"```\n%s\n```",
		"```go\nx\n%s\n```\nafter",
		"~~~\n%s\n~~~\nafter",
		"  ```\n%s\n  ```",
		"```\nunterminated fence\n%s",
		"`unterminated span %s",
		"line one\n`a`\n%s",
		"`a` %s `b`",
		"> quote %s",
		"- bullet %s\n- other",
		"^[ closed ^[x] then %s",
	}
	for _, tpl := range contexts {
		marker := fmt.Sprintf(tpl, "^[raw/x.md]")
		link := fmt.Sprintf(tpl, "[[raw/x]]")
		viaCite := len(cite.Scan(marker)) > 0
		viaRefs := false
		for _, r := range Refs(link) {
			viaRefs = viaRefs || r.Kind == "wikilink"
		}
		if viaCite != viaRefs {
			t.Errorf("context %q: cite.Scan finds a marker = %v, Refs finds a wikilink = %v", tpl, viaCite, viaRefs)
		}
	}
}

// TestCheckRefsWikilinks pins that a wikilink resolves like a path ref once
// ".md" is appended when it is missing, and that the Target and Raw stay as
// written (037 T2, A-037-4).
func TestCheckRefsWikilinks(t *testing.T) {
	tests := []struct {
		text, reason string
		valid        bool
	}{
		{"[[wiki/concepts/kv-cache]]", "", true},
		{"[[wiki/concepts/kv-cache.md]]", "", true},
		{"[[wiki/concepts/kv-cache|the cache]]", "", true},
		{"[[wiki/queries/missing]]", "no such page wiki/queries/missing.md", false},
		{"[[wiki/queries/missing.md]]", "no such page wiki/queries/missing.md", false},
		{"[[raw/papers/a]]", "", true},
		{"[[raw/papers/c.md|no anchors]]", "", true},
		{"[[raw/papers/b]]", "no such source raw/papers/b.md", false},
	}
	for _, tc := range tests {
		refs := Refs(tc.text)
		if len(refs) != 1 {
			t.Fatalf("Refs(%q) = %+v, want one wikilink", tc.text, refs)
		}
		got := CheckRefs(refs, fakeVault)[0]
		if got.Valid != tc.valid || got.Reason != tc.reason {
			t.Errorf("%s: Valid %v Reason %q; want %v %q", tc.text, got.Valid, got.Reason, tc.valid, tc.reason)
		}
		if got.Kind != "wikilink" || got.Raw != refs[0].Raw {
			t.Errorf("%s: kind/raw changed by CheckRefs: %+v", tc.text, got)
		}
		if got.Target != refs[0].Target {
			t.Errorf("%s: CheckRefs rewrote the Target %q to %q", tc.text, refs[0].Target, got.Target)
		}
	}
}

// TestCitesAnyIgnoresWikilinks pins that cite_expected keeps measuring
// citing EVIDENCE: a wikilink is navigation, so naming a source in one never
// satisfies a cite_any entry — exactly as before wikilinks were refs (037
// T2, A-037-4).
func TestCitesAnyIgnoresWikilinks(t *testing.T) {
	targets := []string{"raw/papers/a.md", "wiki/concepts/kv-cache.md"}
	for _, text := range []string{"[[raw/papers/a.md]]", "[[wiki/concepts/kv-cache.md|x]]"} {
		if CitesAny(Refs(text), targets) {
			t.Errorf("CitesAny(%q) = true; a wikilink is not a citation", text)
		}
	}
	for _, text := range []string{"^[raw/papers/a.md]", "wiki/concepts/kv-cache.md", "[[wiki/x]] and ^[raw/papers/a.md p.2]"} {
		if !CitesAny(Refs(text), targets) {
			t.Errorf("CitesAny(%q) = false; a marker or a prose path still counts", text)
		}
	}
}

// TestRawEvidence pins the question abstain_ok asks (A-037-3): does the text
// point at raw evidence? A marker (to anything but a wiki page) or any ref
// to raw/ does; wiki pages — prose path, marker or wikilink — do not: an
// answer may name what the vault does hold without citing evidence for the
// part it does not (037 T2).
func TestRawEvidence(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"", false},
		{"no refs at all, just figures: 8B params", false},
		{"^[raw/a.md]", true},
		{"^[raw/a.md p.3]", true},
		{"^[raw/a.md p.0]", true}, // a malformed marker still points at raw
		{"^[something-else]", true},
		{"see raw/a.md", true},
		{"[[raw/a]]", true},
		{"[[raw/a.md|the paper]]", true},
		{"see wiki/a.md", false},
		{"^[wiki/a.md]", false},
		{"[[wiki/queries/x]]", false},
		{"[[wiki/queries/x]] and wiki/b.md", false},
		{"[[wiki/queries/x]] and ^[raw/a.md]", true},
		{"`^[raw/a.md]` in code", false},
	}
	for _, tc := range tests {
		if got := RawEvidence(Refs(tc.text)); got != tc.want {
			t.Errorf("RawEvidence(Refs(%q)) = %v, want %v", tc.text, got, tc.want)
		}
	}
}
