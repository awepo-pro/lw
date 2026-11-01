// Package digest aggregates committed changes in a vault over a time window
// into a structured digest with markdown and HTML renderers.
package digest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/awepo-pro/lw/internal/cite"
	"github.com/awepo-pro/lw/internal/vault"
)

// Options controls windowing for a digest.
type Options struct {
	Since, Until time.Time      // window [Since, Until)
	Loc          *time.Location // location for day arithmetic and formatting (default: time.Local)
}

// Digest aggregates committed changes in a window.
type Digest struct {
	Since, Until time.Time
	Loc          *time.Location
	Commits      []Commit
	New          []PageInfo
	Updated      []PageInfo
	Deleted      []string
	Raws         []RawInfo
	Reverts      int
	Rejected     int
	ByTag        []TagCount
	PendingNotes []string
}

// Commit describes one counted commit.
type Commit struct {
	ID        string
	Changeset string
	Message   string
	Actor     string // "agent:<model>" or "human"
	TS        time.Time
	Paths     int
}

// PageInfo summarizes one new or updated page.
type PageInfo struct {
	Path    string
	Title   string
	Type    string
	Tags    []string
	Gist    string
	Commits int // how many commits in window touched it
}

// RawInfo summarizes one new raw source.
type RawInfo struct {
	Path      string
	Title     string
	SourceURL string
}

// TagCount counts new and updated pages carrying a tag.
type TagCount struct {
	Tag     string
	New     int
	Updated int
}

type journalActor struct {
	Kind  string `json:"kind"`
	Model string `json:"model"`
}

type journalEvent struct {
	TS        time.Time    `json:"ts"`
	Kind      string       `json:"kind"`
	Changeset string       `json:"changeset"`
	Op        string       `json:"op"`
	Commit    string       `json:"commit"`
	Actor     journalActor `json:"actor"`
	Paths     []string     `json:"paths"`
	Message   string       `json:"message"`
}

type commitRecord struct {
	begin    journalEvent
	hasBegin bool
	hasEnd   bool
	commitID string
}

