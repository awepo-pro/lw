// cmd_config_test.go covers `lw config` (stage S6-T3): resolution display,
// set + round trip, type coercion, secret refusal, the probe seam and the
// leak check. Every case runs the real dispatch path (run) with the config
// directory pointed at a fresh temp directory, so nothing here touches a real
// ~/.config or the network.
package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/llm"
)

// cfgKeyEnv is the variable config.Default() references. The default install
// resolves it from the environment, so every test pins it explicitly — set or
// empty — rather than inheriting whatever the developer's shell exports.
const cfgKeyEnv = "DEEPSEEK_API_KEY"

// cfgLeakedKey is the key-shaped literal the refusal and leak-check tests use.
// It must never appear in any output, in memory or on disk.
const cfgLeakedKey = "sk-LEAKED-VALUE-NOT-A-REAL-KEY"

// configTestEnv points config.ConfigDir at a fresh temp directory and clears
// the default key variable, so a test sees exactly the environment it sets.
func configTestEnv(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "xdg-config")
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv(cfgKeyEnv, "")
	return dir
}

// withFakeProbe swaps the probeProvider seam for fn (cmd_doctor.go's seam,
// reused here so both verbs share one swap point), restoring it on cleanup.
func withFakeProbe(t *testing.T, fn func(context.Context, *config.Config) llm.ProbeResult) {
	t.Helper()
	orig := probeProvider
	probeProvider = fn
	t.Cleanup(func() { probeProvider = orig })
}

// runConfig runs `lw config <args...>` through the real dispatch path and
// returns what it wrote to each stream and the exit code main would exit with.
// No arguments means `lw config` itself.
func runConfig(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	return captureRun(t, func() int { return run(append([]string{"config"}, args...)) })
}

