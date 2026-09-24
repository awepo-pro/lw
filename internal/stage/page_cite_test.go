// page_cite_test.go pins workflow 034 T3's stage side: the drop cascade's
// D3 relation recognizes paged markers — ^[… p.N], not just ^[…[:] —
// through cite.Scan, and Append refuses a create_page/patch_page whose NEW
// page citations the engine cannot resolve, while leaving every marker the
// pre-image already carried untouched (legacy tolerance: 034 only ever
// ADDS the page suffix, so pages written before it must keep patching).
package stage

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// pagedRawBody returns a raw source body with page anchors 1..n — the
// shape internal/extract's PDF ingest writes, one whole-line
// "<!-- page N -->" anchor per physical page.
func pagedRawBody(n int) string {
	var sb strings.Builder
	sb.WriteString("# Paged Fixture\n")
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb, "\n<!-- page %d -->\n\nPage %d text.\n", i, i)
	}
	return sb.String()
}

// TestDropIngestCascadesPageCitedPatch pins D3 through cite.Scan: a patch
// whose post-image carries a PAGED marker (^[… p.2]) and no provenance
// field still depends on the ingest of the source it names, so dropping
// the ingest takes the patch with it.
func TestDropIngestCascadesPageCitedPatch(t *testing.T) {
	e, _ := newDropCascadeEngine(t)
	if _, err := e.OpenChangeset("paged citation d3", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	const paged = "raw/papers/x.md"
	ingestID := stageIngest(t, e, paged, pagedRawBody(3))

	// The paged marker is the patch's ONLY tie to the ingest: no
	// Provenance, no frontmatter sources:, no unpaged marker.
	patch, _ := dependentPatch(t, chainedTargetPath, seededBody(t, e, chainedTargetPath),
		"Supporting claim.^[raw/papers/x.md p.2]")
	patchID, err := e.Append(patch)
	if err != nil {
		t.Fatalf("Append paged-citing patch: %v", err)
	}

	deps, err := e.OpDependents(ingestID)
	if err != nil {
		t.Fatalf("OpDependents(%s): %v", ingestID, err)
	}
	if want := []string{patchID}; !reflect.DeepEqual(deps, want) {
		t.Errorf("OpDependents(%s) = %v; want %v", ingestID, deps, want)
	}

	// The armed drop, exactly as the Review UI makes it (dropop.go):
	// OpDependents first, then DropOps over the whole doomed set — the
	// ingest's drop must leave the citing patch dropped too.
	if err := e.DropOps([]string{ingestID, patchID}); err != nil {
		t.Fatalf("DropOps(ingest, patch): %v", err)
	}
	c, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	op, ok := c.Op(patchID)
	if !ok {
		t.Fatalf("changeset lost %s", patchID)
	}
	if op.State != StateDropped {
		t.Errorf("%s state = %s; want dropped with its ingest", patchID, op.State)
	}
}

// TestStageRefusesBadPageCite pins the four refusal reasons of 034 T3's
// stage-time citation check, one row per reason, each with its exact
// message: a create_page whose post-image NEWLY carries an unresolvable
// paged marker is refused at proposal time instead of surfacing later as
// an ask-time miss.
func TestStageRefusesBadPageCite(t *testing.T) {
	const pagedPath = "wiki/concepts/paged-cite.md"
	rows := []struct {
		name   string
		marker string
		staged bool   // stage the 3-page raw/papers/x.md ingest first
		want   string // the exact refusal, after ErrValidation's text
	}{
		{
			name:   "page past the last anchor",
			marker: "^[raw/papers/x.md p.9]",
			staged: true,
			want:   "create_page: " + pagedPath + ": ^[raw/papers/x.md p.9]: raw/papers/x.md has pages 1-3",
		},
		{
			name:   "source has no page anchors",
			marker: "^[raw/articles/kv-cache-explained.md p.1]",
			want:   "create_page: " + pagedPath + ": ^[raw/articles/kv-cache-explained.md p.1]: raw/articles/kv-cache-explained.md has no page anchors; cite it without a page",
		},
		{
			name:   "malformed page part",
			marker: "^[raw/papers/x.md pp.2]",
			want:   `create_page: ` + pagedPath + `: ^[raw/papers/x.md pp.2]: malformed page citation "^[raw/papers/x.md pp.2]": write ^[<source> p.N] or ^[<source> p.N-M]`,
		},
		{
			name:   "source does not exist",
			marker: "^[raw/papers/nope.md p.1]",
			want:   "create_page: " + pagedPath + ": ^[raw/papers/nope.md p.1]: raw/papers/nope.md does not exist",
		},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			e, _ := newTestEngine(t)
			if _, err := e.OpenChangeset("bad page cite", testAuthor); err != nil {
				t.Fatalf("OpenChangeset: %v", err)
			}
			if row.staged {
				stageIngest(t, e, "raw/papers/x.md", pagedRawBody(3))
			}
			_, err := e.Append(Op{
				Kind:       OpCreatePage,
				Path:       pagedPath,
				Content:    createdNoteContent("Paged Cite", "Claim."+row.marker),
				Rationale:  "034 T3 refusal fixture",
				Provenance: []string{"raw/papers/leviathan-2023.md"},
			})
			if err == nil {
				t.Fatalf("Append create carrying %s = nil error; want refusal", row.marker)
			}
			if !errors.Is(err, ErrValidation) {
				t.Fatalf("Append error %v does not wrap ErrValidation", err)
			}
			if want := "stage: op failed validation: " + row.want; err.Error() != want {
				t.Errorf("refusal =\n\t%q\nwant\n\t%q", err.Error(), want)
			}
		})
	}
}

