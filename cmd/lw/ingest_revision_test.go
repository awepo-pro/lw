package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// commitRawWithURL writes a committed raw source under root with the given
// sourceURL, rel path and body.
func commitRawWithURL(t *testing.T, root, rel, sourceURL, body string) {
	t.Helper()
	ingested, err := vault.ParseDate("2026-10-01")
	if err != nil {
		t.Fatal(err)
	}
	content := (&vault.RawSource{
		SourceURL: sourceURL,
		Ingested:  ingested,
		SHA256:    vault.BodySHA256(body),
		Body:      body,
	}).Serialize()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestC1_CLILine pins 070 C1: lw ingest <edited file> over a vault holding its
// earlier raw prints, before the agent runs, exactly:
// revision of <raw>: <N> page(s) cite it; the new raw will record supersedes
// (N from rawCitations), once per source. No such line for a non-revision, and
// none under --recompile or --attach.
func TestC1_CLILine(t *testing.T) {
	t.Run("one page cites", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		srcPath := writtenSource(t, "doc-c1.md", "# Doc C1\n\nInitial version.\n")
		commitRawWithURL(t, root, "raw/articles/doc-c1.md", srcPath, "# Doc C1\n\nInitial version.\n")
		rcWritePage(t, root, "wiki/concepts/p.md", []string{"raw/articles/doc-c1.md"}, "Body citing ^[raw/articles/doc-c1.md]\n")

		if err := os.WriteFile(srcPath, []byte("# Doc C1\n\nUpdated edited version.\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		var agentRan bool
		withFakeIngestAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
			agentRan = true
			return &fakeStageAgent{
				e:        e,
				sessions: sessions,
				ops: []stage.Op{
					{
						Kind:      stage.OpIngestSource,
						Path:      "raw/articles/doc-c1-2.md",
						Content:   []byte("content\n"),
						Extractor: "test",
					},
				},
			}, nil
		})

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, srcPath})
		})

		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		if !agentRan {
			t.Fatal("fake agent was never called")
		}
		wantLine := "revision of raw/articles/doc-c1.md: 1 page(s) cite it; the new raw will record supersedes"
		if !strings.Contains(stdout, wantLine) {
			t.Errorf("stdout does not contain %q\nstdout:\n%s", wantLine, stdout)
		}
	})

	t.Run("zero pages cite", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		srcPath := writtenSource(t, "doc-c1-zero.md", "# Doc C1 Zero\n\nInitial version.\n")
		commitRawWithURL(t, root, "raw/articles/doc-c1-zero.md", srcPath, "# Doc C1 Zero\n\nInitial version.\n")

		if err := os.WriteFile(srcPath, []byte("# Doc C1 Zero\n\nUpdated edited version.\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		withFakeIngestAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
			return &fakeStageAgent{
				e:        e,
				sessions: sessions,
				ops: []stage.Op{
					{
						Kind:      stage.OpIngestSource,
						Path:      "raw/articles/doc-c1-zero-2.md",
						Content:   []byte("content\n"),
						Extractor: "test",
					},
				},
			}, nil
		})

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, srcPath})
		})

		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		wantLine := "revision of raw/articles/doc-c1-zero.md: 0 page(s) cite it; the new raw will record supersedes"
		if !strings.Contains(stdout, wantLine) {
			t.Errorf("stdout does not contain %q\nstdout:\n%s", wantLine, stdout)
		}
	})

	t.Run("non-revision", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		srcPath := writtenSource(t, "fresh-source.md", "# Fresh\n\nFresh content never seen before.\n")

		withFakeIngestAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
			return &fakeStageAgent{
				e:        e,
				sessions: sessions,
				ops: []stage.Op{
					{
						Kind:      stage.OpIngestSource,
						Path:      "raw/articles/fresh-source.md",
						Content:   []byte("content\n"),
						Extractor: "test",
					},
				},
			}, nil
		})

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, srcPath})
		})

		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		if strings.Contains(stdout, "revision of ") {
			t.Errorf("stdout contains unexpected revision line:\n%s", stdout)
		}
	})

	t.Run("under recompile", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		commitRawWithURL(t, root, "raw/articles/recomp.md", "https://example.com/recomp", "# Recomp\n\nCommitted body.\n")

		withFakeIngestAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
			return &fakeStageAgent{
				e:        e,
				sessions: sessions,
				ops:      []stage.Op{rcPageOp("raw/articles/recomp.md")},
			}, nil
		})

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, "--recompile", "raw/articles/recomp.md"})
		})

		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		if strings.Contains(stdout, "revision of ") {
			t.Errorf("stdout under --recompile contains revision line:\n%s", stdout)
		}
	})

	t.Run("under attach", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		srcPath := writtenSource(t, "attach-doc.md", "# Attach Doc\n\nExact matching body.\n")
		commitRawWithURL(t, root, "raw/articles/attach-doc.md", srcPath, "# Attach Doc\n\nExact matching body.\n")

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, "--attach", srcPath})
		})

		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		if strings.Contains(stdout, "revision of ") {
			t.Errorf("stdout under --attach contains revision line:\n%s", stdout)
		}
	})

	t.Run("multiple sources one revision one fresh", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		revSrc := writtenSource(t, "rev-doc.md", "# Rev Doc\n\nInitial version.\n")
		commitRawWithURL(t, root, "raw/articles/rev-doc.md", revSrc, "# Rev Doc\n\nInitial version.\n")
		rcWritePage(t, root, "wiki/concepts/rev-p.md", []string{"raw/articles/rev-doc.md"}, "Body citing ^[raw/articles/rev-doc.md]\n")

		if err := os.WriteFile(revSrc, []byte("# Rev Doc\n\nUpdated edited version.\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		freshSrc := writtenSource(t, "fresh-doc.md", "# Fresh Doc\n\nFresh version.\n")

		withFakeIngestAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
			return &fakeStageAgent{
				e:        e,
				sessions: sessions,
				ops: []stage.Op{
					{
						Kind:      stage.OpIngestSource,
						Path:      "raw/articles/rev-doc-2.md",
						Content:   []byte("content\n"),
						Extractor: "test",
					},
					{
						Kind:      stage.OpIngestSource,
						Path:      "raw/articles/fresh-doc.md",
						Content:   []byte("content\n"),
						Extractor: "test",
					},
				},
			}, nil
		})

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, revSrc, freshSrc})
		})

		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		wantRev := "revision of raw/articles/rev-doc.md: 1 page(s) cite it; the new raw will record supersedes"
		if !strings.Contains(stdout, wantRev) {
			t.Errorf("stdout missing revision line: %q\nstdout:\n%s", wantRev, stdout)
		}
		if strings.Count(stdout, "revision of ") != 1 {
			t.Errorf("expected exactly 1 revision line, got:\n%s", stdout)
		}
	})
}