// writeConfigFile creates <dir>/lw/config.toml with the given contents,
// creating the directory config.Save would.
func writeConfigFile(t *testing.T, dir, contents string) string {
	t.Helper()
	path := filepath.Join(dir, "lw", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// wantRow asserts stdout carries the `lw config` row for key and that it
// carries every part — the value and, where it matters, the (file) /
// (default) marker. Keys are printed padded to a shared width, so a row is
// matched by its key prefix, never by hand-counted column positions.
func wantRow(t *testing.T, stdout, key string, parts ...string) {
	t.Helper()
	for _, line := range strings.Split(stdout, "\n") {
		if !strings.HasPrefix(line, key+" ") {
			continue
		}
		for _, p := range parts {
			if !strings.Contains(line, p) {
				t.Errorf("row for %s is %q, want it to contain %q:\n%s", key, line, p, stdout)
			}
		}
		return
	}
	t.Errorf("stdout has no row for %q:\n%s", key, stdout)
}

func TestConfigShowDefaults(t *testing.T) {
	configTestEnv(t)

	stdout, stderr, code := runConfig(t)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "not present") {
		t.Errorf("stdout does not say the config file is absent:\n%s", stdout)
	}
	wantRow(t, stdout, "llm.base_url", "https://api.deepseek.com/v1", "(default)")
	wantRow(t, stdout, "llm.model", "deepseek-v4-flash", "(default)")
	wantRow(t, stdout, "llm.limits.max_tool_rounds", "24", "(default)")
	// The key is unresolved: the indirection and its state are shown, and
	// never a value.
	wantRow(t, stdout, "llm.api_key", "env:DEEPSEEK_API_KEY (missing)", "(default)")
	if !strings.Contains(stdout, "(default)") {
		t.Errorf("stdout lost the (default) markers:\n%s", stdout)
	}
}

func TestConfigShowKeyIndirectionSetAndMissing(t *testing.T) {
	dir := configTestEnv(t)
	secret := "lw-config-test-secret-value"

	t.Run("variable set", func(t *testing.T) {
		t.Setenv(cfgKeyEnv, secret)
		stdout, _, code := runConfig(t)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		wantRow(t, stdout, "llm.api_key", "env:DEEPSEEK_API_KEY (set)")
		if strings.Contains(stdout, secret) {
			t.Fatalf("stdout printed the resolved secret:\n%s", stdout)
		}
	})

	t.Run("variable missing", func(t *testing.T) {
		stdout, _, code := runConfig(t)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		wantRow(t, stdout, "llm.api_key", "env:DEEPSEEK_API_KEY (missing)")
		if strings.Contains(stdout, secret) {
			t.Fatalf("stdout printed the secret after the variable was unset:\n%s", stdout)
		}
	})

	// A reference stored in the file behaves the same way, and the row is
	// attributed to the file it came from.
	t.Run("reference from the file", func(t *testing.T) {
		writeConfigFile(t, dir, `[llm]
api_key = "env:CUSTOM_KEY_VAR"
`)
		t.Setenv("CUSTOM_KEY_VAR", secret)
		stdout, _, code := runConfig(t)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		wantRow(t, stdout, "llm.api_key", "env:CUSTOM_KEY_VAR (set)", "(file)")
		if strings.Contains(stdout, secret) {
			t.Fatalf("stdout printed the resolved secret:\n%s", stdout)
		}
	})
}

// TestConfigShowNeverPrintsLiteralKey is the leak check: a config.toml that
// holds a real-looking key must never reach the output.
func TestConfigShowNeverPrintsLiteralKey(t *testing.T) {
	dir := configTestEnv(t)
	writeConfigFile(t, dir, `[llm]
base_url = "https://api.deepseek.com/v1"
model    = "deepseek-v4-flash"
api_key  = "`+cfgLeakedKey+`"
`)

	stdout, _, code := runConfig(t)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if strings.Contains(stdout, "sk-") {
		t.Fatalf("leak: stdout carries the literal key:\n%s", stdout)
	}
	wantRow(t, stdout, "llm.api_key", "literal (set)")
	if !strings.Contains(stdout, "env:NAME") {
		t.Errorf("stdout does not point at the env: indirection:\n%s", stdout)
	}
}

// TestConfigShowAttribution checks the (file)/(default) markers against a
// complete file — the shape `lw config set` itself materializes.
func TestConfigShowAttribution(t *testing.T) {
	dir := configTestEnv(t)
	writeConfigFile(t, dir, `[llm]
base_url    = "https://my-proxy.internal/v1"
model       = "custom-model"
api_key     = "env:MY_KEY"
temperature = 0.9
max_tokens  = 512

[llm.limits]
max_tool_rounds = 3
context_tokens  = 4096
`)

	stdout, _, code := runConfig(t)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "(loaded)") {
		t.Errorf("stdout does not say the config file was loaded:\n%s", stdout)
	}
	wantRow(t, stdout, "llm.base_url", "https://my-proxy.internal/v1", "(file)")
	wantRow(t, stdout, "llm.model", "custom-model", "(file)")
	wantRow(t, stdout, "llm.limits.max_tool_rounds", "3", "(file)")
	// theme is not in the file and keeps lw's empty default.
	wantRow(t, stdout, "theme", "(not set)", "(default)")
}

// TestConfigShowPartialFile pins what a hand-written file that sets one key
// shows. config.Load does not merge defaults into a partial file: the fields
// it omits come back as zero values, and this display prints what the process
// sees rather than a second, prettier resolution. Reported to the
// orchestrator at S6-T3 — if internal/config.Load learns to merge defaults,
// the remaining rows become "(default)" with default values and no change is
// needed here.
func TestConfigShowPartialFile(t *testing.T) {
	dir := configTestEnv(t)
	writeConfigFile(t, dir, `[llm]
model = "custom-model"
`)

	stdout, _, code := runConfig(t)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "(loaded)") {
		t.Errorf("stdout does not say the config file was loaded:\n%s", stdout)
	}
	wantRow(t, stdout, "llm.model", "custom-model", "(file)")
	wantRow(t, stdout, "llm.base_url")
	wantRow(t, stdout, "llm.limits.max_tool_rounds")
}

// TestConfigDisplayCoversEverySettableKey pins the two lists to each other:
// anything `lw config set` accepts is printed, so a user is never told a key
// exists and then cannot see it.
func TestConfigDisplayCoversEverySettableKey(t *testing.T) {
	configTestEnv(t)

	stdout, _, code := runConfig(t)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	for _, f := range configFields {
		if !strings.Contains(stdout, f.key) {
			t.Errorf("settable key %q is missing from the display:\n%s", f.key, stdout)
		}
	}
}

