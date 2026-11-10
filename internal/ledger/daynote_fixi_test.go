package ledger

import (
	"testing"
)

// TestDayNoteFixI_I1_TrailingPunctuation tests I1 (082 A1):
// a note-first item's note loses trailing ',', ';', ':' characters:
// "lunch with sam, 88" -> note "lunch with sam".
func TestDayNoteFixI_I1_TrailingPunctuation(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantNote string
		wantCent int64
		wantTag  string
	}{
		{
			name:     "trailing comma",
			input:    "lunch with sam, 88\n",
			wantNote: "lunch with sam",
			wantCent: -8800,
		},
		{
			name:     "trailing semicolon",
			input:    "coffee with alex; 25\n",
			wantNote: "coffee with alex",
			wantCent: -2500,
		},
		{
			name:     "trailing colon",
			input:    "dinner at home: 42.50\n",
			wantNote: "dinner at home",
			wantCent: -4250,
		},
		{
			name:     "trailing comma with tag",
			input:    "lunch with sam, 88 #food\n",
			wantNote: "lunch with sam",
			wantCent: -8800,
			wantTag:  "food",
		},
		{
			name:     "mixed trailing punctuation and spaces",
			input:    "kfc, ; : 45\n",
			wantNote: "kfc",
			wantCent: -4500,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			note := ParseDayNote("2026-11-10.md", "2026-11-10", []byte(tc.input))
			if len(note.Issues) > 0 {
				t.Fatalf("unexpected issues: %v", note.Issues)
			}
			if len(note.Entries) != 1 {
				t.Fatalf("got %d entries, want 1", len(note.Entries))
			}
			e := note.Entries[0]
			if e.Note != tc.wantNote {
				t.Errorf("note = %q, want %q", e.Note, tc.wantNote)
			}
			if e.Cents != tc.wantCent {
				t.Errorf("cents = %d, want %d", e.Cents, tc.wantCent)
			}
			if e.Category != tc.wantTag {
				t.Errorf("tag = %q, want %q", e.Category, tc.wantTag)
			}
			cl := CentralLine("2026-11-10", note.Entries)
			wantSub := "(" + tc.wantNote + ")"
			if !stringsContains(cl, wantSub) {
				t.Errorf("CentralLine %q does not contain %q", cl, wantSub)
			}
		})
	}
}

func stringsContains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (s == sub || searchSub(s, sub)))
}

func searchSub(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
