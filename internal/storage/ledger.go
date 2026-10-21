package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The ledger is this PC's record of the bytes lw itself put at storage/<name>
// (066 D1, plan M4). The catalog (.llmwiki/storage.json) says what every PC
// should have; the ledger says what THIS PC was given, and that is what lets a
// pull tell three cases apart that a directory listing cannot:
//
//   - a file the catalog dropped and lw put there: deleted (B1, the rm that came back);
//   - a file the catalog dropped and somebody edited since: kept, reported;
//   - a file the catalog replaced elsewhere and nothing here touched: stale, to be re-fetched (B2).
//
// It lives in .llmwiki/tmp/, which vaultsync.Ignore keeps out of git: another
// PC's ledger would describe another PC's disk. The catalog stays schema
// version 1; the ledger has its own version.
//
// FIX1 (rule P) adds what the three cases above were missing: whether a copy of
// the bytes exists anywhere else. "lw put these bytes here and they are unchanged"
// proves lw wrote them, not that they can be had again, and deleting or
// overwriting the only copy was the data loss of B7 and F-1. So the ledger also
// remembers, per sha256:
//
//   - confirmed: the remote's object store held the object when it was last asked.
//     Set when an object was fetched from it, or seen or put there by an upload.
//     Monotonic, because lw never deletes objects from the remote. It is what lets
//     an upload skip asking again (F-3); it is NOT proof enough to destroy local
//     bytes: a store can lose an object lw never touched (restored from a git-only
//     backup, wiped at the same URL), so every destroy re-proves first (Y-3). It is
//     a statement about ONE remote (scope), so it starts over when the remote
//     changes - a vault moved to a new remote has not uploaded to it yet, and
//     believing the old one would skip the upload.
//   - told: a "kept, no copy yet" line was already shown for name@sha, so a retry
//     on every verb does not repeat it.
//   - damaged: a fetch of that object failed verification (remote object damaged).
const ledgerVersion = 1

// ledger is the on-disk form. encoding/json writes map keys sorted, so the file
// is deterministic. An older lw reads the same file and ignores the keys it does
// not know; one that rewrites it drops them, which costs a network question.
type ledger struct {
	Version      int               `json:"version"`
	Materialized map[string]string `json:"materialized"`
	Scope        string            `json:"scope,omitempty"`
	Confirmed    map[string]bool   `json:"confirmed,omitempty"`
	Told         map[string]bool   `json:"told,omitempty"`
	Damaged      map[string]bool   `json:"damaged,omitempty"`

	// scopeReset says loadLedger found the file written for another remote (or for
	// none) and handed back empty sets; saving it makes that official.
	scopeReset bool
}

func (s *Storage) ledgerPath() string {
	return filepath.Join(s.vaultDir, ".llmwiki", "tmp", "storage-local.json")
}

// BindRemote says which remote object store the confirmations of this handle
// refer to; cmd/lw calls it with an identity of the configured remotes before any
// call that reads or writes them. Ledger data recorded for another remote is
// invisible to this handle and replaced the first time it writes. A handle that
// never binds (the verbs that do not touch a remote) sees and keeps whatever the
// ledger holds.
func (s *Storage) BindRemote(id string) { s.remote = id }

// loadLedger reads the ledger; a missing file is an empty one (a vault from
// v2.49.0, which had none). A file that exists but cannot be understood is an
// error rather than an empty ledger: silently starting over would turn every
// stale file into "corrupt" and hide why. It is per-PC derived state, so the
// message says what to do.
func (s *Storage) loadLedger() (*ledger, error) {
	l := &ledger{Version: ledgerVersion}
	data, err := os.ReadFile(s.ledgerPath())
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("storage: read ledger %s: %w", s.ledgerPath(), err)
	default:
		if err := json.Unmarshal(data, l); err != nil {
			return nil, fmt.Errorf("storage: ledger %s is unreadable (%v): delete it and run lw sync again", s.ledgerPath(), err)
		}
		if l.Version == 0 {
			l.Version = ledgerVersion
		}
		if l.Version != ledgerVersion {
			return nil, fmt.Errorf("storage: ledger %s is version %d; this lw reads version %d — upgrade lw, or delete the file", s.ledgerPath(), l.Version, ledgerVersion)
		}
	}
	if l.Materialized == nil {
		l.Materialized = map[string]string{}
	}
	if l.Confirmed == nil {
		l.Confirmed = map[string]bool{}
	}
	if l.Told == nil {
		l.Told = map[string]bool{}
	}
	if l.Damaged == nil {
		l.Damaged = map[string]bool{}
	}
	if s.remote != "" && l.Scope != s.remote {
		// A ledger file that did not exist has nothing to reset; one that did is
		// rewritten the next time anything is saved.
		l.scopeReset = !errors.Is(err, os.ErrNotExist)
		l.Scope = s.remote
		l.Confirmed, l.Told, l.Damaged = map[string]bool{}, map[string]bool{}, map[string]bool{}
	}
	return l, nil
}

