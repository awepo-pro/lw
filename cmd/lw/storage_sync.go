package main

// storage_sync.go is 066's wiring of `lw storage` into the sync verbs. git
// carries the catalog (.llmwiki/storage.json: name -> sha256 and size); the bytes
// travel through vaultsync's content-addressed object store, and internal/storage
// decides, per file, what this PC may do with them. This file only decides WHEN:
//
//	storageUpload     before EVERY git push: content before pointer, so a
//	                  catalog entry never reaches another PC ahead of its bytes
//	                  (B6). The auto-push after commit, reject and note, the
//	                  TUI pusher, lw sync and lw sync init all push git, so all
//	                  of them call it.
//	storageReconcile  after EVERY successful pull: the catalog may have dropped
//	                  files this PC holds, and the only thing that deletes them
//	                  is this call (B1: `lw storage rm` came back).
//	storageFetch      in lw sync (after the git phase) and lw sync clone: bring
//	                  in what the catalog lists and this PC lacks.
//
// 062 ran one mirror between the pull and the push of `lw sync` and returned its
// error, so a storage failure stopped the wiki's own commits (B5). Nothing here
// may do that: a storage problem is told AFTER the git push has run, in one
// line, and `lw sync` then exits 1 (storageIssues.finish); an auto-sync step
// warns once and keeps the verb's exit code (the 042 promise: auto-sync never
// makes a verb fail). The upload is retried by the next run of anything that
// pushes, and `lw sync` runs it every time, not only when it pushes (C-066-1:
// after R5 pushed git, "run lw sync again" has nothing to push).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/storage"
	"github.com/awepo-pro/lw/internal/vaultsync"
)

// storageNotArrived is how every message says a payload is not on this PC yet
// and nothing here can fix that: the PC that stored it has not uploaded it.
const storageNotArrived = "(stored on another PC; run lw sync there)"

// storageVaultRoot resolves the vault for a storage verb and insists it is one
// (066 B4/R4, the 059 noteVaultRoot pattern). findVaultRoot trusts an explicit
// --vault as given: `lw storage add --vault <typo>` then created <typo>/storage
// and <typo>/.llmwiki and exited 0, and list/show/get/rm failed with "entry not
// found" instead of the cause. It returns lw note's text and exit code, and
// creates nothing.
func storageVaultRoot(explicit string) (string, error) {
	root, err := findVaultRoot(explicit)
	if err != nil {
		return "", err
	}
	if err := requireSchema(root); err != nil {
		return "", err
	}
	return root, nil
}

// storageReason is err as one line without its "storage: " prefix, for the
// "storage: <reason> ..." lines (internal/storage errors carry the prefix
// themselves).
func storageReason(err error) string {
	return strings.TrimPrefix(oneLine(err.Error()), "storage: ")
}

// storageWarning is the line a storage failure becomes once the git phase has
// run (066 D4, R5).
func storageWarning(err error) string {
	return "storage: " + storageReason(err) + " (wiki synced; run lw sync again)"
}

// storageIssues are the storage errors one explicit sync met. They are collected
// and told only when the git phase is over, so a failed upload can never stop
// the wiki from reaching the remote (B5).
type storageIssues []error

// add records err (nil is ignored) and logs it, so an issue dropped because the
// git phase failed first is still in lw.log.
func (s *storageIssues) add(err error) {
	if err == nil {
		return
	}
	slog.Warn("storage sync", "err", err)
	*s = append(*s, err)
}

// finish prints the one storage line to w and returns the exit-1 error, or
// nil when there was nothing to tell. The command's own result is already on
// stdout; exitError keeps dispatch from adding a second "lw: sync:" line.
func (s storageIssues) finish(w io.Writer) error {
	if len(s) == 0 {
		return nil
	}
	reasons := make([]string, len(s))
	for i, err := range s {
		reasons[i] = storageReason(err)
	}
	fmt.Fprintln(w, storageWarning(errors.New(strings.Join(reasons, "; "))))
	return &exitError{code: 1}
}

