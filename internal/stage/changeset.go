// changeset.go, at this wave, holds only the declarations engine.go needs
// to compile: the whole backbone §5.3 type block, verbatim, with no
// methods. Engine (D-AO) holds an `open *Changeset` field, so the type name
// must exist in the package before engine.go can build — but the type
// block belongs to S2-T2 and Go declares a struct's fields exactly once
// (backbone §5's "where the shared declarations live" Contract, MASTER §9
// D-AQ, correction C-20).
//
// S2-T1 creates this file with declarations only. Ownership transfers to
// S2-T2 from wave 3, which adds Changeset.Op, Live and Touches plus every
// changeset behaviour (OpenChangeset, Append, DropOp, DropHunk, Refresh,
// Reject — backbone §5.4) and ValidateOp (§5.5), and may restructure
// anything in this file freely.
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
	Cascade    []Op     `json:"cascade,omitempty"` // rename: inbound wikilink rewrites
	Rationale  string   `json:"rationale,omitempty"`
	Provenance []string `json:"provenance,omitempty"`
	State      OpState  `json:"state"`
}

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

// Changeset is a proposed set of ops awaiting review.
type Changeset struct {
	ID       string    `json:"id"` // "cs-0193f2a" (MASTER §9 D-H)
	Intent   string    `json:"intent"`
	Author   Author    `json:"author"`
	OpenedAt time.Time `json:"opened_at"`
	Ops      []Op      `json:"ops"`
	Checks   Checks    `json:"checks"`
}
