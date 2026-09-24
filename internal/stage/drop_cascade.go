// drop_cascade.go implements workflow 030 T1: the op dependency graph over
// a changeset's ops and the batch drop/restore verbs the Review UI (T2)
// builds on. The graph answers the live 030 complaint — "I cannot simply
// drop a file in a changeset … we have to accept all changes or decline all
// changes" — by naming, per op, everything that cannot survive without it:
// dropping one op of a dependent chain leaves the later ops stale
// (liveBefore no longer projects their base, engine_changeset.go) and a
// stale op blocks Commit, so the UI must offer the whole doomed set at
// once, after showing the list.
//
// Three relations, from the frozen 030 contract:
//
//	D1  a LATER live op whose opTouches intersects the op's — the 020
//	    dependent-op shape: a chained patch's Before is the previous op's
//	    After, a rename's dependent patches target its To. Chaining is
//	    validated forward (Append checked the later op against the
//	    projection of everything before it), so D1 is later-only.
//	D2  a live op whose post-image NEWLY links [[s]] to a page the op
//	    brings into existence — a wiki/ path it writes that the committed
//	    vault does not hold, so the link only resolves once the op lands.
//	D3  a live op whose post-image cites a raw path the op ingests — via
//	    a frontmatter sources: entry, a "^[<path> …]" body marker (the
//	    shapes cite.Scan owns, paged or not since 034 T3), or
//	    Op.Provenance.
//
// D2 and D3 carry no direction, exactly as the frozen contract's "a live
// op" wording says: the model routinely patches a hub page to add
// [[new-page]] BEFORE the op that creates new-page — append-time
// validation does not reject a not-yet-resolving link (link-broken is a
// lint finding, not an Append refusal) — so dropping that later create
// must take the earlier linking patch too, or the projected tree gains a
// link-broken finding and Review's lint gate refuses the commit.
//
// OpDependents walks the relations over the live ops (what drops WITH an
// op); OpPrerequisites walks their exact mirror over the dropped ops (what
// must come back for a dropped op to be whole again — including the LATER
// create an earlier linking patch needs back). Both take the transitive
// closure, in changeset order.
package stage

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/awepo-pro/lw/internal/cite"
	"github.com/awepo-pro/lw/internal/vault"
)

// OpDependents returns the ids of the live ops that cannot survive dropping
// opID, in changeset order, excluding opID itself: the transitive closure
// over D1 (a later live op whose opTouches intersects), D2 (a live op whose
// post-image newly links [[s]] to a page that only a dependency op of this
// changeset brings into existence) and D3 (a live op whose post-image cites
// a raw path that only a dependency op ingests — via frontmatter sources:,
// a "^[<path> …]" body marker cite.Scan finds, or Op.Provenance). D1 is
// later-only; D2 and D3 fire in both changeset directions (file comment).
// Unknown id → error.
//
// Read-only: it enters through currentOpen like OpDiff and never takes
// writeMu, persists or journals. An op that is already dropped or rejected
// is not a dependent — it cannot go stale again.
func (e *Engine) OpDependents(opID string) ([]string, error) {
	c, err := e.currentOpen()
	if err != nil {
		return nil, err
	}
	target, ok := c.Op(opID)
	if !ok {
		return nil, fmt.Errorf("stage: op dependents: no such op %q", opID)
	}
	seed, ok := e.seedDepNode(c, opID, target)
	if !ok {
		return nil, fmt.Errorf("stage: op dependents: no such op %q", opID)
	}

	cands := e.depNodes(c, func(op Op) bool {
		return op.State != StateDropped && op.State != StateRejected
	})
	return depClosure(seed, cands, func(cand, node depNode) bool {
		return opDependsOn(cand, node)
	}), nil
}

// OpPrerequisites returns the ids of the DROPPED ops that opID builds on
// under the same three relations, transitively, in changeset order,
// excluding opID. Unknown id → error.
//
// The exact mirror of OpDependents: every edge the dependents walk draws is
// tested the other way round, and only dropped ops are collected — a live
// prerequisite needs no restoring, and RestoreOps' re-derived states (the
// refresh inside persistAfterMutation) settle a chained op fresh only when
// the ops it chains on come back with it. The mirror keeps the D2/D3
// directions too: restoring an earlier linking patch must restore the LATER
// create it links to, exactly as dropping that create drops the patch.
func (e *Engine) OpPrerequisites(opID string) ([]string, error) {
	c, err := e.currentOpen()
	if err != nil {
		return nil, err
	}
	target, ok := c.Op(opID)
	if !ok {
		return nil, fmt.Errorf("stage: op prerequisites: no such op %q", opID)
	}
	seed, ok := e.seedDepNode(c, opID, target)
	if !ok {
		return nil, fmt.Errorf("stage: op prerequisites: no such op %q", opID)
	}

	cands := e.depNodes(c, func(op Op) bool { return op.State == StateDropped })
	return depClosure(seed, cands, func(cand, node depNode) bool {
		return opDependsOn(node, cand)
	}), nil
}

