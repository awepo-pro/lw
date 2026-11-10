package ledger

import (
	"strings"
	"testing"
)

// realFixture is the user's file, byte for byte (CJK written with \u escapes).
const realFixture = "Nov - \u5bb6\u7528 4000 \n\n2026-11-02, 44 (jollibee)\n2026-11-03, 25\n" +
	"2026-11-04, 23 + 45 (kfc) + 32 (\u8461\u64bb)\n2026-11-05, 29 + 30 + 49.5 (7\u4ed4)\n\n"

// TestLoose_L1_Real tests L1: REAL: 9 entries; entry 0 = {KindFixed, Month 2026-11, Cents -400000, Note and Category \u5bb6\u7528, Line 1};
// the day items in order -4400 jollibee, -2500, -2300, -4500 kfc, -3200, -2900, -3000, -4950; 0 issues.
func TestLoose_L1_Real(t *testing.T) {
	entries, issues, budgetCents, currency := ParseLoose(LooseFile, []byte(realFixture))
	if len(issues) != 0 {
		t.Fatalf("expected 0 issues, got %d: %+v", len(issues), issues)
	}
	if budgetCents != 0 {
		t.Errorf("expected budgetCents 0, got %d", budgetCents)
	}
	if currency != "" {
		t.Errorf("expected empty currency, got %q", currency)
	}
	if len(entries) != 9 {
		t.Fatalf("expected 9 entries, got %d", len(entries))
	}

	// Entry 0: KindFixed
	e0 := entries[0]
	if e0.Kind != KindFixed || e0.Month != "2026-11" || e0.Date != "" || e0.Cents != -400000 ||
		e0.Note != "\u5bb6\u7528" || e0.Category != "\u5bb6\u7528" || e0.Line != 1 || e0.Path != LooseFile {
		t.Errorf("entry 0 mismatch: %+v", e0)
	}

	expectedDayItems := []struct {
		cents int64
		note  string
		line  int
		date  string
	}{
		{-4400, "jollibee", 3, "2026-11-02"},
		{-2500, "", 4, "2026-11-03"},
		{-2300, "", 5, "2026-11-04"},
		{-4500, "kfc", 5, "2026-11-04"},
		{-3200, "\u8461\u64bb", 5, "2026-11-04"},
		{-2900, "", 6, "2026-11-05"},
		{-3000, "", 6, "2026-11-05"},
		{-4950, "7\u4ed4", 6, "2026-11-05"},
	}

	for i, exp := range expectedDayItems {
		e := entries[i+1]
		if e.Kind != KindDay || e.Cents != exp.cents || e.Note != exp.note || e.Line != exp.line || e.Date != exp.date || e.Month != "2026-11" {
			t.Errorf("entry %d mismatch: got %+v, want cents=%d note=%q line=%d date=%q", i+1, e, exp.cents, exp.note, exp.line, exp.date)
		}
	}
}

