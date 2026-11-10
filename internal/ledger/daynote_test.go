package ledger

import (
	"reflect"
	"testing"
)

// TestDayNote_D1 tests D1: note "44 (jollibee)\nkfc 45\n23 + bus 6 #transport\n" -> 4 entries
// -4400 jollibee, -4500 kfc, -2300, -600 bus with tag transport;
// CentralLine = "2026-11-10, 44 (jollibee) + 45 (kfc) + 23 + 6 (bus) #transport [[2026-11-10]]".
func TestDayNote_D1(t *testing.T) {
	input := "44 (jollibee)\nkfc 45\n23 + bus 6 #transport\n"
	note := ParseDayNote("2026-11-10.md", "2026-11-10", []byte(input))
	if len(note.Issues) != 0 {
		t.Fatalf("unexpected issues: %+v", note.Issues)
	}
	if len(note.Entries) != 4 {
		t.Fatalf("expected 4 entries, got %d: %+v", len(note.Entries), note.Entries)
	}

	expected := []struct {
		cents int64
		note  string
		cat   string
	}{
		{-4400, "jollibee", ""},
		{-4500, "kfc", ""},
		{-2300, "", ""},
		{-600, "bus", "transport"},
	}

	for i, exp := range expected {
		e := note.Entries[i]
		if e.Cents != exp.cents || e.Note != exp.note || e.Category != exp.cat {
			t.Errorf("entry %d mismatch: got %+v, want cents=%d note=%q cat=%q", i, e, exp.cents, exp.note, exp.cat)
		}
		if e.Date != "2026-11-10" || e.Month != "2026-11" || e.Kind != KindDay {
			t.Errorf("entry %d metadata mismatch: got %+v", i, e)
		}
	}

	gotLine := CentralLine("2026-11-10", note.Entries)
	wantLine := "2026-11-10, 44 (jollibee) + 45 (kfc) + 23 + 6 (bus) #transport [[2026-11-10]]"
	if gotLine != wantLine {
		t.Errorf("CentralLine mismatch:\ngot:  %q\nwant: %q", gotLine, wantLine)
	}
}

// TestDayNote_D2 tests D2: "bus 26 6" -> issue two numbers, 0 entries.
// "lunch with sam" -> issue no amount.
// "-500 refund" -> +50000.
func TestDayNote_D2(t *testing.T) {
	// Case 1: bus 26 6 -> two amount tokens
	note1 := ParseDayNote("2026-11-10.md", "2026-11-10", []byte("bus 26 6\n"))
	if len(note1.Entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(note1.Entries))
	}
	if len(note1.Issues) != 1 {
		t.Fatalf("expected 1 issue, got %d: %+v", len(note1.Issues), note1.Issues)
	}
	wantIssue1 := "two numbers in \"bus 26 6\" (write 23 + 45, or put the note in parentheses)"
	if note1.Issues[0].Msg != wantIssue1 {
		t.Errorf("issue 1 mismatch: got %q, want %q", note1.Issues[0].Msg, wantIssue1)
	}

	// Case 2: lunch with sam -> no amount
	note2 := ParseDayNote("2026-11-10.md", "2026-11-10", []byte("lunch with sam\n"))
	if len(note2.Entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(note2.Entries))
	}
	if len(note2.Issues) != 1 {
		t.Fatalf("expected 1 issue, got %d: %+v", len(note2.Issues), note2.Issues)
	}
	wantIssue2 := "no amount in \"lunch with sam\""
	if note2.Issues[0].Msg != wantIssue2 {
		t.Errorf("issue 2 mismatch: got %q, want %q", note2.Issues[0].Msg, wantIssue2)
	}

	// Case 3: -500 refund -> +50000 cents
	note3 := ParseDayNote("2026-11-10.md", "2026-11-10", []byte("-500 refund\n"))
	if len(note3.Issues) != 0 {
		t.Fatalf("unexpected issues: %+v", note3.Issues)
	}
	if len(note3.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(note3.Entries))
	}
	if note3.Entries[0].Cents != 50000 || note3.Entries[0].Note != "refund" {
		t.Errorf("entry 3 mismatch: got %+v", note3.Entries[0])
	}
}

