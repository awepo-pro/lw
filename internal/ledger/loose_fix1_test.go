package ledger

import (
	"testing"
)

// TestLooseF2FullWidth tests F2 (amendment A-082-1):
// Full-width punctuation and digits normalization.
func TestLooseF2FullWidth(t *testing.T) {
	// 1. Line with full-width punctuation: comma \uff0c, parens \uff08 \uff09, plus \uff0b
	// Note = 2 CJK runes (\u4e00\u4e8c)
	line1 := "2026-11-10\uff0c 38 \uff08\u4e00\u4e8c\uff09\uff0b 12\n"
	entries1, issues1, _, _ := ParseLoose("bookkeeping.md", []byte(line1))
	if len(issues1) != 0 {
		t.Fatalf("unexpected issues for full-width line: %v", issues1)
	}
	if len(entries1) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries1))
	}
	if entries1[0].Cents != -3800 || entries1[0].Note != "\u4e00\u4e8c" {
		t.Errorf("entry 0 mismatch: cents=%d (want -3800), note=%q (want %q)",
			entries1[0].Cents, entries1[0].Note, "\u4e00\u4e8c")
	}
	if entries1[1].Cents != -1200 || entries1[1].Note != "" {
		t.Errorf("entry 1 mismatch: cents=%d (want -1200), note=%q (want \"\")",
			entries1[1].Cents, entries1[1].Note)
	}

	// 2. 11\u6708\uff0d rent 5000 beside a 2026 day line gives a KindFixed -500000
	line2 := "11\u6708\uff0d rent 5000\n2026-11-10, 10\n"
	entries2, issues2, _, _ := ParseLoose("bookkeeping.md", []byte(line2))
	if len(issues2) != 0 {
		t.Fatalf("unexpected issues for 11\\u6708\\uff0d rent: %v", issues2)
	}
	if len(entries2) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries2))
	}
	if entries2[0].Kind != KindFixed || entries2[0].Cents != -500000 || entries2[0].Month != "2026-11" || entries2[0].Note != "rent" {
		t.Errorf("entry 0 mismatch for KindFixed: kind=%v cents=%d month=%q note=%q",
			entries2[0].Kind, entries2[0].Cents, entries2[0].Month, entries2[0].Note)
	}

	// 3. Full-width digits \uff14\uff14 on a day line give -4400
	line3 := "2026-11-10, \uff14\uff14\n"
	entries3, issues3, _, _ := ParseLoose("bookkeeping.md", []byte(line3))
	if len(issues3) != 0 {
		t.Fatalf("unexpected issues for full-width digits: %v", issues3)
	}
	if len(entries3) != 1 || entries3[0].Cents != -4400 {
		t.Fatalf("entry mismatch for full-width digits: got %v", entries3)
	}

	// 4. currency\uff1dmop sets MOP
	line4 := "currency\uff1dmop\n2026-11-10, 10\n"
	_, issues4, _, currency4 := ParseLoose("bookkeeping.md", []byte(line4))
	if len(issues4) != 0 {
		t.Fatalf("unexpected issues for currency\\uff1dmop: %v", issues4)
	}
	if currency4 != "MOP" {
		t.Errorf("currency mismatch: got %q, want MOP", currency4)
	}
}

