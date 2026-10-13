package tools

// staged_readback_damaged_test.go pins 060 review LOW-2: the staged-only
// lookup of wiki.get, wiki.neighbors and wiki.backlinks DECIDES a result (a
// page read back, or "exists only in the open changeset"), so a changeset that
// cannot be read must abort the turn the way the committed path's StagedFile
// error already does — never answer "not found" and send the model back into
// the loop 060 removes. Hints (closestPageList) keep their silence.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/stage"
)

// damagedChangesetRegistry stages paged-attention in one engine, closes it,
// then overwrites the open changeset.json with bytes no JSON decoder accepts
// and returns a registry over a NEW engine on the same vault — a process that
// joins a changeset it can no longer read (cold cache, so the first lookup
// goes to disk).
func damagedChangesetRegistry(t *testing.T) *Registry {
	t.Helper()
	reg, e, dir := engineRegistry(t, nil)
	readbackCreatePagedAttention(t, reg)
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	matches, err := filepath.Glob(filepath.Join(dir, ".llmwiki", "changesets", "open", "*", "changeset.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("open changeset.json = %v, %v; want exactly one", matches, err)
	}
	if err := os.WriteFile(matches[0], []byte("{ this is not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	e2, err := stage.OpenEngine(dir)
	if err != nil {
		t.Fatalf("OpenEngine over a damaged changeset: %v (the test needs an engine that opens)", err)
	}
	t.Cleanup(func() { _ = e2.Close() })
	v2 := e2.Vault()
	return NewRegistry(Deps{Vault: v2, Index: index.Build(v2), Engine: e2, Author: stage.Author{Kind: "agent", Model: "test"}})
}

// TestStagedOnlyLookupAbortsOnDamagedChangeset: a name the committed vault does
// not hold, asked of the tools that now consult the changeset, returns a Go
// error naming the failure — not a Result, and certainly not "not found".
func TestStagedOnlyLookupAbortsOnDamagedChangeset(t *testing.T) {
	for _, tool := range []string{"wiki.get", "wiki.neighbors", "wiki.backlinks"} {
		t.Run(tool, func(t *testing.T) {
			reg := damagedChangesetRegistry(t)
			res, err := reg.Call(t.Context(), tool, []byte(`{"page":"paged-attention"}`))
			if err == nil {
				t.Fatalf("%s over an unreadable changeset returned IsError=%v content %q with a nil error; want a turn-aborting error", tool, res.IsError, res.Content)
			}
			if !strings.Contains(err.Error(), tool) || !strings.Contains(err.Error(), "changeset") {
				t.Errorf("%s error does not name the tool and the changeset: %v", tool, err)
			}
		})
	}
}

// TestStagedOnlyLookupDamagedChangesetCommittedHit: a committed page asked of
// wiki.get with the same unreadable changeset behaves as it did before 060 —
// the committed path's StagedFile surfaces the failure as a Go error too. The
// graph tools never read staged bytes for a committed hit, so they answer the
// committed graph as before.
func TestStagedOnlyLookupDamagedChangesetCommittedHit(t *testing.T) {
	reg := damagedChangesetRegistry(t)

	if res, err := reg.Call(t.Context(), "wiki.get", []byte(`{"page":"kv-cache"}`)); err == nil {
		t.Errorf("wiki.get kv-cache over an unreadable changeset: IsError=%v content %.200q with a nil error; want the StagedFile error", res.IsError, res.Content)
	}
	for _, tool := range []string{"wiki.neighbors", "wiki.backlinks"} {
		res, err := reg.Call(t.Context(), tool, []byte(`{"page":"kv-cache"}`))
		if err != nil || res.IsError {
			t.Errorf("%s kv-cache over an unreadable changeset = %+v, %v; want the committed graph answer", tool, res, err)
		}
	}
}

// TestStagedOnlyLookupNoChangesetStillNotFound: no open changeset is not an
// error — the name is simply not staged — and no engine at all is the same.
func TestStagedOnlyLookupNoChangesetStillNotFound(t *testing.T) {
	reg, _, _ := engineRegistry(t, nil)
	for _, tool := range []string{"wiki.get", "wiki.neighbors", "wiki.backlinks"} {
		res, err := reg.Call(t.Context(), tool, []byte(`{"page":"paged-attention"}`))
		if err != nil || !res.IsError || res.Content != readbackNotFound("paged-attention") {
			t.Errorf("%s with no changeset open = %+v, %v; want the not-found text", tool, res, err)
		}
	}
}

// TestStagedPathsHintsStaySilentOnDamagedChangeset: the hint callers
// (closestPageList, closestRawSources) keep stagedPaths's silence — advice
// appended to a refusal must not turn the refusal into something else — while
// stagedPathsStrict, which the staged-only lookup uses, reports the failure.
func TestStagedPathsHintsStaySilentOnDamagedChangeset(t *testing.T) {
	reg := damagedChangesetRegistry(t)
	d := reg.deps

	if got := stagedPaths(d, stage.OpCreatePage); got != nil {
		t.Errorf("stagedPaths over an unreadable changeset = %v, want nil", got)
	}
	got, err := stagedPathsStrict(d, stage.OpCreatePage)
	if err == nil {
		t.Fatalf("stagedPathsStrict over an unreadable changeset = %v, nil; want an error", got)
	}
	if !strings.Contains(err.Error(), "read open changeset") {
		t.Errorf("stagedPathsStrict error does not say what failed: %v", err)
	}
}