// depNode is one walk node: a top-level op (or the seed opID, which may be
// a cascade sub-op), its changeset position, and the derived fact sets its
// D2/D3 edges are tested against. Every fact here is computed ONCE per node
// (depFacts), so the closure's per-pair tests are pure map and substring
// work — no per-pair re-reads of CAS blobs.
type depNode struct {
	id     string
	idx    int             // changeset order: the sort key and D1's later/earlier test
	op     Op              // the op itself; opTouches recurses through Cascade
	brings map[string]bool // D2: wiki/ paths this op writes that the committed vault lacks

	// D3 facts: the raw paths this op ingests, and the three channels an op
	// can cite a raw path through — its own or a cascade sub-op's
	// Provenance, a frontmatter sources: entry in its post-image, a
	// "^[<path> …]" body marker (any page form) cite.Scan finds in its
	// post-image text (034 T3).
	ingested  map[string]bool
	prov      map[string]bool
	citedSrcs map[string]bool
	cited     map[string]bool // D3: sources of the post-image's body markers, cite.Scan's
	newLinks  []string        // D2: deduped wikilink targets newly linked by the post-image
}

// seedDepNode builds the walk's starting node. idx is the changeset index
// of the top-level op whose subtree carries opID — a cascade sub-op shares
// its parent's position, since Append drew its id in the same breath and
// the drop verbs project (or skip) it with that parent.
func (e *Engine) seedDepNode(c *Changeset, opID string, target *Op) (depNode, bool) {
	idx, ok := topLevelIndex(c.Ops, opID)
	if !ok {
		return depNode{}, false
	}
	n := depNode{id: opID, idx: idx, op: *target}
	e.depFacts(&n)
	return n, true
}

// depNodes returns every top-level op in c that keep accepts, in changeset
// order, as walk nodes with their derived facts.
func (e *Engine) depNodes(c *Changeset, keep func(Op) bool) []depNode {
	var out []depNode
	for i := range c.Ops {
		op := c.Ops[i]
		if !keep(op) {
			continue
		}
		n := depNode{id: op.ID, idx: i, op: op}
		e.depFacts(&n)
		out = append(out, n)
	}
	return out
}

// depFacts fills n's derived fact sets, reading each CAS blob exactly once —
// the per-node half of the no-per-pair-re-reads rule. Facts that need no
// blob (brings, ingested, prov) come from the op fields; newLinks, citedSrcs
// and cited come from opPostImageText's single post-image walk.
func (e *Engine) depFacts(n *depNode) {
	n.brings = e.broughtIntoExistence(n.op)

	n.ingested = map[string]bool{}
	n.prov = map[string]bool{}
	var walk func(o Op)
	walk = func(o Op) {
		if o.Kind == OpIngestSource && o.Path != "" {
			n.ingested[o.Path] = true
		}
		for _, r := range o.Provenance {
			n.prov[r] = true
		}
		for _, sub := range o.Cascade {
			walk(sub)
		}
	}
	walk(n.op)

	post, hasPost := e.opPostImageText(n.op)
	if !hasPost {
		return
	}
	if pg, err := vault.ParsePage(n.op.Path, []byte(post)); err == nil {
		n.citedSrcs = make(map[string]bool, len(pg.FM.Sources))
		for _, src := range pg.FM.Sources {
			n.citedSrcs[src] = true
		}
	}
	n.newLinks = e.newlyLinkedTargets(n.op, post)

	// The marker channel goes through cite.Scan (034 T3), not a literal
	// "^["+r+"]" substring: a paged marker — ^[raw/papers/x.md p.12] —
	// cites its source exactly as much as the legacy unpaged shape, and a
	// marker inside a code fence is an example, not a claim, so dropping
	// the ingest must not drag an op that only shows one in sample text.
	n.cited = make(map[string]bool)
	for _, c := range cite.Scan(post) {
		n.cited[c.Source] = true
	}
}

// depClosure walks the transitive closure from seed: every candidate that
// test pairs with a reached node is reached itself and becomes a node in
// turn. The result is the reached candidates' ids in changeset order, seed
// excluded.
func depClosure(seed depNode, cands []depNode, test func(cand, node depNode) bool) []string {
	reached := map[string]bool{seed.id: true}
	queue := []depNode{seed}
	var out []depNode
	for k := 0; k < len(queue); k++ {
		node := queue[k]
		for _, cand := range cands {
			if reached[cand.id] {
				continue
			}
			if !test(cand, node) {
				continue
			}
			reached[cand.id] = true
			out = append(out, cand)
			queue = append(queue, cand)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].idx < out[j].idx })
	ids := make([]string, len(out))
	for i, n := range out {
		ids[i] = n.id
	}
	return ids
}

