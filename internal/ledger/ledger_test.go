package ledger

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestParseLine validates grammar-v1 line parsing (074 ledger leak prevention).
func TestParseLine(t *testing.T) {
	// 1. docs/mobile.md example line: parses every field exactly
	exLine := "- (id:: 20261031-1230-a) (amt:: -45.00) (cur:: HKD) (cat:: food) (acct:: cash) (receipt:: [[r-20261031-1230.jpg]]) lunch"
	txn, isTxn, err := ParseLine(exLine)
	if err != nil {
		t.Fatalf("unexpected error parsing docs/mobile.md example: %v", err)
	}
	if !isTxn {
		t.Fatalf("expected isTxn=true for docs/mobile.md example")
	}
	want := Txn{
		ID:       "20261031-1230-a",
		Cents:    -4500,
		Currency: "HKD",
		Category: "food",
		Account:  "cash",
		Receipt:  "r-20261031-1230.jpg",
		Desc:     "lunch",
	}
	if txn != want {
		t.Fatalf("ParseLine mismatch:\ngot:  %+v\nwant: %+v", txn, want)
	}

	// 2. Same line without receipt and with empty desc
	noReceiptEmptyDesc := "- (id:: 20261031-1230-a) (amt:: -45.00) (cur:: HKD) (cat:: food) (acct:: cash)"
	txn, isTxn, err = ParseLine(noReceiptEmptyDesc)
	if err != nil {
		t.Fatalf("unexpected error parsing line without receipt/desc: %v", err)
	}
	if !isTxn {
		t.Fatalf("expected isTxn=true")
	}
	wantNoReceipt := Txn{
		ID:       "20261031-1230-a",
		Cents:    -4500,
		Currency: "HKD",
		Category: "food",
		Account:  "cash",
		Receipt:  "",
		Desc:     "",
	}
	if txn != wantNoReceipt {
		t.Fatalf("ParseLine mismatch without receipt/desc:\ngot:  %+v\nwant: %+v", txn, wantNoReceipt)
	}

	// 3. Leading two spaces OK
	leadingSpaces := "  - (id:: 20261031-1230-a) (amt:: -45.00) (cur:: HKD) (cat:: food) (acct:: cash)"
	txn, isTxn, err = ParseLine(leadingSpaces)
	if err != nil || !isTxn || txn != wantNoReceipt {
		t.Fatalf("expected leading spaces OK: isTxn=%v err=%v txn=%+v", isTxn, err, txn)
	}

	// 4. Non-transaction lines: isTxn == false, err == nil
	nonTxnLines := []string{
		"# heading",
		"- plain bullet",
		"",
		"   ",
		"arbitrary note text",
		"- (id: 20261031-1230-a) (amt:: -45.00)", // single colon
	}
	for _, l := range nonTxnLines {
		_, isTxn, err := ParseLine(l)
		if isTxn {
			t.Errorf("expected isTxn=false for non-txn line %q", l)
		}
		if err != nil {
			t.Errorf("expected err=nil for non-txn line %q, got: %v", l, err)
		}
	}

	// 5. Transaction lines rejected for specific errors (isTxn == true, err != nil)
	rejectedLines := []struct {
		name string
		line string
	}{
		{"amt 1.5", "- (id:: 20261031-1230-a) (amt:: 1.5) (cur:: HKD) (cat:: food) (acct:: cash)"},
		{"amt +1.00", "- (id:: 20261031-1230-a) (amt:: +1.00) (cur:: HKD) (cat:: food) (acct:: cash)"},
		{"amt 1,000.00", "- (id:: 20261031-1230-a) (amt:: 1,000.00) (cur:: HKD) (cat:: food) (acct:: cash)"},
		{"cur hkd", "- (id:: 20261031-1230-a) (amt:: -45.00) (cur:: hkd) (cat:: food) (acct:: cash)"},
		{"id month 13", "- (id:: 20261331-1230-a) (amt:: -45.00) (cur:: HKD) (cat:: food) (acct:: cash)"},
		{"id 2460", "- (id:: 20261031-2460-a) (amt:: -45.00) (cur:: HKD) (cat:: food) (acct:: cash)"},
		{"keys out of order", "- (id:: 20261031-1230-a) (cur:: HKD) (amt:: -45.00) (cat:: food) (acct:: cash)"},
		{"missing acct", "- (id:: 20261031-1230-a) (amt:: -45.00) (cur:: HKD) (cat:: food)"},
		{"empty receipt", "- (id:: 20261031-1230-a) (amt:: -45.00) (cur:: HKD) (cat:: food) (acct:: cash) (receipt:: [[]])"},
		{"unknown field tag", "- (id:: 20261031-1230-a) (amt:: -45.00) (cur:: HKD) (cat:: food) (acct:: cash) (tag:: x)"},
	}
	for _, tc := range rejectedLines {
		t.Run(tc.name, func(t *testing.T) {
			_, isTxn, err := ParseLine(tc.line)
			if !isTxn {
				t.Errorf("expected isTxn=true for transaction line %q", tc.line)
			}
			if err == nil {
				t.Errorf("expected error for rejected line %q, got nil", tc.line)
			}
		})
	}

	// 6. Edge cases: CRLF, desc containing "::", UTF-8 BOM
	// 6a. CRLF line endings
	crlfLine := "- (id:: 20261031-1230-a) (amt:: -45.00) (cur:: HKD) (cat:: food) (acct:: cash)\r"
	txn, isTxn, err = ParseLine(crlfLine)
	if err != nil || !isTxn || txn != wantNoReceipt {
		t.Fatalf("CRLF line failed: isTxn=%v err=%v txn=%+v", isTxn, err, txn)
	}

	// 6b. Desc containing "::"
	descColonLine := "- (id:: 20261031-1230-a) (amt:: -45.00) (cur:: HKD) (cat:: food) (acct:: cash) lunch :: with alice"
	txn, isTxn, err = ParseLine(descColonLine)
	if err != nil || !isTxn {
		t.Fatalf("desc containing '::' failed: isTxn=%v err=%v", isTxn, err)
	}
	if txn.Desc != "lunch :: with alice" {
		t.Fatalf("expected desc 'lunch :: with alice', got %q", txn.Desc)
	}

	// 6c. UTF-8 BOM at start
	bomLine := "\ufeff- (id:: 20261031-1230-a) (amt:: -45.00) (cur:: HKD) (cat:: food) (acct:: cash)"
	txn, isTxn, err = ParseLine(bomLine)
	if err != nil || !isTxn || txn != wantNoReceipt {
		t.Fatalf("BOM line failed: isTxn=%v err=%v txn=%+v", isTxn, err, txn)
	}
}

