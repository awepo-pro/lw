package main

// ingest_dir_test.go pins 004 T2: `lw ingest <dir>` — folder expansion
// through extract.Walk (F.I1), the soft ErrNotText skip for directory
// files only (F.I2), the unchanged A-807 dedupe (F.I3), the per-invocation
// limits applied only when at least one argument was a directory (F.I4,
// correction #3), `--dry-run` (F.I5) and the usage text (F.I6). Every test
// runs through the real run() dispatch with the newIngestAgent seam swapped
// for a fake — no LLM, no network (httptest for the URL pin only).

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
)

// ingestLimitsEnv points config.Load at a scratch XDG dir carrying
// max_tokens = 8192 and, when contextTokens > 0, [llm.limits]
// context_tokens — the only knob the F.I4 byte cap reads (capBytes =
// context_tokens × 4 × 25%). Never the user's real config.toml.
func ingestLimitsEnv(t *testing.T, contextTokens int) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "xdg-config")
	t.Setenv("XDG_CONFIG_HOME", dir)
	toml := "[llm]\nmax_tokens = 8192\n"
	if contextTokens > 0 {
		toml += fmt.Sprintf("[llm.limits]\ncontext_tokens = %d\n", contextTokens)
	}
	writeConfigFile(t, dir, toml)
}

// noAgentEver fails the test if cmdIngest reaches newIngestAgent — the
// seam whose call is what resolves the API key and builds the loop — so a
// limit rejection or dry-run cannot secretly construct one.
func noAgentEver(t *testing.T) {
	t.Helper()
	withFakeIngestAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
		t.Fatal("newIngestAgent was called; this path must open no changeset and resolve no key")
		return nil, nil
	})
}

// dirFile writes content to name inside dir (not its own temp dir, the way
// writtenSource does) — a file extract.Walk must select or skip.
func dirFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// originalSources extracts the "  original source: <src>" lines the agent
// message carries, in buildIngestMessage's order — the exact argument list
// (post-expansion) the agent was told to ingest.
func originalSources(t *testing.T, msg string) []string {
	t.Helper()
	var got []string
	for _, line := range strings.Split(msg, "\n") {
		if s, ok := strings.CutPrefix(line, "  original source: "); ok {
			got = append(got, s)
		}
	}
	if len(got) == 0 {
		t.Fatalf("the agent message lists no original sources; message:\n%s", msg)
	}
	return got
}

// wantSourcesEqual fails the test unless got equals want element-wise, in
// order — the pin shape for "the message lists exactly these files".
func wantSourcesEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("agent message lists %d source(s) %q, want %d %q", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("agent message source[%d] = %q, want %q (full list %q, want %q)", i, got[i], want[i], got, want)
		}
	}
}