func TestConfigSetPersistsAndRoundTrips(t *testing.T) {
	configTestEnv(t)

	stdout, stderr, code := runConfig(t, "set", "llm.model", "deepseek-v4-pro")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "llm.model set to deepseek-v4-pro") {
		t.Errorf("stdout does not confirm the set:\n%s", stdout)
	}
	if !strings.Contains(stdout, "saved "+configFilePath()) {
		t.Errorf("stdout does not name the file it saved:\n%s", stdout)
	}

	got, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.LLM.Model != "deepseek-v4-pro" {
		t.Fatalf("Model = %q, want deepseek-v4-pro", got.LLM.Model)
	}
	// The rest of the config survives the save untouched.
	def := config.Default()
	if got.LLM.BaseURL != def.LLM.BaseURL || got.Limits != def.Limits {
		t.Fatalf("save lost unrelated values:\n got  %+v\n want %+v", got, def)
	}
	if got.LLM.APIKey != def.LLM.APIKey {
		t.Fatalf("APIKey = %q, want the %q reference", got.LLM.APIKey, def.LLM.APIKey)
	}

	// And the file on disk is the file the next process reads.
	stdout, _, code = runConfig(t)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	wantRow(t, stdout, "llm.model", "deepseek-v4-pro", "(file)")
}

func TestConfigSetIntsStayInts(t *testing.T) {
	dir := configTestEnv(t)

	for _, tc := range []struct {
		key, value string
	}{
		{"llm.max_tokens", "1024"},
		{"llm.limits.max_tool_rounds", "8"},
		{"llm.limits.context_tokens", "32000"},
	} {
		if _, stderr, code := runConfig(t, "set", tc.key, tc.value); code != 0 {
			t.Fatalf("set %s %s: exit code = %d, want 0; stderr=%q", tc.key, tc.value, code, stderr)
		}
	}

	got, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.LLM.MaxTokens != 1024 {
		t.Errorf("MaxTokens = %d (%T), want int 1024", got.LLM.MaxTokens, got.LLM.MaxTokens)
	}
	if got.Limits.MaxToolRounds != 8 {
		t.Errorf("Limits.MaxToolRounds = %d, want 8", got.Limits.MaxToolRounds)
	}
	if got.Limits.ContextTokens != 32000 {
		t.Errorf("Limits.ContextTokens = %d, want 32000", got.Limits.ContextTokens)
	}

	// The file carries them as bare integers, not quoted strings.
	b, err := os.ReadFile(filepath.Join(dir, "lw", "config.toml"))
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}
	for _, want := range []string{"max_tokens = 1024", "max_tool_rounds = 8", "context_tokens = 32000"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("config file does not hold %q:\n%s", want, b)
		}
	}
	for _, quoted := range []string{`"1024"`, `"8"`, `"32000"`} {
		if strings.Contains(string(b), quoted) {
			t.Errorf("config file stores %s as a string:\n%s", quoted, b)
		}
	}
}

func TestConfigSetFloat(t *testing.T) {
	configTestEnv(t)

	if _, stderr, code := runConfig(t, "set", "llm.temperature", "0.7"); code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	got, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.LLM.Temperature != 0.7 {
		t.Fatalf("Temperature = %v, want 0.7", got.LLM.Temperature)
	}
}

func TestConfigSetUnknownKey(t *testing.T) {
	dir := configTestEnv(t)

	stdout, stderr, code := runConfig(t, "set", "llm.bogus", "x")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%q stdout=%q", code, stderr, stdout)
	}
	if !strings.Contains(stderr, `unknown key "llm.bogus"`) || !strings.Contains(stderr, "llm.model") {
		t.Errorf("stderr = %q, want the unknown key and the known-key list", stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "lw", "config.toml")); !os.IsNotExist(err) {
		t.Errorf("config file written for an unknown key: %v", err)
	}
}

