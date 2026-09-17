// engine.go declares the Engine skeleton (backbone §5.4, MASTER §9 D-AO)
// and implements the four §5.4 methods S2-T1 owns — OpenEngine, Vault,
// Index, Close — plus Journal and the llmwikiDir seam. Every other Engine
// method arrives from a later wave's own file. The complete field set is
// declared here, once, because Go declares a struct's fields exactly once
// and a later wave may not edit this file (00-conventions.md §1 rule 4).
package stage

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/vault"
)

// Engine is the staging engine: the mutable state around a vault's
// .llmwiki/ directory that every stage operation goes through.
type Engine struct {
	root       string // vault root, absolute
	vault      *vault.Vault
	index      *index.Index
	store      *Store
	journal    *Journal
	unlock     func() error            // nil unless Commit holds the lock; guarded by lifecycleMu
	now        func() time.Time        // injected (00-conventions.md §3)
	rand       io.Reader               // injected; id entropy
	faultAfter func(step string) error // test-only; nil in production
	// invalidateOpen is a test-only hook, nil in production: Commit runs it
	// just before reading the cached open changeset, so a test can park a
	// forgetOpen in the Refresh→cachedOpen window deterministically
	// (008 F-R2) instead of racing a real one into it.
	invalidateOpen func()
	// failBeforePersist is a test-only hook, nil in production: when set,
	// persistAndPublish returns its error just before changeset.json is
	// written, so single_writer_test.go can prove a writer that fails
	// before its persist publishes nothing (A-803) without racing a real
	// disk failure into the window.
	failBeforePersist func() error
	forceNext         bool // next Commit overrode a lint regression (D-AG); guarded by lifecycleMu
	// lifecycleMu guards the commit-lifecycle fields unlock and forceNext
	// (008 contract §11, amendment A-804; ORCH-806): Commit writes both,
	// ForceNextCommit and ReloadIfChanged read them, and those run on
	// different goroutines in a library caller, so plain fields were a true
	// data race. It is a leaf lock — holders do nothing but read or write
	// those two fields; never held across I/O or another lock, and never
	// taken while openMu or journalStampMu is held (the reverse is fine:
	// ReloadIfChanged checks the lock between its own lock-guarded reads).
	lifecycleMu sync.Mutex
	// writeMu is the single-writer lock (008 contract §10, amendment
	// A-803): every verb that mutates the open changeset or persists
	// changeset.json — OpenChangeset, Append, DropHunk, UndropHunk, DropOp,
	// Refresh, Reject, Commit — holds it for its whole body. A writer works
	// on a private copy (writerOpen in writer.go) and publishes it under
	// openMu only once it is on disk, so a published changeset is never
	// mutated in place: readers see one whole state or the next, never a
	// half-applied field set. Lock order: writeMu → openMu, never the
	// reverse; writeMu → journalStampMu (via appendJournal); openMu and
	// journalStampMu are never taken together, and openMu is never held
	// across any I/O. ReloadIfChanged deliberately does NOT take writeMu —
	// it only invalidates, and a TUI tick must not queue behind a
	// multi-second Commit. Refresh's and Commit's bodies are reachable
	// lock-free as refreshWriteLocked / commitWriteLocked because Commit
	// calls Refresh (sync.Mutex is not reentrant); no other verb calls
	// another, so they lock in their own body directly.
	writeMu sync.Mutex
	// openMu guards open, nextOp and csStamp (008 A-802, A-803): the reload
	// tick's ReloadIfChanged, an agent turn's Current/Append and a review
	// load all touch those fields from different goroutines, so every read
	// and write goes through the cacheOpen*/forgetOpen/cachedOpen/
	// cachedOpenCopy/drawAppendIDs/publishWritten helpers. It is a
	// short-held lock: never across vault, index, CAS, stat or journal I/O
	// — a writer's coherence stat and persist run under writeMu, and only
	// the pointer/stamp moves go through openMu (A-803).
	openMu sync.Mutex
	open   *Changeset // nil when none is open; guarded by openMu
	nextOp int        // op<N> counter, incl. cascade sub-ops; guarded by openMu
	// csStamp is the (size, mtime) of the changeset.json the published
	// cache was persisted from or rehydrated from — the coherence stamp the
	// A-803 writer check compares against disk before mutating (F-805-1).
	// Guarded by openMu, beside the cache it describes; the stat that
	// produces or compares it always runs under writeMu, never under
	// openMu, so openMu stays I/O-free.
	csStamp journalStamp
	// journalStampMu guards journalStamp: every journal append this Engine
	// makes refreshes the stamp, and ReloadIfChanged (reload.go, 008
	// contract §3) stats the journal and compares it under the same lock,
	// so an own append can never land between the stat and the comparison
	// and be mistaken for a foreign write.
	journalStampMu sync.Mutex
	journalStamp   journalStamp
}

