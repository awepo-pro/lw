package main

// cmd_tui_web_test.go pins 012's T-B wiring (012 contract §4): the
// webConfigured seam is true exactly when webSearchProvider resolves — the
// same signal that puts web.search in the registry — and false for every
// shape of "not configured", including a config that failed to load (nil).
// cmdTUI's ui.Deps literal keys WebSearch off this seam, so the ask pane's
// hint can only fire when the registry really lacks the verb (cs-79f2d7).
// The regression test ships with the subtask and stays forever (D-10C).
// The config helpers come from web_wiring_test.go: webConfigEnv,
// loadedConfig and testWebKeyEnv — the same scratch-XDG rule (never the
// user's config; it holds a secret).

import "testing"

func TestWebConfiguredSeam(t *testing.T) {
	t.Run("resolvable_key_configures_web", func(t *testing.T) {
		t.Setenv(testWebKeyEnv, "lw-test-tavily-key-not-a-real-secret")
		webConfigEnv(t, `[web]
api_key = "env:LW_TEST_KEY"
`)

		if !webConfigured(loadedConfig(t)) {
			t.Fatal("webConfigured = false with a resolvable web.api_key; the ask pane would hint while web.search is offered")
		}
	})

	t.Run("literal_key_configures_web", func(t *testing.T) {
		// A stored literal is a non-empty api_key too: the seam tracks the
		// resolved provider, not the env: reference form.
		webConfigEnv(t, `[web]
api_key = "lw-test-tavily-key-not-a-real-secret"
`)

		if !webConfigured(loadedConfig(t)) {
			t.Fatal("webConfigured = false with a literal web.api_key; the ask pane would hint while web.search is offered")
		}
	})

	t.Run("no_web_table_is_unconfigured", func(t *testing.T) {
		// cs-79f2d7 itself: the merged defaults carry no key, so web.search
		// is not offered and the hint is the truth.
		webConfigEnv(t, "")

		if webConfigured(loadedConfig(t)) {
			t.Fatal("webConfigured = true with no [web] table; the ask pane would stay silent while the registry lacks web.search")
		}
	})

	t.Run("empty_web_table_is_unconfigured", func(t *testing.T) {
		// An empty [web] table: the provider key keeps its default and the
		// api_key stays "" — no credentials, no verb.
		webConfigEnv(t, "[web]\n")

		if webConfigured(loadedConfig(t)) {
			t.Fatal("webConfigured = true with an empty [web] table")
		}
	})

	t.Run("unresolvable_reference_is_unconfigured", func(t *testing.T) {
		// The key is set but its env: name resolves to nothing, so
		// webSearchProvider returns nil and the registry gains no verb —
		// exactly the case where the hint must still speak.
		t.Setenv(testWebKeyEnv, "") // forced missing, whatever the host carries
		webConfigEnv(t, `[web]
api_key = "env:LW_TEST_KEY"
`)

		if webConfigured(loadedConfig(t)) {
			t.Fatal("webConfigured = true with an unresolvable web.api_key reference")
		}
	})

	t.Run("unknown_provider_is_unconfigured", func(t *testing.T) {
		// Parity with TestSearchProviderGuard: an unknown provider wires no
		// verb even when a key resolves, so the UI agrees with the registry.
		t.Setenv(testWebKeyEnv, "lw-test-tavily-key-not-a-real-secret")
		webConfigEnv(t, `[web]
provider = "nope"
api_key = "env:LW_TEST_KEY"
`)

		if webConfigured(loadedConfig(t)) {
			t.Fatal(`webConfigured = true with provider "nope"`)
		}
	})

	t.Run("nil_config_is_unconfigured_and_safe", func(t *testing.T) {
		// config.Load fails into a nil *Config (a malformed config.toml), and
		// cmdTUI passes exactly that here: the seam must answer false, not
		// panic — a broken config is an unconfigured vault as far as web
		// lookup goes.
		if webConfigured(nil) {
			t.Fatal("webConfigured(nil) = true, want false")
		}
	})
}