// storageUpload puts into the remote's object store every payload the catalog
// lists that the remote lacks and this PC holds verified. It runs before every
// git push (B6): the push publishes the catalog, and a catalog entry whose bytes
// are nowhere but here is what 062 left on the other PC as "corrupt".
//
// What it asks first is the catalog alone, one HaveObjects round trip however
// many files there are; only files the remote lacks are read from disk. An
// entry that is not verified here (absent, stale, corrupt) is not this PC's to
// upload, and says so in doctor and `lw storage list -v`, not on every sync. A
// remote that cannot be reached or written is an error; the first such failure
// ends the loop (the next file would fail the same way, after the same
// timeout), while a file that fails for its own reason does not stop the rest.
// Objects are written once through a temp name, so a cut upload leaves nothing
// and is simply done again.
func storageUpload(ctx context.Context, root string, o vaultsync.Options) error {
	if len(o.Remotes) == 0 {
		return nil
	}
	st, err := storage.Open(root)
	if err != nil {
		return err
	}
	entries, err := st.List()
	if err != nil {
		return err
	}
	var asked []vaultsync.Object
	seen := map[string]bool{}
	for _, e := range entries {
		if !seen[e.SHA256] {
			seen[e.SHA256] = true
			asked = append(asked, vaultsync.Object{SHA256: e.SHA256, Size: e.Size})
		}
	}
	if len(asked) == 0 {
		return nil
	}
	have, _, err := vaultsync.HaveObjects(ctx, o, asked)
	if err != nil {
		var re *vaultsync.RemoteError
		if errors.As(err, &re) {
			return fmt.Errorf("remote unreachable: %s", oneLine(err.Error()))
		}
		return err
	}

	var problems []string
	uploaded := 0
	done := map[string]bool{}
	for _, e := range entries {
		if have[e.SHA256] || done[e.SHA256] {
			continue
		}
		// A name that is not already clean could point out of storage/: the
		// catalog comes from other PCs, and its names are never trusted with a path.
		if clean, err := st.CleanName(e.Name); err != nil || clean != e.Name {
			continue
		}
		path := st.Path(e.Name)
		if !storageDiskMatches(path, e) {
			continue
		}
		if err := vaultsync.PutObject(ctx, o, vaultsync.Object{SHA256: e.SHA256, Size: e.Size}, path); err != nil {
			problems = append(problems, fmt.Sprintf("upload of %s failed: %s", e.Name, oneLine(err.Error())))
			var re *vaultsync.RemoteError
			if errors.As(err, &re) || ctx.Err() != nil {
				break
			}
			continue
		}
		done[e.SHA256] = true
		uploaded++
	}
	slog.Info("storage upload", "catalog", len(asked), "remote_had", len(have), "uploaded", uploaded, "failed", len(problems))
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// storageBudget is the part of an auto-sync step's deadline the payload upload
// may use: half of what is left. The git push that follows must always have
// time to run (R5) — a 200 MB PDF on a slow link would otherwise spend the whole
// 15 s step and take the wiki's own commit down with it. A context with no
// deadline (an explicit lw sync, which waits as long as it takes) is passed on.
func storageBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	dl, ok := ctx.Deadline()
	if !ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, time.Until(dl)/2)
}

// storageReconcile brings storage/ in line with the catalog a pull just
// delivered, telling the user on stderr what it removed and what it kept. It
// touches the disk only, never the network.
func storageReconcile(root string) error {
	return storageReconcileTo(root, func(line string) { fmt.Fprintln(os.Stderr, line) })
}

// storageReconcileTo is storageReconcile with the lines handed to say, for the
// TUI's background push, which must not write to a screen it does not own. The
// lines are reported once: Reconcile drops the ledger row of every name it
// decides about, so a second call has nothing left to say.
func storageReconcileTo(root string, say func(line string)) error {
	st, err := storage.Open(root)
	if err != nil {
		return err
	}
	res, err := st.Reconcile()
	for _, name := range res.Removed {
		say("storage: removed " + name + " (removed on another PC)")
	}
	for _, name := range res.Kept {
		say("storage: kept " + name + ": changed locally since it was removed elsewhere")
	}
	if len(res.Removed)+len(res.Kept)+len(res.Adopted) > 0 {
		slog.Info("storage reconcile", "removed", len(res.Removed), "kept", len(res.Kept), "adopted", len(res.Adopted))
	}
	return err
}