// cacheOpen stores c as the engine's cached open changeset, leaving the op
// counter and the coherence stamp alone. A-803 gave it no production
// callers — every writer now publishes through publishWritten or
// cacheOpenAt with the stamp that matches what it just wrote — but it
// remains the exact pointer-move a test needs to install a forged cache
// object (staged_test.go) without disturbing the stamp that still
// describes disk.
func (e *Engine) cacheOpen(c *Changeset) {
	e.openMu.Lock()
	e.open = c
	e.openMu.Unlock()
}

// cacheOpenAt stores c as the cached open changeset together with the op
// counter and the coherence stamp it was persisted with — the shape
// OpenChangeset publishes with (A-803).
func (e *Engine) cacheOpenAt(c *Changeset, nextOp int, stamp journalStamp) {
	e.openMu.Lock()
	e.open = c
	e.nextOp = nextOp
	e.csStamp = stamp
	e.openMu.Unlock()
}

// forgetOpen clears the cached open changeset and leaves the op counter
// alone. Zeroing nextOp would let an Append already holding the old
// changeset draw a duplicate op<N> — it numbers through assignIDs without
// consulting Current again (008 F-R1), and since D-8H a rehydration lifts
// the counter instead of setting it, so the rule has no exception left. A
// stale-high counter at worst leaves a gap in the ids, which is harmless
// and self-heals: every cache repopulation raises nextOp to 1+maxOpN from
// disk. A duplicate id in a persisted changeset is not harmless, so the
// counter is never rewound.
func (e *Engine) forgetOpen() {
	e.openMu.Lock()
	e.open = nil
	e.openMu.Unlock()
}

// cachedOpen returns the cached open changeset, or nil when none is cached.
// In-package callers only: the result is the live cache object, not a copy,
// so it and its slices must not escape the package (D-8H) — anything handed
// to callers outside internal/stage goes through cachedOpenCopy.
func (e *Engine) cachedOpen() *Changeset {
	e.openMu.Lock()
	defer e.openMu.Unlock()
	return e.open
}

// cachedOpenCopy returns a deep copy of the cached open changeset for the
// callers a value escapes to (008 D-8H), nil when none is cached. The copy
// is taken under openMu so it can never catch a cache swap half-done; the
// object it clones is a published state that A-803 writers never mutate in
// place, so the result is whole either way.
func (e *Engine) cachedOpenCopy() *Changeset {
	e.openMu.Lock()
	defer e.openMu.Unlock()
	return e.open.clone()
}

// cacheOpenRehydrated stores c — a changeset just read back from disk — as
// the cached open changeset, LIFTS the op counter to nextOp (never
// lowering it) and records the coherence stamp the read was taken with.
// Ownership of c transfers to the cache: callers hand over their own deep
// copy (rehydrateOpen passes c.clone()) and keep their object unshared.
// The raise-only counter is the D-8H amendment to rehydration: an Append
// whose id is drawn but not yet persisted may be in flight while Current
// reads disk, and disk cannot see that op, so setting nextOp = 1+maxOpN(disk)
// flat could rewind the counter under the drawn id and hand the next
// Append a duplicate. Raising it instead keeps the R-804 rule without
// exception: gaps are harmless and self-heal, duplicates are not.
func (e *Engine) cacheOpenRehydrated(c *Changeset, nextOp int, stamp journalStamp) {
	e.openMu.Lock()
	e.open = c
	if nextOp > e.nextOp {
		e.nextOp = nextOp
	}
	e.csStamp = stamp
	e.openMu.Unlock()
}

