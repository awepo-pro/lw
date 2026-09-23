package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

// TestLintFixJoinsOpenChangeset is 019 T1's frozen lint --fix test: with a
// changeset already open, `lw lint --fix` JOINS it — printing the stderr
// notice before the agent turn — and its repair ops land in the SAME
// changeset as the pre-existing op, instead of the command refusing with
// "stage: a changeset is already open".
func TestLintFixJoinsOpenChangeset(t *testing.T) {
	maxTokens8192Env(t)
	root := testutil.CopyFixture(t, "minimal")

	// One fixable finding, in exactly one bucket: the mis-dated page the
	// 020 fixture helper writes, plus the index line that keeps every other
	// check quiet.
	const slug = "aaa-join"
	writeBrokenPage(t, root, slug)
	idx := filepath.Join(root, "index.md")
	b, err := os.ReadFile(idx)
	if err != nil {
		t.Fatalf("read index.md: %v", err)
	}
	if err := os.WriteFile(idx, append(b, []byte(fmt.Sprintf("- [[%s]] — deliberately mis-dated fixture page.\n", slug))...), 0o644); err != nil {
		t.Fatalf("append index.md: %v", err)
	}
	wantPaths := []string{"wiki/concepts/" + slug + ".md"}
	if got := lintPathsInVault(t, root); len(got) != 1 || got[0] != wantPaths[0] {
		t.Fatalf("fixture precondition: lint spans %v, want exactly %v", got, wantPaths)
	}

	patchID := stagePreExistingPatch(t, root)

	var rec *roundRecordingAgent
	withFakeAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
		rec = &roundRecordingAgent{e: e, sessions: sessions}
		return rec, nil
	})

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"lint", "--fix", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	if !strings.Contains(stderr, "joined open changeset cs-") {
		t.Errorf("stderr = %q, want the joined notice", stderr)
	}
	if !strings.Contains(stderr, "(1 op(s) already staged; they will be reviewed and committed together)") {
		t.Errorf("stderr = %q, want the notice's live-op count and promise", stderr)
	}
	if len(rec.msgs) != 1 {
		t.Fatalf("the agent received %d message(s), want 1 (one finding bucket)", len(rec.msgs))
	}

	// The repair op sits in the SAME changeset as the pre-existing op.
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()
	cs, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got := len(cs.Live()); got != 2 {
		t.Fatalf("the changeset holds %d live op(s), want 2 (the pre-existing patch and the repair)", got)
	}
	patch := findOpInChangeset(t, cs, stage.OpPatchPage)
	if patch.ID != patchID || patch.State != stage.StateProposed {
		t.Errorf("patch op = %s (%s), want %s still live (proposed)", patch.ID, patch.State, patchID)
	}
	repair := findOpInChangeset(t, cs, stage.OpIngestSource)
	if repair.Path != "raw/articles/lint-fix-round-1.md" {
		t.Errorf("repair op path = %q, want the round's own repair", repair.Path)
	}
}
