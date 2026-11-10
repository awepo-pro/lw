package ledger

import (
	"strings"
	"testing"
)

// TestFixR_R1_DayNoteInputs tests every input in R1 contract for ParseDayNote.
func TestFixR_R1_DayNoteInputs(t *testing.T) {
	date := "2026-11-10"

	// 1. "44, 55" -> two items: 44 and 55
	{
		note := ParseDayNote(date+".md", date, []byte("44, 55\n"))
		if len(note.Issues) != 0 {
			t.Fatalf("44, 55 unexpected issues: %+v", note.Issues)
		}
		if len(note.Entries) != 2 {
			t.Fatalf("44, 55 got %d entries, want 2", len(note.Entries))
		}
		if note.Entries[0].Cents != -4400 || note.Entries[0].Note != "" {
			t.Errorf("44, 55 entry 0 mismatch: %+v", note.Entries[0])
		}
		if note.Entries[1].Cents != -5500 || note.Entries[1].Note != "" {
			t.Errorf("44, 55 entry 1 mismatch: %+v", note.Entries[1])
		}
	}

	// 2. "kfc 45, mcd 55" -> two items: 45 (kfc) and 55 (mcd)
	{
		note := ParseDayNote(date+".md", date, []byte("kfc 45, mcd 55\n"))
		if len(note.Issues) != 0 {
			t.Fatalf("kfc 45, mcd 55 unexpected issues: %+v", note.Issues)
		}
		if len(note.Entries) != 2 {
			t.Fatalf("kfc 45, mcd 55 got %d entries, want 2", len(note.Entries))
		}
		if note.Entries[0].Cents != -4500 || note.Entries[0].Note != "kfc" {
			t.Errorf("kfc 45, mcd 55 entry 0 mismatch: %+v", note.Entries[0])
		}
		if note.Entries[1].Cents != -5500 || note.Entries[1].Note != "mcd" {
			t.Errorf("kfc 45, mcd 55 entry 1 mismatch: %+v", note.Entries[1])
		}
	}

	// 3. "lunch with sam, 88" -> one item 88 (lunch with sam)
	{
		note := ParseDayNote(date+".md", date, []byte("lunch with sam, 88\n"))
		if len(note.Issues) != 0 {
			t.Fatalf("lunch with sam, 88 unexpected issues: %+v", note.Issues)
		}
		if len(note.Entries) != 1 {
			t.Fatalf("lunch with sam, 88 got %d entries, want 1", len(note.Entries))
		}
		if note.Entries[0].Cents != -8800 || note.Entries[0].Note != "lunch with sam" {
			t.Errorf("lunch with sam, 88 entry mismatch: %+v", note.Entries[0])
		}
	}

	// 4. "45, kfc" -> one item 45 (kfc)
	{
		note := ParseDayNote(date+".md", date, []byte("45, kfc\n"))
		if len(note.Issues) != 0 {
			t.Fatalf("45, kfc unexpected issues: %+v", note.Issues)
		}
		if len(note.Entries) != 1 {
			t.Fatalf("45, kfc got %d entries, want 1", len(note.Entries))
		}
		if note.Entries[0].Cents != -4500 || note.Entries[0].Note != "kfc" {
			t.Errorf("45, kfc entry mismatch: %+v", note.Entries[0])
		}
	}

	// 5. "kfc, mcd" -> no amount issue
	{
		note := ParseDayNote(date+".md", date, []byte("kfc, mcd\n"))
		if len(note.Entries) != 0 {
			t.Fatalf("kfc, mcd got %d entries, want 0", len(note.Entries))
		}
		if len(note.Issues) != 1 {
			t.Fatalf("kfc, mcd got %d issues, want 1: %+v", len(note.Issues), note.Issues)
		}
		wantMsg := "no amount in \"kfc mcd\""
		if note.Issues[0].Msg != wantMsg {
			t.Errorf("kfc, mcd issue msg = %q, want %q", note.Issues[0].Msg, wantMsg)
		}
	}

	// 6. "44," -> amount 44
	{
		note := ParseDayNote(date+".md", date, []byte("44,\n"))
		if len(note.Issues) != 0 {
			t.Fatalf("44, unexpected issues: %+v", note.Issues)
		}
		if len(note.Entries) != 1 {
			t.Fatalf("44, got %d entries, want 1", len(note.Entries))
		}
		if note.Entries[0].Cents != -4400 || note.Entries[0].Note != "" {
			t.Errorf("44, entry mismatch: %+v", note.Entries[0])
		}
	}

	// 7. "45 dinner 50" -> issue two numbers
	{
		note := ParseDayNote(date+".md", date, []byte("45 dinner 50\n"))
		if len(note.Entries) != 0 {
			t.Fatalf("45 dinner 50 got %d entries, want 0", len(note.Entries))
		}
		if len(note.Issues) != 1 {
			t.Fatalf("45 dinner 50 got %d issues, want 1", len(note.Issues))
		}
		wantMsg := "two numbers in \"45 dinner 50\" (write 23 + 45, or put the note in parentheses)"
		if note.Issues[0].Msg != wantMsg {
			t.Errorf("45 dinner 50 issue msg = %q, want %q", note.Issues[0].Msg, wantMsg)
		}
	}

	// 8. "100 - 50" -> issue two numbers
	{
		note := ParseDayNote(date+".md", date, []byte("100 - 50\n"))
		if len(note.Entries) != 0 {
			t.Fatalf("100 - 50 got %d entries, want 0", len(note.Entries))
		}
		if len(note.Issues) != 1 {
			t.Fatalf("100 - 50 got %d issues, want 1", len(note.Issues))
		}
		wantMsg := "two numbers in \"100 - 50\" (write 23 + 45, or put the note in parentheses)"
		if note.Issues[0].Msg != wantMsg {
			t.Errorf("100 - 50 issue msg = %q, want %q", note.Issues[0].Msg, wantMsg)
		}
	}

	// 9. "bus 26 6" -> issue two numbers
	{
		note := ParseDayNote(date+".md", date, []byte("bus 26 6\n"))
		if len(note.Entries) != 0 {
			t.Fatalf("bus 26 6 got %d entries, want 0", len(note.Entries))
		}
		if len(note.Issues) != 1 {
			t.Fatalf("bus 26 6 got %d issues, want 1", len(note.Issues))
		}
		wantMsg := "two numbers in \"bus 26 6\" (write 23 + 45, or put the note in parentheses)"
		if note.Issues[0].Msg != wantMsg {
			t.Errorf("bus 26 6 issue msg = %q, want %q", note.Issues[0].Msg, wantMsg)
		}
	}

	// 10. "6 (bus 26)" -> valid 1 entry: 6, note "bus 26"
	{
		note := ParseDayNote(date+".md", date, []byte("6 (bus 26)\n"))
		if len(note.Issues) != 0 {
			t.Fatalf("6 (bus 26) unexpected issues: %+v", note.Issues)
		}
		if len(note.Entries) != 1 {
			t.Fatalf("6 (bus 26) got %d entries, want 1", len(note.Entries))
		}
		if note.Entries[0].Cents != -600 || note.Entries[0].Note != "bus 26" {
			t.Errorf("6 (bus 26) entry mismatch: %+v", note.Entries[0])
		}
	}

	// 11. "7\u4ed4 49.5" -> valid 1 entry: 49.5, note "7\u4ed4"
	{
		note := ParseDayNote(date+".md", date, []byte("7\u4ed4 49.5\n"))
		if len(note.Issues) != 0 {
			t.Fatalf("7\u4ed4 49.5 unexpected issues: %+v", note.Issues)
		}
		if len(note.Entries) != 1 {
			t.Fatalf("7\u4ed4 49.5 got %d entries, want 1", len(note.Entries))
		}
		if note.Entries[0].Cents != -4950 || note.Entries[0].Note != "7\u4ed4" {
			t.Errorf("7\u4ed4 49.5 entry mismatch: %+v", note.Entries[0])
		}
	}

	// 12. RF-1 date line: "2026-11-10, 44, 55" -> two items: 44 and 55
	{
		note := ParseDayNote(date+".md", date, []byte("2026-11-10, 44, 55\n"))
		if len(note.Issues) != 0 {
			t.Fatalf("2026-11-10, 44, 55 unexpected issues: %+v", note.Issues)
		}
		if len(note.Entries) != 2 {
			t.Fatalf("2026-11-10, 44, 55 got %d entries, want 2", len(note.Entries))
		}
		if note.Entries[0].Cents != -4400 || note.Entries[1].Cents != -5500 {
			t.Errorf("2026-11-10, 44, 55 cents mismatch: %+v", note.Entries)
		}
	}
}

