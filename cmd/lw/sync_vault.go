package main

// sync_vault.go holds what every 042 sync path asks of the vault itself,
// before or around a git operation: is its format one this lw may touch, is a
// checkout collision still unresolved, is it under lw sync at all, is a commit
// half-applied, which lw commits has git not seen yet. Each answer reads the
// vault directly — a small file, one read-only git query — because the callers
// run before an engine exists (the pull at the start of a verb) or beside one
// (the push after a commit), and none of them may write a byte while asking.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/awepo-pro/lw/internal/stage"
)

// syncGitTimeout bounds one local git query. They touch no network, so only a
// wedged filesystem makes one slow, and it must not hang a verb.
const syncGitTimeout = 10 * time.Second

// trackRef is the remote-tracking ref lw sync keeps: a vault is "under lw
// sync" exactly when it exists (vaultsync's definition, D3). It is never a
// configured git remote, so a stray `git pull` cannot bypass the divergence
// check.
const trackRef = "refs/remotes/lw/main"

// collisionMarkerRel is the file vaultsync leaves in .git while a checkout
// collision is unresolved (A-042-6).
const collisionMarkerRel = ".git/lw-collision"

// checkVaultFormat is the format pre-check every verb makes (042 A-042-2): a
// vault whose .llmwiki/format names a version newer than this lw's is refused
// with the *stage.FormatError text; a format file that cannot be read is an
// error too, because guessing a damaged file's version could open a vault of
// unknown shape. A vault with no format file — every vault today — passes.
func checkVaultFormat(root string) error {
	have, err := stage.ReadFormat(filepath.Join(root, stateDirName))
	if err != nil {
		return err
	}
	if have > stage.FormatVersion {
		return &stage.FormatError{Have: have, Max: stage.FormatVersion}
	}
	return nil
}

// collisionRefusal returns the refusal every writing verb gives while the
// vault's checkout collision is unresolved (A-042-6), or nil when there is
// none. The text is vaultsync's own, rebuilt from the same marker file —
// TestCollisionRefusalMatchesVaultsync pins the two together.
//
// The reason it exists: `lw sync --take-remote` discards tracked edits made
// while the marker is there (it resets the tree to the remote's), so a commit,
// a note or an ingest made in the meantime would be lost. Better to refuse
// before doing anything than to lose work later.
func collisionRefusal(root string) error {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(collisionMarkerRel)))
	if err != nil {
		return nil // no marker (or no .git at all): nothing is unresolved
	}
	var paths []string
	for _, line := range strings.Split(string(b), "\n") {
		if line != "" {
			paths = append(paths, line)
		}
	}
	return fmt.Errorf("a checkout collision is unresolved (%s) — rename the clashing files on the PC that created them, sync there, then run lw sync --take-remote here", strings.Join(paths, ", "))
}

// writableVaultRoot is findVaultRoot for a verb that writes: it also refuses
// while a checkout collision is unresolved. The refusal comes first — before
// the log directory, the auto-pull, the engine — so a refused verb leaves no
// trace.
func writableVaultRoot(explicit string) (string, error) {
	root, err := findVaultRoot(explicit)
	if err != nil {
		return "", err
	}
	if err := collisionRefusal(root); err != nil {
		return "", err
	}
	return root, nil
}

// syncGit runs a read-only git query in the vault and returns its stdout
// untrimmed. The environment is the one vaultsync gives git — nothing that
// redirects it away from the vault, a ceiling so a broken .git cannot make it
// find the repository the vault sits in — and the user's global ignore and
// attributes are switched off, so what git reports is what a sync would see.
// GIT_OPTIONAL_LOCKS=0 keeps `git status` from refreshing the index: a query
// must not write.
func syncGit(root string, args ...string) (string, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), syncGitTimeout)
	defer cancel()
	full := append([]string{
		"-c", "core.quotepath=off",
		"-c", "core.excludesFile=/dev/null",
		"-c", "core.attributesFile=/dev/null",
		"-c", "core.hooksPath=/dev/null",
	}, args...)
	cmd := exec.CommandContext(ctx, git, full...)
	cmd.Dir = root
	cmd.Env = syncGitEnv(root)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// redirectingGitEnv are the variables that point git somewhere other than the
// vault (a `make check` run from a git hook carries GIT_DIR and
// GIT_INDEX_FILE), the same set vaultsync scrubs.
var redirectingGitEnv = map[string]bool{
	"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true, "GIT_OBJECT_DIRECTORY": true,
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true, "GIT_COMMON_DIR": true, "GIT_NAMESPACE": true, "GIT_PREFIX": true,
	"GIT_CEILING_DIRECTORIES": true, "GIT_OPTIONAL_LOCKS": true,
}

