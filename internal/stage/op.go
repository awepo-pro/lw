// op.go holds Op-level helpers shared by validate.go and
// engine_changeset.go: the op<N>-id tree walk DropOp/DropHunk/Current need
// (backbone §5.4), the per-kind path set an op touches, canonical hashing
// and Refresh's per-kind staleness table (D-AJ, D-BC), hunk application for
// DropHunk, and the cascade construction algorithm backbone §5.5 calls "how
// a cascade is actually built" (MASTER §9 D-AM, D-BE).
package stage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/awepo-pro/lw/internal/vault"
)

// opIDPattern matches a well-formed op<N> id.
var opIDPattern = regexp.MustCompile(`^op([0-9]+)$`)

// findOpPtr searches ops, and recursively every op's Cascade, for the Op
// whose ID equals id, returning a pointer into the real slice so callers
// (DropOp, DropHunk) can mutate it in place.
func findOpPtr(ops []Op, id string) *Op {
	for i := range ops {
		if ops[i].ID == id {
			return &ops[i]
		}
		if p := findOpPtr(ops[i].Cascade, id); p != nil {
			return p
		}
	}
	return nil
}

// maxOpN returns the highest N over every "op<N>" id in ops, recursively
// through Cascade — 0 if none match. Current (engine_changeset.go) uses
// 1+maxOpN to derive e.nextOp on rehydration (backbone §5.4, MASTER §9
// D-BB).
func maxOpN(ops []Op) int {
	max := 0
	for _, op := range ops {
		if m := opIDPattern.FindStringSubmatch(op.ID); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n > max {
				max = n
			}
		}
		if n := maxOpN(op.Cascade); n > max {
			max = n
		}
	}
	return max
}

// opTouches returns the vault-relative paths op itself affects — Path,
// From, To, every entry of Sources, whichever are non-empty — plus,
// recursively, every path its Cascade entries touch. Used by
// Changeset.Touches and by the journal Paths carried on op_proposed /
// op_dropped / hunk_dropped events.
func opTouches(op Op) []string {
	var out []string
	if op.Path != "" {
		out = append(out, op.Path)
	}
	if op.From != "" {
		out = append(out, op.From)
	}
	if op.To != "" {
		out = append(out, op.To)
	}
	out = append(out, op.Sources...)
	for _, sub := range op.Cascade {
		out = append(out, opTouches(sub)...)
	}
	return out
}

