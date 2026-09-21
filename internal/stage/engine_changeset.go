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
// trailing "\n". It returns the (size, mtime) fingerprint the renamed file
// carries, captured off the temp file after the final sync — rename(2)
// preserves size and mtime, so this is exactly the stamp the A-803
// coherence check compares against on the next write, with no post-rename
// stat and no window in which a foreign write could be recorded as ours.
//
// The caller must have created dir (A-804, F-806-1): OpenChangeset is the
// only legitimate creator of a changeset directory, and a persist that
// recreated one would resurrect a changeset another process just renamed
// away — so this function never mkdirs, and a missing directory fails at
// CreateTemp instead.
func writeChangesetJSON(dir string, c *Changeset) (journalStamp, error) {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return journalStamp{}, fmt.Errorf("stage: marshal changeset %s: %w", c.ID, err)
	}
	b = append(b, '\n')

	tmp, err := os.CreateTemp(dir, "tmp-*")
	if err != nil {
		return journalStamp{}, fmt.Errorf("stage: write changeset %s: %w", c.ID, err)
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
		return journalStamp{}, fmt.Errorf("stage: write changeset %s: %w", c.ID, err)
	}
	if err := tmp.Sync(); err != nil {
		return journalStamp{}, fmt.Errorf("stage: write changeset %s: %w", c.ID, err)
	}
	info, err := tmp.Stat()
	if err != nil {
		return journalStamp{}, fmt.Errorf("stage: write changeset %s: %w", c.ID, err)
	}
	stamp := journalStamp{size: info.Size(), mtime: info.ModTime()}
	if err := tmp.Close(); err != nil {
		return journalStamp{}, fmt.Errorf("stage: write changeset %s: %w", c.ID, err)
	}
	if err := os.Rename(tmpPath, filepath.Join(dir, "changeset.json")); err != nil {
		return journalStamp{}, fmt.Errorf("stage: write changeset %s: %w", c.ID, err)
	}
	ok = true
	return stamp, nil
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
//
// A-803: the whole body holds writeMu, so two goroutines of one process
// can no longer both pass the empty-directory check and race two
// directories into open/ — the second serializes behind the first and
// sees its directory. The cache is published with the coherence stamp of
// the changeset.json just written, so the next mutating verb's check
// matches until someone else writes the file.
func (e *Engine) OpenChangeset(intent string, a Author) (*Changeset, error) {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()

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
	// A-804 (F-806-1): OpenChangeset is the only creator of a changeset
	// directory — open/ was just proven to exist by the ReadDir above — so
	// the persist path itself never mkdirs and can never resurrect a
	// directory a foreign process renamed away.
	if err := os.Mkdir(dir, 0o755); err != nil {
		return nil, fmt.Errorf("stage: open changeset: %w", err)
	}
	stamp, err := writeChangesetJSON(dir, c)
	if err != nil {
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
	// cache pointer moves — from that moment on, the changeset is the
	// engine's published state, never mutated in place again (A-803).
	clone := c.clone()
	e.cacheOpenAt(c, 1, stamp)
	return clone, nil
}

// Current returns the currently open changeset, ErrNoChangeset when none
// is open.
//
// Contract (backbone §5.4, MASTER §9 D-BB; cache behaviour amended 008
// D-8H, reader side restated by A-803): while the engine holds a cached
// open changeset, Current returns a deep COPY of it and touches neither
// the cache, the op counter nor the filesystem — an Append that has drawn
// an id but not yet persisted must never be rewound, and a display read
// must never overwrite the cache with the on-disk copy it temporarily
// trails. Only when nothing is cached does it read
// changesets/open/<id>/changeset.json (rehydrateOpen), which raises
// e.nextOp to 1+max(N) over every op<N> in the loaded changeset, cascade
// sub-ops included — raised, never lowered, since disk cannot see an
// in-flight Append's id (cacheOpenRehydrated). OpenEngine populates
// neither field, so a fresh engine derives both on its first Current.
//
// Under A-803 a mutating verb never edits the published changeset in
// place — it works on a private copy and swaps the pointer once persisted
// — so this copy always shows one whole published state: the previous one
// or the next, never a half-applied field set.
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
// The coherence stamp is recorded beside the cache (A-803), taken before
// the read so it can never describe a newer state than the bytes the cache
// holds. ErrNoChangeset when changesets/open/ holds no changeset.
func (e *Engine) rehydrateOpen() (*Changeset, error) {
	c, stamp, err := e.readOpenFromDisk()
	if err != nil {
		return nil, err
	}

	// c is unshared until cacheOpenRehydrated, so the counter derivation
	// and the cache's own clone both read it before the engine takes
	// ownership of its copy.
	nextOp := 1 + maxOpN(c.Ops)
	e.cacheOpenRehydrated(c.clone(), nextOp, stamp)
	return c, nil
}

// currentOpen returns the cached open changeset, rehydrating it from disk
// first when nothing is cached — the seam that lets read-side surfaces
// (Diff, OpDiff, StagedFile, ProjectedReport) and Reject work correctly
// whether or not the caller already called Current in this process
// (backbone §5.4, MASTER §9 D-BB). The cache pointer moves under openMu
// (008 A-802), so a concurrent ReloadIfChanged clearing it is either seen
// whole or not at all.
//
// In-package use only: the result is the published cache object, which
// since A-803 is never mutated in place — writers work on private copies
// (writerOpen) — so a reader may hold it and read its fields freely. It
// and its slices must not escape internal/stage; exported surfaces hand
// out copies (Current, cachedOpenCopy, OpenChangeset). Mutating verbs do
// not come through here: they call writerOpen for the coherence check.
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
// The caller must hold openMu (008 A-802, D-8H, A-803): drawAppendIDs runs
// the whole draw — the op's and every cascade sub-op's — in one critical
// section, so the counter cannot move under a drawn id and a concurrent
// Current's rehydration can only lift it, never lower it. The counter is
// read and advanced directly rather than through a helper — sync.Mutex is
// not reentrant, and the lock is already held.
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
// assign op<N> (recursively through Cascade, MASTER §9 D-AK; the draw is
// one openMu critical section, drawAppendIDs) -> recompute Checks by
// materializing the projected tree in memory and running lint -> persist
// changeset.json atomically -> journal op_proposed with TS: e.now().UTC().
// Like every §5.4 method except Commit it never takes the vault lock
// (§5.2), and openMu is held only for the id draw and the publish — never
// across I/O.
//
// A-803: the whole body holds writeMu, and every step after writerOpen
// works on a PRIVATE copy of the open changeset — the published cache is
// only swapped at the persist (persistAndPublish). A failure anywhere
// before that persist therefore publishes nothing, and the cache still
// equals what is on disk; there is no unstage step to get right. The id
// counter never rewinds (R-804/R-805), so the drawn id stays valid across
// a cache invalidation. A journal failure after the persist keeps the op
// in cache and disk alike — the error still reports, since an op_proposed
// that never landed is a pre-existing inconsistency this method does not
// widen.
//
// For rename_page and merge_pages, Append computes op.Cascade itself from
// From/To (or Sources/To) before validating — the rewrite algorithm needs
// vault-wide graph and resolution access no proposer outside this package
// can safely reproduce, and getting it wrong is the exact silent
// corruption backbone §5.5's Contract exists to prevent (MASTER §9 D-AM,
// D-Y). Any Cascade the caller supplied is replaced, not merged.
func (e *Engine) Append(op Op) (string, error) {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()

	c, err := e.writerOpen()
	if err != nil {
		return "", err
	}

	// cv is the vault this op is validated — and, for rename_page and
	// merge_pages, whose cascade is built — against. For the first op of
	// any changeset cascadeBase returns e.vault itself, so every
	// single-op changeset takes byte-for-byte its pre-OR-13 path. Once
	// live ops exist, a content op (create_page, patch_page) validates
	// against the projection of those ops too (020 T-A): a second op on a
	// staged path COMPOSES with the first — its Before is the staged sha,
	// the section check reads the staged body, and a create on a staged
	// path sees its own predecessor and is refused — while a path no live
	// op touches is seeded from disk in the projection, so behaviour
	// there is byte-for-byte what it was before.
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
	case OpCreatePage, OpPatchPage:
		// Root-file patches are the exception (020 T-A): rootfile
		// validation reads v.Root() — checkRootFileOneWriter walks the
		// on-disk open changeset from it, and the projection is rootless
		// (openProjection) — so a patch on one of OQ-9's named root
		// files keeps validating against the working tree, exactly as
		// before. Root files are not chainable content: the one-writer
		// guard's patch-vs-patch arm (checkRootFileOneWriter, rootfile.go,
		// 020 FIX-1) refuses a second patch on an already-patched root
		// file at proposal time, so no staged predecessor for a root file
		// can exist.
		if op.Kind == OpPatchPage && isKnownRootFile(op.Path) {
			break
		}
		var err error
		if cv, err = e.cascadeBase(c.Live()); err != nil {
			return "", fmt.Errorf("stage: append: %w", err)
		}
	}

	// validateOpForAppend is ValidateOp with the committed vault named
	// separately (020 FIX-1): cv may be a projection, and the
	// already-exists/basename-collision refusals must say when their
	// blocker exists only in that staged state.
	if err := validateOpForAppend(op, cv, e.vault, cv.Schema()); err != nil {
		return "", err
	}

	// OQ-9 L2's mirror direction, closed for real (S4-T0 repair-2), widened
	// by 020 T-A: cv is a rootless fstest.MapFS projection (cascadeBase,
	// openProjection) whenever the changeset already carries another live
	// op — precisely when a root-file collision can exist — so the
	// checkNewWriterOneWriter(cv, ...) calls validateCascade and
	// validateCreatePage make read an empty Root() there and cannot see
	// the on-disk changeset. e.vault is always the real, disk-backed
	// vault, so this repeats the same check against it for every
	// automatic-writer kind: rename_page, merge_pages, and — since T-A
	// made create_page validate against the projection too — create_page.
	if op.Kind == OpRenamePage || op.Kind == OpMergePages || op.Kind == OpCreatePage {
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

	// The id draw is one openMu critical section (A-803: the counter is
	// openMu state, and a reader-side rehydration lifts it without holding
	// writeMu). The op is inserted into the private copy below — the cache
	// only sees it at the persist.
	if err := e.drawAppendIDs(c, &op); err != nil {
		return "", err
	}
	c.Ops = append(c.Ops, op)

	// Checks are recomputed over the whole projected tree with the new op
	// in place, on the private copy — a recompute failure publishes
	// nothing, which is what the old unstage step was for.
	checks, err := e.recomputeChecks(c)
	if err != nil {
		return "", fmt.Errorf("stage: append: %w", err)
	}
	c.Checks = checks

	if err := e.persistAndPublish(c); err != nil {
		return "", fmt.Errorf("stage: append: %w", err)
	}

	if err := e.appendJournal(Event{
		TS:        e.now().UTC(),
		Kind:      EvOpProposed,
		Changeset: c.ID,
		Op:        op.ID,
		Actor:     c.Author,
		Paths:     opTouches(op),
	}); err != nil {
		// changeset.json already carries the op and the cache was already
		// published with it, so cache and disk agree — nothing to undo.
		// The error still reports: the journal is the audit trail, and an
		// op_proposed that never landed is a pre-existing inconsistency
		// this method does not widen.
		return "", fmt.Errorf("stage: append: %w", err)
	}

	return op.ID, nil
}

// DropHunk marks the hunk hunkID of op opID as dropped, recomputes that
// op's projected content from Before plus its remaining live hunks,
// stores it, overwrites Op.After with the resulting sha, recomputes
// Checks, persists, and journals hunk_dropped.
//
// Contract (backbone §5.4): After always describes what Commit will
// actually write, never the pre-drop proposal. A-803: the whole body
// holds writeMu and mutates writerOpen's private copy — the R-805 review's
// gap (a failure between the field write and the persist left the cache
// ahead of disk) cannot recur, because the field write lands on the copy
// and the copy is published only by the persist itself.
func (e *Engine) DropHunk(opID, hunkID string) error {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()

	c, err := e.writerOpen()
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
// exactly as DropHunk's. Like DropHunk it holds writeMu for its whole
// body and mutates a private copy (A-803).
func (e *Engine) UndropHunk(opID, hunkID string) error {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()

	c, err := e.writerOpen()
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
//
// A-803: the whole body holds writeMu and mutates writerOpen's private
// copy. This is the verb F-805-2's probe caught writing a cached field
// with no lock at all; under the single-writer model the field write lands
// on a copy no reader can observe until the publish, so the DropOp-vs-
// Current pair is race-free by construction rather than by discipline.
func (e *Engine) DropOp(opID string) error {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()

	c, err := e.writerOpen()
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

// refreshOpStates runs Refresh's per-op staleness pass over every live op
// in c: each is re-anchored on cascadeBase(liveBefore(c.Ops, i)) — the
// projection of the live ops preceding it, which is the working tree
// itself for a path no predecessor touches — and refreshOp's own Cascade
// recursion re-checks sub-ops against that same base, the walk OR-13's
// refreshCascadeStates used to perform separately.
//
// 020 T-A made this the shared tail of Refresh AND the drop verbs. A
// DropOp or DropHunk on a chain's head changes what its dependents will
// apply to (a dropped predecessor stops projecting; a hunk drop rewrites
// its After), so persistAfterMutation must re-anchor TOP-LEVEL ops in the
// same verb — otherwise a dependent whose Before is the head's After would
// keep looking fresh until some later Refresh happened to run, and a
// commit could silently apply a patch computed over a rewrite the
// reviewer just removed. Terminal ops are skipped: Dropped and Rejected
// are review decisions this pass must not disturb, and Live() excludes
// them from what Commit writes anyway.
func (e *Engine) refreshOpStates(c *Changeset) error {
	for i := range c.Ops {
		if c.Ops[i].State == StateDropped || c.Ops[i].State == StateRejected {
			continue
		}
		base, err := e.cascadeBase(liveBefore(c.Ops, i))
		if err != nil {
			return err
		}
		refreshOp(&c.Ops[i], e.vault, base)
	}
	return nil
}

// persistAfterMutation recomputes c.Checks and persists c to disk — the
// shared tail of DropHunk, UndropHunk and DropOp. The caller holds
// writeMu and owns c as a private copy (A-803); persistAndPublish
// publishes it only once the write has landed.
func (e *Engine) persistAfterMutation(c *Changeset) error {
	if err := e.refreshOpStates(c); err != nil {
		return fmt.Errorf("stage: recheck states: %w", err)
	}

	checks, err := e.recomputeChecks(c)
	if err != nil {
		return fmt.Errorf("stage: recompute checks: %w", err)
	}
	c.Checks = checks

	if err := e.persistAndPublish(c); err != nil {
		return fmt.Errorf("stage: persist changeset: %w", err)
	}
	return nil
}

// Refresh re-hashes the working tree against every live op's staleness
// anchor and flips State to StateStale where it no longer matches.
//
// Contract (backbone §5.4, per-kind table, MASTER §9 D-AJ, D-BC): it never
// recomputes Hunks — only State — leaving the last-computed hunks and
// their Dropped flags intact. A-803: the whole body holds writeMu and the
// flips land on writerOpen's private copy, so a persist failure leaves the
// published cache exactly what disk still holds.
func (e *Engine) Refresh() error {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	return e.refreshWriteLocked()
}

// refreshWriteLocked is Refresh's body without writeMu — Commit holds
// writeMu across its whole body and calls this directly (A-803: sync.Mutex
// is not reentrant, so a holder never re-locks).
func (e *Engine) refreshWriteLocked() error {
	c, err := e.writerOpen()
	if err != nil {
		return err
	}

	if err := e.refreshOpStates(c); err != nil {
		return fmt.Errorf("stage: refresh: %w", err)
	}

	if err := e.persistAndPublish(c); err != nil {
		return fmt.Errorf("stage: refresh: %w", err)
	}
	return nil
}

// Reject moves the open changeset from open/<id> to rejected/<id> — never
// a delete — and journals changeset_rejected.
//
// Contract (backbone §5.4, §14, MASTER §9 D-AI): rejections are permanent
// history. A-803: the whole body holds writeMu. Reject mutates no
// changeset content — the rename moves whatever is on disk — so unlike
// the writing verbs it enters through currentOpen, not writerOpen: with a
// stale cache naming a changeset a foreign process already committed, the
// rename fails with ENOENT and the caller finds out, rather than silently
// rejecting a different changeset the user never saw.
func (e *Engine) Reject(reason string) error {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()

	c, err := e.currentOpen()
	if err != nil {
		return err
	}

	src := filepath.Join(e.changesetOpenDir(), c.ID)
	dst := filepath.Join(e.changesetRejectedDir(), c.ID)
	// rejected/ is §14 layout OpenEngine guarantees; this verb creates no
	// directories (A-804, F-806-1), so a missing one fails the rename
	// instead of being silently recreated.
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
