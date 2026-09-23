// drop_cascade_test.go pins workflow 030 T1: the op dependency graph over a
// changeset — D1 (a later live op whose opTouches intersects), D2 (a live op
// whose post-image newly links a page only a dependency op brings into
// existence), D3 (a live op whose post-image cites a raw path only a
// dependency op ingests) — and the batch verbs built on it: OpDependents,
// OpPrerequisites, DropOps and RestoreOps.
//
// The shape these tests close is the live 030 report: an ingest plus a chain
// of dependent patches could only be accepted or declined whole, because
// dropping one op left its dependents stale and a stale op blocks Commit.
// Dropping an op together with everything that cannot survive without it
// commits clean again; RestoreOps undoes the whole decision.
package stage

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// tilelangRawPath is the raw path from the live 030 reproduction — 112
// characters, the same path the user could not individually drop an op
// beside — so every ingest in these tests runs at the real length.
const tilelangRawPath = "raw/papers/tilelang-a-composable-tiled-programming-model-for-ai-systemsthanks-mathsection-equal-contributions.md"

// citeQPath is the second seeded page: the no-citation control patch of
// citation_body_marker_d3 lands here, on a page the ingest does not touch.
const citeQPath = "wiki/concepts/cite-q.md"

// citeQSeed returns the on-disk bytes of citeQPath — the same shape
// chainedTargetSeed gives the first seeded page, so a patch on it validates
// the same way.
func citeQSeed() []byte {
	return []byte("---\n" +
		"title: Cite Q\n" +
		"created: 2026-09-21\n" +
		"updated: 2026-09-21\n" +
		"type: concept\n" +
		"tags: [inference]\n" +
		"confidence: medium\n" +
		"---\n" +
		"\n" +
		"# Cite Q\n" +
		"\n" +
		"Control page linking [[kv-cache]] and [[gpt-4]].\n" +
		"\n" +
		"## Alpha\n" +
		"\n" +
		"Alpha body.\n")
}

// newDropCascadeEngine opens an Engine over a private copy of the minimal
// fixture with TWO seeded pages — chainedTargetPath (the chain tests' shared
// patch target) and citeQPath (the control) — the same
// CopyFixture-then-write pattern newChainedEngine uses, applied BEFORE
// OpenEngine so both load as committed state.
func newDropCascadeEngine(t *testing.T) (*Engine, string) {
	t.Helper()
	dir := testutil.CopyFixture(t, "minimal")
	for _, seed := range []struct {
		path string
		data []byte
	}{
		{chainedTargetPath, chainedTargetSeed()},
		{citeQPath, citeQSeed()},
	} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(seed.path)), seed.data, 0o644); err != nil {
			t.Fatalf("seed %s: %v", seed.path, err)
		}
	}
	e, err := OpenEngine(dir)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	e.now = testutil.FixedClock()
	return e, dir
}

// dependentPatch builds one patch_page over base — the exact "dependent"
// shape every chain here uses: same page, Before = base's canonical sha
// (the previous op's staged After for a chained call), post-image = base
// with extra inserted after its Alpha body line. It returns the op and the
// new canonical content, the next link's base.
func dependentPatch(t *testing.T, path string, base []byte, extra string) (Op, []byte) {
	t.Helper()
	page, err := vault.ParsePage(path, base)
	if err != nil {
		t.Fatalf("parse %s base: %v", path, err)
	}
	patched := *page
	patched.Body = strings.Replace(page.Body, "Alpha body.", "Alpha body.\n\n"+extra, 1)
	content := patched.Serialize()
	return Op{
		Kind:    OpPatchPage,
		Path:    path,
		Section: "## Alpha",
		Before:  page.SHA256(),
		Content: content,
		Hunks:   []Hunk{{ID: "h1", Path: path, Section: "## Alpha", Add: []string{extra, ""}}},
	}, content
}

// appendDependentChain appends len(extras) dependent patch_page ops on path,
// each chaining on the previous op's staged After ("Before" = the previous
// op's "After", the 020 dependent-op shape), and returns their op ids.
func appendDependentChain(t *testing.T, e *Engine, path string, extras ...string) []string {
	t.Helper()
	page, ok := e.Vault().Page(path)
	if !ok {
		t.Fatalf("fixture missing %s", path)
	}
	base := page.Serialize()
	var ids []string
	for _, extra := range extras {
		op, content := dependentPatch(t, path, base, extra)
		id, err := e.Append(op)
		if err != nil {
			t.Fatalf("Append dependent patch on %s: %v", path, err)
		}
		ids = append(ids, id)
		base = content
	}
	return ids
}

