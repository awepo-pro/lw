// chained_ops_test.go pins workflow 020 T-A: within one changeset, a second
// content op on a path COMPOSES with the first — Append validates a
// patch_page/create_page against the projection of the changeset's own live
// ops (cascadeBase), not the committed vault, so a chained op's Before is
// the staged sha and an unchained Before is refused.
//
// Live-proven failure this closes (014 §9 A10): every stage.patch_page
// computed its after-image from the COMMITTED page, so multi-op paths all
// shared one before sha and committed duplicated sections. Dependent ops
// (insert abstract → remove old copy) were structurally impossible.
//
// Every test here was written BEFORE the engine change and proved to fail
// against the unchanged engine (fail-first); the red state is captured in
// runs/T-A/report.md.
package stage

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// chainedTargetPath is the page newChainedEngine seeds into a private copy
// of the minimal fixture: a well-formed concept page whose body carries
// exactly the two sections the compose scenarios reshuffle.
const chainedTargetPath = "wiki/concepts/chain-target.md"

// chainedCreatePath is the target of the create-then-patch scenarios — a
// path that exists ONLY once the create_page op is live, which is what makes
// a follow-up patch_page provably read the staged state.
const chainedCreatePath = "wiki/concepts/chained-create.md"

// chainedTargetSeed returns the on-disk bytes newChainedEngine writes:
// frontmatter valid against the minimal fixture's schema, an intro linking
// two existing pages, then ## Alpha and ## Beta. Beta is the trailing
// section so removing it is a clean truncation, and "## Alpha" appears once
// so inserting Gamma before it is one Replace.
func chainedTargetSeed() []byte {
	return []byte("---\n" +
		"title: Chain Target\n" +
		"created: 2026-09-21\n" +
		"updated: 2026-09-21\n" +
		"type: concept\n" +
		"tags: [inference]\n" +
		"confidence: medium\n" +
		"---\n" +
		"\n" +
		"# Chain Target\n" +
		"\n" +
		"Intro sentence linking [[kv-cache]] and [[gpt-4]].\n" +
		"\n" +
		"## Alpha\n" +
		"\n" +
		"Alpha body.\n" +
		"\n" +
		"## Beta\n" +
		"\n" +
		"Beta body.\n")
}

// newChainedEngine opens an Engine over a private copy of the minimal
// fixture with chainedTargetSeed written at chainedTargetPath — the same
// CopyFixture-then-write pattern apply_test.go's hand-edit simulation uses,
// applied BEFORE OpenEngine so the vault loads the seed as committed state.
func newChainedEngine(t *testing.T) (*Engine, string) {
	t.Helper()
	dir := testutil.CopyFixture(t, "minimal")
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(chainedTargetPath)), chainedTargetSeed(), 0o644); err != nil {
		t.Fatalf("seed %s: %v", chainedTargetPath, err)
	}
	e, err := OpenEngine(dir)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	e.now = testutil.FixedClock()
	return e, dir
}

// removeBeta returns the post-image of a remove_section Beta op over the
// committed chainedTargetPath page: the body with its trailing ## Beta
// section cut, re-serialized canonically. This is patch1 of every chained
// pair — the op whose After the second op chains on.
func removeBeta(t *testing.T, e *Engine) (Op, []byte) {
	t.Helper()
	page, ok := e.Vault().Page(chainedTargetPath)
	if !ok {
		t.Fatalf("fixture missing %s", chainedTargetPath)
	}
	cut := strings.Index(page.Body, "## Beta")
	if cut < 0 {
		t.Fatalf("seeded body has no ## Beta section:\n%s", page.Body)
	}
	stripped := *page
	stripped.Body = strings.TrimRight(page.Body[:cut], "\n") + "\n"
	content := stripped.Serialize()
	return Op{
		Kind:    OpPatchPage,
		Path:    chainedTargetPath,
		Section: "## Beta",
		Before:  page.SHA256(),
		Content: content,
		Hunks:   []Hunk{{ID: "h1", Path: chainedTargetPath, Section: "## Beta", Del: []string{"## Beta", "", "Beta body."}}},
	}, content
}