func TestConfigSetCoercionErrors(t *testing.T) {
	configTestEnv(t)

	for _, tc := range []struct{ key, value string }{
		{"llm.max_tokens", "many"},
		{"llm.limits.max_tool_rounds", "2.5"},
		{"llm.temperature", "warm"},
	} {
		_, stderr, code := runConfig(t, "set", tc.key, tc.value)
		if code != 2 {
			t.Errorf("set %s %s: exit code = %d, want 2", tc.key, tc.value, code)
		}
		if !strings.Contains(stderr, tc.key) {
			t.Errorf("set %s %s: stderr %q does not name the key", tc.key, tc.value, stderr)
		}
	}

	// Nothing was written by any of the failed attempts.
	if got, err := config.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	} else if *got != *config.Default() {
		t.Fatalf("a refused set changed the config:\n got  %+v\n want %+v", *got, *config.Default())
	}
}

// TestConfigSetRefusesSecretLiteral is the refusal: a key-shaped literal is
// never stored, and the refusal points at the env: indirection.
func TestConfigSetRefusesSecretLiteral(t *testing.T) {
	dir := configTestEnv(t)

	stdout, stderr, code := runConfig(t, "set", "llm.api_key", cfgLeakedKey)
	if code == 0 {
		t.Fatalf("exit code = 0, want nonzero; stdout=%q", stdout)
	}
	for _, want := range []string{"refusing", "env:NAME"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr %q does not contain %q", stderr, want)
		}
	}

	b, err := os.ReadFile(filepath.Join(dir, "lw", "config.toml"))
	if err == nil && strings.Contains(string(b), "sk-") {
		t.Fatalf("leak: the key literal was written to the config file:\n%s", b)
	}
}

// TestConfigSetRefusesLongBareToken covers the second half of the heuristic:
// a generated key need not start with a known prefix to be refused.
func TestConfigSetRefusesLongBareToken(t *testing.T) {
	configTestEnv(t)

	_, _, code := runConfig(t, "set", "llm.api_key", "Q8v2mZx1pLr4Tn6Kd0Ws3Yb7Hj5")
	if code == 0 {
		t.Fatal("exit code = 0, want nonzero for a 24-character unbroken token")
	}
}

func TestConfigSetAcceptsEnvReference(t *testing.T) {
	dir := configTestEnv(t)

	t.Run("variable already exported", func(t *testing.T) {
		t.Setenv("LW_CFG_TEST_KEY", "whatever-value")
		stdout, stderr, code := runConfig(t, "set", "llm.api_key", "env:LW_CFG_TEST_KEY")
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		if strings.Contains(stderr, "warning") {
			t.Errorf("stderr warns about an exported variable: %q", stderr)
		}
		if !strings.Contains(stdout, "llm.api_key set to env:LW_CFG_TEST_KEY") {
			t.Errorf("stdout does not confirm the reference:\n%s", stdout)
		}
	})

	t.Run("variable not exported yet", func(t *testing.T) {
		_, stderr, code := runConfig(t, "set", "llm.api_key", "env:LW_CFG_LATER_KEY")
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; the reference is still stored", code)
		}
		if !strings.Contains(stderr, "LW_CFG_LATER_KEY is not set") {
			t.Errorf("stderr = %q, want a warning naming the missing variable", stderr)
		}
	})

	// The reference is what lands on disk, never a value.
	b, err := os.ReadFile(filepath.Join(dir, "lw", "config.toml"))
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}
	if !strings.Contains(string(b), "env:LW_CFG_LATER_KEY") {
		t.Fatalf("config file does not hold the env: reference:\n%s", b)
	}
}

func TestConfigSetAcceptsKeyringReference(t *testing.T) {
	configTestEnv(t)

	_, stderr, code := runConfig(t, "set", "llm.api_key", "keyring:service-name")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; the keyring: prefix is accepted", code)
	}
	if !strings.Contains(stderr, "not implemented in v0.1") {
		t.Errorf("stderr = %q, want the v0.1 note", stderr)
	}

	got, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.LLM.APIKey != "keyring:service-name" {
		t.Fatalf("APIKey = %q, want keyring:service-name", got.LLM.APIKey)
	}
}

