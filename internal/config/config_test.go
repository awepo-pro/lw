package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withConfigDir points ConfigDir() at a fresh temp directory for the
// duration of the test, so Load/Save never touch a real home directory.
func withConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return dir
}

func TestConfigRoundTrip(t *testing.T) {
	withConfigDir(t)

	want := &Config{
		LLM: LLM{
			BaseURL:     "https://api.deepseek.com/v1",
			Model:       "deepseek-v4-flash",
			APIKey:      "env:DEEPSEEK_API_KEY",
			Temperature: 0.2,
			MaxTokens:   8192,
		},
		Limits: Limits{MaxToolRounds: 24, ContextTokens: 96000},
		Theme:  "dark",
	}

	if err := want.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if *got != *want {
		t.Fatalf("round trip mismatch:\n got  %+v\n want %+v", *got, *want)
	}
}

func TestLoadMissingFileReturnsDefault(t *testing.T) {
	withConfigDir(t)

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Default()
	if *got != *want {
		t.Fatalf("Load() with no config file = %+v, want Default() = %+v", *got, *want)
	}
}

func TestLoadMalformedFileErrors(t *testing.T) {
	dir := withConfigDir(t)
	if err := os.MkdirAll(filepath.Join(dir, "lw"), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lw", "config.toml"), []byte("not = [valid toml"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := Load(); err == nil {
		t.Fatal("Load: want error for malformed TOML, got nil")
	}
}

func TestSaveNeverWritesLiteralSecret(t *testing.T) {
	dir := withConfigDir(t)
	t.Setenv("DEEPSEEK_API_KEY", "sk-super-secret-value")

	c := Default()
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	path := filepath.Join(dir, "lw", "config.toml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}
	if strings.Contains(string(b), "sk-super-secret-value") {
		t.Fatalf("config file contains the literal secret:\n%s", b)
	}
	if !strings.Contains(string(b), "env:DEEPSEEK_API_KEY") {
		t.Fatalf("config file lost the env: reference:\n%s", b)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.LLM.APIKey != "env:DEEPSEEK_API_KEY" {
		t.Fatalf("APIKey = %q, want env:DEEPSEEK_API_KEY", got.LLM.APIKey)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config file mode = %o, want 0600", perm)
	}
}

func TestSaveCreatesConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "does-not-exist-yet"))

	if err := Default().Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(ConfigDir()); err != nil {
		t.Fatalf("ConfigDir() not created: %v", err)
	}
}

func TestResolveAPIKeyEnv(t *testing.T) {
	t.Run("env set", func(t *testing.T) {
		t.Setenv("LW_TEST_API_KEY", "sk-abc123")
		c := &Config{LLM: LLM{APIKey: "env:LW_TEST_API_KEY"}}

		got, err := c.ResolveAPIKey()
		if err != nil {
			t.Fatalf("ResolveAPIKey: %v", err)
		}
		if got != "sk-abc123" {
			t.Fatalf("ResolveAPIKey() = %q, want sk-abc123", got)
		}
	})

	t.Run("env unset", func(t *testing.T) {
		c := &Config{LLM: LLM{APIKey: "env:LW_TEST_DEFINITELY_UNSET_KEY"}}

		if _, err := c.ResolveAPIKey(); err == nil {
			t.Fatal("ResolveAPIKey: want error for unset env var, got nil")
		}
	})

	t.Run("env empty", func(t *testing.T) {
		t.Setenv("LW_TEST_EMPTY_KEY", "")
		c := &Config{LLM: LLM{APIKey: "env:LW_TEST_EMPTY_KEY"}}

		if _, err := c.ResolveAPIKey(); err == nil {
			t.Fatal("ResolveAPIKey: want error for empty env var, got nil")
		}
	})

	t.Run("literal", func(t *testing.T) {
		c := &Config{LLM: LLM{APIKey: "sk-literal-key-value"}}

		got, err := c.ResolveAPIKey()
		if err != nil {
			t.Fatalf("ResolveAPIKey: %v", err)
		}
		if got != "sk-literal-key-value" {
			t.Fatalf("ResolveAPIKey() = %q, want sk-literal-key-value", got)
		}
	})

	t.Run("keyring", func(t *testing.T) {
		c := &Config{LLM: LLM{APIKey: "keyring:my-secret"}}

		_, err := c.ResolveAPIKey()
		if err == nil {
			t.Fatal("ResolveAPIKey: want error for keyring: reference, got nil")
		}
		if !strings.Contains(err.Error(), "v0.1") {
			t.Fatalf("ResolveAPIKey error = %q, want it to explain keyring is unsupported in v0.1", err)
		}
	})
}

