package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// initClock is a fixed instant every init test uses, so log.md's entry and
// the scaffold's bytes are identical across runs and across directories
// (00-conventions.md §3: never time.Now() inside a testable function).
var initClock = time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

// initTypedAnswers is the transcript of a user who answers the first two
// questions and lets the rest default: the input runs out during the first
// page-type question, so every later answer is its default.
func initTypedAnswers() *strings.Reader {
	return strings.NewReader("my-domain\ntag1, Tag2, tag3, tag1\n")
}

// initAllDefaults answers every question with Enter.
func initAllDefaults() *strings.Reader {
	return strings.NewReader("\n\n\n\n\n\n")
}

// initTestdataDir returns the directory holding this test file, absolutely —
// the init tests move the process's working directory into the vault they
// build, so a relative testdata path would not survive that move.
func initTestdataDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller could not locate this test file")
	}
	return filepath.Dir(file)
}

// runInitIn scaffolds a vault into dir, moving the process there first, and
// fails t on any error.
func runInitIn(t *testing.T, dir string, o initOpts) {
	t.Helper()
	chdir(t, dir)
	o.now = initClock
	if err := runInit(o); err != nil {
		t.Fatalf("runInit: %v", err)
	}
}

// runInitErrIn is runInitIn for the cases that expect a refusal: it returns
// the error instead of failing on it.
func runInitErrIn(t *testing.T, dir string, o initOpts) error {
	t.Helper()
	chdir(t, dir)
	o.now = initClock
	return runInit(o)
}

// checkScaffoldLayout asserts the §14 tree init is specified to produce,
// including that no v0.1 code wrote .llmwiki/config.toml.
func checkScaffoldLayout(t *testing.T, dir string) {
	t.Helper()

	for _, name := range []string{"SCHEMA.md", "index.md", "log.md", "curator-memory.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, d := range []string{
		"raw/articles", "raw/papers", "raw/transcripts", "raw/assets",
		"wiki/entities", "wiki/concepts", "wiki/comparisons", "wiki/queries",
		".llmwiki/objects",
		".llmwiki/changesets/open", ".llmwiki/changesets/committed", ".llmwiki/changesets/rejected",
		".llmwiki/snapshots",
	} {
		if info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(d))); err != nil {
			t.Errorf("%s: %v", d, err)
		} else if !info.IsDir() {
			t.Errorf("%s: not a directory", d)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".llmwiki", "journal.ndjson")); err != nil {
		t.Errorf(".llmwiki/journal.ndjson: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".llmwiki", "config.toml")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf(".llmwiki/config.toml: no v1 code writes it, got stat err %v", err)
	}
}

// readInitSchema reads and parses the SCHEMA.md init wrote into dir.
func readInitSchema(t *testing.T, dir string) *vault.Schema {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "SCHEMA.md"))
	if err != nil {
		t.Fatalf("read SCHEMA.md: %v", err)
	}
	s, err := vault.ParseSchema(b)
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	return s
}

// TestCmdInitSchemaFlagLintsClean is the gotcha-3 assertion
// (s6-polish-release.md): the first thing a new user runs must not be
// broken. It walks the real command path — `lw init` through run(), then the
// same lint path `lw lint` uses — and requires zero errors.
func TestCmdInitSchemaFlagLintsClean(t *testing.T) {
	dir := t.TempDir()

	chdir(t, dir)
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"init", "--schema", "ml-systems"})
	})
	if code != 0 {
		t.Fatalf("init exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "initialized vault in ") {
		t.Errorf("stdout = %q, want it to report initialization", stdout)
	}

	checkScaffoldLayout(t, dir)

	stdout, stderr, code = captureRun(t, func() int {
		return run([]string{"lint", "--vault", dir})
	})
	if code != 0 {
		t.Fatalf("lint exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if stdout != "clean\n" {
		t.Fatalf("lint stdout = %q, want %q", stdout, "clean\n")
	}
}

func TestCmdInitThenStatusReportsEmptyVault(t *testing.T) {
	dir := t.TempDir()
	runInitIn(t, dir, initOpts{args: []string{"--schema", "ml-systems"}, out: io.Discard})

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"status", "--vault", dir})
	})
	if code != 0 {
		t.Fatalf("status exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if want := "0 pages · 0 raw · 10 tags"; !strings.Contains(stdout, want) {
		t.Errorf("status stdout = %q, want it to contain %q", stdout, want)
	}
	if !strings.Contains(stdout, "no open changeset") {
		t.Errorf("status stdout = %q, want it to contain %q", stdout, "no open changeset")
	}
}

