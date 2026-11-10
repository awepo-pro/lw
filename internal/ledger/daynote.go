package ledger

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// DayNote is one YYYY-MM-DD.md read by ParseDayNote.
type DayNote struct {
	Date    string   // from the file name
	Entries []Entry  // KindDay, Date/Month set, Path/Line of the note; empty when Issues is not empty
	Issues  []Issue  // why lw cannot read it with certainty
	Images  []string // embed targets as written, in order, no duplicates
}

var (
	wikiEmbedRegex = regexp.MustCompile(`!\[\[([^\[\]\|]+)(?:\|[^\[\]]*)?\]\]`)
	mdEmbedRegex   = regexp.MustCompile(`!\[[^\]]*\]\(([^)]+)\)`)
)

type embedMatch struct {
	start  int
	end    int
	target string
}

func extractEmbeds(line string, images *[]string, seenImages map[string]bool) string {
	var matches []embedMatch

	wMatches := wikiEmbedRegex.FindAllStringSubmatchIndex(line, -1)
	for _, m := range wMatches {
		target := strings.TrimSpace(line[m[2]:m[3]])
		matches = append(matches, embedMatch{start: m[0], end: m[1], target: target})
	}

	mMatches := mdEmbedRegex.FindAllStringSubmatchIndex(line, -1)
	for _, m := range mMatches {
		target := strings.TrimSpace(line[m[2]:m[3]])
		matches = append(matches, embedMatch{start: m[0], end: m[1], target: target})
	}

	if len(matches) == 0 {
		return line
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].start < matches[j].start
	})

	var sb strings.Builder
	lastIdx := 0
	for _, m := range matches {
		if m.start < lastIdx {
			continue
		}
		sb.WriteString(line[lastIdx:m.start])
		lastIdx = m.end
		if !seenImages[m.target] {
			seenImages[m.target] = true
			*images = append(*images, m.target)
		}
	}
	sb.WriteString(line[lastIdx:])
	return sb.String()
}

