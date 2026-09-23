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
	"sync/atomic"
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

// originalKinds extracts the "  kind: <kind>" lines the agent message
// carries, in buildIngestMessage's order — the per-source kind after any
// --kind override.
func originalKinds(t *testing.T, msg string) []string {
	t.Helper()
	var got []string
	for _, line := range strings.Split(msg, "\n") {
		if s, ok := strings.CutPrefix(line, "  kind: "); ok {
			got = append(got, s)
		}
	}
	if len(got) == 0 {
		t.Fatalf("the agent message lists no kinds; message:\n%s", msg)
	}
	return got
}

// noChangesetsAnywhere fails the test if the vault holds a changeset in
// any state — the absence proof for "opened nothing" that also covers
// rejected rollbacks, not just open ones.
func noChangesetsAnywhere(t *testing.T, root string) {
	t.Helper()
	for _, state := range []string{"open", "committed", "rejected"} {
		if got := countChangesets(t, root, state); got != 0 {
			t.Errorf("changesets/%s = %d, want 0 (nothing may be opened)", state, got)
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

// TestIngestFlagsAmongSources pins the interleaved parse: the frozen usage
// `lw ingest <url|path|dir>... [--kind K] [--dry-run]` lets the flags sit
// anywhere among the sources, but flag.FlagSet stops at the first
// positional — so cmdIngest must parse flag runs and sources a chunk at a
// time (parse, take one positional, re-parse the rest). "--" still ends
// flag parsing, so a file literally named "--weird.md" stays expressible,
// and flags-first invocations parse exactly as before the fix.
func TestIngestFlagsAmongSources(t *testing.T) {
	t.Run("dry_run_flag_after_dir", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		ingestLimitsEnv(t, 0)
		dir := t.TempDir()
		dirFile(t, dir, "a.md", "# A\n\nAlpha.\n")
		noAgentEver(t)

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, dir, "--dry-run"})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		if want := "would ingest " + filepath.Join(dir, "a.md"); !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
		if want := "within limits: 1 files, 1 KB (limit 10 files, 94 KB)"; !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
		noChangesetsAnywhere(t, root)
	})

	t.Run("kind_flag_between_files_applies_to_both", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		ingestLimitsEnv(t, 0)
		a := writtenSource(t, "first.md", "# First\n\nBody.\n")
		b := writtenSource(t, "second.md", "# Second\n\nBody.\n")
		rec := withRecordingIngestAgent(t, oneIngestOp(), nil)

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, a, "--kind", "paper", b})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		wantSourcesEqual(t, originalSources(t, rec.gotMsg), []string{a, b})
		wantSourcesEqual(t, originalKinds(t, rec.gotMsg), []string{"paper", "paper"})
	})

	t.Run("double_dash_after_source_takes_rest_verbatim", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		ingestLimitsEnv(t, 0)
		plain := writtenSource(t, "plain.md", "# Plain\n\nBody.\n")
		weird := writtenSource(t, "--weird.md", "# Weird\n\nBody.\n")
		rec := withRecordingIngestAgent(t, oneIngestOp(), nil)

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, plain, "--", weird})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		wantSourcesEqual(t, originalSources(t, rec.gotMsg), []string{plain, weird})
	})

	t.Run("double_dash_first_takes_rest_verbatim", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		ingestLimitsEnv(t, 0)
		weird := writtenSource(t, "--weird.md", "# Weird\n\nBody.\n")
		rec := withRecordingIngestAgent(t, oneIngestOp(), nil)

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, "--", weird})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		wantSourcesEqual(t, originalSources(t, rec.gotMsg), []string{weird})
	})

	t.Run("flags_first_unchanged", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		ingestLimitsEnv(t, 0)
		dir := t.TempDir()
		dirFile(t, dir, "a.md", "# A\n\nAlpha.\n")
		noAgentEver(t)

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--dry-run", "--vault", root, dir})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		if want := "within limits: 1 files, 1 KB (limit 10 files, 94 KB)"; !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
		noChangesetsAnywhere(t, root)
	})

	t.Run("undefined_flag_after_source_still_usage_error", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		ingestLimitsEnv(t, 0)
		a := writtenSource(t, "one.md", "# One\n\nBody.\n")
		noAgentEver(t)

		_, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, a, "--bogus"})
		})
		if code != 2 {
			t.Fatalf("exit code = %d, want 2; stderr=%q", code, stderr)
		}
		if !strings.Contains(stderr, "flag provided but not defined") {
			t.Fatalf("stderr = %q, want the flag package's undefined-flag error", stderr)
		}
	})
}

// TestIngestDirAndFileCountedTogether pins F.I4's counting scope: one
// invocation carrying a directory AND an explicit file counts both against
// the caps — 10 walker files + 1 file = 11 kept sources, over the 10-file
// limit, refused with the F.I4 message.
func TestIngestDirAndFileCountedTogether(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	ingestLimitsEnv(t, 0)
	dir := t.TempDir()
	for i := 0; i < 10; i++ {
		dirFile(t, dir, fmt.Sprintf("n%02d.md", i), fmt.Sprintf("# Note %02d\n\nBody.\n", i))
	}
	extra := writtenSource(t, "extra.md", "# Extra\n\nBody.\n")
	noAgentEver(t)

	want := "11 files (1 KB) to ingest; the limit is 10 files and 94 KB per ingest" +
		" (llm.limits.context_tokens 96000 × 4 × 25%). Split the folder into smaller ones."
	_, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--vault", root, dir, extra})
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, want) {
		t.Fatalf("stderr = %q, want it to contain %q", stderr, want)
	}
	noChangesetsAnywhere(t, root)
}

