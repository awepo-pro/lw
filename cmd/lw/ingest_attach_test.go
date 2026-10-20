package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/vault"
)

// TestIngestAttachBackfillsOriginal pins 058 S2 D9 attach backfill lifecycle:
// `lw ingest --attach <file>` on a vault with a matching original-less raw stages the
// attach op with Author{Kind: "human"} (no API key set, no agent constructed), and
// `lw commit` lands both the updated raw .md and .html/.md.orig original on disk.
func TestIngestAttachBackfillsOriginal(t *testing.T) {
	// Ensure no API keys in environment and fail if newIngestAgent is called
	for _, k := range []string{"DEEPSEEK_API_KEY", "GLM_API_KEY", "OPENROUTER_API_KEY", "ANTHROPIC_AUTH_TOKEN", "TAVILY_API_KEY", "OPENAI_API_KEY"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	origAgent := newIngestAgent
	newIngestAgent = func(*stage.Engine, *config.Config, agent.SessionStore, extract.Extractor, []string) (agent.Agent, error) {
		t.Fatal("lw ingest --attach called newIngestAgent; it must call no model and construct no agent")
		return nil, errors.New("agent constructed")
	}
	defer func() { newIngestAgent = origAgent }()

	root := testutil.CopyFixture(t, "minimal")
	tempDir := t.TempDir()

	// 1. Prepare markdown source to attach to raw/articles/kv-cache-explained.md (.md -> .md.orig)
	v, err := vault.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	kvRaw, ok := v.RawSource("raw/articles/kv-cache-explained.md")
	if !ok {
		t.Fatal("missing kv-cache-explained.md in fixture")
	}
	mdSrcFile := filepath.Join(tempDir, "kv-cache-source.md")
	mdBytes := []byte(kvRaw.Body)
	if err := os.WriteFile(mdSrcFile, mdBytes, 0644); err != nil {
		t.Fatal(err)
	}

	// 2. Prepare HTML source to attach to a new committed raw source (.html -> .html)
	htmlContent := "<!DOCTYPE html><html><head><title>Test HTML Article</title></head><body><h1>Test HTML Article</h1><p>Important background prose for test.</p></body></html>\n"
	htmlSrcFile := filepath.Join(tempDir, "test-article.html")
	if err := os.WriteFile(htmlSrcFile, []byte(htmlContent), 0644); err != nil {
		t.Fatal(err)
	}
	htmlDoc, err := extract.NewHTML(http.DefaultClient).Extract(context.Background(), htmlSrcFile)
	if err != nil {
		t.Fatal(err)
	}
	htmlBodySHA := tools.SourceBodySHA(htmlDoc.Markdown)
	htmlRawPath := "raw/articles/test-article.md"
	htmlRawContent := fmt.Sprintf("---\nsource_url: https://example.org/test-article\ningested: 2026-09-01\nsha256: %s\n---\n\n%s", htmlBodySHA, htmlDoc.Markdown)
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(htmlRawPath)), []byte(htmlRawContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Run `lw ingest --attach <mdSrcFile> <htmlSrcFile> --vault <root>`
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--attach", mdSrcFile, htmlSrcFile, "--vault", root})
	})
	if code != 0 {
		t.Fatalf("ingest --attach exited with %d; stdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	// Verify the open changeset has Author{Kind: "human"}
	eng, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Current()
	if err != nil {
		eng.Close()
		t.Fatalf("read open changeset: %v", err)
	}
	if cs.Author.Kind != "human" {
		t.Errorf("changeset Author.Kind = %q, want human", cs.Author.Kind)
	}
	if len(cs.Live()) != 2 {
		t.Errorf("changeset live ops = %d, want 2", len(cs.Live()))
	}
	eng.Close()

	// Run `lw commit --vault <root> -m "backfill originals"`
	cStdout, cStderr, cCode := captureRun(t, func() int {
		return run([]string{"commit", "--vault", root, "-m", "backfill originals"})
	})
	if cCode != 0 {
		t.Fatalf("commit exited with %d; stdout:\n%s\nstderr:\n%s", cCode, cStdout, cStderr)
	}

	// Verify both original files and updated raw .md files exist on disk
	mdOrigPath := filepath.Join(root, "raw", "articles", "kv-cache-explained.md.orig")
	diskMDOrig, err := os.ReadFile(mdOrigPath)
	if err != nil {
		t.Fatalf("md.orig not found on disk at %s: %v", mdOrigPath, err)
	}
	if !bytes.Equal(diskMDOrig, mdBytes) {
		t.Errorf("md.orig bytes mismatch")
	}

	htmlOrigPath := filepath.Join(root, "raw", "articles", "test-article.html")
	diskHTMLOrig, err := os.ReadFile(htmlOrigPath)
	if err != nil {
		t.Fatalf("html orig not found on disk at %s: %v", htmlOrigPath, err)
	}
	if !bytes.Equal(diskHTMLOrig, []byte(htmlContent)) {
		t.Errorf("html orig bytes mismatch")
	}

	// Verify committed raw .md files declare original:
	kvPostBytes, err := os.ReadFile(filepath.Join(root, "raw", "articles", "kv-cache-explained.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(kvPostBytes), "original: raw/articles/kv-cache-explained.md.orig") {
		t.Errorf("kv-cache raw frontmatter missing original: %s", kvPostBytes)
	}

	htmlPostBytes, err := os.ReadFile(filepath.Join(root, "raw", "articles", "test-article.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(htmlPostBytes), "original: raw/articles/test-article.html") {
		t.Errorf("test-article raw frontmatter missing original: %s", htmlPostBytes)
	}
}

// TestIngestAttachSkipsAlreadyAttached pins D9 skip behaviour:
// when the matched raw already has original: set, prints:
// `skipped <src>: <raw> already has original <orig>` and
// `nothing to attach: every matched raw source already has an original`,
// exits 0, and opens no changeset.
func TestIngestAttachSkipsAlreadyAttached(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	tempDir := t.TempDir()

	v, err := vault.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	kvRaw, ok := v.RawSource("raw/articles/kv-cache-explained.md")
	if !ok {
		t.Fatal("missing kv-cache-explained.md in fixture")
	}

	// Update raw/articles/kv-cache-explained.md to already have an original
	rawDiskPath := filepath.Join(root, "raw", "articles", "kv-cache-explained.md")
	existingOrig := "raw/articles/kv-cache-explained.html"
	origSHA := "1111222233334444111122223333444411112222333344441111222233334444"
	kvDisk, err := os.ReadFile(rawDiskPath)
	if err != nil {
		t.Fatal(err)
	}
	withOrig := strings.Replace(string(kvDisk), "---\n\n", fmt.Sprintf("original: %s\noriginal_sha256: %s\n---\n\n", existingOrig, origSHA), 1)
	if err := os.WriteFile(rawDiskPath, []byte(withOrig), 0644); err != nil {
		t.Fatal(err)
	}

	// Prepare source file with matching body
	srcFile := filepath.Join(tempDir, "kv-cache.md")
	if err := os.WriteFile(srcFile, []byte(kvRaw.Body), 0644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--attach", srcFile, "--vault", root})
	})
	if code != 0 {
		t.Fatalf("expected code 0, got %d; stderr: %s", code, stderr)
	}

	wantSkip := fmt.Sprintf("skipped %s: raw/articles/kv-cache-explained.md already has original %s", srcFile, existingOrig)
	if !strings.Contains(stdout, wantSkip) {
		t.Errorf("stdout = %q, want it to contain %q", stdout, wantSkip)
	}
	wantNothing := "nothing to attach: every matched raw source already has an original"
	if !strings.Contains(stdout, wantNothing) {
		t.Errorf("stdout = %q, want it to contain %q", stdout, wantNothing)
	}

	// Verify no changeset was opened
	openDir := filepath.Join(root, ".llmwiki", "changesets", "open")
	entries, _ := os.ReadDir(openDir)
	if len(entries) > 0 {
		t.Errorf("found open changesets: %d, want 0", len(entries))
	}
}

// TestIngestAttachUnmatchedFailsBeforeChangeset pins D9 error behaviour:
// when an input does not match any committed raw's body SHA, exits 1
// naming the 12-hex SHA and opens no changeset.
func TestIngestAttachUnmatchedFailsBeforeChangeset(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	tempDir := t.TempDir()

	srcFile := filepath.Join(tempDir, "unmatched-unique-paper.md")
	unmatchedBody := "# Completely Unmatched Paper\n\nThis body has never been committed to the vault.\n"
	if err := os.WriteFile(srcFile, []byte(unmatchedBody), 0644); err != nil {
		t.Fatal(err)
	}

	doc, err := extract.NewFile().Extract(context.Background(), srcFile)
	if err != nil {
		t.Fatal(err)
	}
	sha12 := tools.SourceBodySHA(doc.Markdown)[:12]

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--attach", srcFile, "--vault", root})
	})
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d; stdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	wantErr := fmt.Sprintf("attach %s: no committed raw source matches its extracted body (sha256 %s) — run lw ingest %s to ingest it as a new source", srcFile, sha12, srcFile)
	if !strings.Contains(stderr, wantErr) {
		t.Errorf("stderr = %q, want it to contain %q", stderr, wantErr)
	}

	// Verify no changeset was opened
	openDir := filepath.Join(root, ".llmwiki", "changesets", "open")
	entries, _ := os.ReadDir(openDir)
	if len(entries) > 0 {
		t.Errorf("found open changesets: %d, want 0", len(entries))
	}
}