// ParseDayNote reads the contents of a day note file.
func ParseDayNote(path, date string, data []byte) DayNote {
	raw := string(data)
	raw = strings.TrimPrefix(raw, "\ufeff")
	splitLines := strings.Split(raw, "\n")

	lines := make([]string, len(splitLines))
	for i, l := range splitLines {
		lines[i] = normalizeFullWidth(strings.TrimRight(l, " \t\r"))
	}

	// G0 frontmatter
	skipFrontmatterUntil := -1
	if len(lines) > 0 && lines[0] == "---" {
		for k := 1; k < len(lines); k++ {
			if lines[k] == "---" {
				skipFrontmatterUntil = k
				break
			}
		}
	}

	// G1 fence pre-pass (RV-1)
	var fenceLines []int
	for idx, l := range lines {
		lineNum := idx + 1
		if idx <= skipFrontmatterUntil {
			continue
		}
		trimmed := strings.TrimLeft(l, " \t")
		if strings.HasPrefix(trimmed, "```") {
			fenceLines = append(fenceLines, lineNum)
		}
	}

	closedFenceRanges := make(map[int]bool)
	unclosedFenceLine := -1
	numFences := len(fenceLines)
	if numFences%2 != 0 {
		unclosedFenceLine = fenceLines[numFences-1]
		numFences--
	}
	for i := 0; i < numFences; i += 2 {
		for ln := fenceLines[i]; ln <= fenceLines[i+1]; ln++ {
			closedFenceRanges[ln] = true
		}
	}

	var issues []Issue
	if unclosedFenceLine != -1 {
		issues = append(issues, Issue{
			Path: path,
			Line: unclosedFenceLine,
			Msg:  "unclosed code fence",
		})
	}

	var images []string
	seenImages := make(map[string]bool)
	var entries []Entry
	month := ""
	if len(date) >= 7 {
		month = date[:7]
	}

	for idx, line := range lines {
		lineNum := idx + 1

		if idx <= skipFrontmatterUntil {
			continue
		}

		// G1 fence
		if closedFenceRanges[lineNum] {
			continue
		}
		if lineNum == unclosedFenceLine {
			continue
		}

		trimmed := strings.TrimLeft(line, " \t")

		// G2 blank
		if trimmed == "" {
			continue
		}

		// G3 comment
		if strings.HasPrefix(trimmed, ">") ||
			strings.HasPrefix(trimmed, "//") ||
			strings.HasPrefix(trimmed, "<!--") ||
			trimmed == "#" ||
			strings.HasPrefix(trimmed, "# ") ||
			isHRule(line) {
			continue
		}

		// Embed extraction
		lineWithoutEmbeds := extractEmbeds(line, &images, seenImages)
		trimmed = strings.TrimSpace(lineWithoutEmbeds)
		if trimmed == "" {
			continue
		}

		// G4 bullet stripped
		content := trimmed
		if strings.HasPrefix(content, "- ") || strings.HasPrefix(content, "* ") {
			content = strings.TrimLeft(content[2:], " \t")
		}

		// Line that is exactly the note's date, alone
		if content == date {
			continue
		}

		// Line starts with YYYY-MM-DD
		if len(content) >= 10 && dayLineRegex.MatchString(content[:10]) && (len(content) == 10 || !unicode.IsDigit(rune(content[10]))) {
			lineDate := content[:10]
			if lineDate != date {
				issues = append(issues, Issue{
					Path: path,
					Line: lineNum,
					Msg:  fmt.Sprintf("date %s is not this note's date", lineDate),
				})
				continue
			}

			rest := content[10:]
			rest = strings.TrimSpace(rest)
			if strings.HasPrefix(rest, ",") || strings.HasPrefix(rest, ":") {
				rest = strings.TrimSpace(rest[1:])
			}
			if rest == "" {
				issues = append(issues, Issue{
					Path: path,
					Line: lineNum,
					Msg:  "no amount after the date",
				})
				continue
			}

			if !checkBalancedParens(rest) {
				issues = append(issues, Issue{
					Path: path,
					Line: lineNum,
					Msg:  "unbalanced parentheses",
				})
				continue
			}

			pieces := splitPlusOutsideParens(rest)
			for _, p := range pieces {
				item, issue := parseDayNotePiece(p, lineNum, path)
				if issue != nil {
					issues = append(issues, *issue)
				} else {
					entries = append(entries, Entry{
						Date:     date,
						Month:    month,
						Cents:    item.Cents,
						Category: item.Tag,
						Note:     item.Note,
						Kind:     KindDay,
						Path:     path,
						Line:     lineNum,
					})
				}
			}
			continue
		}

		// Any other line is an ITEM LINE
		if !checkBalancedParens(content) {
			issues = append(issues, Issue{
				Path: path,
				Line: lineNum,
				Msg:  "unbalanced parentheses",
			})
			continue
		}

		pieces := splitPlusOutsideParens(content)
		for _, p := range pieces {
			item, issue := parseDayNotePiece(p, lineNum, path)
			if issue != nil {
				issues = append(issues, *issue)
			} else {
				entries = append(entries, Entry{
					Date:     date,
					Month:    month,
					Cents:    item.Cents,
					Category: item.Tag,
					Note:     item.Note,
					Kind:     KindDay,
					Path:     path,
					Line:     lineNum,
				})
			}
		}
	}

	if len(issues) > 0 {
		return DayNote{
			Date:    date,
			Entries: nil,
			Issues:  issues,
			Images:  images,
		}
	}

	return DayNote{
		Date:    date,
		Entries: entries,
		Issues:  nil,
		Images:  images,
	}
}

