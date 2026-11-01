package ledger

import (
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Precompiled regular expressions for Grammar v1 validation (074 ledger leak prevention).
var (
	idRegex          = regexp.MustCompile(`^[0-9]{8}-[0-9]{4}-[a-z]+$`)
	amtRegex         = regexp.MustCompile(`^-?[0-9]+\.[0-9]{2}$`)
	curRegex         = regexp.MustCompile(`^[A-Z]{3}$`)
	catRegex         = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	acctRegex        = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	dayFileRegex     = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
	inlineFieldRegex = regexp.MustCompile(`\([a-zA-Z0-9_-]+::`)
)

// Txn is one parsed grammar-v1 transaction line.
type Txn struct {
	ID       string // "20261031-1230-a"
	Cents    int64  // -4500 for "-45.00"
	Currency string // "HKD"
	Category string // "food"
	Account  string // "cash"
	Receipt  string // "r-20261031-1230.jpg" (inside [[...]]), "" when absent
	Desc     string // free text after the fields, trimmed; may be ""
	Path     string // vault-relative, slash-separated: "ledger/2026/10/2026-10-31.md"
	Line     int    // 1-based
}

// Issue is one check finding, printed as "<Path>:<Line>: <Msg>" (Line 0 = whole file).
type Issue struct {
	Path string
	Line int
	Msg  string
}

// ParseLine parses one line. isTxn is false (and err nil) for a line that is not a
// transaction line; a transaction line is one whose text, after leading spaces/tabs,
// starts with "- (id::". For a transaction line that breaks grammar v1, err says what.
func ParseLine(line string) (t Txn, isTxn bool, err error) {
	line = strings.TrimPrefix(line, "\ufeff")
	line = strings.TrimRight(line, "\r\n")

	trimmed := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(trimmed, "- (id::") {
		return Txn{}, false, nil
	}
	isTxn = true

	rest := strings.TrimPrefix(trimmed, "- ")

	// Field 1: (id:: I)
	if !strings.HasPrefix(rest, "(id:: ") {
		return t, true, fmt.Errorf("malformed id field: %q", rest)
	}
	rest = rest[len("(id:: "):]
	idEnd := strings.IndexByte(rest, ')')
	if idEnd == -1 {
		return t, true, fmt.Errorf("unclosed id field")
	}
	idVal := rest[:idEnd]
	rest = rest[idEnd+1:]

	if !idRegex.MatchString(idVal) {
		return t, true, fmt.Errorf("invalid id format: %q", idVal)
	}
	idParts := strings.Split(idVal, "-")
	if len(idParts) != 3 {
		return t, true, fmt.Errorf("invalid id format: %q", idVal)
	}
	if _, dateErr := time.Parse("20060102", idParts[0]); dateErr != nil {
		return t, true, fmt.Errorf("invalid date in id %q: %w", idVal, dateErr)
	}
	if _, timeErr := time.Parse("1504", idParts[1]); timeErr != nil {
		return t, true, fmt.Errorf("invalid time in id %q: %w", idVal, timeErr)
	}
	t.ID = idVal

	// Separator
	if !strings.HasPrefix(rest, " ") {
		return t, true, fmt.Errorf("expected space after id field")
	}
	rest = rest[1:]
	if strings.HasPrefix(rest, " ") {
		return t, true, fmt.Errorf("multiple spaces after id field")
	}

	// Field 2: (amt:: A)
	if !strings.HasPrefix(rest, "(amt:: ") {
		return t, true, fmt.Errorf("expected amt field, got %q", rest)
	}
	rest = rest[len("(amt:: "):]
	amtEnd := strings.IndexByte(rest, ')')
	if amtEnd == -1 {
		return t, true, fmt.Errorf("unclosed amt field")
	}
	amtVal := rest[:amtEnd]
	rest = rest[amtEnd+1:]

	if !amtRegex.MatchString(amtVal) {
		return t, true, fmt.Errorf("invalid amount: %q", amtVal)
	}
	cents, centsErr := parseCents(amtVal)
	if centsErr != nil {
		return t, true, centsErr
	}
	t.Cents = cents

	// Separator
	if !strings.HasPrefix(rest, " ") {
		return t, true, fmt.Errorf("expected space after amt field")
	}
	rest = rest[1:]
	if strings.HasPrefix(rest, " ") {
		return t, true, fmt.Errorf("multiple spaces after amt field")
	}

	// Field 3: (cur:: C)
	if !strings.HasPrefix(rest, "(cur:: ") {
		return t, true, fmt.Errorf("expected cur field, got %q", rest)
	}
	rest = rest[len("(cur:: "):]
	curEnd := strings.IndexByte(rest, ')')
	if curEnd == -1 {
		return t, true, fmt.Errorf("unclosed cur field")
	}
	curVal := rest[:curEnd]
	rest = rest[curEnd+1:]

	if !curRegex.MatchString(curVal) {
		return t, true, fmt.Errorf("invalid currency: %q", curVal)
	}
	t.Currency = curVal

	// Separator
	if !strings.HasPrefix(rest, " ") {
		return t, true, fmt.Errorf("expected space after cur field")
	}
	rest = rest[1:]
	if strings.HasPrefix(rest, " ") {
		return t, true, fmt.Errorf("multiple spaces after cur field")
	}

	// Field 4: (cat:: K)
	if !strings.HasPrefix(rest, "(cat:: ") {
		return t, true, fmt.Errorf("expected cat field, got %q", rest)
	}
	rest = rest[len("(cat:: "):]
	catEnd := strings.IndexByte(rest, ')')
	if catEnd == -1 {
		return t, true, fmt.Errorf("unclosed cat field")
	}
	catVal := rest[:catEnd]
	rest = rest[catEnd+1:]

	if !catRegex.MatchString(catVal) {
		return t, true, fmt.Errorf("invalid category: %q", catVal)
	}
	t.Category = catVal

	// Separator
	if !strings.HasPrefix(rest, " ") {
		return t, true, fmt.Errorf("expected space after cat field")
	}
	rest = rest[1:]
	if strings.HasPrefix(rest, " ") {
		return t, true, fmt.Errorf("multiple spaces after cat field")
	}

	// Field 5: (acct:: N)
	if !strings.HasPrefix(rest, "(acct:: ") {
		return t, true, fmt.Errorf("expected acct field, got %q", rest)
	}
	rest = rest[len("(acct:: "):]
	acctEnd := strings.IndexByte(rest, ')')
	if acctEnd == -1 {
		return t, true, fmt.Errorf("unclosed acct field")
	}
	acctVal := rest[:acctEnd]
	rest = rest[acctEnd+1:]

	if !acctRegex.MatchString(acctVal) {
		return t, true, fmt.Errorf("invalid account: %q", acctVal)
	}
	t.Account = acctVal

	if len(rest) == 0 {
		return t, true, nil
	}

	if !strings.HasPrefix(rest, " ") {
		return t, true, fmt.Errorf("expected space after acct field")
	}
	rest = rest[1:]

	// Optional Field 6: (receipt:: [[R]])
	if strings.HasPrefix(rest, "(receipt:: ") {
		if !strings.HasPrefix(rest, "(receipt:: [[") {
			return t, true, fmt.Errorf("invalid receipt format (want [[...]]): %q", rest)
		}
		receiptEnd := strings.Index(rest, "]])")
		if receiptEnd == -1 {
			return t, true, fmt.Errorf("unclosed receipt field: %q", rest)
		}
		rVal := rest[len("(receipt:: [["):receiptEnd]
		if len(rVal) == 0 {
			return t, true, fmt.Errorf("empty receipt in (receipt:: [[]])")
		}
		if strings.ContainsAny(rVal, "[]|\r\n") {
			return t, true, fmt.Errorf("invalid receipt name: %q", rVal)
		}
		t.Receipt = rVal
		rest = rest[receiptEnd+len("]])"):]

		if len(rest) == 0 {
			return t, true, nil
		}
		if !strings.HasPrefix(rest, " ") {
			return t, true, fmt.Errorf("expected space after receipt field")
		}
		rest = rest[1:]
	}

	desc := strings.TrimSpace(rest)
	if inlineFieldRegex.MatchString(desc) {
		return t, true, fmt.Errorf("unknown field in desc: %q", desc)
	}
	t.Desc = desc
	return t, true, nil
}

// parseCents parses a validated two-decimal amount string into integer cents without using non-integer arithmetic.
func parseCents(amtStr string) (int64, error) {
	neg := strings.HasPrefix(amtStr, "-")
	s := strings.TrimPrefix(amtStr, "-")
	parts := strings.Split(s, ".")
	if len(parts) != 2 || len(parts[1]) != 2 {
		return 0, fmt.Errorf("malformed amount string: %q", amtStr)
	}
	dollars, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid amount %q: %w", amtStr, err)
	}
	centsPart, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid amount %q: %w", amtStr, err)
	}
	if dollars > (math.MaxInt64-centsPart)/100 {
		return 0, fmt.Errorf("amount %q overflows int64", amtStr)
	}
	cents := dollars*100 + centsPart
	if neg {
		cents = -cents
	}
	return cents, nil
}

