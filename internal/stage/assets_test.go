package stage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// TestStageIngestSourceWithAssets pins D3:
// Append of an OpIngestSource carrying 2 Assets stores both blobs in CAS,
// clears Content in changeset.json, registers both in ProjectedVault().AttachmentSHA256(...),
// includes both in Changeset.Touches(), and Commit writes both files under raw/assets/
// and makes them readable via e.Vault().AttachmentSHA256(...).
func TestStageIngestSourceWithAssets(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	e, err := OpenEngine(dir)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()

	c, err := e.OpenChangeset("ingest markdown with images", Author{Kind: "agent"})
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	img1Bytes := []byte("\x89PNG\r\n\x1a\nimage1-data")
	img2Bytes := []byte("\x89PNG\r\n\x1a\nimage2-data")
	h1 := sha256.Sum256(img1Bytes)
	h2 := sha256.Sum256(img2Bytes)
	sha1 := hex.EncodeToString(h1[:])
	sha2 := hex.EncodeToString(h2[:])

	rawBody := "# My Article\n\n![fig1](../../raw/assets/my-article-fig1.png)\n![fig2](../../raw/assets/my-article-fig2.png)\n"
	rawContent := "---\ntitle: My Article\ncreated: 2026-10-20\nupdated: 2026-10-20\nsource_url: https://example.com/article.md\n---\n\n" + rawBody

	op := Op{
		Kind:      OpIngestSource,
		Path:      "raw/articles/my-article.md",
		Content:   []byte(rawContent),
		SHA256:    vault.BodySHA256(rawBody),
		Extractor: "local",
		Assets: []AssetRef{
			{Path: "raw/assets/my-article-fig1.png", SHA256: sha1, Content: img1Bytes},
			{Path: "raw/assets/my-article-fig2.png", SHA256: sha2, Content: img2Bytes},
		},
	}

	opID, err := e.Append(op)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if opID == "" {
		t.Fatal("Append returned empty opID")
	}

	// 1. Both blobs are in CAS:
	if !e.store.Has(sha1) {
		t.Errorf("CAS missing blob for asset 1 (%s)", sha1)
	}
	if !e.store.Has(sha2) {
		t.Errorf("CAS missing blob for asset 2 (%s)", sha2)
	}

	// 2. Content is cleared in changeset.json:
	curr, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	stagedOp, ok := curr.Op(opID)
	if !ok {
		t.Fatalf("Current op %q not found", opID)
	}
	if len(stagedOp.Assets) != 2 {
		t.Fatalf("stagedOp has %d assets, want 2", len(stagedOp.Assets))
	}
	for i, a := range stagedOp.Assets {
		if a.Content != nil {
			t.Errorf("stagedOp asset[%d].Content was not cleared", i)
		}
	}
	// Also check raw on disk changeset.json
	csPath := filepath.Join(dir, ".llmwiki", "changesets", "open", c.ID, "changeset.json")
	csRaw, err := os.ReadFile(csPath)
	if err != nil {
		t.Fatalf("read changeset.json: %v", err)
	}
	if strings.Contains(string(csRaw), "image1-data") || strings.Contains(string(csRaw), "image2-data") {
		t.Errorf("changeset.json contains inlined asset content bytes")
	}

	// 3. Registers both in ProjectedVault().AttachmentSHA256(...):
	pv, err := e.ProjectedVault()
	if err != nil {
		t.Fatalf("ProjectedVault: %v", err)
	}
	gotSHA1, err := pv.AttachmentSHA256("raw/assets/my-article-fig1.png")
	if err != nil || gotSHA1 != sha1 {
		t.Errorf("ProjectedVault AttachmentSHA256(fig1) = %q, %v; want %q, nil", gotSHA1, err, sha1)
	}
	gotSHA2, err := pv.AttachmentSHA256("raw/assets/my-article-fig2.png")
	if err != nil || gotSHA2 != sha2 {
		t.Errorf("ProjectedVault AttachmentSHA256(fig2) = %q, %v; want %q, nil", gotSHA2, err, sha2)
	}

	// 4. Includes both in Changeset.Touches():
	touches := curr.Touches()
	if !slices.Contains(touches, "raw/assets/my-article-fig1.png") {
		t.Errorf("curr.Touches() = %v, does not contain fig1", touches)
	}
	if !slices.Contains(touches, "raw/assets/my-article-fig2.png") {
		t.Errorf("curr.Touches() = %v, does not contain fig2", touches)
	}

	// 5. Commit writes both files under raw/assets/ and makes them readable via e.Vault().AttachmentSHA256(...)
	commitID, err := e.Commit("commit ingest with assets")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if commitID == "" {
		t.Fatal("Commit returned empty commitID")
	}

	f1OnDisk := filepath.Join(dir, "raw", "assets", "my-article-fig1.png")
	b1, err := os.ReadFile(f1OnDisk)
	if err != nil {
		t.Fatalf("read %s on disk: %v", f1OnDisk, err)
	}
	if string(b1) != string(img1Bytes) {
		t.Errorf("fig1 content mismatch on disk: got %q, want %q", b1, img1Bytes)
	}

	f2OnDisk := filepath.Join(dir, "raw", "assets", "my-article-fig2.png")
	b2, err := os.ReadFile(f2OnDisk)
	if err != nil {
		t.Fatalf("read %s on disk: %v", f2OnDisk, err)
	}
	if string(b2) != string(img2Bytes) {
		t.Errorf("fig2 content mismatch on disk: got %q, want %q", b2, img2Bytes)
	}

	vaultSHA1, err := e.Vault().AttachmentSHA256("raw/assets/my-article-fig1.png")
	if err != nil || vaultSHA1 != sha1 {
		t.Errorf("e.Vault().AttachmentSHA256(fig1) = %q, %v; want %q, nil", vaultSHA1, err, sha1)
	}
	vaultSHA2, err := e.Vault().AttachmentSHA256("raw/assets/my-article-fig2.png")
	if err != nil || vaultSHA2 != sha2 {
		t.Errorf("e.Vault().AttachmentSHA256(fig2) = %q, %v; want %q, nil", vaultSHA2, err, sha2)
	}
}

