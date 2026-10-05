package score

import (
	"math/rand"
	"slices"
	"strings"
	"testing"
)

// TestSlugTokensAndNearDuplicate pins the duplicate-page rule: a slug is its
// file's base name, split on "-" into lowercase tokens with the stop words
// dropped and each all-ASCII-letter token stemmed; two slugs are
// near-duplicates when the Jaccard index of their token sets is at least 0.5.
// The rows are page names the ingest actually produced next to the page
// that already covered them (049).
func TestSlugTokensAndNearDuplicate(t *testing.T) {
	t.Run("tokens", func(t *testing.T) {
		tests := []struct {
			slug string
			want []string
		}{
			{"the-HTTP-versions-vs-quic", []string{"http", "quic", "version"}},
			{"http-http-HTTP", []string{"http"}},
			{"the-of-vs", nil},
			{"", nil},
			{"量子-计算", []string{"计算", "量子"}}, // sorted by byte order, not kept as written
		}
		for _, tc := range tests {
			if got := SlugTokens(tc.slug); !slices.Equal(got, tc.want) {
				t.Errorf("SlugTokens(%q) = %q, want %q", tc.slug, got, tc.want)
			}
		}
		// Empty tokens between and around hyphens are dropped; the stem of
		// "cache" is the library's to spell, so compare with the clean form.
		if got, want := SlugTokens("kv--cache-"), SlugTokens("kv-cache"); len(want) != 2 || !slices.Equal(got, want) {
			t.Errorf(`SlugTokens("kv--cache-") = %q, want %q (two tokens)`, got, want)
		}
	})

	t.Run("near-duplicates", func(t *testing.T) {
		tests := []struct {
			a, b string
			want bool
		}{
			{"glm-5-2-on-m3-ultra", "glm-5-2", true},        // 3 of 5
			{"quaternion", "quaternion-3d-rotation", false}, // 1 of 3
			{"http-versions", "http-version", true},         // stemming
			{"tls", "http", false},
			{"量子-计算", "量子-计算", true},                               // non-ASCII kept as is
			{"kv-cache-eviction", "kv-cache-policy", true},         // 2 of 4: exactly the threshold
			{"kv-cache-eviction", "kv-cache-policy-tuning", false}, // 2 of 5
			{"HTTP-Versions", "http-version", true},
			{"the-cache", "cache", true}, // a stop word is not evidence
			{"the", "of", false},         // no tokens each: nothing to compare
			{"", "", false},
			{"the", "tls", false},
		}
		for _, tc := range tests {
			if got := NearDuplicate(tc.a, tc.b); got != tc.want {
				t.Errorf("NearDuplicate(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
			if got := NearDuplicate(tc.b, tc.a); got != tc.want {
				t.Errorf("NearDuplicate(%q, %q) = %v, want %v (symmetry)", tc.b, tc.a, got, tc.want)
			}
		}
	})
}

// TestLinesKept pins what "a patch lost nothing" means for a page body: every
// non-blank line of the old body is still in the new one, as a multiset
// (a line that was there twice must be there twice), compared after the
// trailing spaces and tabs are trimmed. A line also survives when a patch
// only INSERTED text into it at one point (the end of a "See also" line, the
// middle of a sentence that gained a link). New lines, moved lines and
// blank-line changes do not matter; a dropped, shortened, reworded or
// twice-edited line does (049, A-049-4, A-049-6).
func TestLinesKept(t *testing.T) {
	tests := []struct {
		name          string
		before, after string
		want          bool
	}{
		{"a line added between", "a\nb\n", "a\nX\nb\n", true},
		{"a repeated line lost one copy", "a\na\nb", "a\nb", false},
		{"trailing space and a blank line", "a  \n\nb", "a\nb", true},
		{"the last line dropped", "a\nb", "a\n", false},
		{"nothing before", "", "anything\n", true},
		{"only blank lines before", "\n  \n\t\n", "", true},
		{"lines reordered", "a\nb", "b\na", true},
		{"a repeated line kept twice and one more", "a", "a\na", true},
		{"a repeated line with enough copies", "a\na\nb", "b\na\nx\na", true},
		{"leading whitespace is part of the line", "  a", "a", false},
		{"trailing tab trimmed", "a\t\nb", "a\nb", true},
		{"a line edited", "alpha beta", "alpha gamma", false},
		{"no trailing newline either side", "a\nb", "a\nb", true},
		// A-049-4: a line a patch only APPENDED to is kept — a "See also" line
		// that gained a link is the commonest legitimate patch of a page.
		{"a line appended to", "a link\nb", "a link, [[c]]\nb", true},
		{"one staged line absorbs only one", "a\na", "a, x", false},
		{"a line shrunk", "abc", "ab", false},
		{"a line reworded", "x y", "y x", false},
		{"two lines appended to, each in its own", "a\na", "a, x\na, y", true},
		{"exact match first, so the prefix pass is not starved", "a\na b", "a b\na", true},
		{"the appended line trails spaces", "a link  \nb", "a link, [[c]]  \nb", true},
		{"a blank line is never a prefix to match", "a", "\n\n", false},
		// "a" could take either staged line and "ab" only one: giving "a" the
		// first it finds would strand "ab", though a matching exists.
		{"the longer line claims its staged line first", "a\nab", "abc\naxx", true},
		{"more old lines than staged lines to hold them", "a\nab\nab", "abc\naxx", false},
		{"the staged lines are all taken by the longer lines", "ab\nab\na", "abc\nabd", false},
		{"a different line that merely shares a substring", "bc", "abXc", false},
		// A-049-6: a line is also kept when ONE insertion makes the staged line
		// out of it: staged = P + inserted text + S for a split old = P + S, P or
		// S empty, nothing dropped. The first four rows are the amendment's own.
		{"an insertion in the middle of a sentence", "see [[a]] for x", "see [[a]] and [[b]] for x", true},
		{"two insertion points", "abc", "aXbYc", false},
		{"one changed letter is not an insertion", "abc", "abd", false},
		{"a word inserted", "the cat", "the big cat", true},
		{"an insertion at the front", "cat", "the cat", true},
		{"an insertion at the front, with trailing space", "bc  ", "abc", true},
		{"a doubled space is an insertion", "alpha beta", "alpha  beta", true},
		{"an insertion before the last letter", "abc", "abXc", true},
		{"an insertion that also shortens", "abcd", "aXd", false},
		{"an insertion and a changed tail", "abcd", "abXcZ", false},
		{"a whole line replaced", "abc", "xyz", false},
		{"an overlap of head and tail is not an insertion", "aa", "a", false},
		{"an overlap, longer", "abab", "ab", false},
		{"one staged line absorbs one old line, not two", "ab\ncd", "abcd", false},
		{"each old line in its own inserted line", "a b\na b", "a X b\na Y b", true},
		{"one inserted line for two copies", "a b\na b", "a X b", false},
		// "ab" and "cd" can each take "abcd", and "ab" can take "abzz": handing
		// "abcd" to whichever line is tried first strands the other. Neither the
		// order of the old lines, nor of the staged ones, nor their length may
		// decide it, so the lines are matched, not scanned.
		{"two old lines that share one candidate", "ab\ncd", "abcd\nabzz", true},
		{"the same, staged lines swapped", "ab\ncd", "abzz\nabcd", true},
		{"the same, old lines swapped", "cd\nab", "abcd\nabzz", true},
		// "ac" fits "abc" (b inserted) and "abc" fits "Xabc" (X inserted), but
		// "ac" does not fit "Xabc": an exact match for "abc" must not be final.
		{"an exact match given up for a line that needs it", "ac\nabc", "abc\nXabc", true},
		{"the same, with the staged lines the other way round", "ac\nabc", "Xabc\nabc", true},
		{"a chain of three", "ac\nabc\nabcd", "abcd\nabc\nXabcd", true},
		{"a chain with no line for the last", "ac\nabc\nabcd", "abcd\nabc\nXab", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := LinesKept(tc.before, tc.after); got != tc.want {
				t.Errorf("LinesKept(%q, %q) = %v, want %v", tc.before, tc.after, got, tc.want)
			}
		})
	}
}

// bruteLinesKept is LinesKept's definition, written the slow obvious way: try
// every assignment of the old lines to distinct new lines and ask each pair
// the question with an explicit split old = P + S. It shares no code with the
// matching it checks.
func bruteLinesKept(before, after string) bool {
	var old, cur []string
	for _, l := range strings.Split(before, "\n") {
		if l = strings.TrimRight(l, " \t"); l != "" {
			old = append(old, l)
		}
	}
	for _, l := range strings.Split(after, "\n") {
		cur = append(cur, strings.TrimRight(l, " \t"))
	}
	fits := func(l, m string) bool {
		for k := 0; k <= len(l); k++ {
			if len(m) >= len(l) && strings.HasPrefix(m, l[:k]) && strings.HasSuffix(m, l[k:]) {
				return true
			}
		}
		return false
	}
	used := make([]bool, len(cur))
	var assign func(i int) bool
	assign = func(i int) bool {
		if i == len(old) {
			return true
		}
		for j, m := range cur {
			if !used[j] && fits(old[i], m) {
				used[j] = true
				if assign(i + 1) {
					return true
				}
				used[j] = false
			}
		}
		return false
	}
	return assign(0)
}

// TestLinesKeptMatchesBruteForce checks LinesKept against bruteLinesKept on a
// few thousand small random pages, in both directions of the lines' order: a
// scan that gets the matching wrong passes the hand-picked rows above and
// fails here, and the answer must not depend on the order lines are written
// in (049, A-049-6). The generator is seeded, so a failure is reproducible.
func TestLinesKeptMatchesBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(49))
	word := func() string {
		var b strings.Builder
		for n := 1 + rng.Intn(4); n > 0; n-- {
			b.WriteByte("abX"[rng.Intn(3)])
		}
		return b.String()
	}
	page := func(n int) []string {
		lines := make([]string, n)
		for i := range lines {
			lines[i] = word()
		}
		return lines
	}
	yes, no := 0, 0
	for i := 0; i < 4000; i++ {
		old := page(1 + rng.Intn(4))
		cur := page(1 + rng.Intn(5))
		// Half the time make cur from old by inserting into its lines, so the
		// kept cases are not rare.
		if rng.Intn(2) == 0 {
			cur = nil
			for _, l := range old {
				k, ins := rng.Intn(len(l)+1), word()
				cur = append(cur, l[:k]+ins[:rng.Intn(len(ins)+1)]+l[k:])
			}
			cur = append(cur, page(rng.Intn(2))...)
			rng.Shuffle(len(cur), func(a, b int) { cur[a], cur[b] = cur[b], cur[a] })
			if rng.Intn(3) == 0 {
				cur[rng.Intn(len(cur))] = word() // spoil one
			}
		}
		before, after := strings.Join(old, "\n"), strings.Join(cur, "\n")
		want := bruteLinesKept(before, after)
		if want {
			yes++
		} else {
			no++
		}
		if got := LinesKept(before, after); got != want {
			t.Fatalf("LinesKept(%q, %q) = %v, brute force says %v", old, cur, got, want)
		}
		slices.Reverse(old)
		slices.Reverse(cur)
		if got := LinesKept(strings.Join(old, "\n"), strings.Join(cur, "\n")); got != want {
			t.Fatalf("LinesKept(%q, %q) = %v with both pages reversed, %v as written", old, cur, got, want)
		}
	}
	if yes < 500 || no < 500 {
		t.Fatalf("the generator made %d kept and %d lost pages; the check needs both in number", yes, no)
	}
}