// TestLooseF3MonthSignSpacing tests F3:
// The sign of a month line has optional spaces around it.
// Tests the 5 forms for '-', 3 forms for '+', plus guard cases.
func TestLooseF3MonthSignSpacing(t *testing.T) {
	// 5 forms for '-':
	minusForms := []string{
		"Nov-rent 5000",
		"Nov -rent 5000",
		"Nov- rent 5000",
		"Nov 2026-rent 5000",
		"2026-11-rent 5000",
	}

	dayLineContext := "2026-11-10, 10\n"

	for _, form := range minusForms {
		data := form + "\n" + dayLineContext
		entries, issues, _, _ := ParseLoose("bookkeeping.md", []byte(data))
		if len(issues) != 0 {
			t.Errorf("form %q had unexpected issues: %v", form, issues)
			continue
		}
		if len(entries) != 2 {
			t.Errorf("form %q got %d entries, want 2", form, len(entries))
			continue
		}
		mEntry := entries[0]
		if mEntry.Kind != KindFixed || mEntry.Month != "2026-11" || mEntry.Cents != -500000 || mEntry.Note != "rent" || mEntry.Category != "rent" {
			t.Errorf("form %q parsed wrong: kind=%v month=%q cents=%d note=%q category=%q",
				form, mEntry.Kind, mEntry.Month, mEntry.Cents, mEntry.Note, mEntry.Category)
		}
	}

	// 3 forms for '+':
	plusForms := []string{
		"Nov+salary 5000",
		"Nov +salary 5000",
		"Nov+ salary 5000",
	}

	for _, form := range plusForms {
		data := form + "\n" + dayLineContext
		entries, issues, _, _ := ParseLoose("bookkeeping.md", []byte(data))
		if len(issues) != 0 {
			t.Errorf("form %q had unexpected issues: %v", form, issues)
			continue
		}
		if len(entries) != 2 {
			t.Errorf("form %q got %d entries, want 2", form, len(entries))
			continue
		}
		mEntry := entries[0]
		if mEntry.Kind != KindFixed || mEntry.Month != "2026-11" || mEntry.Cents != 500000 || mEntry.Note != "salary" || mEntry.Category != "salary" {
			t.Errorf("form %q parsed wrong: kind=%v month=%q cents=%d note=%q category=%q",
				form, mEntry.Kind, mEntry.Month, mEntry.Cents, mEntry.Note, mEntry.Category)
		}
	}

	// Guard case 1: 2026-11-06, 5 must stay a day line
	guardDay := "2026-11-06, 5\n"
	entriesDay, issuesDay, _, _ := ParseLoose("bookkeeping.md", []byte(guardDay))
	if len(issuesDay) != 0 {
		t.Errorf("guard day line had issues: %v", issuesDay)
	}
	if len(entriesDay) != 1 {
		t.Fatalf("guard day line got %d entries, want 1", len(entriesDay))
	}
	if entriesDay[0].Kind != KindDay || entriesDay[0].Date != "2026-11-06" || entriesDay[0].Cents != -500 {
		t.Errorf("guard day line mismatch: kind=%v date=%q cents=%d",
			entriesDay[0].Kind, entriesDay[0].Date, entriesDay[0].Cents)
	}

	// Guard case 2: 2026-11 - rent 5000 must keep working
	guardFixed := "2026-11 - rent 5000\n"
	entriesFixed, issuesFixed, _, _ := ParseLoose("bookkeeping.md", []byte(guardFixed))
	if len(issuesFixed) != 0 {
		t.Errorf("guard 2026-11 - rent 5000 had issues: %v", issuesFixed)
	}
	if len(entriesFixed) != 1 {
		t.Fatalf("guard 2026-11 - rent 5000 got %d entries, want 1", len(entriesFixed))
	}
	if entriesFixed[0].Kind != KindFixed || entriesFixed[0].Month != "2026-11" || entriesFixed[0].Cents != -500000 || entriesFixed[0].Note != "rent" {
		t.Errorf("guard 2026-11 - rent 5000 mismatch: kind=%v month=%q cents=%d note=%q",
			entriesFixed[0].Kind, entriesFixed[0].Month, entriesFixed[0].Cents, entriesFixed[0].Note)
	}

	// Guard case 3: Nov-12 dinner 5 parses as a correct month line
	guardDinner := "Nov-12 dinner 5\n" + dayLineContext
	entriesDinner, issuesDinner, _, _ := ParseLoose("bookkeeping.md", []byte(guardDinner))
	if len(issuesDinner) != 0 {
		t.Errorf("guard Nov-12 dinner 5 had issues: %v", issuesDinner)
	}
	if len(entriesDinner) != 2 {
		t.Fatalf("guard Nov-12 dinner 5 got %d entries, want 2", len(entriesDinner))
	}
	if entriesDinner[0].Kind != KindFixed || entriesDinner[0].Month != "2026-11" || entriesDinner[0].Cents != -500 || entriesDinner[0].Note != "12 dinner" {
		t.Errorf("guard Nov-12 dinner 5 mismatch: kind=%v month=%q cents=%d note=%q",
			entriesDinner[0].Kind, entriesDinner[0].Month, entriesDinner[0].Cents, entriesDinner[0].Note)
	}
}
