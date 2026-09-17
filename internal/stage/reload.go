// reload.go implements Engine.ReloadIfChanged (008 contract §3): the seam a
// long-lived TUI process uses to notice a commit made by ANOTHER process —
// something this package's verb-per-process CLI never had to do. The engine
// records the journal's (size, mtime) whenever it touches the journal
// itself — at OpenEngine and after every append it makes — and
// ReloadIfChanged compares that stamp against the file on disk, so its own
// writes never look foreign.
package stage

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// journalStamp is the journal's on-disk fingerprint: its byte size and the
// mtime of its last write. Both are compared, because size alone misses a
// same-length rewrite and mtime alone misses an append that lands within
// one filesystem timestamp tick.
type journalStamp struct {
	size  int64
	mtime time.Time
}

// statJournalStamp stats path into a journalStamp.
func statJournalStamp(path string) (journalStamp, error) {
	info, err := os.Stat(path)
	if err != nil {
		return journalStamp{}, err
	}
	return journalStamp{size: info.Size(), mtime: info.ModTime()}, nil
}

// stampJournal refreshes this Engine's journal stamp from disk. Best effort:
// a stat that fails leaves the old stamp standing, and journalChanged
// surfaces the same error to its caller on the next check — a journal that
// cannot be stated is a broken vault this package should not paper over
// with a fresh stamp.
func (e *Engine) stampJournal() {
	stamp, err := statJournalStamp(e.journal.path)
	if err != nil {
		return
	}
	e.journalStampMu.Lock()
	e.journalStamp = stamp
	e.journalStampMu.Unlock()
}

// appendJournal appends ev through the journal and refreshes this Engine's
// stamp, so an append this Engine made never reads as a foreign change to
// ReloadIfChanged. Every journal append the Engine itself makes goes
// through here (008 contract §3); a writer that bypasses this Engine —
// another lw process, or a test seeding history — is exactly the foreign
// write ReloadIfChanged exists to notice.
//
// The append and the stamp refresh hold journalStampMu across BOTH, the
// same lock journalChanged stats and compares under: released one lock
// later, a concurrent ReloadIfChanged could stat the journal after this
// append's bytes landed but before the stamp caught up, take the reload
// path mid-turn, and write e.open under a running Append — the exact race
// reload_test's concurrent subtest exists to catch.
func (e *Engine) appendJournal(ev Event) error {
	e.journalStampMu.Lock()
	defer e.journalStampMu.Unlock()
	if err := e.journal.Append(ev); err != nil {
		return err
	}
	if stamp, err := statJournalStamp(e.journal.path); err == nil {
		e.journalStamp = stamp
	}
	return nil
}

// journalChanged reports whether the journal on disk no longer matches the
// stamp this Engine last recorded. The stat and the comparison share one
// critical section with stampJournal, so an append through THIS Engine can
// never land between them and be mistaken for a foreign write — which is
// what makes ReloadIfChanged -race clean against a concurrent turn appending
// in the same process.
func (e *Engine) journalChanged() (prev, cur journalStamp, changed bool, err error) {
	e.journalStampMu.Lock()
	defer e.journalStampMu.Unlock()
	cur, err = statJournalStamp(e.journal.path)
	if err != nil {
		return journalStamp{}, journalStamp{}, false, fmt.Errorf("stage: reload: %w", err)
	}
	prev = e.journalStamp
	return prev, cur, cur != prev, nil
}

// ReloadIfChanged re-reads the vault and the index from disk when
// .llmwiki/journal.ndjson changed since this Engine last saw it (size or
// mtime), which is how a TUI notices a commit made by another process.
// It returns true when it reloaded. Journal appends made through THIS
// Engine never count as a change. It clears the cached open changeset so
// the next Current() re-reads it. It is a no-op returning (false, nil)
// while this Engine holds the commit lock.
//
// The reload mutates the vault and the index in place — the *index.Index
// handed out before the reload is the same object after it and answers
// for the reloaded vault (008 A-801) — and it is deliberately
// unsynchronized: callers must not run it while another goroutine is using this Engine.
func (e *Engine) ReloadIfChanged() (bool, error) {
	if e.unlock != nil {
		return false, nil
	}
	prev, cur, changed, err := e.journalChanged()
	if err != nil || !changed {
		return false, err
	}

	if err := e.vault.Reload(); err != nil {
		return false, fmt.Errorf("stage: reload: %w", err)
	}
	// The index is a disposable cache (engine.go's OpenEngine contract),
	// but it is rebuilt IN PLACE (008 A-801): the agent's tool registry
	// captured e.index at construction, so a swap would leave it searching
	// a stale index for the life of the process. This mirrors Commit's
	// step 7 — update over the just-reloaded vault. The save is best-effort
	// for the NEXT process only: a failed save returns an error and leaves
	// the stamp standing, so the next call reloads again and retries it.
	e.index.Rebuild(e.vault)
	if err := e.index.Save(filepath.Join(e.llmwikiDir(), "index.gob")); err != nil {
		return false, fmt.Errorf("stage: reload: save index: %w", err)
	}
	e.open = nil
	// Stamp with what this reload actually read (cur, observed before it
	// began) — never a fresh stat. An append landing mid-reload must stay
	// visible to the next call, or the stamp would claim a state the vault
	// above was never reloaded to. The guard leaves an own appendJournal
	// that fired mid-reload in charge: its stamp is newer and already
	// accurate.
	e.journalStampMu.Lock()
	if e.journalStamp == prev {
		e.journalStamp = cur
	}
	e.journalStampMu.Unlock()
	return true, nil
}