// TestConfigSetPreservesOtherFileValues checks the load-mutate-save path
// against a file a human wrote, not one lw materialized: the values the file
// already carried must still be there afterwards.
func TestConfigSetPreservesOtherFileValues(t *testing.T) {
	dir := configTestEnv(t)
	writeConfigFile(t, dir, `[llm]
base_url    = "https://my-proxy.internal/v1"
model       = "my-model"
api_key     = "env:MY_KEY"
max_tokens  = 512

[llm.limits]
max_tool_rounds = 3
`)

	if _, stderr, code := runConfig(t, "set", "llm.limits.context_tokens", "4096"); code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}

	got, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.LLM.BaseURL != "https://my-proxy.internal/v1" {
		t.Errorf("BaseURL = %q, want the file's value kept", got.LLM.BaseURL)
	}
	if got.LLM.MaxTokens != 512 {
		t.Errorf("MaxTokens = %d, want 512", got.LLM.MaxTokens)
	}
	if got.Limits.MaxToolRounds != 3 {
		t.Errorf("Limits.MaxToolRounds = %d, want 3", got.Limits.MaxToolRounds)
	}
	if got.Limits.ContextTokens != 4096 {
		t.Errorf("Limits.ContextTokens = %d, want 4096", got.Limits.ContextTokens)
	}
	if got.LLM.APIKey != "env:MY_KEY" {
		t.Errorf("APIKey = %q, want env:MY_KEY kept", got.LLM.APIKey)
	}
	if got.Theme != "" {
		t.Errorf("Theme = %q, want it left empty", got.Theme)
	}
}

func TestConfigPath(t *testing.T) {
	dir := configTestEnv(t)

	stdout, stderr, code := runConfig(t, "path")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if want := filepath.Join(dir, "lw", "config.toml"); strings.TrimSpace(stdout) != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
}

func TestConfigProbe(t *testing.T) {
	configTestEnv(t)
	t.Setenv(cfgKeyEnv, "lw-config-test-secret-value")

	t.Run("ok", func(t *testing.T) {
		withFakeProbe(t, func(ctx context.Context, cfg *config.Config) llm.ProbeResult {
			return llm.ProbeResult{Reachable: true, Model: cfg.LLM.Model, ToolCalling: true, Latency: 1200}
		})
		stdout, _, code := runConfig(t, "--probe")
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stdout=%q", code, stdout)
		}
		if !strings.Contains(stdout, "probe ok") || !strings.Contains(stdout, "tool calling ok") {
			t.Errorf("stdout = %q, want a passing probe", stdout)
		}
		if strings.Contains(stdout, "lw-config-test-secret-value") {
			t.Fatalf("leak: probe output printed the resolved key:\n%s", stdout)
		}
	})

	t.Run("unreachable", func(t *testing.T) {
		withFakeProbe(t, func(ctx context.Context, cfg *config.Config) llm.ProbeResult {
			return llm.ProbeResult{Reachable: false, Model: cfg.LLM.Model, Err: context.DeadlineExceeded}
		})
		stdout, _, code := runConfig(t, "--probe")
		if code != 1 {
			t.Fatalf("exit code = %d, want 1; stdout=%q", code, stdout)
		}
		if !strings.Contains(stdout, "probe failed") || !strings.Contains(stdout, "fix:") {
			t.Errorf("stdout = %q, want the failure and its fix", stdout)
		}
	})

	t.Run("reachable but no tool calling", func(t *testing.T) {
		withFakeProbe(t, func(ctx context.Context, cfg *config.Config) llm.ProbeResult {
			return llm.ProbeResult{Reachable: true, Model: cfg.LLM.Model}
		})
		stdout, _, code := runConfig(t, "--probe")
		if code != 1 {
			t.Fatalf("exit code = %d, want 1; stdout=%q", code, stdout)
		}
		if !strings.Contains(stdout, "no tool call") {
			t.Errorf("stdout = %q, want the tool-call failure", stdout)
		}
	})

	t.Run("key does not resolve", func(t *testing.T) {
		t.Setenv(cfgKeyEnv, "")
		stdout, _, code := runConfig(t, "--probe")
		if code != 1 {
			t.Fatalf("exit code = %d, want 1; stdout=%q", code, stdout)
		}
		if !strings.Contains(stdout, "DEEPSEEK_API_KEY is not set") {
			t.Errorf("stdout = %q, want the unresolved-variable failure", stdout)
		}
	})
}