// Build compiles a Digest for the window specified in o from the committed
// vault and journal.ndjson. Missing journal returns an empty digest without error.
func Build(v *vault.Vault, journalPath string, o Options) (*Digest, error) {
	loc := o.Loc
	if loc == nil {
		loc = time.Local
	}
	d := &Digest{
		Since: o.Since,
		Until: o.Until,
		Loc:   loc,
	}

	f, err := os.Open(journalPath)
	if err != nil {
		if os.IsNotExist(err) {
			return d, nil
		}
		return nil, fmt.Errorf("open journal: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	commitMap := make(map[string]*commitRecord)
	var changesetOrder []string

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev journalEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			// Skip malformed lines per spec
			continue
		}

		switch ev.Kind {
		case "commit_begin":
			rec, ok := commitMap[ev.Changeset]
			if !ok {
				rec = &commitRecord{}
				commitMap[ev.Changeset] = rec
				changesetOrder = append(changesetOrder, ev.Changeset)
			}
			rec.begin = ev
			rec.hasBegin = true
			if ev.Commit != "" {
				rec.commitID = ev.Commit
			}
		case "commit_end":
			rec, ok := commitMap[ev.Changeset]
			if !ok {
				rec = &commitRecord{}
				commitMap[ev.Changeset] = rec
				changesetOrder = append(changesetOrder, ev.Changeset)
			}
			rec.hasEnd = true
			if ev.Commit != "" {
				rec.commitID = ev.Commit
			}
		case "changeset_rejected":
			if inWindow(ev.TS, o.Since, o.Until) {
				d.Rejected++
			}
		case "reverted":
			if inWindow(ev.TS, o.Since, o.Until) {
				d.Reverts++
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read journal: %w", err)
	}

	pathCommitCounts := make(map[string]int)
	touchedPathsMap := make(map[string]bool)

	for _, cs := range changesetOrder {
		rec := commitMap[cs]
		if rec == nil || !rec.hasBegin || !rec.hasEnd {
			continue
		}
		ts := rec.begin.TS
		if !inWindow(ts, o.Since, o.Until) {
			continue
		}

		uniquePaths := dedupeStrings(rec.begin.Paths)
		actorStr := formatActor(rec.begin.Actor)
		commitID := rec.commitID
		if commitID == "" {
			commitID = rec.begin.Commit
		}

		d.Commits = append(d.Commits, Commit{
			ID:        commitID,
			Changeset: cs,
			Message:   rec.begin.Message,
			Actor:     actorStr,
			TS:        rec.begin.TS,
			Paths:     len(uniquePaths),
		})

		for _, p := range uniquePaths {
			pathCommitCounts[p]++
			touchedPathsMap[p] = true
		}
	}

	sinceDay := time.Date(o.Since.In(loc).Year(), o.Since.In(loc).Month(), o.Since.In(loc).Day(), 0, 0, 0, 0, loc)
	untilDay := time.Date(o.Until.In(loc).Year(), o.Until.In(loc).Month(), o.Until.In(loc).Day(), 0, 0, 0, 0, loc)

	var touchedPaths []string
	for p := range touchedPathsMap {
		touchedPaths = append(touchedPaths, p)
	}
	sort.Strings(touchedPaths)

	for _, p := range touchedPaths {
		if strings.HasPrefix(p, "wiki/") {
			page, ok := v.Page(p)
			if !ok {
				d.Deleted = append(d.Deleted, p)
				continue
			}
			createdDate := page.FM.Created.Time
			var createdDay time.Time
			if !createdDate.IsZero() {
				createdDay = time.Date(createdDate.Year(), createdDate.Month(), createdDate.Day(), 0, 0, 0, 0, loc)
			}
			isNew := !createdDay.IsZero() && !createdDay.Before(sinceDay) && createdDay.Before(untilDay)

			info := PageInfo{
				Path:    page.Path,
				Title:   page.FM.Title,
				Type:    string(page.FM.Type),
				Tags:    copyTags(page.FM.Tags),
				Gist:    ExtractGist(page.Body),
				Commits: pathCommitCounts[p],
			}
			if isNew {
				d.New = append(d.New, info)
			} else {
				d.Updated = append(d.Updated, info)
			}
		} else if strings.HasPrefix(p, "raw/") {
			if strings.HasSuffix(p, ".md") && !strings.HasPrefix(p, "raw/assets/") {
				if r, ok := v.RawSource(p); ok {
					d.Raws = append(d.Raws, RawInfo{
						Path:      r.Path,
						Title:     r.Title,
						SourceURL: r.SourceURL,
					})
				}
			}
		}
	}

	// Sort commits by TS, then ID
	sort.Slice(d.Commits, func(i, j int) bool {
		if !d.Commits[i].TS.Equal(d.Commits[j].TS) {
			return d.Commits[i].TS.Before(d.Commits[j].TS)
		}
		return d.Commits[i].ID < d.Commits[j].ID
	})

	// Sort New and Updated by Type order (entity, concept, comparison, query, summary, other) then Title
	sortPages(d.New)
	sortPages(d.Updated)

	// Sort Raws by Path
	sort.Slice(d.Raws, func(i, j int) bool {
		return d.Raws[i].Path < d.Raws[j].Path
	})

	// Sort Deleted by Path
	sort.Strings(d.Deleted)

	// ByTag by (New+Updated) desc then tag, top 10
	d.ByTag = computeTopTags(d.New, d.Updated)

	return d, nil
}

func copyTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	out := make([]string, len(tags))
	copy(out, tags)
	sort.Strings(out)
	return out
}

func inWindow(t, since, until time.Time) bool {
	utc := t.UTC()
	return !utc.Before(since.UTC()) && utc.Before(until.UTC())
}

func formatActor(a journalActor) string {
	if a.Kind == "agent" {
		if a.Model != "" {
			return "agent:" + a.Model
		}
		return "agent"
	}
	if a.Kind != "" {
		return a.Kind
	}
	return "human"
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func typeOrder(t string) int {
	switch strings.ToLower(t) {
	case "entity":
		return 0
	case "concept":
		return 1
	case "comparison":
		return 2
	case "query":
		return 3
	case "summary":
		return 4
	default:
		return 5
	}
}

func sortPages(pages []PageInfo) {
	sort.Slice(pages, func(i, j int) bool {
		oa, ob := typeOrder(pages[i].Type), typeOrder(pages[j].Type)
		if oa != ob {
			return oa < ob
		}
		if pages[i].Title != pages[j].Title {
			return pages[i].Title < pages[j].Title
		}
		return pages[i].Path < pages[j].Path
	})
}

func computeTopTags(newPages, updatedPages []PageInfo) []TagCount {
	type counts struct {
		newCount     int
		updatedCount int
	}
	tagMap := make(map[string]*counts)

	for _, p := range newPages {
		seen := make(map[string]bool, len(p.Tags))
		for _, t := range p.Tags {
			if !seen[t] {
				seen[t] = true
				if tagMap[t] == nil {
					tagMap[t] = &counts{}
				}
				tagMap[t].newCount++
			}
		}
	}

	for _, p := range updatedPages {
		seen := make(map[string]bool, len(p.Tags))
		for _, t := range p.Tags {
			if !seen[t] {
				seen[t] = true
				if tagMap[t] == nil {
					tagMap[t] = &counts{}
				}
				tagMap[t].updatedCount++
			}
		}
	}

	var list []TagCount
	for tag, c := range tagMap {
		list = append(list, TagCount{
			Tag:     tag,
			New:     c.newCount,
			Updated: c.updatedCount,
		})
	}

	sort.Slice(list, func(i, j int) bool {
		totI := list[i].New + list[i].Updated
		totJ := list[j].New + list[j].Updated
		if totI != totJ {
			return totI > totJ
		}
		return list[i].Tag < list[j].Tag
	})

	if len(list) > 10 {
		list = list[:10]
	}
	return list
}

var reWikilink = regexp.MustCompile(`\[\[([^\]|]+)(?:\|([^\]]+))?\]\]`)

var reMDLink = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)

func isThematicBreak(trimmed string) bool {
	if trimmed == "---" || trimmed == "***" || trimmed == "___" {
		return true
	}
	if len(trimmed) >= 3 && (trimmed[0] == '-' || trimmed[0] == '*' || trimmed[0] == '_') {
		ch := trimmed[0]
		for i := 0; i < len(trimmed); i++ {
			if trimmed[i] != ch && trimmed[i] != ' ' {
				return false
			}
		}
		return true
	}
	return false
}

func isLinkBar(line string) bool {
	stripped := reWikilink.ReplaceAllString(line, "")
	stripped = reMDLink.ReplaceAllString(stripped, "")
	for _, r := range stripped {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// ParseWindow parses sinceStr (e.g. "7d" or "2006-01-02") and untilStr (e.g. "2006-01-02" or "")
// relative to now in location loc. If loc is nil, time.Local is used.
func ParseWindow(now time.Time, sinceStr, untilStr string, loc *time.Location) (since, until time.Time, err error) {
	if loc == nil {
		loc = time.Local
	}
	nowLoc := now.In(loc)
	today := time.Date(nowLoc.Year(), nowLoc.Month(), nowLoc.Day(), 0, 0, 0, 0, loc)
	defaultUntil := today.AddDate(0, 0, 1)

	if strings.HasSuffix(sinceStr, "d") {
		numStr := strings.TrimSuffix(sinceStr, "d")
		n, err := strconv.Atoi(numStr)
		if err != nil || n < 1 || n > 3650 {
			return time.Time{}, time.Time{}, fmt.Errorf("--since: invalid duration %q (must be 1..3650 days)", sinceStr)
		}
		since = today.AddDate(0, 0, -n)
	} else {
		t, err := time.ParseInLocation("2006-01-02", sinceStr, loc)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("--since: %q is not Nd or YYYY-MM-DD", sinceStr)
		}
		since = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	}

	if untilStr == "" {
		until = defaultUntil
	} else {
		t, err := time.ParseInLocation("2006-01-02", untilStr, loc)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("--until: %q is not YYYY-MM-DD", untilStr)
		}
		until = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	}

	if !since.Before(until) {
		return time.Time{}, time.Time{}, fmt.Errorf("--since (%s) must be before --until (%s)",
			since.Format("2006-01-02"), until.Format("2006-01-02"))
	}
	return since, until, nil
}

// ExtractGist extracts the first prose paragraph of the page body (skipping H1,
// headings, blank lines, lists, tables, code fences, block quotes), with
// citation markers removed, wikilinks replaced by label/target, whitespace
// collapsed, cut at 220 runes on a word boundary with "\u2026".
func ExtractGist(body string) string {
	inFence := false
	var fenceChar byte
	var fenceLen int
	inList := false
	var paraLines []string
	collecting := false

	lines := strings.Split(body, "\n")
	for _, line := range lines {
		if inFence {
			if isFenceClose(line, fenceChar, fenceLen) {
				inFence = false
			}
			continue
		}
		if ch, n, ok := isFenceOpen(line); ok {
			if collecting {
				break
			}
			inFence = true
			fenceChar = ch
			fenceLen = n
			continue
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if collecting {
				break
			}
			inList = false
			continue
		}

		// Skip thematic breaks (e.g. ---, ***)
		if isThematicBreak(trimmed) {
			if collecting {
				break
			}
			inList = false
			continue
		}

		// Skip navigation/link bars (e.g. [[a]] | [[b]])
		if isLinkBar(trimmed) {
			if collecting {
				break
			}
			inList = false
			continue
		}

		// Skip headings
		if strings.HasPrefix(trimmed, "#") {
			if collecting {
				break
			}
			inList = false
			continue
		}

		// Skip block quotes
		if strings.HasPrefix(trimmed, ">") {
			if collecting {
				break
			}
			inList = false
			continue
		}

		// Skip tables
		if strings.HasPrefix(trimmed, "|") {
			if collecting {
				break
			}
			inList = false
			continue
		}

		// Skip list items
		if isListItem(trimmed) {
			if collecting {
				break
			}
			inList = true
			continue
		}

		if inList {
			if strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t") {
				continue
			}
			inList = false
		}

		collecting = true
		paraLines = append(paraLines, line)
	}

	if len(paraLines) == 0 {
		return ""
	}

	para := strings.Join(paraLines, "\n")

	// Strip citations using cite.Scan offsets and Raw
	cites := cite.Scan(para)
	for i := len(cites) - 1; i >= 0; i-- {
		c := cites[i]
		if c.Offset >= 0 && c.Offset+len(c.Raw) <= len(para) {
			para = para[:c.Offset] + para[c.Offset+len(c.Raw):]
		}
	}

	// Replace wikilinks: [[target|label]] -> label, [[target]] -> target
	para = reWikilink.ReplaceAllStringFunc(para, func(m string) string {
		sub := reWikilink.FindStringSubmatch(m)
		if len(sub) > 2 && sub[2] != "" {
			return sub[2]
		}
		if len(sub) > 1 {
			return sub[1]
		}
		return m
	})

	// Collapse whitespace
	words := strings.Fields(para)
	para = strings.Join(words, " ")

	// Cut at 220 runes on a word boundary with "\u2026"
	return cutGist(para, 220)
}

