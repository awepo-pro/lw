// Package vaultstats computes vault health, shape, coverage, and activity metrics.
package vaultstats

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/awepo-pro/lw/internal/cite"
	"github.com/awepo-pro/lw/internal/vault"
)

// TagCount records how many distinct pages carry a given tag.
type TagCount struct {
	Tag   string `json:"tag"`
	Pages int    `json:"pages"`
}

// PageAge records a page whose updated date is older than the stale threshold.
type PageAge struct {
	Path    string `json:"path"`
	Updated string `json:"updated"`
	Days    int    `json:"days"`
}

// PageSize records a page's size in body bytes.
type PageSize struct {
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}

// WeekCount records the number of commits in one ISO week.
type WeekCount struct {
	Week    string `json:"week"`
	Commits int    `json:"commits"`
}

// Stats holds the shape, coverage, and activity metrics of a vault.
type Stats struct {
	Pages                   int            `json:"pages"`
	ParseErrors             int            `json:"parse_errors"`
	ByType                  map[string]int `json:"by_type"`
	Raws                    int            `json:"raws"`
	RawsByKind              map[string]int `json:"raws_by_kind"`
	Assets                  int            `json:"assets"`
	DistinctTags            int            `json:"distinct_tags"`
	TopTags                 []TagCount     `json:"top_tags"`
	Confidence              map[string]int `json:"confidence"`
	Contested               int            `json:"contested"`
	ZeroSource              int            `json:"zero_source"`
	SingleSource            int            `json:"single_source"`
	AvgSources              float64        `json:"avg_sources"`
	InlineCites             int            `json:"inline_cites"`
	MalformedCites          int            `json:"malformed_cites"`
	PagesWithoutInlineCites int            `json:"pages_without_inline_cites"`
	UncitedRaws             []string       `json:"uncited_raws"`
	SupersededRaws          int            `json:"superseded_raws"`
	Edges                   int            `json:"edges"`
	DistinctEdges           int            `json:"distinct_edges"`
	Broken                  int            `json:"broken"`
	Orphans                 int            `json:"orphans"`
	AvgOutLinks             float64        `json:"avg_out_links"`
	Freshness               map[string]int `json:"freshness"`
	Stale                   []PageAge      `json:"stale"`
	Largest                 []PageSize     `json:"largest"`
	Commits                 int            `json:"commits"`
	Rejected                int            `json:"rejected"`
	LastCommit              string         `json:"last_commit"`
	Weekly                  []WeekCount    `json:"weekly"`
}

// normaliseSource trims spaces, strips surrounding [[ ]], drops a leading "./",
// and ensures a ".md" suffix when comparing source references.
func normaliseSource(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "[[") && strings.HasSuffix(s, "]]") {
		s = strings.TrimSpace(s[2 : len(s)-2])
	}
	s = strings.TrimPrefix(s, "./")
	if s != "" && !strings.HasSuffix(s, ".md") {
		s += ".md"
	}
	return s
}

