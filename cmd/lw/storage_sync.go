package main

// storage_sync.go is 066's wiring of `lw storage` into the sync verbs. git carries
// the catalog (.llmwiki/storage.json: name -> sha256 and size); the bytes travel
// through vaultsync's content-addressed object store, and internal/storage
// decides, per file, what this PC may do with them. This file only decides WHEN:
//
//	storageUpload     before EVERY git push, and (FIX1 X-2) before the pull as
//	                  well: content before pointer, so a catalog entry never
//	                  reaches another PC ahead of its bytes (B6), and a pull that
//	                  is refused or reshaped (diverged, dirty, a rebase) still
//	                  leaves this PC's payloads where another PC can fetch them.
//	storageReconcile  after EVERY successful pull: the catalog may have dropped
//	                  files this PC holds, and the only thing that deletes them
//	                  is this call (B1: `lw storage rm` came back).
//	storageFetch      in lw sync (after the git phase) and lw sync clone: bring
//	                  in what the catalog lists and this PC lacks.
//
// 062 ran one mirror between the pull and the push of `lw sync` and returned its
// error, so a storage failure stopped the wiki's own commits (B5). Nothing here
// may do that: a storage problem is told AFTER the git push has run, and `lw sync`
// then exits 1 (storageIssues.finish); an auto-sync step warns once and keeps the
// verb's exit code (the 042 promise: auto-sync never makes a verb fail). The
// upload is retried by the next run of anything that pushes, and `lw sync` runs it
// every time, not only when it pushes (C-066-1).
//
// Rule P (066 FIX1, plan R13): lw never deletes or overwrites bytes it put in
// storage/ unless a copy of them is known to be on the remote. That is
// internal/storage's rule; what this file contributes is the way to prove it
// (storageEnsure: one HaveObjects for the batch, an upload for what is missing)
// and the scope of the proof (storageScope: a confirmation is about one remote).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/storage"
	"github.com/awepo-pro/lw/internal/vaultsync"
)

// storageNotArrived is how every message says a payload is not on this PC yet
// and nothing here can fix that: the PC that stored it has not uploaded it.
const storageNotArrived = "(stored on another PC; run lw sync there)"

// storageAutoMaxObject is the largest payload an auto-sync push uploads (X-6, F-3).
// The step has 15 s for the whole of pull, upload and push; a payload that does not
// fit would be re-sent and cut at every push and never arrive. Larger ones wait
// for an explicit lw sync, which has no deadline, and the push says so. A variable
// so a test needs no 33 MiB file.
var storageAutoMaxObject int64 = 32 << 20

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

// shortSHA is the first 12 characters of a sha256 for a message, or all of s when
// it is shorter: a catalog record can hold anything (X-8: a slice [:12] of a short
// sha256 was a panic).
func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// localError marks a storage failure as this PC's own: its catalog, its ledger,
// its disk. Running lw sync again reads the same bytes, so such a failure is not
// told with the advice that fits the remote (X-12).
type localError struct{ err error }

func (e *localError) Error() string { return e.err.Error() }
func (e *localError) Unwrap() error { return e.err }

// localErr wraps err as a local failure (nil stays nil).
func localErr(err error) error {
	if err == nil {
		return nil
	}
	return &localError{err}
}

// storageErr is what one storage step returns when it met problems of both
// kinds: the remote's (a rerun may fix them) and this PC's (it will not). Each is
// one reason, the per-file reasons of the step joined.
type storageErr struct{ remote, local string }

func (e *storageErr) Error() string {
	var parts []string
	for _, p := range []string{e.remote, e.local} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "; ")
}

// stepErr is the error of a step that collected remote and local problems, nil
// when there were none.
func stepErr(remote, local []string) error {
	if len(remote)+len(local) == 0 {
		return nil
	}
	return &storageErr{remote: strings.Join(remote, "; "), local: strings.Join(local, "; ")}
}

// storageReasons splits err into the reason a rerun may fix and the reason it
// will not. A plain error is a remote one: that is what the network steps return.
func storageReasons(err error) (remote, local string) {
	var se *storageErr
	var le *localError
	switch {
	case errors.As(err, &se):
		return se.remote, se.local
	case errors.As(err, &le):
		return "", storageReason(le)
	}
	return storageReason(err), ""
}