// TestIngestDirExpandsWalkerFiles pins F.I1 with a tree holding every skip
// reason except "not a regular file": 3 eligible files must reach the agent
// message as exactly those 3 original sources, and the 5 pass-overs must
// print `skipped <path>: <reason>` on stdout, in WalkDir (lexical) order.
func TestIngestDirExpandsWalkerFiles(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	ingestLimitsEnv(t, 0)
	dir := t.TempDir()
	a := dirFile(t, dir, "a.md", "# A\n\nAlpha.\n")
	dirFile(t, dir, "b.markdown", "# B\n\nBeta.\n")
	dirFile(t, dir, "sub/c.txt", "# C\n\nGamma.\n")
	dirFile(t, dir, ".hidden.md", "# hidden\n")
	dirFile(t, dir, ".git/x.md", "# committed noise\n")
	dirFile(t, dir, "empty.md", "")
	dirFile(t, dir, "img.png", "\x89PNG-not-really")
	if err := os.Symlink(a, filepath.Join(dir, "link.md")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	rec := withRecordingIngestAgent(t, oneIngestOp(), nil)

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--vault", root, dir})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}

	wantSkips := strings.Join([]string{
		"skipped " + filepath.Join(dir, ".git") + ": hidden",
		"skipped " + filepath.Join(dir, ".hidden.md") + ": hidden",
		"skipped " + filepath.Join(dir, "empty.md") + ": empty",
		"skipped " + filepath.Join(dir, "img.png") + ": unsupported type",
		"skipped " + filepath.Join(dir, "link.md") + ": symlink",
	}, "\n")
	if !strings.Contains(stdout, wantSkips) {
		t.Fatalf("stdout is missing the exact skip block:\n%s\ngot:\n%s", wantSkips, stdout)
	}
	wantSourcesEqual(t, originalSources(t, rec.gotMsg), []string{
		filepath.Join(dir, "a.md"),
		filepath.Join(dir, "b.markdown"),
		filepath.Join(dir, "sub", "c.txt"),
	})

	// Exactly one open changeset — the folder became one ingest.
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()
	if _, err := e.Current(); err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got := countChangesets(t, root, "open"); got != 1 {
		t.Errorf("open changesets = %d, want 1", got)
	}
}

// TestIngestDirFileURLMixed pins F.I1's pass-through half: a directory
// argument expands in place between the other arguments — files and a URL
// keep their order, and all three reach the agent message.
func TestIngestDirFileURLMixed(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	ingestLimitsEnv(t, 0)
	dir := t.TempDir()
	inDir := dirFile(t, dir, "from-dir.md", "# From Dir\n\nBody.\n")
	file := writtenSource(t, "solo.md", "# Solo\n\nBody.\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<html><body><h1>Mixed</h1><p>Body.</p></body></html>")
	}))
	defer srv.Close()
	url := srv.URL + "/posts/mixed.html"

	rec := withRecordingIngestAgent(t, oneIngestOp(), nil)

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--vault", root, dir, file, url})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	wantSourcesEqual(t, originalSources(t, rec.gotMsg), []string{inDir, file, url})
}

// TestIngestDirEmptyErrorsWithNoChangeset pins F.I1's empty half: a
// directory that yields zero walker files — empty, or only unsupported
// entries — fails the whole command with `no ingestible files in <dir>`
// before anything is opened, and newIngestAgent is never reached.
func TestIngestDirEmptyErrorsWithNoChangeset(t *testing.T) {
	cases := []struct {
		name string
		fill func(t *testing.T, dir string)
	}{
		{"totally_empty", func(t *testing.T, dir string) {}},
		{"only_unsupported", func(t *testing.T, dir string) {
			dirFile(t, dir, "img.png", "\x89PNG-not-really")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := testutil.CopyFixture(t, "minimal")
			ingestLimitsEnv(t, 0)
			dir := t.TempDir()
			c.fill(t, dir)
			noAgentEver(t)

			_, stderr, code := captureRun(t, func() int {
				return run([]string{"ingest", "--vault", root, dir})
			})
			if code != 1 {
				t.Fatalf("exit code = %d, want 1; stderr=%q", code, stderr)
			}
			want := fmt.Sprintf("no ingestible files in %s", dir)
			if !strings.Contains(stderr, want) {
				t.Fatalf("stderr = %q, want it to contain %q", stderr, want)
			}
			// The error fires during expansion, before the engine is ever
			// opened, so there is no changesets/ directory to count —
			// Current() is the absence proof.
			e, err := stage.OpenEngine(root)
			if err != nil {
				t.Fatalf("OpenEngine: %v", err)
			}
			defer e.Close()
			if _, err := e.Current(); !errors.Is(err, stage.ErrNoChangeset) {
				t.Fatalf("Current = %v, want ErrNoChangeset (nothing opened)", err)
			}
		})
	}
}

// TestIngestDirOverFileLimit pins F.I4's file half: 11 walker files from a
// directory invocation fail after dedupe with the exact limit message, no
// changeset and no agent — the key is never resolved. Default limits: 10
// files, context_tokens 96000 → 96000 bytes ≈ 94 KB; 11 tiny files stay
// under the byte cap, so the file cap is what trips.
func TestIngestDirOverFileLimit(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	ingestLimitsEnv(t, 0)
	dir := t.TempDir()
	for i := 0; i < 11; i++ {
		dirFile(t, dir, fmt.Sprintf("n%02d.md", i), fmt.Sprintf("# Note %02d\n\nBody.\n", i))
	}
	noAgentEver(t)

	want := "11 files (1 KB) to ingest; the limit is 10 files and 94 KB per ingest" +
		" (llm.limits.context_tokens 96000 × 4 × 25%). Split the folder into smaller ones."
	_, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--vault", root, dir})
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, want) {
		t.Fatalf("stderr = %q, want it to contain %q", stderr, want)
	}
	if got := countChangesets(t, root, "open"); got != 0 {
		t.Errorf("open changesets = %d, want 0", got)
	}
}

