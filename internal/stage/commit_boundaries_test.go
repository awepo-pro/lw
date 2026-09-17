// commit_boundaries_test.go — the commit-lifecycle boundaries of the
// single-writer model (008 contract §11, amendment A-804; MASTER §5
// R-807). The R-806 final review's three probes made permanent:
// TestZZR806A1ForeignCommitMidVerbResurrects, TestZZR806A2-
// FailedCommitLeaksLockKillsReload and TestZZR806A3UnlockAndForceNext-
// RaceDetector, plus the review's TOCTOU requirement that a reload never
// runs while a commit is between its lock and its end.
package stage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestCommitBoundaries(t *testing.T) {
	t.Run("foreign commit mid verb is refused", func(t *testing.T) {
		e, dir := newTestEngine(t)
		if _, err := e.OpenChangeset("r807 a1", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		stageIngest(t, e, "raw/notes/a1.md", "a1 body.\n")

		// The writer is mid-verb: past the coherence check, holding the
		// private copy it is about to persist.
		private, err := e.writerOpen()
		if err != nil {
			t.Fatalf("writerOpen: %v", err)
		}

		// A foreign process commits the changeset and opens the next one
		// inside the writer's window.
		fe := foreignEngine(t, dir)
		if _, err := fe.Commit("foreign commit mid-verb"); err != nil {
			t.Fatalf("foreign Commit: %v", err)
		}
		if _, err := fe.OpenChangeset("foreign next turn", testAuthor); err != nil {
			t.Fatalf("foreign OpenChangeset: %v", err)
		}

		// The writer's persist tail must fail closed, not resurrect the
		// committed directory under open/ (A-804, F-806-1).
		err = e.persistAndPublish(private)
		if err == nil {
			t.Fatalf("persistAndPublish succeeded after a foreign commit — the committed changeset %s was resurrected under open/ (F-806-1)", private.ID)
		}
		const want = "stage: the open changeset was committed or rejected by another process"
		if err.Error() != want {
			t.Fatalf("persistAndPublish err = %v, want exactly %q (A-804)", err, want)
		}

		ents, err := os.ReadDir(e.changesetOpenDir())
		if err != nil {
			t.Fatalf("ReadDir open/: %v", err)
		}
		for _, ent := range ents {
			if ent.IsDir() && ent.Name() == private.ID {
				t.Errorf("changesets/open/ contains %s after the refused persist — the committed id was resurrected", ent.Name())
			}
		}
		if _, err := os.Stat(filepath.Join(e.changesetCommittedDir(), private.ID, "changeset.json")); err != nil {
			t.Errorf("committed/%s is not intact after the refused persist: %v", private.ID, err)
		}
	})

	t.Run("failed commit releases the lock", func(t *testing.T) {
		e, dir := newTestEngine(t)
		if _, err := e.OpenChangeset("r807 a2", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		stageIngest(t, e, "raw/notes/a2.md", "a2 body.\n")

		// The commit fails at step 3 — after step 1 took the lock and step
		// 3 journaled commit_begin (the existing faultAfter seam; the vault
		// is untouched, so the following commit below is not refused stale).
		e.faultAfter = func(step string) error {
			if step == "3" {
				return errors.New("boom: journal full mid-commit")
			}
			return nil
		}
		_, err := e.Commit("r807 faulted commit")
		e.faultAfter = nil
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("Commit err = %v, want the injected step-3 failure", err)
		}

		// (a) The lock is released (A-804, F-806-2): a following Commit does
		// not return ErrLocked — it simply commits.
		if _, err := e.Commit("second attempt"); err != nil {
			t.Errorf("second Commit err = %v, want success (errors.Is ErrLocked = %v) — a failed commit must not leak the vault lock into the engine",
				err, errors.Is(err, ErrLocked))
		}

		// (b) The reload tick is alive: a foreign change lands and
		// ReloadIfChanged sees it — the F-806-2 wedge is gone.
		fe := foreignEngine(t, dir)
		foreign, err := fe.OpenChangeset("foreign after recovery", testAuthor)
		if err != nil {
			t.Fatalf("foreign OpenChangeset: %v", err)
		}
		reloaded, err := e.ReloadIfChanged()
		if err != nil {
			t.Fatalf("ReloadIfChanged: %v", err)
		}
		if !reloaded {
			t.Errorf("ReloadIfChanged = false after a foreign changeset opened — the tick is still suppressed by the failed commit (F-806-2)")
		}
		if cur, curErr := e.Current(); curErr != nil || cur.ID != foreign.ID {
			t.Errorf("Current after the reload = %v, %v; want the foreign changeset %s", cur, curErr, foreign.ID)
		}
	})

	t.Run("lifecycle fields are race free", func(t *testing.T) {
		e, _ := newTestEngine(t)
		stop := make(chan struct{})
		var spin sync.WaitGroup
		spin.Add(2)
		go func() { // ForceNextCommit writes e.forceNext
			defer spin.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				e.ForceNextCommit()
			}
		}()
		go func() { // ReloadIfChanged reads e.unlock
			defer spin.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := e.ReloadIfChanged(); err != nil {
					t.Errorf("ReloadIfChanged: %v", err)
					return
				}
			}
		}()
		commits := make(chan struct{})
		go func() { // Commit writes e.unlock (step 1) and consumes e.forceNext
			defer close(commits)
			for i := 0; i < 300; i++ {
				if _, err := e.Commit("r807 race probe"); err != nil && !errors.Is(err, ErrNoChangeset) {
					t.Errorf("Commit: %v", err)
					return
				}
			}
		}()
		<-commits
		close(stop)
		spin.Wait()
		// The race detector, not this body, decides the outcome (A-804,
		// F-806-3 / ORCH-806).
	})

	t.Run("reload never runs during a commit", func(t *testing.T) {
		e, dir := newTestEngine(t)
		if _, err := e.OpenChangeset("r807 reload", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		stageIngest(t, e, "raw/notes/reload.md", "reload body.\n")

		// Park a real commit after step 9: commit_end journalled, the
		// changeset renamed, the lock still held — between its lock and its
		// end (backbone §5.4 steps 1→10).
		parked := make(chan struct{})
		release := make(chan struct{})
		e.faultAfter = func(step string) error {
			if step == "9" {
				close(parked)
				<-release
			}
			return nil
		}
		commitDone := make(chan error, 1)
		go func() {
			_, err := e.Commit("r807 parked commit")
			commitDone <- err
		}()
		<-parked

		// A foreign changeset opens while the commit is parked — the
		// journal now differs from this engine's stamp.
		fe := foreignEngine(t, dir)
		foreign, err := fe.OpenChangeset("foreign during commit", testAuthor)
		if err != nil {
			t.Fatalf("foreign OpenChangeset: %v", err)
		}

		reloaded, err := e.ReloadIfChanged()
		if err != nil {
			t.Fatalf("ReloadIfChanged: %v", err)
		}
		if reloaded {
			t.Errorf("ReloadIfChanged ran while a commit was between its lock and its end — the torn-vault TOCTOU (A-804, F-806-3)")
		}

		close(release)
		if err := <-commitDone; err != nil {
			t.Fatalf("parked Commit: %v", err)
		}

		// The foreign change was real: once the lock is released the same
		// call reloads — proving the no-op above was the lock guard, not an
		// unchanged journal.
		reloaded, err = e.ReloadIfChanged()
		if err != nil {
			t.Fatalf("ReloadIfChanged after the commit: %v", err)
		}
		if !reloaded {
			t.Fatalf("ReloadIfChanged = false after the commit released the lock, want it to see the foreign changeset")
		}
		if cur, curErr := e.Current(); curErr != nil || cur.ID != foreign.ID {
			t.Errorf("Current after the reload = %v, %v; want the foreign changeset %s", cur, curErr, foreign.ID)
		}
	})
}
