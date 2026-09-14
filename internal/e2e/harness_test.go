package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/testutil"
)

// lwDeadline bounds one lw subprocess in the smoke and fault scenarios. It is
// a generous ceiling for the slowest verb those scenarios drive (an ingest
// that talks to the fake LLM twice), far above anything a healthy run needs,
// yet short enough that a fault server which stalls instead of refusing
// cannot hang the test binary. The stress run does not inherit it:
// runLWTimed derives each step's kill ceiling from that step's own budget,
// because a step inside a 120s budget killed at 60s would be misreported as
// a hang instead of measured against the budget it is there to enforce.
const lwDeadline = 60 * time.Second

// stressSlack is the headroom runLWTimed adds on top of a step's budget
// before it kills the subprocess. A budget is the point a step is judged
// against, so the kill must land past it, not at it: the breach is then
// reported by the budget comparison instead of being pre-empted as exit -1.
const stressSlack = 30 * time.Second

// fakeAPIKey is the literal api_key every harness config carries. It resolves
// as a literal (config.ResolveAPIKey's default branch, config.go:159-174), so
// the subprocess needs no environment secret at all — and the fake server
// asserts on exactly this string, proving the config the harness wrote is the
// one lw actually used.
const fakeAPIKey = "e2e-literal-key"

// env is the sandbox one test runs its lw subprocesses in: a private HOME,
// XDG_CONFIG_HOME and TMPDIR under one temp root, plus the directory the
// subprocess runs with as its working directory.
//
// The executable environment is exactly the four variables environ lists —
// never os.Environ() — so no real DEEPSEEK_API_KEY, XDG_DATA_HOME or proxy
// setting can reach lw, and a missing config file could not silently fall
// back to the DeepSeek default and read a live secret either (config.go:115).
type env struct {
	root   string // temp root everything else lives under
	home   string // HOME
	config string // XDG_CONFIG_HOME
	tmp    string // TMPDIR
	work   string // subprocess working directory
}

// newEnv builds a fresh env for one test, with every directory created.
func newEnv(t *testing.T) *env {
	t.Helper()

	e := &env{
		root:   t.TempDir(),
		home:   filepath.Join(t.TempDir(), "home"),
		config: filepath.Join(t.TempDir(), "config"),
		tmp:    filepath.Join(t.TempDir(), "tmp"),
		work:   filepath.Join(t.TempDir(), "work"),
	}
	for _, d := range []string{e.home, e.config, e.tmp, e.work} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("e2e: create %s: %v", d, err)
		}
	}
	return e
}

// environ returns the complete environment the subprocess sees: HOME,
// XDG_CONFIG_HOME, PATH and TMPDIR, and nothing else.
func (e *env) environ() []string {
	return []string{
		"HOME=" + e.home,
		"XDG_CONFIG_HOME=" + e.config,
		"PATH=" + os.Getenv("PATH"),
		"TMPDIR=" + e.tmp,
	}
}

// writeSource writes content to name inside e's working directory (name may
// carry directory separators, created as needed) and returns its absolute
// path — the local-path source an `lw ingest` scenario hands the binary.
func (e *env) writeSource(t *testing.T, name, content string) string {
	t.Helper()

	path := filepath.Join(e.work, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("e2e: create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("e2e: write %s: %v", path, err)
	}
	return path
}

// writeConfig writes <dir>/lw/config.toml — the file config.Load reads when
// XDG_CONFIG_HOME=dir — pointing base_url at baseURL, and returns the path.
//
// The harness ALWAYS writes one: a missing config silently falls back to
// Default(), whose endpoint is the real DeepSeek API and whose key is
// env:DEEPSEEK_API_KEY (config.go:115-117, 179-193). model and api_key are
// literals; max_tool_rounds is high enough that a scripted multi-round
// conversation is never cut short by the cap.
func writeConfig(t *testing.T, dir, baseURL string) string {
	t.Helper()

	path := filepath.Join(dir, "lw", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("e2e: create %s: %v", filepath.Dir(path), err)
	}

	config := "[llm]\n" +
		"base_url = " + tomlQuote(baseURL) + "\n" +
		"model = \"e2e-fake\"\n" +
		"api_key = \"" + fakeAPIKey + "\"\n" +
		"\n[llm.limits]\n" +
		"max_tool_rounds = 24\n" +
		"context_tokens = 96000\n"
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatalf("e2e: write %s: %v", path, err)
	}
	return path
}

// tomlQuote renders s as a single-quoted TOML literal string, which needs no
// escaping at all (an endpoint URL never contains a single quote).
func tomlQuote(s string) string {
	return "'" + s + "'"
}

// newVault creates a small deterministic lw vault — root directory plus
// schema, root files and six well-formed notes — and returns the root, ready
// to be passed to a verb's --vault flag.
func newVault(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "vault")
	testutil.NewScaleVault(t, dir, 6)
	return dir
}

// lwResult is what one lw subprocess run produced.
type lwResult struct {
	Code   int    // process exit code (run()'s convention: 0 ok, 1 verb error, 2 usage)
	Stdout string // everything lw wrote to stdout
	Stderr string // everything lw wrote to stderr
	Output string // stdout then stderr, for assertions that do not care which
}

// runLW execs the built lw binary with args under e's minimal environment
// and returns its exit code and output. It fails the test if the process
// could not be started at all; a non-zero exit is a result, not an error,
// because the fault-path scenarios assert on exactly that. A subprocess that
// does not exit within lwDeadline is killed and surfaces as the failed run
// result code -1 with a note on its stderr, so the scenario's own assertion
// reports the hang instead of the whole test binary hanging inside it.
func runLW(t *testing.T, e *env, args ...string) lwResult {
	t.Helper()
	return runLWWithDeadline(t, e, lwDeadline, args...)
}