// TestStageAcceptsPageCite pins the happy path: a paged RANGE citation of
// a staged source whose anchors cover it appends cleanly.
func TestStageAcceptsPageCite(t *testing.T) {
	e, _ := newTestEngine(t)
	if _, err := e.OpenChangeset("paged range cite", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	stageIngest(t, e, "raw/papers/x.md", pagedRawBody(3))
	if _, err := e.Append(Op{
		Kind:       OpCreatePage,
		Path:       "wiki/concepts/ranged-cite.md",
		Content:    createdNoteContent("Ranged Cite", "Claim.^[raw/papers/x.md p.2-3]"),
		Rationale:  "034 T3 acceptance fixture",
		Provenance: []string{"raw/papers/x.md"},
	}); err != nil {
		t.Fatalf("Append create with ^[raw/papers/x.md p.2-3]: %v", err)
	}
}

// legacyCiteSeed returns the on-disk bytes of a page that already carries
// a page citation no engine could resolve — written before 034's
// stage-time check existed. Its "## Alpha" section matches the shape
// dependentPatch patches.
func legacyCiteSeed() []byte {
	return []byte("---\n" +
		"title: Legacy Cite\n" +
		"created: 2026-09-21\n" +
		"updated: 2026-09-21\n" +
		"type: concept\n" +
		"tags: [inference]\n" +
		"confidence: medium\n" +
		"---\n" +
		"\n" +
		"# Legacy Cite\n" +
		"\n" +
		"Carried over with ^[raw/x.md p.99] already in it.\n" +
		"\n" +
		"## Alpha\n" +
		"\n" +
		"Alpha body.\n")
}

// TestPatchKeepsLegacyBadCite pins the legacy tolerance: a page that
// ALREADY carries an unresolvable paged marker still accepts a patch that
// leaves the marker untouched — the check refuses only citations a patch
// newly introduces, or every pre-034 page would be unpatchable until
// someone hand-fixes markers the engine itself once accepted.
func TestPatchKeepsLegacyBadCite(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	const legacyPath = "wiki/concepts/legacy-cite.md"
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(legacyPath)), legacyCiteSeed(), 0o644); err != nil {
		t.Fatalf("seed %s: %v", legacyPath, err)
	}
	e, err := OpenEngine(dir)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	e.now = testutil.FixedClock()
	if _, err := e.OpenChangeset("legacy cite patch", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	patch, _ := dependentPatch(t, legacyPath, legacyCiteSeed(), "Patched on top of the legacy marker.")
	if _, err := e.Append(patch); err != nil {
		t.Fatalf("Append patch leaving the legacy marker untouched: %v", err)
	}
}

// noncanonicalKVCache reads the noncanonical twin of minimal's
// wiki/concepts/kv-cache.md from spec/fixtures/noncanonical (MAPPING.md
// pairs them): the same page, raw bytes that do not re-serialize to
// themselves — a quoted confidence with trailing whitespace, blank lines
// after the closing ---.
func noncanonicalKVCache(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(testutil.FixtureRoot(t),
		"noncanonical", "wiki", "concepts", "kv-cache.md"))
	if err != nil {
		t.Fatalf("read noncanonical twin of kv-cache.md: %v", err)
	}
	return b
}

