package score

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/awepo-pro/lw/internal/cite"
)

// Ref is one place an answer points at the vault: a ^[…] provenance marker,
// or a bare raw/ or wiki/ .md path written in prose. Refs fills everything
// but Valid; CheckRefs decides that. (037 T2.)
type Ref struct {
	Kind   string // "marker" (a ^[…] marker) | "path" (a raw/… or wiki/… .md path in prose)
	Target string // vault-relative path
	From   int    // page, 0 = none
	To     int
	Raw    string // as written
	Valid  bool
	Reason string // "" when Valid
}

// pathRe is the frozen prose-path grammar: a wiki/ or raw/ path of path
// characters ending in ".md". The class contains "." so the match is greedy
// through a sentence-final full stop and then gives it back to find the
// last ".md": "see wiki/a/b.md." yields "wiki/a/b.md", without the dot.
// (037 T2.)
var pathRe = regexp.MustCompile(`(?:wiki|raw)/[A-Za-z0-9._/-]+\.md`)

// pathLeftBoundary is the bytes that, directly before a pathRe match, mean
// the match is the tail of something longer rather than a path of its own:
// a letter or digit ("foo/wiki/x.md" is foo's subpath), a path or URL
// character ("https://h/x/raw/main/README.md", "../wiki/x.md", "k:wiki/x.md").
// Go's regexp has no look-behind, so the bytes are checked by hand. Opening
// punctuation, quotes, whitespace, start of text and non-ASCII bytes all
// leave the match a path of its own. (037 T2, A-037-1.)
const pathLeftBoundary = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789./_:-"

// startsPath reports whether the pathRe match beginning at byte i of text is
// a path of its own: it opens the text, or the byte before it is not one of
// pathLeftBoundary. No rejected match can hide an accepted one — every byte
// inside a match is itself in pathLeftBoundary, so a path starting inside
// another would be rejected too. (037 T2, A-037-1.)
func startsPath(text string, i int) bool {
	return i == 0 || strings.IndexByte(pathLeftBoundary, text[i-1]) < 0
}

// Refs returns every vault reference in text, in order of appearance, Valid
// unset. Markers come from cite.Scan — the marker grammar lives in one place
// — and a marker cite.Parse rejected carries that Err as its Reason, so the
// reviewer sees the grammar's own words. Prose paths are the pathRe matches
// that are neither inside a marker (the marker is the reference; its path is
// not a second one) nor in code, and that start a path of their own — a
// match glued to a longer path or URL ("https://h/x/raw/main/README.md",
// "foo/wiki/x.md") is a fragment of someone else's path, not a vault
// reference. (037 T2, A-037-1.)
func Refs(text string) []Ref {
	type found struct {
		off int
		ref Ref
	}
	var all []found
	markers := cite.Scan(text)
	for _, c := range markers {
		all = append(all, found{c.Offset, Ref{
			Kind: "marker", Target: c.Source, From: c.From, To: c.To, Raw: c.Raw, Reason: c.Err,
		}})
	}
	for _, m := range prosePaths(text, markers) {
		p := text[m[0]:m[1]]
		all = append(all, found{m[0], Ref{Kind: "path", Target: p, Raw: p}})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].off < all[j].off })
	if len(all) == 0 {
		return nil
	}
	refs := make([]Ref, len(all))
	for i, f := range all {
		refs[i] = f.ref
	}
	return refs
}

// prosePaths returns the [start, end) byte ranges of the pathRe matches in
// text that sit outside every marker and outside code — fenced blocks and
// inline code spans, "code" meaning exactly what cite.Scan skips. It asks
// cite.Scan rather than re-implementing its fence and span rules, which a
// copy would let drift: each candidate is rewritten, in a scratch copy of
// the same byte length, into a well-formed marker "^[xxx]"; Scan then finds a
// marker at that offset if and only if it would have found one there, i.e.
// if the spot is not code. Before that, every "^[" that is not the start of a
// real marker is disarmed ('^' to '_'): such a "^[" had no "]" after it on
// its line, and the "]" the rewrite adds would otherwise let it swallow a
// path that is plainly prose. (037 T2.)
func prosePaths(text string, markers []cite.Cite) [][2]int {
	inMarker := func(i int) bool {
		for _, c := range markers {
			if i >= c.Offset && i < c.Offset+len(c.Raw) {
				return true
			}
		}
		return false
	}
	var cands [][]int
	for _, m := range pathRe.FindAllStringIndex(text, -1) {
		if !inMarker(m[0]) && startsPath(text, m[0]) {
			cands = append(cands, m)
		}
	}
	if len(cands) == 0 {
		return nil
	}
	probe := []byte(text)
	for i := 0; i+1 < len(probe); i++ {
		if probe[i] == '^' && probe[i+1] == '[' && !inMarker(i) {
			probe[i] = '_'
		}
	}
	for _, m := range cands {
		s, e := m[0], m[1] // a match is at least "raw/x.md", 8 bytes: room for "^[" + "]"
		probe[s], probe[s+1], probe[e-1] = '^', '[', ']'
		for j := s + 2; j < e-1; j++ {
			probe[j] = 'x'
		}
	}
	prose := map[int]bool{}
	for _, c := range cite.Scan(string(probe)) {
		prose[c.Offset] = true
	}
	var out [][2]int
	for _, m := range cands {
		if prose[m[0]] {
			out = append(out, [2]int{m[0], m[1]})
		}
	}
	return out
}