// TestC2_Brief pins 070 C2: lw ingest --prepare <edited file> brief contains,
// under that source, the line:
// This source revises raw/articles/x.md. Pages citing the old raw: wiki/concepts/p.md. Re-read each and stage.patch_page it to cite the new raw (keep every claim; fix sources:).
// (or Pages citing the old raw: none.). A non-revision --prepare brief is
// byte-identical to v2.55.0 (golden).
func TestC2_Brief(t *testing.T) {
	t.Run("pages cite old raw", func(t *testing.T) {
		prepHermetic(t)
		prepNoAgent(t)
		root := testutil.CopyFixture(t, "minimal")
		srcPath := writtenSource(t, "brief-rev.md", "# Brief Rev\n\nInitial body.\n")
		commitRawWithURL(t, root, "raw/articles/brief-rev.md", srcPath, "# Brief Rev\n\nInitial body.\n")
		rcWritePage(t, root, "wiki/concepts/p.md", []string{"raw/articles/brief-rev.md"}, "Body citing ^[raw/articles/brief-rev.md]\n")

		if err := os.WriteFile(srcPath, []byte("# Brief Rev\n\nEdited body.\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		stdout, stderr, code := prepareRun(t, root, srcPath)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		if stderr != "" {
			t.Errorf("stderr = %q, want nothing", stderr)
		}

		wantLine := "This source revises raw/articles/brief-rev.md. Pages citing the old raw: wiki/concepts/p.md. Re-read each and stage.patch_page it to cite the new raw (keep every claim; fix sources:)."
		if !strings.Contains(stdout, wantLine) {
			t.Errorf("stdout does not contain %q\nstdout:\n%s", wantLine, stdout)
		}

		// Also verify lw tool --brief renders the same line
		toolOut, toolErr, toolCode := captureRun(t, func() int {
			return run([]string{"tool", "--vault", root, "--brief"})
		})
		if toolCode != 0 {
			t.Fatalf("tool --brief exit code = %d, stderr=%q", toolCode, toolErr)
		}
		if !strings.Contains(toolOut, wantLine) {
			t.Errorf("tool --brief does not contain %q\ntool stdout:\n%s", wantLine, toolOut)
		}
	})

	t.Run("no pages cite old raw", func(t *testing.T) {
		prepHermetic(t)
		prepNoAgent(t)
		root := testutil.CopyFixture(t, "minimal")
		srcPath := writtenSource(t, "brief-none.md", "# Brief None\n\nInitial body.\n")
		commitRawWithURL(t, root, "raw/articles/brief-none.md", srcPath, "# Brief None\n\nInitial body.\n")

		if err := os.WriteFile(srcPath, []byte("# Brief None\n\nEdited body.\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		stdout, stderr, code := prepareRun(t, root, srcPath)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		if stderr != "" {
			t.Errorf("stderr = %q, want nothing", stderr)
		}

		wantLine := "This source revises raw/articles/brief-none.md. Pages citing the old raw: none. Re-read each and stage.patch_page it to cite the new raw (keep every claim; fix sources:)."
		if !strings.Contains(stdout, wantLine) {
			t.Errorf("stdout does not contain %q\nstdout:\n%s", wantLine, stdout)
		}

		// Also verify lw tool --brief
		toolOut, toolErr, toolCode := captureRun(t, func() int {
			return run([]string{"tool", "--vault", root, "--brief"})
		})
		if toolCode != 0 {
			t.Fatalf("tool --brief exit code = %d, stderr=%q", toolCode, toolErr)
		}
		if !strings.Contains(toolOut, wantLine) {
			t.Errorf("tool --brief does not contain %q\ntool stdout:\n%s", wantLine, toolOut)
		}
	})

	t.Run("non-revision prepare brief unchanged", func(t *testing.T) {
		prepHermetic(t)
		prepNoAgent(t)
		root := testutil.CopyFixture(t, "minimal")
		srcPath := writtenSource(t, "fresh-prepare.md", "# Fresh Prepare\n\nFresh body.\n")

		stdout, stderr, code := prepareRun(t, root, srcPath)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		if strings.Contains(stdout, "This source revises") {
			t.Errorf("stdout for non-revision contains revision line:\n%s", stdout)
		}
	})
}