// TestLoadPlanDocumentedFormat decodes the exact config.toml block /PLAN.md
// §11.2 publishes — [llm] followed by a nested [llm.limits] table, as a
// human would hand-write it — and asserts Limits actually lands on Config.
// This is the shape backbone §11 C-96 says must decode; the TOML below is a
// literal, not built from a Config, so the test proves something about the
// documented format rather than about the encoder's own round trip.
func TestLoadPlanDocumentedFormat(t *testing.T) {
	dir := withConfigDir(t)
	if err := os.MkdirAll(filepath.Join(dir, "lw"), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	doc := `[llm]
base_url    = "https://api.deepseek.com/v1"
model       = "deepseek-v4-flash"
api_key     = "env:DEEPSEEK_API_KEY"
temperature = 0.2
max_tokens  = 8192

[llm.limits]
max_tool_rounds = 24
context_tokens  = 96000
`
	if err := os.WriteFile(filepath.Join(dir, "lw", "config.toml"), []byte(doc), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.LLM.BaseURL != "https://api.deepseek.com/v1" {
		t.Errorf("BaseURL = %q, want https://api.deepseek.com/v1", got.LLM.BaseURL)
	}
	if got.LLM.Model != "deepseek-v4-flash" {
		t.Errorf("Model = %q, want deepseek-v4-flash", got.LLM.Model)
	}
	if got.LLM.APIKey != "env:DEEPSEEK_API_KEY" {
		t.Errorf("APIKey = %q, want env:DEEPSEEK_API_KEY", got.LLM.APIKey)
	}
	if got.LLM.Temperature != 0.2 {
		t.Errorf("Temperature = %v, want 0.2", got.LLM.Temperature)
	}
	if got.LLM.MaxTokens != 8192 {
		t.Errorf("MaxTokens = %d, want 8192", got.LLM.MaxTokens)
	}
	if got.Limits.MaxToolRounds != 24 {
		t.Errorf("Limits.MaxToolRounds = %d, want 24", got.Limits.MaxToolRounds)
	}
	if got.Limits.ContextTokens != 96000 {
		t.Errorf("Limits.ContextTokens = %d, want 96000", got.Limits.ContextTokens)
	}
}

// TestSaveEmitsPlanDocumentedFormat asserts Save writes a bare [llm.limits]
// table header — the form /PLAN.md §11.2 documents and a human would write —
// never the quoted ["llm.limits"] a literal "llm.limits" struct tag produces.
func TestSaveEmitsPlanDocumentedFormat(t *testing.T) {
	dir := withConfigDir(t)

	if err := Default().Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, "lw", "config.toml"))
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}

	found := false
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "[llm.limits]" {
			found = true
		}
	}
	if !found {
		t.Fatalf("config file does not contain a bare [llm.limits] line:\n%s", b)
	}
	if strings.Contains(string(b), `["llm.limits"]`) {
		t.Fatalf("config file contains the quoted [\"llm.limits\"] table header:\n%s", b)
	}
}

// TestSaveLoadRoundTripPreservesLimits pins the Default -> Save -> Load
// round trip specifically for Limits, independent of the rest of Config.
func TestSaveLoadRoundTripPreservesLimits(t *testing.T) {
	withConfigDir(t)

	want := Default()
	if err := want.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Limits != want.Limits {
		t.Fatalf("Limits round trip mismatch:\n got  %+v\n want %+v", got.Limits, want.Limits)
	}
}

func TestDefault(t *testing.T) {
	d := Default()
	if d.LLM.BaseURL != "https://api.deepseek.com/v1" {
		t.Errorf("BaseURL = %q, want https://api.deepseek.com/v1", d.LLM.BaseURL)
	}
	if d.LLM.Model != "deepseek-v4-flash" {
		t.Errorf("Model = %q, want deepseek-v4-flash", d.LLM.Model)
	}
	if d.LLM.APIKey != "env:DEEPSEEK_API_KEY" {
		t.Errorf("APIKey = %q, want env:DEEPSEEK_API_KEY", d.LLM.APIKey)
	}
	if d.Limits.MaxToolRounds != 24 {
		t.Errorf("MaxToolRounds = %d, want 24", d.Limits.MaxToolRounds)
	}
	if d.Limits.ContextTokens != 96000 {
		t.Errorf("ContextTokens = %d, want 96000", d.Limits.ContextTokens)
	}
}

