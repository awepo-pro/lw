// engine_changeset.go implements the Engine changeset lifecycle (backbone
// §5.4): OpenChangeset, Current, Append, DropHunk, DropOp, Refresh, Reject.
// Not Diff — that is S2-T5's, in diff.go (MASTER §9 D-BG).
package stage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/awepo-pro/lw/internal/vault"
)

// changesetOpenDir, changesetRejectedDir and changesetCommittedDir return
// the three changeset destinations under e.llmwikiDir()/changesets.
// changesetCommittedDir moved here from apply.go's own inline
// filepath.Join (S3-T0, MASTER §9 D-CI) so changesetIDTaken below and
// Commit's own pre-move existence guard share one path helper instead of
// two spellings of the same join.
func (e *Engine) changesetOpenDir() string {
	return filepath.Join(e.llmwikiDir(), "changesets", "open")
}

func (e *Engine) changesetRejectedDir() string {
	return filepath.Join(e.llmwikiDir(), "changesets", "rejected")
}

func (e *Engine) changesetCommittedDir() string {
	return filepath.Join(e.llmwikiDir(), "changesets", "committed")
}

// maxIDAttempts bounds OpenChangeset's retry loop for drawing a free
// changeset id (MASTER §9 D-CI).
const maxIDAttempts = 8

// changesetIDTaken reports whether id already names a directory under
// changesets/open/ or changesets/committed/ (MASTER §9 D-CI). A Stat error
// other than "not exist" is returned, never swallowed: an unreadable
// committed/ directory must not be reported as "free".
func (e *Engine) changesetIDTaken(id string) (bool, error) {
	for _, dir := range []string{e.changesetOpenDir(), e.changesetCommittedDir()} {
		if _, err := os.Stat(filepath.Join(dir, id)); err == nil {
			return true, nil
		} else if !os.IsNotExist(err) {
			return false, fmt.Errorf("stage: changeset id taken: %w", err)
		}
	}
	return false, nil
}