// syncGitEnv is the environment for syncGit.
func syncGitEnv(root string) []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !redirectingGitEnv[name] {
			env = append(env, kv)
		}
	}
	return append(env, "GIT_CEILING_DIRECTORIES="+filepath.Dir(root), "GIT_OPTIONAL_LOCKS=0")
}

// underLWSync reports whether the vault is under lw sync: its own git work
// tree (a .git in the vault root — a vault that merely sits inside some other
// repository is not) that holds the remote-tracking ref. Anything weaker would
// let auto-sync commit a repository that is the user's own.
func underLWSync(root string) bool {
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return false
	}
	_, err := syncGit(root, "rev-parse", "--verify", "--quiet", trackRef)
	return err == nil
}

// syncNeedsPush reports whether a final push could have something to send:
// the work tree holds changes git has not seen, or HEAD is ahead of the
// remote-tracking ref (042 D5, the TUI's exit flush). A query that fails
// answers true — a push that finds nothing is cheap, a skipped one loses
// the commit's trip to the server.
func syncNeedsPush(root string) bool {
	dirty, err := syncGit(root, "status", "--porcelain", "--untracked-files=normal")
	if err != nil || strings.TrimSpace(dirty) != "" {
		return true
	}
	ahead, err := syncGit(root, "rev-list", "--count", trackRef+"..HEAD", "--")
	if err != nil {
		return true
	}
	return strings.TrimSpace(ahead) != "0"
}

// errSyncBusy means the vault lock is held: a commit is in progress.
var errSyncBusy = errors.New("vault is busy")

// errCommitInterrupted means a commit began and never finished. open/ is per
// PC and never synced, so committing the vault now would put a half-applied
// commit on every PC (A-042-4 M2).
var errCommitInterrupted = errors.New("a commit was interrupted — run lw doctor first")

// lockVault takes the vault lock (stage.AcquireLock on .llmwiki) for a sync
// step and returns what releases it. A held lock is errSyncBusy.
func lockVault(root string) (release func(), err error) {
	dir := filepath.Join(root, stateDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", stateRel, err)
	}
	rel, err := stage.AcquireLock(dir)
	if err != nil {
		if errors.Is(err, stage.ErrLocked) {
			return nil, errSyncBusy
		}
		return nil, err
	}
	return func() {
		if err := rel(); err != nil {
			slog.Warn("sync: release vault lock", "err", err)
		}
	}, nil
}

// commitInterrupted reports whether the journal's last commit_begin has no
// commit_end, or has one but its changeset never left changesets/open/ (the
// two states Engine.Recover calls Interrupted and Unmoved). It reads the
// journal directly because the auto-pull runs before an engine is opened, and
// the engine opens last so that it opens on the pulled vault. A missing
// journal is no commit at all; a line that does not parse is skipped, as a
// torn final line from a crash mid-append must not read as "all clear" or as
// "interrupted".
func commitInterrupted(root string) (bool, error) {
	b, err := os.ReadFile(filepath.Join(root, stateDirName, journalFileName))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read journal: %w", err)
	}
	var begin *stage.Event
	resolved := false
	for _, ev := range journalEvents(b) {
		switch ev.Kind {
		case stage.EvCommitBegin:
			e := ev
			begin, resolved = &e, false
		case stage.EvCommitEnd:
			if begin != nil && ev.Commit == begin.Commit {
				resolved = true
			}
		}
	}
	if begin == nil {
		return false, nil
	}
	if !resolved {
		return true, nil
	}
	_, statErr := os.Stat(filepath.Join(root, stateDirName, "changesets", "open", begin.Changeset))
	return statErr == nil, nil
}

// journalEvents decodes the journal's lines, skipping any that do not parse.
func journalEvents(b []byte) []stage.Event {
	var evs []stage.Event
	for _, line := range bytes.Split(b, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev stage.Event
		if json.Unmarshal(line, &ev) == nil {
			evs = append(evs, ev)
		}
	}
	return evs
}

// noInterruptedCommit is the guard every sync step makes before it commits
// the vault to git.
func noInterruptedCommit(root string) error {
	interrupted, err := commitInterrupted(root)
	if err != nil {
		return err
	}
	if interrupted {
		return errCommitInterrupted
	}
	return nil
}

// syncCommitMessage is the commit message for the git commit a sync makes of
// the vault's current state (042 D5): "lw <id>[, <id>…]: <first commit's
// message>" for the lw commits whose commit_end is in the journal and not yet
// in git — read as the journal's lines beyond what HEAD's copy holds, because
// the journal is append-only and git has the copy as of the last sync — else
// "lw notes" when everything that changed is under notes/, else "lw sync".
// Every path that cannot answer falls back to "lw sync": a commit message is
// never a reason to fail a sync.
func syncCommitMessage(root string) string {
	if ids, first := unsyncedCommits(root); len(ids) > 0 {
		msg := "lw " + strings.Join(ids, ", ")
		if first != "" {
			msg += ": " + first
		}
		return msg
	}
	if onlyNotesChanged(root) {
		return "lw notes"
	}
	return "lw sync"
}

