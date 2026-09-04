package stage

import (
	"errors"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// newTestVault loads a private, read-only copy of the minimal fixture
// vault for tests that only need ValidateOp/*.Vault, not a full Engine.
func newTestVault(t *testing.T) *vault.Vault {
	t.Helper()
	dir := testutil.CopyFixture(t, "minimal")
	v, err := vault.Open(dir)
	if err != nil {
		t.Fatalf("vault.Open: %v", err)
	}
	return v
}

func wantValidationError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("ValidateOp: got nil error, want one wrapping ErrValidation")
	}
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("ValidateOp: got %v, want an error wrapping ErrValidation", err)
	}
}

// TestValidateOp covers every §5.5 malformation bullet, one PASS and at
// least one FAIL per rule.
func TestValidateOp(t *testing.T) {
	t.Run("generic path convention", func(t *testing.T) {
		v := newTestVault(t)
		op := Op{
			Kind:       OpCreatePage,
			Path:       "wiki/concepts/Bad_Name.md",
			Content:    newConceptPageContent("Bad Name"),
			Rationale:  "test",
			Provenance: []string{"raw/papers/leviathan-2023.md"},
		}
		wantValidationError(t, ValidateOp(op, v, v.Schema()))
	})

	t.Run("unknown op kind", func(t *testing.T) {
		v := newTestVault(t)
		op := Op{Kind: OpKind("frobnicate_page"), Path: "wiki/concepts/x.md"}
		wantValidationError(t, ValidateOp(op, v, v.Schema()))
	})

	// --- create_page -------------------------------------------------

	t.Run("create_page valid", func(t *testing.T) {
		v := newTestVault(t)
		op := Op{
			Kind:       OpCreatePage,
			Path:       "wiki/concepts/new-concept.md",
			Content:    newConceptPageContent("New Concept"),
			Rationale:  "central to the sources",
			Provenance: []string{"raw/papers/leviathan-2023.md"},
		}
		if err := ValidateOp(op, v, v.Schema()); err != nil {
			t.Fatalf("ValidateOp: unexpected error: %v", err)
		}
	})

	t.Run("create_page path already exists", func(t *testing.T) {
		v := newTestVault(t)
		op := Op{
			Kind:       OpCreatePage,
			Path:       "wiki/concepts/kv-cache.md",
			Content:    newConceptPageContent("KV Cache"),
			Rationale:  "test",
			Provenance: []string{"raw/papers/leviathan-2023.md"},
		}
		wantValidationError(t, ValidateOp(op, v, v.Schema()))
	})

	t.Run("create_page invalid frontmatter", func(t *testing.T) {
		v := newTestVault(t)
		content := []byte("---\n" +
			"created: 2026-08-29\n" +
			"updated: 2026-08-29\n" +
			"type: concept\n" +
			"---\n\nSee [[kv-cache]] and [[gpt-4]].\n") // no title
		op := Op{
			Kind:       OpCreatePage,
			Path:       "wiki/concepts/new-concept.md",
			Content:    content,
			Rationale:  "test",
			Provenance: []string{"raw/papers/leviathan-2023.md"},
		}
		wantValidationError(t, ValidateOp(op, v, v.Schema()))
	})

	t.Run("create_page fewer than 2 outbound wikilinks", func(t *testing.T) {
		v := newTestVault(t)
		content := []byte("---\n" +
			"title: New Concept\n" +
			"created: 2026-08-29\n" +
			"updated: 2026-08-29\n" +
			"type: concept\n" +
			"---\n\nSee [[kv-cache]] only.\n")
		op := Op{
			Kind:       OpCreatePage,
			Path:       "wiki/concepts/new-concept.md",
			Content:    content,
			Rationale:  "test",
			Provenance: []string{"raw/papers/leviathan-2023.md"},
		}
		wantValidationError(t, ValidateOp(op, v, v.Schema()))
	})

	t.Run("create_page type does not match directory", func(t *testing.T) {
		v := newTestVault(t)
		op := Op{
			Kind:       OpCreatePage,
			Path:       "wiki/entities/new-concept.md", // type: concept belongs under wiki/concepts
			Content:    newConceptPageContent("New Concept"),
			Rationale:  "test",
			Provenance: []string{"raw/papers/leviathan-2023.md"},
		}
		wantValidationError(t, ValidateOp(op, v, v.Schema()))
	})

	t.Run("create_page tag outside taxonomy", func(t *testing.T) {
		v := newTestVault(t)
		content := []byte("---\n" +
			"title: New Concept\n" +
			"created: 2026-08-29\n" +
			"updated: 2026-08-29\n" +
			"type: concept\n" +
			"tags: [not-a-real-tag]\n" +
			"---\n\nSee [[kv-cache]] and [[gpt-4]].\n")
		op := Op{
			Kind:       OpCreatePage,
			Path:       "wiki/concepts/new-concept.md",
			Content:    content,
			Rationale:  "test",
			Provenance: []string{"raw/papers/leviathan-2023.md"},
		}
		wantValidationError(t, ValidateOp(op, v, v.Schema()))
	})

	t.Run("create_page missing rationale", func(t *testing.T) {
		v := newTestVault(t)
		op := Op{
			Kind:       OpCreatePage,
			Path:       "wiki/concepts/new-concept.md",
			Content:    newConceptPageContent("New Concept"),
			Provenance: []string{"raw/papers/leviathan-2023.md"},
		}
		wantValidationError(t, ValidateOp(op, v, v.Schema()))
	})

	t.Run("create_page empty provenance", func(t *testing.T) {
		v := newTestVault(t)
		op := Op{
			Kind:      OpCreatePage,
			Path:      "wiki/concepts/new-concept.md",
			Content:   newConceptPageContent("New Concept"),
			Rationale: "test",
		}
		wantValidationError(t, ValidateOp(op, v, v.Schema()))
	})

	// --- patch_page ----------------------------------------------------

	t.Run("patch_page valid", func(t *testing.T) {
		v := newTestVault(t)
		page, _ := v.Page("wiki/concepts/kv-cache.md")
		op := Op{
			Kind:    OpPatchPage,
			Path:    page.Path,
			Section: "## Related",
			Before:  page.SHA256(),
			Content: page.Serialize(),
			Hunks:   []Hunk{{ID: "h1", Path: page.Path}},
		}
		if err := ValidateOp(op, v, v.Schema()); err != nil {
			t.Fatalf("ValidateOp: unexpected error: %v", err)
		}
	})

	t.Run("patch_page path does not exist", func(t *testing.T) {
		v := newTestVault(t)
		op := Op{Kind: OpPatchPage, Path: "wiki/concepts/nonexistent.md", Section: "## Related", Before: "", Hunks: []Hunk{{ID: "h1", Path: "wiki/concepts/nonexistent.md"}}}
		wantValidationError(t, ValidateOp(op, v, v.Schema()))
	})

	t.Run("patch_page section does not exist", func(t *testing.T) {
		v := newTestVault(t)
		page, _ := v.Page("wiki/concepts/kv-cache.md")
		op := Op{
			Kind:    OpPatchPage,
			Path:    page.Path,
			Section: "## Not A Real Section",
			Before:  page.SHA256(),
			Content: page.Serialize(),
			Hunks:   []Hunk{{ID: "h1", Path: page.Path}},
		}
		wantValidationError(t, ValidateOp(op, v, v.Schema()))
	})

	t.Run("patch_page append_section on a new trailing section", func(t *testing.T) {
		v := newTestVault(t)
		page, _ := v.Page("wiki/concepts/kv-cache.md")
		content := append([]byte{}, page.Serialize()...)
		content = append(content, []byte("\n## New Section\n\nSome new text.\n")...)
		op := Op{
			Kind:    OpPatchPage,
			Path:    page.Path,
			Section: "## New Section",
			Before:  page.SHA256(),
			Content: content,
			Hunks:   []Hunk{{ID: "h1", Path: page.Path}},
		}
		if err := ValidateOp(op, v, v.Schema()); err != nil {
			t.Fatalf("ValidateOp: unexpected error for a legal new trailing section: %v", err)
		}
	})

	t.Run("patch_page before does not match", func(t *testing.T) {
		v := newTestVault(t)
		page, _ := v.Page("wiki/concepts/kv-cache.md")
		op := Op{
			Kind:    OpPatchPage,
			Path:    page.Path,
			Section: "## Related",
			Before:  "0000000000000000000000000000000000000000000000000000000000000000",
			Content: page.Serialize(),
			Hunks:   []Hunk{{ID: "h1", Path: page.Path}},
		}
		wantValidationError(t, ValidateOp(op, v, v.Schema()))
	})

	t.Run("patch_page hunk missing path", func(t *testing.T) {
		v := newTestVault(t)
		page, _ := v.Page("wiki/concepts/kv-cache.md")
		op := Op{
			Kind:    OpPatchPage,
			Path:    page.Path,
			Section: "## Related",
			Before:  page.SHA256(),
			Content: page.Serialize(),
			Hunks:   []Hunk{{ID: "h1"}},
		}
		wantValidationError(t, ValidateOp(op, v, v.Schema()))
	})

	t.Run("patch_page hunk missing id", func(t *testing.T) {
		v := newTestVault(t)
		page, _ := v.Page("wiki/concepts/kv-cache.md")
		op := Op{
			Kind:    OpPatchPage,
			Path:    page.Path,
			Section: "## Related",
			Before:  page.SHA256(),
			Content: page.Serialize(),
			Hunks:   []Hunk{{Path: page.Path}},
		}
		wantValidationError(t, ValidateOp(op, v, v.Schema()))
	})

	// --- rename_page -----------------------------------------------------

	t.Run("rename_page valid with built cascade", func(t *testing.T) {
		v := newTestVault(t)
		from, to := "wiki/concepts/kv-cache.md", "wiki/concepts/kv-cache-renamed.md"
		cascade, err := buildCascade(v, []string{from}, to)
		if err != nil {
			t.Fatalf("buildCascade: %v", err)
		}
		op := Op{Kind: OpRenamePage, From: from, To: to, Cascade: cascade}
		if err := ValidateOp(op, v, v.Schema()); err != nil {
			t.Fatalf("ValidateOp: unexpected error: %v", err)
		}
	})

	t.Run("rename_page source does not exist", func(t *testing.T) {
		v := newTestVault(t)
		op := Op{Kind: OpRenamePage, From: "wiki/concepts/nonexistent.md", To: "wiki/concepts/y.md"}
		wantValidationError(t, ValidateOp(op, v, v.Schema()))
	})

	t.Run("rename_page destination already exists", func(t *testing.T) {
		v := newTestVault(t)
		op := Op{Kind: OpRenamePage, From: "wiki/concepts/kv-cache.md", To: "wiki/concepts/flash-attention.md"}
		wantValidationError(t, ValidateOp(op, v, v.Schema()))
	})

	// --- merge_pages -------------------------------------------------

	t.Run("merge_pages valid with built cascade", func(t *testing.T) {
		v := newTestVault(t)
		sources := []string{"wiki/concepts/flash-attention.md", "wiki/concepts/speculative-decoding.md"}
		to := "wiki/concepts/attention-merged.md"
		cascade, err := buildCascade(v, sources, to)
		if err != nil {
			t.Fatalf("buildCascade: %v", err)
		}
		op := Op{Kind: OpMergePages, Sources: sources, To: to, Cascade: cascade}
		if err := ValidateOp(op, v, v.Schema()); err != nil {
			t.Fatalf("ValidateOp: unexpected error: %v", err)
		}
	})

	t.Run("merge_pages too few sources", func(t *testing.T) {
		v := newTestVault(t)
		op := Op{Kind: OpMergePages, Sources: []string{"wiki/concepts/kv-cache.md"}, To: "wiki/concepts/merged.md"}
		wantValidationError(t, ValidateOp(op, v, v.Schema()))
	})

	t.Run("merge_pages source does not exist", func(t *testing.T) {
		v := newTestVault(t)
		op := Op{Kind: OpMergePages, Sources: []string{"wiki/concepts/kv-cache.md", "wiki/concepts/nonexistent.md"}, To: "wiki/concepts/merged.md"}
		wantValidationError(t, ValidateOp(op, v, v.Schema()))
	})

	// --- split_page ----------------------------------------------------

	t.Run("split_page valid", func(t *testing.T) {
		v := newTestVault(t)
		op := Op{
			Kind:    OpSplitPage,
			Path:    "wiki/concepts/kv-cache.md",
			Sources: []string{"wiki/concepts/kv-cache-part-a.md", "wiki/concepts/kv-cache-part-b.md"},
		}
		if err := ValidateOp(op, v, v.Schema()); err != nil {
			t.Fatalf("ValidateOp: unexpected error: %v", err)
		}
	})

	t.Run("split_page path does not exist", func(t *testing.T) {
		v := newTestVault(t)
		op := Op{
			Kind:    OpSplitPage,
			Path:    "wiki/concepts/nonexistent.md",
			Sources: []string{"wiki/concepts/a.md", "wiki/concepts/b.md"},
		}
		wantValidationError(t, ValidateOp(op, v, v.Schema()))
	})

	t.Run("split_page too few sources", func(t *testing.T) {
		v := newTestVault(t)
		op := Op{
			Kind:    OpSplitPage,
			Path:    "wiki/concepts/kv-cache.md",
			Sources: []string{"wiki/concepts/kv-cache-part-a.md"},
		}
		wantValidationError(t, ValidateOp(op, v, v.Schema()))
	})

	// --- add_link --------------------------------------------------------

	t.Run("add_link valid", func(t *testing.T) {
		v := newTestVault(t)
		op := Op{Kind: OpAddLink, From: "wiki/concepts/kv-cache.md", To: "wiki/entities/gpt-4.md"}
		if err := ValidateOp(op, v, v.Schema()); err != nil {
			t.Fatalf("ValidateOp: unexpected error: %v", err)
		}
	})

	t.Run("add_link endpoint does not exist", func(t *testing.T) {
		v := newTestVault(t)
		op := Op{Kind: OpAddLink, From: "wiki/concepts/kv-cache.md", To: "wiki/entities/nonexistent.md"}
		wantValidationError(t, ValidateOp(op, v, v.Schema()))
	})

	// --- ingest_source -----------------------------------------------

	t.Run("ingest_source valid", func(t *testing.T) {
		v := newTestVault(t)
		op := Op{
			Kind:      OpIngestSource,
			Path:      "raw/papers/new-source.md",
			Extractor: "go/html",
			Content:   []byte("Some unique raw content for this test only.\n"),
		}
		if err := ValidateOp(op, v, v.Schema()); err != nil {
			t.Fatalf("ValidateOp: unexpected error: %v", err)
		}
	})

	t.Run("ingest_source not under raw/", func(t *testing.T) {
		v := newTestVault(t)
		op := Op{Kind: OpIngestSource, Path: "wiki/concepts/not-raw.md", Extractor: "go/html", Content: []byte("x\n")}
		wantValidationError(t, ValidateOp(op, v, v.Schema()))
	})

	t.Run("ingest_source already exists", func(t *testing.T) {
		v := newTestVault(t)
		op := Op{Kind: OpIngestSource, Path: "raw/papers/leviathan-2023.md", Extractor: "go/html", Content: []byte("x\n")}
		wantValidationError(t, ValidateOp(op, v, v.Schema()))
	})

	t.Run("ingest_source duplicate content by hash", func(t *testing.T) {
		v := newTestVault(t)
		existing, ok := v.RawSource("raw/papers/leviathan-2023.md")
		if !ok {
			t.Fatal("fixture missing raw/papers/leviathan-2023.md")
		}
		op := Op{
			Kind:      OpIngestSource,
			Path:      "raw/papers/duplicate.md",
			Extractor: "go/html",
			Content:   []byte(existing.Body),
		}
		wantValidationError(t, ValidateOp(op, v, v.Schema()))
	})

	t.Run("ingest_source missing extractor", func(t *testing.T) {
		v := newTestVault(t)
		op := Op{Kind: OpIngestSource, Path: "raw/papers/new-source.md", Content: []byte("unique text\n")}
		wantValidationError(t, ValidateOp(op, v, v.Schema()))
	})

	// --- retract -----------------------------------------------------

	t.Run("retract valid", func(t *testing.T) {
		v := newTestVault(t)
		op := Op{Kind: OpRetract, Path: "wiki/concepts/flash-attention.md", Rationale: "superseded"}
		if err := ValidateOp(op, v, v.Schema()); err != nil {
			t.Fatalf("ValidateOp: unexpected error: %v", err)
		}
	})

	t.Run("retract path does not exist", func(t *testing.T) {
		v := newTestVault(t)
		op := Op{Kind: OpRetract, Path: "wiki/concepts/nonexistent.md", Rationale: "gone"}
		wantValidationError(t, ValidateOp(op, v, v.Schema()))
	})

	t.Run("retract missing rationale", func(t *testing.T) {
		v := newTestVault(t)
		op := Op{Kind: OpRetract, Path: "wiki/concepts/flash-attention.md"}
		wantValidationError(t, ValidateOp(op, v, v.Schema()))
	})
}