// storageWarningLines are the lines a storage failure becomes once the git phase
// has run (066 D4, R5), in the order the reasons were met and each reason once:
//
//	storage: <reason> (wiki synced; run lw sync again)    the remote's; a rerun can fix it
//	storage: <reason> (wiki synced)                       this PC's own; the reason names the file
func storageWarningLines(errs ...error) []string {
	var lines []string
	seen := map[string]bool{}
	add := func(reason, suffix string) {
		if reason == "" || seen[suffix+reason] {
			return
		}
		seen[suffix+reason] = true
		lines = append(lines, "storage: "+reason+" ("+suffix+")")
	}
	for _, err := range errs {
		if err == nil {
			continue
		}
		remote, local := storageReasons(err)
		add(remote, "wiki synced; run lw sync again")
		add(local, "wiki synced")
	}
	return lines
}

// storageWarning is storageWarningLines for one error, as the one block of text
// the TUI's report prints.
func storageWarning(err error) string {
	return strings.Join(storageWarningLines(err), "\n")
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

// finish prints the storage lines to w and returns the exit-1 error, or nil when
// there was nothing to tell. The command's own result is already on stdout;
// exitError keeps dispatch from adding a second "lw: sync:" line.
func (s storageIssues) finish(w io.Writer) error {
	if len(s) == 0 {
		return nil
	}
	for _, line := range storageWarningLines(s...) {
		fmt.Fprintln(w, line)
	}
	return &exitError{code: 1}
}

// storageScope names the remote object store this PC's confirmations are about:
// the set of configured remotes. They are names for one server (the order is the
// user's, and which answers first varies), so the set, not the order, is the
// identity. A different set is a different store until proven otherwise, and the
// proof is one HaveObjects away (BindRemote, 066 FIX1).
func storageScope(o vaultsync.Options) string {
	remotes := append([]string(nil), o.Remotes...)
	sort.Strings(remotes)
	return strings.Join(remotes, "\n")
}

// storageUsable reports whether any configured remote can hold storage objects
// (an ssh or local path remote). https:// and git:// carry git and nothing else
// (X-7, F-4).
func storageUsable(o vaultsync.Options) bool {
	for _, r := range o.Remotes {
		if vaultsync.HoldsObjects(r) {
			return true
		}
	}
	return false
}

// storageUnsupportedWarning is the one line a run says when [sync] remotes are
// configured, the catalog has stored files, and none of the remotes can hold
// them; "" otherwise. It is a fact about the configuration, not a failure: git
// syncs and the payloads stay on the PCs that stored them (X-7).
func storageUnsupportedWarning(root string, o vaultsync.Options) string {
	if len(o.Remotes) == 0 || storageUsable(o) {
		return ""
	}
	st, err := storage.Open(root)
	if err != nil {
		return ""
	}
	if entries, err := st.List(); err != nil || len(entries) == 0 {
		return ""
	}
	return "storage: " + strings.Join(o.Remotes, ", ") + " cannot hold storage files (needs an ssh or local path remote)"
}

// openBound opens root's storage bound to the remote object store of o, so its
// confirmations are the ones recorded for that store.
func openBound(root string, o vaultsync.Options) (*storage.Storage, error) {
	st, err := storage.Open(root)
	if err != nil {
		return nil, localErr(err)
	}
	st.BindRemote(storageScope(o))
	return st, nil
}

// errNoObjectRemote is why a copy cannot be shown when no remote can hold one.
var errNoObjectRemote = errors.New("none of the configured remotes can hold storage files (they need an ssh or local path remote)")

// storageTransient reports whether err is the kind the same call can get past
// later: the remote could not be reached or written, or the step ran out of time.
func storageTransient(ctx context.Context, err error) bool {
	var re *vaultsync.RemoteError
	return errors.As(err, &re) || ctx.Err() != nil
}

// storageSecure makes sure the remote holds each blob and answers per sha256: ONE
// HaveObjects question for the batch, then an upload for each blob the remote
// lacks. It is the single place payloads are put on the remote, shared by the
// upload that runs before a push and by the proof rule P asks for before
// anything is destroyed. A failure of the question itself is returned too, as
// batch, for the one line it deserves; the per-blob answers carry it as well.
//
// The first upload that fails because of the remote ends the loop - the next blob
// would fail the same way after the same timeout - and the blobs not reached share
// its answer. A blob that fails for its own reason (its source is not the bytes
// the catalog names) does not stop the others.
func storageSecure(ctx context.Context, o vaultsync.Options, blobs []storage.Blob) (answers map[string]storage.EnsureResult, batch error) {
	answers = make(map[string]storage.EnsureResult, len(blobs))
	if len(blobs) == 0 {
		return answers, nil
	}
	failAll := func(err error) (map[string]storage.EnsureResult, error) {
		r := storage.EnsureResult{Err: err, Transient: storageTransient(ctx, err)}
		for _, b := range blobs {
			answers[b.SHA256] = r
		}
		return answers, err
	}
	if !storageUsable(o) {
		return failAll(errNoObjectRemote)
	}
	var asked []vaultsync.Object
	seen := map[string]bool{}
	for _, b := range blobs {
		if !seen[b.SHA256] {
			seen[b.SHA256] = true
			asked = append(asked, vaultsync.Object{SHA256: b.SHA256, Size: b.Size})
		}
	}
	have, _, err := vaultsync.HaveObjects(ctx, o, asked)
	if err != nil {
		transient := storageTransient(ctx, err) // judged before the error is reworded
		var re *vaultsync.RemoteError
		if errors.As(err, &re) {
			err = fmt.Errorf("remote unreachable: %s", oneLine(err.Error()))
		}
		r := storage.EnsureResult{Err: err, Transient: transient}
		for _, b := range blobs {
			answers[b.SHA256] = r
		}
		return answers, err
	}
	for i, b := range blobs {
		if _, done := answers[b.SHA256]; done {
			continue
		}
		if have[b.SHA256] {
			answers[b.SHA256] = storage.EnsureResult{Held: true}
			continue
		}
		err := vaultsync.PutObject(ctx, o, vaultsync.Object{SHA256: b.SHA256, Size: b.Size}, b.Path)
		if err == nil {
			answers[b.SHA256] = storage.EnsureResult{Held: true, Uploaded: true}
			continue
		}
		r := storage.EnsureResult{Err: err, Transient: storageTransient(ctx, err)}
		answers[b.SHA256] = r
		if r.Transient {
			for _, rest := range blobs[i+1:] {
				if _, done := answers[rest.SHA256]; !done {
					answers[rest.SHA256] = r
				}
			}
			break
		}
	}
	return answers, nil
}

// storageEnsure is storage.Ensure for the remote object store of o.
func storageEnsure(o vaultsync.Options) storage.Ensure {
	return func(ctx context.Context, blobs []storage.Blob) map[string]storage.EnsureResult {
		answers, _ := storageSecure(ctx, o, blobs)
		return answers
	}
}

// onceSay returns a say function that writes each distinct line to w once: an
// upload that runs twice in one command (before the pull and again before the
// push) must not tell the user the same thing twice.
func onceSay(w io.Writer) func(line string) {
	seen := map[string]bool{}
	return func(line string) {
		if seen[line] {
			return
		}
		seen[line] = true
		fmt.Fprintln(w, line)
	}
}

// storageUpload puts into the remote's object store every payload the catalog
// lists that the remote lacks and this PC holds verified. It runs before every
// git push (B6) and, since FIX1, before the pull: the push publishes the
// catalog, and a catalog entry whose bytes are nowhere but here is what 062 left
// on the other PC as "corrupt".
//
// What it asks first is the catalog and the ledger alone: an entry whose object
// is already confirmed is not asked about, so a sync of files that have all
// been up once makes no network call at all (X-6). The rest are one batched
// question, then an upload for what the remote lacks. An entry that is not
// verified here (absent, stale) is not this PC's to upload; one whose file is
// here but is not the catalog's cannot be, and is told in one line (X-9). A
// remote that cannot be reached or written is an error; the first such failure
// ends the loop. Objects are written once through a temp name, so a cut upload
// leaves nothing and is simply done again.
func storageUpload(ctx context.Context, root string, o vaultsync.Options) error {
	return storageUploadTo(ctx, root, o, 0, func(line string) { fmt.Fprintln(os.Stderr, line) })
}

// storageUploadTo is storageUpload with a limit and a voice. A payload larger than
// maxObject (when positive) is left for the next lw sync and named in one line to
// say (the auto-sync push, X-6); say gets every line the upload has for the user.
func storageUploadTo(ctx context.Context, root string, o vaultsync.Options, maxObject int64, say func(line string)) error {
	if len(o.Remotes) == 0 || !storageUsable(o) {
		return nil
	}
	st, err := storage.Open(root)
	if err != nil {
		return localErr(err)
	}
	entries, err := st.List()
	if err != nil {
		return localErr(err)
	}
	if len(entries) == 0 {
		return nil
	}
	st.BindRemote(storageScope(o))
	ob, err := st.Outbox()
	if err != nil {
		return localErr(err)
	}
	if line := storageUnreadyLine(ob.Unready); line != "" {
		say(line)
	}
	blobs := ob.Blobs[:0:0]
	for _, b := range ob.Blobs {
		if maxObject > 0 && b.Size > maxObject {
			say(fmt.Sprintf("storage: %s (%s) uploads on the next lw sync", b.Name, formatStorageSize(b.Size)))
			continue
		}
		blobs = append(blobs, b)
	}
	if len(blobs) == 0 {
		return nil
	}

	answers, batch := storageSecure(ctx, o, blobs)
	var held []string
	var problems []string
	uploaded := 0
	remoteDown := false // the blobs after the first remote failure share its cause
	for _, b := range blobs {
		r := answers[b.SHA256]
		if r.Held {
			held = append(held, b.SHA256)
			if r.Uploaded {
				uploaded++
			}
			continue
		}
		if batch != nil {
			if len(problems) == 0 {
				problems = append(problems, oneLine(batch.Error()))
			}
			continue
		}
		if r.Transient && remoteDown {
			continue
		}
		remoteDown = remoteDown || r.Transient
		reason := "no answer"
		if r.Err != nil {
			reason = oneLine(r.Err.Error())
		}
		problems = append(problems, fmt.Sprintf("upload of %s failed: %s", b.Name, reason))
	}
	var local []string
	if len(held) > 0 {
		if err := st.Confirm(held...); err != nil {
			local = append(local, storageReason(err))
		}
	}
	slog.Info("storage upload", "asked", len(blobs), "held", len(held), "uploaded", uploaded, "failed", len(problems))
	return stepErr(problems, local)
}

// storageUnreadyLine is the one line for the catalog entries this PC holds a file
// for that is not the catalog's, so they cannot be uploaded (X-9): it names them,
// where doctor shows the detail; "" when there are none.
func storageUnreadyLine(un []storage.Unready) string {
	switch len(un) {
	case 0:
		return ""
	case 1:
		return "storage: " + un[0].Name + " cannot be uploaded: this PC's copy is not the one the catalog describes (lw doctor shows how)"
	}
	names := make([]string, len(un))
	for i, u := range un {
		names[i] = u.Name
	}
	shown, more := names, ""
	if len(names) > 5 {
		shown, more = names[:5], fmt.Sprintf(", and %d more", len(names)-5)
	}
	return fmt.Sprintf("storage: %d files cannot be uploaded: this PC's copies are not the ones the catalog describes (lw doctor shows how): %s%s", len(un), strings.Join(shown, ", "), more)
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
// delivered, telling the user on stderr what it removed and what it kept. Deleting
// a file that no remote is known to hold is refused (rule P), so it may ask the
// remote, with ctx, for the few payloads that were never confirmed; everything
// else is local.
func storageReconcile(ctx context.Context, root string, o vaultsync.Options) error {
	return storageReconcileTo(ctx, root, o, func(line string) { fmt.Fprintln(os.Stderr, line) })
}

// storageReconcileTo is storageReconcile with the lines handed to say, for the
// TUI's background push, which must not write to a screen it does not own. The
// lines are reported once: Reconcile drops the ledger row of every name it
// decides about (or, for a copy it could not show yet, remembers it said so), so a
// second call has nothing left to say.
func storageReconcileTo(ctx context.Context, root string, o vaultsync.Options, say func(line string)) error {
	st, err := openBound(root, o)
	if err != nil {
		return err
	}
	var ensure storage.Ensure
	if len(o.Remotes) > 0 {
		ensure = storageEnsure(o)
	}
	res, err := st.Reconcile(ctx, ensure)
	preserved := map[string]bool{}
	for _, name := range res.Preserved {
		preserved[name] = true
	}
	for _, name := range res.Removed {
		if preserved[name] {
			say("storage: removed " + name + " (removed on another PC; a copy was kept on the remote store)")
			continue
		}
		say("storage: removed " + name + " (removed on another PC)")
	}
	for _, name := range res.Kept {
		say("storage: kept " + name + ": changed locally since it was removed elsewhere")
	}
	for _, nc := range res.NoCopy {
		say("storage: kept " + nc.Name + ": no copy on the remote yet (" + nc.Reason + ")")
	}
	if res.Skipped {
		slog.Info("storage reconcile skipped: a rebase is in progress")
	}
	if len(res.Removed)+len(res.Kept)+len(res.Adopted)+len(res.NoCopy)+res.Swept > 0 {
		slog.Info("storage reconcile", "removed", len(res.Removed), "preserved", len(res.Preserved), "kept", len(res.Kept), "no_copy", len(res.NoCopy), "adopted", len(res.Adopted), "swept", res.Swept)
	}
	return localErr(err)
}

// storageFetch brings in the payload of every catalog entry that is pending on
// this PC (absent, or an older version lw itself put there). An entry whose
// object is not on the remote yet is not an error: it is the normal state of a
// PC whose neighbour has not uploaded, so it is told in one line and the
// command succeeds (R6b: pending is not corrupt). A remote that cannot be
// reached is an error, and so is an object that fails its sha256/size check - told
// as a damaged remote object, and remembered so doctor says it too (X-4). A stale
// file whose old version has no copy on the remote is left alone (rule P). Corrupt
// entries are never fetched over (internal/storage refuses them).
func storageFetch(ctx context.Context, root string, o vaultsync.Options) error {
	if len(o.Remotes) == 0 || !storageUsable(o) {
		return nil
	}
	st, err := openBound(root, o)
	if err != nil {
		return err
	}
	rep, err := st.StatusQuick()
	if err != nil {
		return localErr(err)
	}
	ensure := storageEnsure(o)
	var remote, local, waiting []string
	fetched := 0
entries:
	for _, es := range rep.Entries {
		if es.State != storage.StatePending {
			continue
		}
		e := es.Entry
		err := storageFetchOne(ctx, st, o, ensure, e)
		var nc *storage.NoCopyError
		var verr *storage.VerifyError
		switch {
		case err == nil:
			fetched++
		case errors.Is(err, vaultsync.ErrObjectMissing):
			waiting = append(waiting, e.Name)
		case errors.Is(err, vaultsync.ErrObjectDamaged) || errors.As(err, &verr):
			if merr := st.MarkDamaged(e.SHA256); merr != nil {
				local = append(local, storageReason(merr))
			}
			remote = append(remote, storageDamagedReason(e.Name, err))
		case errors.As(err, &nc):
			if nc.Transient {
				remote = append(remote, storageReason(err))
				break entries // the remote is the problem: the next file would fail the same way
			}
			local = append(local, storageReason(err))
		case storageTransient(ctx, err):
			remote = append(remote, fmt.Sprintf("fetch of %s failed: %s", e.Name, storageReason(err)))
			break entries
		default:
			local = append(local, fmt.Sprintf("fetch of %s failed: %s", e.Name, storageReason(err)))
		}
	}
	if len(waiting) > 0 {
		fmt.Fprintln(os.Stderr, storageWaitingLine(waiting))
	}
	slog.Info("storage fetch", "fetched", fetched, "waiting", len(waiting), "remote_problems", len(remote), "local_problems", len(local))
	return stepErr(remote, local)
}

// storageDamagedReason is how a payload that fails its check is named: the object
// on the remote is damaged, and the detail says how.
func storageDamagedReason(name string, err error) string {
	return "remote object for " + name + " is damaged: " + storageReason(err)
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

// readerFunc is an io.Reader made of a function.
type readerFunc func(p []byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// storageFetchOne streams entry e's object from the remote into storage/<name>.
// The bytes go straight from GetObject into Materialize through a pipe, which
// verifies sha256 and size against the catalog before anything is renamed into
// place: a wrong or cut object leaves no file, no ledger row and no temp file.
// The transfer starts at the first byte Materialize asks for, so a file it
// refuses before reading (a corrupt one, a stale one with no copy of its old
// version) costs no connection.
//
// Two goroutines, two verdicts. When GetObject ends first (a missing object, an
// unreachable remote) its error is the cause and Materialize's "read stream"
// error only the echo; when Materialize ends first (a checksum it refused) the
// pipe is closed under GetObject, whose error is then the echo.
func storageFetchOne(ctx context.Context, st *storage.Storage, o vaultsync.Options, ensure storage.Ensure, e storage.Entry) error {
	var (
		pr     *io.PipeReader
		done   chan struct{}
		getErr error
	)
	src := readerFunc(func(p []byte) (int, error) {
		if pr == nil {
			var pw *io.PipeWriter
			pr, pw = io.Pipe()
			done = make(chan struct{})
			go func() {
				getErr = vaultsync.GetObject(ctx, o, vaultsync.Object{SHA256: e.SHA256, Size: e.Size}, pw)
				pw.CloseWithError(getErr) // nil closes with EOF
				close(done)
			}()
		}
		return pr.Read(p)
	})
	matErr := st.Materialize(ctx, e.Name, src, ensure)
	if pr == nil {
		return matErr
	}
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
	if !storage.ValidSHA256(e.SHA256) {
		return fmt.Errorf("%s has an invalid record in the catalog (sha256 %q is not 64 lower-case hex digits)", e.Name, e.SHA256)
	}
	es, err := st.Verify(e.Name)
	if err != nil {
		return err
	}
	switch es.State {
	case storage.StateVerified:
		return nil
	case storage.StateCorrupt:
		got := "unreadable"
		if es.DiskSHA != "" {
			got = shortSHA(es.DiskSHA)
		}
		return fmt.Errorf("%s is corrupt on disk (sha256 %s ≠ catalog %s)", e.Name, got, shortSHA(e.SHA256))
	}

	o, ok := storageFetchOptions(root)
	if !ok {
		return fmt.Errorf("%s is not on this PC and no [sync] remote is configured to fetch it from", e.Name)
	}
	if !storageUsable(o) {
		return fmt.Errorf("%s is not on this PC and none of the configured remotes can hold storage files (they need an ssh or local path remote)", e.Name)
	}
	ctx, stop := syncContext()
	defer stop()
	st.BindRemote(storageScope(o))
	err = storageFetchOne(ctx, st, o, storageEnsure(o), e)
	var re *vaultsync.RemoteError
	var verr *storage.VerifyError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, vaultsync.ErrObjectMissing):
		return fmt.Errorf("%s has not arrived yet %s", e.Name, storageNotArrived)
	case errors.Is(err, vaultsync.ErrObjectDamaged) || errors.As(err, &verr):
		if merr := st.MarkDamaged(e.SHA256); merr != nil {
			slog.Warn("storage: could not record the damaged object", "err", merr)
		}
		return errors.New(storageDamagedReason(e.Name, err))
	case errors.As(err, &re):
		return fmt.Errorf("%s has not arrived yet: remote unreachable: %s", e.Name, storageReason(err))
	}
	return err
}

// storageStatusLine is the line `lw sync status` adds when the catalog lists
// stored files (plan R5, X-11): how many are verified, pending and corrupt on this
// PC. "" when the catalog has no entries, so a vault that stores nothing prints
// what it always did.
func storageStatusLine(root string) string {
	st, err := storage.Open(root)
	if err != nil {
		return ""
	}
	rep, err := st.Status()
	if err != nil {
		return "storage: " + storageReason(err)
	}
	if len(rep.Entries) == 0 {
		return ""
	}
	var v, p, c int
	for _, es := range rep.Entries {
		switch es.State {
		case storage.StateVerified:
			v++
		case storage.StatePending:
			p++
		case storage.StateCorrupt:
			c++
		}
	}
	return fmt.Sprintf("storage: %d verified, %d pending, %d corrupt", v, p, c)
}