// TestLoose_L2_MonthLineYear tests L2: the year of a month line: (a) taken from the day line below;
// (b) from above when none below; (c) issue when the file has no day line;
// (d) Nov 2025 - rent 5000 and 2025-11 - rent 5000 give Month 2025-11;
// (e) 11\u6708 - rent 5000 with a 2026 day line gives 2026-11.
func TestLoose_L2_MonthLineYear(t *testing.T) {
	// (a) from day line below
	a := "Nov - rent 5000\n2026-11-02, 10\n"
	entries, issues, _, _ := ParseLoose(LooseFile, []byte(a))
	if len(issues) != 0 || len(entries) != 2 || entries[0].Month != "2026-11" {
		t.Errorf("(a) failed: issues=%v entries=%+v", issues, entries)
	}

	// (b) from above when none below
	b := "2026-11-02, 10\nNov - rent 5000\n"
	entries, issues, _, _ = ParseLoose(LooseFile, []byte(b))
	if len(issues) != 0 || len(entries) != 2 || entries[1].Month != "2026-11" {
		t.Errorf("(b) failed: issues=%v entries=%+v", issues, entries)
	}

	// (c) issue when file has no day line
	c := "Nov - rent 5000\n"
	entries, issues, _, _ = ParseLoose(LooseFile, []byte(c))
	if len(entries) != 0 || len(issues) != 1 || issues[0].Msg != "month line needs a year, e.g. \"Nov 2026 - rent 5000\"" {
		t.Errorf("(c) failed: issues=%v entries=%+v", issues, entries)
	}

	// (d) Nov 2025 - rent 5000 and 2025-11 - rent 5000 give Month 2025-11
	d := "Nov 2025 - rent 5000\n2025-11 - rent 5000\n"
	entries, issues, _, _ = ParseLoose(LooseFile, []byte(d))
	if len(issues) != 0 || len(entries) != 2 || entries[0].Month != "2025-11" || entries[1].Month != "2025-11" {
		t.Errorf("(d) failed: issues=%v entries=%+v", issues, entries)
	}

	// (e) 11\u6708 - rent 5000 with a 2026 day line gives 2026-11
	e := "11\u6708 - rent 5000\n2026-11-02, 10\n"
	entries, issues, _, _ = ParseLoose(LooseFile, []byte(e))
	if len(issues) != 0 || len(entries) != 2 || entries[0].Month != "2026-11" {
		t.Errorf("(e) failed: issues=%v entries=%+v", issues, entries)
	}
}