// Compute analyzes v, its journal at journalPath, and computes vault statistics.
func Compute(v *vault.Vault, journalPath string, now time.Time, staleDays int) Stats {
	pages := v.Pages()
	raws := v.RawSources()

	byType := make(map[string]int)
	for _, p := range pages {
		t := string(p.FM.Type)
		byType[t]++
	}

	rawsByKind := make(map[string]int)
	for _, r := range raws {
		slashPath := filepath.ToSlash(r.Path)
		trimmed := strings.TrimPrefix(slashPath, "raw/")
		parts := strings.Split(trimmed, "/")
		kind := ""
		if len(parts) > 1 && parts[0] != "" {
			kind = parts[0]
		}
		rawsByKind[kind]++
	}

	assets := 0
	if root := v.Root(); root != "" {
		ents, err := os.ReadDir(filepath.Join(root, "raw", "assets"))
		if err == nil {
			for _, e := range ents {
				if !e.IsDir() {
					assets++
				}
			}
		}
	}

	tagPages := make(map[string]int)
	for _, p := range pages {
		seenOnPage := make(map[string]bool)
		for _, t := range p.FM.Tags {
			t = strings.TrimSpace(t)
			if t != "" && !seenOnPage[t] {
				seenOnPage[t] = true
				tagPages[t]++
			}
		}
	}
	distinctTags := len(tagPages)
	topTags := make([]TagCount, 0, len(tagPages))
	for t, count := range tagPages {
		topTags = append(topTags, TagCount{Tag: t, Pages: count})
	}
	sort.Slice(topTags, func(i, j int) bool {
		if topTags[i].Pages != topTags[j].Pages {
			return topTags[i].Pages > topTags[j].Pages
		}
		return topTags[i].Tag < topTags[j].Tag
	})
	if len(topTags) > 15 {
		topTags = topTags[:15]
	}

	confidence := make(map[string]int)
	contested := 0
	zeroSource := 0
	singleSource := 0
	totalSources := 0
	inlineCites := 0
	malformedCites := 0
	pagesWithoutInlineCites := 0

	citedSources := make(map[string]bool)

	for _, p := range pages {
		c := string(p.FM.Confidence)
		if c == "" {
			c = "unset"
		}
		confidence[c]++
		if p.FM.Contested {
			contested++
		}

		nSources := len(p.FM.Sources)
		totalSources += nSources
		if nSources == 0 {
			zeroSource++
		} else if nSources == 1 {
			singleSource++
		}

		for _, src := range p.FM.Sources {
			norm := normaliseSource(src)
			if norm != "" {
				citedSources[norm] = true
			}
		}

		markers := cite.Scan(p.Body)
		validOnPage := 0
		for _, m := range markers {
			if m.Err == "" {
				inlineCites++
				validOnPage++
			} else {
				malformedCites++
			}
			norm := normaliseSource(m.Source)
			if norm != "" {
				citedSources[norm] = true
			}
		}
		if validOnPage == 0 {
			pagesWithoutInlineCites++
		}
	}

	var avgSources float64
	if len(pages) > 0 {
		avgSources = float64(totalSources) / float64(len(pages))
	}

	supersededSet := make(map[string]bool)
	for _, r := range raws {
		if r.Supersedes != "" {
			normSup := normaliseSource(r.Supersedes)
			if normSup != normaliseSource(r.Path) {
				supersededSet[normSup] = true
			}
		}
	}

	supersededRaws := 0
	uncitedRaws := make([]string, 0)
	for _, r := range raws {
		normPath := normaliseSource(r.Path)
		if supersededSet[normPath] {
			supersededRaws++
			continue
		}
		if !citedSources[normPath] {
			uncitedRaws = append(uncitedRaws, r.Path)
		}
	}
	sort.Strings(uncitedRaws)

	g := v.Graph()
	edges := 0
	broken := 0
	distinctPairs := make(map[[2]string]bool)
	pageSet := make(map[string]bool, len(pages))
	for _, p := range pages {
		pageSet[p.Path] = true
	}
	if g != nil {
		for _, p := range pages {
			for _, ref := range g.Outbound(p.Path) {
				if ref.To != "" {
					edges++
					if ref.To != p.Path && pageSet[ref.To] {
						distinctPairs[[2]string{p.Path, ref.To}] = true
					}
				} else {
					broken++
				}
			}
		}
	}
	distinctEdges := len(distinctPairs)
	orphans := 0
	if g != nil {
		orphans = len(g.Orphans())
	}
	var avgOutLinks float64
	if len(pages) > 0 {
		avgOutLinks = float64(edges) / float64(len(pages))
	}

	freshness := map[string]int{
		"<7d":     0,
		"7-30d":   0,
		"30-90d":  0,
		">90d":    0,
		"no date": 0,
	}
	stale := make([]PageAge, 0)
	for _, p := range pages {
		if p.FM.Updated.IsZero() {
			freshness["no date"]++
			continue
		}
		days := int(now.Sub(p.FM.Updated.Time).Hours() / 24)
		if days < 0 {
			days = 0
		}
		if days < 7 {
			freshness["<7d"]++
		} else if days <= 30 {
			freshness["7-30d"]++
		} else if days <= 90 {
			freshness["30-90d"]++
		} else {
			freshness[">90d"]++
		}

		if days > staleDays {
			stale = append(stale, PageAge{
				Path:    p.Path,
				Updated: p.FM.Updated.String(),
				Days:    days,
			})
		}
	}
	sort.Slice(stale, func(i, j int) bool {
		if stale[i].Days != stale[j].Days {
			return stale[i].Days > stale[j].Days
		}
		return stale[i].Path < stale[j].Path
	})
	if len(stale) > 10 {
		stale = stale[:10]
	}

	largest := make([]PageSize, 0, len(pages))
	for _, p := range pages {
		largest = append(largest, PageSize{
			Path:  p.Path,
			Bytes: len(p.Body),
		})
	}
	sort.Slice(largest, func(i, j int) bool {
		if largest[i].Bytes != largest[j].Bytes {
			return largest[i].Bytes > largest[j].Bytes
		}
		return largest[i].Path < largest[j].Path
	})
	if len(largest) > 5 {
		largest = largest[:5]
	}

	weekly := make([]WeekCount, 8)
	weekIndex := make(map[string]int)
	for i := 7; i >= 0; i-- {
		t := now.AddDate(0, 0, -7*i)
		y, w := t.ISOWeek()
		weekStr := fmt.Sprintf("%04d-W%02d", y, w)
		idx := 7 - i
		weekly[idx] = WeekCount{Week: weekStr, Commits: 0}
		weekIndex[weekStr] = idx
	}

	commits := 0
	rejected := 0
	var latestCommit time.Time
	hasCommit := false

	if journalPath != "" {
		if f, err := os.Open(journalPath); err == nil {
			defer f.Close()
			scanner := bufio.NewScanner(f)
			buf := make([]byte, 1024*1024)
			scanner.Buffer(buf, 1024*1024)
			for scanner.Scan() {
				line := bytes.TrimSpace(scanner.Bytes())
				if len(line) == 0 {
					continue
				}
				var ev struct {
					TS   time.Time `json:"ts"`
					Kind string    `json:"kind"`
				}
				if err := json.Unmarshal(line, &ev); err != nil {
					continue
				}
				switch ev.Kind {
				case "commit_end":
					commits++
					if !hasCommit || ev.TS.After(latestCommit) {
						latestCommit = ev.TS
						hasCommit = true
					}
					y, w := ev.TS.ISOWeek()
					wStr := fmt.Sprintf("%04d-W%02d", y, w)
					if idx, ok := weekIndex[wStr]; ok {
						weekly[idx].Commits++
					}
				case "changeset_rejected":
					rejected++
				}
			}
		}
	}

	lastCommitStr := ""
	if hasCommit {
		lastCommitStr = latestCommit.Format("2006-01-02")
	}

	return Stats{
		Pages:                   len(pages),
		ParseErrors:             len(v.ParseErrors()),
		ByType:                  byType,
		Raws:                    len(raws),
		RawsByKind:              rawsByKind,
		Assets:                  assets,
		DistinctTags:            distinctTags,
		TopTags:                 topTags,
		Confidence:              confidence,
		Contested:               contested,
		ZeroSource:              zeroSource,
		SingleSource:            singleSource,
		AvgSources:              avgSources,
		InlineCites:             inlineCites,
		MalformedCites:          malformedCites,
		PagesWithoutInlineCites: pagesWithoutInlineCites,
		UncitedRaws:             uncitedRaws,
		SupersededRaws:          supersededRaws,
		Edges:                   edges,
		DistinctEdges:           distinctEdges,
		Broken:                  broken,
		Orphans:                 orphans,
		AvgOutLinks:             avgOutLinks,
		Freshness:               freshness,
		Stale:                   stale,
		Largest:                 largest,
		Commits:                 commits,
		Rejected:                rejected,
		LastCommit:              lastCommitStr,
		Weekly:                  weekly,
	}
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// WriteText renders a human-readable text report of s to w.
func WriteText(w io.Writer, s Stats) error {
	// 1. pages
	fmt.Fprintf(w, "pages\n")
	fmt.Fprintf(w, "  total: %d\n", s.Pages)
	typeKeys := sortedKeys(s.ByType)
	var typeParts []string
	for _, k := range typeKeys {
		disp := k
		if disp == "" {
			disp = "unset"
		}
		typeParts = append(typeParts, fmt.Sprintf("%s: %d", disp, s.ByType[k]))
	}
	if len(typeParts) > 0 {
		fmt.Fprintf(w, "  by type: %s\n", strings.Join(typeParts, ", "))
	}
	if s.ParseErrors > 0 {
		fmt.Fprintf(w, "  parse errors: %d (run lw lint)\n", s.ParseErrors)
	}

	// 2. raw sources
	fmt.Fprintf(w, "\nraw sources\n")
	fmt.Fprintf(w, "  total: %d, assets: %d\n", s.Raws, s.Assets)
	kindKeys := sortedKeys(s.RawsByKind)
	var kindParts []string
	for _, k := range kindKeys {
		disp := k
		if disp == "" {
			disp = "root"
		}
		kindParts = append(kindParts, fmt.Sprintf("%s: %d", disp, s.RawsByKind[k]))
	}
	if len(kindParts) > 0 {
		fmt.Fprintf(w, "  by kind: %s\n", strings.Join(kindParts, ", "))
	}

	// 3. tags
	fmt.Fprintf(w, "\ntags\n")
	fmt.Fprintf(w, "  distinct: %d\n", s.DistinctTags)
	if len(s.TopTags) > 0 {
		fmt.Fprintf(w, "  top tags:\n")
		for _, tc := range s.TopTags {
			fmt.Fprintf(w, "    %-20s %d\n", tc.Tag, tc.Pages)
		}
	}

	// 4. confidence
	fmt.Fprintf(w, "\nconfidence\n")
	confKeys := sortedKeys(s.Confidence)
	var confParts []string
	for _, k := range confKeys {
		confParts = append(confParts, fmt.Sprintf("%s: %d", k, s.Confidence[k]))
	}
	fmt.Fprintf(w, "  levels: %s; contested: %d\n", strings.Join(confParts, ", "), s.Contested)

	// 5. sourcing & citations
	fmt.Fprintf(w, "\nsourcing & citations\n")
	fmt.Fprintf(w, "  zero-source: %d, single-source: %d, avg sources: %.2f\n", s.ZeroSource, s.SingleSource, s.AvgSources)
	fmt.Fprintf(w, "  inline cites: %d, malformed: %d, pages without inline cites: %d\n", s.InlineCites, s.MalformedCites, s.PagesWithoutInlineCites)

	// 6. coverage
	fmt.Fprintf(w, "\ncoverage\n")
	fmt.Fprintf(w, "  uncited raws: %d, superseded: %d\n", len(s.UncitedRaws), s.SupersededRaws)
	if len(s.UncitedRaws) > 0 {
		limit := 10
		if len(s.UncitedRaws) < limit {
			limit = len(s.UncitedRaws)
		}
		for i := 0; i < limit; i++ {
			p := s.UncitedRaws[i]
			if len(p) > 90 {
				p = p[:87] + "\u2026"
			}
			fmt.Fprintf(w, "    %s\n", p)
		}
		if len(s.UncitedRaws) > 10 {
			fmt.Fprintf(w, "    ... and %d more\n", len(s.UncitedRaws)-10)
		}
	}

	// 7. links
	fmt.Fprintf(w, "\nlinks\n")
	fmt.Fprintf(w, "  links: %d refs (%d distinct page pairs), broken: %d, orphans: %d, avg out-links: %.2f\n",
		s.Edges, s.DistinctEdges, s.Broken, s.Orphans, s.AvgOutLinks)

	// 8. freshness
	fmt.Fprintf(w, "\nfreshness\n")
	fmt.Fprintf(w, "  <7d: %d, 7-30d: %d, 30-90d: %d, >90d: %d, no date: %d\n",
		s.Freshness["<7d"], s.Freshness["7-30d"], s.Freshness["30-90d"], s.Freshness[">90d"], s.Freshness["no date"])
	if len(s.Stale) > 0 {
		fmt.Fprintf(w, "  stale pages:\n")
		for _, pa := range s.Stale {
			p := pa.Path
			if len(p) > 60 {
				p = p[:57] + "\u2026"
			}
			fmt.Fprintf(w, "    %-60s %s (%dd)\n", p, pa.Updated, pa.Days)
		}
	}

	// 9. largest pages
	fmt.Fprintf(w, "\nlargest pages\n")
	for _, ps := range s.Largest {
		p := ps.Path
		if len(p) > 70 {
			p = p[:67] + "\u2026"
		}
		fmt.Fprintf(w, "  %-70s %d bytes\n", p, ps.Bytes)
	}

	// 10. activity
	fmt.Fprintf(w, "\nactivity\n")
	fmt.Fprintf(w, "  commits: %d, rejected: %d, last commit: %s\n", s.Commits, s.Rejected, s.LastCommit)
	maxCommits := 0
	for _, wc := range s.Weekly {
		if wc.Commits > maxCommits {
			maxCommits = wc.Commits
		}
	}
	spark := make([]rune, 8)
	for i, wc := range s.Weekly {
		if maxCommits == 0 || wc.Commits == 0 {
			spark[i] = '\u2581'
		} else {
			lvl := (wc.Commits * 7) / maxCommits
			if lvl > 7 {
				lvl = 7
			}
			spark[i] = rune(0x2581 + lvl)
		}
	}
	numStrs := make([]string, 8)
	for i, wc := range s.Weekly {
		numStrs[i] = strconv.Itoa(wc.Commits)
	}
	fmt.Fprintf(w, "  weekly: %s  (%s)\n", string(spark), strings.Join(numStrs, ", "))

	return nil
}
