package ledger

import (
	"strings"
	"testing"
)

// TestLoose_RV1 tests RV-1: an unclosed code fence produces issue 'unclosed code fence'
// at the line of the opening fence; the lines after it are parsed as if the fence line were
// a comment (nothing vanishes).
func TestLoose_RV1(t *testing.T) {
	input := "2026-11-01, 10\n" +
		"```\n" +
		"2026-11-02, 20\n" +
		"2026-11-03, 30\n"

	entries, issues, _, _ := ParseLoose("bookkeeping.md", []byte(input))
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d: %+v", len(issues), issues)
	}
	if issues[0].Line != 2 || issues[0].Msg != "unclosed code fence" {
		t.Errorf("issue mismatch: got %+v, want Line 2 'unclosed code fence'", issues[0])
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d: %+v", len(entries), entries)
	}
	if entries[0].Cents != -1000 || entries[1].Cents != -2000 || entries[2].Cents != -3000 {
		t.Errorf("entries cents mismatch: got %d, %d, %d", entries[0].Cents, entries[1].Cents, entries[2].Cents)
	}
}

// TestLoose_RV2 tests RV-2: unbalanced parentheses on a day line (an unclosed '(' or
// a stray ')' at depth 0): the line yields no entry and the issue 'unbalanced parentheses'.
func TestLoose_RV2(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"unclosed", "2026-11-04, 25 (lunch + 10\n"},
		{"stray closing", "2026-11-04, 25 lunch) + 10\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries, issues, _, _ := ParseLoose("bookkeeping.md", []byte(tc.input))
			if len(entries) != 0 {
				t.Fatalf("expected 0 entries, got %d: %+v", len(entries), entries)
			}
			if len(issues) != 1 {
				t.Fatalf("expected 1 issue, got %d: %+v", len(issues), issues)
			}
			if issues[0].Msg != "unbalanced parentheses" {
				t.Errorf("issue msg mismatch: got %q, want 'unbalanced parentheses'", issues[0].Msg)
			}
		})
	}
}

// TestReport_RV3 tests RV-3: the 'no entry' line is omitted when days = 0.
func TestReport_RV3(t *testing.T) {
	input := "2026-11-04, -500 (salary)\n"
	entries, issues, budgetCents, currency := ParseLoose("bookkeeping.md", []byte(input))
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
	if strings.Contains(got, "no entry") {
		t.Errorf("report should omit 'no entry' line when days = 0, got:\n%s", got)
	}
}

// TestLoose_RV4 tests RV-4: a tab is whitespace everywhere a space is (after the amount, around signs).
func TestLoose_RV4(t *testing.T) {
	input := "2026-11-04, 25\t(lunch)\n" +
		"2026-11-05, 10\t+\t20\n" +
		"Nov\t-\trent\t5000\n"

	entries, issues, _, _ := ParseLoose("bookkeeping.md", []byte(input))
	if len(issues) != 0 {
		t.Fatalf("unexpected issues: %+v", issues)
	}
	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d: %+v", len(entries), entries)
	}
	if entries[0].Note != "lunch" || entries[0].Cents != -2500 {
		t.Errorf("entry 0 mismatch: %+v", entries[0])
	}
	if entries[1].Cents != -1000 || entries[2].Cents != -2000 {
		t.Errorf("entry 1,2 mismatch: %+v, %+v", entries[1], entries[2])
	}
	if entries[3].Kind != KindFixed || entries[3].Note != "rent" || entries[3].Cents != -500000 {
		t.Errorf("entry 3 mismatch: %+v", entries[3])
	}
}

// TestLoose_RV5 tests RV-5: a month line whose label, after the sign, starts with '+' or '-' is not a month line (G9 issue).
func TestLoose_RV5(t *testing.T) {
	cases := []string{
		"Nov +- rent 5000\n2026-11-10, 10\n",
		"Nov -+ rent 5000\n2026-11-10, 10\n",
		"Nov -- rent 5000\n2026-11-10, 10\n",
		"Nov ++ rent 5000\n2026-11-10, 10\n",
	}

	for _, input := range cases {
		entries, issues, _, _ := ParseLoose("bookkeeping.md", []byte(input))
		if len(issues) != 1 {
			t.Fatalf("expected 1 issue for %q, got %d: %+v", input, len(issues), issues)
		}
		wantMsg := "not understood (want \"YYYY-MM-DD, 44 (note) + 12.5\" or \"Nov - rent 5000\")"
		if issues[0].Line != 1 || issues[0].Msg != wantMsg {
			t.Errorf("issue mismatch for %q: got line %d %q", input, issues[0].Line, issues[0].Msg)
		}
		if len(entries) != 1 || entries[0].Line != 2 {
			t.Errorf("entries mismatch for %q: %+v", input, entries)
		}
	}
}