func TestCmdInitSchemaProducesRealTaxonomy(t *testing.T) {
	dir := t.TempDir()
	runInitIn(t, dir, initOpts{args: []string{"--schema", "ml-systems"}, out: io.Discard})

	s := readInitSchema(t, dir)
	// Schema.Domain is the whole "## Domain" section, prose included, exactly
	// as the minimal fixture's is — the name leads it.
	if !strings.HasPrefix(s.Domain, "ml-systems ") {
		t.Errorf("Domain = %q, want it to lead with the domain name", s.Domain)
	}
	if len(s.Tags) != len(initDefaultTags) {
		t.Fatalf("got %d tags, want %d: %v", len(s.Tags), len(initDefaultTags), s.Tags)
	}
	for _, tag := range initDefaultTags {
		if !s.HasTag(tag.name) {
			t.Errorf("taxonomy is missing starter tag %q", tag.name)
		}
	}
	if len(s.Conventions) != len(initConventions) {
		t.Errorf("got %d conventions, want %d", len(s.Conventions), len(initConventions))
	}

	// The taxonomy is written sorted: it is the list a reader scans first,
	// and the order fm-taxonomy validates against.
	for i := 1; i < len(s.Tags); i++ {
		if s.Tags[i-1] > s.Tags[i] {
			t.Fatalf("tags not sorted: %v", s.Tags)
		}
	}
}

// TestCmdInitGoldenSchema pins the exact bytes of the file a new user reads
// first. The golden lives under this package's own testdata/, never under
// spec/fixtures (00-conventions.md §6).
func TestCmdInitGoldenSchema(t *testing.T) {
	dir := t.TempDir()
	runInitIn(t, dir, initOpts{args: []string{"--schema", "ml-systems"}, out: io.Discard})

	b, err := os.ReadFile(filepath.Join(dir, "SCHEMA.md"))
	if err != nil {
		t.Fatalf("read SCHEMA.md: %v", err)
	}
	testutil.Golden(t, filepath.Join(initTestdataDir(t), "testdata", "init-schema.want.md"), b)
}

func TestCmdInitPromptsReadStdin(t *testing.T) {
	dir := t.TempDir()
	var out strings.Builder
	runInitIn(t, dir, initOpts{in: initTypedAnswers(), out: &out})

	s := readInitSchema(t, dir)
	if !strings.HasPrefix(s.Domain, "my-domain ") {
		t.Errorf("Domain = %q, want it to lead with the typed domain name", s.Domain)
	}
	// Three typed tags — one duplicated, one mixed case — not the ten
	// defaults: what the curator typed is what the taxonomy holds.
	if want := "tag1,tag2,tag3"; strings.Join(s.Tags, ",") != want {
		t.Errorf("Tags = %v, want [%s]", s.Tags, want)
	}

	// The input ran out after the tags answer, so the four page-type
	// questions took their defaults rather than failing.
	b, err := os.ReadFile(filepath.Join(dir, "SCHEMA.md"))
	if err != nil {
		t.Fatalf("read SCHEMA.md: %v", err)
	}
	for _, pt := range initPageTypes {
		if !strings.Contains(string(b), pt.def) {
			t.Errorf("SCHEMA.md is missing the default convention for %s", pt.typ)
		}
	}

	logged, err := os.ReadFile(filepath.Join(dir, "log.md"))
	if err != nil {
		t.Fatalf("read log.md: %v", err)
	}
	if want := "- 2026-09-10T12:00:00Z init my-domain\n"; !strings.HasSuffix(string(logged), want) {
		t.Errorf("log.md = %q, want it to end with %q", logged, want)
	}

	// The input ended after the tags answer; the report says so rather than
	// silently defaulting the page-type conventions.
	if !strings.Contains(out.String(), "input ended before every question was answered") {
		t.Errorf("report = %q, want it to disclose the early end of input", out.String())
	}
}