// opDependsOn reports whether b depends on node: D1 touches-intersect, D2 a
// newly linked wikilink reaching a page node brings into existence, D3 a
// citation of a raw path node ingests.
//
// Only D1 is directional: chaining is validated forward (Append checked the
// later op against the projection of everything before it), so only a LATER
// b chains on an earlier node. D2 and D3 fire in both directions — the
// model patches a hub page to add [[new-page]] before the op that creates
// new-page just as often as after — which is what makes OpPrerequisites the
// exact mirror of OpDependents rather than a direction-flipped copy.
func opDependsOn(b, node depNode) bool {
	if b.idx > node.idx && touchesIntersect(opTouches(b.op), opTouches(node.op)) {
		return true // D1
	}
	for _, t := range b.newLinks {
		for p := range node.brings {
			if linkTargetResolvesTo(t, p) {
				return true // D2
			}
		}
	}
	for r := range node.ingested {
		if b.prov[r] || b.citedSrcs[r] || b.cited[r] {
			return true // D3
		}
	}
	return false
}

// topLevelIndex returns the changeset index of the top-level op whose
// subtree — itself or, recursively, its Cascade — carries id. findOpPtr
// answers the same membership question for Changeset.Op; here the walk
// needs the position, not the pointer.
func topLevelIndex(ops []Op, id string) (int, bool) {
	var subtreeHas func(ops []Op) bool
	subtreeHas = func(ops []Op) bool {
		for _, o := range ops {
			if o.ID == id || subtreeHas(o.Cascade) {
				return true
			}
		}
		return false
	}
	for i := range ops {
		if ops[i].ID == id || subtreeHas(ops[i].Cascade) {
			return i, true
		}
	}
	return -1, false
}

// touchesIntersect reports whether a and b name at least one common path.
func touchesIntersect(a, b []string) bool {
	set := make(map[string]bool, len(a))
	for _, p := range a {
		set[p] = true
	}
	for _, p := range b {
		if set[p] {
			return true
		}
	}
	return false
}

// broughtIntoExistence returns the wiki/ paths op "brings into existence"
// (030 D2): paths op writes — a content op at its Path, a rename or merge
// at its To, recursed through Cascade — that the committed vault does not
// hold. A dependent's wikilink addresses such a page by its slug, the base
// name without .md, which is why D2 resolves links against this set rather
// than against the vault, where the page does not exist yet.
func (e *Engine) broughtIntoExistence(op Op) map[string]bool {
	out := map[string]bool{}
	var walk func(o Op)
	walk = func(o Op) {
		for _, p := range []string{o.Path, o.To} {
			if !opWritesPath(o, p) || !strings.HasPrefix(p, "wiki/") || e.vault.Exists(p) {
				continue
			}
			out[p] = true
		}
		for _, sub := range o.Cascade {
			walk(sub)
		}
	}
	walk(op)
	return out
}

// opWritesPath reports whether op writes post-image bytes at exactly the
// path p, assumed to come from op's own fields. Structural kinds (split,
// add_link, retract) write no post-image bytes themselves.
func opWritesPath(op Op, p string) bool {
	if p == "" {
		return false
	}
	switch op.Kind {
	case OpCreatePage, OpPatchPage, OpIngestSource:
		return p == op.Path
	case OpRenamePage, OpMergePages:
		return p == op.To
	}
	return false
}

