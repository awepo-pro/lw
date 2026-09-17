package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"unicode/utf8"
)

// textCases is testdata/frozen/text-cases.json's shape: one Wrap case and
// one Clip case per row.
type textCases struct {
	Wrap []struct {
		Text  string   `json:"text"`
		Width int      `json:"width"`
		Hang  int      `json:"hang"`
		Want  []string `json:"want"`
	} `json:"wrap"`
	Clip []struct {
		Text  string `json:"text"`
		Width int    `json:"width"`
		Want  string `json:"want"`
	} `json:"clip"`
}

// TestTextCases covers every case in text-cases.json (contract §5's Clip
// and Wrap, ported from mockgen.clip / mockgen.wrap).
func TestTextCases(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "frozen", "text-cases.json"))
	if err != nil {
		t.Fatalf("read text-cases.json: %v", err)
	}
	var cases textCases
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatalf("unmarshal text-cases.json: %v", err)
	}

	for _, c := range cases.Wrap {
		t.Run("wrap", func(t *testing.T) {
			got := Wrap(c.Text, c.Width, c.Hang)
			if !reflect.DeepEqual(got, c.Want) {
				t.Errorf("Wrap(%q, %d, %d) = %#v, want %#v", c.Text, c.Width, c.Hang, got, c.Want)
			}
		})
	}
	for _, c := range cases.Clip {
		t.Run("clip", func(t *testing.T) {
			got := Clip(c.Text, c.Width)
			if got != c.Want {
				t.Errorf("Clip(%q, %d) = %q, want %q", c.Text, c.Width, got, c.Want)
			}
		})
	}
}

// TestShortID pins the one changeset-id truncation shared by the frame
// header (cs9) and, from 005 W1, the Ask panel title (005 contract §6): at
// most 9 runes, cut so it can never emit a broken UTF-8 sequence. For hex
// ids — the only kind real changesets have — the rune rule is
// byte-identical to the inline cs9[:9] it replaces, so no grid moves.
func TestShortID(t *testing.T) {
	t.Run("empty_stays_empty", func(t *testing.T) {
		if got := ShortID(""); got != "" {
			t.Errorf("ShortID(\"\") = %q, want \"\"", got)
		}
	})
	t.Run("shorter_unchanged", func(t *testing.T) {
		if got := ShortID("cs123"); got != "cs123" {
			t.Errorf("ShortID(\"cs123\") = %q, want \"cs123\"", got)
		}
	})
	t.Run("exactly_nine_unchanged", func(t *testing.T) {
		const id = "cs1a2b3c4"
		if got := ShortID(id); got != id {
			t.Errorf("ShortID(%q) = %q, want unchanged", id, got)
		}
	})
	t.Run("longer_truncated_to_nine", func(t *testing.T) {
		const id = "cs1a2b3c4d5e6f708192a3b4c5d6e7f80"
		const want = "cs1a2b3c4"
		got := ShortID(id)
		if got != want {
			t.Errorf("ShortID(%q) = %q, want %q", id, got, want)
		}
		if len(got) != 9 {
			t.Errorf("ShortID(%q) length = %d, want 9", id, len(got))
		}
	})
	t.Run("multibyte_never_split_mid_rune", func(t *testing.T) {
		const id = "csätefst ïdëntïfïer"
		got := ShortID(id)
		if !utf8.ValidString(got) {
			t.Fatalf("ShortID(%q) = %q, which is not valid UTF-8", id, got)
		}
		if n := utf8.RuneCountInString(got); n != 9 {
			t.Errorf("ShortID(%q) rune count = %d, want 9", id, n)
		}
	})
}
