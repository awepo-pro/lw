// Package cards extracts spaced-repetition flashcards from wiki pages
// and exports them in Anki, Markdown, and JSON formats with stable IDs.
package cards

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/awepo-pro/lw/internal/cite"
	"github.com/awepo-pro/lw/internal/vault"
)

// Default limits for card back rune counts.
const (
	defaultMaxBackRunes = 600
	defaultMinBackRunes = 40
	minCardBackRunes    = 20
)

// Card is one spaced-repetition flashcard extracted from a wiki page.
type Card struct {
	ID      string   `json:"id"`
	Front   string   `json:"front"`
	Back    string   `json:"back"`
	Path    string   `json:"path"`
	Title   string   `json:"title"`
	Section string   `json:"section"`
	Tags    []string `json:"tags"`
}

// Options controls card extraction filtering and rune boundaries.
type Options struct {
	Type    string   `json:"type"`
	Tags    []string `json:"tags"` // AND filter
	MaxBack int      `json:"max_back"`
	MinBack int      `json:"min_back"`
}

// skippedHeadings lists level-2 headings that do not become flashcards.
var skippedHeadings = map[string]bool{
	"sources":        true,
	"references":     true,
	"related":        true,
	"see also":       true,
	"links":          true,
	"abstract":       true,
	"notes":          true,
	"open questions": true,
	"backlinks":      true,
}

var (
	reHorizontalSpaces = regexp.MustCompile(`[ \t]+`)
	reMultipleNewlines = regexp.MustCompile(`\n{3,}`)
	reOrderedItem      = regexp.MustCompile(`^[0-9]+\.\s+(.*)`)
	reBelowOrFollowing = regexp.MustCompile(`(?i)\b(below|following)\b`)
)

// Extract extracts flashcards from every page in v matching o.
func Extract(v *vault.Vault, o Options) []Card {
	if o.MaxBack <= 0 {
		o.MaxBack = defaultMaxBackRunes
	}
	if o.MinBack <= 0 {
		o.MinBack = defaultMinBackRunes
	}

	pages := v.Pages()
	var cards []Card

	for _, p := range pages {
		if !matchesFilter(p, o) {
			continue
		}
		pageCards := extractPageCards(v, p, o)
		cards = append(cards, pageCards...)
	}

	return cards
}