// TestIngestSameDirTwiceDedupedNotDoubleCounted pins F.I3's same-invocation
// dedupe for directories: the same directory given twice expands to every
// file twice, and the unchanged A-807 same-content rule drops the second
// copies — they print `skipped …: same content as …` and do NOT count
// toward the limits (10 kept files pass; double-counting would reject 20).
func TestIngestSameDirTwiceDedupedNotDoubleCounted(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	ingestLimitsEnv(t, 0)
	dir := t.TempDir()
	var want []string
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("n%02d.md", i)
		dirFile(t, dir, name, fmt.Sprintf("# Note %02d\n\nBody.\n", i))
		want = append(want, filepath.Join(dir, name))
	}
	rec := withRecordingIngestAgent(t, oneIngestOp(), nil)

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--vault", root, dir, dir})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	for _, p := range want {
		if skip := "skipped " + p + ": same content as " + p; !strings.Contains(stdout, skip) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, skip)
		}
	}
	wantSourcesEqual(t, originalSources(t, rec.gotMsg), want)
	if got := countChangesets(t, root, "open"); got != 1 {
		t.Errorf("open changesets = %d, want 1", got)
	}
}

// TestIngestKBRoundBoundaries pins the KB rendering at the exact rounding
// edges (KB = (bytes+1023)/1024): 1024 extracted bytes render as 1 KB and
// pass a 1024-byte cap exactly; 1025 render as 2 KB and fail it. The file
// contents end with exactly one "\n" and hold no "\r", so
// len(doc.Markdown) == len(file content) and the byte math is exact.
func TestIngestKBRoundBoundaries(t *testing.T) {
	t.Run("exactly_1024_bytes_is_1KB_and_passes", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		ingestLimitsEnv(t, 1024) // capBytes = 1024 × 4 × 25% = 1024
		dir := t.TempDir()
		dirFile(t, dir, "exact.md", strings.Repeat("a", 1023)+"\n") // 1024 bytes
		noAgentEver(t)

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, "--dry-run", dir})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		want := "within limits: 1 files, 1 KB (limit 10 files, 1 KB)"
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
	})

	t.Run("exactly_1025_bytes_is_2KB_and_fails", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		ingestLimitsEnv(t, 1024) // capBytes = 1024
		dir := t.TempDir()
		dirFile(t, dir, "over.md", strings.Repeat("a", 1024)+"\n") // 1025 bytes
		noAgentEver(t)

		want := "1 files (2 KB) to ingest; the limit is 10 files and 1 KB per ingest" +
			" (llm.limits.context_tokens 1024 × 4 × 25%). Split the folder into smaller ones."
		_, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, "--dry-run", dir})
		})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1; stderr=%q", code, stderr)
		}
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr = %q, want it to contain %q", stderr, want)
		}
	})
}

// TestIngestConfigLoadFailureStillSensible pins the config.Load hoist's
// blast radius (F.I4 needs Limits.ContextTokens earlier than pre-004, when
// Load ran after the scratch staging): a broken config.toml fails a
// file-only ingest with the same `load config:` verdict — exit 1, nothing
// opened, no agent — the same visible outcome as before 004. When the
// config is broken AND the folder over the cap, the config error wins:
// capBytes cannot even be computed without it.
func TestIngestConfigLoadFailureStillSensible(t *testing.T) {
	t.Run("file_only_ingest_reports_load_config", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		dir := filepath.Join(t.TempDir(), "xdg-config")
		t.Setenv("XDG_CONFIG_HOME", dir)
		writeConfigFile(t, dir, "not toml at all [[[")
		a := writtenSource(t, "one.md", "# One\n\nBody.\n")
		b := writtenSource(t, "two.md", "# Two\n\nBody.\n")
		noAgentEver(t)

		_, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, a, b})
		})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1; stderr=%q", code, stderr)
		}
		if !strings.Contains(stderr, "load config:") {
			t.Fatalf("stderr = %q, want it to carry the load-config verdict", stderr)
		}
		noChangesetsAnywhere(t, root)
	})

	t.Run("bad_config_wins_over_limit", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		dir := filepath.Join(t.TempDir(), "xdg-config")
		t.Setenv("XDG_CONFIG_HOME", dir)
		writeConfigFile(t, dir, "not toml at all [[[")
		over := t.TempDir()
		for i := 0; i < 11; i++ {
			dirFile(t, over, fmt.Sprintf("n%02d.md", i), fmt.Sprintf("# Note %02d\n\nBody.\n", i))
		}
		noAgentEver(t)

		_, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, over})
		})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1; stderr=%q", code, stderr)
		}
		if !strings.Contains(stderr, "load config:") {
			t.Fatalf("stderr = %q, want the config error to win over the limit message", stderr)
		}
		noChangesetsAnywhere(t, root)
	})
}

// TestIngestDryRunURLStillFetches documents --dry-run's network half:
// sizing a URL source means downloading it, so a dry run still fetches the
// URL — what it skips is the provider call, the changeset, the session and
// the agent. Pinned with a counting httptest server so a future
// "dry-run touches nothing at all" change has to face this behaviour.
func TestIngestDryRunURLStillFetches(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	ingestLimitsEnv(t, 0)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<html><body><h1>Dry</h1><p>Body.</p></body></html>")
	}))
	defer srv.Close()
	url := srv.URL + "/posts/dry.html"
	noAgentEver(t)

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--vault", root, "--dry-run", url})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	if atomic.LoadInt32(&hits) == 0 {
		t.Error("the URL was never fetched; --dry-run must still download a URL source to size it")
	}
	if want := "would ingest " + url; !strings.Contains(stdout, want) {
		t.Errorf("stdout = %q, want it to contain %q", stdout, want)
	}
	noChangesetsAnywhere(t, root)
}
