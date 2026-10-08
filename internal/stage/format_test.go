// format_test.go pins the two 042 additions to this package: the vault
// format gate (format.go) and the terminal-event hook (terminal.go). lw sync
// moves one vault between PCs through git, so an lw that meets a vault
// written by a newer lw must stop before it writes a byte (TestOpenEngine*),
// and the sync layer needs an after-the-fact signal that a commit or a
// rejection landed (TestOnTerminal*). The fingerprint test that keeps the
// format honest lives in format_fingerprint_test.go.
package stage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/testutil"
)

// writeFormatFile writes llmwikiDir/format with body, creating the directory.
func writeFormatFile(t *testing.T, vaultDir, body string) {
	t.Helper()
	dir := filepath.Join(vaultDir, ".llmwiki")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "format"), []byte(body), 0o644); err != nil {
		t.Fatalf("write format file: %v", err)
	}
}

// treeDigest returns one line per directory and file under root — kind, vault
// relative path, size and content sha — joined and hashed, so two calls
// agree iff no path appeared, vanished or changed in between. Directories
// count: a refused OpenEngine that merely created an empty objects/ has
// still written to the vault.
func treeDigest(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			lines = append(lines, "d "+filepath.ToSlash(rel))
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		lines = append(lines, fmt.Sprintf("f %s %d %s", filepath.ToSlash(rel), len(b), hex.EncodeToString(sum[:])))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// TestReadFormat pins the format file's reading rules (042 D4): a missing
// file is format 1 (every vault written before 042 has none), a well-formed
// one is its version, and anything unreadable or below 1 is an error rather
// than a guess — a damaged format file must not silently open a vault of
// unknown shape.
func TestReadFormat(t *testing.T) {
	tests := []struct {
		name    string
		body    *string // nil = no format file at all
		want    int
		wantErr bool
	}{
		{name: "missing file is format 1", body: nil, want: 1},
		{name: "version 1", body: ptr(`{"version": 1}`), want: 1},
		{name: "version 3", body: ptr(`{"version": 3}`), want: 3},
		{name: "trailing newline", body: ptr("{\"version\": 2}\n"), want: 2},
		{name: "unknown keys are tolerated", body: ptr(`{"version": 2, "note": "x"}`), want: 2},
		{name: "malformed json", body: ptr(`{"version": `), wantErr: true},
		{name: "not json", body: ptr(`version=1`), wantErr: true},
		{name: "empty file", body: ptr(``), wantErr: true},
		{name: "version 0", body: ptr(`{"version": 0}`), wantErr: true},
		{name: "negative version", body: ptr(`{"version": -2}`), wantErr: true},
		{name: "no version key", body: ptr(`{}`), wantErr: true},
		{name: "version is a string", body: ptr(`{"version": "2"}`), wantErr: true},
		{name: "version is fractional", body: ptr(`{"version": 1.5}`), wantErr: true},
		{name: "top level is an array", body: ptr(`[1]`), wantErr: true},
		{name: "text after the object", body: ptr(`{"version": 1} junk`), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), ".llmwiki")
			if tt.body != nil {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "format"), []byte(*tt.body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := ReadFormat(dir)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ReadFormat = %d, nil; want an error", got)
				}
				if !strings.HasPrefix(err.Error(), "read .llmwiki/format: ") {
					t.Errorf("error = %q, want the prefix %q", err, "read .llmwiki/format: ")
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadFormat: %v", err)
			}
			if got != tt.want {
				t.Errorf("ReadFormat = %d, want %d", got, tt.want)
			}
		})
	}

	t.Run("the llmwiki dir itself missing is format 1", func(t *testing.T) {
		got, err := ReadFormat(filepath.Join(t.TempDir(), "no-such", ".llmwiki"))
		if err != nil || got != 1 {
			t.Errorf("ReadFormat = %d, %v; want 1, nil", got, err)
		}
	})
	t.Run("a regular file where .llmwiki should be is format 1", func(t *testing.T) {
		// Not a format problem: OpenEngine reports the broken directory where
		// it first needs one, exactly as it did before 042.
		dir := filepath.Join(t.TempDir(), ".llmwiki")
		if err := os.WriteFile(dir, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := ReadFormat(dir)
		if err != nil || got != 1 {
			t.Errorf("ReadFormat = %d, %v; want 1, nil", got, err)
		}
	})
	t.Run("a directory in the format file's place is an error", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), ".llmwiki")
		if err := os.MkdirAll(filepath.Join(dir, "format"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadFormat(dir); err == nil || !strings.HasPrefix(err.Error(), "read .llmwiki/format: ") {
			t.Errorf("ReadFormat error = %v, want a %q error", err, "read .llmwiki/format: ")
		}
	})
}

