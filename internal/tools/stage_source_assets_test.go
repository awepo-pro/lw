package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// TestToolIngestSourceStagesMarkdownAssets pins D4:
// stage.ingest_source on a markdown file with local images stages
// OpIngestSource with Assets, rewrites links to
// ../../raw/assets/<final-slug>-<img-name>.<ext>, re-prefixes when
// sourcePath gets -2 or a name hint, and suffixes -2 on the asset when an
// asset path is already in raw/assets/ or claimed by an earlier live op in the
// open changeset.
func TestToolIngestSourceStagesMarkdownAssets(t *testing.T) {
	vaultDir := testutil.CopyFixture(t, "minimal")
	v, err := vault.Open(vaultDir)
	if err != nil {
		t.Fatalf("vault.Open: %v", err)
	}
	e, err := stage.OpenEngine(vaultDir)
	if err != nil {
		t.Fatalf("stage.OpenEngine: %v", err)
	}
	defer e.Close()

	_, err = e.OpenChangeset("test ingest markdown assets", stage.Author{Kind: "agent", Model: "test"})
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	reg := NewRegistry(Deps{
		Vault:   v,
		Index:   index.Build(v),
		Engine:  e,
		Extract: extract.NewFile(),
		Author:  stage.Author{Kind: "agent", Model: "test"},
	})

	callIngest := func(t *testing.T, args stageIngestSourceArgs) Result {
		t.Helper()
		b, err := json.Marshal(args)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		res, err := reg.Call(context.Background(), "stage.ingest_source", b)
		if err != nil {
			t.Fatalf("reg.Call: %v", err)
		}
		if res.IsError {
			t.Fatalf("stage.ingest_source returned error: %s", res.Content)
		}
		return res
	}

	readStagedRaw := func(t *testing.T, op *stage.Op) string {
		t.Helper()
		b, staged, err := e.StagedFile(op.Path)
		if err != nil || !staged {
			t.Fatalf("StagedFile(%s): staged=%v err=%v", op.Path, staged, err)
		}
		return string(b)
	}

	tmpDir := t.TempDir()
	imgData := []byte("\x89PNG\r\n\x1a\nfig1-content")
	imgDir := filepath.Join(tmpDir, "images")
	if err := os.MkdirAll(imgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	img1Path := filepath.Join(imgDir, "fig1.png")
	if err := os.WriteFile(img1Path, imgData, 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. Basic markdown with local image
	t.Run("basic local image staging and link rewriting", func(t *testing.T) {
		doc1Path := filepath.Join(tmpDir, "article1.md")
		doc1Content := "# Article One\n\nIntroduction.\n\n![Figure 1](images/fig1.png)\n"
		if err := os.WriteFile(doc1Path, []byte(doc1Content), 0o644); err != nil {
			t.Fatal(err)
		}

		res := callIngest(t, stageIngestSourceArgs{URI: doc1Path})
		if !strings.Contains(res.Content, "raw/articles/article-one.md") {
			t.Errorf("result content = %q, want path raw/articles/article-one.md", res.Content)
		}

		curr, err := e.Current()
		if err != nil {
			t.Fatalf("Current: %v", err)
		}
		var found *stage.Op
		for _, op := range curr.Live() {
			if op.Path == "raw/articles/article-one.md" {
				copyOp := op
				found = &copyOp
				break
			}
		}
		if found == nil {
			t.Fatal("op raw/articles/article-one.md not found in live ops")
		}
		if len(found.Assets) != 1 {
			t.Fatalf("found.Assets length = %d, want 1", len(found.Assets))
		}
		wantAssetPath := "raw/assets/article-one-fig1.png"
		if found.Assets[0].Path != wantAssetPath {
			t.Errorf("asset path = %q, want %q", found.Assets[0].Path, wantAssetPath)
		}
		// Check that the staged raw content has rewritten link
		stagedText := readStagedRaw(t, found)
		if !strings.Contains(stagedText, "../../raw/assets/article-one-fig1.png") {
			t.Errorf("staged content does not contain rewritten link ../../raw/assets/article-one-fig1.png:\n%s", stagedText)
		}
		if strings.Contains(stagedText, "images/fig1.png") {
			t.Errorf("staged content still contains un-rewritten images/fig1.png:\n%s", stagedText)
		}
	})

	// 2. Re-prefixes when sourcePath gets -2
	t.Run("re-prefixes assets when sourcePath gets -2", func(t *testing.T) {
		doc2Path := filepath.Join(tmpDir, "article2.md")
		// Same title "Article One" so candidate raw/articles/article-one.md is taken, but different body text
		doc2Content := "# Article One\n\nSecond variation of article one.\n\n![Figure 1](images/fig1.png)\n"
		if err := os.WriteFile(doc2Path, []byte(doc2Content), 0o644); err != nil {
			t.Fatal(err)
		}

		res := callIngest(t, stageIngestSourceArgs{URI: doc2Path})
		if !strings.Contains(res.Content, "raw/articles/article-one-2.md") {
			t.Errorf("result content = %q, want path raw/articles/article-one-2.md", res.Content)
		}

		curr, err := e.Current()
		if err != nil {
			t.Fatalf("Current: %v", err)
		}
		var found *stage.Op
		for _, op := range curr.Live() {
			if op.Path == "raw/articles/article-one-2.md" {
				copyOp := op
				found = &copyOp
				break
			}
		}
		if found == nil {
			t.Fatal("op raw/articles/article-one-2.md not found in live ops")
		}
		if len(found.Assets) != 1 {
			t.Fatalf("found.Assets length = %d, want 1", len(found.Assets))
		}
		// Suffix -2 on sourcePath causes re-prefixing to article-one-2-fig1.png
		wantAssetPath := "raw/assets/article-one-2-fig1.png"
		if found.Assets[0].Path != wantAssetPath {
			t.Errorf("asset path = %q, want %q", found.Assets[0].Path, wantAssetPath)
		}
		stagedText := readStagedRaw(t, found)
		if !strings.Contains(stagedText, "../../raw/assets/article-one-2-fig1.png") {
			t.Errorf("staged content does not contain rewritten link ../../raw/assets/article-one-2-fig1.png:\n%s", stagedText)
		}
	})

	// 3. Re-prefixes when a name hint is used
	t.Run("re-prefixes assets when name hint is used", func(t *testing.T) {
		doc3Path := filepath.Join(tmpDir, "cjk.md")
		doc3Content := "# 四元數簡介\n\nCJK content.\n\n![Architecture](images/fig1.png)\n"
		if err := os.WriteFile(doc3Path, []byte(doc3Content), 0o644); err != nil {
			t.Fatal(err)
		}

		res := callIngest(t, stageIngestSourceArgs{URI: doc3Path, Name: "quaternion-intro"})
		if !strings.Contains(res.Content, "raw/articles/quaternion-intro.md") {
			t.Errorf("result content = %q, want path raw/articles/quaternion-intro.md", res.Content)
		}

		curr, err := e.Current()
		if err != nil {
			t.Fatalf("Current: %v", err)
		}
		var found *stage.Op
		for _, op := range curr.Live() {
			if op.Path == "raw/articles/quaternion-intro.md" {
				copyOp := op
				found = &copyOp
				break
			}
		}
		if found == nil {
			t.Fatal("op raw/articles/quaternion-intro.md not found")
		}
		if len(found.Assets) != 1 {
			t.Fatalf("found.Assets length = %d, want 1", len(found.Assets))
		}
		wantAssetPath := "raw/assets/quaternion-intro-fig1.png"
		if found.Assets[0].Path != wantAssetPath {
			t.Errorf("asset path = %q, want %q", found.Assets[0].Path, wantAssetPath)
		}
		stagedText := readStagedRaw(t, found)
		if !strings.Contains(stagedText, "../../raw/assets/quaternion-intro-fig1.png") {
			t.Errorf("staged content does not contain ../../raw/assets/quaternion-intro-fig1.png:\n%s", stagedText)
		}
	})

	// 4. Suffixes -2 on the asset when an asset path is already in raw/assets/ (committed vault)
	t.Run("suffixes -2 on asset when asset path already in raw/assets", func(t *testing.T) {
		// Put an asset directly in vaultDir to simulate existing vault asset:
		vaultAsset := filepath.Join(vaultDir, "raw", "assets", "vault-existing-fig.png")
		if err := os.MkdirAll(filepath.Dir(vaultAsset), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(vaultAsset, []byte("\x89PNG\r\n\x1a\nexisting-vault-asset"), 0o644); err != nil {
			t.Fatal(err)
		}
		vReloaded, err := vault.Open(vaultDir)
		if err != nil {
			t.Fatal(err)
		}
		reg.deps.Vault = vReloaded

		figImg := filepath.Join(imgDir, "fig.png")
		if err := os.WriteFile(figImg, []byte("\x89PNG\r\n\x1a\nnew-fig"), 0o644); err != nil {
			t.Fatal(err)
		}
		docVPath := filepath.Join(tmpDir, "vault-test.md")
		docVContent := "# Vault Existing\n\nContent.\n\n![Fig](images/fig.png)\n"
		if err := os.WriteFile(docVPath, []byte(docVContent), 0o644); err != nil {
			t.Fatal(err)
		}

		callIngest(t, stageIngestSourceArgs{URI: docVPath})

		curr, err := e.Current()
		if err != nil {
			t.Fatalf("Current: %v", err)
		}
		var found *stage.Op
		for _, op := range curr.Live() {
			if op.Path == "raw/articles/vault-existing.md" {
				copyOp := op
				found = &copyOp
				break
			}
		}
		if found == nil {
			t.Fatal("op raw/articles/vault-existing.md not found")
		}
		if len(found.Assets) != 1 {
			t.Fatalf("found.Assets length = %d, want 1", len(found.Assets))
		}
		// Because raw/assets/vault-existing-fig.png exists in vault, it must get suffixed with -2:
		wantAsset := "raw/assets/vault-existing-fig-2.png"
		if found.Assets[0].Path != wantAsset {
			t.Errorf("asset path = %q, want %q", found.Assets[0].Path, wantAsset)
		}
		stagedText := readStagedRaw(t, found)
		if !strings.Contains(stagedText, "../../raw/assets/vault-existing-fig-2.png") {
			t.Errorf("staged content does not contain rewritten link ../../raw/assets/vault-existing-fig-2.png:\n%s", stagedText)
		}
	})

	// 5. Suffixes -2 on the asset when an asset path is claimed by an earlier live op in open changeset
	t.Run("suffixes -2 on asset when claimed by earlier live op in open changeset", func(t *testing.T) {
		// Doc A has title "Alpha", image "beta-chart.png" -> candidate "alpha-beta-chart.png"
		chartImg := filepath.Join(imgDir, "beta-chart.png")
		if err := os.WriteFile(chartImg, []byte("\x89PNG\r\n\x1a\nchart-data"), 0o644); err != nil {
			t.Fatal(err)
		}
		docAPath := filepath.Join(tmpDir, "docA.md")
		docAContent := "# Alpha\n\nDoc A text.\n\n![Chart](images/beta-chart.png)\n"
		if err := os.WriteFile(docAPath, []byte(docAContent), 0o644); err != nil {
			t.Fatal(err)
		}
		callIngest(t, stageIngestSourceArgs{URI: docAPath})

		// Doc B has title "Alpha Beta", image "chart.png" -> docSlug "alpha-beta", imgSlug "chart".
		// Extract candidate is "alpha-beta-chart.png".
		// But "raw/assets/alpha-beta-chart.png" was already claimed by Doc A in this same open changeset!
		// So Doc B's asset must be suffixed to "raw/assets/alpha-beta-chart-2.png".
		simpleChartImg := filepath.Join(imgDir, "chart.png")
		if err := os.WriteFile(simpleChartImg, []byte("\x89PNG\r\n\x1a\nsimple-chart-data"), 0o644); err != nil {
			t.Fatal(err)
		}
		docBPath := filepath.Join(tmpDir, "docB.md")
		docBContent := "# Alpha Beta\n\nDoc B text.\n\n![Chart](images/chart.png)\n"
		if err := os.WriteFile(docBPath, []byte(docBContent), 0o644); err != nil {
			t.Fatal(err)
		}
		callIngest(t, stageIngestSourceArgs{URI: docBPath})

		curr, err := e.Current()
		if err != nil {
			t.Fatalf("Current: %v", err)
		}
		var foundB *stage.Op
		for _, op := range curr.Live() {
			if op.Path == "raw/articles/alpha-beta.md" {
				copyOp := op
				foundB = &copyOp
				break
			}
		}
		if foundB == nil {
			t.Fatal("op raw/articles/alpha-beta.md not found")
		}
		if len(foundB.Assets) != 1 {
			t.Fatalf("foundB.Assets length = %d, want 1", len(foundB.Assets))
		}
		wantAssetB := "raw/assets/alpha-beta-chart-2.png"
		if foundB.Assets[0].Path != wantAssetB {
			t.Errorf("Doc B asset path = %q, want %q", foundB.Assets[0].Path, wantAssetB)
		}
		stagedTextB := readStagedRaw(t, foundB)
		if !strings.Contains(stagedTextB, "../../raw/assets/alpha-beta-chart-2.png") {
			t.Errorf("staged content does not contain ../../raw/assets/alpha-beta-chart-2.png:\n%s", stagedTextB)
		}
	})

	// 6. Regression for F-1: no cascading link replacement or sibling collision when vault already holds -fig.png
	t.Run("no cascading replacement collision when sibling assets collide with vault", func(t *testing.T) {
		for _, existing := range []string{"cascade-doc-fig.png", "cascade-doc-fig-2.png"} {
			p := filepath.Join(vaultDir, "raw", "assets", existing)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("\x89PNG\r\n\x1a\npreexisting-"+existing), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		vReloaded, err := vault.Open(vaultDir)
		if err != nil {
			t.Fatal(err)
		}
		reg.deps.Vault = vReloaded

		subA := filepath.Join(tmpDir, "subA")
		subB := filepath.Join(tmpDir, "subB")
		for _, d := range []string{subA, subB} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(subA, "fig.png"), []byte("\x89PNG\r\n\x1a\nsubA-fig"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(subB, "fig.png"), []byte("\x89PNG\r\n\x1a\nsubB-fig"), 0o644); err != nil {
			t.Fatal(err)
		}

		docCPath := filepath.Join(tmpDir, "cascade.md")
		docCContent := "# Cascade Doc\n\nFirst: ![A](subA/fig.png)\n\nSecond: ![B](subB/fig.png)\n"
		if err := os.WriteFile(docCPath, []byte(docCContent), 0o644); err != nil {
			t.Fatal(err)
		}

		callIngest(t, stageIngestSourceArgs{URI: docCPath})

		curr, err := e.Current()
		if err != nil {
			t.Fatalf("Current: %v", err)
		}
		var foundC *stage.Op
		for _, op := range curr.Live() {
			if op.Path == "raw/articles/cascade-doc.md" {
				copyOp := op
				foundC = &copyOp
				break
			}
		}
		if foundC == nil {
			t.Fatal("op raw/articles/cascade-doc.md not found")
		}
		if len(foundC.Assets) != 2 {
			t.Fatalf("foundC.Assets length = %d, want 2", len(foundC.Assets))
		}
		if foundC.Assets[0].Path != "raw/assets/cascade-doc-fig-3.png" {
			t.Errorf("asset[0].Path = %q, want raw/assets/cascade-doc-fig-3.png", foundC.Assets[0].Path)
		}
		if foundC.Assets[1].Path != "raw/assets/cascade-doc-fig-2-2.png" {
			t.Errorf("asset[1].Path = %q, want raw/assets/cascade-doc-fig-2-2.png", foundC.Assets[1].Path)
		}
		stagedTextC := readStagedRaw(t, foundC)
		if !strings.Contains(stagedTextC, "![A](../../raw/assets/cascade-doc-fig-3.png)") {
			t.Errorf("staged text missing first rewritten link:\n%s", stagedTextC)
		}
		if !strings.Contains(stagedTextC, "![B](../../raw/assets/cascade-doc-fig-2-2.png)") {
			t.Errorf("staged text missing second rewritten link:\n%s", stagedTextC)
		}
	})
}