func isFenceOpen(line string) (byte, int, bool) {
	i := 0
	for i < len(line) && line[i] == ' ' && i < 3 {
		i++
	}
	rest := line[i:]
	if len(rest) < 3 {
		return 0, 0, false
	}
	ch := rest[0]
	if ch != '`' && ch != '~' {
		return 0, 0, false
	}
	cnt := 0
	for cnt < len(rest) && rest[cnt] == ch {
		cnt++
	}
	if cnt >= 3 {
		return ch, cnt, true
	}
	return 0, 0, false
}

func isFenceClose(line string, ch byte, minLen int) bool {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) < minLen {
		return false
	}
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] != ch {
			return false
		}
	}
	return true
}

func isListItem(trimmed string) bool {
	if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "+ ") {
		return true
	}
	i := 0
	for i < len(trimmed) && trimmed[i] >= '0' && trimmed[i] <= '9' {
		i++
	}
	if i > 0 && i < len(trimmed) {
		if (trimmed[i] == '.' || trimmed[i] == ')') && i+1 < len(trimmed) && trimmed[i+1] == ' ' {
			return true
		}
	}
	return false
}

func cutGist(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	prefix := string(runes[:maxRunes])
	if runes[maxRunes] == ' ' {
		prefix = strings.TrimRight(prefix, " ")
	} else {
		idx := strings.LastIndex(prefix, " ")
		if idx > 0 {
			prefix = prefix[:idx]
		}
		prefix = strings.TrimRight(prefix, " ")
	}
	return prefix + "\u2026"
}