// TestDayNote_D3 tests D3: "![[receipt 1.jpg]]\n88 (dinner) ![](img/b.png)\n" -> 1 entry,
// Images ["receipt 1.jpg", "img/b.png"].
func TestDayNote_D3(t *testing.T) {
	input := "![[receipt 1.jpg]]\n88 (dinner) ![](img/b.png)\n"
	note := ParseDayNote("2026-11-10.md", "2026-11-10", []byte(input))
	if len(note.Issues) != 0 {
		t.Fatalf("unexpected issues: %+v", note.Issues)
	}
	if len(note.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(note.Entries))
	}
	if note.Entries[0].Cents != -8800 || note.Entries[0].Note != "dinner" {
		t.Errorf("entry mismatch: %+v", note.Entries[0])
	}
	wantImages := []string{"receipt 1.jpg", "img/b.png"}
	if !reflect.DeepEqual(note.Images, wantImages) {
		t.Errorf("images mismatch: got %+v, want %+v", note.Images, wantImages)
	}
}

// TestDayNote_D4 tests D4: only "![[r.jpg]]" -> 0 entries, 0 issues, 1 image.
func TestDayNote_D4(t *testing.T) {
	input := "![[r.jpg]]\n"
	note := ParseDayNote("2026-11-10.md", "2026-11-10", []byte(input))
	if len(note.Issues) != 0 {
		t.Fatalf("expected 0 issues, got %+v", note.Issues)
	}
	if len(note.Entries) != 0 {
		t.Fatalf("expected 0 entries, got %+v", note.Entries)
	}
	wantImages := []string{"r.jpg"}
	if !reflect.DeepEqual(note.Images, wantImages) {
		t.Errorf("images mismatch: got %+v, want %+v", note.Images, wantImages)
	}
}

// TestDayNote_D5 tests D5: a "2026-11-09, 5" line inside the 11-10 note -> issue;
// "2026-11-10, 5 + 6" inside it -> 2 entries.
func TestDayNote_D5(t *testing.T) {
	// Wrong date
	input1 := "2026-11-09, 5\n"
	note1 := ParseDayNote("2026-11-10.md", "2026-11-10", []byte(input1))
	if len(note1.Entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(note1.Entries))
	}
	if len(note1.Issues) != 1 {
		t.Fatalf("expected 1 issue, got %d: %+v", len(note1.Issues), note1.Issues)
	}
	wantIssue := "date 2026-11-09 is not this note's date"
	if note1.Issues[0].Msg != wantIssue {
		t.Errorf("issue mismatch: got %q, want %q", note1.Issues[0].Msg, wantIssue)
	}

	// Correct date
	input2 := "2026-11-10, 5 + 6\n"
	note2 := ParseDayNote("2026-11-10.md", "2026-11-10", []byte(input2))
	if len(note2.Issues) != 0 {
		t.Fatalf("unexpected issues: %+v", note2.Issues)
	}
	if len(note2.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d: %+v", len(note2.Entries), note2.Entries)
	}
	if note2.Entries[0].Cents != -500 || note2.Entries[1].Cents != -600 {
		t.Errorf("entries mismatch: %+v", note2.Entries)
	}
}

// TestDayNote_D6 tests D6: frontmatter, heading "# Monday", full-width punctuation, CRLF: read like D1.
func TestDayNote_D6(t *testing.T) {
	input := "---\r\ntitle: Today\r\n---\r\n# Monday\r\n\r\n" +
		"44\uff08jollibee\uff09\r\nkfc 45\r\n23 \uff0b bus 6 \uff03transport\r\n"

	note := ParseDayNote("2026-11-10.md", "2026-11-10", []byte(input))
	if len(note.Issues) != 0 {
		t.Fatalf("unexpected issues: %+v", note.Issues)
	}
	if len(note.Entries) != 4 {
		t.Fatalf("expected 4 entries, got %d: %+v", len(note.Entries), note.Entries)
	}

	gotLine := CentralLine("2026-11-10", note.Entries)
	wantLine := "2026-11-10, 44 (jollibee) + 45 (kfc) + 23 + 6 (bus) #transport [[2026-11-10]]"
	if gotLine != wantLine {
		t.Errorf("CentralLine mismatch:\ngot:  %q\nwant: %q", gotLine, wantLine)
	}
}