// TestFixR_R1_CentralFileInputs tests every input in R1 contract for ParseLoose (central file).
func TestFixR_R1_CentralFileInputs(t *testing.T) {
	// 1. "2026-11-10, 44, 55" -> bad amount issue (comma does not separate items in central file)
	{
		line := "2026-11-10, 44, 55\n"
		entries, issues, _, _ := ParseLoose("bookkeeping.md", []byte(line))
		if len(entries) != 0 {
			t.Fatalf("ParseLoose '44, 55' got %d entries, want 0", len(entries))
		}
		if len(issues) != 1 {
			t.Fatalf("ParseLoose '44, 55' got %d issues, want 1: %+v", len(issues), issues)
		}
		if !strings.HasPrefix(issues[0].Msg, "bad amount \"44, 55\"") {
			t.Errorf("ParseLoose '44, 55' issue msg = %q, want prefix 'bad amount \"44, 55\"'", issues[0].Msg)
		}
	}

	// 2. "2026-11-10, 45 dinner 50" -> two numbers issue
	{
		line := "2026-11-10, 45 dinner 50\n"
		entries, issues, _, _ := ParseLoose("bookkeeping.md", []byte(line))
		if len(entries) != 0 {
			t.Fatalf("ParseLoose '45 dinner 50' got %d entries, want 0", len(entries))
		}
		if len(issues) != 1 {
			t.Fatalf("ParseLoose '45 dinner 50' got %d issues, want 1: %+v", len(issues), issues)
		}
		wantMsg := "two numbers in \"45 dinner 50\" (write 23 + 45, or put the note in parentheses)"
		if issues[0].Msg != wantMsg {
			t.Errorf("ParseLoose '45 dinner 50' issue msg = %q, want %q", issues[0].Msg, wantMsg)
		}
	}

	// 3. "2026-11-10, 100 - 50" -> two numbers issue
	{
		line := "2026-11-10, 100 - 50\n"
		entries, issues, _, _ := ParseLoose("bookkeeping.md", []byte(line))
		if len(entries) != 0 {
			t.Fatalf("ParseLoose '100 - 50' got %d entries, want 0", len(entries))
		}
		if len(issues) != 1 {
			t.Fatalf("ParseLoose '100 - 50' got %d issues, want 1: %+v", len(issues), issues)
		}
		wantMsg := "two numbers in \"100 - 50\" (write 23 + 45, or put the note in parentheses)"
		if issues[0].Msg != wantMsg {
			t.Errorf("ParseLoose '100 - 50' issue msg = %q, want %q", issues[0].Msg, wantMsg)
		}
	}
}