// TestRenameRequiresFullCascade pins C-37/D-BD's completeness rule: a
// rename of a page WITH real inbound backlinks, proposed with an
// incomplete (here, empty) Cascade, must be rejected — cascade
// completeness is the entire reason rename_page exists.
func TestRenameRequiresFullCascade(t *testing.T) {
	v := newTestVault(t)
	// wiki/concepts/kv-cache.md has real inbound backlinks from
	// flash-attention.md, speculative-decoding.md, gpt-4.md and index.md.
	op := Op{Kind: OpRenamePage, From: "wiki/concepts/kv-cache.md", To: "wiki/concepts/kv-cache2.md"}
	wantValidationError(t, ValidateOp(op, v, v.Schema()))
}

// TestRenameWithNoBacklinksValidates pins C-37/D-BD: a rename of a page
// with zero inbound backlinks (link-orphan is only a warn) must validate
// with an empty Cascade — the schema cannot express "no minItems" without
// this actually being legal, and no test scoped to a fully populated
// changeset could catch its absence.
func TestRenameWithNoBacklinksValidates(t *testing.T) {
	fsys := syntheticNoBacklinksVault(t)
	v, err := vault.OpenFS(fsys)
	if err != nil {
		t.Fatalf("vault.OpenFS: %v", err)
	}

	op := Op{Kind: OpRenamePage, From: "wiki/concepts/lonely.md", To: "wiki/concepts/lonely-renamed.md"}
	if err := ValidateOp(op, v, v.Schema()); err != nil {
		t.Fatalf("ValidateOp: unexpected error for a rename of a page with no backlinks: %v", err)
	}
}