// createdNoteContent renders a create_page post-image: valid concept
// frontmatter, a title heading, an optional marker line (the "^[<path>]"
// citation the D3 subtests stage), and two outbound links to pages the
// fixture already holds — validateCreatePage's minimum.
func createdNoteContent(title, markerLine string) []byte {
	body := "# " + title + "\n\n"
	if markerLine != "" {
		body += markerLine + "\n\n"
	}
	body += "See [[kv-cache]] and [[gpt-4]] for background.\n"
	return []byte("---\n" +
		"title: " + title + "\n" +
		"created: 2026-09-21\n" +
		"updated: 2026-09-21\n" +
		"type: concept\n" +
		"tags: [inference]\n" +
		"confidence: medium\n" +
		"---\n" +
		"\n" + body)
}

// mustCreatePage appends one create_page op at path carrying content.
func mustCreatePage(t *testing.T, e *Engine, path string, content []byte, provenance []string) string {
	t.Helper()
	id, err := e.Append(Op{
		Kind:       OpCreatePage,
		Path:       path,
		Content:    content,
		Rationale:  "030 dependency-graph fixture",
		Provenance: provenance,
	})
	if err != nil {
		t.Fatalf("Append create_page %s: %v", path, err)
	}
	return id
}

// staleCount returns how many of c's top-level ops sit at StateStale.
func staleCount(c *Changeset) int {
	n := 0
	for _, op := range c.Ops {
		if op.State == StateStale {
			n++
		}
	}
	return n
}

// diffForPath returns every FileDiff d renders for path, in d's order.
func diffForPath(t *testing.T, e *Engine, path string) []FileDiff {
	t.Helper()
	d, err := e.Diff()
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	var out []FileDiff
	for _, fd := range d.Files {
		if fd.Path == path {
			out = append(out, fd)
		}
	}
	return out
}

