package main

// sync_auto.go is 042's auto-sync: what a verb does about the remote vault
// when the user has asked for nothing — and has configured [sync] remotes.
//
// Two moments. At the START of a writing verb (tui, ingest, lint --fix,
// revert, commit, note) the vault is fast-forwarded to the remote, before the
// engine opens, so the verb works on the newest vault — a pull and nothing
// else: committing the work tree here would make a commit the remote lacks, and
// the PC that does it while another pushes has diverged (A-042-7 b). AFTER a
// change — a commit or a rejection, or the end of lw note — the vault is
// committed to git and pushed. Both are non-interactive and bounded
// (syncAutoTimeout in all), and both are the same promise: a failure warns on
// one line and the command carries on with the local vault. Auto-sync never
// makes a verb fail and never changes what it prints when nothing is wrong.
//
// It acts only on a vault that is under lw sync (its own .git with the
// remote-tracking ref): a repository the user made for themselves is theirs.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/vault"
	"github.com/awepo-pro/lw/internal/vaultsync"
)

// syncFlushBudget is how long the TUI's exit waits for its last pushes
// (042 D5).
const syncFlushBudget = 20 * time.Second

// The clock, the deadline, the debounce and the three vaultsync calls are
// package variables for the same reason newAgent is one: a test has to pin a
// time, shorten a wait, or count calls without a real server behind it.
var (
	syncNow          = time.Now
	syncAutoTimeout  = 15 * time.Second // ONE deadline for an auto-sync step; vaultsync's Timeout is per git call
	syncPushDebounce = time.Second

	// syncMinRemoteBudget is the least each remote gets of the step's deadline
	// (remoteBudget), and syncSlowAfter how long a step runs before it says so.
	syncMinRemoteBudget = 5 * time.Second
	syncSlowAfter       = time.Second
	syncPull            = vaultsync.Pull
	syncPush            = vaultsync.Push
	syncCommitWork      = vaultsync.CommitWork
)

// autoSync is one verb's auto-sync, nil when auto-sync does not apply to its
// vault — and every method is safe on nil, so a verb calls them without
// asking.
type autoSync struct {
	root    string
	remotes []string
	pusher  *syncPusher // the TUI's; nil for a CLI verb, which pushes in place

	mu     sync.Mutex // one CLI push at a time
	queued bool       // a CLI verb has asked for its push at the end (atVerbEnd)
}

// notSyncingLine is what a writing verb says when [sync] remotes are
// configured and the vault is not under lw sync, or git cannot say whether it
// is. Silence there would leave a user who set sync up believing a vault is
// being synced when nothing is (S3d M1).
const notSyncingLine = "sync: this vault is not under lw sync — run lw sync init or lw sync clone; not syncing"

// newAutoSync returns the auto-sync for root, or nil unless the config asks
// for it (remotes configured, auto not false) AND the vault is under lw sync.
// When remotes are configured and the vault is not under lw sync it says so
// once on stderr (notSyncingLine) and logs why — this is for the verbs that
// write; newQuietAutoSync is for the ones that only need the hook.
func newAutoSync(root string, cfg *config.Config) *autoSync { return buildAutoSync(root, cfg, true) }

// newQuietAutoSync is newAutoSync for a verb that is not a writing verb: it
// would only attach the hook (a rejection it makes), so a vault that is not
// under lw sync is not worth a line.
func newQuietAutoSync(root string, cfg *config.Config) *autoSync {
	return buildAutoSync(root, cfg, false)
}

func buildAutoSync(root string, cfg *config.Config, announce bool) *autoSync {
	if cfg == nil || !cfg.Sync.AutoSync() {
		return nil
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil
	}
	ok, why := underLWSyncErr(abs)
	if !ok {
		slog.Warn("sync: this vault is not under lw sync; not syncing", "vault", abs, "err", why)
		if announce {
			fmt.Fprintln(os.Stderr, notSyncingLine)
		}
		return nil
	}
	return &autoSync{root: abs, remotes: cfg.Sync.RemoteList()}
}