func ptr(s string) *string { return &s }

// TestFormatErrorText pins the sentence lw prints for a vault from the
// future, because cmd/lw shows it verbatim.
func TestFormatErrorText(t *testing.T) {
	err := error(&FormatError{Have: 2, Max: 1})
	const want = "vault format 2 is newer than this lw supports (1): upgrade lw"
	if err.Error() != want {
		t.Errorf("FormatError text = %q, want %q", err.Error(), want)
	}
	if FormatVersion != 1 {
		t.Errorf("FormatVersion = %d; 042 ships format 1 — a bump needs a migration and a fingerprint update", FormatVersion)
	}
}

// TestOpenEngineRefusesNewerFormat is the gate itself: a vault whose format
// file names a version this lw does not know is refused with *FormatError,
// and refused BEFORE anything is written — not the recovery pass, not the
// index, not the journal, not even vault.Open's attachment cache. A tree
// digest before and after proves it, on a virgin vault and on one that was
// opened (so it holds a journal and an index a careless open would touch).
func TestOpenEngineRefusesNewerFormat(t *testing.T) {
	t.Run("virgin vault", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		writeFormatFile(t, dir, `{"version": 2}`)
		before := treeDigest(t, dir)

		e, err := OpenEngine(dir)
		if e != nil {
			e.Close()
			t.Fatal("OpenEngine returned an engine for a newer-format vault")
		}
		var fe *FormatError
		if !errors.As(err, &fe) {
			t.Fatalf("OpenEngine error = %T %v, want *FormatError", err, err)
		}
		if fe.Have != 2 || fe.Max != FormatVersion {
			t.Errorf("FormatError = %+v, want Have 2 Max %d", fe, FormatVersion)
		}
		if got, want := err.Error(), "vault format 2 is newer than this lw supports (1): upgrade lw"; got != want {
			t.Errorf("error text = %q, want %q", got, want)
		}
		if _, isBare := err.(*FormatError); !isBare {
			t.Errorf("OpenEngine wrapped the FormatError (%T); cmd/lw prints its text verbatim", err)
		}
		if after := treeDigest(t, dir); after != before {
			t.Error("a refused OpenEngine changed the vault's tree")
		}
		for _, p := range []string{"journal.ndjson", "index.gob", "objects", "snapshots", "changesets", "cache"} {
			if _, statErr := os.Stat(filepath.Join(dir, ".llmwiki", p)); statErr == nil {
				t.Errorf(".llmwiki/%s exists after a refused OpenEngine", p)
			}
		}
	})

	t.Run("previously opened vault", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		e, err := OpenEngine(dir)
		if err != nil {
			t.Fatalf("first OpenEngine: %v", err)
		}
		e.Close()
		writeFormatFile(t, dir, `{"version": 99}`)
		before := treeDigest(t, dir)

		_, err = OpenEngine(dir)
		var fe *FormatError
		if !errors.As(err, &fe) || fe.Have != 99 {
			t.Fatalf("OpenEngine error = %v, want *FormatError{Have: 99}", err)
		}
		if after := treeDigest(t, dir); after != before {
			t.Error("a refused OpenEngine changed an already-initialised vault")
		}
	})

	t.Run("a malformed format file is refused too", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		writeFormatFile(t, dir, `{"version": `)
		before := treeDigest(t, dir)

		_, err := OpenEngine(dir)
		if err == nil || !strings.Contains(err.Error(), "read .llmwiki/format: ") {
			t.Fatalf("OpenEngine error = %v, want one naming %q", err, "read .llmwiki/format: ")
		}
		var fe *FormatError
		if errors.As(err, &fe) {
			t.Errorf("a malformed file produced a *FormatError: %v", err)
		}
		if after := treeDigest(t, dir); after != before {
			t.Error("a refused OpenEngine changed the vault's tree")
		}
	})
}