// writeChangesetJSON persists c to dir/changeset.json, atomically (temp +
// fsync + rename inside dir), per §14: json.MarshalIndent(cs, "", "  "),
// trailing "\n".
func writeChangesetJSON(dir string, c *Changeset) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("stage: marshal changeset %s: %w", c.ID, err)
	}
	b = append(b, '\n')

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("stage: write changeset %s: %w", c.ID, err)
	}

	tmp, err := os.CreateTemp(dir, "tmp-*")
	if err != nil {
		return fmt.Errorf("stage: write changeset %s: %w", c.ID, err)
	}
	tmpPath := tmp.Name()

	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(b); err != nil {
		return fmt.Errorf("stage: write changeset %s: %w", c.ID, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("stage: write changeset %s: %w", c.ID, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("stage: write changeset %s: %w", c.ID, err)
	}
	if err := os.Rename(tmpPath, filepath.Join(dir, "changeset.json")); err != nil {
		return fmt.Errorf("stage: write changeset %s: %w", c.ID, err)
	}
	ok = true
	return nil
}

// OpenChangeset opens a new changeset with the given intent and author.
//
// Contract (backbone §5.4, MASTER §9 D-BA): returns ErrOpenChangeset when
// changesets/open/ is non-empty — a disk check, since lw is a
// verb-per-process CLI and nothing in memory survives to the next process.
// Otherwise it creates changesets/open/<id>/ and writes changeset.json
// there before returning, with Ops initialized to a non-nil []Op{} and
// Checks computed over the projection with an empty override set — which
// at zero ops is definitionally the current vault, the same code path
// Append uses. Journals changeset_opened.
func (e *Engine) OpenChangeset(intent string, a Author) (*Changeset, error) {
	openDir := e.changesetOpenDir()
	entries, err := os.ReadDir(openDir)
	if err != nil {
		return nil, fmt.Errorf("stage: open changeset: %w", err)
	}
	if len(entries) > 0 {
		return nil, ErrOpenChangeset
	}

	// D-CI: an id names a directory, so it must be free in BOTH
	// changesets/open/ and changesets/committed/ before it is used.
	// Drawing again is cheap and the check is two Stats, so a collision is
	// resolved here — the only point in a changeset's life where nothing
	// has happened yet and a different id costs nothing. Commit's own
	// guard (D-CI part B2, apply.go step 2a) is the backstop, not the
	// mechanism.
	//
	// With a fixed clock AND a fixed reader (as some tests deliberately
	// use), every attempt yields the same candidate and the loop
	// legitimately exhausts — that is correct behaviour, not a bug to
	// paper over by reseeding. e.rand in production is crypto/rand.Reader,
	// which does vary, so a real collision resolves on the very next
	// attempt.
	var id string
	for attempt := 0; ; attempt++ {
		candidate, err := newChangesetID(e.now(), e.rand)
		if err != nil {
			return nil, fmt.Errorf("stage: open changeset: %w", err)
		}
		taken, err := e.changesetIDTaken(candidate)
		if err != nil {
			return nil, fmt.Errorf("stage: open changeset: %w", err)
		}
		if !taken {
			id = candidate
			break
		}
		if attempt+1 >= maxIDAttempts {
			return nil, fmt.Errorf("stage: open changeset: could not draw a free id in %d attempts: %w",
				maxIDAttempts, ErrIDExhausted)
		}
	}

	c := &Changeset{
		ID:       id,
		Intent:   intent,
		Author:   a,
		OpenedAt: e.now().UTC(),
		Ops:      []Op{},
	}

	checks, err := e.recomputeChecks(c)
	if err != nil {
		return nil, fmt.Errorf("stage: open changeset: %w", err)
	}
	c.Checks = checks

	dir := filepath.Join(openDir, id)
	if err := writeChangesetJSON(dir, c); err != nil {
		return nil, fmt.Errorf("stage: open changeset: %w", err)
	}

	if err := e.journal.Append(Event{
		TS:        e.now().UTC(),
		Kind:      EvChangesetOpened,
		Changeset: c.ID,
		Actor:     a,
		Message:   intent,
	}); err != nil {
		return nil, fmt.Errorf("stage: open changeset: %w", err)
	}

	e.open = c
	e.nextOp = 1
	return c, nil
}

// Current returns the currently open changeset, ErrNoChangeset when none
// is open.
//
// Contract (backbone §5.4, MASTER §9 D-BB): loads
// changesets/open/<id>/changeset.json, assigns e.open, and sets e.nextOp
// to 1+max(N) over every op<N> in the loaded changeset, cascade sub-ops
// included. OpenEngine populates neither field, so this must be derived
// fresh on every call rather than assumed carried over from a prior
// process.
func (e *Engine) Current() (*Changeset, error) {
	openDir := e.changesetOpenDir()
	entries, err := os.ReadDir(openDir)
	if err != nil {
		return nil, fmt.Errorf("stage: current: %w", err)
	}

	var id string
	for _, ent := range entries {
		if ent.IsDir() {
			id = ent.Name()
			break
		}
	}
	if id == "" {
		return nil, ErrNoChangeset
	}

	b, err := os.ReadFile(filepath.Join(openDir, id, "changeset.json"))
	if err != nil {
		return nil, fmt.Errorf("stage: current: %w", err)
	}
	var c Changeset
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("stage: current: %w", err)
	}

	e.open = &c
	e.nextOp = 1 + maxOpN(c.Ops)
	return &c, nil
}

// currentOpen returns e.open, calling Current to rehydrate it first when
// nil — the seam that lets Append/DropHunk/DropOp/Refresh/Reject work
// correctly whether or not the caller already called Current in this
// process (backbone §5.4, MASTER §9 D-BB).
func (e *Engine) currentOpen() (*Changeset, error) {
	if e.open != nil {
		return e.open, nil
	}
	return e.Current()
}

// storeOpContent stores both images the Append Contract names ("store
// pre/post images in the CAS", backbone §5.4): the CURRENT canonical
// content of a patch_page op's Path — so op.Before, already validated to
// equal that content's sha, is backed by a real object DropHunk can
// Store.Get later — and op.Content, writing the resulting sha into After
// (and into SHA256 for ingest_source), then clearing Content. Recurses
// through Cascade, since a cascade sub-op's pre- and post-image bytes
// travel through the identical channels (backbone §5.3/§5.4, MASTER §9
// D-AY).
func (e *Engine) storeOpContent(op *Op) error {
	if op.Kind == OpPatchPage {
		if pre, ok := canonicalContent(e.vault, op.Path); ok {
			if _, err := e.store.Put(pre); err != nil {
				return fmt.Errorf("stage: store pre-image: %w", err)
			}
		}
	}
	if op.Content != nil {
		sha, err := e.store.Put(op.Content)
		if err != nil {
			return fmt.Errorf("stage: store op content: %w", err)
		}
		op.After = sha
		if op.Kind == OpIngestSource {
			op.SHA256 = sha
		}
		op.Content = nil
	}
	for i := range op.Cascade {
		if err := e.storeOpContent(&op.Cascade[i]); err != nil {
			return err
		}
	}
	return nil
}