// loadAutoSync is newAutoSync for a verb that has not loaded the config. A
// config that does not load means no auto-sync, not a failed verb: the verbs
// that need the config report it themselves, and one that never read it must
// not start to.
func loadAutoSync(root string) *autoSync {
	cfg, err := config.Load()
	if err != nil {
		slog.Warn("sync: config not loaded; no auto-sync", "err", err)
		return nil
	}
	return newAutoSync(root, cfg)
}

// loadQuietAutoSync is loadAutoSync for a verb that only needs the hook.
func loadQuietAutoSync(root string) *autoSync {
	cfg, err := config.Load()
	if err != nil {
		slog.Warn("sync: config not loaded; no auto-sync", "err", err)
		return nil
	}
	return newQuietAutoSync(root, cfg)
}

// newTUIAutoSync is newAutoSync for the TUI: pushes go through an
// asynchronous pusher (sync_pusher.go) instead of blocking the screen.
func newTUIAutoSync(root string, cfg *config.Config) *autoSync {
	a := newAutoSync(root, cfg)
	if a != nil {
		a.pusher = newSyncPusher(a.pushOnce, syncPushDebounce)
	}
	return a
}

// loadTUIAutoSync is loadAutoSync for the TUI.
func loadTUIAutoSync(root string) *autoSync {
	cfg, err := config.Load()
	if err != nil {
		slog.Warn("sync: config not loaded; no auto-sync", "err", err)
		return nil
	}
	return newTUIAutoSync(root, cfg)
}

// options is vaultsync's view of this vault: non-interactive (BatchMode, a
// bounded connect, no prompts), each remote's network call bounded by its share
// of the step's deadline (remoteBudget). The remotes are tried last-answering
// first, and the journal is the append-only file a pull carries across
// (A-042-7).
func (a *autoSync) options() vaultsync.Options {
	return vaultsync.Options{
		Dir:        a.root,
		Remotes:    orderedRemotes(a.root, a.remotes),
		Timeout:    remoteBudget(syncAutoTimeout, len(a.remotes)),
		AppendOnly: syncAppendOnly,
		Quiesce:    quiesceVault(a.root),
	}
}

// remoteBudget is how long one remote may take out of total, the step's whole
// deadline: total divided among the n remotes, but never less than
// syncMinRemoteBudget, and the whole of it for a single remote. With the share
// a remote that stalls — a LAN name when away from home — costs its share and
// no more, and the remote behind it still gets its turn (S3d M2). vaultsync
// applies a Timeout to each git call; the step's context still ends the lot at
// total.
func remoteBudget(total time.Duration, n int) time.Duration {
	if n <= 1 {
		return total
	}
	share := total / time.Duration(n)
	if share < syncMinRemoteBudget {
		share = syncMinRemoteBudget
	}
	if share > total {
		share = total
	}
	return share
}

// quiesceVault is Options.Quiesce for the vault at root: what vaultsync holds
// while it changes the work tree, and not a moment before or after (A-042-9,
// A-042-10). It takes the vault lock — so a commit cannot land in the middle of
// a fast-forward or a rebase — checks that no commit is half-applied, and then
// the exclusive side of the lock every journal append takes shared, so no line
// is appended meanwhile. Both are given back together. vaultsync asks for it
// after its fetch and gives it back before its push, so the network is never
// under the vault lock: a foreground commit fails "busy" for the length of a
// local git command, not of a round trip to a far-away server.
func quiesceVault(root string) func() (func(), error) {
	return func() (func(), error) {
		unlock, err := lockVault(root)
		if err != nil {
			return nil, err
		}
		if err := noInterruptedCommit(root); err != nil {
			unlock()
			return nil, err
		}
		release, err := stage.QuiesceJournal(filepath.Join(root, stateDirName))
		if err != nil {
			unlock()
			return nil, err
		}
		return func() { release(); unlock() }, nil
	}
}