// TestOpenEngineNoFormatFileUnchanged keeps the gate invisible to every
// vault that predates it, and to lw init: with no format file the engine
// opens as before, writes none while FormatVersion is 1, and an explicit
// {"version": 1} opens too and is left byte-for-byte alone.
func TestOpenEngineNoFormatFileUnchanged(t *testing.T) {
	t.Run("no format file", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		e, err := OpenEngine(dir)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		defer e.Close()

		if _, statErr := os.Stat(filepath.Join(dir, ".llmwiki", "format")); !errors.Is(statErr, fs.ErrNotExist) {
			t.Errorf("OpenEngine left .llmwiki/format behind (stat err %v); lw init's tree must not gain a format file at format 1", statErr)
		}
		// lw init builds its vault with this very OpenEngine call, so the
		// tree it leaves is the one asserted here: the engine's five entries
		// and no format file among them.
		entries, err := os.ReadDir(filepath.Join(dir, ".llmwiki"))
		if err != nil {
			t.Fatal(err)
		}
		have := map[string]bool{}
		for _, ent := range entries {
			have[ent.Name()] = true
		}
		for _, name := range []string{"changesets", "index.gob", "journal.ndjson", "objects", "snapshots"} {
			if !have[name] {
				t.Errorf(".llmwiki/%s is missing after OpenEngine", name)
			}
		}
		if have["format"] {
			t.Error("OpenEngine wrote .llmwiki/format; at format 1 lw init's tree must not gain one")
		}

		// A second open is still idempotent and still writes no format file.
		before := treeDigest(t, dir)
		e2, err := OpenEngine(dir)
		if err != nil {
			t.Fatalf("second OpenEngine: %v", err)
		}
		e2.Close()
		if after := treeDigest(t, dir); after != before {
			t.Error("a second OpenEngine changed the vault's tree")
		}
	})

	t.Run("explicit version 1", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		const body = "{\"version\": 1}\n"
		writeFormatFile(t, dir, body)
		e, err := OpenEngine(dir)
		if err != nil {
			t.Fatalf("OpenEngine with format 1: %v", err)
		}
		e.Close()
		got, err := os.ReadFile(filepath.Join(dir, ".llmwiki", "format"))
		if err != nil || string(got) != body {
			t.Errorf("format file after open = %q, %v; want it untouched (%q)", got, err, body)
		}
	})
}

// goroutineID returns the current goroutine's id — test-only, parsed from
// runtime.Stack's "goroutine N [" header. The hook contract says fn runs on
// the CALLER's goroutine; a go statement between the engine and fn is the
// one mistake only an id comparison can see.
func goroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	fields := strings.Fields(string(buf[:n]))
	if len(fields) < 2 {
		return 0
	}
	id, _ := strconv.ParseUint(fields[1], 10, 64)
	return id
}

// probeNoEngineLock proves that, right now, none of the engine's mutexes and
// not the vault file lock is held: each call below takes one of them (writeMu
// via Refresh, openMu via Current, lifecycleMu and journalStampMu via
// ReloadIfChanged and Close) and must return. A held mutex would block the
// probe forever, so every probe runs on its own goroutine under a deadline —
// a regression then fails the test instead of hanging the package.
//
// The spec's wording is "fn can call e.Status()"; Engine has no Status, so
// this exercises the engine's real verbs instead — see the report.
func probeNoEngineLock(t *testing.T, e *Engine) {
	t.Helper()
	probes := []struct {
		name string
		call func()
	}{
		{"Refresh (writeMu)", func() { _ = e.Refresh() }},
		{"Current (openMu)", func() { _, _ = e.Current() }},
		{"ReloadIfChanged (lifecycleMu, journalStampMu)", func() { _, _ = e.ReloadIfChanged() }},
		{"Close (lifecycleMu)", func() { _ = e.Close() }},
		{"Journal().Query", func() { _, _ = e.Journal().Query(Filter{}) }},
		{"AcquireLock (the vault file lock)", func() {
			release, err := AcquireLock(e.llmwikiDir())
			if err != nil {
				t.Errorf("AcquireLock inside the hook = %v; the commit lock must be released before fn runs", err)
				return
			}
			_ = release()
		}},
	}
	for _, p := range probes {
		done := make(chan struct{})
		go func() {
			defer close(done)
			p.call()
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("%s did not return inside the hook: an engine lock is held while fn runs", p.name)
			return
		}
	}
}