// TestLoose_L3_MoneyIn tests L3: money in: 2026-11-06, -500 (refund) -> Cents 50000;
// Nov + salary 20000 -> KindFixed Cents 2000000.
func TestLoose_L3_MoneyIn(t *testing.T) {
	input := "Nov + salary 20000\n2026-11-06, -500 (refund)\n"
	entries, issues, _, _ := ParseLoose(LooseFile, []byte(input))
	if len(issues) != 0 {
		t.Fatalf("expected 0 issues, got %+v", issues)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Kind != KindFixed || entries[0].Cents != 2000000 || entries[0].Note != "salary" {
		t.Errorf("entry 0 mismatch: %+v", entries[0])
	}
	if entries[1].Kind != KindDay || entries[1].Cents != 50000 || entries[1].Note != "refund" {
		t.Errorf("entry 1 mismatch: %+v", entries[1])
	}
}

// TestLoose_L4_AllOrNothing tests L4: all-or-nothing: 2026-11-06, 12 + abc -> 0 entries, 1 issue naming abc;
// 2026-11-06 and 2026-11-06, -> issue no amount after the date; 2026-13-40, 5 -> invalid date.
func TestLoose_L4_AllOrNothing(t *testing.T) {
	// 2026-11-06, 12 + abc
	input1 := "2026-11-06, 12 + abc\n"
	entries, issues, _, _ := ParseLoose(LooseFile, []byte(input1))
	if len(entries) != 0 || len(issues) != 1 || !strings.Contains(issues[0].Msg, "abc") ||
		issues[0].Msg != "bad amount \"abc\" (want e.g. 44, 49.5 (note), 45 (kfc) #food)" {
		t.Errorf("input1 failed: entries=%v issues=%v", entries, issues)
	}

	// 2026-11-06
	input2 := "2026-11-06\n"
	entries, issues, _, _ = ParseLoose(LooseFile, []byte(input2))
	if len(entries) != 0 || len(issues) != 1 || issues[0].Msg != "no amount after the date" {
		t.Errorf("input2 failed: entries=%v issues=%v", entries, issues)
	}

	// 2026-11-06,
	input3 := "2026-11-06,\n"
	entries, issues, _, _ = ParseLoose(LooseFile, []byte(input3))
	if len(entries) != 0 || len(issues) != 1 || issues[0].Msg != "no amount after the date" {
		t.Errorf("input3 failed: entries=%v issues=%v", entries, issues)
	}

	// 2026-13-40, 5
	input4 := "2026-13-40, 5\n"
	entries, issues, _, _ = ParseLoose(LooseFile, []byte(input4))
	if len(entries) != 0 || len(issues) != 1 || issues[0].Msg != "invalid date \"2026-13-40\"" {
		t.Errorf("input4 failed: entries=%v issues=%v", entries, issues)
	}
}

// TestLoose_L5_PlusInParens tests L5: + inside parentheses does not split:
// 2026-11-06, 30 (a + b) + 5 -> 2 items, notes a + b and "".
func TestLoose_L5_PlusInParens(t *testing.T) {
	input := "2026-11-06, 30 (a + b) + 5\n"
	entries, issues, _, _ := ParseLoose(LooseFile, []byte(input))
	if len(issues) != 0 {
		t.Fatalf("expected 0 issues, got %+v", issues)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Cents != -3000 || entries[0].Note != "a + b" {
		t.Errorf("entry 0 mismatch: %+v", entries[0])
	}
	if entries[1].Cents != -500 || entries[1].Note != "" {
		t.Errorf("entry 1 mismatch: %+v", entries[1])
	}
}

// TestLoose_L6_Categories tests L6: categories: with #food = jollibee, KFC, * and #transport = bus:
// 44 (Jollibee) food, 6 (bus 26) transport, 25 food (catch-all), 45 (kfc) #treat treat;
// a month line label rent stays rent (the catch-all is ignored for month lines);
// with no rule a bare item has Category "".
func TestLoose_L6_Categories(t *testing.T) {
	input := `#food = jollibee, KFC, *
#transport = bus
Nov - rent 5000
2026-11-02, 44 (Jollibee)
2026-11-03, 6 (bus 26)
2026-11-04, 25
2026-11-05, 45 (kfc) #treat
`
	entries, issues, _, _ := ParseLoose(LooseFile, []byte(input))
	if len(issues) != 0 {
		t.Fatalf("expected 0 issues, got %+v", issues)
	}
	if len(entries) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(entries))
	}
	// Nov - rent 5000 -> Note: "rent", Category: "rent"
	if entries[0].Category != "rent" || entries[0].Note != "rent" {
		t.Errorf("entry 0: category=%q note=%q", entries[0].Category, entries[0].Note)
	}
	// 44 (Jollibee) -> food
	if entries[1].Category != "food" {
		t.Errorf("entry 1: category=%q, want food", entries[1].Category)
	}
	// 6 (bus 26) -> transport
	if entries[2].Category != "transport" {
		t.Errorf("entry 2: category=%q, want transport", entries[2].Category)
	}
	// 25 -> food (catch-all)
	if entries[3].Category != "food" {
		t.Errorf("entry 3: category=%q, want food", entries[3].Category)
	}
	// 45 (kfc) #treat -> treat
	if entries[4].Category != "treat" || entries[4].Note != "kfc" {
		t.Errorf("entry 4: category=%q note=%q, want treat and kfc", entries[4].Category, entries[4].Note)
	}

	// with no rule a bare item has Category ""
	bareInput := "2026-11-06, 10\n"
	entries2, issues2, _, _ := ParseLoose(LooseFile, []byte(bareInput))
	if len(issues2) != 0 || len(entries2) != 1 || entries2[0].Category != "" {
		t.Errorf("bare item without rules failed: issues=%v entries=%+v", issues2, entries2)
	}
}

// TestLoose_L7_SkippedLines tests L7: skipped lines: frontmatter, a fenced block holding a line that
// looks like a day line, # Nov, ---, > quote, // c produce no entry and no issue;
// hello world produces the G9 issue with its line number.
func TestLoose_L7_SkippedLines(t *testing.T) {
	input := `---
author: test
---
` + "```" + `
2026-11-02, 10
` + "```" + `
# Nov
---
> quote
// c
<!-- comment -->
hello world
`
	entries, issues, _, _ := ParseLoose(LooseFile, []byte(input))
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d: %+v", len(issues), issues)
	}
	if issues[0].Line != 12 || issues[0].Msg != "not understood (want \"YYYY-MM-DD, 44 (note) + 12.5\" or \"Nov - rent 5000\")" {
		t.Errorf("issue mismatch: got %+v", issues[0])
	}
}