// TestParseLineEdgeCases exercises grammar-v1 value edge cases and constraints (074).
func TestParseLineEdgeCases(t *testing.T) {
	// 1. Amount edge cases
	validAmts := []struct {
		amtStr string
		cents  int64
	}{
		{"0.00", 0},
		{"-0.00", 0},
		{"0.05", 5},
		{"-0.05", -5},
		{"1234567.89", 123456789},
		{"-1234567.89", -123456789},
	}
	for _, tc := range validAmts {
		line := "- (id:: 20261031-1230-a) (amt:: " + tc.amtStr + ") (cur:: HKD) (cat:: food) (acct:: cash)"
		txn, isTxn, err := ParseLine(line)
		if err != nil || !isTxn || txn.Cents != tc.cents {
			t.Errorf("amt %s: got isTxn=%v err=%v cents=%d, want %d", tc.amtStr, isTxn, err, txn.Cents, tc.cents)
		}
	}

	invalidAmts := []string{
		".50", "1.", "1.000", "+1.00", "1,000.00", "-1,000.00", "abc", "", "-", "--1.00", "1.-00",
	}
	for _, bad := range invalidAmts {
		line := "- (id:: 20261031-1230-a) (amt:: " + bad + ") (cur:: HKD) (cat:: food) (acct:: cash)"
		_, isTxn, err := ParseLine(line)
		if !isTxn || err == nil {
			t.Errorf("expected rejection for amt %q: isTxn=%v err=%v", bad, isTxn, err)
		}
	}

	// 2. ID validation
	// Leap year valid: 2024 is a leap year
	leapLine := "- (id:: 20240229-1200-a) (amt:: -10.00) (cur:: HKD) (cat:: food) (acct:: cash)"
	if _, isTxn, err := ParseLine(leapLine); !isTxn || err != nil {
		t.Errorf("leap year date should be valid: isTxn=%v err=%v", isTxn, err)
	}
	// Leap year invalid: 2026 is not a leap year
	nonLeapLine := "- (id:: 20260229-1200-a) (amt:: -10.00) (cur:: HKD) (cat:: food) (acct:: cash)"
	if _, isTxn, err := ParseLine(nonLeapLine); !isTxn || err == nil {
		t.Errorf("non-leap year Feb 29 should be rejected: isTxn=%v err=%v", isTxn, err)
	}
	// Bad month/day/hour/minute
	badIDs := []string{
		"20260431-1200-a", // April has 30 days
		"20260001-1200-a", // Month 00
		"20260100-1200-a", // Day 00
		"20261031-2400-a", // Hour 24
		"20261031-1260-a", // Minute 60
		"20261031-1230-A", // Uppercase suffix
		"20261031-1230-1", // Numeric suffix
		"20261031-1230-",  // Missing suffix
		"20261031-1230",   // No suffix
	}
	for _, badID := range badIDs {
		line := "- (id:: " + badID + ") (amt:: -10.00) (cur:: HKD) (cat:: food) (acct:: cash)"
		_, isTxn, err := ParseLine(line)
		if !isTxn || err == nil {
			t.Errorf("expected rejection for id %q: isTxn=%v err=%v", badID, isTxn, err)
		}
	}

	// 3. Category & Account slug rules: ^[a-z0-9][a-z0-9-]*$
	validSlugs := []string{"food", "c1", "eating-out", "a-b-c"}
	for _, slug := range validSlugs {
		line := "- (id:: 20261031-1230-a) (amt:: -10.00) (cur:: HKD) (cat:: " + slug + ") (acct:: " + slug + ")"
		if _, isTxn, err := ParseLine(line); !isTxn || err != nil {
			t.Errorf("slug %q should be valid: isTxn=%v err=%v", slug, isTxn, err)
		}
	}
	invalidSlugs := []string{"-food", "Food", "food_bar", "food!", "", "food/bar"}
	for _, badSlug := range invalidSlugs {
		catLine := "- (id:: 20261031-1230-a) (amt:: -10.00) (cur:: HKD) (cat:: " + badSlug + ") (acct:: cash)"
		if _, isTxn, err := ParseLine(catLine); !isTxn || err == nil {
			t.Errorf("expected rejection for category %q", badSlug)
		}
		acctLine := "- (id:: 20261031-1230-a) (amt:: -10.00) (cur:: HKD) (cat:: food) (acct:: " + badSlug + ")"
		if _, isTxn, err := ParseLine(acctLine); !isTxn || err == nil {
			t.Errorf("expected rejection for account %q", badSlug)
		}
	}

	// 4. Receipt validation
	validReceipts := []string{
		"r-20261031-1230.jpg",
		"receipts/2026/10/r-1.png",
		"r_test.jpeg",
	}
	for _, r := range validReceipts {
		line := "- (id:: 20261031-1230-a) (amt:: -10.00) (cur:: HKD) (cat:: food) (acct:: cash) (receipt:: [[" + r + "]])"
		txn, isTxn, err := ParseLine(line)
		if err != nil || !isTxn || txn.Receipt != r {
			t.Errorf("receipt %q should be valid: isTxn=%v err=%v receipt=%q", r, isTxn, err, txn.Receipt)
		}
	}
	invalidReceipts := []string{
		"[[]]",
		"[[r|desc]]",
		"[[r[1].jpg]]",
		"[[r\n.jpg]]",
		"bare-file.jpg", // without [[...]]
	}
	for _, badR := range invalidReceipts {
		line := "- (id:: 20261031-1230-a) (amt:: -10.00) (cur:: HKD) (cat:: food) (acct:: cash) (receipt:: " + badR + ")"
		if _, isTxn, err := ParseLine(line); !isTxn || err == nil {
			t.Errorf("expected rejection for receipt %q", badR)
		}
	}

	// 5. Desc with parentheses vs unknown inline fields
	// Free text with parens is OK as long as it's not a field
	descWithParens := "- (id:: 20261031-1230-a) (amt:: -10.00) (cur:: HKD) (cat:: food) (acct:: cash) lunch (with alice)"
	txn, isTxn, err := ParseLine(descWithParens)
	if err != nil || !isTxn || txn.Desc != "lunch (with alice)" {
		t.Errorf("desc with parens failed: isTxn=%v err=%v desc=%q", isTxn, err, txn.Desc)
	}

	// Desc containing unknown inline field is rejected
	descWithInlineField := "- (id:: 20261031-1230-a) (amt:: -10.00) (cur:: HKD) (cat:: food) (acct:: cash) lunch (note:: test)"
	if _, isTxn, err := ParseLine(descWithInlineField); !isTxn || err == nil {
		t.Errorf("expected rejection for desc with inline field")
	}

	// 6. Multiple spaces between fields
	doubleSpaceLine := "- (id:: 20261031-1230-a)  (amt:: -10.00) (cur:: HKD) (cat:: food) (acct:: cash)"
	if _, isTxn, err := ParseLine(doubleSpaceLine); !isTxn || err == nil {
		t.Errorf("expected rejection for double space between id and amt")
	}

	// 7. Tab indentation
	tabLine := "\t- (id:: 20261031-1230-a) (amt:: -10.00) (cur:: HKD) (cat:: food) (acct:: cash)"
	if _, isTxn, err := ParseLine(tabLine); !isTxn || err != nil {
		t.Errorf("tab indented line failed: isTxn=%v err=%v", isTxn, err)
	}
}