// TestFixR_HostileDayNoteInputs tests at least 25 hostile day-note lines to ensure no silent wrong money totals.
func TestFixR_HostileDayNoteInputs(t *testing.T) {
	date := "2026-11-10"
	cases := []struct {
		input       string
		expectIssue bool
		wantCents   []int64
	}{
		{"lunch40\n", true, nil},                                      // 1. number glued to end of word -> no amount
		{"40lunch\n", true, nil},                                      // 2. number glued to start of word -> no amount
		{"12,5\n", true, nil},                                         // 3. decimal with comma -> no amount
		{"1,200\n", true, nil},                                        // 4. thousands with comma -> no amount (never 1 and 200!)
		{"1,200 rent\n", true, nil},                                   // 5. thousands with note -> no amount
		{"rent 1,200\n", true, nil},                                   // 6. thousands note-first -> no amount
		{"2 x 15\n", true, nil},                                       // 7. multiplication -> two numbers issue
		{"3 coffees 45\n", true, nil},                                 // 8. quantity and price -> two numbers issue
		{"lunch on 2026-11-10 40\n", false, []int64{-4000}},           // 9. date inside note -> amount 40
		{"call 555-1234 50\n", false, []int64{-5000}},                 // 10. phone with hyphen -> amount 50
		{"call 12345678 50\n", true, nil},                             // 11. phone digits -> two numbers issue
		{"12:30 lunch 40\n", false, []int64{-4000}},                   // 12. time with colon -> amount 40
		{"tip 10% 5\n", false, []int64{-500}},                         // 13. percentage in note -> amount 5
		{"10% tip 5\n", false, []int64{-500}},                         // 14. percentage before note -> amount 5
		{"-50\n", false, []int64{5000}},                               // 15. negative alone -> money in +5000
		{"-$50\n", false, []int64{5000}},                              // 16. negative with $ -> money in +5000
		{"+$50 refund\n", false, []int64{-5000}},                      // 17. +$50 -> spending 5000
		{"$45.50 dinner\n", false, []int64{-4550}},                    // 18. $45.50 dinner -> amount 45.50
		{"dinner $45.50\n", false, []int64{-4550}},                    // 19. dinner $45.50 -> amount 45.50
		{"４４， ５５\n", false, []int64{-4400, -5500}},                    // 20. full-width digits and comma -> two items 44 and 55
		{"１，２００\n", true, nil},                                        // 21. full-width thousands -> no amount
		{"１２，５\n", true, nil},                                         // 22. full-width comma decimal -> no amount
		{"44,55\n", true, nil},                                        // 23. comma between digits without space -> no amount
		{"3.14159 pi 50\n", true, nil},                                // 24. >2 decimal places + 50 -> two numbers issue
		{"coffee $4.50 + muffin $3.25\n", false, []int64{-450, -325}}, // 25. $ with plus -> 4.50 and 3.25
		{"room 101 rent 1500\n", true, nil},                           // 26. room number and rent -> two numbers issue
		{"flight UA821 350\n", false, []int64{-35000}},                // 27. flight code with digits -> amount 350
		{"bus #42 5\n", false, []int64{-500}},                         // 28. hash tag #42 -> tag 42, amount 5
		{"(3 coffees 45) 45\n", false, []int64{-4500}},                // 29. numbers in parens -> amount 45
		{"45 (3 coffees 45)\n", false, []int64{-4500}},                // 30. amount first, parens -> amount 45
	}

	for i, tc := range cases {
		note := ParseDayNote(date+".md", date, []byte(tc.input))
		if tc.expectIssue {
			if len(note.Issues) == 0 {
				t.Errorf("case %d %q: expected issue, got %d entries: %+v", i+1, tc.input, len(note.Entries), note.Entries)
			}
			if len(note.Entries) != 0 {
				t.Errorf("case %d %q: expected 0 entries with issue, got %d", i+1, tc.input, len(note.Entries))
			}
		} else {
			if len(note.Issues) != 0 {
				t.Errorf("case %d %q: unexpected issues: %+v", i+1, tc.input, note.Issues)
			}
			if len(note.Entries) != len(tc.wantCents) {
				t.Fatalf("case %d %q: got %d entries, want %d", i+1, tc.input, len(note.Entries), len(tc.wantCents))
			}
			for j, wantC := range tc.wantCents {
				if note.Entries[j].Cents != wantC {
					t.Errorf("case %d %q entry %d: got cents %d, want %d", i+1, tc.input, j, note.Entries[j].Cents, wantC)
				}
			}
		}
	}
}