// unsyncedCommits lists the commit ids of the commit_end events appended to
// the journal since HEAD, oldest first, with the first line of the first one's
// message.
func unsyncedCommits(root string) (ids []string, first string) {
	work, err := os.ReadFile(filepath.Join(root, stateDirName, journalFileName))
	if err != nil {
		return nil, ""
	}
	// HEAD's copy is empty when git has no journal yet (a vault that began
	// journalling after its last sync): every commit_end is new.
	head, _ := syncGit(root, "cat-file", "blob", "HEAD:"+stateDirName+"/"+journalFileName)
	if !bytes.HasPrefix(work, []byte(head)) {
		return nil, "" // not an append to what git holds: say nothing rather than guess
	}
	for _, ev := range journalEvents(work[len(head):]) {
		if ev.Kind != stage.EvCommitEnd || ev.Commit == "" {
			continue
		}
		if len(ids) == 0 {
			first = firstLine(ev.Message)
		}
		ids = append(ids, ev.Commit)
	}
	return ids, first
}

// onlyNotesChanged reports whether the work tree differs from HEAD in notes/
// and nowhere else.
func onlyNotesChanged(root string) bool {
	out, err := syncGit(root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return false
	}
	seen := false
	entries := strings.Split(out, "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		seen = true
		if !strings.HasPrefix(e[3:], noteDirName+"/") {
			return false
		}
		if strings.ContainsAny(e[:2], "RC") { // the entry after a rename is its source
			i++
		}
	}
	return seen
}

// firstLine is s up to its first newline, trimmed.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}

// oneLine collapses all whitespace runs to single spaces: a warning is one
// line, whatever multi-line text git put in the error.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// orderedRemotes is the configured remotes with the one that answered last put
// first (A-042-7 c): sync.json records it, and trying it first means a remote
// that is unreachable from this PC — the LAN name when away from home — costs
// its connect timeout once, not on every step. The rest keep the order the
// user wrote, and a recorded remote that is no longer configured is ignored.
// The result is a new slice; configured is not edited.
func orderedRemotes(root string, configured []string) []string {
	last := readSyncState(root).Remote
	out := make([]string, 0, len(configured))
	found := false
	for _, r := range configured {
		if r == last && last != "" {
			found = true
		}
	}
	if found {
		out = append(out, last)
	}
	for _, r := range configured {
		if !(found && r == last) {
			out = append(out, r)
		}
	}
	return out
}

// syncState is .llmwiki/sync.json (042 D5): per-PC bookkeeping, listed in the
// managed .gitignore so it never travels. It records when this PC last synced
// successfully, with which remote, and why the last step failed, if it did.
type syncState struct {
	LastOK    string `json:"last_ok"`
	Remote    string `json:"remote"`
	LastError string `json:"last_error"`
}

// syncStatePath is where the vault's sync.json lives.
func syncStatePath(root string) string { return filepath.Join(root, stateDirName, "sync.json") }

// readSyncState returns the recorded state; a missing or unreadable file is
// the zero state (never synced).
func readSyncState(root string) syncState {
	var st syncState
	if b, err := os.ReadFile(syncStatePath(root)); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

// recordSync stamps sync.json after a sync step. A success sets last_ok to
// now (UTC, RFC 3339), remote to the one that answered and clears last_error;
// a failure sets last_error and leaves the rest as the last success wrote it.
// It is best effort — the step already did its work, and a bookkeeping file
// that cannot be written is logged, never a reason to fail it.
func recordSync(root, remote string, stepErr error) {
	st := readSyncState(root)
	if stepErr != nil {
		st.LastError = oneLine(stepErr.Error())
	} else {
		st.LastOK = syncNow().UTC().Format(time.RFC3339)
		if remote != "" {
			st.Remote = remote
		}
		st.LastError = ""
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		slog.Warn("sync: encode sync.json", "err", err)
		return
	}
	// Written under .llmwiki/tmp (ignored) and renamed, so a reader never
	// sees half a file and git never sees a stray temp.
	tmpDir := filepath.Join(root, stateDirName, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		slog.Warn("sync: write sync.json", "err", err)
		return
	}
	tmp, err := os.CreateTemp(tmpDir, "sync.json.*")
	if err != nil {
		slog.Warn("sync: write sync.json", "err", err)
		return
	}
	_, werr := tmp.Write(append(b, '\n'))
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp.Name(), 0o644)
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), syncStatePath(root))
	}
	if werr != nil {
		os.Remove(tmp.Name())
		slog.Warn("sync: write sync.json", "err", werr)
	}
}