// sortedSet returns the keys of set, sorted.
func sortedSet(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// canonicalContent returns path's current canonical bytes as they stand in
// v: Page.Serialize() for a wiki page (backbone §5.1, MASTER §9 D-AE —
// always Serialize()'d bytes, never the raw disk bytes), or the raw file
// bytes for a vault-root file with no page structure (index.md,
// curator-memory.md — §2 defines no canonical form for them). false when
// path does not exist in v.
func canonicalContent(v *vault.Vault, p string) ([]byte, bool) {
	if pg, ok := v.Page(p); ok {
		return pg.Serialize(), true
	}
	b, err := v.Read(p)
	if err != nil {
		return nil, false
	}
	return b, true
}

// canonicalSHA returns the hex sha256 of canonicalContent(v, p).
func canonicalSHA(v *vault.Vault, p string) (string, bool) {
	b, ok := canonicalContent(v, p)
	if !ok {
		return "", false
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), true
}

// refreshOp applies the per-kind staleness table to op (and recursively to
// its Cascade), skipping an op that is already Dropped or Rejected — those
// are terminal review decisions Refresh must not disturb.
//
// Contract (backbone §5.4 per-kind staleness table, MASTER §9 D-AJ, D-BC):
//
//	patch_page                        the path's current canonical sha != Before
//	create_page, ingest_source        the target path now exists
//	rename_page, merge_pages,         any source page's current canonical sha
//	  split_page                      != the sha captured in SourceSHAs
//	add_link                          either endpoint no longer exists
//	retract                           the path no longer exists
//
// It only ever flips State to StateStale — Hunks and their Dropped flags
// are left exactly as last computed (D-AJ).
func refreshOp(op *Op, v *vault.Vault) {
	if op.State == StateDropped || op.State == StateRejected {
		return
	}

	stale := false
	switch op.Kind {
	case OpPatchPage:
		cur, ok := canonicalSHA(v, op.Path)
		stale = !ok || cur != op.Before
	case OpCreatePage, OpIngestSource:
		stale = v.Exists(op.Path)
	case OpRenamePage:
		stale = sourcesChanged(v, []string{op.From}, op.SourceSHAs)
	case OpMergePages:
		stale = sourcesChanged(v, op.Sources, op.SourceSHAs)
	case OpSplitPage:
		stale = sourcesChanged(v, []string{op.Path}, op.SourceSHAs)
	case OpAddLink:
		stale = !v.Exists(op.From) || !v.Exists(op.To)
	case OpRetract:
		stale = !v.Exists(op.Path)
	}
	if stale {
		op.State = StateStale
	}

	for i := range op.Cascade {
		refreshOp(&op.Cascade[i], v)
	}
}

// sourcesChanged reports whether any of sources' current canonical sha
// differs from the corresponding entry captured in shas at Append time
// (positional, backbone §5.3 D-BC). A length mismatch — shas was never
// captured, or a source vanished from the slice — is itself staleness.
func sourcesChanged(v *vault.Vault, sources, shas []string) bool {
	if len(sources) != len(shas) {
		return true
	}
	for i, src := range sources {
		cur, ok := canonicalSHA(v, src)
		if !ok || cur != shas[i] {
			return true
		}
	}
	return false
}

// applyHunks reconstructs a patch_page op's projected content by applying
// its non-dropped hunks, in order, to before's lines (backbone §5.4
// DropHunk Contract: "recomputes the op's projected content from Before
// plus its remaining live hunks").
//
// This is deliberately minimal, not diff.ComputeHunks' inverse — that
// generic Myers/LCS machinery lives in a later wave's diff.go (S2-T5,
// §5.6), and this subtask's non-goals explicitly exclude hunk computation.
// Every hunk this package itself constructs (buildCascadeHunks) pairs each
// Del line with the Add line it becomes, one pair per hunk, so applying a
// hunk is "find this old line, replace it with this new line"; a hunk
// with more Del than Add entries removes the extras, and one with more Add
// than Del inserts the extras after the last matched position (or at the
// end of the body if nothing matched).
func applyHunks(before []byte, hunks []Hunk) []byte {
	lines := strings.Split(string(before), "\n")
	for _, h := range hunks {
		if h.Dropped {
			continue
		}
		n := len(h.Del)
		if len(h.Add) > n {
			n = len(h.Add)
		}
		pos := len(lines)
		for i := 0; i < n; i++ {
			switch {
			case i < len(h.Del) && i < len(h.Add):
				if idx := indexOfLine(lines, h.Del[i]); idx >= 0 {
					lines[idx] = h.Add[i]
					pos = idx + 1
				}
			case i < len(h.Del):
				if idx := indexOfLine(lines, h.Del[i]); idx >= 0 {
					lines = append(lines[:idx], lines[idx+1:]...)
					pos = idx
				}
			default:
				ins := h.Add[i]
				tail := append([]string{ins}, lines[pos:]...)
				lines = append(lines[:pos], tail...)
				pos++
			}
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

// indexOfLine returns the index of the first element of lines equal to s,
// or -1.
func indexOfLine(lines []string, s string) int {
	for i, l := range lines {
		if l == s {
			return i
		}
	}
	return -1
}

// cascadeRoots are the two vault-root files a cascade must also cover,
// beyond Graph().Backlinks (backbone §5.5, MASTER §9 D-BE): index.md
// links to every page in the vault by construction, and BuildGraph walks
// Vault.Pages() only, so it structurally cannot see either file's links.
// log.md (history) and SCHEMA.md (a prose example, not a link) are
// deliberately excluded.
var cascadeRoots = []string{"index.md", "curator-memory.md"}

// cascadeLinkingPages returns the sorted, deduped set of paths that must
// carry a cascade rewrite when every page in froms is retargeted: every
// page with an inbound backlink to any of froms (Graph().Backlinks, deduped
// by Ref.From), plus cascadeRoots when they wikilink to one of froms
// (backbone §5.5 Contract, MASTER §9 D-AM, D-BE).
func cascadeLinkingPages(v *vault.Vault, froms []string) []string {
	fromSet := make(map[string]bool, len(froms))
	for _, f := range froms {
		fromSet[f] = true
	}

	set := map[string]bool{}
	g := v.Graph()
	for _, f := range froms {
		for _, ref := range g.Backlinks(f) {
			set[ref.From] = true
		}
	}

	for _, root := range cascadeRoots {
		b, err := v.Read(root)
		if err != nil {
			continue // no such root file in this vault: nothing to cascade
		}
		for _, w := range vault.ParseWikilinks(string(b)) {
			if resolved, ok := vault.Resolve(v, w.Target); ok && fromSet[resolved] {
				set[root] = true
			}
		}
	}

	return sortedSet(set)
}

// reverseAddressing recomputes the spelling a rewritten wikilink target
// should use, preserving the original addressing style the link used
// (backbone §5.5 Contract, MASTER §9 D-AM). target is known to resolve to
// from (the caller already checked that via vault.Resolve); to is the
// page from is being retargeted to. This reverses whichever of Resolve's
// four branches matched, in Resolve's own order, without re-implementing
// Resolve's vault-wide search — it is four string comparisons against one
// known path (from), not a second copy of that search, so it cannot drift
// from it.
func reverseAddressing(target, from, to string) string {
	switch {
	case target == from:
		// exact vault-relative path
		return to
	case target+".md" == from:
		// "<t>.md" form
		return strings.TrimSuffix(to, ".md")
	case target == path.Base(from):
		// bare basename, exact (including ".md")
		return path.Base(to)
	case strings.EqualFold(strings.TrimSuffix(target, ".md"), strings.TrimSuffix(path.Base(from), ".md")):
		// bare basename, case-insensitive, ".md" optional — resolveByBasename's
		// own match rule. Re-emitted in the new page's case.
		return strings.TrimSuffix(path.Base(to), ".md")
	default:
		// "<dir>/<t>[.md]" form: keep target's own directory prefix, swap in
		// the new page's base name.
		dir := path.Dir(target)
		newBase := strings.TrimSuffix(path.Base(to), ".md")
		if strings.HasSuffix(target, ".md") {
			newBase += ".md"
		}
		if dir == "." {
			return newBase
		}
		return dir + "/" + newBase
	}
}

// buildCascadeHunks diffs oldBody against newBody line by line and returns
// one Hunk per changed line ("h1", "h2", … in body order), each pairing the
// single old line it replaces (Del) with the single new line it becomes
// (Add). RewriteWikilinks only ever substitutes text within a line — it
// never inserts or removes a "\n" — so oldBody and newBody always have the
// same line count and a positional line-by-line diff is exact, not an
// approximation of a general LCS diff (which is diff.go's job, a later
// wave — see applyHunks).
func buildCascadeHunks(p, oldBody, newBody string) []Hunk {
	oldLines := strings.Split(oldBody, "\n")
	newLines := strings.Split(newBody, "\n")
	n := len(oldLines)
	if len(newLines) < n {
		n = len(newLines)
	}
	var hunks []Hunk
	id := 1
	for i := 0; i < n; i++ {
		if oldLines[i] == newLines[i] {
			continue
		}
		hunks = append(hunks, Hunk{
			ID:   fmt.Sprintf("h%d", id),
			Path: p,
			Del:  []string{oldLines[i]},
			Add:  []string{newLines[i]},
		})
		id++
	}
	return hunks
}

// buildCascadeOp builds the single cascade Op rewriting p's inbound links
// to any page in froms, retargeting them to to. It returns nil, nil when p
// turns out to need no rewrite (defensive; cascadeLinkingPages should never
// produce a false positive, but a op that ends up a no-op must never be
// silently included as an empty edit).
func buildCascadeOp(v *vault.Vault, p string, froms []string, to string) (*Op, error) {
	fromSet := make(map[string]bool, len(froms))
	for _, f := range froms {
		fromSet[f] = true
	}
	rewrite := func(oldBody string, links []vault.Wikilink) (string, bool) {
		newBody := vault.RewriteWikilinks(oldBody, links, func(w vault.Wikilink) (string, bool) {
			resolved, ok := vault.Resolve(v, w.Target)
			if !ok || !fromSet[resolved] {
				return "", false
			}
			return reverseAddressing(w.Target, resolved, to), true
		})
		return newBody, newBody != oldBody
	}

	if page, ok := v.Page(p); ok {
		newBody, changed := rewrite(page.Body, page.Links)
		if !changed {
			return nil, nil
		}
		before := page.SHA256()
		rewritten := *page
		rewritten.Body = newBody
		content := rewritten.Serialize()
		return &Op{
			Kind:    OpPatchPage,
			Path:    p,
			Before:  before,
			Content: content,
			Hunks:   buildCascadeHunks(p, page.Body, newBody),
		}, nil
	}

	// A vault-root file with no page/frontmatter structure (index.md,
	// curator-memory.md): rewrite the whole raw content directly.
	raw, err := v.Read(p)
	if err != nil {
		return nil, fmt.Errorf("stage: build cascade: read %s: %w", p, err)
	}
	links := vault.ParseWikilinks(string(raw))
	newContent, changed := rewrite(string(raw), links)
	if !changed {
		return nil, nil
	}
	sum := sha256.Sum256(raw)
	before := hex.EncodeToString(sum[:])
	return &Op{
		Kind:    OpPatchPage,
		Path:    p,
		Before:  before,
		Content: []byte(newContent),
		Hunks:   buildCascadeHunks(p, string(raw), newContent),
	}, nil
}

// buildCascade computes the complete cascade for retargeting every page in
// froms to to: one patch_page Op per linking page returned by
// cascadeLinkingPages, covering every inbound backlink plus the two
// vault-root files (backbone §5.5, MASTER §9 D-AM, D-BE). Sub-ops carry no
// Section (D-BD: a cascade rewrite is whole-body) and their post-image
// bytes travel in Content exactly like any other Op (D-AY) — Append's
// generic content-storage step Store.Puts them and assigns their op<N> ids
// alongside the top-level op's (D-AK).
func buildCascade(v *vault.Vault, froms []string, to string) ([]Op, error) {
	pages := cascadeLinkingPages(v, froms)

	var ops []Op
	for _, p := range pages {
		op, err := buildCascadeOp(v, p, froms, to)
		if err != nil {
			return nil, err
		}
		if op != nil {
			ops = append(ops, *op)
		}
	}
	return ops, nil
}