// storageFetch brings in the payload of every catalog entry that is pending on
// this PC (absent, or an older version lw itself put there). An entry whose
// object is not on the remote yet is not an error: it is the normal state of a
// PC whose neighbour has not uploaded, so it is told in one line and the
// command succeeds (R6b: pending is not corrupt). A remote that cannot be
// reached, or an object that fails its sha256/size check, is an error. Corrupt
// entries are never fetched over (internal/storage refuses them).
func storageFetch(ctx context.Context, root string, o vaultsync.Options) error {
	if len(o.Remotes) == 0 {
		return nil
	}
	st, err := storage.Open(root)
	if err != nil {
		return err
	}
	rep, err := st.Status()
	if err != nil {
		return err
	}
	var problems, waiting []string
	fetched := 0
entries:
	for _, es := range rep.Entries {
		if es.State != storage.StatePending {
			continue
		}
		err := storageFetchOne(ctx, st, o, es.Entry)
		var re *vaultsync.RemoteError
		switch {
		case err == nil:
			fetched++
		case errors.Is(err, vaultsync.ErrObjectMissing):
			waiting = append(waiting, es.Entry.Name)
		default:
			problems = append(problems, fmt.Sprintf("fetch of %s failed: %s", es.Entry.Name, storageReason(err)))
			if ctx.Err() != nil || errors.As(err, &re) {
				// The remote is the problem: the next file would fail the same way.
				break entries
			}
		}
	}
	if len(waiting) > 0 {
		fmt.Fprintln(os.Stderr, storageWaitingLine(waiting))
	}
	slog.Info("storage fetch", "fetched", fetched, "waiting", len(waiting), "failed", len(problems))
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// storageWaitingLine is the one stderr line for the entries whose payload is not
// on the remote yet.
func storageWaitingLine(names []string) string {
	if len(names) == 1 {
		return "storage: " + names[0] + " has not arrived yet " + storageNotArrived
	}
	shown, more := names, ""
	if len(names) > 5 {
		shown, more = names[:5], fmt.Sprintf(", and %d more", len(names)-5)
	}
	return fmt.Sprintf("storage: %d files have not arrived yet %s: %s%s", len(names), storageNotArrived, strings.Join(shown, ", "), more)
}

// storageFetchOne streams entry e's object from the remote into storage/<name>.
// The bytes go straight from GetObject into Materialize through a pipe, which
// verifies sha256 and size against the catalog before anything is renamed into
// place: a wrong or cut object leaves no file, no ledger row and no temp file.
//
// Two goroutines, two verdicts. When GetObject ends first (a missing object, an
// unreachable remote) its error is the cause and Materialize's "read stream"
// error only the echo; when Materialize ends first (a checksum it refused) the
// pipe is closed under GetObject, whose error is then the echo.
func storageFetchOne(ctx context.Context, st *storage.Storage, o vaultsync.Options, e storage.Entry) error {
	pr, pw := io.Pipe()
	done := make(chan struct{})
	var getErr error
	go func() {
		getErr = vaultsync.GetObject(ctx, o, vaultsync.Object{SHA256: e.SHA256, Size: e.Size}, pw)
		pw.CloseWithError(getErr) // nil closes with EOF
		close(done)
	}()
	matErr := st.Materialize(e.Name, pr)
	select {
	case <-done:
		if getErr != nil {
			return getErr
		}
		return matErr
	default:
	}
	pr.Close() // Materialize stopped reading: release the writer
	<-done
	return matErr
}

// storageHashFile returns the sha256 and the length of the file at path.
func storageHashFile(path string) (sha string, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// storageDiskMatches reports whether the file at path is exactly entry e:
// a regular file of e's size whose sha256 is e's. It is internal/storage's
// "verified" for one entry, without the ledger and without reading every other
// file the way Status does.
func storageDiskMatches(path string, e storage.Entry) bool {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() != e.Size {
		return false
	}
	sha, size, err := storageHashFile(path)
	return err == nil && size == e.Size && sha == e.SHA256
}

// storageFetchOptions is the vaultsync view of this vault for fetching one
// payload on demand (`lw storage get`): the configured remotes, non-interactive
// and bounded like auto-sync's, and only for a vault that is under lw sync —
// a PC whose config names remotes for another vault must not fetch a stranger's
// objects into this one. ok is false when there is nothing to fetch from.
func storageFetchOptions(root string) (o vaultsync.Options, ok bool) {
	cfg, err := config.Load()
	if err != nil {
		slog.Warn("storage: config not loaded; no on-demand fetch", "err", err)
		return o, false
	}
	remotes := cfg.Sync.RemoteList()
	if len(remotes) == 0 || !underLWSync(root) {
		return o, false
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return o, false
	}
	return vaultsync.Options{Dir: abs, Remotes: orderedRemotes(abs, remotes)}, true
}

// storageEnsureLocal makes sure entry e's bytes are on this PC and exactly the
// catalog's before `lw storage get` serves them (066 R6). A verified file is
// served as it is. A pending one is fetched on demand — the user asked for it,
// so waiting for a `lw sync` they may not think of is the 062 dead end (an ENOENT).
// A corrupt one is never served and never overwritten.
func storageEnsureLocal(st *storage.Storage, root string, e storage.Entry) error {
	path := st.Path(e.Name)
	if storageDiskMatches(path, e) {
		return nil
	}
	rep, err := st.Status()
	if err != nil {
		return err
	}
	var es *storage.EntryStatus
	for i := range rep.Entries {
		if rep.Entries[i].Entry.Name == e.Name {
			es = &rep.Entries[i]
		}
	}
	switch {
	case es == nil:
		return fmt.Errorf("storage: entry %q not found", e.Name)
	case es.State == storage.StateVerified:
		return nil // fixed between the two reads
	case es.State == storage.StateCorrupt:
		got := "unreadable"
		if sha, _, err := storageHashFile(path); err == nil {
			got = sha[:12]
		}
		return fmt.Errorf("%s is corrupt on disk (sha256 %s ≠ catalog %s)", e.Name, got, e.SHA256[:12])
	}

	o, ok := storageFetchOptions(root)
	if !ok {
		return fmt.Errorf("%s is not on this PC and no [sync] remote is configured to fetch it from", e.Name)
	}
	ctx, stop := syncContext()
	defer stop()
	err = storageFetchOne(ctx, st, o, e)
	var re *vaultsync.RemoteError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, vaultsync.ErrObjectMissing):
		return fmt.Errorf("%s has not arrived yet %s", e.Name, storageNotArrived)
	case errors.As(err, &re):
		return fmt.Errorf("%s has not arrived yet: remote unreachable: %s", e.Name, storageReason(err))
	}
	return err
}
