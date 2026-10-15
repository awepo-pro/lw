package vault

import (
	"maps"
	"path"
	"slices"
	"strings"
)

// ResolveAmong is Resolve over an arbitrary set of page paths instead of a
// vault's committed pages (060 D1): the same four steps of backbone §2.9 in
// the same order — exact path; target+".md" (only when target carries no
// ".md" suffix); a unique case-insensitive basename match with ".md" stripped
// from both sides; "<dir>/<target>.md" for each distinct parent directory of
// paths, unique. Ambiguity at a step fails that step and falls through to the
// next, exactly as Resolve does, and an empty target resolves to nothing.
//
// It exists for the paths only the open changeset holds. A page staged by
// stage.create_page is in no snapshot, so wiki.get could not find a page the
// model had just written itself, and the 2026-10-08 joined recompile turn
// spent its 24 rounds on six not-found reads of exactly such pages. The
// caller supplies the set; a path listed twice is one page (the staged
// create and a later patch of it both name the path), so duplicates never
// make a basename ambiguous.
//
// snapshot.resolve is deliberately not rebuilt on top of this: the committed
// lookup is the hot path and stays as it was. TestResolveAmongMatchesResolve
// pins the two against each other instead.
func ResolveAmong(paths []string, target string) (string, bool) {
	if target == "" {
		return "", false
	}
	// Dedupe and order once: membership for the exact steps, a sorted slice
	// for the scans, so no step depends on map iteration order.
	set := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		set[p] = struct{}{}
	}
	uniq := slices.Sorted(maps.Keys(set))

	if _, ok := set[target]; ok {
		return target, true
	}
	if !strings.HasSuffix(target, ".md") {
		if candidate := target + ".md"; hasPath(set, candidate) {
			return candidate, true
		}
	}

	want := strings.ToLower(strings.TrimSuffix(target, ".md"))
	var matches []string
	for _, p := range uniq {
		if strings.ToLower(strings.TrimSuffix(path.Base(p), ".md")) == want {
			matches = append(matches, p)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}

	t := strings.TrimSuffix(target, ".md")
	// Sorted paths do not keep one directory's files adjacent (a nested
	// "wiki/a/b/q.md" sorts between two "wiki/a/" files), so distinct
	// directories are tracked by name, not by neighbour.
	matches = matches[:0]
	seenDir := make(map[string]bool)
	for _, p := range uniq {
		dir := path.Dir(p)
		if seenDir[dir] {
			continue
		}
		seenDir[dir] = true
		if candidate := dir + "/" + t + ".md"; hasPath(set, candidate) {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return "", false
}

func hasPath(set map[string]struct{}, p string) bool {
	_, ok := set[p]
	return ok
}