// journalEvents returns the journal events of the given kind.
func journalEvents(t *testing.T, e *Engine, kind EventKind) []Event {
	t.Helper()
	evs, err := e.Journal().Query(Filter{Kinds: []EventKind{kind}})
	if err != nil {
		t.Fatalf("Journal().Query(%s): %v", kind, err)
	}
	return evs
}

// TestOnTerminalCommit pins the commit half of the hook (042 D4): fn runs
// once, with the exact fields, on the caller's goroutine, after commit_end
// is durable and the changeset has moved to committed/, and with no engine
// mutex held — it calls straight back into the engine without deadlock,
// which is what lw sync's push-after-commit does (it takes the vault lock).
func TestOnTerminalCommit(t *testing.T) {
	e, _ := newTestEngine(t)
	cs, err := e.OpenChangeset("hook: commit", testAuthor)
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	stageKVCachePatch(t, e)

	var got []TerminalEvent
	callerGID := goroutineID()
	e.OnTerminal(func(ev TerminalEvent) {
		got = append(got, ev)
		if gid := goroutineID(); gid != callerGID {
			t.Errorf("fn ran on goroutine %d, want the caller's %d", gid, callerGID)
		}
		ends := journalEvents(t, e, EvCommitEnd)
		if len(ends) != 1 || ends[0].Commit != ev.CommitID || ends[0].Changeset != ev.Changeset {
			t.Errorf("at fn time the journal's commit_end events = %+v, want exactly one for %s/%s", ends, ev.Changeset, ev.CommitID)
		}
		if state, serr := e.ChangesetState(ev.Changeset); serr != nil || state != "committed" {
			t.Errorf("at fn time the changeset's state = %q, %v; want committed", state, serr)
		}
		probeNoEngineLock(t, e)
	})

	commitID, err := e.Commit("hook commit message")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	want := TerminalEvent{Kind: "commit", Changeset: cs.ID, CommitID: commitID, Message: "hook commit message"}
	if len(got) != 1 || got[0] != want {
		t.Errorf("fn calls = %+v, want exactly one: %+v", got, want)
	}
}

