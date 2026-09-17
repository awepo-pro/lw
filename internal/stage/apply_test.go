package stage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/vault"
)

// assertNoLwTmpFiles fails t if any *.lw-tmp file remains anywhere under
// root — the temp-file suffix writeFileAtomic uses, which every
// successful (or cleanly faulted, between named steps) Commit call must
// leave behind nowhere.
func assertNoLwTmpFiles(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".lw-tmp") {
			t.Errorf("stray temp file left behind: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}

// TestCommitAppliesAndSnapshots is the end-to-end happy path: Append a
// create_page op, Commit it, and check every one of the ten steps' visible
// effects — the file on disk, the changeset's move to committed/, the
// snapshot, the reloaded Vault/Index, log.md's new entry, and the
// journal's commit_begin/commit_end pair carrying the lint baseline.
func TestCommitAppliesAndSnapshots(t *testing.T) {
	e, dir := newTestEngine(t)

	cs, err := e.OpenChangeset("add a page", testAuthor)
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	const path = "wiki/concepts/new-page.md"
	content := newConceptPageContent("New Page")
	if _, err := e.Append(Op{
		Kind:       OpCreatePage,
		Path:       path,
		Content:    content,
		Rationale:  "test",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	commitID, err := e.Commit("add new-page")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if commitID != "000001" {
		t.Fatalf("commit id = %q, want 000001", commitID)
	}

	got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
	if err != nil {
		t.Fatalf("read committed page: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("committed page content =\n%q\nwant\n%q", got, content)
	}

	assertNoLwTmpFiles(t, dir)

	if _, err := os.Stat(filepath.Join(dir, ".llmwiki", "changesets", "open", cs.ID)); !os.IsNotExist(err) {
		t.Fatalf("changeset %s still under changesets/open/ (stat err = %v)", cs.ID, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".llmwiki", "changesets", "committed", cs.ID, "changeset.json")); err != nil {
		t.Fatalf("changeset %s not found under changesets/committed/: %v", cs.ID, err)
	}

	snap, err := ReadSnapshot(filepath.Join(dir, ".llmwiki", "snapshots"), commitID)
	if err != nil {
		t.Fatalf("ReadSnapshot: %v", err)
	}
	if want, got := sha256Hex(content), snap[path]; got != want {
		t.Fatalf("snapshot[%s] = %q, want %q", path, got, want)
	}
	for _, must := range []string{"SCHEMA.md", "index.md", "log.md", "curator-memory.md", "wiki/concepts/kv-cache.md"} {
		if _, ok := snap[must]; !ok {
			t.Errorf("snapshot is missing %s", must)
		}
	}

	if _, ok := e.Vault().Page(path); !ok {
		t.Fatal("e.Vault() was not reloaded: new page not found (step 7)")
	}

	logBytes, err := os.ReadFile(filepath.Join(dir, "log.md"))
	if err != nil {
		t.Fatalf("read log.md: %v", err)
	}
	wantLine := "- 2026-08-29 12:00 000001 add a page (+1 pages, ~0 edits)"
	if !strings.Contains(string(logBytes), wantLine) {
		t.Fatalf("log.md = %q, want it to contain %q", logBytes, wantLine)
	}

	events, err := e.Journal().Query(Filter{Kinds: []EventKind{EvCommitBegin, EvCommitEnd}})
	if err != nil {
		t.Fatalf("Journal().Query: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("journal has %d commit_begin/commit_end events, want 2", len(events))
	}
	if events[0].Kind != EvCommitBegin || events[1].Kind != EvCommitEnd {
		t.Fatalf("journal order = %v, %v; want commit_begin then commit_end", events[0].Kind, events[1].Kind)
	}
	// commit_end's Data carries the lint baseline (D-AG): parseable, and
	// consistent with a lint.Run over the same committed vault. Not
	// asserted at 0 here — new-page.md carries no index.md entry, so
	// index-sync legitimately reports 1 error; TestRetractLeavesTombstone
	// and TestRenameMovesSourceToTombstones are what pin the 0-errors,
	// 0-warns claim, over scenarios that keep index.md in sync.
	var data struct {
		LintErrors int `json:"lint_errors"`
		LintWarns  int `json:"lint_warns"`
	}
	if err := json.Unmarshal(events[1].Data, &data); err != nil {
		t.Fatalf("commit_end Data does not parse: %v", err)
	}
	wantReport := lint.Run(&lint.Context{Vault: e.Vault(), Index: e.Index(), Graph: e.Vault().Graph()}, nil)
	if data.LintErrors != wantReport.Errors || data.LintWarns != wantReport.Warns {
		t.Fatalf("commit_end Data = {errors:%d warns:%d}, want {errors:%d warns:%d} (a fresh lint.Run over the committed vault)",
			data.LintErrors, data.LintWarns, wantReport.Errors, wantReport.Warns)
	}

	report, err := e.Recover()
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if report.Interrupted {
		t.Fatalf("Recover after a clean commit: Interrupted = true, want false")
	}
}

// TestCommitRefusesStale pins backbone §5.4 step 2: Refresh flips a
// hand-edited op's Before to no longer match, and Commit must refuse with
// ErrStale rather than applying a stale hunk over content it no longer
// describes. This is a clean refusal, not a crash — the lock must be
// released so the caller can retry.
func TestCommitRefusesStale(t *testing.T) {
	e, dir := newTestEngine(t)

	if _, err := e.OpenChangeset("patch kv-cache", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
	if !ok {
		t.Fatal("fixture missing wiki/concepts/kv-cache.md")
	}
	before := page.SHA256()

	rewritten := *page
	rewritten.Body = page.Body + "\nAn appended sentence.\n"
	content := rewritten.Serialize()

	if _, err := e.Append(Op{
		Kind:    OpPatchPage,
		Path:    "wiki/concepts/kv-cache.md",
		Before:  before,
		Content: content,
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Simulate an out-of-band edit (e.g. hand-editing in Obsidian) that
	// changes the file's canonical sha out from under the proposed op. A
	// real lw commit is a fresh process — OpenEngine loads e.vault from
	// disk right before Refresh ever runs — so this long-lived test
	// engine reloads e.vault itself to reproduce that same "Refresh sees
	// the hand-edit" starting condition; Refresh (engine_changeset.go,
	// S2-T2's file) re-hashes against whatever e.vault already holds, it
	// does not reload on its own.
	absPath := filepath.Join(dir, "wiki", "concepts", "kv-cache.md")
	handEdited := append(append([]byte{}, page.Serialize()...), []byte("\nSomeone edited this by hand.\n")...)
	if err := os.WriteFile(absPath, handEdited, 0o644); err != nil {
		t.Fatalf("simulate hand-edit: %v", err)
	}
	if err := e.vault.Reload(); err != nil {
		t.Fatalf("reload vault: %v", err)
	}

	if _, err := e.Commit("should refuse"); !errors.Is(err, ErrStale) {
		t.Fatalf("Commit: got %v, want ErrStale", err)
	}

	// A clean refusal must release the lock, unlike a simulated crash.
	release, err := AcquireLock(filepath.Join(dir, ".llmwiki"))
	if err != nil {
		t.Fatalf("lock was not released after ErrStale: %v", err)
	}
	release()

	// Nothing was written to the vault, and the changeset is still open.
	if _, err := os.Stat(filepath.Join(dir, ".llmwiki", "changesets", "committed")); err == nil {
		entries, _ := os.ReadDir(filepath.Join(dir, ".llmwiki", "changesets", "committed"))
		if len(entries) != 0 {
			t.Fatalf("changesets/committed/ is non-empty after a refused commit: %v", entries)
		}
	}
}

// TestCrashAfterStep is the crash-safety deliverable: for a crash
// (simulated via e.faultAfter) after each of backbone §5.4's steps 3
// through 9, Recover must describe exactly what was left behind, and no
// vault file may ever be half-written.
func TestCrashAfterStep(t *testing.T) {
	for _, step := range []string{"3", "4", "5", "6", "7", "8", "9"} {
		t.Run(step, func(t *testing.T) {
			e, dir := newTestEngine(t)

			cs, err := e.OpenChangeset("add a page", testAuthor)
			if err != nil {
				t.Fatalf("OpenChangeset: %v", err)
			}

			path := fmt.Sprintf("wiki/concepts/crash-%s.md", step)
			if _, err := e.Append(Op{
				Kind:       OpCreatePage,
				Path:       path,
				Content:    newConceptPageContent("Crash " + step),
				Rationale:  "test",
				Provenance: []string{"raw/papers/leviathan-2023.md"},
			}); err != nil {
				t.Fatalf("Append: %v", err)
			}

			injected := errors.New("simulated crash after step " + step)
			e.faultAfter = func(s string) error {
				if s == step {
					return injected
				}
				return nil
			}

			if _, err := e.Commit("crash test"); !errors.Is(err, injected) {
				t.Fatalf("Commit: got %v, want it to wrap the injected fault", err)
			}

			assertNoLwTmpFiles(t, dir)

			// A-804 (F-806-2): a commit that RETURNS an error releases the
			// lock — a returned error is a lived-through failure, not a
			// crash, and holding it would wedge a long-lived caller forever.
			// A real crash never runs this code at all; its stale on-disk
			// lock is cleared by AcquireLock's liveness check or
			// lw doctor --unlock.
			unlock, lockErr := AcquireLock(filepath.Join(dir, ".llmwiki"))
			if errors.Is(lockErr, ErrLocked) {
				t.Errorf("lock after a faulted commit: AcquireLock = ErrLocked, want the lock released (A-804)")
			} else if lockErr != nil {
				t.Errorf("lock after a faulted commit: AcquireLock err = %v", lockErr)
			} else {
				unlock()
			}

			absPath := filepath.Join(dir, filepath.FromSlash(path))
			raw, statErr := os.ReadFile(absPath)
			written := statErr == nil
			if !written && !os.IsNotExist(statErr) {
				t.Fatalf("stat/read %s: %v", path, statErr)
			}
			if written {
				if _, err := vault.ParsePage(path, raw); err != nil {
					t.Fatalf("%s exists but does not parse as a complete page (partial write): %v", path, err)
				}
			}

			report, err := e.Recover()
			if err != nil {
				t.Fatalf("Recover: %v", err)
			}

			if step == "9" {
				// Step 9 journals commit_end AND moves the changeset
				// before this fault fires — the commit is fully,
				// durably complete in every respect except releasing
				// the lock (step 10), so Recover must report a
				// resolved (non-interrupted) history: the backbone's
				// "rest of the struct is zero" case.
				if report.Interrupted {
					t.Fatalf("Recover after a full step 9: Interrupted = true, want false (commit_end was already journaled)")
				}
				if report.Commit != "" || len(report.Applied) != 0 || len(report.Pending) != 0 || report.Fixable {
					t.Fatalf("Recover after a full step 9 = %+v, want the zero value", report)
				}
				if !written {
					t.Fatalf("target page missing even though commit_end was journaled")
				}
				if _, err := os.Stat(filepath.Join(dir, ".llmwiki", "changesets", "committed", cs.ID, "changeset.json")); err != nil {
					t.Fatalf("changeset not moved to committed/ despite step 9 completing: %v", err)
				}
				return
			}

			if !report.Interrupted {
				t.Fatalf("Recover: Interrupted = false, want true")
			}
			if report.Commit != "000001" {
				t.Fatalf("Recover: Commit = %q, want 000001", report.Commit)
			}
			if !report.Fixable {
				t.Fatalf("Recover: Fixable = false, want true (the target's content was already in the CAS from Append time)")
			}

			switch step {
			case "3", "4":
				if written {
					t.Fatalf("target page was written on disk before step 5 ran")
				}
				if len(report.Pending) != 1 || report.Pending[0] != path {
					t.Fatalf("Pending = %v, want [%s]", report.Pending, path)
				}
				if len(report.Applied) != 0 {
					t.Fatalf("Applied = %v, want none", report.Applied)
				}
			default: // "5", "6", "7", "8"
				if !written {
					t.Fatalf("target page missing after step %s", step)
				}
				if len(report.Applied) != 1 || report.Applied[0] != path {
					t.Fatalf("Applied = %v, want [%s]", report.Applied, path)
				}
				if len(report.Pending) != 0 {
					t.Fatalf("Pending = %v, want none", report.Pending)
				}
			}
		})
	}
}

// TestCrashAfterStep4MakesRetractFixable exercises D-BN's actual point for
// the hard case it exists for: a retract's tombstone AND its original
// pre-image are both stored in the CAS in step 4, before step 5 ever
// touches disk — so a crash immediately after step 4 must already report
// Fixable = true, not merely "eventually, once the write completes".
func TestCrashAfterStep4MakesRetractFixable(t *testing.T) {
	e, dir := newTestEngine(t)

	if _, err := e.OpenChangeset("retract kv-cache", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if _, err := e.Append(Op{
		Kind:      OpRetract,
		Path:      "wiki/concepts/kv-cache.md",
		Rationale: "obsolete",
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	injected := errors.New("simulated crash after step 4")
	e.faultAfter = func(step string) error {
		if step == "4" {
			return injected
		}
		return nil
	}

	if _, err := e.Commit("retract kv-cache"); !errors.Is(err, injected) {
		t.Fatalf("Commit: got %v, want it to wrap the injected fault", err)
	}

	report, err := e.Recover()
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if !report.Interrupted {
		t.Fatal("Recover: Interrupted = false, want true")
	}
	if len(report.Pending) != 1 || report.Pending[0] != "wiki/concepts/kv-cache.md" {
		t.Fatalf("Pending = %v, want [wiki/concepts/kv-cache.md]", report.Pending)
	}
	if !report.Fixable {
		t.Fatal("Fixable = false, want true — step 4 already stored both the tombstone and the original pre-image (D-BN)")
	}

	// step 5 never ran: the original page must still be exactly intact.
	b, err := os.ReadFile(filepath.Join(dir, "wiki", "concepts", "kv-cache.md"))
	if err != nil {
		t.Fatalf("read kv-cache.md: %v", err)
	}
	if _, err := vault.ParsePage("wiki/concepts/kv-cache.md", b); err != nil {
		t.Fatalf("kv-cache.md does not parse as a complete page after the crash: %v", err)
	}
}

// TestCrashAfterStepMakesSplitPageRecoverable is R1's regression test.
//
// Before this repair, collectRecoverTargets treated a split_page source as
// move: true — the same shape rename_page/merge_pages sources use — which
// resolveRecoverTarget's move branch reports "applied" only when the path
// is os.Lstat-absent. But S2-T8 rule (c) stopped moving a split source to
// tombstones/ and started writing a disambiguation stub in place, so that
// path is never absent again: an interrupted split commit left the source
// permanently Pending, with Fixable computed against the ORIGINAL page's
// sha (SourceSHAs[0]) rather than the stub Commit actually needs to finish
// writing — a vault that never fully recovers, since Recover gates
// OpenChangeset in the step-9 Unmoved window (D-BP).
//
// Proves the fix at two crash windows, the same fault-injection machinery
// TestCrashAfterStep and TestCrashAfterStep4MakesRetractFixable use:
//
//   - after step 4 (before step 5 writes anything): the source still holds
//     its ORIGINAL content, must be Pending, and — mirroring D-BN's point
//     for retract — must ALREADY be Fixable, because step 4 stored the
//     stub's bytes in the CAS before step 5 ever ran. Fixable here can only
//     be true if it is being checked against the stub's sha, not the
//     original page's (which was never the question — the original was
//     already durable at Append time).
//   - after step 5 (the stub is already on disk): the source must be
//     Applied, not stuck in Pending — the exact "permanently Pending" bug
//     this repair fixes. Before the fix this crash point looked identical
//     to every other one: Lstat found the path present and reported
//     applied=false regardless of what was actually on disk.
func TestCrashAfterStepMakesSplitPageRecoverable(t *testing.T) {
	const source = "wiki/concepts/kv-cache.md"
	products := []string{"wiki/concepts/kv-cache-part-a.md", "wiki/concepts/kv-cache-part-b.md"}

	for _, step := range []string{"4", "5"} {
		t.Run(step, func(t *testing.T) {
			e, dir := newTestEngine(t)

			if _, err := e.OpenChangeset("split kv-cache", testAuthor); err != nil {
				t.Fatalf("OpenChangeset: %v", err)
			}
			if _, err := e.Append(Op{Kind: OpSplitPage, Path: source, Sources: products}); err != nil {
				t.Fatalf("Append split_page: %v", err)
			}
			for _, p := range products {
				if _, err := e.Append(Op{
					Kind:       OpCreatePage,
					Path:       p,
					Content:    newConceptPageContent("Part of KV Cache"),
					Rationale:  "test",
					Provenance: []string{"raw/papers/leviathan-2023.md"},
				}); err != nil {
					t.Fatalf("Append create_page(%s): %v", p, err)
				}
			}

			injected := errors.New("simulated crash after step " + step)
			e.faultAfter = func(s string) error {
				if s == step {
					return injected
				}
				return nil
			}

			if _, err := e.Commit("split kv-cache"); !errors.Is(err, injected) {
				t.Fatalf("Commit: got %v, want it to wrap the injected fault", err)
			}

			report, err := e.Recover()
			if err != nil {
				t.Fatalf("Recover: %v", err)
			}
			if !report.Interrupted {
				t.Fatal("Recover: Interrupted = false, want true")
			}

			b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(source)))
			if err != nil {
				t.Fatalf("read %s: %v", source, err)
			}

			switch step {
			case "4":
				if !strings.Contains(string(b), "kv_cache_hit_rate") {
					t.Fatalf("%s was written before step 5 ran (crash-after-4 should leave the ORIGINAL content in place)", source)
				}
				if !containsString(report.Pending, source) {
					t.Fatalf("Pending = %v, want it to contain %s", report.Pending, source)
				}
				if !report.Fixable {
					t.Fatal("Fixable = false, want true — step 4 already stored the stub's bytes in the CAS (D-BN), same as retract")
				}

				// Fixable must be true FOR THE RIGHT REASON: the CAS
				// holds the STUB's sha specifically (splitStub of the
				// still-original page b, which is what resolveRecoverTarget
				// re-derives and checks), not merely true because some
				// other, unrelated sha happens to already be stored.
				page, err := vault.ParsePage(source, b)
				if err != nil {
					t.Fatalf("parse %s: %v", source, err)
				}
				wantSHA := sha256Hex(splitStub(page, products, e.now().UTC().Format("2006-01-02")))
				if !e.store.Has(wantSHA) {
					t.Fatalf("CAS does not hold %s, the stub's own sha — Fixable cannot legitimately be true against it", wantSHA)
				}
			case "5":
				if !strings.Contains(string(b), "> **Split.**") {
					t.Fatalf("%s does not hold the stub after step 5", source)
				}
				if containsString(report.Pending, source) {
					t.Fatalf("Pending = %v, want it to NOT contain %s — the stub is already on disk, this is the permanently-Pending bug", report.Pending, source)
				}
				if !containsString(report.Applied, source) {
					t.Fatalf("Applied = %v, want it to contain %s", report.Applied, source)
				}
			}
		})
	}
}

// TestRecoverNoCommits proves the zero-history case: a vault that has
// never committed anything reports a non-interrupted, all-zero
// RecoveryReport.
func TestRecoverNoCommits(t *testing.T) {
	e, _ := newTestEngine(t)

	report, err := e.Recover()
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if report.Interrupted || report.Commit != "" || len(report.Applied) != 0 || len(report.Pending) != 0 || report.Fixable {
		t.Fatalf("Recover on a vault with no commits = %+v, want the zero value", report)
	}
}

// TestRecoverReportsUnmovedChangeset pins backbone §5.4's Unmoved Contract
// (MASTER §9 D-BP): step 9 journals commit_end and only then os.Renames
// changesets/open/<id> into committed/, so a crash between those two
// syscalls leaves a commit that is complete in every observable way while
// the changeset directory never left open/. Interrupted stays false — the
// last commit_begin does have a matching commit_end — so Unmoved is the
// only place that state is reportable at all.
//
// The crash state is reproduced exactly as the orchestrator did: commit
// normally, then recreate changesets/open/<id>/changeset.json from the
// committed copy (never through the engine), reopen the engine, and call
// Recover — a fresh process, the same way lw doctor would encounter it.
func TestRecoverReportsUnmovedChangeset(t *testing.T) {
	e, dir := newTestEngine(t)

	cs, err := e.OpenChangeset("add a page", testAuthor)
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if _, err := e.Append(Op{
		Kind:       OpCreatePage,
		Path:       "wiki/concepts/new-page.md",
		Content:    newConceptPageContent("New Page"),
		Rationale:  "test",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := e.Commit("add new-page"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	t.Run("normal completed commit leaves Unmoved empty", func(t *testing.T) {
		e2, err := OpenEngine(dir)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		defer e2.Close()

		report, err := e2.Recover()
		if err != nil {
			t.Fatalf("Recover: %v", err)
		}
		if report.Interrupted {
			t.Errorf("Interrupted = true, want false")
		}
		if report.Unmoved != "" {
			t.Errorf("Unmoved = %q, want \"\" after a fully completed commit", report.Unmoved)
		}
	})

	t.Run("changeset still in open/ after a completed commit_end", func(t *testing.T) {
		committedJSON := filepath.Join(dir, ".llmwiki", "changesets", "committed", cs.ID, "changeset.json")
		b, err := os.ReadFile(committedJSON)
		if err != nil {
			t.Fatalf("read committed changeset.json: %v", err)
		}

		openDir := filepath.Join(dir, ".llmwiki", "changesets", "open", cs.ID)
		if err := os.MkdirAll(openDir, 0o755); err != nil {
			t.Fatalf("recreate open dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(openDir, "changeset.json"), b, 0o644); err != nil {
			t.Fatalf("recreate changeset.json: %v", err)
		}

		e2, err := OpenEngine(dir)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		defer e2.Close()

		report, err := e2.Recover()
		if err != nil {
			t.Fatalf("Recover: %v", err)
		}
		if report.Interrupted {
			t.Errorf("Interrupted = true, want false — the last commit_begin has a matching commit_end")
		}
		if report.Unmoved != cs.ID {
			t.Errorf("Unmoved = %q, want %q", report.Unmoved, cs.ID)
		}
		if report.Commit != "" || len(report.Applied) != 0 || len(report.Pending) != 0 || report.Fixable {
			t.Errorf("Recover = %+v, want only Unmoved set", report)
		}

		// Recover only reports; it must never perform the move itself.
		if _, err := os.Stat(openDir); err != nil {
			t.Errorf("open dir %s no longer present after Recover (err=%v); Recover must never write to the vault", openDir, err)
		}
	})
}

// TestRetractLeavesTombstone pins backbone §5.4's retract-tombstone
// Contract (MASTER §9 D-BL): the original frontmatter survives unchanged
// except sources is dropped and retracted: <date> is added, the body
// opens with the title and a Retracted block, the original ## Related
// section is copied verbatim, and — the assertion nothing else makes —
// the committed vault lints at 0 errors AND 0 warns.
func TestRetractLeavesTombstone(t *testing.T) {
	e, dir := newTestEngine(t)

	orig, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
	if !ok {
		t.Fatal("fixture missing wiki/concepts/kv-cache.md")
	}
	origRelated, hasRelated := orig.Section("## Related")
	if !hasRelated {
		t.Fatal("test assumption broken: kv-cache.md has no ## Related section")
	}
	wantRelated := orig.Body[origRelated.Start:origRelated.End]
	origCreated := orig.FM.Created.String()
	origType := orig.FM.Type

	if _, err := e.OpenChangeset("retract kv-cache", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if _, err := e.Append(Op{
		Kind:      OpRetract,
		Path:      "wiki/concepts/kv-cache.md",
		Rationale: "superseded by a newer writeup",
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	commitID, err := e.Commit("retract kv-cache")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if commitID != "000001" {
		t.Fatalf("commit id = %q, want 000001", commitID)
	}

	b, err := os.ReadFile(filepath.Join(dir, "wiki", "concepts", "kv-cache.md"))
	if err != nil {
		t.Fatalf("read tombstone: %v", err)
	}
	tomb, err := vault.ParsePage("wiki/concepts/kv-cache.md", b)
	if err != nil {
		t.Fatalf("tombstone does not parse: %v", err)
	}

	if tomb.FM.Title != orig.FM.Title {
		t.Errorf("tombstone title = %q, want %q", tomb.FM.Title, orig.FM.Title)
	}
	if tomb.FM.Type != origType {
		t.Errorf("tombstone type = %q, want %q (never \"retracted\")", tomb.FM.Type, origType)
	}
	if tomb.FM.Created.String() != origCreated {
		t.Errorf("tombstone created = %q, want %q", tomb.FM.Created.String(), origCreated)
	}
	if tomb.FM.Sources != nil {
		t.Errorf("tombstone sources = %v, want dropped (nil)", tomb.FM.Sources)
	}
	if got := tomb.FM.Extra["retracted"]; got != "2026-08-29" {
		t.Errorf("tombstone retracted date = %q, want 2026-08-29 (FixedClock)", got)
	}
	wantOpen := "# KV Cache\n\n> **Retracted.** superseded by a newer writeup\n"
	if !strings.HasPrefix(tomb.Body, wantOpen) {
		t.Errorf("tombstone body does not open with %q:\n%s", wantOpen, tomb.Body)
	}
	if !strings.Contains(tomb.Body, wantRelated) {
		t.Errorf("tombstone body does not carry the original ## Related section verbatim")
	}

	report := lint.Run(&lint.Context{Vault: e.Vault(), Index: e.Index(), Graph: e.Vault().Graph()}, nil)
	if report.Errors != 0 || report.Warns != 0 {
		t.Fatalf("lint over the committed vault: errors=%d warns=%d, want 0/0\nfindings: %+v", report.Errors, report.Warns, report.Findings)
	}
}

// TestRenameMovesSourceToTombstones pins backbone §5.4's rename/merge
// disposal Contract (MASTER §9 D-BM): the source is os.Rename'd into
// .llmwiki/tombstones/<commitID>/, never left page-shaped in the vault,
// every inbound backlink (including index.md's) is cascade-rewritten, and
// the committed vault lints at 0 errors AND 0 warns.
func TestRenameMovesSourceToTombstones(t *testing.T) {
	e, dir := newTestEngine(t)

	const from = "wiki/concepts/kv-cache.md"
	const to = "wiki/concepts/kv-caching.md"

	orig, ok := e.Vault().Page(from)
	if !ok {
		t.Fatal("fixture missing wiki/concepts/kv-cache.md")
	}
	origBytes := orig.Serialize()

	if _, err := e.OpenChangeset("rename kv-cache", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if _, err := e.Append(Op{Kind: OpRenamePage, From: from, To: to}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	commitID, err := e.Commit("rename kv-cache")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(from))); !os.IsNotExist(err) {
		t.Fatalf("source %s still present in the vault (stat err = %v), want it moved out", from, err)
	}

	gotTo, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(to)))
	if err != nil {
		t.Fatalf("read renamed page: %v", err)
	}
	if string(gotTo) != string(origBytes) {
		t.Fatalf("renamed page content changed:\ngot:  %q\nwant: %q", gotTo, origBytes)
	}

	tombPath := filepath.Join(dir, ".llmwiki", "tombstones", commitID, filepath.FromSlash(from))
	gotTomb, err := os.ReadFile(tombPath)
	if err != nil {
		t.Fatalf("read tombstone at %s: %v", tombPath, err)
	}
	if string(gotTomb) != string(origBytes) {
		t.Fatalf("tombstoned source content changed:\ngot:  %q\nwant: %q", gotTomb, origBytes)
	}

	for _, p := range []string{"wiki/concepts/flash-attention.md", "wiki/concepts/speculative-decoding.md"} {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		if strings.Contains(string(b), "[[kv-cache]]") {
			t.Errorf("%s still links to [[kv-cache]] after the rename", p)
		}
		if !strings.Contains(string(b), "kv-caching") {
			t.Errorf("%s does not link to the renamed page", p)
		}
	}
	idx, err := os.ReadFile(filepath.Join(dir, "index.md"))
	if err != nil {
		t.Fatalf("read index.md: %v", err)
	}
	if strings.Contains(string(idx), "[[kv-cache]]") {
		t.Errorf("index.md still links to [[kv-cache]] after the rename")
	}
	if !strings.Contains(string(idx), "kv-caching") {
		t.Errorf("index.md does not link to the renamed page")
	}

	report := lint.Run(&lint.Context{Vault: e.Vault(), Index: e.Index(), Graph: e.Vault().Graph()}, nil)
	if report.Errors != 0 || report.Warns != 0 {
		t.Fatalf("lint over the committed vault: errors=%d warns=%d, want 0/0\nfindings: %+v", report.Errors, report.Warns, report.Findings)
	}
}

// TestLogRotate pins backbone §5.4 step 8's rotation boundary against
// internal/lint/check_log_rotate.go, which is what judges it in
// production: a log.md already at the threshold rotates its entire
// contents (seed entries plus the newly appended one) to
// log-<year>.md, leaving log.md with zero entries — so the check never
// fires on the file this method just wrote.
func TestLogRotate(t *testing.T) {
	e, dir := newTestEngine(t)

	var seed strings.Builder
	seed.WriteString("# Log\n\n")
	for i := 1; i <= logRotateThreshold; i++ {
		fmt.Fprintf(&seed, "- 2020-01-01 00:00 000000 seed entry %d (+0 pages, ~0 edits)\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "log.md"), []byte(seed.String()), 0o644); err != nil {
		t.Fatalf("seed log.md: %v", err)
	}

	if _, err := e.OpenChangeset("tip it over", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if _, err := e.Append(Op{
		Kind:       OpCreatePage,
		Path:       "wiki/concepts/rotate-me.md",
		Content:    newConceptPageContent("Rotate Me"),
		Rationale:  "test",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := e.Commit("one more entry"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	freshLog, err := os.ReadFile(filepath.Join(dir, "log.md"))
	if err != nil {
		t.Fatalf("read log.md: %v", err)
	}
	if string(freshLog) != "# Log\n\n" {
		t.Fatalf("post-rotation log.md = %q, want just the header", freshLog)
	}

	archived, err := os.ReadFile(filepath.Join(dir, "log-2020.md"))
	if err != nil {
		t.Fatalf("read log-2020.md: %v", err)
	}
	if got, want := countLogEntries(string(archived)), logRotateThreshold+1; got != want {
		t.Fatalf("log-2020.md has %d entries, want %d (the %d seeded plus the new one)", got, want, logRotateThreshold)
	}

	report := lint.Run(&lint.Context{Vault: e.Vault(), Index: e.Index(), Graph: e.Vault().Graph()}, []string{"log-rotate"})
	if len(report.Findings) != 0 {
		t.Fatalf("log-rotate over the rotated log.md: findings = %+v, want none", report.Findings)
	}
}

// countLogEntries counts lines beginning with "- ", the same definition
// internal/lint/check_log_rotate.go uses.
func countLogEntries(content string) int {
	n := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "- ") {
			n++
		}
	}
	return n
}

// TestCommitWritesBaselineSnapshot locks MASTER §9 D-BU (§10 OR-9): the
// first Commit captures the PRE-commit tree as snapshots/000000.tree, so
// that Revert("000001") — the one revert gate G2 runs — has the
// predecessor backbone §5.8 tells it to diff against.
//
// Without the fix nothing ever writes 000000.tree (nextCommitID yields
// "000001" on an empty snapshots/ and Commit's step 6 writes only its own
// id), so Revert of the first commit has nothing to compare and the M2
// thesis cannot be demonstrated end to end.
func TestCommitWritesBaselineSnapshot(t *testing.T) {
	e, dir := newTestEngine(t)
	snapshotsDir := filepath.Join(dir, ".llmwiki", "snapshots")

	// The tree as it stands before any commit — what 000000.tree must hold.
	wantBaseline, err := e.buildSnapshot()
	if err != nil {
		t.Fatalf("buildSnapshot: %v", err)
	}

	newPath := "wiki/concepts/baseline-probe.md"
	if _, err := e.OpenChangeset("baseline", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	if _, err := e.Append(Op{
		Kind: OpCreatePage, Path: newPath,
		Content:   newConceptPageContent("Baseline Probe"),
		Rationale: "lock D-BU", Provenance: []string{"raw/baseline.md"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	commitID, err := e.Commit("baseline")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if commitID != "000001" {
		t.Fatalf("commit id = %q, want 000001 — the baseline must not consume an id", commitID)
	}

	// 1. The baseline exists and is readable through the public wrapper.
	got, err := e.Snapshot("000000")
	if err != nil {
		t.Fatalf("Snapshot(000000): %v — D-BU's baseline was not written", err)
	}

	// 2. It is the PRE-commit tree, byte for byte.
	if len(got) != len(wantBaseline) {
		t.Fatalf("baseline has %d entries, want %d", len(got), len(wantBaseline))
	}
	for p, sha := range wantBaseline {
		if got[p] != sha {
			t.Errorf("baseline[%q] = %q, want %q", p, got[p], sha)
		}
	}
	if _, ok := got[newPath]; ok {
		t.Errorf("baseline contains %q, but that page did not exist before the commit", newPath)
	}

	// 3. The delta against it is exactly this commit's own effect — which
	//    is the whole point: Revert(000001) must not propose retracting the
	//    pre-existing vault.
	cur, err := e.Snapshot("000001")
	if err != nil {
		t.Fatalf("Snapshot(000001): %v", err)
	}
	var added []string
	for p := range cur {
		if _, ok := got[p]; !ok {
			added = append(added, p)
		}
	}
	if len(added) != 1 || added[0] != newPath {
		t.Errorf("added paths = %v, want exactly [%s]", added, newPath)
	}

	// 4. A second commit still gets 000002, and must NOT rewrite the
	//    baseline or its own predecessor's recorded snapshot.
	before000001, err := os.ReadFile(filepath.Join(snapshotsDir, "000001.tree"))
	if err != nil {
		t.Fatalf("read 000001.tree: %v", err)
	}
	if _, err := e.OpenChangeset("second", testAuthor); err != nil {
		t.Fatalf("OpenChangeset 2: %v", err)
	}
	if _, err := e.Append(Op{
		Kind: OpCreatePage, Path: "wiki/concepts/baseline-probe-two.md",
		Content:   newConceptPageContent("Baseline Probe Two"),
		Rationale: "lock D-BU", Provenance: []string{"raw/baseline.md"},
	}); err != nil {
		t.Fatalf("Append 2: %v", err)
	}
	id2, err := e.Commit("second")
	if err != nil {
		t.Fatalf("Commit 2: %v", err)
	}
	if id2 != "000002" {
		t.Fatalf("second commit id = %q, want 000002", id2)
	}
	after000001, err := os.ReadFile(filepath.Join(snapshotsDir, "000001.tree"))
	if err != nil {
		t.Fatalf("re-read 000001.tree: %v", err)
	}
	if string(before000001) != string(after000001) {
		t.Error("the second commit rewrote snapshots/000001.tree — a recorded snapshot is history and must never be overwritten")
	}
}

// TestPredecessorCommitID locks the arithmetic Revert names its
// predecessor snapshot with (MASTER §9 D-BU).
func TestPredecessorCommitID(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "000002", want: "000001"},
		{in: "000001", want: "000000"},
		{in: "000042", want: "000041"},
		{in: "000100", want: "000099"},
		{in: "000000", wantErr: true},
		{in: "1", wantErr: true},
		{in: "", wantErr: true},
		{in: "00001a", wantErr: true},
		{in: "000001.tree", wantErr: true},
	} {
		got, err := predecessorCommitID(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("predecessorCommitID(%q) = %q, want an error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("predecessorCommitID(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("predecessorCommitID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestCommitRefusesAnAlreadyCommittedID pins MASTER §9 D-CI Part B2, the
// Commit backstop at step 2a: if something creates
// changesets/committed/<id> for the currently open changeset's own id
// AFTER OpenChangeset already drew it — the residual collision B1 cannot
// retroactively prevent — Commit must refuse with ErrIDCollision before
// step 3's commit_begin and before step 5 writes a single byte, and it
// must release the lock it took at step 1 rather than leak it.
func TestCommitRefusesAnAlreadyCommittedID(t *testing.T) {
	e, dir := newTestEngine(t)

	c, err := e.OpenChangeset("collide at commit", testAuthor)
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	const path = "wiki/concepts/collision-page.md"
	content := newConceptPageContent("Collision Page")
	if _, err := e.Append(Op{
		Kind:       OpCreatePage,
		Path:       path,
		Content:    content,
		Rationale:  "test",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Simulate the residual collision: changesets/committed/<c.ID> now
	// exists, even though OpenChangeset's own B1 defence found it free at
	// draw time (e.g. two processes racing on the same clock+entropy).
	collidingDir := filepath.Join(dir, ".llmwiki", "changesets", "committed", c.ID)
	if err := os.MkdirAll(collidingDir, 0o755); err != nil {
		t.Fatalf("seed colliding committed dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(collidingDir, "changeset.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("seed colliding changeset.json: %v", err)
	}

	logBefore, _ := os.ReadFile(filepath.Join(dir, "log.md"))
	snapshotsDir := filepath.Join(dir, ".llmwiki", "snapshots")
	entriesBefore, err := os.ReadDir(snapshotsDir)
	if err != nil {
		t.Fatalf("read snapshots/ before Commit: %v", err)
	}

	if _, err := e.Commit("attempt a colliding commit"); !errors.Is(err, ErrIDCollision) {
		t.Fatalf("Commit = %v, want ErrIDCollision", err)
	}

	// The vault must be byte-unchanged: the target page was never written
	// (step 5 never ran), log.md was never appended to (step 8 never ran),
	// and no snapshot was written (step 4a/6 never ran).
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(path))); !os.IsNotExist(err) {
		t.Fatalf("Commit wrote the target page despite refusing before step 5 (stat err = %v)", err)
	}
	logAfter, _ := os.ReadFile(filepath.Join(dir, "log.md"))
	if string(logAfter) != string(logBefore) {
		t.Fatalf("log.md changed despite the pre-mutation refusal:\nbefore: %q\nafter:  %q", logBefore, logAfter)
	}
	entriesAfter, err := os.ReadDir(snapshotsDir)
	if err != nil {
		t.Fatalf("read snapshots/ after Commit: %v", err)
	}
	if len(entriesAfter) != len(entriesBefore) {
		t.Fatalf("a snapshot was written despite the pre-mutation refusal: before=%d after=%d", len(entriesBefore), len(entriesAfter))
	}

	// No commit_begin was journalled — the whole point of placing the
	// guard before step 3, not at step 9 where the collision used to
	// surface.
	events, err := e.Journal().Query(Filter{Kinds: []EventKind{EvCommitBegin}})
	if err != nil {
		t.Fatalf("Journal().Query: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("commit_begin was journalled despite the pre-mutation refusal: %+v", events)
	}

	// The lock taken at step 1 must have been released, not leaked: a
	// fresh AcquireLock on the same vault must succeed.
	release, err := AcquireLock(e.llmwikiDir())
	if err != nil {
		t.Fatalf("AcquireLock after Commit returned ErrIDCollision: %v (the lock was leaked)", err)
	}
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}
}
