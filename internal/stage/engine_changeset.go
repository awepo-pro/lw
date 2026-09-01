// engine_changeset.go implements the Engine changeset lifecycle (backbone
// §5.4): OpenChangeset, Current, Append, DropHunk, DropOp, Refresh, Reject.
// Not Diff — that is S2-T5's, in diff.go (MASTER §9 D-BG).
package stage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// changesetOpenDir and changesetRejectedDir return two of the three
// changeset destinations under e.llmwikiDir()/changesets — the third,
// "committed", belongs to Commit (a later wave).
func (e *Engine) changesetOpenDir() string {
	return filepath.Join(e.llmwikiDir(), "changesets", "open")
}

func (e *Engine) changesetRejectedDir() string {
	return filepath.Join(e.llmwikiDir(), "changesets", "rejected")
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

	id, err := newChangesetID(e.now(), e.rand)
	if err != nil {
		return nil, fmt.Errorf("stage: open changeset: %w", err)
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

	switch op.Kind {
	case OpRenamePage:
		cascade, err := buildCascade(e.vault, []string{op.From}, op.To)
		if err != nil {
			return "", fmt.Errorf("stage: append: %w", err)
		}
		op.Cascade = cascade
	case OpMergePages:
		cascade, err := buildCascade(e.vault, op.Sources, op.To)
		if err != nil {
			return "", fmt.Errorf("stage: append: %w", err)
		}
		op.Cascade = cascade
	}

	if err := ValidateOp(op, e.vault, e.vault.Schema()); err != nil {
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

// persistAfterMutation recomputes c.Checks and persists c to disk — the
// shared tail of DropHunk and DropOp.
func (e *Engine) persistAfterMutation(c *Changeset) error {
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
		refreshOp(&c.Ops[i], e.vault)
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