// Load walks <root>/ledger/**/*.md (skipping <root>/ledger/receipts/ and any path
// element starting with "."), parses every file, and returns the valid transactions
// (in path, then line order), every issue (same order), and the number of files read.
// A missing ledger/ directory is not an error: (nil, nil, 0, nil).
func Load(root string) (txns []Txn, issues []Issue, files int, err error) {
	ledgerDir := filepath.Join(root, "ledger")
	info, statErr := os.Stat(ledgerDir)
	if os.IsNotExist(statErr) {
		return nil, nil, 0, nil
	}
	if statErr != nil {
		return nil, nil, 0, statErr
	}
	if !info.IsDir() {
		return nil, nil, 0, nil
	}

	var mdFiles []string
	walkErr := filepath.WalkDir(ledgerDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := d.Name()
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		relSlash := filepath.ToSlash(rel)

		if d.IsDir() {
			if strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			if relSlash == "ledger/receipts" || strings.HasPrefix(relSlash, "ledger/receipts/") {
				return filepath.SkipDir
			}
			return nil
		}

		if strings.HasPrefix(name, ".") {
			return nil
		}
		if !strings.HasSuffix(name, ".md") {
			return nil
		}
		mdFiles = append(mdFiles, relSlash)
		return nil
	})
	if walkErr != nil {
		return nil, nil, 0, walkErr
	}

	sort.Strings(mdFiles)
	files = len(mdFiles)

	type seenLoc struct {
		Path string
		Line int
	}
	seenIDs := make(map[string]seenLoc)

	for _, relPath := range mdFiles {
		parts := strings.Split(relPath, "/")
		isDayFile := false
		var fileDate string
		if len(parts) == 4 && parts[0] == "ledger" {
			yearStr := parts[1]
			monthStr := parts[2]
			fileName := parts[3]
			if strings.HasSuffix(fileName, ".md") {
				dateStr := strings.TrimSuffix(fileName, ".md")
				if dayFileRegex.MatchString(dateStr) {
					dateParts := strings.Split(dateStr, "-")
					if dateParts[0] == yearStr && dateParts[1] == monthStr {
						if _, parseErr := time.Parse("2006-01-02", dateStr); parseErr == nil {
							isDayFile = true
							fileDate = dateStr
						}
					}
				}
			}
		}

		if !isDayFile {
			issues = append(issues, Issue{
				Path: relPath,
				Line: 0,
				Msg:  "not a ledger day file (want ledger/YYYY/MM/YYYY-MM-DD.md)",
			})
		}

		fullPath := filepath.Join(root, filepath.FromSlash(relPath))
		data, readErr := os.ReadFile(fullPath)
		if readErr != nil {
			return nil, nil, 0, readErr
		}

		lines := strings.Split(string(data), "\n")
		for idx, lineStr := range lines {
			lineNum := idx + 1
			parsedTxn, isTxnLine, pErr := ParseLine(lineStr)
			if !isTxnLine {
				continue
			}
			if pErr != nil {
				issues = append(issues, Issue{
					Path: relPath,
					Line: lineNum,
					Msg:  "bad line: " + pErr.Error(),
				})
				continue
			}

			parsedTxn.Path = relPath
			parsedTxn.Line = lineNum

			if isDayFile {
				idDate := parsedTxn.ID[:4] + "-" + parsedTxn.ID[4:6] + "-" + parsedTxn.ID[6:8]
				if idDate != fileDate {
					issues = append(issues, Issue{
						Path: relPath,
						Line: lineNum,
						Msg:  fmt.Sprintf("id date %s is not the file date %s", idDate, fileDate),
					})
				}
			}

			if first, seen := seenIDs[parsedTxn.ID]; seen {
				issues = append(issues, Issue{
					Path: relPath,
					Line: lineNum,
					Msg:  fmt.Sprintf("duplicate id %s (first at %s:%d)", parsedTxn.ID, first.Path, first.Line),
				})
			} else {
				seenIDs[parsedTxn.ID] = seenLoc{Path: relPath, Line: lineNum}
			}

			txns = append(txns, parsedTxn)
		}
	}

	return txns, issues, files, nil
}