// TestCascadeCoversIndexMd pins C-39/D-BE: index.md is not a Page (§2.8),
// so Graph().Backlinks structurally cannot see its links — yet
// wiki/entities/gpt-4.md is linked from minimal/index.md, and a cascade
// that omits it would leave index.md pointing at a path that no longer
// exists.
func TestCascadeCoversIndexMd(t *testing.T) {
	v := newTestVault(t)

	t.Run("built cascade includes index.md", func(t *testing.T) {
		cascade, err := buildCascade(v, []string{"wiki/entities/gpt-4.md"}, "wiki/entities/gpt-4x.md")
		if err != nil {
			t.Fatalf("buildCascade: %v", err)
		}
		found := false
		for _, sub := range cascade {
			if sub.Path == "index.md" {
				found = true
			}
		}
		if !found {
			t.Fatalf("buildCascade cascade = %+v, want an entry for index.md", cascade)
		}
	})

	t.Run("a cascade missing index.md is rejected", func(t *testing.T) {
		// Every page-level backlink is present, but index.md is not.
		cascade, err := buildCascade(v, []string{"wiki/entities/gpt-4.md"}, "wiki/entities/gpt-4x.md")
		if err != nil {
			t.Fatalf("buildCascade: %v", err)
		}
		var withoutIndex []Op
		for _, sub := range cascade {
			if sub.Path != "index.md" {
				withoutIndex = append(withoutIndex, sub)
			}
		}
		if len(withoutIndex) == len(cascade) {
			t.Fatal("test setup: buildCascade did not include index.md in the first place")
		}
		op := Op{Kind: OpRenamePage, From: "wiki/entities/gpt-4.md", To: "wiki/entities/gpt-4x.md", Cascade: withoutIndex}
		wantValidationError(t, ValidateOp(op, v, v.Schema()))
	})
}