func parseDayNotePiece(p string, lineNum int, path string) (intermediateDayItem, *Issue) {
	p = strings.TrimSpace(p)
	if p == "" {
		return intermediateDayItem{}, &Issue{
			Path: path,
			Line: lineNum,
			Msg:  "no amount in \"\"",
		}
	}

	// (a) G7 item (amount first)
	m := dayItemRegex.FindStringSubmatch(p)
	if m != nil {
		tail := m[3]
		if tail == "" || strings.HasPrefix(tail, " ") || strings.HasPrefix(tail, "\t") || strings.HasPrefix(tail, "(") {
			tag, note, inParens := parseItemTailWithParens(tail)
			if !inParens && note != "" {
				noteTokens := strings.Fields(note)
				if len(noteTokens) > 0 {
					allNumbers := true
					for _, tok := range noteTokens {
						if !isNumberToken(tok) {
							allNumbers = false
							break
						}
					}
					if allNumbers {
						return intermediateDayItem{}, &Issue{
							Path: path,
							Line: lineNum,
							Msg:  fmt.Sprintf("two numbers in %q (write 23 + 45, or put the note in parentheses)", p),
						}
					}
				}
			}

			centsVal, err := parseLooseAmount(m[2])
			if err != nil {
				return intermediateDayItem{}, &Issue{
					Path: path,
					Line: lineNum,
					Msg:  fmt.Sprintf("bad amount %q (want e.g. 44, 49.5 (note), 45 (kfc) #food)", p),
				}
			}

			if m[1] == "-" {
				// money in -> positive cents
			} else {
				// spending -> negative cents
				centsVal = -centsVal
			}

			return intermediateDayItem{
				Cents: centsVal,
				Tag:   tag,
				Note:  note,
			}, nil
		}
	}

	// (b) NOTE FIRST
	rawTokens := strings.Fields(p)
	var nonTags []string
	var firstTag string
	for _, tok := range rawTokens {
		if strings.HasPrefix(tok, "#") && len(tok) > 1 && isValidTagWord(tok[1:]) {
			if firstTag == "" {
				firstTag = strings.ToLower(tok[1:])
			}
		} else {
			nonTags = append(nonTags, tok)
		}
	}

	var amtIndices []int
	for i, tok := range nonTags {
		tokM := dayItemRegex.FindStringSubmatch(tok)
		if tokM != nil && tokM[3] == "" {
			amtIndices = append(amtIndices, i)
		}
	}

	if len(amtIndices) == 0 {
		return intermediateDayItem{}, &Issue{
			Path: path,
			Line: lineNum,
			Msg:  fmt.Sprintf("no amount in %q", p),
		}
	}

	if len(amtIndices) >= 2 {
		return intermediateDayItem{}, &Issue{
			Path: path,
			Line: lineNum,
			Msg:  fmt.Sprintf("two numbers in %q (write 23 + 45, or put the note in parentheses)", p),
		}
	}

	amtIdx := amtIndices[0]
	amtTok := nonTags[amtIdx]
	tokM := dayItemRegex.FindStringSubmatch(amtTok)
	centsVal, err := parseLooseAmount(tokM[2])
	if err != nil {
		return intermediateDayItem{}, &Issue{
			Path: path,
			Line: lineNum,
			Msg:  fmt.Sprintf("bad amount %q (want e.g. 44, 49.5 (note), 45 (kfc) #food)", p),
		}
	}
	if tokM[1] == "-" {
		// money in -> positive cents
	} else {
		// spending -> negative cents
		centsVal = -centsVal
	}

	var noteWords []string
	for i, tok := range nonTags {
		if i != amtIdx {
			noteWords = append(noteWords, tok)
		}
	}
	note := strings.Join(noteWords, " ")
	note = strings.TrimSpace(note)
	note = strings.TrimRight(note, ",;:\t ")
	if strings.HasPrefix(note, "(") && strings.HasSuffix(note, ")") {
		note = strings.TrimSpace(note[1 : len(note)-1])
		note = strings.TrimRight(note, ",;:\t ")
	}

	return intermediateDayItem{
		Cents: centsVal,
		Tag:   firstTag,
		Note:  note,
	}, nil
}

// CentralLine renders the line collect appends: "2026-11-10, 44 (jollibee) + 12.50 (bus) [[2026-11-10]]".
func CentralLine(date string, entries []Entry) string {
	if len(entries) == 0 {
		return fmt.Sprintf("%s [[%s]]", date, date)
	}

	var items []string
	for _, e := range entries {
		var s strings.Builder
		cents := e.Cents
		if cents > 0 {
			s.WriteString("-")
		} else {
			cents = -cents
		}
		formatted := FormatCents(cents)
		formatted = strings.TrimSuffix(formatted, ".00")
		s.WriteString(formatted)

		if e.Note != "" {
			s.WriteString(" (")
			s.WriteString(e.Note)
			s.WriteString(")")
		}
		if e.Category != "" {
			s.WriteString(" #")
			s.WriteString(e.Category)
		}
		items = append(items, s.String())
	}

	return fmt.Sprintf("%s, %s [[%s]]", date, strings.Join(items, " + "), date)
}