// chainWithIngest opens a changeset and appends the tests' common base
// shape: op1 ingest of the real 112-char raw path, then len(n) dependent
// patches on chainedTargetPath. It returns the patch ids.
func chainWithIngest(t *testing.T, e *Engine, n int) []string {
	t.Helper()
	if _, err := e.OpenChangeset("030 dependency graph", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	stageIngest(t, e, tilelangRawPath, "# TileLang\n\nA tiled programming model.\n")
	extras := make([]string, n)
	for i := range extras {
		extras[i] = "Chain note for step " + string(rune('a'+i)) + "."
	}
	return appendDependentChain(t, e, chainedTargetPath, extras...)
}

func TestOpDependencies(t *testing.T) {
	t.Run("chain_d1", func(t *testing.T) {
		e, _ := newDropCascadeEngine(t)
		patches := chainWithIngest(t, e, 3) // op2, op3, op4
		deps, err := e.OpDependents(patches[0])
		if err != nil {
			t.Fatalf("OpDependents(%s): %v", patches[0], err)
		}
		want := []string{patches[1], patches[2]}
		if !reflect.DeepEqual(deps, want) {
			t.Errorf("OpDependents(%s) = %v; want %v", patches[0], deps, want)
		}
	})

	t.Run("chain_tail", func(t *testing.T) {
		e, _ := newDropCascadeEngine(t)
		patches := chainWithIngest(t, e, 3)
		deps, err := e.OpDependents(patches[2])
		if err != nil {
			t.Fatalf("OpDependents(%s): %v", patches[2], err)
		}
		if len(deps) != 0 {
			t.Errorf("OpDependents(%s) = %v; want empty, not error", patches[2], deps)
		}
	})

	t.Run("citation_body_marker_d3", func(t *testing.T) {
		e, _ := newDropCascadeEngine(t)
		if _, err := e.OpenChangeset("citation marker", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		op1 := stageIngest(t, e, tilelangRawPath, "# TileLang\n\nBody marker fixture.\n")

		op2, _ := dependentPatch(t, chainedTargetPath, seededBody(t, e, chainedTargetPath),
			"Supporting claim.^["+tilelangRawPath+"]")
		id2, err := e.Append(op2)
		if err != nil {
			t.Fatalf("Append citing patch: %v", err)
		}
		op3, _ := dependentPatch(t, citeQPath, seededBody(t, e, citeQPath), "Unrelated note.")
		if _, err := e.Append(op3); err != nil {
			t.Fatalf("Append control patch: %v", err)
		}

		deps, err := e.OpDependents(op1)
		if err != nil {
			t.Fatalf("OpDependents(%s): %v", op1, err)
		}
		want := []string{id2}
		if !reflect.DeepEqual(deps, want) {
			t.Errorf("OpDependents(%s) = %v; want %v", op1, deps, want)
		}
	})

	t.Run("citation_provenance_d3", func(t *testing.T) {
		e, _ := newDropCascadeEngine(t)
		if _, err := e.OpenChangeset("provenance", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		op1 := stageIngest(t, e, tilelangRawPath, "# TileLang\n\nProvenance fixture.\n")
		id2 := mustCreatePage(t, e, "wiki/concepts/tilelang-note.md",
			createdNoteContent("TileLang Note", ""), []string{tilelangRawPath})

		deps, err := e.OpDependents(op1)
		if err != nil {
			t.Fatalf("OpDependents(%s): %v", op1, err)
		}
		want := []string{id2}
		if !reflect.DeepEqual(deps, want) {
			t.Errorf("OpDependents(%s) = %v; want %v", op1, deps, want)
		}
	})

	t.Run("link_d2", func(t *testing.T) {
		e, _ := newDropCascadeEngine(t)
		if _, err := e.OpenChangeset("created link", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		const createdPath = "wiki/concepts/cascade-created.md"
		op1 := mustCreatePage(t, e, createdPath,
			createdNoteContent("Cascade Created", ""), []string{"raw/papers/leviathan-2023.md"})

		op2, _ := dependentPatch(t, chainedTargetPath, seededBody(t, e, chainedTargetPath),
			"See [[cascade-created]].")
		id2, err := e.Append(op2)
		if err != nil {
			t.Fatalf("Append linking patch: %v", err)
		}

		deps, err := e.OpDependents(op1)
		if err != nil {
			t.Fatalf("OpDependents(%s): %v", op1, err)
		}
		want := []string{id2}
		if !reflect.DeepEqual(deps, want) {
			t.Errorf("OpDependents(%s) = %v; want %v", op1, deps, want)
		}
	})

	t.Run("link_d2_earlier", func(t *testing.T) {
		e, _ := newDropCascadeEngine(t)
		patchID, createID := appendLinkEarlierShape(t, e)

		deps, err := e.OpDependents(createID)
		if err != nil {
			t.Fatalf("OpDependents(%s): %v", createID, err)
		}
		want := []string{patchID}
		if !reflect.DeepEqual(deps, want) {
			t.Errorf("OpDependents(%s) = %v; want %v (the EARLIER linking patch)", createID, deps, want)
		}
	})

	t.Run("citation_d3_earlier", func(t *testing.T) {
		e, _ := newDropCascadeEngine(t)
		if _, err := e.OpenChangeset("citation earlier than ingest", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		op1, _ := dependentPatch(t, chainedTargetPath, seededBody(t, e, chainedTargetPath),
			"Supporting claim.^["+tilelangRawPath+"]")
		patchID, err := e.Append(op1)
		if err != nil {
			t.Fatalf("Append earlier citing patch: %v", err)
		}
		ingestID := stageIngest(t, e, tilelangRawPath, "# TileLang\n\nEarlier-citation fixture.\n")

		deps, err := e.OpDependents(ingestID)
		if err != nil {
			t.Fatalf("OpDependents(%s): %v", ingestID, err)
		}
		want := []string{patchID}
		if !reflect.DeepEqual(deps, want) {
			t.Errorf("OpDependents(%s) = %v; want %v (the EARLIER citing patch)", ingestID, deps, want)
		}
	})

	t.Run("transitive", func(t *testing.T) {
		e, _ := newDropCascadeEngine(t)
		if _, err := e.OpenChangeset("transitive", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		op1 := stageIngest(t, e, tilelangRawPath, "# TileLang\n\nTransitive fixture.\n")
		id2 := mustCreatePage(t, e, "wiki/concepts/cite-note.md",
			createdNoteContent("Cite Note", "Claim.^["+tilelangRawPath+"]"),
			[]string{"raw/papers/leviathan-2023.md"})

		op3, _ := dependentPatch(t, chainedTargetPath, seededBody(t, e, chainedTargetPath),
			"See [[cite-note]].")
		if _, err := e.Append(op3); err != nil {
			t.Fatalf("Append linking patch: %v", err)
		}

		deps, err := e.OpDependents(op1)
		if err != nil {
			t.Fatalf("OpDependents(%s): %v", op1, err)
		}
		want := []string{id2, "op3"}
		if !reflect.DeepEqual(deps, want) {
			t.Errorf("OpDependents(%s) = %v; want %v", op1, deps, want)
		}
	})

	t.Run("rename_d1", func(t *testing.T) {
		e, _ := newDropCascadeEngine(t)
		if _, err := e.OpenChangeset("rename chain", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		const toPath = "wiki/concepts/chain-target-2.md"
		if _, err := e.Append(Op{Kind: OpRenamePage, From: chainedTargetPath, To: toPath}); err != nil {
			t.Fatalf("Append rename_page: %v", err)
		}
		op2, _ := dependentPatch(t, toPath, seededBody(t, e, chainedTargetPath), "Patched after the rename.")
		id2, err := e.Append(op2)
		if err != nil {
			t.Fatalf("Append patch on the renamed path: %v", err)
		}

		deps, err := e.OpDependents("op1")
		if err != nil {
			t.Fatalf("OpDependents(op1): %v", err)
		}
		want := []string{id2}
		if !reflect.DeepEqual(deps, want) {
			t.Errorf("OpDependents(op1) = %v; want %v", deps, want)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		e, _ := newDropCascadeEngine(t)
		if _, err := e.OpenChangeset("unknown id", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		_, err := e.OpDependents("op99")
		if err == nil {
			t.Fatal("OpDependents(op99) = nil error; want one naming op99")
		}
		if !strings.Contains(err.Error(), "op99") {
			t.Errorf("error %v does not name op99", err)
		}
		if _, err := e.OpPrerequisites("op99"); err == nil || !strings.Contains(err.Error(), "op99") {
			t.Errorf("OpPrerequisites(op99) = %v; want an error naming op99", err)
		}
	})
}

// appendLinkEarlierShape builds the shape the model actually produces when
// it works hub-first: op1 patches the existing page P to add [[<created
// slug>]] while the page the link names does not exist yet (append-time
// validation does not reject a not-yet-resolving link — link-broken is a
// lint finding), then op2 create_page brings it into existence LATER in
// changeset order. It returns the two op ids.
func appendLinkEarlierShape(t *testing.T, e *Engine) (patchID, createID string) {
	t.Helper()
	if _, err := e.OpenChangeset("link earlier than create", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	op1, _ := dependentPatch(t, chainedTargetPath, seededBody(t, e, chainedTargetPath),
		"See [[early-created]].")
	patchID, err := e.Append(op1)
	if err != nil {
		t.Fatalf("Append earlier linking patch: %v", err)
	}
	createID = mustCreatePage(t, e, "wiki/concepts/early-created.md",
		createdNoteContent("Early Created", ""), []string{"raw/papers/leviathan-2023.md"})
	return patchID, createID
}

// seededBody returns the canonical bytes of the committed page at path —
// the base every unchained fixture patch in this file builds on.
func seededBody(t *testing.T, e *Engine, path string) []byte {
	t.Helper()
	page, ok := e.Vault().Page(path)
	if !ok {
		t.Fatalf("fixture missing %s", path)
	}
	return page.Serialize()
}

func TestDropRestoreOps(t *testing.T) {
	t.Run("drop_chain_commits_clean", func(t *testing.T) {
		e, dir := newDropCascadeEngine(t)
		patches := chainWithIngest(t, e, 6) // op2..op7 on chainedTargetPath

		c, err := e.Current()
		if err != nil {
			t.Fatalf("Current: %v", err)
		}
		live3, ok := c.Op(patches[1]) // op3
		if !ok {
			t.Fatalf("changeset lost %s", patches[1])
		}
		after3 := live3.After

		// patches[0..5] are op2..op7; drop the tail op4..op7.
		if err := e.DropOps([]string{patches[2], patches[3], patches[4], patches[5]}); err != nil {
			t.Fatalf("DropOps(op4..op7): %v", err)
		}

		c, err = e.Current()
		if err != nil {
			t.Fatalf("Current after DropOps: %v", err)
		}
		if n := staleCount(c); n != 0 {
			t.Errorf("%d stale ops after dropping the chain tail; want 0", n)
		}

		if _, err := e.Commit("drop the dependent tail"); err != nil {
			t.Fatalf("Commit after dropping op4..op7: %v", err)
		}
		disk, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(chainedTargetPath)))
		if err != nil {
			t.Fatalf("read committed page: %v", err)
		}
		want, err := e.store.Get(after3)
		if err != nil {
			t.Fatalf("store.Get(op3.After): %v", err)
		}
		if !bytes.Equal(disk, want) {
			t.Errorf("committed page is not op3's After blob:\n%s\nwant:\n%s", disk, want)
		}
	})

	t.Run("restore_prerequisites", func(t *testing.T) {
		e, _ := newDropCascadeEngine(t)
		patches := chainWithIngest(t, e, 3) // op2, op3, op4

		before := diffForPath(t, e, chainedTargetPath)

		if err := e.DropOps([]string{patches[0], patches[1], patches[2]}); err != nil {
			t.Fatalf("DropOps(op2..op4): %v", err)
		}

		prereqs, err := e.OpPrerequisites(patches[2])
		if err != nil {
			t.Fatalf("OpPrerequisites(%s): %v", patches[2], err)
		}
		want := []string{patches[0], patches[1]}
		if !reflect.DeepEqual(prereqs, want) {
			t.Errorf("OpPrerequisites(%s) = %v; want %v", patches[2], prereqs, want)
		}

		if err := e.RestoreOps([]string{patches[0], patches[1], patches[2]}); err != nil {
			t.Fatalf("RestoreOps(op2..op4): %v", err)
		}

		c, err := e.Current()
		if err != nil {
			t.Fatalf("Current after RestoreOps: %v", err)
		}
		for _, id := range patches {
			op, ok := c.Op(id)
			if !ok {
				t.Fatalf("changeset lost %s", id)
			}
			if op.State != StateProposed {
				t.Errorf("%s state = %s; want proposed", id, op.State)
			}
		}
		if n := staleCount(c); n != 0 {
			t.Errorf("%d stale ops after restore; want 0", n)
		}

		after := diffForPath(t, e, chainedTargetPath)
		if !reflect.DeepEqual(before, after) {
			t.Errorf("Diff for %s changed across drop+restore:\nbefore: %+v\nafter:  %+v",
				chainedTargetPath, before, after)
		}

		events, err := e.Journal().Query(Filter{Kinds: []EventKind{EvOpRestored}})
		if err != nil {
			t.Fatalf("journal query: %v", err)
		}
		if len(events) != 3 {
			t.Fatalf("%d op_restored events; want 3", len(events))
		}
		restored := map[string]bool{}
		for _, ev := range events {
			restored[ev.Op] = true
		}
		for _, id := range patches {
			if !restored[id] {
				t.Errorf("no op_restored event for %s", id)
			}
		}
	})

	t.Run("restore_pulls_later_prerequisite", func(t *testing.T) {
		e, _ := newDropCascadeEngine(t)
		patchID, createID := appendLinkEarlierShape(t, e)

		if err := e.DropOps([]string{patchID, createID}); err != nil {
			t.Fatalf("DropOps(linking patch, create): %v", err)
		}

		// The mirror in the direction deviation 4 lost: the dropped patch
		// is EARLIER, yet the create it newly links is its prerequisite —
		// restoring the patch without the create would come back linking a
		// page that does not exist.
		prereqs, err := e.OpPrerequisites(patchID)
		if err != nil {
			t.Fatalf("OpPrerequisites(%s): %v", patchID, err)
		}
		want := []string{createID}
		if !reflect.DeepEqual(prereqs, want) {
			t.Errorf("OpPrerequisites(%s) = %v; want %v (the LATER create)", patchID, prereqs, want)
		}

		if err := e.RestoreOps([]string{patchID, createID}); err != nil {
			t.Fatalf("RestoreOps(linking patch, create): %v", err)
		}

		c, err := e.Current()
		if err != nil {
			t.Fatalf("Current after RestoreOps: %v", err)
		}
		for _, id := range []string{patchID, createID} {
			op, ok := c.Op(id)
			if !ok {
				t.Fatalf("changeset lost %s", id)
			}
			if op.State != StateProposed {
				t.Errorf("%s state = %s; want proposed", id, op.State)
			}
		}
		if n := staleCount(c); n != 0 {
			t.Errorf("%d stale ops after restore; want 0", n)
		}
	})

	t.Run("unknown_id_changes_nothing", func(t *testing.T) {
		e, _ := newDropCascadeEngine(t)
		patches := chainWithIngest(t, e, 3)

		c, err := e.Current()
		if err != nil {
			t.Fatalf("Current: %v", err)
		}
		csPath := filepath.Join(e.changesetOpenDir(), c.ID, "changeset.json")
		before, err := os.ReadFile(csPath)
		if err != nil {
			t.Fatalf("read changeset.json: %v", err)
		}

		err = e.DropOps([]string{patches[0], "op99"})
		if err == nil {
			t.Fatal("DropOps with an unknown id = nil error; want refusal")
		}
		if !strings.Contains(err.Error(), "op99") {
			t.Errorf("error %v does not name op99", err)
		}

		after, err := os.ReadFile(csPath)
		if err != nil {
			t.Fatalf("read changeset.json after refusal: %v", err)
		}
		if !bytes.Equal(before, after) {
			t.Errorf("changeset.json changed under a refused DropOps call")
		}

		c, err = e.Current()
		if err != nil {
			t.Fatalf("Current: %v", err)
		}
		for _, id := range patches {
			op, ok := c.Op(id)
			if !ok {
				t.Fatalf("changeset lost %s", id)
			}
			if op.State == StateDropped {
				t.Errorf("%s was dropped by a refused DropOps call", id)
			}
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		e, _ := newDropCascadeEngine(t)
		patches := chainWithIngest(t, e, 3)

		if err := e.DropOps([]string{patches[0]}); err != nil {
			t.Fatalf("DropOps(%s): %v", patches[0], err)
		}
		c, _ := e.Current()
		if op, _ := c.Op(patches[0]); op.State != StateDropped {
			t.Fatalf("%s state = %s; want dropped", patches[0], op.State)
		}

		// Already-dropped: a no-op that changes nothing — no second
		// op_dropped event, no state move.
		if err := e.DropOps([]string{patches[0]}); err != nil {
			t.Fatalf("DropOps on an already-dropped op: %v", err)
		}
		c, _ = e.Current()
		if op, _ := c.Op(patches[0]); op.State != StateDropped {
			t.Errorf("%s state = %s after the no-op drop; want still dropped", patches[0], op.State)
		}
		drops, err := e.Journal().Query(Filter{Kinds: []EventKind{EvOpDropped}})
		if err != nil {
			t.Fatalf("journal query: %v", err)
		}
		if len(drops) != 1 {
			t.Errorf("%d op_dropped events after the no-op re-drop; want 1", len(drops))
		}

		if err := e.RestoreOps([]string{patches[0]}); err != nil {
			t.Fatalf("RestoreOps(%s): %v", patches[0], err)
		}
		// Already live: a no-op that changes nothing.
		if err := e.RestoreOps([]string{patches[0]}); err != nil {
			t.Fatalf("RestoreOps on a live op: %v", err)
		}
		c, _ = e.Current()
		if op, _ := c.Op(patches[0]); op.State != StateProposed {
			t.Errorf("%s state = %s after the no-op restore; want proposed", patches[0], op.State)
		}
		restores, err := e.Journal().Query(Filter{Kinds: []EventKind{EvOpRestored}})
		if err != nil {
			t.Fatalf("journal query: %v", err)
		}
		if len(restores) != 1 {
			t.Errorf("%d op_restored events after the no-op re-restore; want 1", len(restores))
		}
	})
}