// guardVault is the check a sync step makes before it starts: the vault is not
// busy and no commit is half-applied. It holds the vault lock only to ask — the
// lock proper is taken, for the local mutation, by the quiesce — so a step that
// has nothing to change still says why it cannot go on.
func guardVault(root string) error {
	release, err := lockVault(root)
	if err != nil {
		return err
	}
	defer release()
	return noInterruptedCommit(root)
}

// commitWorkLocked is the commit of the work tree to git: under the vault lock
// for that commit alone, with no commit half-applied. The commit message is
// read under it, from the same journal the commit sees.
func commitWorkLocked(root string, o vaultsync.Options) (bool, error) {
	release, err := lockVault(root)
	if err != nil {
		return false, err
	}
	defer release()
	if err := noInterruptedCommit(root); err != nil {
		return false, err
	}
	return syncCommitWork(o, syncCommitMessage(root))
}

// syncAppendOnly is the vault's one append-only file: the journal. An open
// changeset has appended to it, so a PC in the middle of one still has to be
// able to take another PC's commits; vaultsync.Pull carries the uncommitted
// lines across the fast-forward (A-042-7 a).
var syncAppendOnly = []string{journalRel}

// syncReason turns a sync error into the words a warning uses, and says
// whether they are one of the reasons the spec names (so the caller adds no
// "pull failed:" in front of them).
func syncReason(err error, st vaultsync.State) (text string, named bool) {
	var fe *vaultsync.FormatError
	switch {
	case errors.Is(err, errCommitInterrupted):
		return errCommitInterrupted.Error(), true
	case errors.Is(err, errSyncBusy):
		return "vault is busy", true
	case errors.Is(err, vaultsync.ErrDiverged):
		return fmt.Sprintf("diverged from %s — run lw sync", st.Remote), true
	case errors.As(err, &fe):
		return fe.Error(), true
	case errors.Is(err, vaultsync.ErrDirty):
		return "the vault has uncommitted changes — run lw sync", true
	case errors.Is(err, vaultsync.ErrRebaseInProgress):
		return oneLine(err.Error()), true
	}
	return oneLine(err.Error()), false
}

// startWarning is the one line a failed start-of-verb sync prints. step names
// the part that failed ("commit" or "pull") for the reasons that have no name
// of their own. An interrupted commit prints its sentence alone (A-042-4 M2).
func startWarning(step string, err error, st vaultsync.State) string {
	text, named := syncReason(err, st)
	switch {
	case errors.Is(err, errCommitInterrupted):
		return "sync: " + text
	case named:
		return "sync: " + text + "; working on the local vault"
	}
	return "sync: " + step + " failed: " + text + "; working on the local vault"
}

// pushFailureLine is the one line a failed push after a change prints.
func pushFailureLine(res syncPushResult) string {
	text, _ := syncReason(res.Err, vaultsync.State{Remote: res.Remote})
	if errors.Is(res.Err, errCommitInterrupted) {
		return "sync: " + text
	}
	return "sync: push failed (" + text + "); the commit is safe locally — lw sync will retry"
}