// matchesFilter checks if p satisfies Type and Tags filters in o.
func matchesFilter(p *vault.Page, o Options) bool {
	if o.Type != "" {
		if !strings.EqualFold(string(p.FM.Type), o.Type) {
			return false
		}
	}
	if len(o.Tags) > 0 {
		for _, required := range o.Tags {
			reqNorm := normalizeTag(required)
			found := false
			for _, pt := range p.FM.Tags {
				if normalizeTag(pt) == reqNorm {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	return true
}

// extractPageCards extracts the lead card and level-2 section cards of p.
func extractPageCards(v *vault.Vault, p *vault.Page, o Options) []Card {
	var out []Card
	tags := buildTags(p.FM.Type, p.FM.Tags)

	// 1. Lead card
	if leadCard, ok := extractLeadCard(v, p, o, tags); ok {
		out = append(out, leadCard)
	}

	// 2. Section cards
	sectionCards := extractSectionCards(v, p, o, tags)
	out = append(out, sectionCards...)

	return out
}

// extractLeadCard extracts the lead card for page p, if eligible.
func extractLeadCard(v *vault.Vault, p *vault.Page, o Options, tags []string) (Card, bool) {
	leadStart := 0
	if len(p.Sections) > 0 && p.Sections[0].Level == 1 {
		leadStart = p.Sections[0].Body
	}

	leadEnd := len(p.Body)
	for _, sec := range p.Sections {
		if sec.Level == 2 {
			leadEnd = sec.Start
			break
		}
	}

	if leadStart > leadEnd || leadStart > len(p.Body) {
		return Card{}, false
	}
	leadText := p.Body[leadStart:leadEnd]

	blocks := extractBlocks(leadText)
	var firstProse string
	found := false
	for _, b := range blocks {
		if b.kind == blockParagraph {
			firstProse = b.text
			found = true
			break
		}
	}
	if !found {
		return Card{}, false
	}

	back := cleanBack(v, firstProse, o.MaxBack)
	if utf8.RuneCountInString(back) < minCardBackRunes {
		return Card{}, false
	}

	front := leadFront(p.FM.Type, p.FM.Title)
	card := Card{
		ID:      computeID(p.Path, ""),
		Front:   front,
		Back:    back,
		Path:    p.Path,
		Title:   p.FM.Title,
		Section: "",
		Tags:    tags,
	}
	return card, true
}

// leadFront builds the front prompt for a lead card.
func leadFront(pt vault.PageType, title string) string {
	title = strings.TrimSpace(title)
	switch strings.ToLower(string(pt)) {
	case "entity", "concept":
		return "What is " + title + "?"
	case "comparison":
		return "Compare: " + title
	case "query":
		if strings.HasSuffix(title, "?") {
			return title
		}
		return title + "?"
	default:
		return title + ": summary"
	}
}

// extractSectionCards extracts cards for eligible level-2 sections of p.
func extractSectionCards(v *vault.Vault, p *vault.Page, o Options, tags []string) []Card {
	var cards []Card

	for _, sec := range p.Sections {
		if sec.Level != 2 {
			continue
		}
		if isSkippedSection(sec.Title) {
			continue
		}

		secStart := sec.Body
		secEnd := sec.End
		if secStart < 0 || secStart > len(p.Body) || secEnd > len(p.Body) || secStart > secEnd {
			continue
		}

		secText := p.Body[secStart:secEnd]
		blocks := extractBlocks(secText)
		if len(blocks) == 0 {
			continue
		}

		back := buildSectionBack(v, blocks, o)
		if utf8.RuneCountInString(back) < minCardBackRunes {
			continue
		}

		front := p.FM.Title + " \u2014 " + sec.Title
		c := Card{
			ID:      computeID(p.Path, sec.Title),
			Front:   front,
			Back:    back,
			Path:    p.Path,
			Title:   p.FM.Title,
			Section: sec.Title,
			Tags:    tags,
		}
		cards = append(cards, c)
	}

	return cards
}

// buildSectionBack constructs the section back using the MinBack fill rule or table/list special cases.
func buildSectionBack(v *vault.Vault, blocks []block, o Options) string {
	if len(blocks) == 0 {
		return ""
	}

	// (a) If the section's first block is a table, back is that table.
	if blocks[0].kind == blockTable {
		return cleanBack(v, blocks[0].text, o.MaxBack)
	}

	firstProseIdx := -1
	for i, b := range blocks {
		if b.kind == blockParagraph {
			firstProseIdx = i
			break
		}
	}

	// (b) If the chosen paragraph ends with ':' or contains 'below'/'following',
	// and the next block is a table or list, back is that paragraph plus the table/list.
	if firstProseIdx >= 0 {
		paraText := strings.TrimSpace(removeCitations(blocks[firstProseIdx].text))
		if strings.HasSuffix(paraText, ":") || reBelowOrFollowing.MatchString(paraText) {
			if firstProseIdx+1 < len(blocks) {
				next := blocks[firstProseIdx+1]
				if next.kind == blockTable || next.kind == blockList {
					return cleanBack(v, blocks[firstProseIdx].text+"\n\n"+next.text, o.MaxBack)
				}
			}
		}
	}

	if firstProseIdx >= 0 {
		acc := blocks[firstProseIdx].text
		cleaned := cleanBack(v, acc, o.MaxBack)
		if utf8.RuneCountInString(cleaned) >= o.MinBack {
			return cleaned
		}
		for j := firstProseIdx + 1; j < len(blocks); j++ {
			if blocks[j].kind == blockParagraph {
				acc += "\n\n" + blocks[j].text
			} else {
				acc += "\n" + blocks[j].text
			}
			cleaned = cleanBack(v, acc, o.MaxBack)
			if utf8.RuneCountInString(cleaned) >= o.MinBack {
				break
			}
		}
		return cleaned
	}

	acc := ""
	for j := 0; j < len(blocks); j++ {
		if acc == "" {
			acc = blocks[j].text
		} else if blocks[j].kind == blockParagraph {
			acc += "\n\n" + blocks[j].text
		} else {
			acc += "\n" + blocks[j].text
		}
		cleaned := cleanBack(v, acc, o.MaxBack)
		if utf8.RuneCountInString(cleaned) >= o.MinBack {
			return cleaned
		}
	}
	return cleanBack(v, acc, o.MaxBack)
}

// isSkippedSection checks if heading is in the skipped sections list.
func isSkippedSection(heading string) bool {
	norm := strings.ToLower(strings.TrimSpace(heading))
	return skippedHeadings[norm]
}

// computeID computes the stable card ID: "lw-" + first 12 hex chars of sha256(Path + "\x00" + Section).
func computeID(path, section string) string {
	h := sha256.New()
	h.Write([]byte(path))
	h.Write([]byte{0})
	h.Write([]byte(section))
	sum := hex.EncodeToString(h.Sum(nil))
	return "lw-" + sum[:12]
}

// normalizeTag trims, lowercases, and replaces spaces with hyphens.
func normalizeTag(t string) string {
	norm := strings.ToLower(strings.TrimSpace(t))
	return strings.ReplaceAll(norm, " ", "-")
}

// buildTags builds the card tags slice: "lw" + page type + page tags, normalized and deduplicated.
func buildTags(pt vault.PageType, pageTags []string) []string {
	var raw []string
	raw = append(raw, "lw")
	if pt != "" {
		raw = append(raw, string(pt))
	}
	raw = append(raw, pageTags...)

	seen := make(map[string]bool)
	var out []string
	for _, t := range raw {
		norm := normalizeTag(t)
		if norm == "" {
			continue
		}
		if !seen[norm] {
			seen[norm] = true
			out = append(out, norm)
		}
	}
	return out
}

type blockKind int

const (
	blockParagraph blockKind = iota
	blockList
	blockTable
)

type block struct {
	kind blockKind
	text string
}

// parseTableRow parses a table row line starting with '|'.
// If the row is a separator row (e.g. |---|---|), isSep is true.
// Otherwise, row is formatted as "cell | cell | cell".
func parseTableRow(line string) (row string, isSep bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "|") {
		return "", false
	}
	content := strings.TrimPrefix(trimmed, "|")
	content = strings.TrimSuffix(content, "|")
	parts := strings.Split(content, "|")
	var cells []string
	allSep := len(parts) > 0
	for _, p := range parts {
		c := strings.TrimSpace(p)
		cells = append(cells, c)
		if len(c) == 0 || strings.Trim(c, "-: ") != "" || !strings.Contains(c, "-") {
			allSep = false
		}
	}
	if allSep {
		return "", true
	}
	return strings.Join(cells, " | "), false
}

// extractBlocks extracts prose paragraphs, grouped lists, and tables,
// skipping fences, HTML comments, blank lines, headings, and block quotes.
func extractBlocks(markdown string) []block {
	lines := strings.Split(markdown, "\n")
	var blocks []block
	var curParagraph []string
	var curList []string
	var curTable []string
	inFence := false
	var fenceMarker string
	inComment := false

	flushParagraph := func() {
		if len(curParagraph) > 0 {
			joined := strings.Join(curParagraph, " ")
			blocks = append(blocks, block{kind: blockParagraph, text: joined})
			curParagraph = nil
		}
	}
	flushList := func() {
		if len(curList) > 0 {
			joined := strings.Join(curList, "\n")
			blocks = append(blocks, block{kind: blockList, text: joined})
			curList = nil
		}
	}
	flushTable := func() {
		if len(curTable) > 0 {
			joined := strings.Join(curTable, "\n")
			blocks = append(blocks, block{kind: blockTable, text: joined})
			curTable = nil
		}
	}
	flushAll := func() {
		flushParagraph()
		flushList()
		flushTable()
	}

	for _, rawLine := range lines {
		trimmed := strings.TrimSpace(rawLine)

		if inFence {
			if strings.HasPrefix(trimmed, fenceMarker) && len(trimmed) >= len(fenceMarker) {
				inFence = false
			}
			continue
		}

		if inComment {
			if strings.Contains(trimmed, "-->") {
				inComment = false
			}
			continue
		}

		if strings.HasPrefix(trimmed, "```") {
			flushAll()
			inFence = true
			fenceMarker = "```"
			continue
		}
		if strings.HasPrefix(trimmed, "~~~") {
			flushAll()
			inFence = true
			fenceMarker = "~~~"
			continue
		}

		if strings.HasPrefix(trimmed, "<!--") {
			flushAll()
			if !strings.Contains(trimmed, "-->") {
				inComment = true
			}
			continue
		}

		if trimmed == "" {
			flushAll()
			continue
		}

		if strings.HasPrefix(trimmed, "#") {
			flushAll()
			continue
		}

		if strings.HasPrefix(trimmed, ">") {
			flushAll()
			continue
		}

		if strings.HasPrefix(trimmed, "|") {
			flushParagraph()
			flushList()
			row, isSep := parseTableRow(trimmed)
			if !isSep && row != "" {
				curTable = append(curTable, row)
			}
			continue
		}

		if isUnorderedListItem(trimmed) {
			flushParagraph()
			flushTable()
			itemText := strings.TrimSpace(trimmed[2:])
			curList = append(curList, "- "+itemText)
			continue
		}

		if m := reOrderedItem.FindStringSubmatch(trimmed); len(m) > 1 {
			flushParagraph()
			flushTable()
			itemText := strings.TrimSpace(m[1])
			curList = append(curList, "- "+itemText)
			continue
		}

		if len(curList) > 0 && len(curParagraph) == 0 && len(curTable) == 0 && (strings.HasPrefix(rawLine, "  ") || strings.HasPrefix(rawLine, "\t")) {
			curList[len(curList)-1] += " " + trimmed
			continue
		}

		flushList()
		flushTable()
		curParagraph = append(curParagraph, trimmed)
	}

	flushAll()
	return blocks
}

func isUnorderedListItem(s string) bool {
	return strings.HasPrefix(s, "- ") || strings.HasPrefix(s, "* ") || strings.HasPrefix(s, "+ ")
}

// cleanBack applies the cleaning pipeline: remove citations, replace wikilinks,
// collapse runs of spaces, and cut at maxBack runes on a word boundary.
func cleanBack(v *vault.Vault, text string, maxBack int) string {
	text = removeCitations(text)
	text = replaceWikilinks(v, text)
	text = collapseSpaces(text)
	text = truncateRunes(text, maxBack)
	return text
}

// removeCitations cuts out ^[...] citations using cite.Scan.
func removeCitations(text string) string {
	cites := cite.Scan(text)
	for i := len(cites) - 1; i >= 0; i-- {
		c := cites[i]
		if c.Offset >= 0 && c.Offset+len(c.Raw) <= len(text) {
			text = text[:c.Offset] + text[c.Offset+len(c.Raw):]
		}
	}
	return text
}

// replaceWikilinks replaces [[target|label]] with label and [[target]] with resolved title else target.
func replaceWikilinks(v *vault.Vault, text string) string {
	links := vault.ParseWikilinks(text)
	for i := len(links) - 1; i >= 0; i-- {
		l := links[i]
		if l.Start < 0 || l.End > len(text) || l.Start > l.End {
			continue
		}
		var rep string
		if l.Alias != "" {
			rep = l.Alias
		} else {
			if resolved, ok := vault.Resolve(v, l.Target); ok {
				if targetPage, ok := v.Page(resolved); ok && targetPage.FM.Title != "" {
					rep = targetPage.FM.Title
				} else {
					rep = l.Target
				}
			} else {
				rep = l.Target
			}
		}
		text = text[:l.Start] + rep + text[l.End:]
	}
	return text
}

// collapseSpaces collapses runs of spaces and horizontal tabs per line, and multiple blank lines.
func collapseSpaces(text string) string {
	var outLines []string
	for _, line := range strings.Split(text, "\n") {
		line = reHorizontalSpaces.ReplaceAllString(line, " ")
		line = strings.Trim(line, " \t")
		outLines = append(outLines, line)
	}
	res := strings.Join(outLines, "\n")
	res = reMultipleNewlines.ReplaceAllString(res, "\n\n")
	return strings.Trim(res, "\n ")
}

// truncateRunes cuts text at maxBack runes on a word boundary with " \u2026".
func truncateRunes(text string, maxBack int) string {
	if maxBack <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= maxBack {
		return text
	}

	cut := maxBack
	for i := maxBack - 1; i >= 0; i-- {
		if unicode.IsSpace(runes[i]) {
			cut = i
			break
		}
	}
	prefix := strings.TrimRight(string(runes[:cut]), " \t\n\r")
	return prefix + " \u2026"
}