// saveLedger writes the ledger the way saveManifest writes the catalog: a temp
// file in the same directory, fsync, rename. The ledger decides what lw may
// delete, so a reader must never see a half-written one; a crash leaves the old
// ledger, or the new one, and at worst a hidden temp file nobody reads.
func (s *Storage) saveLedger(l *ledger) error {
	l.Version = ledgerVersion
	dir := filepath.Dir(s.ledgerPath())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("storage: create ledger directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".storage-local-tmp-*")
	if err != nil {
		return fmt.Errorf("storage: create temp ledger: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()

	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("storage: marshal ledger: %w", err)
	}
	data = append(data, '\n')
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("storage: write temp ledger: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("storage: sync temp ledger: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("storage: close temp ledger: %w", err)
	}
	if err := os.Rename(tmpName, s.ledgerPath()); err != nil {
		return fmt.Errorf("storage: rename ledger: %w", err)
	}
	tmpName = ""
	return nil
}

// record sets the ledger row for name and saves it. Callers hold the lock and
// call it only after the file (and, for Add, the catalog) is in place: a crash
// before the row is written leaves a verified file with no row, which Reconcile
// adopts; the other order would leave a row for a file the catalog may not list
// yet, and the next Reconcile would delete a file the user just stored.
func (s *Storage) record(l *ledger, name, sha string) error {
	l.Materialized[name] = sha
	return s.saveLedger(l)
}

// Confirm records that the remote's object store holds the objects with these
// sha256 (rule P): they were seen there by HaveObjects or put there by an
// upload, so the next upload need not ask about them. An object that was marked
// damaged is no longer: the caller has just seen or replaced it. Values that are
// not a sha256 are ignored.
func (s *Storage) Confirm(shas ...string) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	l, err := s.loadLedger()
	if err != nil {
		return err
	}
	changed := false
	for _, sha := range shas {
		if !ValidSHA256(sha) {
			continue
		}
		if !l.Confirmed[sha] {
			l.Confirmed[sha] = true
			changed = true
		}
		if l.Damaged[sha] {
			delete(l.Damaged, sha)
			changed = true
		}
	}
	// A ledger written for another remote is replaced even when nothing is added:
	// from here on it speaks for this one.
	if changed || l.scopeReset {
		return s.saveLedger(l)
	}
	return nil
}

// MarkDamaged records that a fetch of the object with this sha256 failed
// verification, so doctor and `lw storage list -v` can say the remote's copy is
// damaged and not merely late. Confirm (an upload that replaced it) clears it.
func (s *Storage) MarkDamaged(sha string) error {
	if !ValidSHA256(sha) {
		return nil
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	l, err := s.loadLedger()
	if err != nil {
		return err
	}
	if l.Damaged[sha] {
		return nil
	}
	l.Damaged[sha] = true
	return s.saveLedger(l)
}

// toldKey is the throttle key of one "kept, no copy yet" line.
func toldKey(name, sha string) string { return name + "@" + sha }

// pruneTold drops the throttle entries whose situation is over (the row is gone
// or holds other bytes), so the set cannot grow and a later, new situation for
// the same name speaks again. It reports whether it changed anything.
func (l *ledger) pruneTold() bool {
	changed := false
	for key := range l.Told {
		i := strings.LastIndex(key, "@")
		if i < 0 || l.Materialized[key[:i]] != key[i+1:] {
			delete(l.Told, key)
			changed = true
		}
	}
	return changed
}
