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
	unlock     func() error            // nil unless Commit holds the lock
	now        func() time.Time        // injected (00-conventions.md §3)
	rand       io.Reader               // injected; id entropy
	faultAfter func(step string) error // test-only; nil in production
	// invalidateOpen is a test-only hook, nil in production: Commit runs it
	// just before reading the cached open changeset, so a test can park a
	// forgetOpen in the Refresh→cachedOpen window deterministically
	// (008 F-R2) instead of racing a real one into it.
	invalidateOpen func()
	forceNext      bool // next Commit overrode a lint regression (D-AG)
	// openMu guards open and nextOp (008 A-802): the reload tick's
	// ReloadIfChanged, an agent turn's Current/Append and a review load all
	// touch those two fields from different goroutines, so every read and
	// write of either goes through the cacheOpen*/forgetOpen/cachedOpen/
	// cachedOpenCopy/stageAppendOp/unstageAppendOp/assignIDs helpers below.
	// It is a short-held lock: never across vault, index, CAS or journal
	// I/O — Append stages the op into the cache under it, then recomputes
	// and persists with it released (008 D-8H). It is never held together
	// with journalStampMu — no code path takes both, so there is no lock
	// order between them to document.
	openMu sync.Mutex
	open   *Changeset // nil when none is open; guarded by openMu
	nextOp int        // op<N> counter, incl. cascade sub-ops; guarded by openMu
	// journalStampMu guards journalStamp: every journal append this Engine
	// makes refreshes the stamp, and ReloadIfChanged (reload.go, 008
	// contract §3) stats the journal and compares it under the same lock,
	// so an own append can never land between the stat and the comparison
	// and be mistaken for a foreign write.
	journalStampMu sync.Mutex
	journalStamp   journalStamp
}

// cacheOpen stores c as the engine's cached open changeset, leaving the op
// counter alone — the shape Append and the mutation tails need, where the
// changeset object is extended in place and only the cache pointer moves.
func (e *Engine) cacheOpen(c *Changeset) {
	e.openMu.Lock()
	e.open = c
	e.openMu.Unlock()
}

// cacheOpenAt stores c as the cached open changeset together with the op
// counter rehydrated from it — the shape OpenChangeset and Current need.
func (e *Engine) cacheOpenAt(c *Changeset, nextOp int) {
	e.openMu.Lock()
	e.open = c
	e.nextOp = nextOp
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
// is taken under openMu so it can never catch an Append's id draw and op
// insert half-done — that pair is one critical section (stageAppendOp).
func (e *Engine) cachedOpenCopy() *Changeset {
	e.openMu.Lock()
	defer e.openMu.Unlock()
	return e.open.clone()
}

// cacheOpenRehydrated stores c — a changeset just read back from disk — as
// the cached open changeset and LIFTS the op counter to nextOp, never
// lowering it. Ownership of c transfers to the cache: callers hand over
// their own deep copy (rehydrateOpen passes c.clone()) and keep their
// object unshared. This is the D-8H amendment to rehydration: an Append
// whose id is drawn but not yet persisted may be in flight while Current
// reads disk, and disk cannot see that op, so setting nextOp = 1+maxOpN(disk)
// flat could rewind the counter under the drawn id and hand the next
// Append a duplicate. Raising it instead keeps the R-804 rule without
// exception: gaps are harmless and self-heal, duplicates are not.
func (e *Engine) cacheOpenRehydrated(c *Changeset, nextOp int) {
	e.openMu.Lock()
	e.open = c
	if nextOp > e.nextOp {
		e.nextOp = nextOp
	}
	e.openMu.Unlock()
}

// stageAppendOp is Append's cache critical section (008 D-8H). Under ONE
// openMu hold it draws op's op<N> id — its cascade sub-ops included — and
// inserts the numbered op into the engine's CACHED changeset, so a
// concurrent Current can neither rewind the counter under the drawn id nor
// swap the cache to a disk copy that lacks this op. It returns the cached
// changeset the op landed in: the object Append's whole persist tail —
// checks recompute, changeset.json write, journal — must then use, so the
// cache and what lands on disk stay the same thing.
//
// A cache cleared mid-Append (a concurrent forgetOpen) is re-seeded with c:
// the changeset is unmodified on disk and nextOp already counts past every
// op c carries. A cache holding a rehydrated copy of the SAME changeset is
// adopted as the target — D-8H stages into whatever the cache holds. Only
// a DIFFERENT changeset id is refused, before any id is drawn: grafting the
// op onto another changeset, or resurrecting c over it, would corrupt one
// of them.
//
// The caller must have finished every step that reads or writes op before
// calling this — validation, content storage, SourceSHA capture — since
// numbering and insertion happen inside the lock.
func (e *Engine) stageAppendOp(c *Changeset, op *Op) (*Changeset, error) {
	e.openMu.Lock()
	defer e.openMu.Unlock()
	target := e.open
	switch {
	case target == nil:
		target = c
		e.open = c
	case target != c && target.ID != c.ID:
		return nil, fmt.Errorf("stage: append: the open changeset changed while the op was being prepared: %s is open, not %s",
			target.ID, c.ID)
	}
	e.assignIDs(op)
	target.Ops = append(target.Ops, *op)
	return target, nil
}

// unstageAppendOp is stageAppendOp's inverse for a failed persist tail
// (008 D-8H): the cache must equal what will be on disk, and a tail that
// failed before the changeset.json write leaves the op unpersisted — so the
// cache drops it again, under openMu, and checks is restored with it. Only
// a cache that still holds the changeset the op was staged into is
// corrected: a cache that has moved on is not this Append's to rewrite, and
// the op was never on disk for it to miss.
func (e *Engine) unstageAppendOp(c *Changeset, opID string, checks Checks) {
	e.openMu.Lock()
	defer e.openMu.Unlock()
	if e.open != c {
		return
	}
	for i := range c.Ops {
		if c.Ops[i].ID == opID {
			c.Ops = append(c.Ops[:i], c.Ops[i+1:]...)
			break
		}
	}
	c.Checks = checks
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
func (e *Engine) ForceNextCommit() { e.forceNext = true }

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
// handle. Close is safe to call twice.
func (e *Engine) Close() error {
	if e.unlock == nil {
		return nil
	}
	unlock := e.unlock
	e.unlock = nil
	return unlock()
}
