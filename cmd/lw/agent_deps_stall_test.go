package main

// agent_deps_stall_test.go pins the one wiring line 026 T3 adds (F.K4): the
// agent client's llm.Config carries the configured stall bound, mapped
// through config.LLM.StallTimeoutDuration. The mapping is asserted through
// ingestLLMConfig — the [llm]-to-client-config seam newIngestAgent builds
// its client over — so no network and no LLM is involved.

import (
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/config"
)

func TestIngestLLMConfigStallTimeout(t *testing.T) {
	t.Run("configured_value_passes_through", func(t *testing.T) {
		cfg := config.Default()
		cfg.LLM.StallTimeout = "45s"
		got := ingestLLMConfig(cfg, "test-key")
		if got.StallTimeout != 45*time.Second {
			t.Fatalf("ingestLLMConfig.StallTimeout = %s, want 45s", got.StallTimeout)
		}
	})

	t.Run("keyless_config_gets_default", func(t *testing.T) {
		cfg := config.Default() // StallTimeout "" — the key absent
		got := ingestLLMConfig(cfg, "test-key")
		if got.StallTimeout != config.DefaultStallTimeout {
			t.Fatalf("ingestLLMConfig.StallTimeout = %s, want %s", got.StallTimeout, config.DefaultStallTimeout)
		}
	})
}