// TestFormatCents asserts exact rendering of int64 cents as fixed two-decimal strings (074).
func TestFormatCents(t *testing.T) {
	cases := []struct {
		cents int64
		want  string
	}{
		{-4500, "-45.00"},
		{5, "0.05"},
		{-5, "-0.05"},
		{0, "0.00"},
		{123456789, "1234567.89"},
		{1, "0.01"},
		{-1, "-0.01"},
		{99, "0.99"},
		{-99, "-0.99"},
		{100, "1.00"},
		{-100, "-1.00"},
		{math.MaxInt64, "92233720368547758.07"},
		{math.MinInt64, "-92233720368547758.08"},
	}
	for _, tc := range cases {
		got := FormatCents(tc.cents)
		if got != tc.want {
			t.Errorf("FormatCents(%d) = %q, want %q", tc.cents, got, tc.want)
		}
	}
}

// TestLoad validates walking <root>/ledger/**/*.md, skipping ignored paths,
// deterministic ordering, issue generation, and missing ledger directory handling (074).
func TestLoad(t *testing.T) {
	vault := t.TempDir()

	// Missing ledger/ -> (nil, nil, 0, nil)
	txns, issues, files, err := Load(vault)
	if err != nil {
		t.Fatalf("unexpected error on missing ledger/: %v", err)
	}
	if txns != nil || issues != nil || files != 0 {
		t.Fatalf("expected (nil, nil, 0, nil) on missing ledger/, got txns=%v issues=%v files=%d", txns, issues, files)
	}

	// Create fixture:
	// Two day files:
	// 1. ledger/2026/10/2026-10-30.md (clean)
	// 2. ledger/2026/10/2026-10-31.md (id-date mismatch, duplicate, bad line, valid line)
	// One non-day file:
	// 3. ledger/notes.md (outside layout Line 0, contains one valid line)
	// Ignored paths:
	// 4. ledger/receipts/x.md (ignored)
	// 5. ledger/.trash/y.md (ignored)

	day1Dir := filepath.Join(vault, "ledger", "2026", "10")
	if err := os.MkdirAll(day1Dir, 0755); err != nil {
		t.Fatal(err)
	}
	day1Path := filepath.Join(day1Dir, "2026-10-30.md")
	day1Content := "- (id:: 20261030-1000-a) (amt:: -10.00) (cur:: USD) (cat:: coffee) (acct:: cash) morning coffee\n"
	if err := os.WriteFile(day1Path, []byte(day1Content), 0644); err != nil {
		t.Fatal(err)
	}

	day2Path := filepath.Join(day1Dir, "2026-10-31.md")
	day2Content := strings.Join([]string{
		"- (id:: 20261030-1200-a) (amt:: -20.00) (cur:: USD) (cat:: supplies) (acct:: visa) wrong date",
		"- (id:: 20261031-1100-a) (amt:: -15.00) (cur:: USD) (cat:: food) (acct:: cash) original",
		"- (id:: 20261031-1100-a) (amt:: -15.00) (cur:: USD) (cat:: food) (acct:: cash) duplicate",
		"- (id:: 20261031-1500-a) (amt:: 1.5) (cur:: USD) (cat:: food) (acct:: cash) bad amt line",
		"- (id:: 20261031-1600-a) (amt:: -30.00) (cur:: USD) (cat:: food) (acct:: cash) valid line",
	}, "\n") + "\n"
	if err := os.WriteFile(day2Path, []byte(day2Content), 0644); err != nil {
		t.Fatal(err)
	}

	notesPath := filepath.Join(vault, "ledger", "notes.md")
	notesContent := "- (id:: 20261029-0900-a) (amt:: -5.00) (cur:: USD) (cat:: books) (acct:: cash) valid line in notes\n"
	if err := os.WriteFile(notesPath, []byte(notesContent), 0644); err != nil {
		t.Fatal(err)
	}

	receiptsDir := filepath.Join(vault, "ledger", "receipts")
	if err := os.MkdirAll(receiptsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(receiptsDir, "x.md"), []byte("- (id:: 20261031-1000-a) (amt:: -1.00) (cur:: USD) (cat:: x) (acct:: cash)\n"), 0644); err != nil {
		t.Fatal(err)
	}

	trashDir := filepath.Join(vault, "ledger", ".trash")
	if err := os.MkdirAll(trashDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trashDir, "y.md"), []byte("- (id:: 20261031-1001-a) (amt:: -1.00) (cur:: USD) (cat:: y) (acct:: cash)\n"), 0644); err != nil {
		t.Fatal(err)
	}

	txns, issues, files, err = Load(vault)
	if err != nil {
		t.Fatalf("unexpected Load error: %v", err)
	}

	if files != 3 {
		t.Fatalf("expected files=3, got %d", files)
	}

	// Exact issue list in (path, line) order:
	// 1. ledger/2026/10/2026-10-31.md:1 -> id-date mismatch
	// 2. ledger/2026/10/2026-10-31.md:3 -> duplicate id
	// 3. ledger/2026/10/2026-10-31.md:4 -> bad line
	// 4. ledger/notes.md:0 -> not a ledger day file
	if len(issues) != 4 {
		t.Fatalf("expected 4 issues, got %d: %+v", len(issues), issues)
	}

	// Issue 1: id-date
	if issues[0].Path != "ledger/2026/10/2026-10-31.md" || issues[0].Line != 1 ||
		issues[0].Msg != "id date 2026-10-30 is not the file date 2026-10-31" {
		t.Errorf("issue 0 mismatch: %+v", issues[0])
	}

	// Issue 2: duplicate
	if issues[1].Path != "ledger/2026/10/2026-10-31.md" || issues[1].Line != 3 ||
		issues[1].Msg != "duplicate id 20261031-1100-a (first at ledger/2026/10/2026-10-31.md:2)" {
		t.Errorf("issue 1 mismatch: %+v", issues[1])
	}

	// Issue 3: bad line
	if issues[2].Path != "ledger/2026/10/2026-10-31.md" || issues[2].Line != 4 ||
		!strings.HasPrefix(issues[2].Msg, "bad line: ") {
		t.Errorf("issue 2 mismatch: %+v", issues[2])
	}

	// Issue 4: layout
	if issues[3].Path != "ledger/notes.md" || issues[3].Line != 0 ||
		issues[3].Msg != "not a ledger day file (want ledger/YYYY/MM/YYYY-MM-DD.md)" {
		t.Errorf("issue 3 mismatch: %+v", issues[3])
	}

	// Only the bad line is missing from txns:
	// day1: 1 txn
	// day2: 4 valid txns (wrong date + original + duplicate + valid line)
	// notes: 1 valid txn
	// total = 6 txns
	if len(txns) != 6 {
		t.Fatalf("expected 6 valid txns, got %d: %+v", len(txns), txns)
	}
	expectedIDs := []string{
		"20261030-1000-a",
		"20261030-1200-a",
		"20261031-1100-a",
		"20261031-1100-a",
		"20261031-1600-a",
		"20261029-0900-a",
	}
	for i, wantID := range expectedIDs {
		if txns[i].ID != wantID {
			t.Errorf("txn %d id mismatch: got %q want %q", i, txns[i].ID, wantID)
		}
	}
}

