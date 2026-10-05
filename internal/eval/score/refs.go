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
	// Kind is "marker" (a ^[…] marker), "path" (a raw/… or wiki/… .md path in
	// prose) or "wikilink" ([[target]] / [[target|alias]] naming a wiki/ or
	// raw/ path with or without .md — A-037-4).
	Kind   string
	Target string // vault-relative path; for a wikilink, as written (".md" may be missing, the alias is not part of it)
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

// wikilinkRe is the frozen wikilink grammar (A-037-4): [[target]] or
// [[target|alias]] where the target is a wiki/ or raw/ path of path
// characters, ".md" optional, and the alias is anything up to the closing
// "]]" on the same line. A slug link ([[kv-cache]]) names no path and a link
// with an anchor ([[wiki/a#h]]) is not a plain path, so neither matches.
// (037 T2.)
var wikilinkRe = regexp.MustCompile(`\[\[((?:wiki|raw)/[A-Za-z0-9._/-]+)(?:\|[^\]\n]*)?\]\]`)

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
// reviewer sees the grammar's own words. Prose paths and wikilinks are the
// pathRe and wikilinkRe matches that are not inside a marker (the marker is
// the reference; its path is not a second one), not in code, and that start
// a path of their own — a match glued to a longer path or URL
// ("https://h/x/raw/main/README.md", "foo/wiki/x.md", "foo[[wiki/x]]") is a
// fragment of someone else's text, not a vault reference. A path inside a
// wikilink ([[wiki/a.md]]) belongs to the wikilink: one reference, counted
// once. (037 T2, A-037-1, A-037-4.)
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
	for _, p := range proseRefs(text, markers) {
		all = append(all, found{p.start, Ref{Kind: p.kind, Target: p.target, Raw: text[p.start:p.end]}})
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

// proseRef is one prose reference found by proseRefs: its byte range, its
// Kind ("path" or "wikilink") and its Target.
type proseRef struct {
	start, end int
	kind       string
	target     string
}

// proseRefs returns the wikilinks and bare paths in text that sit outside
// every marker and outside code — fenced blocks and inline code spans, "code"
// meaning exactly what cite.Scan skips. It asks cite.Scan rather than
// re-implementing its fence and span rules, which a copy would let drift:
// each candidate is rewritten, in a scratch copy of the same byte length,
// into a well-formed marker "^[xxx]"; Scan then finds a marker at that offset
// if and only if it would have found one there, i.e. if the spot is not code.
// Before that, every "^[" that is not the start of a real marker is disarmed
// ('^' to '_'): such a "^[" had no "]" after it on its line, and the "]" the
// rewrite adds would otherwise let it swallow a path that is plainly prose.
// A wikilink is rewritten the same way ("[[wiki/a]]" becomes "^[xxxxxxx]"),
// and a path inside ANY wikilink match is left to the link, whether or not
// the link itself passed the boundary rule. (037 T2, A-037-4.)
func proseRefs(text string, markers []cite.Cite) []proseRef {
	inMarker := func(i int) bool {
		for _, c := range markers {
			if i >= c.Offset && i < c.Offset+len(c.Raw) {
				return true
			}
		}
		return false
	}
	links := wikilinkRe.FindAllStringSubmatchIndex(text, -1)
	inLink := func(i int) bool {
		for _, m := range links {
			if i >= m[0] && i < m[1] {
				return true
			}
		}
		return false
	}
	var cands []proseRef
	for _, m := range links {
		if !inMarker(m[0]) && startsPath(text, m[0]) {
			cands = append(cands, proseRef{m[0], m[1], "wikilink", text[m[2]:m[3]]})
		}
	}
	for _, m := range pathRe.FindAllStringIndex(text, -1) {
		if !inMarker(m[0]) && !inLink(m[0]) && startsPath(text, m[0]) {
			cands = append(cands, proseRef{m[0], m[1], "path", text[m[0]:m[1]]})
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
	for _, c := range cands {
		s, e := c.start, c.end // a candidate is at least "raw/x.md" or "[[raw/x]]": room for "^[" + "]"
		probe[s], probe[s+1], probe[e-1] = '^', '[', ']'
		for j := s + 2; j < e-1; j++ {
			probe[j] = 'x'
		}
	}
	prose := map[int]bool{}
	for _, c := range cite.Scan(string(probe)) {
		prose[c.Offset] = true
	}
	var out []proseRef
	for _, c := range cands {
		if prose[c.start] {
			out = append(out, c)
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
// frontmatter stripped — and wiki/ targets as pages. A wikilink resolves the
// same way once ".md" is appended to a target that lacks it (its Target
// stays as written). A paged marker also
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
			resolved := r
			if r.Kind == "wikilink" && !strings.HasSuffix(r.Target, ".md") {
				resolved.Target += ".md"
			}
			r.Reason = checkRef(resolved, resolve, sources)
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
// vault-relative path. A wikilink never counts (A-037-4): it is navigation,
// not citation, and cite_any measures citing evidence — the same answer must
// score the same before and after wikilinks became refs. Valid is deliberately not consulted: a model that
// cites the right source with a page that does not exist did cite it, and
// the bad page is counted by the invalid-ref tally instead. Reading Valid
// here would also make the answer depend on whether the caller had run
// CheckRefs yet. (037 T2.)
func CitesAny(refs []Ref, targets []string) bool {
	for _, r := range refs {
		if r.Kind != "wikilink" && slices.Contains(targets, r.Target) {
			return true
		}
	}
	return false
}

// RawEvidence reports whether any ref points at raw evidence: any ref whose
// target is under raw/, or a marker that does not name a wiki page (a marker
// is provenance, and "^[something]" is a claim of evidence whatever it
// names). A wiki page — as a prose path, a marker or a wikilink — is not: an
// answer that says "not from your vault" may still name what the vault does
// hold, and only an appeal to raw sources for the part it does not cover
// contradicts the label. (037 T2, A-037-3.)
func RawEvidence(refs []Ref) bool {
	for _, r := range refs {
		switch {
		case strings.HasPrefix(r.Target, "raw/"):
			return true
		case r.Kind == "marker" && !strings.HasPrefix(r.Target, "wiki/"):
			return true
		}
	}
	return false
}