func TestConfigUsageErrors(t *testing.T) {
	configTestEnv(t)

	t.Run("unknown subcommand", func(t *testing.T) {
		_, stderr, code := runConfig(t, "frobnicate")
		if code != 2 {
			t.Fatalf("exit code = %d, want 2", code)
		}
		if !strings.Contains(stderr, `unknown subcommand "frobnicate"`) || !strings.Contains(stderr, "usage: lw config") {
			t.Errorf("stderr = %q, want the reason and the usage", stderr)
		}
	})

	t.Run("set with a missing value", func(t *testing.T) {
		_, stderr, code := runConfig(t, "set", "llm.model")
		if code != 2 {
			t.Fatalf("exit code = %d, want 2", code)
		}
		if !strings.Contains(stderr, "usage: lw config") {
			t.Errorf("stderr = %q, want the usage", stderr)
		}
	})

	t.Run("set with no arguments", func(t *testing.T) {
		_, _, code := runConfig(t, "set")
		if code != 2 {
			t.Fatalf("exit code = %d, want 2", code)
		}
	})

	t.Run("path with an extra argument", func(t *testing.T) {
		_, _, code := runConfig(t, "path", "extra")
		if code != 2 {
			t.Fatalf("exit code = %d, want 2", code)
		}
	})

	t.Run("probe and path together", func(t *testing.T) {
		withFakeProbe(t, func(ctx context.Context, cfg *config.Config) llm.ProbeResult {
			t.Error("probe ran for a usage error")
			return llm.ProbeResult{Reachable: true, ToolCalling: true}
		})
		_, _, code := runConfig(t, "--probe", "path")
		if code != 2 {
			t.Fatalf("exit code = %d, want 2", code)
		}
	})
}

// TestConfigSetMaterializesDefaultsOnPartialFile pins the write path against
// the damage a partial file would otherwise take. config.Load returns the
// file's values and nothing else, so saving that verbatim would write an
// empty api_key and zero limits over a file that simply did not mention them.
// `lw config set` writes the defaults those silences stand for instead.
func TestConfigSetMaterializesDefaultsOnPartialFile(t *testing.T) {
	dir := configTestEnv(t)
	path := writeConfigFile(t, dir, `[llm]
model = "custom-model"
`)

	if _, stderr, code := runConfig(t, "set", "llm.base_url", "https://proxy.internal/v1"); code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}

	got, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	def := config.Default()
	if got.LLM.APIKey != def.LLM.APIKey {
		t.Errorf("APIKey = %q, want the %q reference kept, not wiped", got.LLM.APIKey, def.LLM.APIKey)
	}
	if got.Limits.MaxToolRounds != def.Limits.MaxToolRounds || got.Limits.ContextTokens != def.Limits.ContextTokens {
		t.Errorf("Limits = %+v, want the defaults %+v, not zeros", got.Limits, def.Limits)
	}
	if got.LLM.BaseURL != "https://proxy.internal/v1" || got.LLM.Model != "custom-model" {
		t.Errorf("file values lost: base_url %q model %q", got.LLM.BaseURL, got.LLM.Model)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}
	for _, gone := range []string{`api_key = ""`, "temperature = 0.0", "max_tokens = 0", "context_tokens = 0"} {
		if strings.Contains(string(b), gone) {
			t.Errorf("config file now holds %q, a value the user never set:\n%s", gone, b)
		}
	}
}

// TestConfigSetOnMalformedFileFails: a file lw cannot parse is reported as a
// failure rather than silently replaced with the defaults.
func TestConfigSetOnMalformedFileFails(t *testing.T) {
	dir := configTestEnv(t)
	writeConfigFile(t, dir, "not = [valid toml")

	if _, _, code := runConfig(t, "set", "llm.model", "x"); code == 0 {
		t.Fatal("exit code = 0, want nonzero for an unparsable config file")
	}
	if _, _, code := runConfig(t); code == 0 {
		t.Fatal("show: exit code = 0, want nonzero for an unparsable config file")
	}
}
