package extract

// html.go implements backbone §10's NewHTML: an Extractor for saved web
// pages, fetched over http/https or read straight off disk when the
// argument is already a local .html/.htm file (the shape `lw ingest
// <path-to-saved-page.html>` needs — the tool layer's stage.ingest_source
// only ever accepts a local path, so a URL source is always fetched and
// turned into local content by this package before anything is staged).
//
// Determinism is the whole contract (backbone §10): the same bytes must
// walk the DOM the same way and produce the same markdown every time, so
// the tree walk below never ranges a map, never consults the wall clock,
// and never reflows or wraps prose — each block becomes exactly one line,
// however long.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/net/html"
)

// dropTags are elements whose entire subtree — including any text nested
// inside them — is discarded rather than walked (backbone §10). "head" is
// included so collectBlocks never turns <title>/<meta>/<style> siblings
// into body content; titleTagText below searches for <title> separately,
// deliberately bypassing this set, since head is exactly where it lives.
var dropTags = map[string]bool{
	"script": true,
	"style":  true,
	"nav":    true,
	"footer": true,
	"aside":  true,
	"head":   true,
}

// htmlExtractor is the Extractor NewHTML returns.
type htmlExtractor struct {
	client *http.Client
}

// NewHTML returns an Extractor for http/https sources, and for a local
// path ending in .html or .htm — saved pages are exactly what this
// subtask's own fixtures and manual verification use, and no network call
// is made for them (backbone §10).
func NewHTML(client *http.Client) Extractor {
	if client == nil {
		client = http.DefaultClient
	}
	return &htmlExtractor{client: client}
}

// CanHandle reports whether uri is an http/https URL or a local path with
// an .html/.htm extension.
func (h *htmlExtractor) CanHandle(uri string) bool {
	if isRemoteURL(uri) {
		return true
	}
	ext := strings.ToLower(filepath.Ext(uri))
	return ext == ".html" || ext == ".htm"
}

// Extract fetches uri (http/https) or reads it from disk (a local
// .html/.htm path), then parses the bytes into a deterministic Doc.
func (h *htmlExtractor) Extract(ctx context.Context, uri string) (*Doc, error) {
	body, err := h.read(ctx, uri)
	if err != nil {
		return nil, err
	}
	doc, err := parseHTMLDoc(body)
	if err != nil {
		return nil, fmt.Errorf("extract: parse %s: %w", uri, err)
	}
	doc.SourceURL = uri
	return doc, nil
}

// read returns uri's raw bytes: an HTTP GET for http/https, otherwise a
// local file read.
func (h *htmlExtractor) read(ctx context.Context, uri string) ([]byte, error) {
	if !isRemoteURL(uri) {
		b, err := os.ReadFile(uri)
		if err != nil {
			return nil, fmt.Errorf("extract: read %s: %w", uri, err)
		}
		return b, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, fmt.Errorf("extract: build request for %s: %w", uri, err)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		// The host guard (client.go) travels inside net/http's *url.Error
		// wrapper wherever it fired — the initial request or any redirect
		// hop. Unwrap it so callers see its exact, bare message.
		var blocked *blockedHostError
		if errors.As(err, &blocked) {
			return nil, blocked
		}
		return nil, fmt.Errorf("extract: fetch %s: %w", uri, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("extract: fetch %s: unexpected status %s", uri, resp.Status)
	}
	// MaxBodyBytes+1 lets read tell "exactly at the cap" apart from "ran
	// past it": only an over-long body yields more than MaxBodyBytes.
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("extract: read response body from %s: %w", uri, err)
	}
	if len(b) > MaxBodyBytes {
		return nil, errBodyExceedsLimit
	}
	return b, nil
}

// isRemoteURL reports whether uri parses with an http or https scheme.
func isRemoteURL(uri string) bool {
	u, err := url.Parse(uri)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https")
}

// parseHTMLDoc is the deterministic core NewHTML's Extract delegates to,
// tested directly on bytes (no network, no filesystem) by this package's
// golden tests. Title prefers the document's first <h1>, falling back to
// <title>; Markdown is every whitelisted block, in document order, joined
// by a single blank line and ending with exactly one newline
// (00-conventions.md §2's "every file ends with exactly one newline").
func parseHTMLDoc(body []byte) (*Doc, error) {
	root, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}

	title := firstH1(root)
	if title == "" {
		title = titleTagText(root)
	}

	var blocks []string
	collectBlocks(root, &blocks)
	md := strings.TrimSpace(strings.Join(blocks, "\n\n"))
	if md != "" {
		md += "\n"
	}

	return &Doc{
		Title:     title,
		Markdown:  md,
		Kind:      "article",
		Extractor: "go/html",
	}, nil
}

