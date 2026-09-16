package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
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
