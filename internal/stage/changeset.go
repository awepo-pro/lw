// changeset.go holds the backbone §5.3 types — Op, Hunk, Author, Checks,
// Changeset, OpKind, OpState — and the Changeset methods (Op, Live,
// Touches). S2-T1 created this file with the type block only, verbatim,
// because engine.go (which it owns) names *Changeset in its field list and
// Go declares a struct's fields exactly once (MASTER §9 D-AQ). Ownership
// transferred to S2-T2 at wave 3, which added Content and SourceSHAs to Op
// (D-AY, D-BC) and the three Changeset methods below.
package stage

import "time"

// OpKind is the kind of edit an Op proposes.
type OpKind string

const (
	OpIngestSource OpKind = "ingest_source"
	OpCreatePage   OpKind = "create_page"
	OpPatchPage    OpKind = "patch_page"
	OpRenamePage   OpKind = "rename_page"
	OpMergePages   OpKind = "merge_pages"
	OpSplitPage    OpKind = "split_page"
	OpAddLink      OpKind = "add_link"
	OpRetract      OpKind = "retract"
)

// OpState is where an Op currently stands in review.
type OpState string

const (
	StateProposed OpState = "proposed"
	StateAccepted OpState = "accepted"
	StateDropped  OpState = "dropped"  // reviewer removed it
	StateRejected OpState = "rejected" // whole changeset rejected
	StateStale    OpState = "stale"    // working tree changed under it
)

// Hunk is the unit of review. Reviewers accept or drop hunks, not ops.
type Hunk struct {
	ID      string   `json:"id"`   // "h1", "h2", … unique within the op
	Path    string   `json:"path"` // the file this hunk edits
	Section string   `json:"section,omitempty"`
	Before  []string `json:"-"` // context/removed lines, for display
	Add     []string `json:"+,omitempty"`
	Del     []string `json:"-,omitempty"`
	Dropped bool     `json:"dropped,omitempty"`
}

// Op is one proposed edit within a Changeset.
type Op struct {
	ID         string   `json:"id"` // "op1", "op2", … unique within the changeset
	Kind       OpKind   `json:"op"`
	Path       string   `json:"path,omitempty"`
	From       string   `json:"from,omitempty"` // rename/merge source
	To         string   `json:"to,omitempty"`
	Sources    []string `json:"sources,omitempty"` // merge_pages / split_page members
	Section    string   `json:"section,omitempty"`
	Before     string   `json:"before,omitempty"` // sha256 of the pre-image blob ("" = new file)
	After      string   `json:"after,omitempty"`  // sha256 of the post-image blob
	SHA256     string   `json:"sha256,omitempty"` // ingest_source: sha of the raw body
	Extractor  string   `json:"extractor,omitempty"`
	Hunks      []Hunk   `json:"hunks,omitempty"`
	Cascade    []Op     `json:"cascade,omitempty"`     // rename: inbound wikilink rewrites
	SourceSHAs []string `json:"source_shas,omitempty"` // staleness anchor (MASTER §9 D-BC)
	Rationale  string   `json:"rationale,omitempty"`
	Provenance []string `json:"provenance,omitempty"`
	State      OpState  `json:"state"`
	Content    []byte   `json:"-"` // post-image bytes in, sha out (MASTER §9 D-AY)
}

// Contract — Content (MASTER §9 D-AY). The ONLY channel by which raw bytes
// reach the engine. §5.4 Append is contracted to "store pre/post images in
// the CAS", but every other Op field is a sha, and Append(op Op) has no
// second parameter — so without this field there is no legal path for a
// new page's bytes to reach objects/ at all. The proposer (internal/tools
// §6, cmd/lw §13 — both OTHER packages, hence exported rather than
// unexported) sets it; ValidateOp reads it for §5.5's three
// content-dependent create_page bullets; Append Store.Puts it, writes the
// sha into After (and SHA256 for ingest_source), then CLEARS it. json:"-"
// keeps it out of changeset.json entirely, so the wire format and
// spec/changeset.schema.json are unchanged. After Append, the content is
// recoverable only from the CAS via After — which is what Diff, Commit and
// a reloaded Current all use, so nothing else ever needs this field.

// Contract — SourceSHAs (MASTER §9 D-BC, superseding D-AJ's "unexported,
// non-serialized" clause). The canonical sha of each source page captured
// at Append time, in order: From for rename_page, each entry of Sources
// for merge_pages, Path for split_page. It MUST be serialized: lw stage,
// lw diff and lw commit are three separate processes, so an unexported
// field is a zero value on every Op json.Unmarshal produces, and Refresh
// would compare a real sha against "" — ErrStale on every rename, merge
// and split, forever.

// Contract — the fields that are never absent (MASTER §9 D-BH). Op.ID,
// Op.State and Hunk.Path carry NO omitempty, so encoding/json always emits
// them. A subschema applies whenever a property is present, even when the
// property is optional — so an unset one emits "" and fails vaultPath /
// ^op[0-9]+$ / the state enum. Therefore every Op this package marshals,
// RECURSIVELY THROUGH Cascade, carries a real op<N> id and a valid State,
// and every Hunk carries the path of the file it edits. Append's
// assignIDs (engine_changeset.go) is what establishes that invariant,
// recursing into Cascade with the single op<N> counter (D-AK), before the
// changeset is ever persisted.