// collectBlocks walks n in document order, appending one markdown block to
// blocks for each whitelisted block-level element it finds — h1-h6, p,
// pre, blockquote, ul, ol, table (backbone §10's whitelist) — and never
// descending into a dropTags subtree. Any other element (div, span,
// article, body, html, …) is transparent: collectBlocks recurses into its
// children looking for whitelisted content further down, exactly as a
// naive reading of "keep h1-h6,p,ul,ol,li,pre,code,blockquote,a,strong,em,
// table,tr,td,th; drop script/style/nav/footer/aside" requires — nothing
// else is inferred about the document's structure.
func collectBlocks(n *html.Node, blocks *[]string) {
	if n.Type == html.ElementNode {
		if dropTags[n.Data] {
			return
		}
		switch n.Data {
		case "h1", "h2", "h3", "h4", "h5", "h6":
			if text := collapseWS(inlineText(n)); text != "" {
				level := int(n.Data[1] - '0')
				*blocks = append(*blocks, strings.Repeat("#", level)+" "+text)
			}
			return
		case "p":
			if text := collapseWS(inlineText(n)); text != "" {
				*blocks = append(*blocks, text)
			}
			return
		case "pre":
			if text := strings.Trim(rawText(n), "\n"); text != "" {
				*blocks = append(*blocks, "```\n"+text+"\n```")
			}
			return
		case "blockquote":
			var inner []string
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				collectBlocks(c, &inner)
			}
			if len(inner) > 0 {
				*blocks = append(*blocks, quoteBlock(strings.Join(inner, "\n\n")))
			}
			return
		case "ul":
			if s := renderList(n, false); s != "" {
				*blocks = append(*blocks, s)
			}
			return
		case "ol":
			if s := renderList(n, true); s != "" {
				*blocks = append(*blocks, s)
			}
			return
		case "table":
			if s := renderTable(n); s != "" {
				*blocks = append(*blocks, s)
			}
			return
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		collectBlocks(c, blocks)
	}
}

// quoteBlock prefixes every line of s with "> ", the markdown blockquote
// marker, using a bare "> " for lines a blank line between blockquote
// paragraphs would otherwise leave empty.
func quoteBlock(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l == "" {
			lines[i] = ">"
		} else {
			lines[i] = "> " + l
		}
	}
	return strings.Join(lines, "\n")
}

// renderList renders every direct <li> child of n as one markdown list
// line, "- " for ul or "1. " for every ol item (valid, deterministic
// markdown — renderers number an all-"1." ordered list themselves), and
// recurses into a nested ul/ol found inside an <li> as an indented
// sub-list beneath it.
func renderList(n *html.Node, ordered bool) string {
	var items []string
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == "li" {
			items = append(items, renderListItem(c, ordered))
		}
	}
	return strings.Join(items, "\n")
}

// renderListItem renders one <li>'s own inline text plus, when present,
// its nested ul/ol as an indented block on the following lines.
func renderListItem(li *html.Node, ordered bool) string {
	marker := "-"
	if ordered {
		marker = "1."
	}

	var text strings.Builder
	var subLists []string
	for c := li.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && (c.Data == "ul" || c.Data == "ol") {
			if sub := renderList(c, c.Data == "ol"); sub != "" {
				subLists = append(subLists, indentBlock(sub, "  "))
			}
			continue
		}
		text.WriteString(inlineText(c))
	}

	line := marker + " " + collapseWS(text.String())
	if len(subLists) > 0 {
		line += "\n" + strings.Join(subLists, "\n")
	}
	return line
}

