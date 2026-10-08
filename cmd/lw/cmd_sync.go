package main

// cmd_sync.go implements `lw sync` (042 D5): the four verbs that move a vault
// between PCs through a storage-only git remote, and the config lines that
// make the first one stick.
//
//	lw sync [--take-remote]       commit, pull, push
//	lw sync status                ahead/behind, changing nothing
//	lw sync init <remote>         put this vault under lw sync, push it
//	lw sync clone <remote> <dir>  copy a synced vault to this PC
//
// These are the explicit verbs: interactive (ssh may prompt, git's progress
// passes through to stderr, nothing is bounded but the user's patience — a
// first push through a slow tunnel can take minutes) and loud (an error is an
// error and exits 1). Auto-sync (sync_auto.go) is the quiet counterpart that
// runs inside the other verbs. Both do their git work through internal/vaultsync
// and stay out of the vault's .gitignore: lw sync OWNS that file and rewrites it
// to the managed bytes whenever they differ, which is why the help says so.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/vault"
	"github.com/awepo-pro/lw/internal/vaultsync"
)

// syncUsage is the help for the sync verbs.
func syncUsage(w io.Writer) {
	fmt.Fprint(w, `usage: lw sync [--take-remote] [--vault P]    pull then push the vault through its [sync] remotes
       lw sync status [--vault P]               show ahead/behind against the remote, without changing anything
       lw sync init <remote> [--vault P]        put this vault under lw sync and push it to an empty remote
       lw sync clone <remote> <dir>             copy a synced vault from its remote to this PC

A remote is host:path (over ssh), an ssh:// URL or a local path; the server needs
only sshd and git. [sync] remotes in config.toml lists them, tried in order.

lw sync owns the vault's .gitignore: it rewrites it to the per-PC state it keeps
out of the remote (the search index, caches, logs, locks), whenever the bytes differ.
Divergence is detected and refused, never merged; --take-remote keeps the remote and
saves this PC's commits on a branch named lw-diverged-<time>.
`)
}

// errNoSyncRemotes is what every sync verb says when the config names none.
var errNoSyncRemotes = errors.New("no [sync] remotes in config — run lw sync init <host:path> first")

// cmdSync dispatches the sync verbs. Flags may precede or follow a
// subcommand word; --take-remote belongs to the bare verb alone.
func cmdSync(args []string) error {
	// Help is a request, not a mistake: it prints to stdout and succeeds, the
	// way lw --help does. The flag package would print it to stderr and make
	// the caller exit 2, so the words are found first.
	if wantsHelp(args) {
		syncUsage(os.Stdout)
		return nil
	}
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { syncUsage(os.Stderr) }
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	takeRemote := fs.Bool("take-remote", false, "keep the remote's vault and save this PC's commits on a backup branch")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}

	rest := fs.Args()
	if len(rest) == 0 {
		return syncRun(*vaultPath, *takeRemote)
	}
	if *takeRemote {
		fmt.Fprintln(os.Stderr, "lw sync: --take-remote takes no subcommand")
		return &exitError{code: 2}
	}
	switch sub, subArgs := rest[0], rest[1:]; sub {
	case "status":
		vp, pos, err := parseSyncSub("status", subArgs, *vaultPath)
		if err != nil {
			return err
		}
		if len(pos) != 0 {
			fmt.Fprintf(os.Stderr, "lw sync status: unexpected argument %q\n", pos[0])
			return &exitError{code: 2}
		}
		return syncStatus(vp)
	case "init":
		vp, pos, err := parseSyncSub("init", subArgs, *vaultPath)
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			fmt.Fprintln(os.Stderr, "usage: lw sync init <remote> [--vault P]")
			return &exitError{code: 2}
		}
		return syncInit(vp, pos[0])
	case "clone":
		_, pos, err := parseSyncSub("clone", subArgs, "")
		if err != nil {
			return err
		}
		if len(pos) != 2 {
			fmt.Fprintln(os.Stderr, "usage: lw sync clone <remote> <dir>")
			return &exitError{code: 2}
		}
		return syncClone(pos[0], pos[1])
	}
	fmt.Fprintf(os.Stderr, "lw sync: unknown subcommand %q\n", rest[0])
	syncUsage(os.Stderr)
	return &exitError{code: 2}
}

