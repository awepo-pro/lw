package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

// TestCmdIngestFolderWithMarkdownAndImages pins the CLI behavior for ingesting a folder
// containing markdown files and referenced images (064 S2):
// `lw ingest --prepare <folder>` on a folder containing article.md (referencing ./images/fig1.png)
// + images/fig1.png + images/unused.png prints `skipped .../unused.png: unsupported type`
// (and NOT `fig1.png`), stages raw/articles/<slug>.md with ![...](../../raw/assets/<slug>-fig1.png)
// and op.Assets, commits cleanly (e.Commit), writes raw/assets/<slug>-fig1.png on disk,
// passes `lw lint`, and a second `lw ingest --prepare <folder>` reports `already in the vault`
// (`nothing to ingest`).
func TestCmdIngestFolderWithMarkdownAndImages(t *testing.T) {
	syncHermetic(t)
	root := testutil.CopyFixture(t, "minimal")

	// Set up source folder with article.md, referenced fig1.png, and unreferenced unused.png
	folder := filepath.Join(t.TempDir(), "source-docs")
	imgDir := filepath.Join(folder, "images")
	if err := os.MkdirAll(imgDir, 0o755); err != nil {
		t.Fatal(err)
	}

	fig1Bytes := []byte("\x89PNG\r\n\x1a\nfig1-binary-content")
	unusedBytes := []byte("\x89PNG\r\n\x1a\nunused-binary-content")
	if err := os.WriteFile(filepath.Join(imgDir, "fig1.png"), fig1Bytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imgDir, "unused.png"), unusedBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	articleMD := "# Great Research\n\nIntroductory section with an important figure.\n\n![Main Figure](./images/fig1.png)\n"
	if err := os.WriteFile(filepath.Join(folder, "article.md"), []byte(articleMD), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. Run `lw ingest --prepare <folder>`
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--prepare", folder, "--vault", root})
	})
	if code != 0 {
		t.Fatalf("ingest --prepare exit code = %d; stdout=%q stderr=%q", code, stdout, stderr)
	}

	combinedOutput := stdout + "\n" + stderr
	// Must report skipped unused.png
	if !strings.Contains(combinedOutput, "unused.png") || !strings.Contains(combinedOutput, "unsupported type") {
		t.Errorf("output does not report unused.png unsupported type:\n%s", combinedOutput)
	}
	// Must NOT report skipped fig1.png
	if strings.Contains(combinedOutput, "fig1.png: unsupported type") || strings.Contains(combinedOutput, "skipped") && strings.Contains(combinedOutput, "fig1.png") {
		t.Errorf("output falsely reports fig1.png as skipped:\n%s", combinedOutput)
	}

	// 2. Inspect open changeset
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}

	curr, err := e.Current()
	if err != nil {
		e.Close()
		t.Fatalf("Current: %v", err)
	}

	var found *stage.Op
	for _, op := range curr.Live() {
		if op.Path == "raw/articles/great-research.md" {
			copyOp := op
			found = &copyOp
			break
		}
	}
	if found == nil {
		e.Close()
		t.Fatalf("op raw/articles/great-research.md not found in live ops (%d ops)", len(curr.Live()))
	}

	wantAssetPath := "raw/assets/great-research-fig1.png"
	if len(found.Assets) != 1 || found.Assets[0].Path != wantAssetPath {
		e.Close()
		t.Fatalf("found.Assets = %+v, want 1 asset at %q", found.Assets, wantAssetPath)
	}

	stagedBytes, staged, err := e.StagedFile(found.Path)
	if err != nil || !staged {
		e.Close()
		t.Fatalf("StagedFile(%s): staged=%v err=%v", found.Path, staged, err)
	}
	if !strings.Contains(string(stagedBytes), "![Main Figure](../../raw/assets/great-research-fig1.png)") {
		e.Close()
		t.Errorf("staged raw content lacks rewritten link:\n%s", string(stagedBytes))
	}

	// 3. Commit cleanly
	commitID, err := e.Commit("ingest folder with images")
	e.Close()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if commitID == "" {
		t.Fatal("Commit returned empty commitID")
	}

	// 4. Verify disk
	diskAsset := filepath.Join(root, "raw", "assets", "great-research-fig1.png")
	b, err := os.ReadFile(diskAsset)
	if err != nil {
		t.Fatalf("read %s: %v", diskAsset, err)
	}
	if string(b) != string(fig1Bytes) {
		t.Errorf("asset bytes on disk mismatch: got %q, want %q", b, fig1Bytes)
	}

	// 5. Pass `lw lint`
	stdoutLint, stderrLint, codeLint := captureRun(t, func() int {
		return run([]string{"lint", "--vault", root})
	})
	if codeLint != 0 {
		t.Fatalf("lw lint failed with exit code %d; stdout=%q stderr=%q", codeLint, stdoutLint, stderrLint)
	}

	// 6. Second `lw ingest --prepare <folder>` reports "already in the vault" / "nothing to ingest"
	stdout2, stderr2, code2 := captureRun(t, func() int {
		return run([]string{"ingest", "--prepare", folder, "--vault", root})
	})
	if code2 != 0 {
		t.Fatalf("second ingest exit code = %d; stdout=%q stderr=%q", code2, stdout2, stderr2)
	}
	combined2 := stdout2 + "\n" + stderr2
	if !strings.Contains(combined2, "already in the vault") && !strings.Contains(combined2, "nothing to ingest") {
		t.Errorf("second ingest output = %q, want 'already in the vault' or 'nothing to ingest'", combined2)
	}
}
