package extract

// pdf_anchor.go implements 033 T1: page anchors in the PDF backend's
// markdown and the cache-version suffix that re-converts every pre-033
// entry. Docling's CLI has no page-break flag (probe on a real 58-page
// DeepSeek-V4 paper, 2026-09-23), so anchors come from aligning the
// DoclingDocument JSON's body-order items to the markdown: each anchor is
// the exact line `<!-- page N -->` placed immediately before the markdown
// line holding the first locatable item of page N, so a later 033 stage can
// cite PDF pages. The probe also showed the one miss was a table-only page,
// which is why table cell texts are probed exactly like text items.
//
// The JSON is never held whole — a book's DoclingDocument runs to tens of
// MB (F.P4) — so the reader streams it with json.Decoder and keeps only the
// tiny alignment records: per item, a page number and probe strings
// truncated to their first 40 runes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// PDFCacheVersionSuffix ends the PDF extractor's cache version string: a
// version bump makes every entry cached before 033 miss, so no cached Doc is
// ever served without its page anchors.
const PDFCacheVersionSuffix = "+anchors1"

// PDFCacheVersion is the cache version string for the PDF extractor: the
// sidecar's own version (PDFVersion) plus PDFCacheVersionSuffix.
func PDFCacheVersion(ctx context.Context, cfg PDFConfig) (string, error) {
	v, err := PDFVersion(ctx, cfg)
	if err != nil {
		return "", err
	}
	return v + PDFCacheVersionSuffix, nil
}

// Anchor insertion shares countJSONPages' streaming rules (F.P4), and the
// probe bounds are its alignment contract: a probe must carry at least
// minProbeRunes runes to be trusted (shorter strings match by accident all
// over a page), and only its first maxProbeRunes runes are searched for —
// the tail of a long paragraph is where Docling's rendering and the
// markdown diverge, while the head is what both keep verbatim.
const (
	minProbeRunes = 12
	maxProbeRunes = 40
)

// bodyNode is one node of the DoclingDocument's body tree: either a "$ref"
// into one of the item arrays ("#/texts/0", "#/tables/1", "#/pictures/0",
// "#/groups/2") or a group's inline children. Only the two fields the
// ordered walk needs are decoded.
type bodyNode struct {
	Ref      string     `json:"$ref"`
	Children []bodyNode `json:"children"`
}

// provItem is the one member of a "prov" array this alignment reads.
type provItem struct {
	PageNo int `json:"page_no"`
}

// textItem is one element of the "texts" array. Every real DoclingDocument
// item — texts included — carries its own "children" of refs.
type textItem struct {
	Text     string     `json:"text"`
	Children []bodyNode `json:"children"`
	Prov     []provItem `json:"prov"`
}

// tableItem is one element of the "tables" array: its locatable text lives
// in data.table_cells' cell texts. Its own "children" are refs too.
type tableItem struct {
	Children []bodyNode `json:"children"`
	Data     struct {
		TableCells []struct {
			Text string `json:"text"`
		} `json:"table_cells"`
	} `json:"data"`
	Prov []provItem `json:"prov"`
}

// pictureItem is one element of the "pictures" array — never locatable
// itself (a placeholder carries no text), but real Docling hangs the
// figure's caption text items off the picture's "children" (measured
// 2.130.0: body.children → {"$ref":"#/pictures/12"}, pictures[12].children
// → [{"$ref":"#/texts/699"}] with label "caption"), so the walk must
// resolve them or every caption-only page goes unanchored.
type pictureItem struct {
	Children []bodyNode `json:"children"`
	Prov     []provItem `json:"prov"`
}

// groupItem is one element of the "groups" array: a container whose
// children are refs, resolved recursively by the body walk.
type groupItem struct {
	Children []bodyNode `json:"children"`
}

// docItem is one body-order item's alignment record: its page (0 when the
// item carries no prov), its probe candidates — each already ≥
// minProbeRunes and truncated to maxProbeRunes runes — and the refs in the
// item's own "children", for the body walk to resolve after the item.
type docItem struct {
	page     int
	probes   []string
	children []bodyNode
}