// TestIngestDirOverByteLimit pins F.I4's byte half: context_tokens = 40
// puts capBytes at 40; two files totalling 41 bytes of extracted markdown
// fail with the same message shape. Every file ends with exactly one "\n"
// and holds no "\r", so len(doc.Markdown) == len(file content) and the
// byte math is exact.
func TestIngestDirOverByteLimit(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	ingestLimitsEnv(t, 40)
	dir := t.TempDir()
	dirFile(t, dir, "big.md", strings.Repeat("a", 29)+"\n")   // 30 bytes
	dirFile(t, dir, "small.md", strings.Repeat("b", 10)+"\n") // 11 bytes → 41 total
	noAgentEver(t)

	want := "2 files (1 KB) to ingest; the limit is 10 files and 1 KB per ingest" +
		" (llm.limits.context_tokens 40 × 4 × 25%). Split the folder into smaller ones."
	_, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--vault", root, dir})
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, want) {
		t.Fatalf("stderr = %q, want it to contain %q", stderr, want)
	}
	if got := countChangesets(t, root, "open"); got != 0 {
		t.Errorf("open changesets = %d, want 0", got)
	}
}

// TestIngestDirExactlyAtCaps pins F.I4's boundary: exactly 10 files pass,
// and exactly capBytes bytes pass — "over" means strictly greater.
func TestIngestDirExactlyAtCaps(t *testing.T) {
	t.Run("exactly_10_files", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		ingestLimitsEnv(t, 0)
		dir := t.TempDir()
		for i := 0; i < 10; i++ {
			dirFile(t, dir, fmt.Sprintf("n%02d.md", i), fmt.Sprintf("# Note %02d\n\nBody.\n", i))
		}
		rec := withRecordingIngestAgent(t, oneIngestOp(), nil)

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, dir})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		wantSourcesEqual(t, originalSources(t, rec.gotMsg), func() []string {
			paths := make([]string, 0, 10)
			for i := 0; i < 10; i++ {
				paths = append(paths, filepath.Join(dir, fmt.Sprintf("n%02d.md", i)))
			}
			return paths
		}())
		if got := countChangesets(t, root, "open"); got != 1 {
			t.Errorf("open changesets = %d, want 1", got)
		}
	})

	t.Run("exactly_cap_bytes", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		ingestLimitsEnv(t, 40) // capBytes = 40
		dir := t.TempDir()
		dirFile(t, dir, "big.md", strings.Repeat("a", 29)+"\n")  // 30
		dirFile(t, dir, "small.md", strings.Repeat("b", 9)+"\n") // 10 → 40 total
		rec := withRecordingIngestAgent(t, oneIngestOp(), nil)

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, dir})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		wantSourcesEqual(t, originalSources(t, rec.gotMsg), []string{
			filepath.Join(dir, "big.md"),
			filepath.Join(dir, "small.md"),
		})
		if got := countChangesets(t, root, "open"); got != 1 {
			t.Errorf("open changesets = %d, want 1", got)
		}
	})
}

// TestIngestDirNotTextSkippedSoftly pins F.I2's soft half: a NUL-bearing
// .txt inside the directory prints `skipped <path>: not text` and is
// dropped — the rest of the folder still ingests, exit 0.
func TestIngestDirNotTextSkippedSoftly(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	ingestLimitsEnv(t, 0)
	dir := t.TempDir()
	dirFile(t, dir, "bad.txt", "ok\x00binary")
	good := dirFile(t, dir, "good.md", "# Good\n\nBody.\n")
	rec := withRecordingIngestAgent(t, oneIngestOp(), nil)

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--vault", root, dir})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	want := "skipped " + filepath.Join(dir, "bad.txt") + ": not text"
	if !strings.Contains(stdout, want) {
		t.Fatalf("stdout = %q, want it to contain %q", stdout, want)
	}
	wantSourcesEqual(t, originalSources(t, rec.gotMsg), []string{good})
	if got := countChangesets(t, root, "open"); got != 1 {
		t.Errorf("open changesets = %d, want 1", got)
	}
}