// indentBlock prefixes every line of s with prefix.
func indentBlock(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// renderTable renders every <tr> found anywhere under n (so a thead/tbody
// wrapper is transparent, matching collectBlocks' own "unlisted elements
// are transparent" rule) as a GFM-style pipe table, treating the first row
// as the header and padding any short row with empty cells rather than
// dropping it.
func renderTable(n *html.Node) string {
	var rows [][]string
	collectRows(n, &rows)
	if len(rows) == 0 {
		return ""
	}

	cols := len(rows[0])
	var b strings.Builder
	writeRow := func(cells []string) {
		b.WriteString("|")
		for i := 0; i < cols; i++ {
			cell := ""
			if i < len(cells) {
				cell = cells[i]
			}
			b.WriteString(" " + cell + " |")
		}
		b.WriteString("\n")
	}

	writeRow(rows[0])
	b.WriteString("|")
	for i := 0; i < cols; i++ {
		b.WriteString(" --- |")
	}
	b.WriteString("\n")
	for _, r := range rows[1:] {
		writeRow(r)
	}
	return strings.TrimRight(b.String(), "\n")
}

// collectRows appends one []string per <tr> found under n, in document
// order, to rows.
func collectRows(n *html.Node, rows *[][]string) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == "tr" {
			var cells []string
			for cc := c.FirstChild; cc != nil; cc = cc.NextSibling {
				if cc.Type == html.ElementNode && (cc.Data == "td" || cc.Data == "th") {
					cells = append(cells, collapseWS(inlineText(cc)))
				}
			}
			*rows = append(*rows, cells)
			continue
		}
		collectRows(c, rows)
	}
}

// inlineText renders n and its descendants as inline markdown: <a> becomes
// a markdown link, <strong>/<b> becomes "**…**", <em>/<i> becomes "*…*",
// <code> becomes "`…`" (backbone §10's whitelist), a dropTags element
// contributes nothing, and any other element is transparent — its
// children are rendered but it adds no markup of its own. Whitespace is
// collapsed only once, by the block-level caller (collapseWS), so
// concatenating raw text nodes here is deliberate: the original source
// whitespace between elements is what makes "<b>a</b> b" render as
// "**a** b" rather than "**a**b".
func inlineText(n *html.Node) string {
	switch n.Type {
	case html.TextNode:
		return n.Data
	case html.ElementNode:
		if dropTags[n.Data] {
			return ""
		}
		switch n.Data {
		case "a":
			inner := inlineChildren(n)
			href := attrVal(n, "href")
			if href == "" || strings.TrimSpace(inner) == "" {
				return inner
			}
			return "[" + inner + "](" + href + ")"
		case "strong", "b":
			inner := inlineChildren(n)
			if strings.TrimSpace(inner) == "" {
				return inner
			}
			return "**" + inner + "**"
		case "em", "i":
			inner := inlineChildren(n)
			if strings.TrimSpace(inner) == "" {
				return inner
			}
			return "*" + inner + "*"
		case "code":
			inner := inlineChildren(n)
			if strings.TrimSpace(inner) == "" {
				return inner
			}
			return "`" + inner + "`"
		default:
			return inlineChildren(n)
		}
	default:
		return inlineChildren(n)
	}
}

// inlineChildren concatenates inlineText over every child of n, in order.
func inlineChildren(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(inlineText(c))
	}
	return b.String()
}

// rawText concatenates every text node under n verbatim, with no
// whitespace collapsing, for <pre> — the one place source whitespace is
// significant.
func rawText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.ElementNode && dropTags[x.Data] {
			return
		}
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
			return
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

// collapseWS collapses every run of whitespace in s to a single space and
// trims the result — how HTML itself renders inline whitespace, and the
// one normalization backbone §10's "never reflow or wrap" allows: a block
// still becomes exactly one line, however long, never re-wrapped at a
// column width.
func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// firstH1 returns the collapsed inline text of the first <h1> found in
// document order, skipping dropTags subtrees, or "" if there is none.
func firstH1(root *html.Node) string {
	var found string
	var walk func(*html.Node) bool
	walk = func(n *html.Node) bool {
		if n.Type == html.ElementNode {
			if dropTags[n.Data] {
				return false
			}
			if n.Data == "h1" {
				found = collapseWS(inlineText(n))
				return true
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if walk(c) {
				return true
			}
		}
		return false
	}
	walk(root)
	return found
}

// titleTagText returns the collapsed text of the document's <title>
// element, or "" if there is none. It deliberately does not consult
// dropTags — head is dropped from collectBlocks' body walk, but this is
// the one place title's fallback needs to look inside it.
func titleTagText(root *html.Node) string {
	var found string
	var walk func(*html.Node) bool
	walk = func(n *html.Node) bool {
		if n.Type == html.ElementNode && n.Data == "title" {
			found = collapseWS(textOnly(n))
			return true
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if walk(c) {
				return true
			}
		}
		return false
	}
	walk(root)
	return found
}

// textOnly concatenates every text node under n verbatim, ignoring any
// element structure — used only for <title>, which never carries markup.
func textOnly(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
		} else {
			b.WriteString(textOnly(c))
		}
	}
	return b.String()
}

// attrVal returns n's attribute value for key, or "" if n has none.
func attrVal(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