// ForceNextCommit marks the next Commit as one that overrode a lint
// regression refusal, so its commit_end event carries "forced":true
// alongside the counts (backbone §5.7, MASTER §9 D-AG).
//
// Contract (MASTER §9 D-CD): Commit consumes and clears the flag before
// it can fail, so a refused or failed commit can never leak it into a
// later one. It exists because the regression gate itself lives in
// cmd/lw — Engine.Commit performs no lint comparison and its §5.4
// signature is frozen — while the event that must record the override is
// written inside Commit. Without this seam the CLI can only append a
// SECOND commit_end, which makes the journal state that one commit ended
// twice.
func (e *Engine) ForceNextCommit() {
	e.lifecycleMu.Lock()
	e.forceNext = true
	e.lifecycleMu.Unlock()
}

// takeForceNext consumes and returns the D-AG force flag under
// lifecycleMu — Commit's step 0 (A-804; the flag is lifecycle state like
// unlock, written by ForceNextCommit on another goroutine).
func (e *Engine) takeForceNext() bool {
	e.lifecycleMu.Lock()
	forced := e.forceNext
	e.forceNext = false
	e.lifecycleMu.Unlock()
	return forced
}

// setCommitUnlock records unlock as the release for the commit lock this
// Engine now holds (Commit's step 1), under lifecycleMu (A-804).
func (e *Engine) setCommitUnlock(unlock func() error) {
	e.lifecycleMu.Lock()
	e.unlock = unlock
	e.lifecycleMu.Unlock()
}

// commitLockHeld reports whether this Engine currently holds the vault
// commit lock — ReloadIfChanged's guard, evaluated before AND after its
// stamp read (008 contract §3 and §11, A-804).
func (e *Engine) commitLockHeld() bool {
	e.lifecycleMu.Lock()
	defer e.lifecycleMu.Unlock()
	return e.unlock != nil
}

// takeCommitUnlock returns and clears the held unlock under lifecycleMu,
// nil when none is held. Close's documented safe-to-call-twice contract
// rides on the take being atomic: exactly one caller ever receives a
// non-nil func (A-804; ORCH-806).
func (e *Engine) takeCommitUnlock() func() error {
	e.lifecycleMu.Lock()
	unlock := e.unlock
	e.unlock = nil
	e.lifecycleMu.Unlock()
	return unlock
}

// ErrNoChangeset, ErrOpenChangeset, ErrStale and ErrValidation are the
// sentinels backbone §5.4 defines for the Engine's changeset lifecycle.
// Declared here — alongside the struct that names them in doc comments
// elsewhere in the package — so no later wave invents its own.
//
// ErrIDCollision and ErrIDExhausted (MASTER §9 D-CI, S3-T0) join them here
// for the same reason: OpenChangeset's B1 defence and Commit's B2 backstop
// live in engine_changeset.go and apply.go respectively, but every
// sentinel the package exports is declared in this one file.
var (
	// ErrNoChangeset is returned by Current when no changeset is open.
	ErrNoChangeset = errors.New("stage: no open changeset")
	// ErrOpenChangeset is returned by OpenChangeset when one is already open.
	ErrOpenChangeset = errors.New("stage: a changeset is already open")
	// ErrStale is returned by Commit when Refresh finds a stale op.
	ErrStale = errors.New("stage: op is stale; working tree changed")
	// ErrValidation is returned by ValidateOp/Append when an op fails
	// validation.
	ErrValidation = errors.New("stage: op failed validation")
	// ErrIDCollision is returned by Commit when the changeset's id already
	// names a committed changeset (D-CI).
	ErrIDCollision = errors.New("stage: changeset id already committed")
	// ErrIDExhausted is returned by OpenChangeset when it could not draw a
	// free changeset id (D-CI).
	ErrIDExhausted = errors.New("stage: could not draw a free changeset id")
	// ErrNothingToCommit is returned by Commit when the open changeset has
	// no live op (every op dropped or rejected, or none was ever proposed).
	// Checked after the stale-op check and before commit_begin is
	// journalled, so a refused commit writes nothing. Like git's "nothing
	// to commit", it is an invariant, not a policy gate (008 contract §3,
	// C-802).
	ErrNothingToCommit = errors.New("stage: nothing to commit; the changeset has no live ops")
)

// llmwikiDir returns the absolute path to e.root's .llmwiki directory
// (MASTER §9 D-AS seam).
func (e *Engine) llmwikiDir() string {
	return filepath.Join(e.root, ".llmwiki")
}