// TestLoadEdgeCases verifies CRLF line endings, UTF-8 BOM, non-md files, and triplicate duplicate detection (074).
func TestLoadEdgeCases(t *testing.T) {
	vault := t.TempDir()

	dayDir := filepath.Join(vault, "ledger", "2026", "10")
	if err := os.MkdirAll(dayDir, 0755); err != nil {
		t.Fatal(err)
	}

	// 1. CRLF line endings and UTF-8 BOM in a day file
	dayFile := filepath.Join(dayDir, "2026-10-31.md")
	content := "\ufeff- (id:: 20261031-1000-a) (amt:: -10.00) (cur:: HKD) (cat:: food) (acct:: cash)\r\n" +
		"- (id:: 20261031-1000-a) (amt:: -10.00) (cur:: HKD) (cat:: food) (acct:: cash) dup1\r\n" +
		"- (id:: 20261031-1000-a) (amt:: -10.00) (cur:: HKD) (cat:: food) (acct:: cash) dup2\r\n"
	if err := os.WriteFile(dayFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// 2. Non-md files should be ignored
	if err := os.WriteFile(filepath.Join(dayDir, "notes.txt"), []byte("random text"), 0644); err != nil {
		t.Fatal(err)
	}

	// 3. Subdirectories in receipts should be ignored
	receiptSub := filepath.Join(vault, "ledger", "receipts", "2026", "10")
	if err := os.MkdirAll(receiptSub, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(receiptSub, "r.md"), []byte("ignored"), 0644); err != nil {
		t.Fatal(err)
	}

	txns, issues, files, err := Load(vault)
	if err != nil {
		t.Fatalf("unexpected Load error: %v", err)
	}
	if files != 1 {
		t.Fatalf("expected 1 file read, got %d", files)
	}
	if len(txns) != 3 {
		t.Fatalf("expected 3 txns, got %d", len(txns))
	}
	if len(issues) != 2 {
		t.Fatalf("expected 2 duplicate issues, got %d: %+v", len(issues), issues)
	}

	// Both duplicates should point to line 1 as the first occurrence
	wantDup1 := "duplicate id 20261031-1000-a (first at ledger/2026/10/2026-10-31.md:1)"
	if issues[0].Line != 2 || issues[0].Msg != wantDup1 {
		t.Errorf("issue 0 mismatch: %+v", issues[0])
	}
	if issues[1].Line != 3 || issues[1].Msg != wantDup1 {
		t.Errorf("issue 1 mismatch: %+v", issues[1])
	}
}

// TestSum asserts aggregation by currency and category, month filtering, sorting, and counts (074).
func TestSum(t *testing.T) {
	txns := []Txn{
		{ID: "20261015-1000-a", Currency: "USD", Category: "food", Cents: -1500},
		{ID: "20261020-1100-a", Currency: "USD", Category: "coffee", Cents: -500},
		{ID: "20261025-1200-a", Currency: "USD", Category: "food", Cents: -2500},
		{ID: "20261026-1300-a", Currency: "HKD", Category: "transit", Cents: -1200},
		{ID: "20261101-0900-a", Currency: "USD", Category: "food", Cents: -3000},
		{ID: "20261102-1000-a", Currency: "HKD", Category: "transit", Cents: -800},
	}

	// 1. Without month filter (""): all txns
	allTotals := Sum(txns, "")
	wantAll := []Total{
		{Currency: "HKD", Category: "transit", Cents: -2000, Count: 2},
		{Currency: "USD", Category: "coffee", Cents: -500, Count: 1},
		{Currency: "USD", Category: "food", Cents: -7000, Count: 3},
	}
	if !reflect.DeepEqual(allTotals, wantAll) {
		t.Fatalf("Sum without month mismatch:\ngot:  %+v\nwant: %+v", allTotals, wantAll)
	}

	// 2. With month filter "2026-10"
	octTotals := Sum(txns, "2026-10")
	wantOct := []Total{
		{Currency: "HKD", Category: "transit", Cents: -1200, Count: 1},
		{Currency: "USD", Category: "coffee", Cents: -500, Count: 1},
		{Currency: "USD", Category: "food", Cents: -4000, Count: 2},
	}
	if !reflect.DeepEqual(octTotals, wantOct) {
		t.Fatalf("Sum with month 2026-10 mismatch:\ngot:  %+v\nwant: %+v", octTotals, wantOct)
	}

	// 3. With month filter "2026-11"
	novTotals := Sum(txns, "2026-11")
	wantNov := []Total{
		{Currency: "HKD", Category: "transit", Cents: -800, Count: 1},
		{Currency: "USD", Category: "food", Cents: -3000, Count: 1},
	}
	if !reflect.DeepEqual(novTotals, wantNov) {
		t.Fatalf("Sum with month 2026-11 mismatch:\ngot:  %+v\nwant: %+v", novTotals, wantNov)
	}

	// 4. Empty result
	emptyTotals := Sum(txns, "2026-12")
	if len(emptyTotals) != 0 {
		t.Fatalf("expected 0 totals for 2026-12, got: %+v", emptyTotals)
	}
}

// TestNoFloatInSources asserts that grep -n float internal/ledger/*.go is empty in non-test files (074).
func TestNoFloatInSources(t *testing.T) {
	matches, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range matches {
		if strings.HasSuffix(m, "_test.go") {
			continue
		}
		data, err := os.ReadFile(m)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(data), "\n")
		for lineNum, line := range lines {
			if strings.Contains(strings.ToLower(line), "float") {
				t.Errorf("%s:%d contains 'float': %s", m, lineNum+1, line)
			}
		}
	}
}