// TestOnTerminalReject is the reject half: fn runs once after
// changeset_rejected is durable, carrying the reason as Message and no
// commit id, again on the caller's goroutine with no engine lock held.
func TestOnTerminalReject(t *testing.T) {
	e, _ := newTestEngine(t)
	cs, err := e.OpenChangeset("hook: reject", testAuthor)
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	stageKVCachePatch(t, e)

	var got []TerminalEvent
	callerGID := goroutineID()
	e.OnTerminal(func(ev TerminalEvent) {
		got = append(got, ev)
		if gid := goroutineID(); gid != callerGID {
			t.Errorf("fn ran on goroutine %d, want the caller's %d", gid, callerGID)
		}
		rej := journalEvents(t, e, EvChangesetRejected)
		if len(rej) != 1 || rej[0].Changeset != ev.Changeset || rej[0].Message != ev.Message {
			t.Errorf("at fn time the journal's changeset_rejected events = %+v, want exactly one for %s", rej, ev.Changeset)
		}
		if state, serr := e.ChangesetState(ev.Changeset); serr != nil || state != "rejected" {
			t.Errorf("at fn time the changeset's state = %q, %v; want rejected", state, serr)
		}
		probeNoEngineLock(t, e)
	})

	if err := e.Reject("not wanted"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	want := TerminalEvent{Kind: "reject", Changeset: cs.ID, Message: "not wanted"}
	if len(got) != 1 || got[0] != want {
		t.Errorf("fn calls = %+v, want exactly one: %+v", got, want)
	}
}

// TestOnTerminalNotOnFailure: a commit or reject that returns an error did
// not land, so there is nothing to push and fn must stay silent — through
// every refusal the engine's public verbs can produce, then through a commit
// that fails at each of the ten steps after commit_begin.
func TestOnTerminalNotOnFailure(t *testing.T) {
	t.Run("commit with no changeset", func(t *testing.T) {
		e, _ := newTestEngine(t)
		var calls int
		e.OnTerminal(func(TerminalEvent) { calls++ })
		if _, err := e.Commit("nothing open"); !errors.Is(err, ErrNoChangeset) {
			t.Fatalf("Commit error = %v, want ErrNoChangeset", err)
		}
		if calls != 0 {
			t.Errorf("fn called %d times for a failed commit", calls)
		}
	})

	t.Run("commit with nothing live", func(t *testing.T) {
		e, _ := newTestEngine(t)
		var calls int
		e.OnTerminal(func(TerminalEvent) { calls++ })
		if _, err := e.OpenChangeset("empty", testAuthor); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Commit("empty"); !errors.Is(err, ErrNothingToCommit) {
			t.Fatalf("Commit error = %v, want ErrNothingToCommit", err)
		}
		if calls != 0 {
			t.Errorf("fn called %d times for a failed commit", calls)
		}
	})

	t.Run("commit of a stale op", func(t *testing.T) {
		e, dir := newTestEngine(t)
		var calls int
		e.OnTerminal(func(TerminalEvent) { calls++ })
		if _, err := e.OpenChangeset("stale", testAuthor); err != nil {
			t.Fatal(err)
		}
		stageKVCachePatch(t, e)
		kv := filepath.Join(dir, "wiki", "concepts", "kv-cache.md")
		b, err := os.ReadFile(kv)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(kv, append(b, []byte("\nA line someone added behind lw's back.\n")...), 0o644); err != nil {
			t.Fatal(err)
		}
		// Refresh re-hashes against what the engine's vault holds; a real
		// commit is a fresh process that has just loaded it from disk, so
		// this long-lived test engine reloads it itself (TestCommitRefusesStale).
		if err := e.Vault().Reload(); err != nil {
			t.Fatalf("reload vault: %v", err)
		}
		if _, err := e.Commit("stale"); !errors.Is(err, ErrStale) {
			t.Fatalf("Commit error = %v, want ErrStale", err)
		}
		if calls != 0 {
			t.Errorf("fn called %d times for a failed commit", calls)
		}
	})

	t.Run("commit while another process holds the vault lock", func(t *testing.T) {
		e, _ := newTestEngine(t)
		var calls int
		e.OnTerminal(func(TerminalEvent) { calls++ })
		if _, err := e.OpenChangeset("locked", testAuthor); err != nil {
			t.Fatal(err)
		}
		stageKVCachePatch(t, e)
		release, err := AcquireLock(e.llmwikiDir())
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		if _, err := e.Commit("locked"); !errors.Is(err, ErrLocked) {
			t.Fatalf("Commit error = %v, want ErrLocked", err)
		}
		if calls != 0 {
			t.Errorf("fn called %d times for a failed commit", calls)
		}
	})

	t.Run("reject with no changeset", func(t *testing.T) {
		e, _ := newTestEngine(t)
		var calls int
		e.OnTerminal(func(TerminalEvent) { calls++ })
		if err := e.Reject("nothing open"); !errors.Is(err, ErrNoChangeset) {
			t.Fatalf("Reject error = %v, want ErrNoChangeset", err)
		}
		if calls != 0 {
			t.Errorf("fn called %d times for a failed reject", calls)
		}
	})

	t.Run("reject whose rename fails", func(t *testing.T) {
		e, dir := newTestEngine(t)
		var calls int
		e.OnTerminal(func(TerminalEvent) { calls++ })
		if _, err := e.OpenChangeset("doomed", testAuthor); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(dir, ".llmwiki", "changesets", "rejected")); err != nil {
			t.Fatal(err)
		}
		if err := e.Reject("cannot move"); err == nil {
			t.Fatal("Reject succeeded without a rejected/ directory")
		}
		if calls != 0 {
			t.Errorf("fn called %d times for a failed reject", calls)
		}
	})

	// A fault injected after step N is a failure the caller sees as an
	// error, so the hook stays silent even for step 9, where commit_end is
	// already durable — a half-failed commit is the caller's to retry or
	// recover, not an event to push.
	for _, step := range []string{"3", "4", "5", "6", "7", "8", "9"} {
		t.Run("commit faulted after step "+step, func(t *testing.T) {
			e, _ := newTestEngine(t)
			var calls int
			e.OnTerminal(func(TerminalEvent) { calls++ })
			if _, err := e.OpenChangeset("faulted", testAuthor); err != nil {
				t.Fatal(err)
			}
			stageKVCachePatch(t, e)
			injected := errors.New("injected fault after step " + step)
			e.faultAfter = func(s string) error {
				if s == step {
					return injected
				}
				return nil
			}
			if _, err := e.Commit("faulted"); !errors.Is(err, injected) {
				t.Fatalf("Commit error = %v, want the injected fault", err)
			}
			if calls != 0 {
				t.Errorf("fn called %d times for a commit that returned an error", calls)
			}
		})
	}
}