// parseSyncSub parses a subcommand's arguments: --vault, defaulting to the one
// given before the subcommand word, and the positional words, which may be
// mixed with flags (`lw sync init host:path --vault P`) — the flag package
// stops at the first positional, so each is taken off and parsing resumes.
func parseSyncSub(name string, args []string, vaultDefault string) (vaultPath string, positional []string, err error) {
	fs := flag.NewFlagSet("sync "+name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { syncUsage(os.Stderr) }
	vp := fs.String("vault", vaultDefault, "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	for {
		if perr := fs.Parse(args); perr != nil {
			return "", nil, &exitError{code: 2}
		}
		if fs.NArg() == 0 {
			return *vp, positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// wantsHelp reports whether args ask for help: -h, -help or --help anywhere.
func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "-help" || a == "--help" {
			return true
		}
	}
	return false
}

// syncContext is the context of an explicit sync step: cancelled by Ctrl-C,
// bounded by nothing else (vaultsync's interactive calls are unbounded by
// design — a person is watching).
func syncContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

// syncMode says what a sync verb is about to do to the vault, which decides
// whether a checkout collision stops it and whether it may create the log
// directory.
type syncMode int

const (
	syncModeWrite syncMode = iota // commit, pull, push: refused while a collision is unresolved
	syncModeTake                  // --take-remote: the way out of a collision, so never refused
	syncModeRead                  // status: changes nothing, joins the log, creates none
)

// syncSetup is what every sync verb but clone and init starts from: the vault
// root, and the options to sync it with.
func syncSetup(vaultPath string, mode syncMode) (root string, o vaultsync.Options, err error) {
	if mode == syncModeWrite {
		root, err = writableVaultRoot(vaultPath)
	} else {
		root, err = findVaultRoot(vaultPath)
	}
	if err != nil {
		return "", o, err
	}
	if root, err = filepath.Abs(root); err != nil {
		return "", o, err
	}
	if mode == syncModeRead {
		attachLoggingAt(root)
	} else {
		initLoggingAt(root)
	}

	cfg, err := config.Load()
	if err != nil {
		return "", o, fmt.Errorf("load config: %w", err)
	}
	remotes := cfg.Sync.RemoteList()
	if len(remotes) == 0 {
		return "", o, errNoSyncRemotes
	}
	if !underLWSync(root) {
		return "", o, vaultsync.ErrNotRepo
	}
	return root, vaultsync.Options{
		Dir:         root,
		Remotes:     orderedRemotes(root, remotes),
		Interactive: true,
		Stderr:      os.Stderr,
		AppendOnly:  syncAppendOnly,
	}, nil
}

// syncExplicitErr is the error an explicit sync shows for a vaultsync error: a
// divergence gets the long sentence that names the way out; every other error
// (the format, the remotes, the repo) already reads as the message.
func syncExplicitErr(err error, st vaultsync.State) error {
	if errors.Is(err, vaultsync.ErrDiverged) {
		return fmt.Errorf("diverged from %s: this PC has %d commit(s) the remote lacks, the remote has %d this PC lacks; "+
			"nothing was changed — lw sync --take-remote keeps the remote and saves this PC's commits on a backup branch",
			st.Remote, st.Ahead, st.Behind)
	}
	return err
}

// syncGuard takes the vault lock and checks no commit is half-applied — the two
// things every explicit sync step does before it changes the vault. The
// returned release is a no-op after a refusal.
func syncGuard(root string) (release func(), err error) {
	release, err = lockVault(root)
	if err != nil {
		if errors.Is(err, errSyncBusy) {
			return nil, errors.New("vault is busy (a commit is in progress) — try again")
		}
		return nil, err
	}
	if err := noInterruptedCommit(root); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// syncRun is `lw sync` and `lw sync --take-remote`.
//
// The plain verb pulls first (A-042-7 b): a PC whose only uncommitted change is
// journal lines takes the remote's commits with those lines carried across. A
// work tree with anything else uncommitted comes back ErrDirty, and only then is
// it committed and the pull tried again — which may diverge, correctly, because
// by then there is a local commit. Whatever is left uncommitted after the pull
// (the carried lines) is committed, and the commit pushed. The vault lock covers
// the commit-work and pull; the push runs without it (A-042-7 d).
func syncRun(vaultPath string, takeRemote bool) error {
	mode := syncModeWrite
	if takeRemote {
		mode = syncModeTake
	}
	root, o, err := syncSetup(vaultPath, mode)
	if err != nil {
		return err
	}
	ctx, stop := syncContext()
	defer stop()

	if takeRemote {
		release, err := syncGuard(root)
		if err != nil {
			return err
		}
		defer release()
		return syncTake(ctx, root, o)
	}

	st, committed, err := syncPullLocked(ctx, root, o)
	if err != nil {
		recordSync(root, "", err)
		return syncExplicitErr(err, st)
	}
	pulled := st.Pulled
	remote := st.Remote
	if pulled > 0 {
		fmt.Printf("pulled %d commit(s) from %s\n", pulled, remote)
		if err := printIndexRebuild(root); err != nil {
			return err
		}
	}

	// Only a commit this run made, or one that was already waiting (Ahead), has
	// anything to push — and a second fetch to pay for.
	pushed := 0
	if committed || st.Ahead > 0 {
		pst, err := syncPush(ctx, o)
		if err != nil {
			recordSync(root, "", err)
			return syncExplicitErr(err, pst)
		}
		pushed, remote = pst.Pushed, pst.Remote
	}
	recordSync(root, remote, nil)

	switch {
	case pushed > 0:
		fmt.Printf("pushed %d commit(s) to %s\n", pushed, remote)
	case pulled == 0:
		fmt.Printf("up to date with %s\n", remote)
	}
	return nil
}

// syncPullLocked is the locked half of an explicit sync: under the vault lock,
// with no commit half-applied, pull; on ErrDirty commit the work tree and pull
// again; then commit what the pull left (carried journal lines). It reports
// whether it committed anything. The lock is released before it returns, so
// the push that follows is not under it.
func syncPullLocked(ctx context.Context, root string, o vaultsync.Options) (st vaultsync.State, committed bool, err error) {
	release, err := syncGuard(root)
	if err != nil {
		return st, false, err
	}
	defer release()

	st, err = syncPull(ctx, o, stage.FormatVersion)
	if errors.Is(err, vaultsync.ErrDirty) {
		if committed, err = syncCommitWork(o, syncCommitMessage(root)); err != nil {
			return st, false, err
		}
		if st.Behind > 0 {
			// The remote had news the edit stood in the way of; now that the
			// edit is a commit, the pull is a fast-forward or a divergence.
			if st, err = syncPull(ctx, o, stage.FormatVersion); err != nil {
				return st, committed, err
			}
		} else {
			err = nil // the fetch showed nothing to take: committing was all it needed
		}
	}
	if err != nil {
		return st, false, err
	}
	c, err := syncCommitWork(o, syncCommitMessage(root))
	if err != nil {
		return st, committed, err
	}
	return st, committed || c, nil
}

// syncTake is `lw sync --take-remote`: the escape hatch for a divergence. The
// remote wins; this PC's commits, and any work not yet committed, are kept on
// a backup branch.
func syncTake(ctx context.Context, root string, o vaultsync.Options) error {
	backup, st, err := vaultsync.TakeRemote(ctx, o, stage.FormatVersion)
	if err != nil {
		recordSync(root, "", err)
		return err
	}
	recordSync(root, st.Remote, nil)
	fmt.Printf("took %s; this PC's commits are saved on branch %s (git -C %s log %s)\n", st.Remote, backup, root, backup)
	return printIndexRebuild(root)
}

// printIndexRebuild rebuilds the search index when the pull left it stale and
// says so. It is doctor --rebuild-index's own rebuild, run because the pull
// changed the pages the index is built from.
func printIndexRebuild(root string) error {
	rebuilt, err := rebuildIndexIfStale(root)
	if err != nil {
		return fmt.Errorf("rebuild the search index: %w", err)
	}
	if rebuilt {
		fmt.Println("rebuilt the search index")
	}
	return nil
}

// syncStatus is `lw sync status`: fetch and report, change nothing.
func syncStatus(vaultPath string) error {
	root, o, err := syncSetup(vaultPath, syncModeRead)
	if err != nil {
		return err
	}
	ctx, stop := syncContext()
	defer stop()
	st, err := vaultsync.Status(ctx, o)
	if err != nil {
		return err
	}
	last := readSyncState(root).LastOK
	if last == "" {
		last = "never"
	}
	fmt.Printf("remote   %s\nahead    %d\nbehind   %d\nformat   %d (this lw: %d)\nlast     %s\n",
		st.Remote, st.Ahead, st.Behind, st.RemoteFormat, stage.FormatVersion, last)
	if st.Diverged() {
		fmt.Println("diverged — run lw sync for details")
	}
	return nil
}

// syncInit is `lw sync init <remote>`: put the vault under lw sync and push it
// to an empty remote, then remember the remote and the vault in the config.
func syncInit(vaultPath, remote string) error {
	root, err := writableVaultRoot(vaultPath)
	if err != nil {
		return err
	}
	// Init commits EVERYTHING under the directory it is given. A mistyped
	// --vault must not turn a home directory into a repository.
	if err := requireSchema(root); err != nil {
		return err
	}
	if root, err = filepath.Abs(root); err != nil {
		return err
	}
	initLoggingAt(root)

	ctx, stop := syncContext()
	defer stop()
	release, err := syncGuard(root)
	if err != nil {
		return err
	}
	defer release()

	o := vaultsync.Options{Dir: root, Remotes: []string{remote}, Interactive: true, Stderr: os.Stderr}
	st, err := vaultsync.Init(ctx, o)
	if err != nil {
		recordSync(root, "", err)
		return err
	}
	recordSync(root, st.Remote, nil)
	fmt.Printf("pushed %d commit(s) to %s\n", st.Pushed, st.Remote)
	return adoptSyncConfig(os.Stdout, root, remote)
}

// syncClone is `lw sync clone <remote> <dir>`: copy a synced vault to this PC,
// build its index, and remember the vault and the remote in the config.
func syncClone(remote, dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	ctx, stop := syncContext()
	defer stop()
	o := vaultsync.Options{Dir: abs, Remotes: []string{remote}, Interactive: true, Stderr: os.Stderr}
	if err := vaultsync.Clone(ctx, o, stage.FormatVersion); err != nil {
		return err
	}

	// git keeps no empty directory and the index is per PC, so the clone has
	// neither; the index is built here, the rest is made by the first verb.
	v, err := vault.Open(abs)
	if err != nil {
		return fmt.Errorf("open the cloned vault %s: %w", abs, err)
	}
	if _, err := rebuildIndex(abs, v); err != nil {
		return fmt.Errorf("index the cloned vault: %w", err)
	}
	recordSync(abs, remote, nil)
	fmt.Printf("cloned %s into %s\n", remote, abs)
	return adoptSyncConfig(os.Stdout, abs, remote)
}

// adoptSyncConfig writes the config keys sync init and sync clone imply — and
// only the ones the config lacks. A PC that already names a vault or remotes
// keeps its own words (the remote list is the user's ordered choice among
// names for one server, and sync init's single remote is not a replacement for
// it), and a config that already holds both is not even rewritten. One line per
// key it set.
func adoptSyncConfig(w io.Writer, root, remote string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("the vault is synced but the config could not be read to remember it: %w", err)
	}
	var lines []string
	if cfg.Vault.Path == "" {
		cfg.Vault.Path = root
		lines = append(lines, fmt.Sprintf("config: [vault] path = %s", root))
	}
	if len(cfg.Sync.RemoteList()) == 0 {
		auto := (*bool)(nil)
		if cfg.Sync != nil {
			auto = cfg.Sync.Auto
		}
		cfg.Sync = &config.Sync{Remotes: []string{remote}, Auto: auto}
		lines = append(lines, fmt.Sprintf("config: [sync] remotes = [%q]", remote))
	}
	if len(lines) == 0 {
		return nil
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("the vault is synced but the config could not be saved: %w", err)
	}
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
	return nil
}

// requireSchema refuses a root that is not a vault, as lw note does: SCHEMA.md
// is the marker findVaultRoot itself walks up for.
func requireSchema(root string) error {
	if info, err := os.Stat(filepath.Join(root, "SCHEMA.md")); err != nil || info.IsDir() {
		return fmt.Errorf("%s is not a vault (no SCHEMA.md); pass --vault", root)
	}
	return nil
}