// captureSourceSHAs records op.SourceSHAs — the canonical sha of each
// source page at Append time, in the schema's field order: From for
// rename_page, each entry of Sources for merge_pages, Path for split_page
// (backbone §5.3, MASTER §9 D-BC).
func (e *Engine) captureSourceSHAs(op *Op) error {
	var sources []string
	switch op.Kind {
	case OpRenamePage:
		sources = []string{op.From}
	case OpMergePages:
		sources = append([]string(nil), op.Sources...)
	case OpSplitPage:
		sources = []string{op.Path}
	default:
		return nil
	}

	shas := make([]string, len(sources))
	for i, src := range sources {
		sha, ok := canonicalSHA(e.vault, src)
		if !ok {
			return fmt.Errorf("stage: capture source shas: %s: not found", src)
		}
		shas[i] = sha
	}
	op.SourceSHAs = shas
	return nil
}

// assignIDs assigns op the next "op<N>" id from e.nextOp, defaults its
// State to StateProposed when unset, and recurses into Cascade — a single
// counter numbers every op nested in a Cascade too (backbone §5.4,
// MASTER §9 D-AK), so DropOp can address a cascade entry by id.
func (e *Engine) assignIDs(op *Op) {
	op.ID = fmt.Sprintf("op%d", e.nextOp)
	e.nextOp++
	if op.State == "" {
		op.State = StateProposed
	}
	for i := range op.Cascade {
		e.assignIDs(&op.Cascade[i])
	}
}

// Append validates op, assigns it a sequential id, stores its content, and
// recomputes Checks over every live op.
//
// Contract (backbone §5.4): validate (§5.5) -> store pre/post images in
// the CAS, writing After (and SHA256 for ingest_source), then clear
// Content -> capture SourceSHAs for rename_page/merge_pages/split_page ->
// assign op<N> (recursively through Cascade, MASTER §9 D-AK) -> append ->
// recompute Checks by materializing the projected tree in memory and
// running lint -> persist changeset.json atomically -> journal
// op_proposed with TS: e.now().UTC(). Append does not take the lock.
//
// For rename_page and merge_pages, Append computes op.Cascade itself from
// From/To (or Sources/To) before validating — the rewrite algorithm needs
// vault-wide graph and resolution access no proposer outside this package
// can safely reproduce, and getting it wrong is the exact silent
// corruption backbone §5.5's Contract exists to prevent (MASTER §9 D-AM,
// D-Y). Any Cascade the caller supplied is replaced, not merged.
func (e *Engine) Append(op Op) (string, error) {
	c, err := e.currentOpen()
	if err != nil {
		return "", err
	}

	// cv is the vault this op's cascade is built and validated against.
	// For a cascade-less kind, and for the first op of any changeset, it
	// is the working tree — byte-for-byte the pre-OR-13 behaviour.
	cv := e.vault
	switch op.Kind {
	case OpRenamePage, OpMergePages:
		froms := []string{op.From}
		if op.Kind == OpMergePages {
			froms = op.Sources
		}
		// Chaining off another op's output is not supported: Commit
		// materializes a rename from the working tree, so a source that
		// only exists in the projection has no pre-image to move.
		// Refuse it here, explicitly, rather than let the projected-tree
		// validation below accept a shape Commit cannot honour.
		for _, f := range froms {
			if f != "" && !e.vault.Exists(f) {
				return "", fmt.Errorf("%w: %s: source %s does not exist in the working tree; commit the op that produces it first", ErrValidation, op.Kind, f)
			}
		}

		var err error
		if cv, err = e.cascadeBase(c.Live()); err != nil {
			return "", fmt.Errorf("stage: append: %w", err)
		}
		cascade, err := buildCascade(cv, froms, op.To)
		if err != nil {
			return "", fmt.Errorf("stage: append: %w", err)
		}
		op.Cascade = cascade
	}

	if err := ValidateOp(op, cv, cv.Schema()); err != nil {
		return "", err
	}

	if err := e.storeOpContent(&op); err != nil {
		return "", fmt.Errorf("stage: append: %w", err)
	}
	if err := e.captureSourceSHAs(&op); err != nil {
		return "", fmt.Errorf("stage: append: %w", err)
	}
	e.assignIDs(&op)

	c.Ops = append(c.Ops, op)

	checks, err := e.recomputeChecks(c)
	if err != nil {
		return "", fmt.Errorf("stage: append: %w", err)
	}
	c.Checks = checks

	if err := writeChangesetJSON(filepath.Join(e.changesetOpenDir(), c.ID), c); err != nil {
		return "", fmt.Errorf("stage: append: %w", err)
	}

	if err := e.journal.Append(Event{
		TS:        e.now().UTC(),
		Kind:      EvOpProposed,
		Changeset: c.ID,
		Op:        op.ID,
		Actor:     c.Author,
		Paths:     opTouches(op),
	}); err != nil {
		return "", fmt.Errorf("stage: append: %w", err)
	}

	e.open = c
	return op.ID, nil
}