// newlyLinkedTargets returns the deduped wikilink targets that appear in
// post — op's concatenated post-image, already fetched by depFacts — but
// not in its pre-image (D2's "newly links"). The pre-image is the Before
// blob the CAS holds (empty for a create, which has no pre-image). An op
// with no readable post-image — a rename, whose moved bytes the CAS never
// stored — newly links nothing; depFacts only calls here when it has one.
func (e *Engine) newlyLinkedTargets(op Op, post string) []string {
	var pre strings.Builder
	var walkPre func(o Op)
	walkPre = func(o Op) {
		if o.Before != "" {
			if b, err := e.store.Get(o.Before); err == nil {
				pre.Write(b)
			}
		}
		for _, sub := range o.Cascade {
			walkPre(sub)
		}
	}
	walkPre(op)

	old := map[string]bool{}
	for _, w := range vault.ParseWikilinks(pre.String()) {
		old[w.Target] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, w := range vault.ParseWikilinks(post) {
		if seen[w.Target] || old[w.Target] {
			continue
		}
		seen[w.Target] = true
		out = append(out, w.Target)
	}
	return out
}

// opPostImageText concatenates op's post-image with every cascade sub-op's,
// recursively — the bytes OpDiff renders for this op. false when any of
// them has no readable post-image.
func (e *Engine) opPostImageText(op Op) (string, bool) {
	var sb strings.Builder
	var walk func(o Op) bool
	walk = func(o Op) bool {
		b, err := e.postImage(o)
		if err != nil {
			return false
		}
		sb.Write(b)
		for _, sub := range o.Cascade {
			if !walk(sub) {
				return false
			}
		}
		return true
	}
	if !walk(op) {
		return "", false
	}
	return sb.String(), true
}

// linkTargetResolvesTo mirrors vault.Resolve's four lookup steps (backbone
// §2.9) for a target that must reach p — a path that does not exist in the
// committed vault, so Resolve itself cannot answer: exact path;
// "<target>.md" as an exact path; basename match, case-insensitively and
// with any ".md" stripped; and "<wiki-subdir>/<target>[.md]".
func linkTargetResolvesTo(target, p string) bool {
	if target == "" {
		return false
	}
	if target == p || target+".md" == p {
		return true
	}
	if strings.EqualFold(strings.TrimSuffix(target, ".md"), strings.TrimSuffix(path.Base(p), ".md")) {
		return true
	}
	t := strings.TrimSuffix(target, ".md")
	if slash := strings.IndexByte(t, '/'); slash >= 0 {
		dir, base := t[:slash], t[slash+1:]
		return dir == strings.TrimPrefix(path.Dir(p), "wiki/") &&
			strings.EqualFold(base, strings.TrimSuffix(path.Base(p), ".md"))
	}
	return false
}

// DropOps marks every listed op StateDropped in one writeMu-held mutation
// and one persist, journaling op_dropped per op (030). Already-dropped ids
// are a no-op; an unknown id fails the whole call with nothing changed —
// every id is resolved before the first state moves, because a half-applied
// batch is exactly the half-dropped chain this verb exists to stop
// offering.
//
// The pattern is DropOp's (A-803): the whole body holds writeMu and
// mutates writerOpen's private copy; the copy is published only by
// persistAfterMutation's persist, after refreshOpStates has re-derived the
// survivors' staleness against the now-smaller live set. Only the ops that
// actually transition journal: a re-drop of an already-dropped id is a
// no-op that persists and journals nothing.
func (e *Engine) DropOps(ids []string) error {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()

	c, err := e.writerOpen()
	if err != nil {
		return err
	}

	seen := make(map[string]bool, len(ids))
	var targets []*Op
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		op, ok := c.Op(id)
		if !ok {
			return fmt.Errorf("stage: drop ops: no such op %q", id)
		}
		targets = append(targets, op)
	}

	var dropped []*Op
	for _, op := range targets {
		if op.State == StateDropped {
			continue
		}
		op.State = StateDropped
		dropped = append(dropped, op)
	}
	if len(dropped) == 0 {
		return nil
	}

	if err := e.persistAfterMutation(c); err != nil {
		return err
	}
	for _, op := range dropped {
		if err := e.appendJournal(Event{
			TS:        e.now().UTC(),
			Kind:      EvOpDropped,
			Changeset: c.ID,
			Op:        op.ID,
			Actor:     c.Author,
			Paths:     opTouches(*op),
		}); err != nil {
			return fmt.Errorf("stage: drop ops: %w", err)
		}
	}
	return nil
}

// RestoreOps returns every listed StateDropped op to StateProposed in one
// mutation, re-derives op states (the refresh inside persistAfterMutation),
// and journals op_restored per op (030, D-30A). A listed op that is not
// dropped is a no-op; an unknown id fails the whole call with nothing
// changed, resolved before the first state moves exactly as DropOps does.
//
// The re-derivation is the verb's honesty: a chained op restored without
// the ops it chains on comes back StateStale, not silently fresh, because
// its Before still names a predecessor the live set no longer projects.
func (e *Engine) RestoreOps(ids []string) error {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()

	c, err := e.writerOpen()
	if err != nil {
		return err
	}

	seen := make(map[string]bool, len(ids))
	var targets []*Op
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		op, ok := c.Op(id)
		if !ok {
			return fmt.Errorf("stage: restore ops: no such op %q", id)
		}
		targets = append(targets, op)
	}

	var restored []*Op
	for _, op := range targets {
		if op.State != StateDropped {
			continue
		}
		op.State = StateProposed
		restored = append(restored, op)
	}
	if len(restored) == 0 {
		return nil
	}

	if err := e.persistAfterMutation(c); err != nil {
		return err
	}
	for _, op := range restored {
		if err := e.appendJournal(Event{
			TS:        e.now().UTC(),
			Kind:      EvOpRestored,
			Changeset: c.ID,
			Op:        op.ID,
			Actor:     c.Author,
			Paths:     opTouches(*op),
		}); err != nil {
			return fmt.Errorf("stage: restore ops: %w", err)
		}
	}
	return nil
}
