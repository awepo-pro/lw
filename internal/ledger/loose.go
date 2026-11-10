package ledger

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	multiSpaceRegex = regexp.MustCompile(`\s+`)
)

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
func parseItemTail(tail string) (firstTag string, note string) {
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
		rem = rem[1 : len(rem)-1]
		rem = strings.TrimSpace(rem)
	}
	note = rem
	return firstTag, note
}

type intermediateDayLine struct {
	LineNum  int
	Date     string
	Month    string
	Year     string
	Items    []intermediateDayItem
	DateWarn *Issue
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
		lines[i] = strings.TrimRight(l, " \t\r")
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

	inFence := false
	var rules []categoryRule
	var dayLines []intermediateDayLine
	var monthLines []intermediateMonthLine
	seenDates := make(map[string]int)

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
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
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

			if rest == "" {
				issues = append(issues, Issue{
					Path: path,
					Line: lineNum,
					Msg:  "no amount after the date",
				})
				continue
			}

			pieces := splitPlusOutsideParens(rest)
			allValid := true
			var lineItems []intermediateDayItem

			for _, p := range pieces {
				if p == "" {
					allValid = false
					issues = append(issues, Issue{
						Path: path,
						Line: lineNum,
						Msg:  fmt.Sprintf("bad amount %q (want e.g. 44, 49.5 (note), 45 (kfc) #food)", p),
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
				if tail != "" && !strings.HasPrefix(tail, " ") && !strings.HasPrefix(tail, "(") {
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

				tag, note := parseItemTail(tail)
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
			if firstLine, seen := seenDates[dateStr]; seen {
				warn = &Issue{
					Path: path,
					Line: lineNum,
					Msg:  fmt.Sprintf("date %s also on line %d", dateStr, firstLine),
					Warn: true,
				}
			} else {
				seenDates[dateStr] = lineNum
			}

			dl := intermediateDayLine{
				LineNum:  lineNum,
				Date:     dateStr,
				Month:    dateStr[:7],
				Year:     dateStr[:4],
				Items:    lineItems,
				DateWarn: warn,
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
	// Look for last token as amount: digits with optional . and 1-2 digits
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return intermediateMonthLine{}, false
	}

	lastTok := fields[len(fields)-1]
	m := budgetAmtRegex.FindStringSubmatch(lastTok)
	if m == nil {
		return intermediateMonthLine{}, false
	}
	amountStr := m[1]

	// Find the sign: - or +
	// Sign can have optional spaces around it.
	// But sign must be after the MONTH [YYYY] part and before the label.
	// Let's inspect the beginning of the line for MONTH and optional YYYY.
	monthToken := fields[0]
	explicitYear := ""
	monthNum := ""
	restIdx := 1

	// Check YYYY-MM
	if len(monthToken) == 7 && monthToken[4] == '-' {
		y := monthToken[:4]
		mPart := monthToken[5:7]
		if _, err := strconv.Atoi(y); err == nil {
			if mVal, err := strconv.Atoi(mPart); err == nil && mVal >= 1 && mVal <= 12 {
				explicitYear = y
				monthNum = mPart
			}
		}
	}

	// Check <N>\u6708
	if monthNum == "" && strings.HasSuffix(monthToken, "\u6708") {
		numStr := strings.TrimSuffix(monthToken, "\u6708")
		if n, err := strconv.Atoi(numStr); err == nil && n >= 1 && n <= 12 {
			monthNum = fmt.Sprintf("%02d", n)
		}
	}

	// Check English month name
	if monthNum == "" {
		if mn, ok := monthLineMonths[strings.ToLower(monthToken)]; ok {
			monthNum = mn
		}
	}

	if monthNum == "" {
		return intermediateMonthLine{}, false
	}

	// If explicitYear wasn't set by YYYY-MM, check if next field is a 4-digit year
	if explicitYear == "" && restIdx < len(fields)-1 {
		cand := fields[restIdx]
		if len(cand) == 4 {
			if _, err := strconv.Atoi(cand); err == nil {
				explicitYear = cand
				restIdx++
			}
		}
	}

	// What remains after MONTH [YYYY]:
	// In the original line, find where MONTH [YYYY] ended.
	var afterMonth string
	if explicitYear != "" && len(monthToken) != 7 {
		// Both monthToken and explicitYear were separate
		idx1 := strings.Index(line, monthToken)
		idx2 := strings.Index(line[idx1+len(monthToken):], explicitYear)
		afterMonth = line[idx1+len(monthToken)+idx2+len(explicitYear):]
	} else {
		idx1 := strings.Index(line, monthToken)
		afterMonth = line[idx1+len(monthToken):]
	}

	// In afterMonth, find the sign (- or +)
	afterMonthTrimmed := strings.TrimLeft(afterMonth, " \t")
	if len(afterMonthTrimmed) == 0 {
		return intermediateMonthLine{}, false
	}

	signChar := afterMonthTrimmed[0]
	if signChar != '-' && signChar != '+' {
		return intermediateMonthLine{}, false
	}
	sign := string(signChar)

	// After sign up to the last whitespace-delimited token is label
	afterSign := afterMonthTrimmed[1:]
	// The amount is the last token
	lastIdx := strings.LastIndex(afterSign, lastTok)
	if lastIdx == -1 {
		return intermediateMonthLine{}, false
	}
	label := strings.TrimSpace(afterSign[:lastIdx])
	if label == "" {
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