// pull is the start-of-verb step: make sure the vault is not busy and no commit
// is half-applied, and fast-forward to the remote. The vault lock is taken by
// vaultsync's quiesce for the fast-forward itself and not for the fetch before
// it (A-042-10). It only pulls (A-042-7 b): a
// verb's start never commits the work tree to git, because a commit made here
// is a commit the remote lacks, and the first PC to do it while the other
// pushes has diverged. What stands in the way of a pull — uncommitted lines in
// the journal — vaultsync carries across it; what it cannot carry (a tracked
// file edited) it refuses, and the verb says so only if the remote had news.
// Called before the engine opens, so the engine opens on the pulled vault. Any
// failure is one warning on stderr and the verb goes on.
func (a *autoSync) pull() {
	if a == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), syncAutoTimeout)
	defer cancel()

	if err := guardVault(a.root); err != nil {
		a.warnStart("", err, vaultsync.State{})
		return
	}
	if err := a.recoverRebase(ctx); err != nil {
		a.warnStart("recover", err, vaultsync.State{})
		return
	}

	endNotice := slowNotice(os.Stderr, "pulling")
	st, err := syncPull(ctx, a.options(), stage.FormatVersion)
	endNotice()
	if errors.Is(err, vaultsync.ErrDirty) && st.Behind == 0 {
		// Nothing to pull, so nothing the edit stands in the way of: the fetch
		// worked and the remote is quiet.
		recordSyncAhead(a.root, st.Remote, nil, st.Ahead)
		a.retryIfAhead(st)
		return
	}
	if err != nil {
		recordSyncAhead(a.root, "", err, st.Ahead)
		a.warnStart("pull", err, st)
		return
	}
	recordSyncAhead(a.root, st.Remote, nil, st.Ahead)
	slog.Info("sync pull", "remote", st.Remote, "pulled", st.Pulled, "ahead", st.Ahead)
	a.retryIfAhead(st)
	if st.Pulled == 0 {
		return
	}
	if _, err := rebuildIndexIfStale(a.root); err != nil {
		slog.Warn("sync: rebuild index after pull", "err", err)
	}
	fmt.Fprintf(os.Stderr, "sync: pulled %d commit(s) from %s\n", st.Pulled, st.Remote)
	if st.Rebased > 0 {
		fmt.Fprintln(os.Stderr, rebasedLine("sync: ", st.Rebased, st.Remote))
	}
}

// recoverRebase undoes a rebase a crashed process left in the vault and says so
// (A-042-8), before this step builds on the vault. A rebase that is the user's
// is an error and is left alone.
func (a *autoSync) recoverRebase(ctx context.Context) error {
	note, err := vaultsync.Recover(ctx, a.options())
	if note != "" {
		fmt.Fprintln(os.Stderr, "sync: "+note)
	}
	return err
}

// retryIfAhead asks for a push when the pull found commits in git that the
// remote lacks (S3d H1). A push that failed, or a process killed between the
// commit and the push, leaves exactly that, and until now nothing looked again:
// the commit waited for the next change to push it alongside. The TUI's pusher
// is triggered; a CLI verb pushes once it has finished.
func (a *autoSync) retryIfAhead(st vaultsync.State) {
	if st.Ahead > 0 {
		slog.Info("sync: commits waiting to be pushed", "ahead", st.Ahead)
		a.requestPush()
	}
}