// DropHunk marks the hunk hunkID of op opID as dropped, recomputes that
// op's projected content from Before plus its remaining live hunks,
// stores it, overwrites Op.After with the resulting sha, recomputes
// Checks, persists, and journals hunk_dropped.
//
// Contract (backbone §5.4): After always describes what Commit will
// actually write, never the pre-drop proposal.
func (e *Engine) DropHunk(opID, hunkID string) error {
	c, err := e.currentOpen()
	if err != nil {
		return err
	}

	op, ok := c.Op(opID)
	if !ok {
		return fmt.Errorf("stage: drop hunk: no such op %q", opID)
	}

	var hunk *Hunk
	for i := range op.Hunks {
		if op.Hunks[i].ID == hunkID {
			hunk = &op.Hunks[i]
			break
		}
	}
	if hunk == nil {
		return fmt.Errorf("stage: drop hunk: op %s has no hunk %q", opID, hunkID)
	}
	hunk.Dropped = true

	before, err := e.store.Get(op.Before)
	if err != nil {
		return fmt.Errorf("stage: drop hunk: %w", err)
	}
	newContent := applyHunks(before, op.Hunks)
	sha, err := e.store.Put(newContent)
	if err != nil {
		return fmt.Errorf("stage: drop hunk: %w", err)
	}
	op.After = sha

	if err := e.persistAfterMutation(c); err != nil {
		return err
	}

	return e.journal.Append(Event{
		TS:        e.now().UTC(),
		Kind:      EvHunkDropped,
		Changeset: c.ID,
		Op:        opID,
		Hunk:      hunkID,
		Actor:     c.Author,
		Paths:     opTouches(*op),
	})
}

// DropOp marks op opID (and, transitively, everything it projects) as
// dropped, recomputes Checks, persists, and journals op_dropped.
func (e *Engine) DropOp(opID string) error {
	c, err := e.currentOpen()
	if err != nil {
		return err
	}

	op, ok := c.Op(opID)
	if !ok {
		return fmt.Errorf("stage: drop op: no such op %q", opID)
	}
	op.State = StateDropped

	if err := e.persistAfterMutation(c); err != nil {
		return err
	}

	return e.journal.Append(Event{
		TS:        e.now().UTC(),
		Kind:      EvOpDropped,
		Changeset: c.ID,
		Op:        opID,
		Actor:     c.Author,
		Paths:     opTouches(*op),
	})
}

// cascadeBase returns the vault a cascade must be built — and later
// re-checked — against: base's ops projected over a fresh read of the
// working tree (MASTER §10 OR-13, closing OQ-10).
//
// Contract: an empty base returns e.vault itself, not an equivalent copy,
// so the overwhelmingly common single-op changeset takes exactly the code
// path it took before OR-13. It shares projectedTree with recomputeChecks,
// Diff and ProjectedReport deliberately — the wave-7 lesson (C-65, C-69)
// is that every surface describing what a commit will write must be the
// same call, or they drift.
func (e *Engine) cascadeBase(base []Op) (*vault.Vault, error) {
	if len(base) == 0 {
		return e.vault, nil
	}
	tree, err := e.projectedTree(base)
	if err != nil {
		return nil, err
	}
	return openProjection(tree)
}