// insertGammaBeforeAlpha returns the post-image of an insert_before Alpha
// op over the ALREADY-BETA-LESS body bytes — the tool-side shape T-B will
// compute from staged state; here it stands in for it by construction.
func insertGammaBeforeAlpha(t *testing.T, base []byte) (Op, []byte) {
	t.Helper()
	page, err := vault.ParsePage(chainedTargetPath, base)
	if err != nil {
		t.Fatalf("parse staged body: %v", err)
	}
	inserted := *page
	inserted.Body = strings.Replace(page.Body,
		"## Alpha", "## Gamma\n\nGamma body.\n\n## Alpha", 1)
	content := inserted.Serialize()
	return Op{
		Kind:    OpPatchPage,
		Path:    chainedTargetPath,
		Section: "## Alpha",
		Content: content,
		Hunks:   []Hunk{{ID: "h1", Path: chainedTargetPath, Section: "## Alpha", Add: []string{"## Gamma", "", "Gamma body.", ""}}},
	}, content
}

// appendChainedPair appends patch1 (remove_section Beta, Before = the
// committed sha) and patch2 (insert_before Alpha, Before = patch1's staged
// After sha, read back through Current) and returns their op ids. Every
// chained-op test starts from this pair; only what it asserts afterwards
// differs.
func appendChainedPair(t *testing.T, e *Engine) (string, string) {
	t.Helper()
	patch1, p1Content := removeBeta(t, e)
	op1, err := e.Append(patch1)
	if err != nil {
		t.Fatalf("Append patch1 (remove Beta): %v", err)
	}
	c, err := e.Current()
	if err != nil {
		t.Fatalf("Current after patch1: %v", err)
	}
	live1, ok := c.Op(op1)
	if !ok {
		t.Fatalf("changeset lost %s", op1)
	}

	patch2, _ := insertGammaBeforeAlpha(t, p1Content)
	patch2.Before = live1.After
	op2, err := e.Append(patch2)
	if err != nil {
		t.Fatalf("Append patch2 (insert Gamma, chained Before): %v", err)
	}
	return op1, op2
}

// TestAppendChainedPatchesCompose is the headline scenario: remove Beta,
// then insert Gamma before Alpha, both in one changeset. Pre-020 the second
// Append was refused — its chained Before never matched the committed sha —
// and had it been forced through, both ops would have committed from one
// shared before-image with Beta duplicated back in. Post-T-A both Append,
// the projection carries patch1's post-image into patch2's validation, and
// Commit writes the composition: Gamma before Alpha, no Beta anywhere.
func TestAppendChainedPatchesCompose(t *testing.T) {
	e, dir := newChainedEngine(t)
	if _, err := e.OpenChangeset("chained patches", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	patch1, p1Content := removeBeta(t, e)
	op1, err := e.Append(patch1)
	if err != nil {
		t.Fatalf("Append patch1: %v", err)
	}

	// StagedFile must already read the P−Beta bytes: it is the read seam a
	// T-B tool uses to compute the next op's before-image from staged state
	// instead of re-reading the committed page.
	staged, found, err := e.StagedFile(chainedTargetPath)
	if err != nil {
		t.Fatalf("StagedFile: %v", err)
	}
	if !found {
		t.Fatal("StagedFile reported nothing staged for the just-appended patch")
	}
	if !bytes.Equal(staged, p1Content) {
		t.Errorf("StagedFile = %q; want the P−Beta post-image %q", staged, p1Content)
	}

	c, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	live1, _ := c.Op(op1)
	patch2, _ := insertGammaBeforeAlpha(t, p1Content)
	patch2.Before = live1.After
	if _, err := e.Append(patch2); err != nil {
		t.Fatalf("Append patch2 with the staged Before: %v", err)
	}

	if _, err := e.Commit("remove Beta, insert Gamma before Alpha"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	onDisk, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(chainedTargetPath)))
	if err != nil {
		t.Fatalf("read committed page: %v", err)
	}
	body := string(onDisk)
	if i, j := strings.Index(body, "## Gamma"), strings.Index(body, "## Alpha"); i < 0 || j < 0 || i > j {
		t.Errorf("committed page does not have Gamma before Alpha (gamma=%d alpha=%d):\n%s", i, j, body)
	}
	if strings.Contains(body, "## Beta") || strings.Contains(body, "Beta body.") {
		t.Errorf("committed page still carries Beta content:\n%s", body)
	}
}