func cutCommitMessage(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes])
}

func typePlural(t string) string {
	switch strings.ToLower(t) {
	case "entity":
		return "Entities"
	case "concept":
		return "Concepts"
	case "comparison":
		return "Comparisons"
	case "query":
		return "Queries"
	case "summary":
		return "Summaries"
	default:
		if t == "" {
			return "Other"
		}
		if strings.HasSuffix(t, "y") && !strings.HasSuffix(t, "ey") && !strings.HasSuffix(t, "ay") && !strings.HasSuffix(t, "oy") {
			return strings.ToUpper(t[:1]) + t[1:len(t)-1] + "ies"
		}
		return strings.ToUpper(t[:1]) + t[1:] + "s"
	}
}

// WriteMarkdown writes the formatted markdown digest to w.
func WriteMarkdown(w io.Writer, d *Digest) error {
	loc := d.Loc
	if loc == nil {
		loc = time.Local
	}
	sinceStr := d.Since.In(loc).Format("2006-01-02")
	inclusiveEnd := d.Until.In(loc).AddDate(0, 0, -1).Format("2006-01-02")

	fmt.Fprintf(w, "# Wiki digest %s .. %s\n", sinceStr, inclusiveEnd)

	if len(d.Commits) == 0 {
		fmt.Fprintf(w, "\nnothing was committed in this window\n")
		if len(d.PendingNotes) > 0 {
			fmt.Fprintf(w, "\n## Notes not yet ingested\n")
			for _, stem := range d.PendingNotes {
				fmt.Fprintf(w, "- %s\n", stem)
			}
		}
		return nil
	}

	summary := fmt.Sprintf("%d commits - %d new pages - %d updated - %d new sources",
		len(d.Commits), len(d.New), len(d.Updated), len(d.Raws))
	if d.Reverts > 0 {
		summary += fmt.Sprintf(" - %d reverted", d.Reverts)
	}
	if d.Rejected > 0 {
		summary += fmt.Sprintf(" - %d rejected", d.Rejected)
	}
	fmt.Fprintf(w, "%s\n", summary)

	if len(d.New) > 0 {
		fmt.Fprintf(w, "\n## New pages\n")
		typeMap := make(map[string][]PageInfo)
		var typesInOrder []string
		for _, p := range d.New {
			t := p.Type
			if len(typeMap[t]) == 0 {
				typesInOrder = append(typesInOrder, t)
			}
			typeMap[t] = append(typeMap[t], p)
		}

		for _, t := range typesInOrder {
			fmt.Fprintf(w, "\n### %s\n", typePlural(t))
			for _, p := range typeMap[t] {
				line := fmt.Sprintf("- **%s** (`%s`)", p.Title, p.Path)
				if p.Gist != "" {
					line += " - " + p.Gist
				}
				if len(p.Tags) > 0 {
					line += " *" + strings.Join(p.Tags, ", ") + "*"
				}
				fmt.Fprintf(w, "%s\n", line)
			}
		}
	}

	if len(d.Updated) > 0 {
		fmt.Fprintf(w, "\n## Updated pages\n")
		for _, p := range d.Updated {
			line := fmt.Sprintf("- **%s** (`%s`)", p.Title, p.Path)
			if p.Commits > 1 {
				line += fmt.Sprintf(" x%d", p.Commits)
			}
			fmt.Fprintf(w, "%s\n", line)
		}
	}

	if len(d.Raws) > 0 {
		fmt.Fprintf(w, "\n## New sources\n")
		for _, r := range d.Raws {
			title := strings.TrimSpace(r.Title)
			if title == "" || title == r.Path {
				if strings.HasPrefix(r.SourceURL, "http://") || strings.HasPrefix(r.SourceURL, "https://") {
					fmt.Fprintf(w, "- `%s` - %s\n", r.Path, r.SourceURL)
				} else {
					fmt.Fprintf(w, "- `%s`\n", r.Path)
				}
			} else {
				if strings.HasPrefix(r.SourceURL, "http://") || strings.HasPrefix(r.SourceURL, "https://") {
					fmt.Fprintf(w, "- %s - %s (`%s`)\n", title, r.SourceURL, r.Path)
				} else {
					fmt.Fprintf(w, "- %s (`%s`)\n", title, r.Path)
				}
			}
		}
	}

	if len(d.ByTag) > 0 {
		fmt.Fprintf(w, "\n## Top tags\n")
		for _, tc := range d.ByTag {
			fmt.Fprintf(w, "- %s: %d new, %d updated\n", tc.Tag, tc.New, tc.Updated)
		}
	}

	if len(d.Commits) > 0 {
		fmt.Fprintf(w, "\n## Commits\n")
		for _, c := range d.Commits {
			msg := cutCommitMessage(c.Message, 100)
			dateStr := c.TS.In(loc).Format("2006-01-02")
			fmt.Fprintf(w, "- %s %s %s (%s)\n", dateStr, c.ID, msg, c.Actor)
		}
	}

	if len(d.PendingNotes) > 0 {
		fmt.Fprintf(w, "\n## Notes not yet ingested\n")
		for _, stem := range d.PendingNotes {
			fmt.Fprintf(w, "- %s\n", stem)
		}
	}

	return nil
}