// Author identifies who proposed a Changeset.
type Author struct {
	Kind    string `json:"kind"` // "agent" | "human"
	Model   string `json:"model,omitempty"`
	Session string `json:"session,omitempty"`
}

// Checks is the computed review summary for a Changeset (backbone §5.3).
type Checks struct {
	Schema      string `json:"schema"` // "pass" | "fail"
	Lint        string `json:"lint"`
	Orphans     int    `json:"orphans"`
	BrokenLinks int    `json:"broken_links"`
}

// Contract (MASTER §9 D-AF) — all four are computed over the FULL projected
// tree (§5.4 Append), never over the currently-committed vault and never
// over only the touched paths:
//
//	Lint        = "fail" iff !lint.Run(ctx, nil).Clean()   (Errors > 0 only;
//	              warnings and info never fail a changeset)
//	Schema      = "fail" iff any projected page fails Frontmatter.Validate(v.Schema())
//	Orphans     = len(Graph.Orphans())
//	BrokenLinks = len(Graph.Broken())
//
// Whole-tree scope is load-bearing, not tidiness: index-sync and log-rotate
// both return no findings at all when Vault.Read fails (they read
// index.md / log.md), so a PARTIAL projection makes them silently report
// clean — a false "lint: pass" on the changeset a human is about to
// approve.

// Changeset is a proposed set of ops awaiting review.
type Changeset struct {
	ID       string    `json:"id"` // "cs-0193f2a" (MASTER §9 D-H)
	Intent   string    `json:"intent"`
	Author   Author    `json:"author"`
	OpenedAt time.Time `json:"opened_at"`
	Ops      []Op      `json:"ops"`
	Checks   Checks    `json:"checks"`
}

// Op returns the Op with the given id — searched recursively through every
// op's Cascade, since DropOp and DropHunk must be able to address a
// cascade entry (backbone §5.4) — and whether it was found.
func (c *Changeset) Op(id string) (*Op, bool) {
	if p := findOpPtr(c.Ops, id); p != nil {
		return p, true
	}
	return nil, false
}

// Live returns the top-level ops whose State is neither Dropped nor
// Rejected (backbone §5.3).
func (c *Changeset) Live() []Op {
	var out []Op
	for _, op := range c.Ops {
		if op.State == StateDropped || op.State == StateRejected {
			continue
		}
		out = append(out, op)
	}
	return out
}

// Touches returns every vault path affected by c's live ops — top-level and
// cascade, recursively — sorted and deduped.
func (c *Changeset) Touches() []string {
	set := map[string]bool{}
	for _, op := range c.Live() {
		for _, p := range opTouches(op) {
			set[p] = true
		}
	}
	return sortedSet(set)
}

// clone returns a deep copy of c, nil for a nil receiver. Every slice the
// type graph holds is copied — Ops, each op's Sources/SourceSHAs/
// Provenance/Content, each Hunk's Before/Add/Del lines, and each Cascade
// tree recursively — so mutating a clone, or any slice reachable from it,
// can never reach the original. This is what makes D-8H's copy rule
// possible: Current hands callers a clone of the engine's cached changeset,
// never the cache itself, so a caller's mutation cannot corrupt engine
// state.
func (c *Changeset) clone() *Changeset {
	if c == nil {
		return nil
	}
	out := *c
	out.Ops = cloneOps(c.Ops)
	return &out
}

// cloneOps deep-copies an op slice, preserving nil and empty-non-nil
// distinctions — Ops marshals without omitempty, so a changeset whose Ops
// is []Op{} must not come back as nil (`"ops": null`).
func cloneOps(ops []Op) []Op {
	if ops == nil {
		return nil
	}
	out := make([]Op, len(ops))
	for i := range ops {
		out[i] = ops[i].clone()
	}
	return out
}

// clone returns a deep copy of op, recursing through Cascade (cloneOps).
func (op Op) clone() Op {
	op.Sources = cloneStrings(op.Sources)
	op.SourceSHAs = cloneStrings(op.SourceSHAs)
	op.Provenance = cloneStrings(op.Provenance)
	op.Content = cloneBytes(op.Content)
	op.Hunks = cloneHunks(op.Hunks)
	op.Cascade = cloneOps(op.Cascade)
	return op
}

// cloneHunks deep-copies a hunk slice; each hunk's line slices are copied
// too, since a caller mutating a returned hunk's Add/Del/Before must not
// reach the engine's copy.
func cloneHunks(hunks []Hunk) []Hunk {
	if hunks == nil {
		return nil
	}
	out := make([]Hunk, len(hunks))
	for i := range hunks {
		out[i] = Hunk{
			ID:      hunks[i].ID,
			Path:    hunks[i].Path,
			Section: hunks[i].Section,
			Before:  cloneStrings(hunks[i].Before),
			Add:     cloneStrings(hunks[i].Add),
			Del:     cloneStrings(hunks[i].Del),
			Dropped: hunks[i].Dropped,
		}
	}
	return out
}

// cloneStrings deep-copies a string slice, preserving nil.
func cloneStrings(s []string) []string {
	if s == nil {
		return nil
	}
	out := make([]string, len(s))
	copy(out, s)
	return out
}

// cloneBytes deep-copies a byte slice, preserving nil.
func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