// TestOnTerminalReplaceAndClear: OnTerminal keeps exactly one hook — a new
// fn replaces the old, nil clears — and an engine with no hook commits as it
// always did.
func TestOnTerminalReplaceAndClear(t *testing.T) {
	e, _ := newTestEngine(t)

	// No hook at all: both terminal paths are unaffected.
	if _, err := e.OpenChangeset("no hook", testAuthor); err != nil {
		t.Fatal(err)
	}
	if err := e.Reject("no hook"); err != nil {
		t.Fatalf("Reject without a hook: %v", err)
	}

	var first, second int
	e.OnTerminal(func(TerminalEvent) { first++ })
	e.OnTerminal(func(TerminalEvent) { second++ })
	if _, err := e.OpenChangeset("replaced", testAuthor); err != nil {
		t.Fatal(err)
	}
	if err := e.Reject("replaced"); err != nil {
		t.Fatal(err)
	}
	if first != 0 || second != 1 {
		t.Errorf("after replacing the hook: first=%d second=%d, want 0 and 1", first, second)
	}

	e.OnTerminal(nil)
	if _, err := e.OpenChangeset("cleared", testAuthor); err != nil {
		t.Fatal(err)
	}
	if err := e.Reject("cleared"); err != nil {
		t.Fatal(err)
	}
	if first != 0 || second != 1 {
		t.Errorf("after OnTerminal(nil): first=%d second=%d, want the same 0 and 1", first, second)
	}
}

// TestOnTerminalConcurrentInstall runs OnTerminal against terminal events
// from other goroutines. Under -race (CI runs it) a plain field instead of
// the atomic pointer is a reported data race: the TUI installs the hook
// while its commit pane may already be committing.
func TestOnTerminalConcurrentInstall(t *testing.T) {
	e, _ := newTestEngine(t)
	var fired atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			e.OnTerminal(func(TerminalEvent) { fired.Add(1) })
			e.OnTerminal(nil)
		}
	}()
	for i := 0; i < 20; i++ {
		if _, err := e.OpenChangeset("race", testAuthor); err != nil {
			t.Fatal(err)
		}
		if err := e.Reject("race"); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	if n := fired.Load(); n > 20 {
		t.Errorf("fn fired %d times for 20 rejects", n)
	}
}

// TestOnTerminalPanicIsRecovered: the hook is the sync layer's code, and it
// runs after the commit is durable. A panic in it must not unwind through
// Commit or Reject into a verb or the TUI that would then report a failure
// for work that landed; it is logged and the call returns its normal result.
func TestOnTerminalPanicIsRecovered(t *testing.T) {
	logDir := installFileLog(t)
	e, _ := newTestEngine(t)
	cs, err := e.OpenChangeset("hook: panic", testAuthor)
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	stageKVCachePatch(t, e)

	var calls int
	e.OnTerminal(func(TerminalEvent) {
		calls++
		panic("sync layer blew up")
	})

	commitID, err := e.Commit("panicking hook")
	if err != nil || commitID != "000001" {
		t.Fatalf("Commit = %q, %v; want 000001, nil — a hook panic must not change the result", commitID, err)
	}
	if state, serr := e.ChangesetState(cs.ID); serr != nil || state != "committed" {
		t.Errorf("changeset state = %q, %v; want committed", state, serr)
	}

	if _, err := e.OpenChangeset("hook: panic on reject", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if err := e.Reject("panicking hook"); err != nil {
		t.Fatalf("Reject = %v; want nil — a hook panic must not change the result", err)
	}
	if calls != 2 {
		t.Errorf("hook ran %d times, want once per terminal event (2)", calls)
	}

	log := readLog(t, logDir)
	for _, kind := range []string{"kind=commit", "kind=reject"} {
		var found bool
		for _, line := range strings.Split(log, "\n") {
			if strings.Contains(line, "terminal hook panicked") && strings.Contains(line, kind) && strings.Contains(line, "sync layer blew up") {
				found = true
			}
		}
		if !found {
			t.Errorf("lw.log has no %q record carrying %q and the panic value:\n%s", "terminal hook panicked", kind, log)
		}
	}
}