// TestIngestExplicitNotTextFileFails pins F.I2's hard half: the same
// non-text file handed over as an EXPLICIT argument fails the command
// exactly as any other extract failure — nothing opened, no soft skip.
func TestIngestExplicitNotTextFileFails(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	ingestLimitsEnv(t, 0)
	bad := writtenSource(t, "bad.txt", "ok\x00binary")
	noAgentEver(t)

	_, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--vault", root, bad})
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "not text") {
		t.Fatalf("stderr = %q, want it to carry the not-text verdict", stderr)
	}
	if !strings.Contains(stderr, bad) {
		t.Fatalf("stderr = %q, want it to name %q", stderr, bad)
	}
	// Extraction fails before the engine is ever opened (the "extract
	// first, open nothing on failure" ordering), so there is no
	// changesets/ directory to count — Current() is the absence proof.
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()
	if _, err := e.Current(); !errors.Is(err, stage.ErrNoChangeset) {
		t.Fatalf("Current = %v, want ErrNoChangeset (nothing opened)", err)
	}
}

// TestIngestDirDryRun pins F.I5: --dry-run runs expansion, extraction,
// dedupe and the limit check, prints `would ingest <src>` per kept source
// plus the final verdict line — and opens nothing: no changeset in any
// state (sessions live inside open changesets, so this covers "no session
// file" too) and no agent construction. The over-limit dry-run fails with
// the F.I4 message, exit 1, still nothing opened.
func TestIngestDirDryRun(t *testing.T) {
	t.Run("within_limits_prints_verdict_opens_nothing", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		ingestLimitsEnv(t, 0)
		dir := t.TempDir()
		dirFile(t, dir, "a.md", "# A\n\nAlpha.\n")
		dirFile(t, dir, "b.markdown", "# B\n\nBeta.\n")
		dirFile(t, dir, "c.txt", "# C\n\nGamma.\n")
		dirFile(t, dir, "img.png", "\x89PNG-not-really")
		noAgentEver(t)

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, "--dry-run", dir})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		for _, f := range []string{"a.md", "b.markdown", "c.txt"} {
			if want := "would ingest " + filepath.Join(dir, f); !strings.Contains(stdout, want) {
				t.Errorf("stdout = %q, want it to contain %q", stdout, want)
			}
		}
		// 34 extracted bytes → ceiling to 1 KB.
		wantVerdict := "within limits: 3 files, 1 KB (limit 10 files, 94 KB)"
		if !strings.Contains(stdout, wantVerdict) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, wantVerdict)
		}
		if !strings.Contains(stdout, "skipped "+filepath.Join(dir, "img.png")+": unsupported type") {
			t.Errorf("stdout = %q, want the walker skip line", stdout)
		}
		for _, state := range []string{"open", "committed", "rejected"} {
			if got := countChangesets(t, root, state); got != 0 {
				t.Errorf("changesets/%s = %d, want 0 (dry-run opens nothing)", state, got)
			}
		}
	})

	t.Run("over_limit_fails_with_message", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		ingestLimitsEnv(t, 0)
		dir := t.TempDir()
		for i := 0; i < 11; i++ {
			dirFile(t, dir, fmt.Sprintf("n%02d.md", i), fmt.Sprintf("# Note %02d\n\nBody.\n", i))
		}
		noAgentEver(t)

		want := "11 files (1 KB) to ingest; the limit is 10 files and 94 KB per ingest" +
			" (llm.limits.context_tokens 96000 × 4 × 25%). Split the folder into smaller ones."
		_, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, "--dry-run", dir})
		})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1; stderr=%q", code, stderr)
		}
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr = %q, want it to contain %q", stderr, want)
		}
		for _, state := range []string{"open", "committed", "rejected"} {
			if got := countChangesets(t, root, state); got != 0 {
				t.Errorf("changesets/%s = %d, want 0", state, got)
			}
		}
	})
}

