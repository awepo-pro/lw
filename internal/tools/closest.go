package tools

import (
	"cmp"
	"path"
	"slices"
	"strings"
	"unicode"

	"github.com/awepo-pro/lw/internal/stage"
)

// closest.go is 036 D1: when a tool refuses a path the model guessed, name
// the paths that exist and resemble it. A live session hunted one raw source
// through thirteen guesses (.html, papers/, sources/, /index.md …) while the
// vault held the real path the whole time — the tool had the fact and the
// message withheld it. The matcher is deliberately crude (a longest shared
// run of letters and digits in the file name, not an edit distance): the
// failures it must catch are a wrong directory, a dropped hyphen and an
// invented extension, all of which leave a long run of the real name intact,
// and a crude rule is one a model can predict and a test can pin.

// closestMinScore is the shortest shared run, in runes, that counts as a
// resemblance. Three runes match too much of English ("the", "ing", "ion");
// four keeps "cache" and "tile" while turning "zz" and "doe" away.
const closestMinScore = 4

// closestMax caps how many candidates a message offers: enough to cover a
// directory typo plus its near neighbours, few enough that the clause stays
// one line and the model reads it rather than skims it.
const closestMax = 3

// squash reduces a path to what its file name means: the base name without
// its extension, lowercased, keeping only letters and digits. Hyphens,
// underscores, dots and case are exactly the spelling a guess gets wrong
// while the name stays the same ("leviathan2023" for "leviathan-2023"), so
// they must not count against a match. Runes, not bytes: a CJK file name is
// as many characters as it looks, and bytes would let a lone two-character
// word clear the threshold.
func squash(p string) []rune {
	base := path.Base(p)
	base = strings.TrimSuffix(base, path.Ext(base))
	out := make([]rune, 0, len(base))
	for _, r := range base {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out = append(out, unicode.ToLower(r))
		}
	}
	return out
}

// longestCommonRun is the length in runes of the longest substring a and b
// share. It keeps two rows of the classic table, so the cost is
// O(len(a)*len(b)) time and O(len(b)) space — file names are short, and a
// vault of thousands of pages still costs a few million cell updates per
// refusal.
func longestCommonRun(a, b []rune) int {
	best := 0
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				cur[j] = prev[j-1] + 1
				best = max(best, cur[j])
			} else {
				cur[j] = 0
			}
		}
		prev, cur = cur, prev
	}
	return best
}

// closestPaths returns at most closestMax of the paths in have that resemble
// want, best first.
//
// A path qualifies when its squashed name equals want's (the same name under
// another directory, extension or spelling) or shares a run of at least
// closestMinScore runes with it. Order is: equal squash first, then longer
// shared run, then path ascending — the last key makes the answer a pure
// function of the two inputs, not of the order a vault listing or a
// changeset happened to produce them in. want itself is never offered (a
// message saying "X was not found; closest: X" would be absurd), and a path
// listed twice — a committed page the open changeset also stages — is
// offered once.
//
// An empty squash equals nothing, not every other empty squash: a name made
// only of punctuation ("---.md") says nothing about what the model meant,
// and offering every other punctuation-only file would be an arbitrary
// answer presented as advice. have is read, never reordered.
func closestPaths(want string, have []string) []string {
	wantSquash := squash(want)
	type candidate struct {
		path  string
		equal bool
		score int
	}
	var found []candidate
	seen := make(map[string]bool, len(have))
	for _, h := range have {
		if h == want || seen[h] {
			continue
		}
		seen[h] = true
		hs := squash(h)
		equal := len(wantSquash) > 0 && slices.Equal(wantSquash, hs)
		score := longestCommonRun(wantSquash, hs)
		if !equal && score < closestMinScore {
			continue
		}
		found = append(found, candidate{path: h, equal: equal, score: score})
	}
	slices.SortFunc(found, func(x, y candidate) int {
		if x.equal != y.equal {
			if x.equal {
				return -1
			}
			return 1
		}
		if c := cmp.Compare(y.score, x.score); c != 0 {
			return c
		}
		return cmp.Compare(x.path, y.path)
	})
	if len(found) > closestMax {
		found = found[:closestMax]
	}
	var out []string
	for _, c := range found {
		out = append(out, c.path)
	}
	return out
}

// stagedPaths lists the vault-relative paths the open changeset's live
// top-level ops of kind propose — the paths that exist only inside the
// changeset, which the committed vault cannot offer. Live(), not Ops: a
// dropped op is gone from what Commit would write, so offering its path
// would point the model at a file that will never exist. No engine, no open
// changeset or a failed read all mean "nothing staged": this is advice
// appended to a refusal, and a hint that cannot be computed must not turn
// the refusal into something else.
func stagedPaths(d Deps, kind stage.OpKind) []string {
	if d.Engine == nil {
		return nil
	}
	cs, err := d.Engine.Current()
	if err != nil {
		return nil
	}
	var out []string
	for _, op := range cs.Live() {
		if op.Kind == kind && op.Path != "" {
			out = append(out, op.Path)
		}
	}
	return out
}

// closestRawSources are the raw sources that resemble want: every committed
// raw source plus every one the open changeset stages with
// stage.ingest_source. A model that just ingested a source and mistypes its
// path is guessing at a file only the changeset holds.
func closestRawSources(d Deps, want string) []string {
	var have []string
	if d.Vault != nil {
		for _, r := range d.Vault.RawSources() {
			have = append(have, r.Path)
		}
	}
	have = append(have, stagedPaths(d, stage.OpIngestSource)...)
	return closestPaths(want, have)
}

// closestPageList are the pages that resemble want: every committed page
// plus every one the open changeset creates with stage.create_page.
func closestPageList(d Deps, want string) []string {
	var have []string
	if d.Vault != nil {
		for _, p := range d.Vault.Pages() {
			have = append(have, p.Path)
		}
	}
	have = append(have, stagedPaths(d, stage.OpCreatePage)...)
	return closestPaths(want, have)
}

// closestClause renders the "; closest <what>: a, b" clause a not-found
// message appends — or "" when there is no candidate, so a refusal nothing
// resembles keeps its pre-036 text byte for byte. The leading "; " matches
// the separator every clause of those messages already uses.
func closestClause(what string, paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	return "; closest " + what + ": " + strings.Join(paths, ", ")
}

// closestPagesClause is closestClause for pages: the one call the
// stage.patch_page and stage.add_link refusals share.
func closestPagesClause(d Deps, want string) string {
	return closestClause("pages", closestPageList(d, want))
}
