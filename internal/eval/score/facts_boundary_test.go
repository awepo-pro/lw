package score

import "testing"

// TestMatchFactWordBoundary pins A-037-6: a literal alternative whose first
// (last) rune is an ASCII letter or digit must sit on a word boundary on that
// side, so a short fact cannot hit inside a longer word or number — "IAM" in
// "diameter", "1.50" in "11.50", "62.1" in "162.1". An edge that is
// punctuation or non-ASCII carries no requirement, and "re:" alternatives
// are the author's own regexp and are left alone (037 T2).
func TestMatchFactWordBoundary(t *testing.T) {
	tests := []struct {
		name string
		text string
		alts []string
		want bool
	}{
		{"IAM is not in diameter", "the diameter is 5 cm", []string{"IAM"}, false},
		{"IAM as a word", "Grant the role in IAM first.", []string{"IAM"}, true},
		{"IAM at the start and end of the text", "IAM", []string{"iam"}, true},
		{"1.50 is not in 11.50", "it costs $11.50 a month", []string{"1.50"}, false},
		{"1.50 is in $1.50", "it costs $1.50 a month", []string{"1.50"}, true},
		{"62.1 is not in 162.1", "a score of 162.1 overall", []string{"62.1"}, false},
		{"62.1 is in 62.1%", "accuracy 62.1% overall", []string{"62.1"}, true},
		{"the right edge too: 1.5 is not in 1.50", "rate 1.50", []string{"1.5"}, false},
		{"the right edge at a sentence end", "the rate is 1.5.", []string{"1.5"}, true},
		{"v4 is not in v45", "deepseek v45", []string{"v4"}, false},
		{"v4 is not in dv4", "the dv4 board", []string{"v4"}, false},
		{"v4 in prose", "deepseek v4, then more", []string{"v4"}, true},
		{"a later occurrence on a boundary still hits", "diameter, then IAM again", []string{"iam"}, true},
		{"every earlier occurrence is inside a word", "diameter and Miami", []string{"iam"}, false},
		{"a suffix is a miss: daemonsets", "uses daemonsets", []string{"daemonset"}, false},
		{"the suffix can be listed as another alternative", "uses daemonsets", []string{"daemonset", "daemonsets"}, true},
		{"a punctuation edge carries no requirement: .net", "built on .NET and more", []string{".net"}, true},
		{"a punctuation edge, glued to a word", "dotnet .NET", []string{".net"}, true},
		{"c++ glued on the left is a miss", "abc++ code", []string{"c++"}, false},
		{"c++ on a boundary", "plain C++ code", []string{"c++"}, true},
		{"a non-ASCII neighbour is not a word: CJK prose without spaces", "使用IAM角色管理", []string{"IAM"}, true},
		{"a non-ASCII edge carries no requirement", "角色管理员", []string{"管理"}, true},
		{"an underscore is not a word character", "the iam_role module", []string{"iam"}, true},
		{"a hyphen is not a word character", "multi-iam setup", []string{"iam"}, true},
		{"a multi-word literal is checked at its outer edges only", "the xdaemonset exporter pods", []string{"daemonset exporter"}, false},
		{"a multi-word literal on its boundaries", "the daemonset exporter pods", []string{"daemonset exporter"}, true},
		{"a regexp alternative is left alone: iam in diameter", "the diameter", []string{"re:iam"}, true},
		{"a regexp may still spell its own boundary", "the diameter", []string{`re:\biam\b`}, false},
		{"whitespace folding still applies", "Grant\n  IAM   access", []string{"iam access"}, true},
		{"curly apostrophes still fold", "the vault doesn’t cover it", []string{"vault doesn't cover"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MatchFact(tc.text, tc.alts)
			if err != nil {
				t.Fatalf("MatchFact(%q, %q): %v", tc.text, tc.alts, err)
			}
			if got != tc.want {
				t.Errorf("MatchFact(%q, %q) = %v, want %v", tc.text, tc.alts, got, tc.want)
			}
		})
	}
}

// TestFactRecallWordBoundary pins that the boundary rule reaches FactRecall:
// a fact that only hits inside a longer word is a missed fact, reported
// with its alternatives as written (037 T2, A-037-6).
func TestFactRecallWordBoundary(t *testing.T) {
	facts := [][]string{{"IAM"}, {"1.50"}, {"62.1", "62.10"}}
	hit, missed, err := FactRecall("the diameter costs 11.50 for 162.1 units", facts)
	if err != nil {
		t.Fatal(err)
	}
	if hit != 0 || len(missed) != 3 {
		t.Errorf("hit %d missed %v; want every fact missed", hit, missed)
	}
	hit, missed, err = FactRecall("Use IAM. It costs $1.50 for 62.1 units.", facts)
	if err != nil {
		t.Fatal(err)
	}
	if hit != 3 || len(missed) != 0 {
		t.Errorf("hit %d missed %v; want all three", hit, missed)
	}
}