// TestLoose_RV9 tests RV-9: a day item whose note, outside parentheses, consists only of number tokens
// is the issue 'two numbers in one item "..." (write 23 + 45, or put the note in parentheses)'.
// '25 (bus 26)' stays valid.
func TestLoose_RV9(t *testing.T) {
	inputBad := "2026-11-04, 23 45\n"
	entries, issues, _, _ := ParseLoose("bookkeeping.md", []byte(inputBad))
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries for bad input, got %d: %+v", len(entries), entries)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue for bad input, got %d: %+v", len(issues), issues)
	}
	wantMsg := "two numbers in one item \"23 45\" (write 23 + 45, or put the note in parentheses)"
	if issues[0].Msg != wantMsg {
		t.Errorf("issue msg mismatch: got %q, want %q", issues[0].Msg, wantMsg)
	}

	inputGood := "2026-11-04, 25 (bus 26)\n"
	entriesGood, issuesGood, _, _ := ParseLoose("bookkeeping.md", []byte(inputGood))
	if len(issuesGood) != 0 {
		t.Fatalf("unexpected issues for good input: %+v", issuesGood)
	}
	if len(entriesGood) != 1 {
		t.Fatalf("expected 1 entry for good input, got %d: %+v", len(entriesGood), entriesGood)
	}
	if entriesGood[0].Cents != -2500 || entriesGood[0].Note != "bus 26" {
		t.Errorf("entry mismatch: got %+v", entriesGood[0])
	}
}

// TestLoose_B1 tests B1: 2026-11-10, 44 (kfc) [[2026-11-10]] -> 1 entry, Source 2026-11-10, note kfc;
// two lines for one date, one with a link -> no warning; neither with a link -> the warning as before.
func TestLoose_B1(t *testing.T) {
	// Single line with link
	input1 := "2026-11-10, 44 (kfc) [[2026-11-10]]\n"
	entries1, issues1, _, _ := ParseLoose("bookkeeping.md", []byte(input1))
	if len(issues1) != 0 {
		t.Fatalf("unexpected issues: %+v", issues1)
	}
	if len(entries1) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries1))
	}
	if entries1[0].Source != "2026-11-10" || entries1[0].Note != "kfc" || entries1[0].Cents != -4400 {
		t.Errorf("entry mismatch: %+v", entries1[0])
	}

	// Two lines, one with link (first without, second with)
	input2 := "2026-11-10, 10\n2026-11-10, 44 (kfc) [[2026-11-10]]\n"
	entries2, issues2, _, _ := ParseLoose("bookkeeping.md", []byte(input2))
	if len(issues2) != 0 {
		t.Errorf("expected 0 issues (no warning), got %+v", issues2)
	}
	if len(entries2) != 2 {
		t.Errorf("expected 2 entries, got %d", len(entries2))
	}

	// Two lines, one with link (first with, second without)
	input3 := "2026-11-10, 44 (kfc) [[2026-11-10]]\n2026-11-10, 10\n"
	entries3, issues3, _, _ := ParseLoose("bookkeeping.md", []byte(input3))
	if len(issues3) != 0 {
		t.Errorf("expected 0 issues (no warning), got %+v", issues3)
	}
	if len(entries3) != 2 {
		t.Errorf("expected 2 entries, got %d", len(entries3))
	}

	// Neither with link -> warning as before
	input4 := "2026-11-10, 10\n2026-11-10, 20\n"
	entries4, issues4, _, _ := ParseLoose("bookkeeping.md", []byte(input4))
	if len(entries4) != 2 {
		t.Errorf("expected 2 entries, got %d", len(entries4))
	}
	if len(issues4) != 1 || !issues4[0].Warn || issues4[0].Line != 2 {
		t.Errorf("expected 1 warning on line 2, got %+v", issues4)
	}
}