// TestValidateIngestAssets pins D3 validation rules:
// rejects Assets on non-OpIngestSource ops, paths outside raw/assets/,
// .md extensions, duplicate asset paths, existing vault paths, and
// mismatched/missing content hashes.
func TestValidateIngestAssets(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	v, err := vault.Open(dir)
	if err != nil {
		t.Fatalf("vault.Open: %v", err)
	}
	s := v.Schema()

	data := []byte("hello-image")
	h := sha256.Sum256(data)
	validSHA := hex.EncodeToString(h[:])

	// 1. Assets on non-OpIngestSource (e.g. OpCreatePage):
	t.Run("rejects assets on non-ingest_source", func(t *testing.T) {
		op := Op{
			Kind:       OpCreatePage,
			Path:       "wiki/concepts/some-concept.md",
			Rationale:  "test",
			Provenance: []string{"raw/articles/some-article.md"},
			Content:    []byte("---\ntitle: Some Concept\ntype: concept\ncreated: 2026-10-20\nupdated: 2026-10-20\ntags: [test]\n---\n\n# Some Concept\n\n[[concept-a]] and [[concept-b]]\n"),
			Assets: []AssetRef{
				{Path: "raw/assets/img.png", SHA256: validSHA, Content: data},
			},
		}
		err := ValidateOp(op, v, s)
		if err == nil || !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "assets are only allowed on ingest_source") {
			t.Errorf("ValidateOp = %v, want ErrValidation naming assets only allowed on ingest_source", err)
		}
	})

	baseIngest := func() Op {
		body := "# Title\n\nSome body text.\n"
		return Op{
			Kind:      OpIngestSource,
			Path:      "raw/articles/new-art.md",
			Content:   []byte("---\ntitle: Title\ncreated: 2026-10-20\nupdated: 2026-10-20\nsource_url: https://example.com/art.md\n---\n\n" + body),
			SHA256:    vault.BodySHA256(body),
			Extractor: "local",
		}
	}

	// 2. Paths outside raw/assets/:
	t.Run("rejects paths outside raw/assets", func(t *testing.T) {
		for _, badPath := range []string{
			"assets/img.png",
			"raw/img.png",
			"raw/articles/img.png",
			"raw/assets/sub/img.png",
			"raw/assets/UPPER.png",
			"raw/assets/-leading.png",
			"raw/assets/trailing-.png",
		} {
			op := baseIngest()
			op.Assets = []AssetRef{{Path: badPath, SHA256: validSHA, Content: data}}
			err := ValidateOp(op, v, s)
			if err == nil || !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "must be raw/assets/<name>.<ext>") {
				t.Errorf("ValidateOp(%s) = %v, want ErrValidation naming raw/assets/<name>.<ext>", badPath, err)
			}
		}
	})

	// 3. .md and .markdown extensions:
	t.Run("rejects .md and .markdown extensions", func(t *testing.T) {
		for _, bad := range []string{"raw/assets/some-asset.md", "raw/assets/some-asset.markdown"} {
			op := baseIngest()
			op.Assets = []AssetRef{{Path: bad, SHA256: validSHA, Content: data}}
			err := ValidateOp(op, v, s)
			if err == nil || !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "non-.md extension") {
				t.Errorf("ValidateOp(%s) = %v, want ErrValidation naming non-.md extension", bad, err)
			}
		}
	})

	// 4. Duplicate asset paths:
	t.Run("rejects duplicate asset paths", func(t *testing.T) {
		op := baseIngest()
		op.Assets = []AssetRef{
			{Path: "raw/assets/dup.png", SHA256: validSHA, Content: data},
			{Path: "raw/assets/dup.png", SHA256: validSHA, Content: data},
		}
		err := ValidateOp(op, v, s)
		if err == nil || !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "duplicate asset path") {
			t.Errorf("ValidateOp = %v, want ErrValidation naming duplicate asset path", err)
		}
	})

	// 5. Existing vault paths:
	t.Run("rejects existing vault paths", func(t *testing.T) {
		// create a file under raw/assets/ in fixture
		existingPath := filepath.Join(dir, "raw", "assets", "already-there.png")
		if err := os.MkdirAll(filepath.Dir(existingPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(existingPath, []byte("existing"), 0o644); err != nil {
			t.Fatal(err)
		}
		vReload, err := vault.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		op := baseIngest()
		op.Assets = []AssetRef{{Path: "raw/assets/already-there.png", SHA256: validSHA, Content: data}}
		err = ValidateOp(op, vReload, s)
		if err == nil || !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "already exists") {
			t.Errorf("ValidateOp = %v, want ErrValidation naming already exists", err)
		}
	})

	// 6. Mismatched or missing content hashes:
	t.Run("rejects missing sha256 or content", func(t *testing.T) {
		op := baseIngest()
		op.Assets = []AssetRef{{Path: "raw/assets/img.png", SHA256: "", Content: data}}
		err := ValidateOp(op, v, s)
		if err == nil || !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "requires both sha256 and content") {
			t.Errorf("ValidateOp = %v, want ErrValidation naming requires both sha256 and content", err)
		}

		op.Assets = []AssetRef{{Path: "raw/assets/img.png", SHA256: validSHA, Content: nil}}
		err = ValidateOp(op, v, s)
		if err == nil || !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "requires both sha256 and content") {
			t.Errorf("ValidateOp = %v, want ErrValidation naming requires both sha256 and content", err)
		}
	})

	t.Run("rejects hash mismatch", func(t *testing.T) {
		op := baseIngest()
		wrongSHA := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		op.Assets = []AssetRef{{Path: "raw/assets/img.png", SHA256: wrongSHA, Content: data}}
		err := ValidateOp(op, v, s)
		if err == nil || !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "not the declared") {
			t.Errorf("ValidateOp = %v, want ErrValidation naming not the declared", err)
		}
	})
}