type htmlPageData struct {
	HeaderDates    string
	Empty          bool
	Summary        string
	NewPagesByType []htmlTypeGroup
	Updated        []PageInfo
	Raws           []htmlRawItem
	ByTag          []TagCount
	Commits        []htmlCommitItem
	PendingNotes   []string
}

type htmlTypeGroup struct {
	TypePlural string
	Pages      []htmlPageItem
}

type htmlPageItem struct {
	Title   string
	Path    string
	Gist    string
	TagsStr string
}

type htmlRawItem struct {
	Title     string
	Path      string
	SourceURL string
	IsHTTP    bool
	HasTitle  bool
}

type htmlCommitItem struct {
	DateStr string
	ID      string
	Message string
	Actor   string
}

var htmlTmpl = template.Must(template.New("digest").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Wiki digest {{.HeaderDates}}</title>
<style>
:root {
  --bg: #ffffff;
  --fg: #1f2328;
  --card-bg: #f6f8fa;
  --border: #d0d7de;
  --link: #0969da;
  --muted: #656d76;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #0d1117;
    --fg: #e6edf3;
    --card-bg: #161b22;
    --border: #30363d;
    --link: #2f81f7;
    --muted: #8b949e;
  }
}
* { box-sizing: border-box; }
body {
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif;
  line-height: 1.5;
  color: var(--fg);
  background-color: var(--bg);
  max-width: 800px;
  margin: 0 auto;
  padding: 16px;
  word-wrap: break-word;
}
h1, h2, h3 { color: var(--fg); font-weight: 600; margin-top: 24px; margin-bottom: 12px; }
h1 { font-size: 1.6em; border-bottom: 1px solid var(--border); padding-bottom: 8px; }
h2 { font-size: 1.3em; border-bottom: 1px solid var(--border); padding-bottom: 6px; }
h3 { font-size: 1.1em; }
p.summary { font-size: 1.05em; color: var(--muted); margin-bottom: 20px; }
ul { padding-left: 20px; margin: 8px 0; }
li { margin-bottom: 6px; }
code { font-family: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace; font-size: 0.9em; background-color: var(--card-bg); padding: 2px 4px; border-radius: 4px; }
a { color: var(--link); text-decoration: none; }
a:hover { text-decoration: underline; }
em.tags { color: var(--muted); font-style: italic; }
</style>
</head>
<body>
<h1>Wiki digest {{.HeaderDates}}</h1>
{{if .Empty}}
<p>nothing was committed in this window</p>
{{else}}
<p class="summary">{{.Summary}}</p>
{{if .NewPagesByType}}
<h2>New pages</h2>
{{range .NewPagesByType}}
<h3>{{.TypePlural}}</h3>
<ul>
{{range .Pages}}
<li><strong>{{.Title}}</strong> (<code>{{.Path}}</code>){{if .Gist}} - {{.Gist}}{{end}}{{if .TagsStr}} <em class="tags">({{.TagsStr}})</em>{{end}}</li>
{{end}}
</ul>
{{end}}
{{end}}
{{if .Updated}}
<h2>Updated pages</h2>
<ul>
{{range .Updated}}
<li><strong>{{.Title}}</strong> (<code>{{.Path}}</code>){{if gt .Commits 1}} x{{.Commits}}{{end}}</li>
{{end}}
</ul>
{{end}}
{{if .Raws}}
<h2>New sources</h2>
<ul>
{{range .Raws}}
<li>{{if .HasTitle}}{{.Title}}{{if .IsHTTP}} - <a href="{{.SourceURL}}">{{.SourceURL}}</a>{{end}} (<code>{{.Path}}</code>){{else}}<code>{{.Path}}</code>{{if .IsHTTP}} - <a href="{{.SourceURL}}">{{.SourceURL}}</a>{{end}}{{end}}</li>
{{end}}
</ul>
{{end}}
{{if .ByTag}}
<h2>Top tags</h2>
<ul>
{{range .ByTag}}
<li>{{.Tag}}: {{.New}} new, {{.Updated}} updated</li>
{{end}}
</ul>
{{end}}
{{if .Commits}}
<h2>Commits</h2>
<ul>
{{range .Commits}}
<li>{{.DateStr}} {{.ID}} {{.Message}} ({{.Actor}})</li>
{{end}}
</ul>
{{end}}
{{end}}
{{if .PendingNotes}}
<h2>Notes not yet ingested</h2>
<ul>
{{range .PendingNotes}}
<li>{{.}}</li>
{{end}}
</ul>
{{end}}
</body>
</html>
`))

// WriteHTML writes a self-contained HTML page containing the digest to w.
func WriteHTML(w io.Writer, d *Digest) error {
	loc := d.Loc
	if loc == nil {
		loc = time.Local
	}
	sinceStr := d.Since.In(loc).Format("2006-01-02")
	inclusiveEnd := d.Until.In(loc).AddDate(0, 0, -1).Format("2006-01-02")
	headerDates := sinceStr + " .. " + inclusiveEnd

	data := htmlPageData{
		HeaderDates:  headerDates,
		PendingNotes: d.PendingNotes,
	}

	if len(d.Commits) == 0 {
		data.Empty = true
		return htmlTmpl.Execute(w, data)
	}

	summary := fmt.Sprintf("%d commits - %d new pages - %d updated - %d new sources",
		len(d.Commits), len(d.New), len(d.Updated), len(d.Raws))
	if d.Reverts > 0 {
		summary += fmt.Sprintf(" - %d reverted", d.Reverts)
	}
	if d.Rejected > 0 {
		summary += fmt.Sprintf(" - %d rejected", d.Rejected)
	}
	data.Summary = summary
	data.Updated = d.Updated
	data.ByTag = d.ByTag

	// Group new pages by type
	typeMap := make(map[string][]htmlPageItem)
	var typesInOrder []string
	for _, p := range d.New {
		t := p.Type
		if len(typeMap[t]) == 0 {
			typesInOrder = append(typesInOrder, t)
		}
		item := htmlPageItem{
			Title: p.Title,
			Path:  p.Path,
			Gist:  p.Gist,
		}
		if len(p.Tags) > 0 {
			item.TagsStr = strings.Join(p.Tags, ", ")
		}
		typeMap[t] = append(typeMap[t], item)
	}
	for _, t := range typesInOrder {
		data.NewPagesByType = append(data.NewPagesByType, htmlTypeGroup{
			TypePlural: typePlural(t),
			Pages:      typeMap[t],
		})
	}

	// Raws
	for _, r := range d.Raws {
		title := strings.TrimSpace(r.Title)
		hasTitle := title != "" && title != r.Path
		isHTTP := strings.HasPrefix(r.SourceURL, "http://") || strings.HasPrefix(r.SourceURL, "https://")
		data.Raws = append(data.Raws, htmlRawItem{
			Title:     title,
			Path:      r.Path,
			SourceURL: r.SourceURL,
			IsHTTP:    isHTTP,
			HasTitle:  hasTitle,
		})
	}

	// Commits
	for _, c := range d.Commits {
		msg := cutCommitMessage(c.Message, 100)
		data.Commits = append(data.Commits, htmlCommitItem{
			DateStr: c.TS.In(loc).Format("2006-01-02"),
			ID:      c.ID,
			Message: msg,
			Actor:   c.Actor,
		})
	}

	return htmlTmpl.Execute(w, data)
}