// OpenEngine opens the vault rooted at vaultRoot, creating .llmwiki/ — the
// full backbone §14 layout — if it is absent or only partially populated.
//
// Contract (backbone §5.4, MASTER §9 D-AS): root is stored absolute
// (filepath.Abs of vaultRoot). It calls vault.Open(root), then creates
// .llmwiki/ with objects/, changesets/open/, changesets/committed/,
// changesets/rejected/ (D-AI), snapshots/ and an empty journal.ndjson. It
// opens the store and the journal. It never acquires the lock — only
// Commit does (§5.2). The index is a disposable cache: any error loading
// it, or a loaded index reporting StaleAgainst the vault, means rebuild
// with index.Build, never a fatal OpenEngine; the rebuilt index is Saved,
// and a failed Save is returned as an error since .llmwiki/ has already
// been proven writable. OpenEngine is idempotent — a second call on the
// same vault changes nothing on disk.
//
// Each opts entry is applied to the Engine immediately after it is built
// with its defaults (contract §6 note 3, MASTER C24) — before anything
// reads e.now or e.rand — so WithClock and WithEntropy pin every
// timestamp and changeset id the engine produces. Production passes no
// options.
func OpenEngine(vaultRoot string, opts ...Option) (*Engine, error) {
	root, err := filepath.Abs(vaultRoot)
	if err != nil {
		return nil, fmt.Errorf("stage: open engine: %w", err)
	}

	v, err := vault.Open(root)
	if err != nil {
		return nil, fmt.Errorf("stage: open engine: %w", err)
	}

	e := &Engine{
		root:  root,
		vault: v,
		now:   time.Now,
		rand:  rand.Reader,
	}
	// The options are applied here — before the rest of this function and
	// every later Engine method reads e.now or e.rand — so an injected
	// clock or entropy source covers the engine's whole life. The rest of
	// OpenEngine itself reads neither.
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}

	dir := e.llmwikiDir()
	dirs := []string{
		dir,
		filepath.Join(dir, "objects"),
		filepath.Join(dir, "changesets", "open"),
		filepath.Join(dir, "changesets", "committed"),
		filepath.Join(dir, "changesets", "rejected"),
		filepath.Join(dir, "snapshots"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("stage: open engine: %w", err)
		}
	}

	journalPath := filepath.Join(dir, "journal.ndjson")
	jf, err := os.OpenFile(journalPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("stage: open engine: %w", err)
	}
	if err := jf.Close(); err != nil {
		return nil, fmt.Errorf("stage: open engine: %w", err)
	}

	store, err := OpenStore(filepath.Join(dir, "objects"))
	if err != nil {
		return nil, fmt.Errorf("stage: open engine: %w", err)
	}
	e.store = store

	j, err := OpenJournal(journalPath)
	if err != nil {
		return nil, fmt.Errorf("stage: open engine: %w", err)
	}
	e.journal = j
	// The first journal stamp (008 contract §3): everything the journal
	// holds at this point is pre-existing history this Engine did not write,
	// and the stamp is what keeps ReloadIfChanged from calling it a change.
	e.stampJournal()

	indexPath := filepath.Join(dir, "index.gob")
	ix, loadErr := index.Load(indexPath)
	if loadErr != nil || ix.StaleAgainst(v) {
		ix = index.Build(v)
		if err := ix.Save(indexPath); err != nil {
			return nil, fmt.Errorf("stage: open engine: save index: %w", err)
		}
	}
	e.index = ix

	return e, nil
}

// Vault returns the engine's loaded vault.
func (e *Engine) Vault() *vault.Vault {
	return e.vault
}

// Index returns the engine's word index.
func (e *Engine) Index() *index.Index {
	return e.index
}

// Journal returns the engine's append-only event journal.
func (e *Engine) Journal() *Journal {
	return e.journal
}

// Close releases the lock when the Engine holds one and does nothing else.
//
// Contract (backbone §5.4, MASTER §9 D-AS): it does not close the journal —
// §5.7 gives Journal no Close method, since it holds no persistent file
// handle. Close is safe to call twice; the take below is atomic under
// lifecycleMu, so exactly one caller receives the unlock (008 A-804).
func (e *Engine) Close() error {
	unlock := e.takeCommitUnlock()
	if unlock == nil {
		return nil
	}
	return unlock()
}