func TestAppendRejectsCollidingAssetInOpenChangeset(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	e, err := OpenEngine(dir)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()

	if _, err := e.OpenChangeset("collision test", Author{Kind: "agent"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	img := []byte("\x89PNG\r\n\x1a\nshared")
	h := sha256.Sum256(img)
	sha := hex.EncodeToString(h[:])

	op1 := Op{
		Kind:      OpIngestSource,
		Path:      "raw/articles/first.md",
		Content:   stagedRawDoc("https://example.test/1", "# First\n\nBody one.\n"),
		Extractor: "local",
		Assets:    []AssetRef{{Path: "raw/assets/shared-fig.png", SHA256: sha, Content: img}},
	}
	if _, err := e.Append(op1); err != nil {
		t.Fatalf("Append op1: %v", err)
	}

	op2 := Op{
		Kind:      OpIngestSource,
		Path:      "raw/articles/second.md",
		Content:   stagedRawDoc("https://example.test/2", "# Second\n\nBody two.\n"),
		Extractor: "local",
		Assets:    []AssetRef{{Path: "raw/assets/shared-fig.png", SHA256: sha, Content: img}},
	}
	_, err = e.Append(op2)
	if err == nil || !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "staged in this changeset") {
		t.Fatalf("Append op2 = %v, want ErrValidation naming staged in this changeset", err)
	}
}

func TestRevertReportsSkippedIngestAssets(t *testing.T) {
	e, _ := newTestEngine(t)

	if _, err := e.OpenChangeset("ingest with assets", testAuthor); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	img := []byte("\x89PNG\r\n\x1a\nrevert-asset")
	h := sha256.Sum256(img)
	sha := hex.EncodeToString(h[:])

	const rawPath = "raw/articles/with-asset.md"
	const assetPath = "raw/assets/with-asset-fig.png"
	if _, err := e.Append(Op{
		Kind:      OpIngestSource,
		Path:      rawPath,
		Extractor: "local",
		Content:   stagedRawDoc("https://example.test/with-asset", "# With Asset\n\n![fig](../../raw/assets/with-asset-fig.png)\n"),
		Assets:    []AssetRef{{Path: assetPath, SHA256: sha, Content: img}},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	commitID, err := e.Commit("commit source with asset")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	revertCS, err := e.Revert(commitID)
	if err != nil {
		t.Fatalf("Revert(%s): %v", commitID, err)
	}
	if !strings.Contains(revertCS.Intent, rawPath) || !strings.Contains(revertCS.Intent, assetPath) {
		t.Errorf("revertCS.Intent = %q, want both %s and %s", revertCS.Intent, rawPath, assetPath)
	}
	skipped := lastRevertedSkipped(t, e)
	if !containsString(skipped, rawPath) || !containsString(skipped, assetPath) {
		t.Fatalf("skipped = %v, want both %s and %s", skipped, rawPath, assetPath)
	}
}