// requestPush asks for the vault to be pushed: the TUI's pusher is triggered,
// and a CLI verb gets its push after dispatch has printed the verb's own
// result (M2) — once, however many times it is asked.
func (a *autoSync) requestPush() {
	if a == nil {
		return
	}
	if a.pusher != nil {
		a.pusher.Trigger()
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.queued {
		a.queued = true
		atVerbEnd(a.pushInline)
	}
}

// warnStart prints a start-of-verb warning and logs it.
func (a *autoSync) warnStart(step string, err error, st vaultsync.State) {
	slog.Info("sync pull", "err", err)
	fmt.Fprintln(os.Stderr, startWarning(step, err, st))
}

// pushOnce is the whole push after a change: commit the work tree to git (under
// the vault lock, with no commit half-applied), then push. Any failure ends
// it — Push is never tried after a failed commit-work. It is what the CLI runs
// in place and what the TUI's pusher runs in the background; either way it logs
// its result to lw.log, never to a screen.
func (a *autoSync) pushOnce(ctx context.Context) syncPushResult {
	res := a.push(ctx)
	slog.Info("sync push", "remote", res.Remote, "pushed", res.Pushed, "err", res.Err)
	return res
}

func (a *autoSync) push(ctx context.Context) syncPushResult {
	pulled, err := a.commitAndPull(ctx)
	if err != nil {
		recordSyncAhead(a.root, "", err, pulled.Ahead)
		return syncPushResult{Remote: pulled.Remote, Err: err}
	}
	if pulled.Pulled > 0 {
		// The remote's commits are in the tree now (a divergence was rebased):
		// the pages may have changed under the search index.
		if _, ierr := rebuildIndexIfStale(a.root); ierr != nil {
			slog.Warn("sync: rebuild index after push", "err", ierr)
		}
	}
	// No lock is held: the push touches no file of the vault (it sends HEAD),
	// and holding one across a network round trip would make a foreground
	// commit fail with "vault is locked" for as long as the server is slow
	// (A-042-7 d). The fetch before the pull is no different, which is why the
	// vault lock is taken inside the pull's quiesce and not around it
	// (A-042-10).
	st, err := syncPush(ctx, a.options())
	res := syncPushResult{Remote: st.Remote, Pushed: st.Pushed, Pulled: pulled.Pulled, Rebased: pulled.Rebased}
	if err != nil {
		recordSyncAhead(a.root, "", err, st.Ahead)
		res.Err = err
		return res
	}
	recordSyncAhead(a.root, st.Remote, nil, st.Ahead)
	return res
}

// commitAndPull is the local half of a push: commit the work tree to git — the
// one place auto-sync commits (A-042-7 b) — then pull, which replays those
// commits on the remote's when the remote has moved and nothing conflicts
// (A-042-8). The rebase lives here and not in Push because it takes the work
// tree apart, and only Pull does that under the journal quiesce (A-042-9 c).
//
// Locks are held for local work only (A-042-10): the commit takes the vault lock
// for itself, and the pull takes it, together with the journal, inside its
// quiesce — after the fetch, not across it. A tree that became dirty after the
// commit (a session record landing) makes the pull refuse with ErrDirty; that
// record is committed and, if the remote had news, the pull tried once more, so
// the commits it brings in are not reported as a divergence. A tree that is
// dirty again after that is not an error: nothing is taken and Push goes on,
// which refuses a divergence it cannot reach.
func (a *autoSync) commitAndPull(ctx context.Context) (vaultsync.State, error) {
	if err := guardVault(a.root); err != nil {
		return vaultsync.State{}, err
	}
	if err := a.recoverRebase(context.Background()); err != nil {
		return vaultsync.State{}, err
	}
	commit := func() error {
		if _, err := commitWorkLocked(a.root, a.options()); err != nil {
			recordSync(a.root, "", err)
			return err
		}
		return nil
	}
	if err := commit(); err != nil {
		return vaultsync.State{}, err
	}
	st, err := syncPull(ctx, a.options(), stage.FormatVersion)
	if errors.Is(err, vaultsync.ErrDirty) {
		if err := commit(); err != nil {
			return st, err
		}
		if st.Behind == 0 {
			// The fetch showed nothing to take, so the late write stood in the
			// way of nothing: it is committed now and the push sends it.
			return st, nil
		}
		st, err = syncPull(ctx, a.options(), stage.FormatVersion)
		if errors.Is(err, vaultsync.ErrDirty) {
			return st, nil
		}
	}
	return st, err
}

// pushInline is the CLI's push after a change: run when the verb has finished
// and printed its result (requestPush registers it with dispatch), under the
// auto-sync deadline, reporting on stderr. A push still running after a second
// says so.
func (a *autoSync) pushInline() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.queued = false
	a.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), syncAutoTimeout)
	defer cancel()
	endNotice := slowNotice(os.Stderr, "pushing")
	res := a.pushOnce(ctx)
	endNotice()
	a.report(os.Stderr, res)
}

// rebasedLine is the line for a divergence that was resolved by replaying local
// commits on the remote's tip (A-042-8).
func rebasedLine(prefix string, n int, remote string) string {
	return fmt.Sprintf("%srebased %d local commit(s) onto %s", prefix, n, remote)
}

