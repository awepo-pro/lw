// writer.go implements the single-writer model (008 contract §10,
// amendment A-803, user decision D-8I). One engine-side rule replaces the
// per-path rules four consecutive reviews kept finding holes in:
//
//  1. Every verb that mutates the open changeset or persists
//     changeset.json holds e.writeMu for its whole body (see writeMu's
//     field comment in engine.go for the verb list and the lock order).
//  2. A writer works on a PRIVATE copy — writerOpen hands out a clone of
//     the published cache, or a fresh disk object after the coherence
//     check — and publishes it under openMu only once it is persisted. No
//     writer mutates a published object in place, which closes F-805-2
//     (unlocked field writes racing Current's clone) and F-805-3
//     (unstageAppendOp's single-appender assumption) at the root.
//  3. Coherence before mutating (F-805-1): the writer compares
//     changeset.json's (size, mtime) against csStamp — the stamp recorded
//     when the cache was published — and re-reads the changeset from disk
//     when they differ, so a stale cache can never write a whole-file copy
//     over another process's change. The stamp is refreshed on every
//     persist (persistAndPublish) and every rehydration
//     (cacheOpenRehydrated).
//
// Residual, accepted by A-803 and restated here: two processes writing
// within the same stat→write window are still last-writer-wins, and the
// loser's change is lost without a signal only if it lands inside that
// window — microseconds, versus the seconds-long window F-805-1 measured.
// Commit keeps the exclusive lock file (backbone §5.2); no other verb
// takes it, per A-803's documented deviation from the user's sketch.
package stage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// openChangesetStamp stats the open changeset's changeset.json. ok is
// false when the file cannot be stated at all — missing (the state between
// OpenChangeset's id draw and its first persist) or unreadable — and in
// both cases the caller must treat the cache as possibly stale. The stamp
// the A-803 coherence check compares is exactly this fingerprint; mtime
// from os.Stat carries no monotonic reading, so journalStamp's == compare
// is sound.
func openChangesetStamp(dir, id string) (stamp journalStamp, ok bool) {
	info, err := os.Stat(filepath.Join(dir, id, "changeset.json"))
	if err != nil {
		return journalStamp{}, false
	}
	return journalStamp{size: info.Size(), mtime: info.ModTime()}, true
}

// readOpenFromDisk reads the open changeset the way a fresh process would:
// directory walk under changesets/open/, then changeset.json. The returned
// object is unshared — nothing in the engine aliases it — and stamp
// describes the file the bytes were read from, taken BEFORE the read: the
// stamp is then never newer than the content, so any later mismatch means
// "content may be stale" and triggers the safe re-read, never a skipped
// one. A stat that fails is swallowed into a zero stamp — which the
// coherence check reads as permanently stale, the fail-closed direction —
// because the READ owns the error shape: a fabricated directory without a
// changeset.json must fail naming that file (cmd/lw's C-118 test pins the
// text).
//
// Error shapes match rehydrateOpen's historical ones ("stage: current:
// %w", raw ErrNoChangeset) because every caller's error text flows
// straight through to the verb it serves.
func (e *Engine) readOpenFromDisk() (*Changeset, journalStamp, error) {
	openDir := e.changesetOpenDir()
	entries, err := os.ReadDir(openDir)
	if err != nil {
		return nil, journalStamp{}, fmt.Errorf("stage: current: %w", err)
	}

	var id string
	for _, ent := range entries {
		if ent.IsDir() {
			id = ent.Name()
			break
		}
	}
	if id == "" {
		return nil, journalStamp{}, ErrNoChangeset
	}

	path := filepath.Join(openDir, id, "changeset.json")
	stamp, _ := openChangesetStamp(openDir, id)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, journalStamp{}, fmt.Errorf("stage: current: %w", err)
	}
	var c Changeset
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, journalStamp{}, fmt.Errorf("stage: current: %w", err)
	}
	return &c, stamp, nil
}