// TestPatchKeepsLegacyBadCiteOnNoncanonicalPage pins the legacy tolerance
// on a page whose raw bytes are NOT canonical: the pre-image the cite
// check reads is page.Serialize() — frontmatter re-rendered canonically —
// so a marker already in the body must survive that re-serialization to
// count as pre-existing, while a NEW bad marker on the same page is still
// refused. spec/fixtures/noncanonical is the standing fixture for exactly
// this: bytes ParsePage accepts and Serialize normalizes.
func TestPatchKeepsLegacyBadCiteOnNoncanonicalPage(t *testing.T) {
	const p = "wiki/concepts/kv-cache.md"
	// The Abstract section's last sentence; "## Why it matters" repeats it
	// later, so the replace below must hit the first occurrence.
	const anchor = "memory that grows with sequence length."

	seedEngine := func(t *testing.T, seed []byte) *Engine {
		t.Helper()
		dir := testutil.CopyFixture(t, "minimal")
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(p)), seed, 0o644); err != nil {
			t.Fatalf("seed %s: %v", p, err)
		}
		e, err := OpenEngine(dir)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		t.Cleanup(func() { e.Close() })
		e.now = testutil.FixedClock()
		if _, err := e.OpenChangeset("noncanonical page cite patch", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		return e
	}

	t.Run("legacy marker stays patchable", func(t *testing.T) {
		seed := bytes.Replace(noncanonicalKVCache(t), []byte(anchor),
			[]byte(anchor+" Carried over with ^[raw/x.md p.99] already in it."), 1)
		canon, err := vault.Canonical(p, seed)
		if err != nil {
			t.Fatalf("canonicalize seed: %v", err)
		}
		if bytes.Equal(seed, canon) {
			t.Fatal("twin bytes re-serialize to themselves; the test no longer exercises a noncanonical page")
		}
		e := seedEngine(t, seed)

		// Before is the canonical sha the engine expects — page.SHA256()
		// of the parsed seed, not the raw bytes' sha — exactly the value a
		// client reading the projection computes.
		page, err := vault.ParsePage(p, seed)
		if err != nil {
			t.Fatalf("parse seed: %v", err)
		}
		patched := *page
		patched.Body = strings.Replace(page.Body,
			anchor+" Carried over with ^[raw/x.md p.99] already in it.",
			anchor+" Carried over with ^[raw/x.md p.99] already in it.\n\nPatched on top of the legacy marker.", 1)
		op := Op{
			Kind:    OpPatchPage,
			Path:    p,
			Section: "## Abstract",
			Before:  page.SHA256(),
			Content: patched.Serialize(),
		}
		if _, err := e.Append(op); err != nil {
			t.Fatalf("Append patch leaving the noncanonical page's legacy marker untouched: %v", err)
		}
	})

	t.Run("new bad marker still refused", func(t *testing.T) {
		e := seedEngine(t, noncanonicalKVCache(t))
		page, err := vault.ParsePage(p, noncanonicalKVCache(t))
		if err != nil {
			t.Fatalf("parse seed: %v", err)
		}
		patched := *page
		patched.Body = strings.Replace(page.Body, anchor,
			anchor+" New claim.^[raw/papers/nope.md p.1]", 1)
		op := Op{
			Kind:    OpPatchPage,
			Path:    p,
			Section: "## Abstract",
			Before:  page.SHA256(),
			Content: patched.Serialize(),
		}
		_, err = e.Append(op)
		if err == nil {
			t.Fatalf("Append patch adding ^[raw/papers/nope.md p.1] = nil error; want refusal")
		}
		if !errors.Is(err, ErrValidation) {
			t.Fatalf("Append error %v does not wrap ErrValidation", err)
		}
		if want := "stage: op failed validation: patch_page: " + p + ": ^[raw/papers/nope.md p.1]: raw/papers/nope.md does not exist"; err.Error() != want {
			t.Errorf("refusal =\n\t%q\nwant\n\t%q", err.Error(), want)
		}
	})
}

// TestAppendRefusesCallerSuppliedCascade pins the seam the new-cite check
// would otherwise leak through: storeOpContent and planOp recurse into
// op.Cascade for EVERY kind, but only rename_page and merge_pages have
// Append rebuild that Cascade — so a patch_page riding a create_page as a
// caller-supplied cascade sub-op would land its bytes in the vault at
// Commit past every per-kind check, the bad paged marker included
// (confirmed by probe before the guard existed). A Cascade is only
// honored where Append builds it; every other kind's edits travel as
// sibling top-level ops (backbone §5.4 D-AZ/D-AK).
func TestAppendRefusesCallerSuppliedCascade(t *testing.T) {
	e, _ := newDropCascadeEngine(t)
	if _, err := e.OpenChangeset("caller cascade refused", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	page, err := vault.ParsePage(chainedTargetPath, seededBody(t, e, chainedTargetPath))
	if err != nil {
		t.Fatalf("parse seed: %v", err)
	}
	patched := *page
	patched.Body += "\nRogue.^[raw/nope.md p.9]\n"
	_, err = e.Append(Op{
		Kind:       OpCreatePage,
		Path:       "wiki/concepts/cascade-carrier.md",
		Content:    createdNoteContent("Cascade Carrier", "Body with [[kv-cache]] and [[flash-attention]] links."),
		Rationale:  "034 T3 cascade-seam fixture",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
		Cascade: []Op{{
			Kind:    OpPatchPage,
			Path:    chainedTargetPath,
			Section: "## Alpha",
			Before:  page.SHA256(),
			Content: patched.Serialize(),
		}},
	})
	if err == nil {
		t.Fatal("Append create carrying a caller-supplied cascade = nil error; want refusal")
	}
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Append error %v does not wrap ErrValidation", err)
	}
	if want := "stage: op failed validation: create_page: cascade is not accepted; only rename_page and merge_pages carry one, and Append builds it itself"; err.Error() != want {
		t.Errorf("refusal =\n\t%q\nwant\n\t%q", err.Error(), want)
	}
}