func TestCmdInitEnterThroughTakesDefaults(t *testing.T) {
	dir := t.TempDir()
	var out strings.Builder
	runInitIn(t, dir, initOpts{in: initAllDefaults(), out: &out})

	s := readInitSchema(t, dir)
	// The directory's own name is the domain a bare Enter accepts.
	if want := filepath.Base(dir); !strings.HasPrefix(s.Domain, want+" ") {
		t.Errorf("Domain = %q, want it to lead with the directory name %q", s.Domain, want)
	}
	if len(s.Tags) != len(initDefaultTags) {
		t.Errorf("got %d tags, want the %d defaults", len(s.Tags), len(initDefaultTags))
	}
}

func TestCmdInitPromptTranscriptNamesEachQuestion(t *testing.T) {
	dir := t.TempDir()
	var out strings.Builder
	runInitIn(t, dir, initOpts{in: initTypedAnswers(), out: &out})

	got := out.String()
	for _, want := range []string{
		"Domain (a short lowercase name for the wiki)",
		"Tags, comma-separated (10-20 recommended)",
		"entity pages — what belongs in wiki/entities",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt transcript is missing %q:\n%s", want, got)
		}
	}
}

func TestCmdInitUselessTagAnswerFallsBackToStarterTaxonomy(t *testing.T) {
	dir := t.TempDir()
	var out strings.Builder
	// Nothing here survives normalization: an empty taxonomy would fail
	// every future page's fm-taxonomy check.
	runInitIn(t, dir, initOpts{in: strings.NewReader("my-domain\n  ,,,  \n"), out: &out})

	s := readInitSchema(t, dir)
	if len(s.Tags) != len(initDefaultTags) {
		t.Errorf("got %d tags, want the %d defaults", len(s.Tags), len(initDefaultTags))
	}
	if !strings.Contains(out.String(), "no usable tags in that answer") {
		t.Errorf("report = %q, want it to disclose the fallback", out.String())
	}
}

func TestCmdInitSchemaFlagNeverReadsStdin(t *testing.T) {
	dir := t.TempDir()
	// ErrReader fails any read. --schema is the non-interactive path, so a
	// read here would mean the flag does not do what it says.
	runInitIn(t, dir, initOpts{
		args: []string{"--schema", "ml-systems"},
		in:   iotest.ErrReader(errors.New("init read stdin under --schema")),
		out:  io.Discard,
	})
	checkScaffoldLayout(t, dir)
}

func TestCmdInitRefusesNonEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(existing, []byte("keep me\n"), 0o644); err != nil {
		t.Fatalf("write notes.txt: %v", err)
	}

	err := runInitErrIn(t, dir, initOpts{in: strings.NewReader(""), out: io.Discard})
	if err == nil {
		t.Fatal("runInit succeeded in a non-empty directory, want a refusal")
	}
	// The refusal names what it found and what --force does.
	for _, want := range []string{"notes.txt", "--force"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not mention %q", err.Error(), want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "SCHEMA.md")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("SCHEMA.md was written despite the refusal: %v", err)
	}
	if b, err := os.ReadFile(existing); err != nil || string(b) != "keep me\n" {
		t.Errorf("notes.txt = %q, %v; want it untouched", b, err)
	}
}

