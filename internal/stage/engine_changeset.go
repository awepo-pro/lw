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

// ChangesetState reports which of the three changeset state directories
// holds id: "open", "committed" or "rejected" — the directory's name IS the
// state in backbone §9's frozen layout, and the same words `lw session
// list` prints from cmd/lw's sessionStates. An id in none of them returns
// an error matching ErrNoChangeset, the sentinel Current already owns.
//
// It is deliberately a read-only stat and nothing more (005 contract §6, as
// amended by R-509) — though not because Engine state is unguarded: since
// 008 A-802 the engine's open/nextOp fields are openMu-guarded, and Current
// is safe to call from the ask pane's turn goroutine. The stat-only shape
// stays right for its own reasons: it costs a directory walk instead of
// Current's read-and-cache, and answering a display question must not
// repopulate the engine's changeset cache as a side effect.
// ChangesetState reads directory entries under e.root and nothing else.
//
// id must be a bare directory name, as every id newChangesetID draws is:
// a name carrying a path separator or one of "." / ".." would make the
// stat below reach outside the three state dirs (or answer from a
// container dir), so it is refused with ErrNoChangeset — no id any
// changeset ever had is lost to this, and no id can answer from a
// directory that is not a changeset.
func (e *Engine) ChangesetState(id string) (string, error) {
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id {
		return "", fmt.Errorf("stage: changeset state of %s: %w", id, ErrNoChangeset)
	}
	for _, d := range []struct{ dir, state string }{
		{e.changesetOpenDir(), "open"},
		{e.changesetCommittedDir(), "committed"},
		{e.changesetRejectedDir(), "rejected"},
	} {
		if info, err := os.Stat(filepath.Join(d.dir, id)); err == nil && info.IsDir() {
			return d.state, nil
		} else if err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("stage: changeset state of %s: %w", id, err)
		}
	}
	return "", fmt.Errorf("stage: changeset state of %s: %w", id, ErrNoChangeset)
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

	if err := e.appendJournal(Event{
		TS:        e.now().UTC(),
		Kind:      EvChangesetOpened,
		Changeset: c.ID,
		Actor:     a,
		Message:   intent,
	}); err != nil {
		return nil, fmt.Errorf("stage: open changeset: %w", err)
	}

	// D-8H copy rule: the value escapes the package, so the caller gets a
	// clone and the cache keeps its own. The clone is taken before the
	// cache pointer moves — from that moment on, engine mutators own c.
	clone := c.clone()
	e.cacheOpenAt(c, 1)
	return clone, nil
}

// Current returns the currently open changeset, ErrNoChangeset when none
// is open.
//
// Contract (backbone §5.4, MASTER §9 D-BB; cache behaviour amended 008
// D-8H): while the engine holds a cached open changeset, Current returns a
// deep COPY of it and touches neither the cache, the op counter nor the
// filesystem — an Append that has drawn an id but not yet persisted must
// never be rewound, and a display read must never overwrite the cache with
// the on-disk copy it temporarily trails. Only when nothing is cached does
// it read changesets/open/<id>/changeset.json (rehydrateOpen), which raises
// e.nextOp to 1+max(N) over every op<N> in the loaded changeset, cascade
// sub-ops included — raised, never lowered, since disk cannot see an
// in-flight Append's id (cacheOpenRehydrated). OpenEngine populates neither
// field, so a fresh engine derives both on its first Current.
//
// The returned changeset is the caller's to mutate; no mutation of it can
// reach the cache.
func (e *Engine) Current() (*Changeset, error) {
	if c := e.cachedOpenCopy(); c != nil {
		return c, nil
	}
	return e.rehydrateOpen()
}

// rehydrateOpen reads the open changeset from disk into an empty cache and
// returns it. The cache keeps its own deep copy (so later engine mutators
// never write through this object), and the returned changeset is therefore
// unshared: in-package callers may mutate it freely, and the exported
// Current hands out exactly this object as the copy its contract promises.
// ErrNoChangeset when changesets/open/ holds no changeset.
func (e *Engine) rehydrateOpen() (*Changeset, error) {
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

	// c is unshared until cacheOpenRehydrated, so the counter derivation
	// and the cache's own clone both read it before the engine takes
	// ownership of its copy.
	nextOp := 1 + maxOpN(c.Ops)
	e.cacheOpenRehydrated(c.clone(), nextOp)
	return &c, nil
}

