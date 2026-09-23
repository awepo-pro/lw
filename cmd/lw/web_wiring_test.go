package main

// web_wiring_test.go pins cmd/lw's half of the [web] table (010 contract
// §4, C-1001): the deps builders offer web.search only when web.api_key
// resolves — a *web.Tavily over the house HTTP client — and offer nothing
// otherwise, and doctor reports the configured lookup on its own `web:`
// line without ever printing a value. The regression test ships with the
// subtask and stays forever (D-10C). The agentExtractors call sites were
// updated mechanically for 007 F.W1's signature — it now takes the vault
// root and config so it can wire the extraction cache and per-source cap —
// with no change to what this file pins.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/mcp"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/web"
)

// webConfigEnv points config.ConfigDir at a fresh scratch XDG_CONFIG_HOME
// and, when toml is non-empty, writes it as <xdg>/lw/config.toml — the
// only config these tests may read (never the user's; it holds a secret).
func webConfigEnv(t *testing.T, toml string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if toml == "" {
		return
	}
	if err := os.MkdirAll(filepath.Join(dir, "lw"), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lw", "config.toml"), []byte(toml), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// loadedConfig loads the config webConfigEnv wrote, so a test wires the
// deps exactly the verbs do: config.Load over the scratch XDG dir.
func loadedConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

// testWebKeyEnv is the variable the tests reference; blocks run with it
// unset, and the tests force it either way so they never read a real one.
const testWebKeyEnv = "LW_TEST_KEY"

func TestDepsWired(t *testing.T) {
	t.Run("key_builds_tavily_search", func(t *testing.T) {
		t.Setenv(testWebKeyEnv, "lw-test-tavily-key-not-a-real-secret")
		webConfigEnv(t, `[web]
api_key = "env:LW_TEST_KEY"
`)
		e := openEngine(t, testutil.CopyFixture(t, "minimal"))

		cfg := loadedConfig(t)
		deps := agentToolDeps(e, cfg, agentExtractors(e.Vault().Root(), cfg))

		if deps.Search == nil {
			t.Fatal("Deps.Search is nil with a resolvable web.api_key; web.search would not be offered")
		}
		tv, ok := deps.Search.(*web.Tavily)
		if !ok {
			t.Fatalf("Deps.Search is %T, want *web.Tavily", deps.Search)
		}
		if tv.APIKey != "lw-test-tavily-key-not-a-real-secret" {
			t.Errorf("Tavily.APIKey = %q, want the resolved environment value", tv.APIKey)
		}
		if tv.HTTP == nil {
			t.Error("Tavily.HTTP is nil; want the house client from extract.NewHTTPClient")
		}
	})

	t.Run("no_key_means_nil_search", func(t *testing.T) {
		// No [web] section at all: the merged default keeps api_key empty,
		// so web.search is simply not offered — the registry never gains
		// the verb, and nothing fails.
		webConfigEnv(t, "")
		e := openEngine(t, testutil.CopyFixture(t, "minimal"))

		cfg := loadedConfig(t)
		deps := agentToolDeps(e, cfg, agentExtractors(e.Vault().Root(), cfg))

		if deps.Search != nil {
			t.Fatalf("Deps.Search = %T with no web.api_key configured, want nil", deps.Search)
		}
	})
}

func TestDoctorWeb(t *testing.T) {
	// webLine renders a check the way the contract words it — `web: <detail>`
	// — so the assertions below read as the exact doctor line.
	webLine := func(c doctorCheck) string { return c.Name + ": " + c.Detail }

	t.Run("missing_env_key_is_reported", func(t *testing.T) {
		t.Setenv(testWebKeyEnv, "") // forced missing, whatever the host carries
		webConfigEnv(t, `[web]
api_key = "env:LW_TEST_KEY"
`)
		root := testutil.CopyFixture(t, "minimal")

		c := checkByName(t, runDoctor(context.Background(), root, doctorOptions{}), "web")

		const want = "web: provider tavily, env:LW_TEST_KEY (missing)"
		if got := webLine(c); got != want {
			t.Errorf("doctor reported %q, want %q", got, want)
		}
		// A warn, not a failure: the vault is healthy and the verb is
		// simply absent, the way the llm budget check warns under an OK.
		if !c.OK || !c.Warn || c.Remedy == "" {
			t.Errorf("missing web key: want a warn with a remedy, got %+v", c)
		}
	})

	t.Run("no_web_section_is_silent", func(t *testing.T) {
		webConfigEnv(t, "") // no config file: the defaults, no [web] table
		root := testutil.CopyFixture(t, "minimal")

		rep := runDoctor(context.Background(), root, doctorOptions{})

		for _, c := range rep.Checks {
			if c.Name == "web" {
				t.Fatalf("doctor reported a web check with no [web] section: %+v", c)
			}
		}
	})

	t.Run("unknown_provider_flagged_by_doctor", func(t *testing.T) {
		t.Setenv(testWebKeyEnv, "")
		webConfigEnv(t, `[web]
provider = "nope"
api_key = "env:LW_TEST_KEY"
`)
		root := testutil.CopyFixture(t, "minimal")

		c := checkByName(t, runDoctor(context.Background(), root, doctorOptions{}), "web")

		const want = `web: unknown provider "nope"`
		if got := webLine(c); got != want {
			t.Errorf("doctor reported %q, want %q", got, want)
		}
		if !c.Warn {
			t.Errorf("unknown provider reported without a warn: %+v", c)
		}
		if strings.Contains(c.Detail, "LW_TEST_KEY") {
			t.Errorf("unknown-provider line leaks the key reference: %+v", c)
		}
	})
}

// TestMCPDepsServesWebSearch pins the MCP half of the parity invariant
// (A-10-5, backbone §6's two consumers): the registry mcpDeps hands
// mcp.Serve — and Serve advertises over the wire, via r.List() in
// internal/mcp's NewServer — carries web.search, the 19th tool, exactly when
// the CLI's agent registry would, so a configured user's MCP client sees the
// same surface as the in-process agent verbs, and an unconfigured one simply
// never sees the verb.
func TestMCPDepsServesWebSearch(t *testing.T) {
	t.Run("configured_registry_carries_the_19th_tool", func(t *testing.T) {
		t.Setenv(testWebKeyEnv, "lw-test-tavily-key-not-a-real-secret")
		webConfigEnv(t, `[web]
api_key = "env:LW_TEST_KEY"
`)
		e := openEngine(t, testutil.CopyFixture(t, "minimal"))

		reg := tools.NewRegistry(mcpDeps(e, loadedConfig(t)))

		list := reg.List()
		if len(list) != 19 {
			t.Errorf("MCP registry lists %d tools, want 19 (18 vault tools + web.search)", len(list))
		}
		found := false
		for _, tool := range list {
			if tool.Name == "web.search" {
				found = true
			}
		}
		if !found {
			t.Error("MCP registry does not list web.search with a resolvable web.api_key")
		}
		if got := mcp.MCPName("web.search"); got != "web_search" {
			t.Errorf("MCP wire name for web.search = %q, want web_search", got)
		}
	})

	t.Run("unconfigured_registry_stays_at_the_18_vault_tools", func(t *testing.T) {
		webConfigEnv(t, "") // no [web] section: web.search is not offered
		e := openEngine(t, testutil.CopyFixture(t, "minimal"))

		reg := tools.NewRegistry(mcpDeps(e, loadedConfig(t)))

		if got := len(reg.List()); got != 18 {
			t.Errorf("MCP registry lists %d tools with no provider wired, want the 18 vault tools", got)
		}
		for _, tool := range reg.List() {
			if tool.Name == "web.search" {
				t.Error("MCP registry lists web.search with no provider wired; the verb must be unoffered, not failing")
			}
		}
	})
}

// TestSearchProviderGuard pins the wiring half of A-10-5 item 3: an unknown
// provider wires no verb even when a key resolves, so the wiring agrees with
// doctor's unknown-provider warn — the verb is absent and `lw doctor`
// explains why.
func TestSearchProviderGuard(t *testing.T) {
	cfg := config.Default()
	cfg.Web.Provider = "nope"
	cfg.Web.APIKey = "env:" + testWebKeyEnv
	t.Setenv(testWebKeyEnv, "lw-test-tavily-key-not-a-real-secret") // resolvable, and still no verb

	if got := webSearchProvider(cfg); got != nil {
		t.Errorf("webSearchProvider with provider %q = %T, want nil", cfg.Web.Provider, got)
	}
	e := openEngine(t, testutil.CopyFixture(t, "minimal"))
	if deps := agentToolDeps(e, cfg, agentExtractors(e.Vault().Root(), cfg)); deps.Search != nil {
		t.Errorf("agentToolDeps Search with provider %q = %T, want nil", cfg.Web.Provider, deps.Search)
	}

	// And doctor names the reason, so the user can find the fix.
	c := checkWeb(cfg, nil)
	if c == nil {
		t.Fatal("checkWeb reported nothing for an unknown provider with a key set")
	}
	if !c.Warn {
		t.Errorf("unknown-provider check is not a warn: %+v", c)
	}
	if !strings.Contains(c.Remedy, "lw config set web.provider tavily") {
		t.Errorf("remedy %q does not name the fix", c.Remedy)
	}
}

// TestCheckWebEmptyEnvName pins the remedy guard for a malformed reference:
// `env:` with no variable name must not render `export ,` — the reference is
// named literally instead (A-10-5 item on the empty-name remedy).
func TestCheckWebEmptyEnvName(t *testing.T) {
	cfg := config.Default()
	cfg.Web.APIKey = "env:"

	c := checkWeb(cfg, nil)
	if c == nil {
		t.Fatal("checkWeb reported nothing for an env: reference with no name")
	}
	if !c.Warn {
		t.Errorf("empty-name reference is not a warn: %+v", c)
	}
	if strings.Contains(c.Remedy, "export ,") {
		t.Errorf("remedy %q renders the empty variable name", c.Remedy)
	}
	if !strings.Contains(c.Remedy, `"env:"`) || !strings.Contains(c.Remedy, "lw config set web.api_key env:NAME") {
		t.Errorf("remedy %q does not name the broken reference and the fix", c.Remedy)
	}
	if !strings.Contains(c.Detail, "env: (missing)") {
		t.Errorf("detail %q does not report the reference as missing", c.Detail)
	}
}