// TestLoose_L8_Directives tests L8: directives: currency = mop -> "MOP" on every loose entry;
// budget = 6000 -> 600000; currency = dollars and budget = x are issues.
func TestLoose_L8_Directives(t *testing.T) {
	input1 := `currency = mop
budget = 6000
2026-11-02, 10
`
	entries, issues, budgetCents, currency := ParseLoose(LooseFile, []byte(input1))
	if len(issues) != 0 {
		t.Fatalf("expected 0 issues, got %+v", issues)
	}
	if currency != "MOP" {
		t.Errorf("currency = %q, want MOP", currency)
	}
	if budgetCents != 600000 {
		t.Errorf("budgetCents = %d, want 600000", budgetCents)
	}
	if len(entries) != 1 || entries[0].Currency != "MOP" {
		t.Errorf("entry mismatch: %+v", entries)
	}

	input2 := `currency = dollars
budget = x
`
	_, issues2, _, _ := ParseLoose(LooseFile, []byte(input2))
	if len(issues2) != 2 {
		t.Fatalf("expected 2 issues, got %d: %+v", len(issues2), issues2)
	}
	if issues2[0].Msg != "bad currency \"dollars\"" {
		t.Errorf("issue 0 mismatch: %q", issues2[0].Msg)
	}
	if issues2[1].Msg != "bad budget \"x\"" {
		t.Errorf("issue 1 mismatch: %q", issues2[1].Msg)
	}
}

// TestLoose_L9_DuplicateDateWarning tests L9: duplicate date: the warning text above, Warn true,
// on the later line; both lines' entries present.
func TestLoose_L9_DuplicateDateWarning(t *testing.T) {
	input := `2026-11-04, 20
2026-11-04, 30
`
	entries, issues, _, _ := ParseLoose(LooseFile, []byte(input))
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d: %+v", len(issues), issues)
	}
	if !issues[0].Warn || issues[0].Line != 2 || issues[0].Msg != "date 2026-11-04 also on line 1" {
		t.Errorf("warning mismatch: %+v", issues[0])
	}
}

// TestLoose_L10_BulletsAndSeparators tests L10: bullets and separators:
// - 2026-11-06, 5, * 2026-11-06 5, 2026-11-06: 5, 2026-11-06,5(x) each give one entry of -500
// (the last with note x); $5 is accepted.
func TestLoose_L10_BulletsAndSeparators(t *testing.T) {
	input := `- 2026-11-06, 5
* 2026-11-06 5
2026-11-06: 5
2026-11-06,5(x)
2026-11-06, $5
`
	entries, issues, _, _ := ParseLoose(LooseFile, []byte(input))
	if len(issues) != 4 { // duplicate dates on lines 2, 3, 4, 5
		for _, iss := range issues {
			if !iss.Warn {
				t.Fatalf("unexpected non-warning issue: %+v", iss)
			}
		}
	}
	if len(entries) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(entries))
	}
	for i, e := range entries {
		if e.Cents != -500 {
			t.Errorf("entry %d cents mismatch: %d", i, e.Cents)
		}
		if i == 3 && e.Note != "x" {
			t.Errorf("entry 3 note mismatch: %q", e.Note)
		}
	}
}

// TestLoose_L11_CRLFAndBOM tests L11: CRLF line ends and a BOM give the same entries as L1.
func TestLoose_L11_CRLFAndBOM(t *testing.T) {
	crlf := "\ufeff" + strings.ReplaceAll(realFixture, "\n", "\r\n")
	entries, issues, budgetCents, currency := ParseLoose(LooseFile, []byte(crlf))
	if len(issues) != 0 {
		t.Fatalf("expected 0 issues, got %d: %+v", len(issues), issues)
	}
	if budgetCents != 0 || currency != "" {
		t.Errorf("unexpected budget=%d or currency=%q", budgetCents, currency)
	}
	if len(entries) != 9 {
		t.Fatalf("expected 9 entries, got %d", len(entries))
	}
	if entries[0].Note != "\u5bb6\u7528" || entries[0].Cents != -400000 {
		t.Errorf("entry 0 mismatch: %+v", entries[0])
	}
	if entries[8].Note != "7\u4ed4" || entries[8].Cents != -4950 {
		t.Errorf("entry 8 mismatch: %+v", entries[8])
	}
}