// TestDayNote_D7 tests D7: CentralLine output parses back through ParseLoose to the same cents,
// notes and tags (round trip), for D1 and for a money-in item and a 49.5 amount.
func TestDayNote_D7(t *testing.T) {
	// Round trip for D1
	inputD1 := "44 (jollibee)\nkfc 45\n23 + bus 6 #transport\n"
	noteD1 := ParseDayNote("2026-11-10.md", "2026-11-10", []byte(inputD1))
	lineD1 := CentralLine("2026-11-10", noteD1.Entries)

	entriesD1, issuesD1, _, _ := ParseLoose("bookkeeping.md", []byte(lineD1+"\n"))
	if len(issuesD1) != 0 {
		t.Fatalf("unexpected issues on D1 round trip: %+v", issuesD1)
	}
	if len(entriesD1) != len(noteD1.Entries) {
		t.Fatalf("entry count mismatch: got %d, want %d", len(entriesD1), len(noteD1.Entries))
	}
	for i := range entriesD1 {
		if entriesD1[i].Cents != noteD1.Entries[i].Cents {
			t.Errorf("entry %d cents mismatch: got %d, want %d", i, entriesD1[i].Cents, noteD1.Entries[i].Cents)
		}
		if entriesD1[i].Note != noteD1.Entries[i].Note {
			t.Errorf("entry %d note mismatch: got %q, want %q", i, entriesD1[i].Note, noteD1.Entries[i].Note)
		}
		if entriesD1[i].Category != noteD1.Entries[i].Category {
			t.Errorf("entry %d category mismatch: got %q, want %q", i, entriesD1[i].Category, noteD1.Entries[i].Category)
		}
		if entriesD1[i].Source != "2026-11-10" {
			t.Errorf("entry %d source mismatch: got %q, want '2026-11-10'", i, entriesD1[i].Source)
		}
	}

	// Round trip for money in and 49.5
	inputExtra := "-500 refund\nsnack 49.5\n"
	noteExtra := ParseDayNote("2026-11-10.md", "2026-11-10", []byte(inputExtra))
	lineExtra := CentralLine("2026-11-10", noteExtra.Entries)

	entriesExtra, issuesExtra, _, _ := ParseLoose("bookkeeping.md", []byte(lineExtra+"\n"))
	if len(issuesExtra) != 0 {
		t.Fatalf("unexpected issues on extra round trip: %+v", issuesExtra)
	}
	if len(entriesExtra) != len(noteExtra.Entries) {
		t.Fatalf("extra entry count mismatch: got %d, want %d", len(entriesExtra), len(noteExtra.Entries))
	}
	for i := range entriesExtra {
		if entriesExtra[i].Cents != noteExtra.Entries[i].Cents {
			t.Errorf("extra %d cents mismatch: got %d, want %d", i, entriesExtra[i].Cents, noteExtra.Entries[i].Cents)
		}
		if entriesExtra[i].Note != noteExtra.Entries[i].Note {
			t.Errorf("extra %d note mismatch: got %q, want %q", i, entriesExtra[i].Note, noteExtra.Entries[i].Note)
		}
	}

	// Round trip for R1 inputs: 44, 55; kfc 45, mcd 55; lunch with sam, 88; 45, kfc; 6 (bus 26); 7仔 49.5; 44,
	r1Inputs := []string{
		"44, 55\n",
		"kfc 45, mcd 55\n",
		"lunch with sam, 88\n",
		"45, kfc\n",
		"6 (bus 26)\n",
		"7\u4ed4 49.5\n",
		"44,\n",
	}
	for _, in := range r1Inputs {
		n := ParseDayNote("2026-11-10.md", "2026-11-10", []byte(in))
		if len(n.Issues) != 0 {
			t.Fatalf("R1 input %q has issues: %+v", in, n.Issues)
		}
		cl := CentralLine("2026-11-10", n.Entries)
		e, iss, _, _ := ParseLoose("bookkeeping.md", []byte(cl+"\n"))
		if len(iss) != 0 {
			t.Fatalf("R1 input %q CentralLine %q ParseLoose issues: %+v", in, cl, iss)
		}
		if len(e) != len(n.Entries) {
			t.Fatalf("R1 input %q entry count mismatch: got %d, want %d", in, len(e), len(n.Entries))
		}
		for i := range e {
			if e[i].Cents != n.Entries[i].Cents {
				t.Errorf("R1 input %q entry %d cents mismatch: %d vs %d", in, i, e[i].Cents, n.Entries[i].Cents)
			}
			if e[i].Note != n.Entries[i].Note {
				t.Errorf("R1 input %q entry %d note mismatch: %q vs %q", in, i, e[i].Note, n.Entries[i].Note)
			}
			if e[i].Category != n.Entries[i].Category {
				t.Errorf("R1 input %q entry %d cat mismatch: %q vs %q", in, i, e[i].Category, n.Entries[i].Category)
			}
		}
	}
}