// sourceInfo is what CheckRefs needs to know about one raw source, computed
// once per target: a page-heavy answer cites the same paper a dozen times,
// and re-splitting a 300 KB extracted PDF for each marker is the cost lint
// already learned to avoid. (037 T2.)
type sourceInfo struct {
	exists  bool
	anchors int // how many page anchors the body has
	last    int // the highest anchor page
}

// CheckRefs resolves every ref against the vault and returns a copy with
// Valid and Reason set; refs itself is not modified, so a report can still
// show what the model wrote. A marker and a prose path resolve the same way:
// raw/ targets must exist as raw sources — resolve returns the source BODY,
// frontmatter stripped — and wiki/ targets as pages. A paged marker also
// needs the source to carry page anchors and no page past the last one,
// exactly lint's cite-page rule (a page missing from the middle of the run
// is fine: table-only pages get no anchor, and the nearest one above covers
// them). A ref that arrives with a Reason — a marker Refs saw cite.Parse
// reject — keeps it and stays invalid. Any other target is invalid too: only
// raw/ sources and wiki/ pages are vault paths. (037 T2.)
func CheckRefs(refs []Ref, resolve func(path string) (body string, ok bool)) []Ref {
	out := make([]Ref, len(refs))
	sources := map[string]sourceInfo{}
	for i, r := range refs {
		if r.Reason == "" {
			r.Reason = checkRef(r, resolve, sources)
		}
		r.Valid = r.Reason == ""
		out[i] = r
	}
	return out
}

// checkRef returns r's resolution failure, "" when it resolves.
func checkRef(r Ref, resolve func(string) (string, bool), sources map[string]sourceInfo) string {
	switch {
	case strings.HasPrefix(r.Target, "raw/"):
		info, seen := sources[r.Target]
		if !seen {
			if body, ok := resolve(r.Target); ok {
				pages := cite.Pages(body)
				info = sourceInfo{exists: true, anchors: len(pages)}
				if len(pages) > 0 {
					info.last = slices.Max(pages)
				}
			}
			sources[r.Target] = info
		}
		if !info.exists {
			return "no such source " + r.Target
		}
		if r.From == 0 {
			return ""
		}
		if info.anchors == 0 {
			return r.Target + " has no page anchors"
		}
		for _, p := range []int{r.From, r.To} {
			if p > info.last {
				return fmt.Sprintf("page %d not in %s (pages 1-%d)", p, r.Target, info.last)
			}
		}
		return ""
	case strings.HasPrefix(r.Target, "wiki/"):
		if _, ok := resolve(r.Target); !ok {
			return "no such page " + r.Target
		}
		return ""
	default:
		return r.Target + " is neither a raw/ source nor a wiki/ page"
	}
}

// abstainPhrases are the wordings of "the vault does not answer this" the
// scorer accepts, compared after Normalize. The prompt asks for a first line
// reading "Not from your vault:", but a model also says it in its own words,
// and an abstention scored as a wrong answer would punish the right
// behaviour. The list is closed on purpose: a looser net ("vault" near "no")
// would score ordinary answers as abstentions. (037 T2.)
var abstainPhrases = []string{
	"not from your vault",
	"your vault has nothing",
	"not in your vault",
	"no information in your vault",
	"vault does not cover",
	"vault doesn't cover",
}

// Abstained reports whether answer says the vault does not cover the
// question, anywhere in the text (037 T2).
func Abstained(answer string) bool {
	norm := Normalize(answer)
	for _, p := range abstainPhrases {
		if strings.Contains(norm, p) {
			return true
		}
	}
	return false
}

// CitesAny reports whether any ref points at any of targets, by exact
// vault-relative path. Valid is deliberately not consulted: a model that
// cites the right source with a page that does not exist did cite it, and
// the bad page is counted by the invalid-ref tally instead. Reading Valid
// here would also make the answer depend on whether the caller had run
// CheckRefs yet. (037 T2.)
func CitesAny(refs []Ref, targets []string) bool {
	for _, r := range refs {
		if slices.Contains(targets, r.Target) {
			return true
		}
	}
	return false
}
