package ledger

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const LooseFile = "bookkeeping.md" // at the vault root

type Kind int

const (
	KindDay   Kind = iota // one item of a loose day line
	KindFixed             // a loose month line
	KindV1                // a grammar-v1 transaction
)

type Entry struct {
	Date     string // "YYYY-MM-DD"; "" for KindFixed
	Month    string // "YYYY-MM"; always set
	Cents    int64  // NEGATIVE = money out, positive = money in (the v1 convention)
	Currency string // v1: its cur field. loose: the file's currency directive, "" when none
	Category string // "" = uncategorized
	Note     string
	Kind     Kind
	Path     string // vault-relative, slash-separated
	Line     int    // 1-based
	Source   string // text inside [[...]] source link at end of day line, "" when absent
}

type Book struct {
	Entries     []Entry // v1 entries (path, line order), then loose entries (line order, items left to right)
	Issues      []Issue // v1 issues, then loose issues (line order); Warn marks a warning
	Files       int     // v1 files read, +1 when bookkeeping.md exists
	BudgetCents int64   // the loose budget directive, 0 when none
	Currency    string  // the loose currency directive, "" when none
}

type categoryRule struct {
	Name     string
	Keywords []string
}

var (
	dayLineRegex    = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}`)
	dayItemRegex    = regexp.MustCompile(`^(-)?\$?([0-9]{1,12}(?:\.[0-9]{1,2})?)(.*)$`)
	currencyRegex   = regexp.MustCompile(`^[A-Z]{3}$`)
	budgetAmtRegex  = regexp.MustCompile(`^\$?([0-9]{1,12}(?:\.[0-9]{1,2})?)$`)
	monthLineMonths = map[string]string{
		"january":   "01",
		"jan":       "01",
		"february":  "02",
		"feb":       "02",
		"march":     "03",
		"mar":       "03",
		"april":     "04",
		"apr":       "04",
		"may":       "05",
		"june":      "06",
		"jun":       "06",
		"july":      "07",
		"jul":       "07",
		"august":    "08",
		"aug":       "08",
		"september": "09",
		"sep":       "09",
		"sept":      "09",
		"october":   "10",
		"oct":       "10",
		"november":  "11",
		"nov":       "11",
		"december":  "12",
		"dec":       "12",
	}
	monthLineOrder = []string{
		"september", "february", "november", "december",
		"january", "october", "august", "march", "april",
		"june", "july", "sept", "jan", "feb", "mar",
		"apr", "may", "jun", "jul", "aug", "sep", "oct",
		"nov", "dec",
	}
	multiSpaceRegex  = regexp.MustCompile(`\s+`)
	numberTokenRegex = regexp.MustCompile(`^[-+]?\$?[0-9]+(?:\.[0-9]+)?$`)
	sourceLinkRegex  = regexp.MustCompile(`\s+\[\[([^\[\]]+)\]\]$`)
	amountTokenRegex = regexp.MustCompile(`^([+-])?\$?([0-9]{1,12}(?:\.[0-9]+)?)([,;:.]*)$`)
)

func isNumberToken(s string) bool {
	return numberTokenRegex.MatchString(s)
}

func stripParens(s string) string {
	var sb strings.Builder
	depth := 0
	for _, r := range s {
		if r == '(' {
			depth++
			sb.WriteByte(' ')
		} else if r == ')' {
			if depth > 0 {
				depth--
			}
			sb.WriteByte(' ')
		} else if depth == 0 {
			sb.WriteRune(r)
		} else {
			sb.WriteByte(' ')
		}
	}
	return sb.String()
}

func isStandaloneAmountToken(tok string) bool {
	m := amountTokenRegex.FindStringSubmatch(tok)
	if m == nil {
		return false
	}
	return len(m[2]) > 0
}

func amountTokensOutsideParens(s string) (amtIndices []int, allTokens []string) {
	outside := stripParens(s)
	rawTokens := strings.Fields(outside)
	var tokens []string
	for _, tok := range rawTokens {
		if strings.HasPrefix(tok, "#") && len(tok) > 1 && isValidTagWord(tok[1:]) {
			continue
		}
		tokens = append(tokens, tok)
	}
	for i, tok := range tokens {
		if isStandaloneAmountToken(tok) {
			amtIndices = append(amtIndices, i)
		}
	}
	return amtIndices, tokens
}

func hasAmountToken(s string) bool {
	amtIndices, _ := amountTokensOutsideParens(s)
	return len(amtIndices) > 0
}

func splitCommaOutsideParens(s string) []string {
	var pieces []string
	var cur strings.Builder
	depth := 0
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '(' {
			depth++
			cur.WriteRune(r)
		} else if r == ')' {
			if depth > 0 {
				depth--
			}
			cur.WriteRune(r)
		} else if r == ',' && depth == 0 {
			// Do NOT split if comma is between digits (e.g. 1,200 or 12,5)
			if i > 0 && unicode.IsDigit(runes[i-1]) && i+1 < len(runes) && unicode.IsDigit(runes[i+1]) {
				cur.WriteRune(r)
			} else {
				pieces = append(pieces, strings.TrimSpace(cur.String()))
				cur.Reset()
			}
		} else {
			cur.WriteRune(r)
		}
	}
	pieces = append(pieces, strings.TrimSpace(cur.String()))
	return pieces
}

func repairCommaPieces(pieces []string) []string {
	var res []string
	for _, p := range pieces {
		p = strings.TrimSpace(p)
		if p != "" {
			res = append(res, p)
		}
	}
	if len(res) <= 1 {
		return res
	}
	i := 0
	for i < len(res) {
		if !hasAmountToken(res[i]) {
			if i+1 < len(res) {
				// join with space to the piece after it
				res[i+1] = res[i] + " " + res[i+1]
				res = append(res[:i], res[i+1:]...)
			} else if i > 0 {
				// last piece: join to the piece before it
				res[i-1] = res[i-1] + " " + res[i]
				res = res[:i]
				i--
			} else {
				i++
			}
		} else {
			i++
		}
	}
	return res
}

func checkBalancedParens(s string) bool {
	depth := 0
	for _, r := range s {
		if r == '(' {
			depth++
		} else if r == ')' {
			if depth == 0 {
				return false
			}
			depth--
		}
	}
	return depth == 0
}

func is4Digits(s string) bool {
	if len(s) != 4 {
		return false
	}
	for i := 0; i < 4; i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func normalizeFullWidth(line string) string {
	var sb strings.Builder
	for _, r := range line {
		switch {
		case r == '\uff0c':
			sb.WriteByte(',')
		case r == '\uff1a':
			sb.WriteByte(':')
		case r == '\uff0b':
			sb.WriteByte('+')
		case r == '\uff08':
			sb.WriteByte('(')
		case r == '\uff09':
			sb.WriteByte(')')
		case r == '\uff03':
			sb.WriteByte('#')
		case r == '\uff1d':
			sb.WriteByte('=')
		case r == '\uff04':
			sb.WriteByte('$')
		case r == '\uff0d':
			sb.WriteByte('-')
		case r == '\uff0e':
			sb.WriteByte('.')
		case r == '\u3000':
			sb.WriteByte(' ')
		case r >= '\uff10' && r <= '\uff19':
			sb.WriteByte(byte('0' + (r - '\uff10')))
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// parseLooseAmount converts an amount string (like "49.5" or "44") to integer cents without non-integer types.
func parseLooseAmount(s string) (int64, error) {
	parts := strings.Split(s, ".")
	if len(parts) == 1 {
		dollars, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return 0, err
		}
		return dollars * 100, nil
	}
	if len(parts) == 2 {
		dollars, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return 0, err
		}
		dec := parts[1]
		if len(dec) == 1 {
			cents := int64(dec[0]-'0') * 10
			return dollars*100 + cents, nil
		}
		if len(dec) == 2 {
			cents := int64(dec[0]-'0')*10 + int64(dec[1]-'0')
			return dollars*100 + cents, nil
		}
	}
	return 0, fmt.Errorf("invalid amount %q", s)
}

// splitPlusOutsideParens splits a string on '+' characters that are not inside parentheses.
func splitPlusOutsideParens(s string) []string {
	var pieces []string
	var cur strings.Builder
	depth := 0
	for _, r := range s {
		if r == '(' {
			depth++
			cur.WriteRune(r)
		} else if r == ')' {
			if depth > 0 {
				depth--
			}
			cur.WriteRune(r)
		} else if r == '+' && depth == 0 {
			pieces = append(pieces, strings.TrimSpace(cur.String()))
			cur.Reset()
		} else {
			cur.WriteRune(r)
		}
	}
	pieces = append(pieces, strings.TrimSpace(cur.String()))
	return pieces
}

// isHRule checks if line holds only chars -, *, _, = and spaces/tabs with at least 3 of the symbols.
func isHRule(line string) bool {
	count := 0
	for _, r := range line {
		if r == '-' || r == '*' || r == '_' || r == '=' {
			count++
		} else if r != ' ' && r != '\t' {
			return false
		}
	}
	return count >= 3
}

// isValidTagWord checks if s is one or more letters, digits, _, or -.
func isValidTagWord(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

// parseItemTail extracts the first tag (if any) and the note from an item's tail.
func parseItemTailWithParens(tail string) (firstTag string, note string, inParens bool) {
	tokens := strings.Fields(tail)
	var nonTags []string
	for _, tok := range tokens {
		if strings.HasPrefix(tok, "#") && len(tok) > 1 && isValidTagWord(tok[1:]) {
			if firstTag == "" {
				firstTag = strings.ToLower(tok[1:])
			}
		} else {
			nonTags = append(nonTags, tok)
		}
	}
	rem := strings.Join(nonTags, " ")
	rem = strings.TrimSpace(rem)
	if strings.HasPrefix(rem, "(") && strings.HasSuffix(rem, ")") {
		inParens = true
		rem = rem[1 : len(rem)-1]
		rem = strings.TrimSpace(rem)
	}
	note = rem
	return firstTag, note, inParens
}

func parseItemTail(tail string) (firstTag string, note string) {
	tag, note, _ := parseItemTailWithParens(tail)
	return tag, note
}

type intermediateDayLine struct {
	LineNum  int
	Date     string
	Month    string
	Year     string
	Items    []intermediateDayItem
	DateWarn *Issue
	Source   string
}

type intermediateDayItem struct {
	Cents int64
	Tag   string
	Note  string
}

type intermediateMonthLine struct {
	LineNum      int
	MonthPart    string
	ExplicitYear string
	Sign         string
	Label        string
	AmountStr    string
}

// ParseLoose parses the bytes of a loose file. path is only copied into Entry.Path / Issue.Path.
func ParseLoose(path string, data []byte) (entries []Entry, issues []Issue, budgetCents int64, currency string) {
	raw := string(data)
	raw = strings.TrimPrefix(raw, "\ufeff")
	splitLines := strings.Split(raw, "\n")

	lines := make([]string, len(splitLines))
	for i, l := range splitLines {
		lines[i] = normalizeFullWidth(strings.TrimRight(l, " \t\r"))
	}

	// G0 frontmatter: when line 1 is exactly `---` and a later line is exactly `---`, lines 1..that line are skipped.
	skipFrontmatterUntil := -1
	if len(lines) > 0 && lines[0] == "---" {
		for k := 1; k < len(lines); k++ {
			if lines[k] == "---" {
				skipFrontmatterUntil = k
				break
			}
		}
	}

	// G1 fence pre-pass
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
	if unclosedFenceLine != -1 {
		issues = append(issues, Issue{
			Path: path,
			Line: unclosedFenceLine,
			Msg:  "unclosed code fence",
		})
	}

	var rules []categoryRule
	var dayLines []intermediateDayLine
	var monthLines []intermediateMonthLine
	type dateOccurrence struct {
		lineNum   int
		hasSource bool
	}
	seenDateOccurrences := make(map[string][]dateOccurrence)

	type lineResult struct {
		lineNum  int
		isDay    bool
		dayIdx   int
		isMonth  bool
		monthIdx int
	}
	var lineResults []lineResult

	for idx, line := range lines {
		lineNum := idx + 1

		// G0 frontmatter
		if idx <= skipFrontmatterUntil {
			continue
		}

		trimmed := strings.TrimLeft(line, " \t")

		// G1 fence
		if closedFenceRanges[lineNum] {
			continue
		}
		if lineNum == unclosedFenceLine {
			continue
		}

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

		// G4 bullet
		content := trimmed
		if strings.HasPrefix(content, "- ") || strings.HasPrefix(content, "* ") {
			content = strings.TrimLeft(content[2:], " \t")
		}

		// G5 directive: currency = XXX or budget = N
		eqIdx := strings.Index(content, "=")
		if eqIdx != -1 && !strings.HasPrefix(content, "#") {
			key := strings.ToLower(strings.TrimSpace(content[:eqIdx]))
			val := strings.TrimSpace(content[eqIdx+1:])
			if key == "currency" {
				upperVal := strings.ToUpper(val)
				if currencyRegex.MatchString(upperVal) {
					currency = upperVal
				} else {
					issues = append(issues, Issue{
						Path: path,
						Line: lineNum,
						Msg:  fmt.Sprintf("bad currency %q", val),
					})
				}
				continue
			}
			if key == "budget" {
				m := budgetAmtRegex.FindStringSubmatch(val)
				if m != nil {
					amtCents, err := parseLooseAmount(m[1])
					if err == nil && amtCents >= 0 {
						budgetCents = amtCents
					} else {
						issues = append(issues, Issue{
							Path: path,
							Line: lineNum,
							Msg:  fmt.Sprintf("bad budget %q", val),
						})
					}
				} else {
					issues = append(issues, Issue{
						Path: path,
						Line: lineNum,
						Msg:  fmt.Sprintf("bad budget %q", val),
					})
				}
				continue
			}
		}

		// G6 category rule: #<name> = kw1, kw2, ...
		if strings.HasPrefix(content, "#") && eqIdx != -1 {
			nameCandidate := strings.TrimSpace(content[1:eqIdx])
			if isValidTagWord(nameCandidate) {
				kwPart := content[eqIdx+1:]
				rawKws := strings.Split(kwPart, ",")
				var kws []string
				for _, kw := range rawKws {
					trimmedKw := strings.ToLower(strings.TrimSpace(kw))
					if trimmedKw != "" {
						kws = append(kws, trimmedKw)
					}
				}
				if len(kws) == 0 {
					issues = append(issues, Issue{
						Path: path,
						Line: lineNum,
						Msg:  "category rule has no keyword",
					})
				} else {
					rules = append(rules, categoryRule{
						Name:     strings.ToLower(nameCandidate),
						Keywords: kws,
					})
				}
				continue
			}
		}

		// G7 day line: starts with YYYY-MM-DD
		if len(content) >= 10 && dayLineRegex.MatchString(content[:10]) && (len(content) == 10 || !unicode.IsDigit(rune(content[10]))) {
			dateStr := content[:10]
			if _, err := time.Parse("2006-01-02", dateStr); err != nil {
				issues = append(issues, Issue{
					Path: path,
					Line: lineNum,
					Msg:  fmt.Sprintf("invalid date %q", dateStr),
				})
				continue
			}

			rest := content[10:]
			rest = strings.TrimSpace(rest)
			if strings.HasPrefix(rest, ",") || strings.HasPrefix(rest, ":") {
				rest = strings.TrimSpace(rest[1:])
			}

			source := ""
			if m := sourceLinkRegex.FindStringSubmatchIndex(rest); m != nil {
				source = rest[m[2]:m[3]]
				rest = strings.TrimSpace(rest[:m[0]])
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
			allValid := true
			var lineItems []intermediateDayItem

			for _, p := range pieces {
				if p == "" || strings.ContainsRune(stripParens(p), ',') {
					allValid = false
					issues = append(issues, Issue{
						Path: path,
						Line: lineNum,
						Msg:  fmt.Sprintf("bad amount %q (want e.g. 44, 49.5 (note), 45 (kfc) #food)", p),
					})
					break
				}
				if amtIndices, _ := amountTokensOutsideParens(p); len(amtIndices) >= 2 {
					allValid = false
					issues = append(issues, Issue{
						Path: path,
						Line: lineNum,
						Msg:  fmt.Sprintf("two numbers in %q (write 23 + 45, or put the note in parentheses)", p),
					})
					break
				}
				m := dayItemRegex.FindStringSubmatch(p)
				if m == nil {
					allValid = false
					issues = append(issues, Issue{
						Path: path,
						Line: lineNum,
						Msg:  fmt.Sprintf("bad amount %q (want e.g. 44, 49.5 (note), 45 (kfc) #food)", p),
					})
					break
				}

				tail := m[3]
				if tail != "" && !strings.HasPrefix(tail, " ") && !strings.HasPrefix(tail, "\t") && !strings.HasPrefix(tail, "(") {
					allValid = false
					issues = append(issues, Issue{
						Path: path,
						Line: lineNum,
						Msg:  fmt.Sprintf("bad amount %q (want e.g. 44, 49.5 (note), 45 (kfc) #food)", p),
					})
					break
				}

				centsVal, err := parseLooseAmount(m[2])
				if err != nil {
					allValid = false
					issues = append(issues, Issue{
						Path: path,
						Line: lineNum,
						Msg:  fmt.Sprintf("bad amount %q (want e.g. 44, 49.5 (note), 45 (kfc) #food)", p),
					})
					break
				}

				if m[1] == "-" {
					// negative sign means money in -> positive cents
				} else {
					// spending -> negative cents
					centsVal = -centsVal
				}

				tag, note, _ := parseItemTailWithParens(tail)

				lineItems = append(lineItems, intermediateDayItem{
					Cents: centsVal,
					Tag:   tag,
					Note:  note,
				})
			}

			if !allValid {
				continue
			}

			var warn *Issue
			hasSource := (source != "")
			if prevs, seen := seenDateOccurrences[dateStr]; seen {
				if !hasSource {
					for _, p := range prevs {
						if !p.hasSource {
							warn = &Issue{
								Path: path,
								Line: lineNum,
								Msg:  fmt.Sprintf("date %s also on line %d", dateStr, p.lineNum),
								Warn: true,
							}
							break
						}
					}
				}
			}
			seenDateOccurrences[dateStr] = append(seenDateOccurrences[dateStr], dateOccurrence{
				lineNum:   lineNum,
				hasSource: hasSource,
			})

			dl := intermediateDayLine{
				LineNum:  lineNum,
				Date:     dateStr,
				Month:    dateStr[:7],
				Year:     dateStr[:4],
				Items:    lineItems,
				DateWarn: warn,
				Source:   source,
			}
			dayLines = append(dayLines, dl)
			lineResults = append(lineResults, lineResult{
				lineNum: lineNum,
				isDay:   true,
				dayIdx:  len(dayLines) - 1,
			})
			continue
		}

		// G8 month line: <MONTH>[ <YYYY>] <sign> <label> <amount>
		// MONTH can be English, <N>\u6708, or YYYY-MM
		parsedMonth, ok := tryParseMonthLine(content)
		if ok {
			parsedMonth.LineNum = lineNum
			monthLines = append(monthLines, parsedMonth)
			lineResults = append(lineResults, lineResult{
				lineNum:  lineNum,
				isMonth:  true,
				monthIdx: len(monthLines) - 1,
			})
			continue
		}

		// G9 anything else
		issues = append(issues, Issue{
			Path: path,
			Line: lineNum,
			Msg:  "not understood (want \"YYYY-MM-DD, 44 (note) + 12.5\" or \"Nov - rent 5000\")",
		})
	}

	// Resolve category helper for day items
	resolveDayCategory := func(tag string, note string) string {
		if tag != "" {
			return tag
		}
		lowerNote := strings.ToLower(note)
		if lowerNote != "" {
			for _, r := range rules {
				for _, kw := range r.Keywords {
					if kw != "*" && strings.Contains(lowerNote, kw) {
						return r.Name
					}
				}
			}
		}
		for _, r := range rules {
			for _, kw := range r.Keywords {
				if kw == "*" {
					return r.Name
				}
			}
		}
		return ""
	}

	// Resolve month lines and entries
	// Note: entries must be in line order; issues must be in line order.
	var lineEntriesMap = make(map[int][]Entry)
	var lineIssuesMap = make(map[int][]Issue)

	for _, dl := range dayLines {
		if dl.DateWarn != nil {
			lineIssuesMap[dl.LineNum] = append(lineIssuesMap[dl.LineNum], *dl.DateWarn)
		}
		var itemEntries []Entry
		for _, item := range dl.Items {
			cat := resolveDayCategory(item.Tag, item.Note)
			itemEntries = append(itemEntries, Entry{
				Date:     dl.Date,
				Month:    dl.Month,
				Cents:    item.Cents,
				Currency: currency,
				Category: cat,
				Note:     item.Note,
				Kind:     KindDay,
				Path:     path,
				Line:     dl.LineNum,
				Source:   dl.Source,
			})
		}
		lineEntriesMap[dl.LineNum] = itemEntries
	}

	for _, ml := range monthLines {
		year := ml.ExplicitYear
		if year == "" {
			// Find year from first valid day line below
			for _, dl := range dayLines {
				if dl.LineNum > ml.LineNum {
					year = dl.Year
					break
				}
			}
			// If none below, from nearest valid day line above
			if year == "" {
				for i := len(dayLines) - 1; i >= 0; i-- {
					if dayLines[i].LineNum < ml.LineNum {
						year = dayLines[i].Year
						break
					}
				}
			}
		}

		if year == "" {
			lineIssuesMap[ml.LineNum] = append(lineIssuesMap[ml.LineNum], Issue{
				Path: path,
				Line: ml.LineNum,
				Msg:  "month line needs a year, e.g. \"Nov 2026 - rent 5000\"",
			})
			continue
		}

		monthVal := fmt.Sprintf("%s-%s", year, ml.MonthPart)
		amtCents, err := parseLooseAmount(ml.AmountStr)
		if err != nil {
			lineIssuesMap[ml.LineNum] = append(lineIssuesMap[ml.LineNum], Issue{
				Path: path,
				Line: ml.LineNum,
				Msg:  fmt.Sprintf("bad amount %q", ml.AmountStr),
			})
			continue
		}
		if ml.Sign == "-" {
			amtCents = -amtCents
		}

		tag, note := parseItemTail(ml.Label)
		cat := ""
		if tag != "" {
			cat = tag
		} else {
			// rule match IGNORING catch-all
			lowerNote := strings.ToLower(note)
			if lowerNote != "" {
				for _, r := range rules {
					for _, kw := range r.Keywords {
						if kw != "*" && strings.Contains(lowerNote, kw) {
							cat = r.Name
							break
						}
					}
					if cat != "" {
						break
					}
				}
			}
			if cat == "" {
				cat = multiSpaceRegex.ReplaceAllString(strings.ToLower(note), "-")
			}
		}

		entry := Entry{
			Date:     "",
			Month:    monthVal,
			Cents:    amtCents,
			Currency: currency,
			Category: cat,
			Note:     note,
			Kind:     KindFixed,
			Path:     path,
			Line:     ml.LineNum,
		}
		lineEntriesMap[ml.LineNum] = []Entry{entry}
	}

	// Merge issues in line order
	sort.SliceStable(issues, func(i, j int) bool {
		return issues[i].Line < issues[j].Line
	})
	var allIssues []Issue
	// Existing directive/rule/parse issues
	issueIdx := 0
	for lineNum := 1; lineNum <= len(lines); lineNum++ {
		for issueIdx < len(issues) && issues[issueIdx].Line == lineNum {
			allIssues = append(allIssues, issues[issueIdx])
			issueIdx++
		}
		if extra, ok := lineIssuesMap[lineNum]; ok {
			allIssues = append(allIssues, extra...)
		}
	}
	for issueIdx < len(issues) {
		allIssues = append(allIssues, issues[issueIdx])
		issueIdx++
	}

	// Collect entries in line order
	for lineNum := 1; lineNum <= len(lines); lineNum++ {
		if ens, ok := lineEntriesMap[lineNum]; ok {
			entries = append(entries, ens...)
		}
	}

	return entries, allIssues, budgetCents, currency
}

// tryParseMonthLine attempts to parse a G8 month line.
func tryParseMonthLine(line string) (intermediateMonthLine, bool) {
	// Guard: a day line starting with YYYY-MM-DD must never be parsed as a month line.
	if dayLineRegex.MatchString(line) {
		return intermediateMonthLine{}, false
	}

	// The amount is digits with optional . and 1-2 digits, the LAST whitespace-delimited token.
	lastSpaceIdx := strings.LastIndexAny(line, " \t")
	if lastSpaceIdx == -1 {
		return intermediateMonthLine{}, false
	}
	lastTok := strings.TrimSpace(line[lastSpaceIdx:])
	m := budgetAmtRegex.FindStringSubmatch(lastTok)
	if m == nil {
		return intermediateMonthLine{}, false
	}
	amountStr := m[1]

	head := strings.TrimRight(line[:lastSpaceIdx], " \t")
	if head == "" {
		return intermediateMonthLine{}, false
	}

	var explicitYear string
	var monthNum string
	var rem string

	// Form A: YYYY-MM
	if len(head) >= 7 && is4Digits(head[:4]) && head[4] == '-' {
		mCandidate := head[5:7]
		if mVal, err := strconv.Atoi(mCandidate); err == nil && mVal >= 1 && mVal <= 12 {
			if len(head) == 7 || head[7] == ' ' || head[7] == '\t' || head[7] == '-' || head[7] == '+' {
				explicitYear = head[:4]
				monthNum = mCandidate
				rem = head[7:]
			}
		}
	}

	// Form B: <N>\u6708 (N 1..12)
	if monthNum == "" && strings.Contains(head, "\u6708") {
		idx := strings.Index(head, "\u6708")
		if idx == 1 || idx == 2 {
			nStr := head[:idx]
			if n, err := strconv.Atoi(nStr); err == nil && n >= 1 && n <= 12 {
				afterYue := head[idx+len("\u6708"):]
				if afterYue == "" || afterYue[0] == ' ' || afterYue[0] == '\t' || afterYue[0] == '-' || afterYue[0] == '+' {
					monthNum = fmt.Sprintf("%02d", n)
					rem = afterYue
				}
			}
		}
	}

	// Form C: English month name
	if monthNum == "" {
		lowerHead := strings.ToLower(head)
		for _, cand := range monthLineOrder {
			if strings.HasPrefix(lowerHead, cand) {
				cLen := len(cand)
				if len(head) > cLen && (head[cLen] == ' ' || head[cLen] == '\t' || head[cLen] == '-' || head[cLen] == '+') {
					monthNum = monthLineMonths[cand]
					rem = head[cLen:]
					break
				}
			}
		}
	}

	if monthNum == "" {
		return intermediateMonthLine{}, false
	}

	// Parse optional 4-digit YYYY if not already explicit from YYYY-MM
	remTrimmed := strings.TrimLeft(rem, " \t")
	if explicitYear == "" && len(remTrimmed) >= 4 && is4Digits(remTrimmed[:4]) {
		if len(remTrimmed) == 4 || remTrimmed[4] == ' ' || remTrimmed[4] == '\t' || remTrimmed[4] == '-' || remTrimmed[4] == '+' {
			explicitYear = remTrimmed[:4]
			remTrimmed = strings.TrimLeft(remTrimmed[4:], " \t")
		}
	}

	// The sign is '-' or '+' with optional spaces around it
	if len(remTrimmed) == 0 || (remTrimmed[0] != '-' && remTrimmed[0] != '+') {
		return intermediateMonthLine{}, false
	}
	sign := string(remTrimmed[0])

	// Label is what lies between sign and amount, trimmed, not empty
	label := strings.TrimSpace(remTrimmed[1:])
	if label == "" || strings.HasPrefix(label, "+") || strings.HasPrefix(label, "-") {
		return intermediateMonthLine{}, false
	}

	return intermediateMonthLine{
		MonthPart:    monthNum,
		ExplicitYear: explicitYear,
		Sign:         sign,
		Label:        label,
		AmountStr:    amountStr,
	}, true
}

// LoadBook loads transactions from both ledger/ directory and bookkeeping.md loose ledger.
func LoadBook(root string) (Book, error) {
	txns, v1Issues, files, err := Load(root)
	if err != nil {
		return Book{}, err
	}

	var entries []Entry
	for _, t := range txns {
		var d, m string
		if len(t.ID) >= 8 {
			d = t.ID[:4] + "-" + t.ID[4:6] + "-" + t.ID[6:8]
			m = t.ID[:4] + "-" + t.ID[4:6]
		}
		entries = append(entries, Entry{
			Date:     d,
			Month:    m,
			Cents:    t.Cents,
			Currency: t.Currency,
			Category: t.Category,
			Note:     t.Desc,
			Kind:     KindV1,
			Path:     t.Path,
			Line:     t.Line,
		})
	}

	loosePath := filepath.Join(root, LooseFile)
	info, statErr := os.Stat(loosePath)
	if statErr == nil && info.Mode().IsRegular() {
		files++
		data, readErr := os.ReadFile(loosePath)
		if readErr != nil {
			return Book{}, readErr
		}
		looseEntries, looseIssues, budgetCents, currency := ParseLoose(LooseFile, data)
		entries = append(entries, looseEntries...)
		v1Issues = append(v1Issues, looseIssues...)
		return Book{
			Entries:     entries,
			Issues:      v1Issues,
			Files:       files,
			BudgetCents: budgetCents,
			Currency:    currency,
		}, nil
	}

	return Book{
		Entries: entries,
		Issues:  v1Issues,
		Files:   files,
	}, nil
}