// probeOf returns s's first maxProbeRunes runes when s carries at least
// minProbeRunes of them, else "" — the alignment's trust threshold.
func probeOf(s string) string {
	runes := []rune(s)
	if len(runes) < minProbeRunes {
		return ""
	}
	if len(runes) > maxProbeRunes {
		runes = runes[:maxProbeRunes]
	}
	return string(runes)
}

// mdEscapes renders s the way the docling markdown serializer renders text:
// underscores escaped (MarkdownParams escape_underscores, the default) and
// HTML-escaped `& < >` (escape_html), table-cell pipes as the `&#124;`
// entity (docling-core MarkdownDocSerializer, measured 2.130.0). Asterisks
// are not escaped and ligatures/hyphenation are not rewritten — nothing
// else in the item text changes on the way to markdown.
var mdEscapes = strings.NewReplacer(
	"_", `\_`, "&", "&amp;", "<", "&lt;", ">", "&gt;", "|", "&#124;",
)

// probeVariants returns the probe strings for one item text: the raw first
// maxProbeRunes runes (when the text carries minProbeRunes of them), then —
// when the serializer would have rewritten any of them — the same runes as
// the markdown renders them. Raw is searched first; the rendered variant
// only recovers items whose `_ & < > |` were escaped on the way to
// markdown, the pages a raw-only search anchors nothing on (2 of 58 on the
// real DeepSeek-V4 paper). A rendered match is the item's own text as
// rendered — the same trust a raw match carries, never a guess.
func probeVariants(s string) []string {
	p := probeOf(s)
	if p == "" {
		return nil
	}
	if !strings.ContainsAny(p, "_&<>|") {
		return []string{p}
	}
	return []string{p, mdEscapes.Replace(p)}
}

// provPage returns the page number of the first prov entry, or 0 when the
// item carries none.
func provPage(prov []provItem) int {
	if len(prov) == 0 {
		return 0
	}
	return prov[0].PageNo
}

// parseDocItems streams the DoclingDocument read from r into the
// body-order item list the alignment walks. Everything except the five
// keys below is skipped with the same token streaming countJSONPages uses;
// element-wise Decode keeps one array item in memory at a time, so the
// peak footprint stays proportional to the body tree plus the probe
// records, never to the file.
func parseDocItems(r io.Reader) ([]docItem, error) {
	dec := json.NewDecoder(r)
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("not a JSON object")
	}

	var body []bodyNode
	groups := map[int]groupItem{}
	texts := map[int]docItem{}
	tables := map[int]docItem{}
	pictures := map[int]pictureItem{}

	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch key, _ := keyTok.(string); key {
		case "body":
			// The body tree is one node per block — tiny records, decoded
			// whole; a book's tree is thousands of nodes, not MB.
			var root bodyNode
			if err := dec.Decode(&root); err != nil {
				return nil, err
			}
			body = root.Children
		case "groups":
			if err := decodeArray(dec, func(i int, g groupItem) { groups[i] = g }); err != nil {
				return nil, err
			}
		case "texts":
			if err := decodeArray(dec, func(i int, t textItem) {
				texts[i] = docItem{page: provPage(t.Prov), probes: probeVariants(t.Text), children: t.Children}
			}); err != nil {
				return nil, err
			}
		case "tables":
			if err := decodeArray(dec, func(i int, tb tableItem) {
				it := docItem{page: provPage(tb.Prov), children: tb.Children}
				for _, c := range tb.Data.TableCells {
					it.probes = append(it.probes, probeVariants(c.Text)...)
				}
				tables[i] = it
			}); err != nil {
				return nil, err
			}
		case "pictures":
			if err := decodeArray(dec, func(i int, p pictureItem) { pictures[i] = p }); err != nil {
				return nil, err
			}
		default:
			if err := skipJSONValue(dec); err != nil {
				return nil, err
			}
		}
	}

	var items []docItem
	appendRefItems(body, groups, texts, tables, pictures, &items, 0)
	return items, nil
}

// decodeArray streams a JSON array, visiting each element with its index.
func decodeArray[T any](dec *json.Decoder, visit func(i int, el T)) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return errors.New("not a JSON array")
	}
	for i := 0; dec.More(); i++ {
		var el T
		if err := dec.Decode(&el); err != nil {
			return err
		}
		visit(i, el)
	}
	_, err = dec.Token() // the closing ']'
	return err
}

