package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
const ledgerVersion = 1

// ledger is the on-disk form: {"version":1,"materialized":{"<name>":"<sha256>"}}.
// encoding/json writes map keys sorted, so the file is deterministic.
type ledger struct {
	Version      int               `json:"version"`
	Materialized map[string]string `json:"materialized"`
}

func (s *Storage) ledgerPath() string {
	return filepath.Join(s.vaultDir, ".llmwiki", "tmp", "storage-local.json")
}

// loadLedger reads the ledger; a missing file is an empty one (a vault from
// v2.49.0, which had none). A file that exists but cannot be understood is an
// error rather than an empty ledger: silently starting over would turn every
// stale file into "corrupt" and hide why. It is per-PC derived state, so the
// message says what to do.
func (s *Storage) loadLedger() (*ledger, error) {
	data, err := os.ReadFile(s.ledgerPath())
	if errors.Is(err, os.ErrNotExist) {
		return &ledger{Version: ledgerVersion, Materialized: map[string]string{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("storage: read ledger: %w", err)
	}
	var l ledger
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("storage: ledger %s is unreadable (%v): delete it and run lw sync again", s.ledgerPath(), err)
	}
	if l.Version == 0 {
		l.Version = ledgerVersion
	}
	if l.Version != ledgerVersion {
		return nil, fmt.Errorf("storage: ledger %s is version %d; this lw reads version %d — upgrade lw, or delete the file", s.ledgerPath(), l.Version, ledgerVersion)
	}
	if l.Materialized == nil {
		l.Materialized = map[string]string{}
	}
	return &l, nil
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

// record sets the ledger row for name and saves it. Callers hold s.mu and call
// it only after the file (and, for Add, the catalog) is in place: a crash
// before the row is written leaves a verified file with no row, which Reconcile
// adopts; the other order would leave a row for a file the catalog may not list
// yet, and the next Reconcile would delete a file the user just stored.
func (s *Storage) record(l *ledger, name, sha string) error {
	l.Materialized[name] = sha
	return s.saveLedger(l)
}