// liveBefore returns the ops at indices < i that Live() would keep — the
// op set the cascade of ops[i] was built against when it was appended.
//
// Dropping an earlier op therefore changes a later op's base, and the
// cascade sub-ops built on top of it go StateStale on the next check.
// That is the intended behaviour and the reason the drop path re-runs it:
// a cascade post-image computed over an op that no longer applies would
// otherwise reintroduce that op's rewrite at commit time.
func liveBefore(ops []Op, i int) []Op {
	var out []Op
	for _, op := range ops[:i] {
		if op.State == StateDropped || op.State == StateRejected {
			continue
		}
		out = append(out, op)
	}
	return out
}

// refreshCascadeStates re-checks every live op's cascade sub-ops against
// the tree that op will actually apply to, flipping them StateStale where
// the base they were built on no longer holds.
//
// It deliberately never touches a TOP-LEVEL op's State: working-tree
// staleness is Refresh's job and its anchor is captureSourceSHAs' hash of
// the working tree, which dropping a sibling op does not change.
func (e *Engine) refreshCascadeStates(c *Changeset) error {
	for i := range c.Ops {
		op := &c.Ops[i]
		if len(op.Cascade) == 0 {
			continue
		}
		if op.State == StateDropped || op.State == StateRejected {
			continue
		}
		base, err := e.cascadeBase(liveBefore(c.Ops, i))
		if err != nil {
			return err
		}
		for j := range op.Cascade {
			refreshOp(&op.Cascade[j], base, base)
		}
	}
	return nil
}

// persistAfterMutation recomputes c.Checks and persists c to disk — the
// shared tail of DropHunk and DropOp.
func (e *Engine) persistAfterMutation(c *Changeset) error {
	if err := e.refreshCascadeStates(c); err != nil {
		return fmt.Errorf("stage: recheck cascades: %w", err)
	}

	checks, err := e.recomputeChecks(c)
	if err != nil {
		return fmt.Errorf("stage: recompute checks: %w", err)
	}
	c.Checks = checks

	if err := writeChangesetJSON(filepath.Join(e.changesetOpenDir(), c.ID), c); err != nil {
		return fmt.Errorf("stage: persist changeset: %w", err)
	}
	e.open = c
	return nil
}

// Refresh re-hashes the working tree against every live op's staleness
// anchor and flips State to StateStale where it no longer matches.
//
// Contract (backbone §5.4, per-kind table, MASTER §9 D-AJ, D-BC): it never
// recomputes Hunks — only State — leaving the last-computed hunks and
// their Dropped flags intact.
func (e *Engine) Refresh() error {
	c, err := e.currentOpen()
	if err != nil {
		return err
	}

	for i := range c.Ops {
		base, err := e.cascadeBase(liveBefore(c.Ops, i))
		if err != nil {
			return fmt.Errorf("stage: refresh: %w", err)
		}
		refreshOp(&c.Ops[i], e.vault, base)
	}

	if err := writeChangesetJSON(filepath.Join(e.changesetOpenDir(), c.ID), c); err != nil {
		return fmt.Errorf("stage: refresh: %w", err)
	}
	e.open = c
	return nil
}

// Reject moves the open changeset from open/<id> to rejected/<id> — never
// a delete — and journals changeset_rejected.
//
// Contract (backbone §5.4, §14, MASTER §9 D-AI): rejections are permanent
// history.
func (e *Engine) Reject(reason string) error {
	c, err := e.currentOpen()
	if err != nil {
		return err
	}

	src := filepath.Join(e.changesetOpenDir(), c.ID)
	dst := filepath.Join(e.changesetRejectedDir(), c.ID)
	if err := os.MkdirAll(e.changesetRejectedDir(), 0o755); err != nil {
		return fmt.Errorf("stage: reject: %w", err)
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("stage: reject: %w", err)
	}

	if err := e.journal.Append(Event{
		TS:        e.now().UTC(),
		Kind:      EvChangesetRejected,
		Changeset: c.ID,
		Actor:     c.Author,
		Message:   reason,
	}); err != nil {
		return fmt.Errorf("stage: reject: %w", err)
	}

	e.open = nil
	e.nextOp = 0
	return nil
}
