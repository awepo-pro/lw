// The on-disk fault state the doctor tests inject. Kept beside
// cmd_doctor_test.go so that file stays about the checks and this one about
// manufacturing a vault whose damage doctor has to find.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/vault"
)

// The two paths the interrupted-apply state projects onto: the first is
// written with exactly its post-image (Recover counts it applied), the
// second is left unwritten (Recover counts it pending and fixable).
const (
	appliedRecoverPath = "wiki/concepts/doctor-applied.md"
	pendingRecoverPath = "wiki/concepts/doctor-pending.md"
)

// commitIngestSource opens a changeset, proposes one ingest_source op and
// commits it, producing the real on-disk state doctor inspects: objects in
// the CAS, a snapshot, journal events and a saved index. It returns the
// object sha the raw source's content landed under.
func commitIngestSource(t *testing.T, root string) string {
	t.Helper()
	e := openEngine(t, root)

	body := "# Doctor Test Source\n\nA body ingested so doctor has objects to check.\n"
	fm := fmt.Sprintf("---\nsource_url: https://example.org/doctor-test\ningested: 2026-09-09\nsha256: %s\n---\n\n", vault.BodySHA256(body))
	rs, err := vault.ParseRawSource("raw/articles/doctor-test.md", []byte(fm+body))
	if err != nil {
		t.Fatalf("parse raw source: %v", err)
	}

	if _, err := e.OpenChangeset("doctor test ingest", stage.Author{Kind: "human"}); err != nil {
		t.Fatalf("open changeset: %v", err)
	}
	opID, err := e.Append(stage.Op{
		Kind:      stage.OpIngestSource,
		Path:      "raw/articles/doctor-test.md",
		Extractor: "passthrough",
		Content:   rs.Serialize(),
	})
	if err != nil {
		t.Fatalf("append ingest_source: %v", err)
	}
	if _, err := e.Commit("doctor test commit"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := e.Current(); !errors.Is(err, stage.ErrNoChangeset) {
		t.Fatalf("after Commit, Current = %v, want ErrNoChangeset", err)
	}

	store, err := stage.OpenStore(filepath.Join(root, stateDirName, objectsDirName))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	// Read the committed changeset back, as a second process would, so the
	// sha the test deletes is the one the engine actually recorded.
	entries, err := os.ReadDir(filepath.Join(root, stateDirName, "changesets", "committed"))
	if err != nil {
		t.Fatalf("list committed changesets: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("%d committed changesets, want 1", len(entries))
	}
	b, err := os.ReadFile(filepath.Join(root, stateDirName, "changesets", "committed", entries[0].Name(), "changeset.json"))
	if err != nil {
		t.Fatalf("read changeset.json: %v", err)
	}
	var cs stage.Changeset
	if err := json.Unmarshal(b, &cs); err != nil {
		t.Fatalf("parse changeset.json: %v", err)
	}
	op, ok := cs.Op(opID)
	if !ok {
		t.Fatalf("changeset %s has no op %s", cs.ID, opID)
	}
	sha := op.SHA256
	if sha == "" {
		sha = op.After
	}
	if !store.Has(sha) {
		t.Fatalf("object %s for op %s is not in the CAS", sha, opID)
	}
	return sha
}

// seedInterruptedApply builds the on-disk state an interrupted apply leaves:
// an open changeset whose two create_page ops project onto
// appliedRecoverPath and pendingRecoverPath, a journal commit_begin naming
// both paths with no matching commit_end, and both post-images in the CAS.
func seedInterruptedApply(t *testing.T, root string) {
	t.Helper()
	e := openEngine(t, root)

	const (
		csID     = "cs-doctor00000009"
		commitID = "000009"
	)

	postImage := func(path, title string) []byte {
		page, err := vault.ParsePage(path, []byte(fmt.Sprintf("---\ntitle: %s\ntype: concept\ncreated: 2026-09-09\nupdated: 2026-09-09\n---\n\n%s body.\n", title, title)))
		if err != nil {
			t.Fatalf("parse page: %v", err)
		}
		return page.Serialize()
	}

	store, err := stage.OpenStore(filepath.Join(root, stateDirName, objectsDirName))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	paths := []struct {
		path  string
		title string
	}{
		{appliedRecoverPath, "Doctor Applied"},
		{pendingRecoverPath, "Doctor Pending"},
	}

	ops := make([]stage.Op, 0, len(paths))
	for i, p := range paths {
		content := postImage(p.path, p.title)
		sha, err := store.Put(content)
		if err != nil {
			t.Fatalf("put %s: %v", p.path, err)
		}
		ops = append(ops, stage.Op{
			ID:    fmt.Sprintf("op%d", i+1),
			Kind:  stage.OpCreatePage,
			Path:  p.path,
			After: sha,
			State: stage.StateProposed,
		})
	}

	// Only the first path was written before the interrupt, and its bytes
	// are exactly its post-image — which is what Recover's applied test
	// compares.
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(appliedRecoverPath)), postImage(appliedRecoverPath, "Doctor Applied"), 0o644); err != nil {
		t.Fatalf("write applied path: %v", err)
	}

	cs := stage.Changeset{
		ID:       csID,
		Intent:   "doctor interrupted apply",
		Author:   stage.Author{Kind: "human"},
		OpenedAt: fixedTS(),
		Ops:      ops,
		Checks:   stage.Checks{Schema: "pass", Lint: "pass"},
	}
	csDir := filepath.Join(root, stateDirName, "changesets", "open", csID)
	if err := os.MkdirAll(csDir, 0o755); err != nil {
		t.Fatalf("create changeset dir: %v", err)
	}
	b, err := json.MarshalIndent(cs, "", "  ")
	if err != nil {
		t.Fatalf("marshal changeset: %v", err)
	}
	if err := os.WriteFile(filepath.Join(csDir, "changeset.json"), b, 0o644); err != nil {
		t.Fatalf("write changeset.json: %v", err)
	}

	targets := make([]string, 0, len(paths))
	for _, p := range paths {
		targets = append(targets, p.path)
	}
	if err := e.Journal().Append(stage.Event{
		TS:        fixedTS(),
		Kind:      stage.EvCommitBegin,
		Changeset: csID,
		Commit:    commitID,
		Actor:     stage.Author{Kind: "human"},
		Paths:     targets,
	}); err != nil {
		t.Fatalf("journal commit_begin: %v", err)
	}
}
