package ledger

import (
	"strings"
	"testing"
)

func normalizeReport(s string) string {
	lines := strings.Split(s, "\n")
	var norm []string
	for _, l := range lines {
		l = strings.TrimRight(l, " \t\r")
		words := strings.Fields(l)
		norm = append(norm, strings.Join(words, " "))
	}
	for len(norm) > 0 && norm[len(norm)-1] == "" {
		norm = norm[:len(norm)-1]
	}
	return strings.Join(norm, "\n")
}

// TestReport_R1_Real tests R1: the REAL report:
// 2026-11
// spent 277.50 (8 item(s) on 4 day(s))
// fixed 4000.00 (\u5bb6\u7528 4000.00)
// money in 0.00
// net out 4277.50
// by category:
// \u5bb6\u7528 4000.00 (1)
// uncategorized 277.50 (8)
// average per recorded day 69.38
// no entry on 5 day(s): 06 07 08 09 10
func TestReport_R1_Real(t *testing.T) {
	entries, issues, budgetCents, currency := ParseLoose(LooseFile, []byte(realFixture))
	if len(issues) != 0 {
		t.Fatalf("unexpected issues: %+v", issues)
	}
	b := Book{
		Entries:     entries,
		BudgetCents: budgetCents,
		Currency:    currency,
		Files:       1,
	}

	got := Report(b, "2026-11", "2026-11-10")
	want := "2026-11\n" +
		"spent 277.50 (8 item(s) on 4 day(s))\n" +
		"fixed 4000.00 (\u5bb6\u7528 4000.00)\n" +
		"money in 0.00\n" +
		"net out 4277.50\n" +
		"by category:\n" +
		"\u5bb6\u7528 4000.00 (1)\n" +
		"uncategorized 277.50 (8)\n" +
		"average per recorded day 69.38\n" +
		"no entry on 5 day(s): 06 07 08 09 10"

	if normalizeReport(got) != normalizeReport(want) {
		t.Errorf("report mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestReport_R2_Budget tests R2: with budget = 300 the line budget 300.00: 22.50 left;
// with budget = 200 budget 200.00: 77.50 over.
func TestReport_R2_Budget(t *testing.T) {
	entries, _, _, _ := ParseLoose(LooseFile, []byte(realFixture))

	// budget = 300
	b1 := Book{
		Entries:     entries,
		BudgetCents: 30000,
		Currency:    "",
		Files:       1,
	}
	got1 := Report(b1, "2026-11", "2026-11-10")
	norm1 := normalizeReport(got1)
	if !strings.Contains(norm1, "budget 300.00: 22.50 left") {
		t.Errorf("expected 'budget 300.00: 22.50 left', got:\n%s", got1)
	}
	// Verify it is placed after net out
	lines1 := strings.Split(norm1, "\n")
	netOutIdx := -1
	budgetIdx := -1
	for i, l := range lines1 {
		if strings.HasPrefix(l, "net out") {
			netOutIdx = i
		}
		if strings.HasPrefix(l, "budget") {
			budgetIdx = i
		}
	}
	if netOutIdx == -1 || budgetIdx != netOutIdx+1 {
		t.Errorf("budget line not right after net out: netOut=%d budget=%d", netOutIdx, budgetIdx)
	}

	// budget = 200
	b2 := Book{
		Entries:     entries,
		BudgetCents: 20000,
		Currency:    "",
		Files:       1,
	}
	got2 := Report(b2, "2026-11", "2026-11-10")
	if !strings.Contains(normalizeReport(got2), "budget 200.00: 77.50 over") {
		t.Errorf("expected 'budget 200.00: 77.50 over', got:\n%s", got2)
	}
}

// TestReport_R3_PastMonthNoEntryDays tests R3: --today 2026-12-03, month 2026-11:
// the no-entry line lists 06 through 30.
func TestReport_R3_PastMonthNoEntryDays(t *testing.T) {
	entries, _, _, _ := ParseLoose(LooseFile, []byte(realFixture))
	b := Book{
		Entries: entries,
		Files:   1,
	}
	got := Report(b, "2026-11", "2026-12-03")
	wantLine := "no entry on 25 day(s): 06 07 08 09 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30"
	if !strings.Contains(normalizeReport(got), wantLine) {
		t.Errorf("expected line %q, got:\n%s", wantLine, got)
	}
}

// TestReport_R4_NoEntries tests R4: a month with no entries -> the two no entries lines.
func TestReport_R4_NoEntries(t *testing.T) {
	entries, _, _, _ := ParseLoose(LooseFile, []byte(realFixture))
	b := Book{
		Entries: entries,
		Files:   1,
	}
	// Month with no entries, but book has entries in 2026-11
	got := Report(b, "2026-12", "2026-12-03")
	want := "no entries in 2026-12\nlatest month with entries: 2026-11"
	if normalizeReport(got) != want {
		t.Errorf("expected:\n%s\ngot:\n%s", want, got)
	}

	// Book with no entries at all
	emptyBook := Book{}
	gotEmpty := Report(emptyBook, "2026-12", "2026-12-03")
	wantEmpty := "no entries in 2026-12"
	if normalizeReport(gotEmpty) != wantEmpty {
		t.Errorf("expected:\n%s\ngot:\n%s", wantEmpty, gotEmpty)
	}
}

// TestReport_R5_Currency tests R5: with currency = MOP line 1 is 2026-11 MOP.
func TestReport_R5_Currency(t *testing.T) {
	fixtureWithCur := "currency = MOP\n" + realFixture
	entries, issues, budgetCents, currency := ParseLoose(LooseFile, []byte(fixtureWithCur))
	if len(issues) != 0 {
		t.Fatalf("unexpected issues: %+v", issues)
	}
	b := Book{
		Entries:     entries,
		BudgetCents: budgetCents,
		Currency:    currency,
		Files:       1,
	}
	got := Report(b, "2026-11", "2026-11-10")
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "2026-11 MOP" {
		t.Errorf("line 1 mismatch: got %q, want '2026-11 MOP'", lines[0])
	}
}

// TestReport_R6_MultipleCurrencies tests R6: a v1 day file entry in HKD (same month) plus REAL:
// two blocks separated by one blank line, the block with the empty currency first (line 1 2026-11),
// then the block whose line 1 is 2026-11 HKD.
func TestReport_R6_MultipleCurrencies(t *testing.T) {
	entries, _, _, _ := ParseLoose(LooseFile, []byte(realFixture))
	v1Entry := Entry{
		Kind:     KindV1,
		Date:     "2026-11-01",
		Month:    "2026-11",
		Cents:    -1500,
		Currency: "HKD",
		Category: "transit",
		Note:     "subway",
		Path:     "ledger/2026/11/2026-11-01.md",
		Line:     1,
	}
	allEntries := append([]Entry{v1Entry}, entries...)
	b := Book{
		Entries: allEntries,
		Files:   2,
	}

	got := Report(b, "2026-11", "2026-11-10")
	want := "2026-11\n" +
		"spent 277.50 (8 item(s) on 4 day(s))\n" +
		"fixed 4000.00 (\u5bb6\u7528 4000.00)\n" +
		"money in 0.00\n" +
		"net out 4277.50\n" +
		"by category:\n" +
		"\u5bb6\u7528 4000.00 (1)\n" +
		"uncategorized 277.50 (8)\n" +
		"average per recorded day 69.38\n" +
		"no entry on 5 day(s): 06 07 08 09 10\n\n" +
		"2026-11 HKD\n" +
		"spent 15.00 (1 item(s) on 1 day(s))\n" +
		"fixed 0.00\n" +
		"money in 0.00\n" +
		"net out 15.00\n" +
		"by category:\n" +
		"transit 15.00 (1)\n" +
		"average per recorded day 15.00\n" +
		"no entry on 9 day(s): 02 03 04 05 06 07 08 09 10"

	if normalizeReport(got) != normalizeReport(want) {
		t.Errorf("report mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}