func TestCmdInitForceScaffoldsAroundExistingFiles(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(existing, []byte("keep me\n"), 0o644); err != nil {
		t.Fatalf("write notes.txt: %v", err)
	}

	runInitIn(t, dir, initOpts{args: []string{"--force", "--schema", "ml-systems"}, out: io.Discard})
	checkScaffoldLayout(t, dir)

	if b, err := os.ReadFile(existing); err != nil || string(b) != "keep me\n" {
		t.Fatalf("notes.txt = %q, %v; want it untouched", b, err)
	}

	// A second --force run is idempotent: nothing already there is rewritten.
	schema := filepath.Join(dir, "SCHEMA.md")
	before, err := os.ReadFile(schema)
	if err != nil {
		t.Fatalf("read SCHEMA.md: %v", err)
	}
	var out strings.Builder
	if err := runInitErrIn(t, dir, initOpts{args: []string{"--force", "--schema", "ml-systems"}, in: strings.NewReader(""), out: &out}); err != nil {
		t.Fatalf("second runInit: %v", err)
	}
	after, err := os.ReadFile(schema)
	if err != nil {
		t.Fatalf("re-read SCHEMA.md: %v", err)
	}
	if string(before) != string(after) {
		t.Error("SCHEMA.md was rewritten by a second --force run")
	}
	if !strings.Contains(out.String(), "skipped") {
		t.Errorf("second run stdout = %q, want it to report skipped files", out.String())
	}
}

func TestCmdInitDeterministicBytes(t *testing.T) {
	build := func(t *testing.T) map[string][]byte {
		t.Helper()
		dir := t.TempDir()
		runInitIn(t, dir, initOpts{args: []string{"--schema", "ml-systems"}, out: io.Discard})
		out := make(map[string][]byte, 4)
		for _, name := range []string{"SCHEMA.md", "index.md", "log.md", "curator-memory.md"} {
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			out[name] = b
		}
		return out
	}

	first, second := build(t), build(t)
	for name, want := range first {
		if string(second[name]) != string(want) {
			t.Errorf("%s differs between two runs with the same answers and clock:\nfirst:\n%s\nsecond:\n%s",
				name, want, second[name])
		}
		// Every file ends with exactly one newline (00-conventions.md §3).
		if !strings.HasSuffix(string(want), "\n") || strings.HasSuffix(string(want), "\n\n") {
			t.Errorf("%s does not end with exactly one newline", name)
		}
	}
}

// TestCmdInitCuratorMemoryHasNoSeededRules is the regression test for
// S6-C129: a live URL ingest showed that copying /.dev-notes/PLAN-v1.md §6's example
// verbatim into a fresh vault taught the curator a preference no reviewer
// had ever stated. curator-memory.md must show the file's shape — both
// headings, plus the explanatory comment — but seed zero rule lines.
func TestCmdInitCuratorMemoryHasNoSeededRules(t *testing.T) {
	dir := t.TempDir()
	runInitIn(t, dir, initOpts{args: []string{"--schema", "ml-systems"}, out: io.Discard})

	b, err := os.ReadFile(filepath.Join(dir, "curator-memory.md"))
	if err != nil {
		t.Fatalf("read curator-memory.md: %v", err)
	}
	got := string(b)

	for _, want := range []string{"## Page thresholds", "## Naming"} {
		if !strings.Contains(got, want) {
			t.Errorf("curator-memory.md is missing heading %q:\n%s", want, got)
		}
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "- ") {
			t.Errorf("curator-memory.md seeds a rule line %q, want none", line)
		}
	}
	for _, stale := range []string{"benchmark numbers", "gpt4"} {
		if strings.Contains(got, stale) {
			t.Errorf("curator-memory.md still contains the old seeded example %q", stale)
		}
	}
}

func TestCmdInitBadFlag(t *testing.T) {
	_, stderr, code := captureRun(t, func() int {
		return run([]string{"init", "--bogusflag"})
	})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%q", code, stderr)
	}
}

func TestCmdInitUnexpectedArg(t *testing.T) {
	_, stderr, code := captureRun(t, func() int {
		return run([]string{"init", "somewhere-else"})
	})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%q", code, stderr)
	}
}

func TestCmdInitEmptySchemaFlag(t *testing.T) {
	_, stderr, code := captureRun(t, func() int {
		return run([]string{"init", "--schema", ""})
	})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "--schema needs a domain name") {
		t.Errorf("stderr = %q, want it to explain the empty --schema", stderr)
	}
}
