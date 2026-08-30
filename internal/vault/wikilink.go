package vault

import (
	"bytes"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"go.abhg.dev/goldmark/wikilink"
)

// Wikilink is one [[target]], [[target#fragment]], [[target|alias]] or
// [[target#fragment|alias]] occurrence.
type Wikilink struct {
	Target   string // target path/name only, e.g. "kv-cache" — fragment and alias split out
	Fragment string // "" when absent; the "#section" part, WITHOUT the leading "#"
	Alias    string // "" when absent
	Start    int    // byte offset of the leading "[[" in Page.Body
	End      int    // byte offset one past the trailing "]]"
	Line     int    // 1-based line number in Page.Body
}

// ParseWikilinks walks body's Markdown AST for [[target]], [[target#fragment]],
// [[target|alias]] and [[target#fragment|alias]] occurrences and returns one
// Wikilink per occurrence, in ascending Start order.
//
// Contract (backbone §2.5): goldmark plus go.abhg.dev/goldmark/wikilink, so
// links inside fenced code blocks and inline code are correctly skipped —
// a fenced block's contents never reach inline parsing at all, and a code
// span (backtick-delimited) is consumed as one atomic token by goldmark's
// inline scanner before the "[[" trigger ever gets a chance to fire on
// anything inside it.
func ParseWikilinks(body string) []Wikilink {
	src := []byte(body)
	md := goldmark.New(goldmark.WithExtensions(&wikilink.Extender{}))
	doc := md.Parser().Parse(text.NewReader(src))

	var links []Wikilink
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		wn, ok := n.(*wikilink.Node)
		// Embeds (![[target]]) are a different, undocumented-here markup
		// form (backbone §2.5 only defines the four forms above); skip
		// rather than guess at their semantics.
		if !ok || wn.Embed {
			return ast.WalkContinue, nil
		}
		if w, ok := extractWikilink(src, wn); ok {
			links = append(links, w)
		}
		return ast.WalkContinue, nil
	})

	// ast.Walk already visits inline nodes in document order, so links is
	// already ascending by Start; sort explicitly anyway so the contract
	// holds regardless of walk-order implementation details.
	sort.Slice(links, func(i, j int) bool { return links[i].Start < links[j].Start })
	return links
}

// extractWikilink recovers a Wikilink's exact [[...]] byte range from a
// *wikilink.Node, then re-derives Target/Fragment/Alias directly from those
// raw source bytes rather than from the node's own Target/Fragment fields.
//
// Why not trust the node's fields: go.abhg.dev/goldmark/wikilink's parser
// splits on the first "|" to find the alias/label *before* it splits the
// remainder on the last "#" to find the fragment (wikilink@v0.6.0's
// parser.go). Reconstructing our Start from that same intermediate state
// would need the length of that pre-fragment-split "target#fragment" slice,
// which the node throws away once it trims Target down to "target" — using
// the node's final, fragment-stripped Target's length there silently
// mis-locates "[[" whenever both a fragment and an alias are present (e.g.
// "[[a/b#Sec|Text]]"). Once Start/End are known, body[Start+2:End-2] is the
// literal inner text and can be split ourselves, which sidesteps that
// entirely and matches the node's own splitting order exactly (see
// splitWikilinkInner).
//
// What IS trustworthy from the node: its only child, an *ast.Text, carries
// a Segment covering the *label* text — the alias when "|alias" is present,
// or the whole inner text otherwise — and critically, that Segment's Stop is
// always the byte offset of the "]]", regardless of any alias/fragment
// splitting. Checking the single byte immediately before that Segment's
// Start tells us whether an alias was present; if so, "[[" must be the
// nearest preceding occurrence on the same source line (a wikilink can
// never span a line — go.abhg.dev/goldmark/wikilink requires it to close on
// the line it opens on), which we can locate directly in src without
// depending on any node field's length at all.
func extractWikilink(src []byte, wn *wikilink.Node) (Wikilink, bool) {
	child, ok := wn.FirstChild().(*ast.Text)
	if !ok {
		return Wikilink{}, false
	}
	seg := child.Segment

	anchor := seg.Start
	hasAlias := seg.Start > 0 && src[seg.Start-1] == '|'
	if hasAlias {
		anchor = seg.Start - 1 // the "|" byte; "[[" precedes it, same line
	}

	ls := lineStart(src, anchor)
	openIdx := bytes.LastIndex(src[ls:anchor], []byte("[["))
	if openIdx < 0 {
		return Wikilink{}, false
	}
	start := ls + openIdx
	end := seg.Stop + len("]]")
	if end > len(src) {
		return Wikilink{}, false
	}

	target, fragment, alias := splitWikilinkInner(string(src[start+2 : end-2]))

	return Wikilink{
		Target:   target,
		Fragment: fragment,
		Alias:    alias,
		Start:    start,
		End:      end,
		Line:     lineNumber(src, start),
	}, true
}

// splitWikilinkInner splits the raw text between "[[" and "]]" into its
// three parts, in real Obsidian syntax order: target, then an optional
// "#fragment", then an optional "|alias".
//
// The "|" is matched first (its first occurrence, matching
// go.abhg.dev/goldmark/wikilink's own parser), so a "#" that appears after
// it belongs to the alias, not a fragment — backbone §2.5, MASTER §9 D-Y:
// "[[foo|bar#notafragment]]" is Target "foo", Fragment "", Alias
// "bar#notafragment".
func splitWikilinkInner(inner string) (target, fragment, alias string) {
	targetAndFragment := inner
	if idx := strings.IndexByte(inner, '|'); idx >= 0 {
		targetAndFragment = inner[:idx]
		alias = inner[idx+1:]
	}
	target = targetAndFragment
	if idx := strings.LastIndexByte(targetAndFragment, '#'); idx >= 0 {
		target = targetAndFragment[:idx]
		fragment = targetAndFragment[idx+1:]
	}
	return target, fragment, alias
}

// lineNumber returns the 1-based line number of byte offset pos within src.
func lineNumber(src []byte, pos int) int {
	line := 1
	for _, b := range src[:pos] {
		if b == '\n' {
			line++
		}
	}
	return line
}

// RewriteWikilinks rewrites the target of every link in links for which
// newTarget returns true, and returns the resulting body.
//
// Contract (backbone §2.5): applies replacements in descending Start order
// so earlier offsets stay valid as the string is rebuilt; a newTarget
// returning false leaves that link byte-identical; both the fragment and
// the alias are always preserved, re-emitted as
// "[[<new>#<Fragment>|<Alias>]]" with whichever part is empty omitted.
func RewriteWikilinks(body string, links []Wikilink, newTarget func(w Wikilink) (string, bool)) string {
	ordered := append([]Wikilink(nil), links...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Start > ordered[j].Start })

	for _, w := range ordered {
		target, ok := newTarget(w)
		if !ok {
			continue
		}
		replacement := "[[" + target
		if w.Fragment != "" {
			replacement += "#" + w.Fragment
		}
		if w.Alias != "" {
			replacement += "|" + w.Alias
		}
		replacement += "]]"
		body = body[:w.Start] + replacement + body[w.End:]
	}
	return body
}