// currentOpen returns the cached open changeset, rehydrating it from disk
// first when nothing is cached — the seam that lets
// Append/DropHunk/DropOp/Refresh/Reject work correctly whether or not the
// caller already called Current in this process (backbone §5.4, MASTER §9
// D-BB). The cache pointer moves under openMu (008 A-802), so a concurrent
// ReloadIfChanged clearing it is either seen whole or not at all.
//
// In-package use only (D-8H): on the cache hit the result is the live cache
// object, and the package's mutators are its intended writers; on the
// rehydrate path it is rehydrateOpen's unshared object, which every mutator
// tail re-publishes with cacheOpen. It and its slices must not escape
// internal/stage — exported surfaces hand out copies (Current,
// cachedOpenCopy, OpenChangeset).
func (e *Engine) currentOpen() (*Changeset, error) {
	if c := e.cachedOpen(); c != nil {
		return c, nil
	}
	return e.rehydrateOpen()
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

// assignIDs assigns op the next "op<N>" id from the engine's op counter,
// defaults its State to StateProposed when unset, and recurses into
// Cascade — a single counter numbers every op nested in a Cascade too
// (backbone §5.4, MASTER §9 D-AK), so DropOp can address a cascade entry by
// id.
//
// The caller must hold openMu (008 A-802, D-8H): stageAppendOp numbers and
// inserts the op in one critical section, so the id draw cannot be
// separated from the cache insert by a concurrent Current's rehydration.
// The counter is read and advanced directly rather than through a helper —
// sync.Mutex is not reentrant, and the lock is already held.
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
// assign op<N> (recursively through Cascade, MASTER §9 D-AK) and insert the
// numbered op into the CACHED changeset, in one openMu critical section
// (stageAppendOp, 008 D-8H) -> recompute Checks by materializing the
// projected tree in memory and running lint -> persist changeset.json
// atomically -> journal op_proposed with TS: e.now().UTC(). Like every
// §5.4 method except Commit it never takes the vault lock (§5.2), and the
// cache lock is held only for the stage step — never across I/O. A failure
// between staging and the persist removes the op from the cache again
// (unstageAppendOp), so the cache always equals what will be on disk.
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

	// OQ-9 L2's mirror direction, closed for real (S4-T0 repair-2): cv is a
	// rootless fstest.MapFS projection (cascadeBase, openProjection)
	// whenever the changeset already carries another live op — precisely
	// when a root-file collision can exist — so validateCascade's own
	// checkNewWriterOneWriter(cv, ...) call reads an empty Root() there and
	// cannot see the on-disk changeset. e.vault is always the real,
	// disk-backed vault, so this repeats the same check against it.
	if op.Kind == OpRenamePage || op.Kind == OpMergePages {
		if err := checkNewWriterOneWriter(e.vault, op); err != nil {
			return "", err
		}
	}

	if err := e.storeOpContent(&op); err != nil {
		return "", fmt.Errorf("stage: append: %w", err)
	}
	if err := e.captureSourceSHAs(&op); err != nil {
		return "", fmt.Errorf("stage: append: %w", err)
	}

	// D-8H: the id draw and the cache insert are one openMu critical
	// section, so no concurrent Current can rewind the counter under the
	// drawn id or repopulate the cache from a disk copy that lacks this op.
	// staged is the cached changeset the op landed in — the object the
	// whole persist tail below reads and writes, so cache and disk stay the
	// same thing. prevChecks is what disk still holds if the tail fails.
	prevChecks := c.Checks
	staged, err := e.stageAppendOp(c, &op)
	if err != nil {
		return "", err
	}

	checks, err := e.recomputeChecks(staged)
	if err != nil {
		e.unstageAppendOp(staged, op.ID, prevChecks)
		return "", fmt.Errorf("stage: append: %w", err)
	}
	// Checks lands under openMu: a concurrent Current's copy
	// (cachedOpenCopy) reads the cached struct under the same lock, and
	// this write must not tear against it (008 D-8H). The recompute above
	// only reads the cached ops, so it can stay outside the lock.
	e.openMu.Lock()
	staged.Checks = checks
	e.openMu.Unlock()

	if err := writeChangesetJSON(filepath.Join(e.changesetOpenDir(), staged.ID), staged); err != nil {
		e.unstageAppendOp(staged, op.ID, prevChecks)
		return "", fmt.Errorf("stage: append: %w", err)
	}

	if err := e.appendJournal(Event{
		TS:        e.now().UTC(),
		Kind:      EvOpProposed,
		Changeset: staged.ID,
		Op:        op.ID,
		Actor:     staged.Author,
		Paths:     opTouches(op),
	}); err != nil {
		// changeset.json already carries the op, so the cache must keep it
		// too — and hold this object, in case a mid-tail invalidation left
		// the pointer elsewhere. The error still reports: the journal is
		// the audit trail, and an op_proposed that never landed is a
		// pre-existing inconsistency this method does not widen.
		e.cacheOpen(staged)
		return "", fmt.Errorf("stage: append: %w", err)
	}

	e.cacheOpen(staged)
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

	return e.appendJournal(Event{
		TS:        e.now().UTC(),
		Kind:      EvHunkDropped,
		Changeset: c.ID,
		Op:        opID,
		Hunk:      hunkID,
		Actor:     c.Author,
		Paths:     opTouches(*op),
	})
}