// writerOpen is the mutating verbs' one entry into the open changeset
// (A-803). It returns the changeset to mutate — always private to the
// caller, never the published cache object — after the coherence check:
//
//   - cache miss: rehydrateOpen reads disk, publishes the cache's own
//     clone plus the fresh stamp, and hands back its unshared object.
//   - cache hit with an unchanged stamp: the cache describes disk, so the
//     caller gets a deep clone of it.
//   - cache hit with anything else — a CHANGED stamp (a foreign process
//     rewrote changeset.json since this cache was published, F-805-1), a
//     REMOVED one (a foreign commit), or an unstattable one: disk is the
//     truth, so the changeset is re-read and republished, and the op
//     counter is raised to max(in-memory, 1+maxOpN(disk)) — never
//     rewound (R-805). The caller mutates that fresh object.
//
// A cache that names a changeset no longer on disk (a foreign process
// committed it) errors with the same "no such file" the cold engine's
// read produces — instead of resurrecting the committed directory.
//
// The caller must hold writeMu. The stat runs under writeMu, never under
// openMu, so openMu stays I/O-free.
func (e *Engine) writerOpen() (*Changeset, error) {
	e.openMu.Lock()
	c := e.open
	stamp := e.csStamp
	e.openMu.Unlock()

	if c == nil {
		return e.rehydrateOpen()
	}

	if cur, ok := openChangesetStamp(e.changesetOpenDir(), c.ID); ok && cur == stamp {
		return c.clone(), nil
	}

	// The published cache no longer describes disk (or cannot be proven
	// to). Re-read, republish, and hand the writer the fresh object; the
	// counter rises only.
	fresh, freshStamp, err := e.readOpenFromDisk()
	if err != nil {
		return nil, err
	}
	e.cacheOpenRehydrated(fresh.clone(), 1+maxOpN(fresh.Ops), freshStamp)
	return fresh, nil
}

// drawAppendIDs numbers op — its cascade sub-ops included — from the
// engine's op counter, under openMu so the draw cannot tear against a
// concurrent Current rehydration lifting the same counter (that reader
// path holds no writeMu). The caller holds writeMu, so no other writer can
// be drawing at the same time; the guard below nevertheless re-checks the
// cache identity under openMu, preserving stageAppendOp's old protection:
// if a rehydration republished a DIFFERENT changeset while this Append was
// preparing (a foreign commit plus a foreign open, seen mid-tail), the op
// is refused rather than grafted onto the wrong changeset or persisted
// back over it.
//
// The op is inserted into the caller's private copy afterwards — not into
// the cache (that was D-8H's shape, superseded by A-803's
// copy-mutate-publish). The id draw still happens under openMu because
// e.nextOp is openMu-guarded state, but the cache is only touched again at
// publish time.
func (e *Engine) drawAppendIDs(c *Changeset, op *Op) error {
	e.openMu.Lock()
	defer e.openMu.Unlock()
	if e.open != nil && e.open.ID != c.ID {
		return fmt.Errorf("stage: append: the open changeset changed while the op was being prepared: %s is open, not %s",
			e.open.ID, c.ID)
	}
	e.assignIDs(op)
	return nil
}

// persistAndPublish is a writer's persist tail (A-803): an optional
// test-only failure seam, the atomic changeset.json write, and — only on
// success — the publish that moves the private copy into the cache
// together with the stamp of the file just written. Because the publish
// happens strictly after the write succeeded, a writer that fails before
// its persist publishes nothing and the cache still equals disk.
//
// The stamp is captured from the temp file BEFORE the rename
// (writeChangesetJSON): rename(2) preserves size and mtime, so it is
// exactly the fingerprint the renamed file carries — there is no
// post-rename stat and therefore no window in which a foreign write could
// be recorded as this one.
func (e *Engine) persistAndPublish(c *Changeset) error {
	if e.failBeforePersist != nil {
		if err := e.failBeforePersist(); err != nil {
			return err
		}
	}
	stamp, err := writeChangesetJSON(filepath.Join(e.changesetOpenDir(), c.ID), c)
	if err != nil {
		return err
	}
	e.publishWritten(c, stamp)
	return nil
}

// publishWritten moves the persisted copy into the cache under openMu —
// the short publish A-803 allows a writeMu holder. The caller holds
// writeMu; nothing else publishes between this writer's persist and its
// publish except a concurrent Current's rehydration of an emptied cache,
// which this publish supersedes.
func (e *Engine) publishWritten(c *Changeset, stamp journalStamp) {
	e.openMu.Lock()
	e.open = c
	e.csStamp = stamp
	e.openMu.Unlock()
}
