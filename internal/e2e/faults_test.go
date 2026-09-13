// faults_test.go walks the failure paths the smoke suite has to survive: an
// unknown verb, a commit with no message, an endpoint that cannot be reached
// and a key that cannot be resolved. The contract each one asserts is the
// same three lines: lw reports a one-line error, it exits with the usage or
// verb code — never a panic — and it leaves no open changeset behind.
package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// unsetKeyEnv is the api_key reference TestFaultBadKeyEnv writes into its
// config: a name no environment in this repo sets, so resolving it must fail
// loudly — and the failure must name it, which is also the proof lw read the
// harness config rather than falling back to the DeepSeek default.
const unsetKeyEnv = "LW_DEFINITELY_UNSET"

// writeConfigWithKey writes the config writeConfig builds, with api_key set
// to keyRef instead of the literal the fake-LLM scenarios use. It lives beside
// the fault scenarios rather than in the task-2 helpers because a key that
// cannot resolve is exactly what only a fault path wants.
func writeConfigWithKey(t *testing.T, dir, baseURL, keyRef string) string {
	t.Helper()

	path := filepath.Join(dir, "lw", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("e2e: create %s: %v", filepath.Dir(path), err)
	}

	config := "[llm]\n" +
		"base_url = " + tomlQuote(baseURL) + "\n" +
		"model = \"e2e-fake\"\n" +
		"api_key = " + tomlQuote(keyRef) + "\n" +
		"\n[llm.limits]\n" +
		"max_tool_rounds = 24\n" +
		"context_tokens = 96000\n"
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatalf("e2e: write %s: %v", path, err)
	}
	return path
}

// assertNoPanic fails t if output carries a Go panic. A fault path that
// panics — recovered or not — is lw surfacing a crash where the contract
// promises one line on stderr, so it is never an acceptable way to fail.
func assertNoPanic(t *testing.T, output string) {
	t.Helper()

	for _, marker := range []string{"panic: ", "runtime error: ", "goroutine 1 [running]"} {
		if strings.Contains(output, marker) {
			t.Errorf("lw panicked (%q in output):\n%s", marker, output)
			return
		}
	}
}

// assertNoOpenChangeset runs `lw status` and expects the no-changeset line: a
// failed ingest must have rejected what it opened (cmd_ingest.go's
// rejectAndReturn, C-113), leaving nothing for a human to trip over later.
func assertNoOpenChangeset(t *testing.T, e *env, vault string) {
	t.Helper()

	res := runLW(t, e, "status", "--vault", vault)
	if res.Code != 0 {
		t.Fatalf("lw status: exit %d, want 0\n%s", res.Code, res.Output)
	}
	if want := "no open changeset"; !strings.Contains(res.Output, want) {
		t.Errorf("lw status: %q not in output, so the failed run left a changeset open\n%s", want, res.Output)
	}
}

// TestFaultUnknownVerb feeds lw a verb that is not in the table and expects
// the usage text and the exit-2 usage code (main.go:66-67).
func TestFaultUnknownVerb(t *testing.T) {
	e := newEnv(t)

	res := runLW(t, e, "frobnicate")
	if res.Code != 2 {
		t.Fatalf("lw frobnicate: exit %d, want 2\n%s", res.Code, res.Output)
	}
	if want := "usage: lw <command>"; !strings.Contains(res.Output, want) {
		t.Errorf("lw frobnicate: output does not carry %q\n%s", want, res.Output)
	}
	assertNoPanic(t, res.Output)
}

// TestFaultCommitNeedsM commits with no message and expects a non-zero exit
// whose usage line names the -m flag the message is required through
// (cmd_commit.go:36-39). The vault path does not exist — deliberately, since
// the message check must run before the vault is resolved: had the ordering
// regressed, the error would be an engine error and the -m line would never
// appear.
func TestFaultCommitNeedsM(t *testing.T) {
	e := newEnv(t)

	res := runLW(t, e, "commit", "--vault", filepath.Join(e.root, "no-such-vault"))
	if res.Code == 0 {
		t.Fatalf("lw commit without -m: exit 0, want non-zero\n%s", res.Output)
	}
	if want := "-m"; !strings.Contains(res.Output, want) {
		t.Errorf("lw commit without -m: output does not mention %q\n%s", want, res.Output)
	}
	assertNoPanic(t, res.Output)
}

// TestFaultUnreachableEndpoint points the harness config at a provably closed
// port and expects ingest to fail without panicking and without leaving an
// open changeset behind. Either the endpoint address or the client's own
// status error counts as naming it: closedPort releases its port before lw
// dials, and that window occasionally lets another listener answer instead —
// a refusal from it is still a refusal from the configured endpoint.
func TestFaultUnreachableEndpoint(t *testing.T) {
	e := newEnv(t)
	addr := closedPort(t)
	writeConfig(t, e.config, "http://"+addr+"/v1")

	vault := newVault(t)
	src := e.writeSource(t, "unreachable.md", "# Unreachable\n\nA source the dead endpoint must never see.\n")

	res := runLW(t, e, "ingest", "--vault", vault, "--kind", "article", src)
	if res.Code == 0 {
		t.Fatalf("lw ingest against a closed port: exit 0, want non-zero\n%s", res.Output)
	}
	if !strings.Contains(res.Output, addr) && !strings.Contains(res.Output, "llm: unexpected status") {
		t.Errorf("lw ingest against a closed port: output does not name the endpoint %s\n%s", addr, res.Output)
	}
	assertNoPanic(t, res.Output)
	assertNoOpenChangeset(t, e, vault)
}

// TestFaultBadKeyEnv writes api_key as an env reference to a name that is not
// set anywhere and expects ingest to refuse before it opens anything. The
// endpoint is a closed port too, so even a regression in the key check could
// not carry the scenario to a real network. No secret material may appear in
// the output: not the default key's name, not the harness key, and nothing
// from the subprocess environment, which the harness keeps at four variables
// that cannot include a key at all.
func TestFaultBadKeyEnv(t *testing.T) {
	e := newEnv(t)
	addr := closedPort(t)
	writeConfigWithKey(t, e.config, "http://"+addr+"/v1", "env:"+unsetKeyEnv)

	vault := newVault(t)
	src := e.writeSource(t, "bad-key.md", "# Bad Key\n\nA source no unresolvable key may turn into a page.\n")

	res := runLW(t, e, "ingest", "--vault", vault, "--kind", "article", src)
	if res.Code == 0 {
		t.Fatalf("lw ingest with an unresolvable key: exit 0, want non-zero\n%s", res.Output)
	}
	if !strings.Contains(res.Output, unsetKeyEnv) {
		t.Errorf("lw ingest with an unresolvable key: output does not name %s, so the harness config may not be the one read\n%s", unsetKeyEnv, res.Output)
	}
	assertNoPanic(t, res.Output)
	assertNoOpenChangeset(t, e, vault)

	// No secret material, by name or by value.
	for _, leaked := range []string{"DEEPSEEK_API_KEY", fakeAPIKey} {
		if strings.Contains(res.Output, leaked) {
			t.Errorf("lw ingest with an unresolvable key: output leaks %q\n%s", leaked, res.Output)
		}
	}
	for _, kv := range e.environ() {
		if strings.HasPrefix(kv, "DEEPSEEK_API_KEY=") {
			t.Errorf("subprocess environment carries DEEPSEEK_API_KEY (%q)", kv)
		}
	}
}