// TestIngestAttachFlagsAndDryRun pins flag mutual exclusions and dry-run:
// refuses --attach with --recompile, --prepare, or --kind (exit 2);
// --attach --dry-run prints `would attach ...` and opens no changeset.
func TestIngestAttachFlagsAndDryRun(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	tempDir := t.TempDir()

	v, err := vault.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	kvRaw, ok := v.RawSource("raw/articles/kv-cache-explained.md")
	if !ok {
		t.Fatal("missing kv-cache-explained.md in fixture")
	}
	srcFile := filepath.Join(tempDir, "kv-cache.md")
	if err := os.WriteFile(srcFile, []byte(kvRaw.Body), 0644); err != nil {
		t.Fatal(err)
	}

	// 1. Refuses --attach with --recompile (exit 2)
	t.Run("conflict_recompile", func(t *testing.T) {
		_, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--attach", "--recompile", srcFile, "--vault", root})
		})
		if code != 2 {
			t.Errorf("code = %d, want 2", code)
		}
		if !strings.Contains(stderr, "cannot use --attach with --recompile") {
			t.Errorf("stderr = %q, want cannot use --attach with --recompile", stderr)
		}
	})

	// 2. Refuses --attach with --prepare (exit 2)
	t.Run("conflict_prepare", func(t *testing.T) {
		_, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--attach", "--prepare", srcFile, "--vault", root})
		})
		if code != 2 {
			t.Errorf("code = %d, want 2", code)
		}
		if !strings.Contains(stderr, "cannot use --attach with --prepare") {
			t.Errorf("stderr = %q, want cannot use --attach with --prepare", stderr)
		}
	})

	// 3. Refuses --attach with --kind (exit 2)
	t.Run("conflict_kind", func(t *testing.T) {
		_, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--attach", "--kind", "article", srcFile, "--vault", root})
		})
		if code != 2 {
			t.Errorf("code = %d, want 2", code)
		}
		if !strings.Contains(stderr, "cannot use --attach with --kind") {
			t.Errorf("stderr = %q, want cannot use --attach with --kind", stderr)
		}
	})

	// 4. --attach --dry-run prints would attach and opens no changeset
	t.Run("dry_run", func(t *testing.T) {
		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--attach", "--dry-run", srcFile, "--vault", root})
		})
		if code != 0 {
			t.Fatalf("dry run exited with %d; stderr: %s", code, stderr)
		}
		wantLine := fmt.Sprintf("would attach %s -> %s (original: %s.orig)", srcFile, kvRaw.Path, kvRaw.Path)
		if !strings.Contains(stdout, wantLine) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, wantLine)
		}

		openDir := filepath.Join(root, ".llmwiki", "changesets", "open")
		entries, _ := os.ReadDir(openDir)
		if len(entries) > 0 {
			t.Errorf("found open changesets: %d, want 0", len(entries))
		}
	})
}