// appendRefItems walks body nodes in order, appending each ref's alignment
// record to items and recursing — depth-capped against a pathological
// self-referencing document — into groups and into every resolved item's
// OWN "children" (real DoclingDocument shape, measured 2.130.0: every item
// carries them, and picture captions in particular live as the picture's
// child refs, which the markdown serializer emits right after the
// picture's placeholder). Item first, then its children — the same order
// the serializer renders them. Unresolvable or unknown refs — including
// item kinds this alignment does not model — contribute nothing.
func appendRefItems(nodes []bodyNode, groups map[int]groupItem,
	texts, tables map[int]docItem, pictures map[int]pictureItem, items *[]docItem, depth int) {
	if depth > 32 {
		return
	}
	for _, n := range nodes {
		if n.Ref != "" {
			if col, idx, ok := splitRef(n.Ref); ok {
				switch col {
				case "texts":
					it := texts[idx]
					*items = append(*items, it)
					appendRefItems(it.children, groups, texts, tables, pictures, items, depth+1)
				case "tables":
					it := tables[idx]
					*items = append(*items, it)
					appendRefItems(it.children, groups, texts, tables, pictures, items, depth+1)
				case "pictures":
					p := pictures[idx]
					*items = append(*items, docItem{page: provPage(p.Prov)})
					appendRefItems(p.Children, groups, texts, tables, pictures, items, depth+1)
				case "groups":
					if g, ok := groups[idx]; ok {
						appendRefItems(g.Children, groups, texts, tables, pictures, items, depth+1)
					}
				}
			}
		}
		appendRefItems(n.Children, groups, texts, tables, pictures, items, depth)
	}
}

// splitRef resolves "#/texts/0" into ("texts", 0, true).
func splitRef(ref string) (string, int, bool) {
	parts := strings.Split(ref, "/")
	if len(parts) != 3 || parts[0] != "#" || parts[1] == "" {
		return "", 0, false
	}
	idx, err := strconv.Atoi(parts[2])
	if err != nil {
		return "", 0, false
	}
	return parts[1], idx, true
}

// insertPageAnchors aligns items — in body order — against body and returns
// it with the `<!-- page N -->` anchors inserted, plus the anchor count.
//
// Page 1's anchor is always the first line; every later page is anchored
// only when it has a locatable item: one with a probe (a text field or a
// table cell text of ≥ minProbeRunes runes, raw or serializer-rendered)
// whose probe occurs in the markdown at or after the previous anchor. A
// page with no locatable item gets no anchor, so the anchors stay strictly
// increasing. A body tree that yields no items at all leaves the markdown
// untouched — a real DoclingDocument always carries one, and anchoring a
// sidecar's broken tree would re-pin every pre-033 markdown test for no
// information the alignment actually has.
func insertPageAnchors(body string, items []docItem) (string, int) {
	if body == "" || len(items) == 0 {
		return body, 0
	}

	type anchorAt struct {
		off  int // byte offset of the line the anchor goes before
		page int
	}
	anchors := []anchorAt{{off: 0, page: 1}} // page 1: always the first line
	lastPage, prevPos := 1, 0
	for _, it := range items {
		if it.page <= lastPage {
			continue
		}
		for _, probe := range it.probes {
			idx := strings.Index(body[prevPos:], probe)
			if idx < 0 {
				continue
			}
			off := lineStart(body, prevPos+idx)
			if off <= anchors[len(anchors)-1].off {
				// The item sits on the previous anchor's own line: the
				// anchors must stay strictly increasing, so this page —
				// whose first located item is here — gets no anchor.
				break
			}
			anchors = append(anchors, anchorAt{off: off, page: it.page})
			lastPage = it.page
			prevPos += idx
			break
		}
	}

	var b strings.Builder
	b.Grow(len(body) + 18*len(anchors))
	prev := 0
	for _, a := range anchors {
		b.WriteString(body[prev:a.off])
		fmt.Fprintf(&b, "<!-- page %d -->\n\n", a.page)
		prev = a.off
	}
	b.WriteString(body[prev:])
	return b.String(), len(anchors)
}

// lineStart returns the byte offset of the line containing pos.
func lineStart(s string, pos int) int {
	if i := strings.LastIndexByte(s[:pos], '\n'); i >= 0 {
		return i + 1
	}
	return 0
}