// runLWWithDeadline is runLW with the kill ceiling spelled out: lwDeadline
// for every scenario that does not override it, budget+stressSlack for the
// stress steps whose budgets reach past the smoke ceiling. The timeout note
// names the deadline that actually fired, so a killed stress step is
// distinguishable from a killed smoke one in the log.
func runLWWithDeadline(t *testing.T, e *env, deadline time.Duration, args ...string) lwResult {
	t.Helper()

	if lwBin == "" {
		t.Fatal("e2e: lw binary was not built")
	}

	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	cmd := exec.CommandContext(ctx, lwBin, args...)
	cmd.Dir = e.work
	cmd.Env = e.environ()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if ctx.Err() == nil {
			t.Fatalf("e2e: run lw %s: %v", strings.Join(args, " "), err)
		}
		if ctx.Err() != nil {
			code = -1
			fmt.Fprintf(&stderr, "e2e: lw %s did not exit within %s (%v)\n", strings.Join(args, " "), deadline, ctx.Err())
		}
	}

	return lwResult{
		Code:   code,
		Stdout: stdout.String(),
		Stderr: stderr.String(),
		Output: stdout.String() + stderr.String(),
	}
}

// closedPort returns "host:port" for an address that is provably not
// listening: a listener is bound, its address recorded, then released, so
// the next dial to it is refused. Binding to port 0 is the only way to name
// a free port without racing whatever else is on the machine.
func closedPort(t *testing.T) string {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("e2e: bind a port to close: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("e2e: close the probe listener: %v", err)
	}
	return addr
}

// TestHarness is the harness's own proof: the binary builds and reports its
// version, the sandbox isolates lw from the session's real configuration and
// secrets, and a scripted two-round ingest runs the whole staging pipeline.
func TestHarness(t *testing.T) {
	t.Run("version", harnessVersion)
	t.Run("config_isolated", harnessConfigIsolated)
	t.Run("fakellm_ingest_roundtrip", harnessFakellmIngestRoundtrip)
}

// harnessVersion runs `lw --version` and expects the exact version string on
// stdout (main.go:15, 52-54 — nothing else on the wire).
func harnessVersion(t *testing.T) {
	t.Helper()

	e := newEnv(t)
	res := runLW(t, e, "--version")

	if res.Code != 0 {
		t.Fatalf("lw --version: exit %d, want 0\n%s", res.Code, res.Output)
	}
	if got, want := strings.TrimSpace(res.Stdout), "lw 1.0.0-dev"; got != want {
		t.Errorf("lw --version: stdout = %q, want %q", got, want)
	}
	if res.Stderr != "" {
		t.Errorf("lw --version: stderr = %q, want empty", res.Stderr)
	}
}

// harnessConfigIsolated points a config at a closed port and expects ingest
// to fail with a dial error to that address. The failure shape is the proof
// of isolation: had XDG_CONFIG_HOME not been honoured, config.Load would
// have returned Default() and ResolveAPIKey would have died on the missing
// DEEPSEEK_API_KEY instead — a different error naming a real secret.
func harnessConfigIsolated(t *testing.T) {
	t.Helper()

	e := newEnv(t)
	addr := closedPort(t)
	writeConfig(t, e.config, "http://"+addr+"/v1")

	vault := newVault(t)
	src := e.writeSource(t, "isolation-probe.md", "# Isolation Probe\n\nA source the unreachable endpoint must never see.\n")

	res := runLW(t, e, "ingest", "--vault", vault, "--kind", "article", src)

	// Exit 1 exactly: cmdIngest returns a bare error here, so dispatch — not
	// the verb — chose the code, and it always chooses 1.
	if res.Code != 1 {
		t.Fatalf("lw ingest against a closed port: exit %d, want 1\n%s", res.Code, res.Output)
	}

	// Either the endpoint address (the ordinary refusal) or the client's own
	// status error satisfies this: closedPort releases its port before lw
	// dials it, and that small window occasionally lets another listener
	// answer instead. A status error from whatever took the port is still a
	// failure delivered by the configured endpoint, which is the property
	// under test.
	if !strings.Contains(res.Output, addr) && !strings.Contains(res.Output, "llm: unexpected status") {
		t.Errorf("lw ingest against a closed port: output does not name the endpoint %s\n%s", addr, res.Output)
	}
	if strings.Contains(res.Output, "DEEPSEEK_API_KEY") {
		t.Errorf("lw ingest against a closed port: output mentions the real default key, so the sandbox config was not the one used\n%s", res.Output)
	}

	// The environment is the proof no real key could have been read, no
	// matter what lw did with it.
	for _, kv := range e.environ() {
		if strings.HasPrefix(kv, "DEEPSEEK_API_KEY=") {
			t.Errorf("subprocess environment carries DEEPSEEK_API_KEY (%q)", kv)
		}
	}
	if got, want := len(e.environ()), 4; got != want {
		t.Errorf("subprocess environment has %d variables, want exactly %d (HOME, XDG_CONFIG_HOME, PATH, TMPDIR)", got, want)
	}
}

// openOpsRE matches `lw status`'s open-changeset line — "open changeset <id>:
// <intent> (N op(s), ..." — and captures N, so a scenario can assert an open
// changeset carries at least one op.
var openOpsRE = regexp.MustCompile(`open changeset \S+: .*\((\d+) op\(s\)`)

// openIntentRE captures the intent of that same line — the text between
// "open changeset <id>: " and the op count — for scenarios that must show
// *what* is open, not merely that something is. openOpsRE keeps its op-count
// group position unchanged, since fakellm_test.go indexes it.
var openIntentRE = regexp.MustCompile(`open changeset \S+: (.*?) \(\d+ op\(s\)`)