// TestIngestDirAlreadyIngestedNotCounted pins F.I3's half of the folder
// leg: a directory file whose body the vault already holds is skipped by
// the unchanged A-807 dedupe and does not count toward the F.I4 limits.
// The proof: context_tokens = 40 (capBytes 40) while the committed body is
// 713 bytes — had the skipped file been counted, the byte cap would reject;
// the fresh 6-byte file alone passes.
func TestIngestDirAlreadyIngestedNotCounted(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	ingestLimitsEnv(t, 40)

	// The committed body, byte for byte — extract gives it back unchanged
	// (already \n-normalized with exactly one trailing newline), so
	// SourceBodySHA lands on the frontmatter sha the vault recorded.
	e := openEngine(t, root)
	r, ok := e.Vault().RawSource("raw/articles/kv-cache-explained.md")
	if !ok {
		t.Fatal("fixture has no raw/articles/kv-cache-explained.md")
	}
	if tools.SourceBodySHA(r.Body) != r.SHA256 {
		t.Fatalf("fixture sha mismatch: SourceBodySHA(body) = %s, stored %s", tools.SourceBodySHA(r.Body), r.SHA256)
	}
	e.Close()

	dir := t.TempDir()
	dirFile(t, dir, "kv.md", r.Body)
	fresh := dirFile(t, dir, "fresh.md", "fresh\n") // 6 bytes ≤ capBytes 40
	rec := withRecordingIngestAgent(t, oneIngestOp(), nil)

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--vault", root, dir})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	want := "skipped " + filepath.Join(dir, "kv.md") + ": already in the vault at raw/articles/kv-cache-explained.md"
	if !strings.Contains(stdout, want) {
		t.Fatalf("stdout = %q, want it to contain %q", stdout, want)
	}
	wantSourcesEqual(t, originalSources(t, rec.gotMsg), []string{fresh})
	if got := countChangesets(t, root, "open"); got != 1 {
		t.Errorf("open changesets = %d, want 1", got)
	}
}

// TestIngestFileOnlyNoLimit pins correction #3: the F.I4 limits apply only
// when at least one argument was a directory — 11 explicit file arguments
// (more than the 10-file cap) still ingest, byte-identical behaviour to
// before 004 for the invocation shape users already rely on.
func TestIngestFileOnlyNoLimit(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	ingestLimitsEnv(t, 0)
	var args []string
	for i := 0; i < 11; i++ {
		args = append(args, writtenSource(t, fmt.Sprintf("solo%02d.md", i), fmt.Sprintf("# Solo %02d\n\nBody.\n", i)))
	}
	rec := withRecordingIngestAgent(t, oneIngestOp(), nil)

	stdout, stderr, code := captureRun(t, func() int {
		return run(append([]string{"ingest", "--vault", root}, args...))
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	if got := originalSources(t, rec.gotMsg); len(got) != 11 {
		t.Fatalf("agent message lists %d source(s), want 11", len(got))
	}
	if got := countChangesets(t, root, "open"); got != 1 {
		t.Errorf("open changesets = %d, want 1", got)
	}
}

// TestIngestUsageAndHelp pins F.I6: the no-args usage line names dirs and
// both flags, and the top-level help row advertises <url|path|dir>.
func TestIngestUsageAndHelp(t *testing.T) {
	t.Run("no_args_usage_line", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		_, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root})
		})
		if code != 2 {
			t.Fatalf("exit code = %d, want 2", code)
		}
		want := "usage: lw ingest <url|path|dir>... [--kind K] [--dry-run]\n"
		if stderr != want {
			t.Fatalf("stderr = %q, want %q", stderr, want)
		}
	})

	t.Run("top_level_help_row", func(t *testing.T) {
		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"--help"})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		if !strings.Contains(stdout, "ingest <url|path|dir>...") {
			t.Fatalf("help output = %q, want it to contain the dir-ingest row", stdout)
		}
	})
}
