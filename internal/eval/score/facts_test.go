package score

import (
	"reflect"
	"strings"
	"testing"
)

// TestMatchFact pins the fact matcher: literal alternatives after Normalize
// (whitespace collapse + case fold), "re:" alternatives as regexps over that
// same normalised text, and an error only for a regexp that does not
// compile (037 T2). Every case is a mistake the matcher would otherwise make
// silently: a wrapped line missing a literal, an alternative that holds back
// the one that would have hit, a v45 passing for v4.
func TestMatchFact(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		alts    []string
		want    bool
		wantErr bool
	}{
		{"whitespace collapse and case", "The  DaemonSet\nexporter", []string{"daemonset exporter"}, true, false},
		{"tabs and non-breaking spaces collapse too", "the\tdaemonset  exporter", []string{"DaemonSet Exporter"}, true, false},
		{"second alternative hits", "1.6 Trillion params", []string{"1.6T", "1.6 trillion"}, true, false},
		{"no alternative hits", "1.6 billion params", []string{"1.6T", "1.6 trillion"}, false, false},
		{"regexp hits v4", "deepseek v4", []string{`re:\bv[34]\b`}, true, false},
		{"regexp hits v3 in prose", "Compare V3, then see more", []string{`re:\bv[34]\b`}, true, false},
		{"regexp misses v45", "deepseek v45", []string{`re:\bv[34]\b`}, false, false},
		{"regexp is case-insensitive over the normalised text", "Uses TileLang kernels", []string{`re:Tile[A-Z]ang`}, true, false},
		{"bad regexp is an error", "anything", []string{"re:("}, false, true},
		{"bad regexp errors even after an earlier alternative hit", "anything", []string{"anything", "re:("}, false, true},
		{"empty regexp is an error, not a match-all", "anything", []string{"re:"}, false, true},
		{"no alternatives never matches", "anything", nil, false, false},
		{"an empty literal alternative never matches", "anything", []string{"", "  "}, false, false},
		{"curly apostrophe in the text matches a straight one in the fact", "the vault doesn’t cover it", []string{"vault doesn't cover"}, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MatchFact(tc.text, tc.alts)
			if (err != nil) != tc.wantErr {
				t.Fatalf("MatchFact(%q, %q) err = %v, wantErr %v", tc.text, tc.alts, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("MatchFact(%q, %q) = %v, want %v", tc.text, tc.alts, got, tc.want)
			}
		})
	}
}

// TestNormalize pins the one fold every comparison in this package rides on:
// lower-case, whitespace runs to one space, ends trimmed, typographic
// apostrophes straightened (037 T2).
func TestNormalize(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"  \n\t ", ""},
		{"The  DaemonSet\nexporter", "the daemonset exporter"},
		{"  Not From Your Vault:  x ", "not from your vault: x"},
		{"vault doesn’t cover", "vault doesn't cover"},
		{"語　語", "語 語"},
	}
	for _, tc := range tests {
		if got := Normalize(tc.in); got != tc.want {
			t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestFactRecall scores three facts of which two hit: the hit count is 2 and
// missed carries the missing fact's alternatives exactly as the eval set
// wrote them — the report prints them, so a re-ordered or normalised copy
// would misdescribe the set (037 T2).
func TestFactRecall(t *testing.T) {
	facts := [][]string{
		{"tilelang"},
		{"1.6T", "1.6 trillion"},
		{`re:\bv[34]\b`, "version four"},
	}
	text := "DeepSeek-V4 is built on TileLang kernels and has 1.6 Trillion params."

	t.Run("two of three hit", func(t *testing.T) {
		hit, missed, err := FactRecall(text, [][]string{facts[0], facts[1], {"expert parallelism", "all-to-all"}})
		if err != nil {
			t.Fatal(err)
		}
		if hit != 2 {
			t.Errorf("hit = %d, want 2", hit)
		}
		want := [][]string{{"expert parallelism", "all-to-all"}}
		if !reflect.DeepEqual(missed, want) {
			t.Errorf("missed = %#v, want %#v", missed, want)
		}
	})

	t.Run("all hit leaves missed empty", func(t *testing.T) {
		hit, missed, err := FactRecall(text, facts)
		if err != nil {
			t.Fatal(err)
		}
		if hit != 3 || len(missed) != 0 {
			t.Errorf("hit = %d, missed = %#v, want 3 and none", hit, missed)
		}
	})

	t.Run("no facts is zero recall, not an error", func(t *testing.T) {
		hit, missed, err := FactRecall(text, nil)
		if err != nil || hit != 0 || len(missed) != 0 {
			t.Errorf("FactRecall(nil) = %d, %#v, %v", hit, missed, err)
		}
	})

	t.Run("a bad regexp fails the whole recall and names the fact", func(t *testing.T) {
		_, _, err := FactRecall(text, [][]string{facts[0], {"re:("}})
		if err == nil {
			t.Fatal("want an error for the bad regexp")
		}
		if !strings.Contains(err.Error(), "fact 1") {
			t.Errorf("err = %q, want it to name the failing fact (fact 1)", err)
		}
	})
}