// TestAppendRejectsUnchainedBeforeOnStagedPath pins the refusal that makes
// chaining safe rather than merely possible: once a live op stages new
// content at a path, a second patch whose Before is still the COMMITTED sha
// is rejected with the existing before-mismatch error. The proposing agent
// self-corrects by re-reading the staged sha (StagedFile / T-B), instead of
// smuggling two independently-computed post-images past validation.
func TestAppendRejectsUnchainedBeforeOnStagedPath(t *testing.T) {
	e, _ := newChainedEngine(t)
	if _, err := e.OpenChangeset("unchained before", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	patch1, p1Content := removeBeta(t, e)
	if _, err := e.Append(patch1); err != nil {
		t.Fatalf("Append patch1: %v", err)
	}

	// patch2 computed the old way — from the committed page — so its Before
	// is patch1's Before, not patch1's After.
	patch2, _ := insertGammaBeforeAlpha(t, p1Content)
	patch2.Before = patch1.Before
	_, err := e.Append(patch2)
	if err == nil {
		t.Fatal("Append accepted an unchained Before on a staged path; want refusal")
	}
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Append error = %v; want ErrValidation", err)
	}
	if !strings.Contains(err.Error(), "before does not match") {
		t.Errorf("error does not name the before mismatch: %v", err)
	}
}

// TestAppendCreateThenPatchComposes covers the other chaining shape: a page
// that exists ONLY in the projection. Pre-020 the follow-up patch_page was
// refused outright ("does not exist" — validatePatchPage read the committed
// vault), so a create could never be corrected in the same changeset.
func TestAppendCreateThenPatchComposes(t *testing.T) {
	e, dir := newChainedEngine(t)
	if _, err := e.OpenChangeset("create then patch", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	created, err := vault.ParsePage(chainedCreatePath, []byte("---\n"+
		"title: Chained Create\n"+
		"created: 2026-09-21\n"+
		"updated: 2026-09-21\n"+
		"type: concept\n"+
		"tags: [inference]\n"+
		"confidence: medium\n"+
		"---\n"+
		"\n"+
		"# Chained Create\n"+
		"\n"+
		"Fresh page linking [[kv-cache]] and [[gpt-4]].\n"+
		"\n"+
		"## Related\n"+
		"\n"+
		"- [[kv-cache]] — background.\n"))
	if err != nil {
		t.Fatalf("build create content: %v", err)
	}
	if _, err := e.Append(Op{
		Kind:       OpCreatePage,
		Path:       chainedCreatePath,
		Content:    created.Serialize(),
		Rationale:  "chaining fixture",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	}); err != nil {
		t.Fatalf("Append create_page: %v", err)
	}

	// The patch chains on the staged create: Before is the sha of the page
	// the projection now holds at the path.
	patched := *created
	patched.Body = strings.Replace(created.Body,
		"- [[kv-cache]] — background.", "- [[kv-cache]] — updated background.", 1)
	if _, err := e.Append(Op{
		Kind:    OpPatchPage,
		Path:    chainedCreatePath,
		Section: "## Related",
		Before:  created.SHA256(),
		Content: patched.Serialize(),
		Hunks: []Hunk{{
			ID:      "h1",
			Path:    chainedCreatePath,
			Section: "## Related",
			Del:     []string{"- [[kv-cache]] — background."},
			Add:     []string{"- [[kv-cache]] — updated background."},
		}},
	}); err != nil {
		t.Fatalf("Append patch_page on the staged create: %v", err)
	}

	c, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if live := c.Live(); len(live) != 2 {
		t.Fatalf("live ops = %d; want both the create and the patch live", len(live))
	}

	if _, err := e.Commit("create then patch"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	onDisk, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(chainedCreatePath)))
	if err != nil {
		t.Fatalf("read committed page: %v", err)
	}
	if !bytes.Equal(onDisk, patched.Serialize()) {
		t.Errorf("committed page is not the patched create:\n%s", onDisk)
	}
}

// TestAppendRefusesDuplicateCreateOnStagedPath closes F-4's original
// two-creates report from the engine side: create_page validating against
// the projection sees its own earlier create's staged path as existing, so
// the second create is refused with the existing already-exists error and
// the agent self-corrects to patch_page.
func TestAppendRefusesDuplicateCreateOnStagedPath(t *testing.T) {
	e, _ := newChainedEngine(t)
	if _, err := e.OpenChangeset("duplicate create", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	content := newConceptPageContent("Chained Create Dup")
	first := Op{
		Kind:       OpCreatePage,
		Path:       chainedCreatePath,
		Content:    content,
		Rationale:  "first create",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	}
	if _, err := e.Append(first); err != nil {
		t.Fatalf("Append create_page #1: %v", err)
	}

	_, err := e.Append(first)
	if err == nil {
		t.Fatal("Append accepted a second create_page on a staged path; want refusal")
	}
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Append error = %v; want ErrValidation", err)
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error does not say the path already exists: %v", err)
	}
}