// UndropHunk clears the Dropped flag on hunk hunkID of op opID, recomputes
// that op's projected content from Before plus its now-live hunks, stores
// it, overwrites Op.After with the resulting sha, recomputes Checks,
// persists, and journals hunk_undropped.
//
// Contract (backbone §5.4, MASTER §9 D-CL): the exact inverse of DropHunk,
// mirrored line for line — see that method's contract for why each step
// exists. Undropping a hunk that is already live (Dropped already false)
// is a no-op that succeeds: the review screen's "y" is pressed on every
// hunk it walks past, including ones never dropped, and it must not error
// just because the reviewer agreed with the default — recomputing an
// already-live hunk's content reproduces the same After sha, so nothing
// observable changes. An unknown op or hunk id is still an error, worded
// exactly as DropHunk's.
func (e *Engine) UndropHunk(opID, hunkID string) error {
	c, err := e.currentOpen()
	if err != nil {
		return err
	}

	op, ok := c.Op(opID)
	if !ok {
		return fmt.Errorf("stage: undrop hunk: no such op %q", opID)
	}

	var hunk *Hunk
	for i := range op.Hunks {
		if op.Hunks[i].ID == hunkID {
			hunk = &op.Hunks[i]
			break
		}
	}
	if hunk == nil {
		return fmt.Errorf("stage: undrop hunk: op %s has no hunk %q", opID, hunkID)
	}
	hunk.Dropped = false

	before, err := e.store.Get(op.Before)
	if err != nil {
		return fmt.Errorf("stage: undrop hunk: %w", err)
	}
	newContent := applyHunks(before, op.Hunks)
	sha, err := e.store.Put(newContent)
	if err != nil {
		return fmt.Errorf("stage: undrop hunk: %w", err)
	}
	op.After = sha

	if err := e.persistAfterMutation(c); err != nil {
		return err
	}

	return e.appendJournal(Event{
		TS:        e.now().UTC(),
		Kind:      EvHunkUndropped,
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

	return e.appendJournal(Event{
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
	e.cacheOpen(c)
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
	e.cacheOpen(c)
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

	if err := e.appendJournal(Event{
		TS:        e.now().UTC(),
		Kind:      EvChangesetRejected,
		Changeset: c.ID,
		Actor:     c.Author,
		Message:   reason,
	}); err != nil {
		return fmt.Errorf("stage: reject: %w", err)
	}

	e.forgetOpen()
	return nil
}
