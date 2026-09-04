package stage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// forcedFlag reads the "forced" key of the most recent commit_end event.
func forcedFlag(t *testing.T, e *Engine) (forced bool, commitEnds int) {
	t.Helper()
	evs, err := e.journal.Query(Filter{Kinds: []EventKind{EvCommitEnd}})
	if err != nil {
		t.Fatalf("query journal: %v", err)
	}
	if len(evs) == 0 {
		t.Fatal("no commit_end event in the journal")
	}
	var data struct {
		LintErrors int  `json:"lint_errors"`
		LintWarns  int  `json:"lint_warns"`
		Forced     bool `json:"forced"`
	}
	if err := json.Unmarshal(evs[len(evs)-1].Data, &data); err != nil {
		t.Fatalf("parse commit_end data: %v", err)
	}
	return data.Forced, len(evs)
}

// TestForceNextCommitMarksCommitEnd pins MASTER §9 D-CD / §10 OR-12: the
// override is recorded on the REAL commit_end, so ONE commit produces
// exactly ONE commit_end. `lw commit --force` cannot record it any other
// way — Engine.Commit's §5.4 signature is frozen and the regression gate
// itself lives in cmd/lw — and appending a second commit_end instead
// makes the journal state that one commit ended twice.
func TestForceNextCommitMarksCommitEnd(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("forced", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	appendCreate(t, e, "wiki/concepts/brand-new.md", newConceptPageContent("Brand New"))

	e.ForceNextCommit()
	if _, err := e.Commit("forced"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	forced, n := forcedFlag(t, e)
	if !forced {
		t.Error(`commit_end Data has no "forced":true after ForceNextCommit`)
	}
	if n != 1 {
		t.Errorf("commit_end events = %d, want 1 — one commit ends exactly once", n)
	}
}

// TestCommitEndOmitsForcedByDefault pins the omitempty half: an ordinary
// commit's payload stays byte-identical to what every wave before OR-12
// wrote, so a journal written by an older build and one written by this
// one are the same file.
func TestCommitEndOmitsForcedByDefault(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("plain", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	appendCreate(t, e, "wiki/concepts/brand-new.md", newConceptPageContent("Brand New"))
	if _, err := e.Commit("plain"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	evs, err := e.journal.Query(Filter{Kinds: []EventKind{EvCommitEnd}})
	if err != nil {
		t.Fatalf("query journal: %v", err)
	}
	if got, want := string(evs[len(evs)-1].Data), `{"lint_errors":0,"lint_warns":1}`; got != want {
		t.Errorf("commit_end Data = %s, want %s (forced must be omitted entirely)", got, want)
	}
}

// TestForceNextCommitIsConsumedNotSticky pins the consume-and-clear rule:
// Commit reads the flag before any step can fail, so an override can
// never leak into a LATER commit and silently mark it forced.
func TestForceNextCommitIsConsumedNotSticky(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("first", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	appendCreate(t, e, "wiki/concepts/first-page.md", newConceptPageContent("First Page"))
	e.ForceNextCommit()
	if _, err := e.Commit("first"); err != nil {
		t.Fatalf("Commit first: %v", err)
	}
	if forced, _ := forcedFlag(t, e); !forced {
		t.Fatal("first commit was not marked forced")
	}

	if _, err := e.OpenChangeset("second", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	appendCreate(t, e, "wiki/concepts/second-page.md", newConceptPageContent("Second Page"))
	if _, err := e.Commit("second"); err != nil {
		t.Fatalf("Commit second: %v", err)
	}
	forced, n := forcedFlag(t, e)
	if forced {
		t.Error("the second, unforced commit is marked forced — the flag leaked")
	}
	if n != 2 {
		t.Errorf("commit_end events = %d, want 2", n)
	}
}

// TestForceNextCommitClearedByAFailedCommit pins the ordering: the flag is
// consumed at the TOP of Commit, before the step that can refuse, so a
// commit that fails on stale ops does not leave the next one marked.
func TestForceNextCommitClearedByAFailedCommit(t *testing.T) {
	e, dir := newTestEngine(t)
	if _, err := e.OpenChangeset("stale", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	page, ok := e.Vault().Page("wiki/concepts/kv-cache.md")
	if !ok {
		t.Fatal("fixture missing kv-cache.md")
	}
	rewritten := *page
	rewritten.Body = page.Body + "\nAn extra line.\n"
	if _, err := e.Append(Op{
		Kind: OpPatchPage, Path: page.Path, Section: "## Related",
		Before: page.SHA256(), Content: rewritten.Serialize(),
		Hunks: []Hunk{{ID: "h1", Path: page.Path, Add: []string{"An extra line."}}},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Make the op stale behind the engine's back, so Commit refuses.
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(page.Path)),
		append(page.Serialize(), []byte("\ndrift\n")...), 0o644); err != nil {
		t.Fatalf("write drift: %v", err)
	}
	if err := e.Vault().Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	e.ForceNextCommit()
	if _, err := e.Commit("stale"); err == nil {
		t.Fatal("Commit succeeded on a stale op, want a refusal")
	}
	if e.forceNext {
		t.Error("forceNext survived a failed Commit — the next commit would be marked forced")
	}
}