// Total is one (currency, category) sum.
type Total struct {
	Currency string
	Category string
	Cents    int64
	Count    int
}

// Sum totals txns whose ID date falls in month ("YYYY-MM"; "" = every month),
// sorted by Currency, then Category.
func Sum(txns []Txn, month string) []Total {
	type key struct {
		cur string
		cat string
	}
	totalsMap := make(map[key]*Total)
	var keys []key

	for _, t := range txns {
		if month != "" {
			if len(t.ID) < 6 {
				continue
			}
			idMonth := t.ID[:4] + "-" + t.ID[4:6]
			if idMonth != month {
				continue
			}
		}
		k := key{cur: t.Currency, cat: t.Category}
		tot, exists := totalsMap[k]
		if !exists {
			tot = &Total{
				Currency: t.Currency,
				Category: t.Category,
			}
			totalsMap[k] = tot
			keys = append(keys, k)
		}
		tot.Cents += t.Cents
		tot.Count++
	}

	sort.Slice(keys, func(i, j int) bool {
		if keys[i].cur != keys[j].cur {
			return keys[i].cur < keys[j].cur
		}
		return keys[i].cat < keys[j].cat
	})

	res := make([]Total, 0, len(keys))
	for _, k := range keys {
		res = append(res, *totalsMap[k])
	}
	return res
}

// FormatCents renders cents as a fixed two-decimal string: -4500 -> "-45.00", 5 -> "0.05", -5 -> "-0.05".
func FormatCents(c int64) string {
	sign := ""
	var u uint64
	if c < 0 {
		sign = "-"
		u = uint64(-(c + 1)) + 1
	} else {
		u = uint64(c)
	}
	dollars := u / 100
	centsRemainder := u % 100
	return fmt.Sprintf("%s%d.%02d", sign, dollars, centsRemainder)
}