// writeConfig writes a config.toml into the config directory the test has
// already pointed XDG_CONFIG_HOME at, so a test can name the file it wants
// Load to read without repeating the lw/ path join.
func writeConfig(t *testing.T, dir, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "lw"), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lw", "config.toml"), []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// TestLoadMergesDefaultsBehindPartialFile is the fix this subtask exists
// for: a hand-written file naming one key must not zero every other field.
// The measured defect sent MaxToolRounds 0 and MaxTokens 0 into the agent
// loop and made `lw config set` write `api_key = ""` over the credential
// reference.
func TestLoadMergesDefaultsBehindPartialFile(t *testing.T) {
	dir := withConfigDir(t)
	writeConfig(t, dir, `[llm]
model = "custom-model"
`)

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	def := Default()

	if got.LLM.Model != "custom-model" {
		t.Errorf("Model = %q, want the file's custom-model", got.LLM.Model)
	}
	if got.LLM.BaseURL != def.LLM.BaseURL {
		t.Errorf("BaseURL = %q, want the default %q for a key the file omits", got.LLM.BaseURL, def.LLM.BaseURL)
	}
	if got.LLM.APIKey != def.LLM.APIKey {
		t.Errorf("APIKey = %q, want the default %q, not an empty reference", got.LLM.APIKey, def.LLM.APIKey)
	}
	if got.LLM.Temperature != def.LLM.Temperature {
		t.Errorf("Temperature = %v, want the default %v", got.LLM.Temperature, def.LLM.Temperature)
	}
	if got.LLM.MaxTokens != def.LLM.MaxTokens {
		t.Errorf("MaxTokens = %d, want the default %d, not 0", got.LLM.MaxTokens, def.LLM.MaxTokens)
	}
	if got.Limits != def.Limits {
		t.Errorf("Limits = %+v, want the defaults %+v — 0 tool rounds is a runaway agent", got.Limits, def.Limits)
	}
	if got.Theme != def.Theme {
		t.Errorf("Theme = %q, want the default %q", got.Theme, def.Theme)
	}
}

// TestLoadPartialNestedLimits pins the C-96 wire format through the merge:
// [llm] plus a nested [llm.limits] is the shape /PLAN.md §11.2 documents,
// so a file that sets one limit and omits the other must land the one it
// set and keep lw's default for the other.
func TestLoadPartialNestedLimits(t *testing.T) {
	dir := withConfigDir(t)
	writeConfig(t, dir, `[llm]
model = "custom-model"

[llm.limits]
max_tool_rounds = 7
`)

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Limits.MaxToolRounds != 7 {
		t.Errorf("Limits.MaxToolRounds = %d, want the file's 7", got.Limits.MaxToolRounds)
	}
	if got.Limits.ContextTokens != 96000 {
		t.Errorf("Limits.ContextTokens = %d, want the default 96000, not 0", got.Limits.ContextTokens)
	}
	if got.LLM.Model != "custom-model" {
		t.Errorf("Model = %q, want custom-model", got.LLM.Model)
	}
	if got.LLM.APIKey != "env:DEEPSEEK_API_KEY" {
		t.Errorf("APIKey = %q, want the default reference", got.LLM.APIKey)
	}
}

// TestLoadFileWinsOnExplicitZero pins the other half of the merge: a key
// the file SETS is kept even when it is a zero value. BurntSushi's
// MetaData distinguishes "omitted" from "explicitly zero", and merging the
// two into one would make an explicit setting impossible to express.
func TestLoadFileWinsOnExplicitZero(t *testing.T) {
	dir := withConfigDir(t)
	writeConfig(t, dir, `[llm]
model        = "custom-model"
api_key      = ""
temperature  = 0.0
max_tokens   = 0

[llm.limits]
max_tool_rounds = 0
context_tokens  = 0
`)

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.LLM.APIKey != "" {
		t.Errorf("APIKey = %q, want the empty string the file states", got.LLM.APIKey)
	}
	if got.LLM.Temperature != 0 {
		t.Errorf("Temperature = %v, want the explicit 0", got.LLM.Temperature)
	}
	if got.LLM.MaxTokens != 0 {
		t.Errorf("MaxTokens = %d, want the explicit 0", got.LLM.MaxTokens)
	}
	if got.Limits.MaxToolRounds != 0 || got.Limits.ContextTokens != 0 {
		t.Errorf("Limits = %+v, want the explicit zeros the file states", got.Limits)
	}
	if got.LLM.BaseURL != "https://api.deepseek.com/v1" || got.LLM.Model != "custom-model" {
		t.Errorf("BaseURL %q / Model %q, want the default base_url behind the file's model", got.LLM.BaseURL, got.LLM.Model)
	}
}

// TestLoadMergeRoundTripsThroughSave: what Load resolves is what Save
// writes, so a partial file materialized by one `lw config set` reads back
// exactly as it was written — the defaults included.
func TestLoadMergeRoundTripsThroughSave(t *testing.T) {
	dir := withConfigDir(t)
	writeConfig(t, dir, `[llm]
model = "custom-model"
`)

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := got.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	again, err := Load()
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	if *again != *got {
		t.Fatalf("round trip mismatch:\n got  %+v\n want %+v", *again, *got)
	}
}