// report prints a push's outcome as the CLI line: first, when the push had to
// take the remote's commits and replay its own on them, what happened to the
// vault; then the success line when it sent commits, the failure line when it
// failed, nothing when there was nothing to send.
func (a *autoSync) report(w io.Writer, res syncPushResult) {
	if res.Pulled > 0 {
		fmt.Fprintf(w, "sync: pulled %d commit(s) from %s\n", res.Pulled, res.Remote)
	}
	if res.Rebased > 0 {
		fmt.Fprintln(w, rebasedLine("sync: ", res.Rebased, res.Remote))
	}
	switch {
	case res.Err != nil:
		fmt.Fprintln(w, pushFailureLine(res))
	case res.Pushed > 0:
		fmt.Fprintf(w, "sync: pushed %d commit(s) to %s\n", res.Pushed, res.Remote)
	}
}

// attach installs the terminal hook on e: a commit or a rejection — every
// terminal event, so an empty ask's auto-close and lw doctor --discard-changeset
// too (A-042-4 M4) — pushes the vault. openVaultEngine is the only caller.
func (a *autoSync) attach(e *stage.Engine) {
	if a == nil || e == nil {
		return
	}
	e.OnTerminal(a.onTerminal)
}

// onTerminal is the hook. It may run on any goroutine and holds no lock of the
// engine's, and it never waits on the network: the TUI hands the work to its
// pusher, a CLI verb to the end of the verb — after Commit has returned and the
// verb has printed "committed <id>".
func (a *autoSync) onTerminal(ev stage.TerminalEvent) {
	slog.Info("sync trigger", "kind", ev.Kind, "changeset", ev.Changeset)
	a.requestPush()
}

// afterNote is lw note's push: the note is already on disk and named on
// stdout, so the vault is committed to git and pushed once the verb has
// finished.
func (a *autoSync) afterNote() { a.requestPush() }

// finish is the TUI's exit: wait for the push in flight, send what is left,
// and print the CLI's success or failure line to w — called after the terminal
// has been restored, so it lands on the shell and not in the alternate screen.
func (a *autoSync) finish(w io.Writer) {
	if a == nil || a.pusher == nil {
		return
	}
	endNotice := slowNotice(w, "pushing")
	res := a.pusher.Flush(syncFlushBudget, func() bool { return syncNeedsPush(a.root) })
	endNotice()
	a.report(w, res)
}

// openVaultEngine is the one way a verb opens the vault's engine (A-042-4 M3).
// It opens it and, when auto-sync applies, installs the terminal hook that
// pushes after a commit or a rejection. A verb that opened the engine itself
// would commit without ever syncing, and nothing would say so —
// TestEngineOpensGoThroughOneHelper keeps the list of exceptions short.
func openVaultEngine(root string, a *autoSync) (*stage.Engine, error) {
	e, err := stage.OpenEngine(root)
	if err != nil {
		return nil, err
	}
	a.attach(e)
	return e, nil
}

// rebuildIndexIfStale rebuilds the search index when it no longer matches the
// vault — absent, unreadable, or built from other pages — and reports whether
// it did. After a pull this is "the pull changed wiki/": the index covers the
// pages and nothing else, so exactly the pulls that changed them leave it
// stale. The rebuild is doctor --rebuild-index's own (rebuildIndex).
func rebuildIndexIfStale(root string) (bool, error) {
	v, err := vault.Open(root)
	if err != nil {
		return false, fmt.Errorf("open vault %s: %w", root, err)
	}
	ix, loadErr := index.Load(filepath.Join(root, stateDirName, indexFileName))
	if loadErr == nil && !ix.StaleAgainst(v) {
		return false, nil
	}
	if _, err := rebuildIndex(root, v); err != nil {
		return false, err
	}
	return true, nil
}
