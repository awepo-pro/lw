package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/testutil"
)

// budgetKeyEnv is the variable every budgetEnv config references as its
// api_key — set by the test itself, never inherited, so the config check
// passes no matter what the outer shell exports. C-809: the default
// env:DEEPSEEK_API_KEY reference made low_budget_warns_with_remedy pass
// only where that key happened to be exported.
const budgetKeyEnv = "LW_TEST_BUDGET_KEY"

// budgetEnv points config.Load at a fresh temp config dir (never the
// user's real one) and pins the api_key to budgetKeyEnv; when toml is
// non-empty it is written as the config file the run reads.
func budgetEnv(t *testing.T, toml string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "xdg-config")
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv(budgetKeyEnv, testAPIKey)
	if toml != "" {
		writeConfigFile(t, dir, toml)
	}
}

// TestDoctorBudget pins the new "llm budget" check (008 contract §6): a
// max_tokens below config.MinRecommendedMaxTokens warns — OK stays true,
// so a warn is a pass — with the exact detail and remedy, the default
// budget passes unremarked, and the no-key remedy names env:LW_API_KEY.
// Every config here references budgetKeyEnv, set by the test itself, so
// nothing depends on the ambient environment (C-809).
func TestDoctorBudget(t *testing.T) {
	t.Run("low_budget_warns_with_remedy", func(t *testing.T) {
		// (A-007-5) the [extract] table pins the fake sidecar's ABSOLUTE
		// path and probeExtractorVersion is swapped, mirroring
		// doctorTestEnv: run() below dispatches with probe: true, so the
		// pdf extractor check would otherwise exec the real `docling
		// --version` (or take a different branch without it).
		budgetEnv(t, "[llm]\nmax_tokens = 8192\napi_key = \"env:"+budgetKeyEnv+"\"\n"+"[extract]\ncommand = \""+fakeDoclingCommand(t)+"\"\n")
		withProbe(t, healthyProbe) // the run() below probes with doctorOptions{probe: true}; never the network
		withExtractorProbe(t, func(ctx context.Context, cfg extract.PDFConfig) (string, error) {
			return doclingTestedVersion, nil
		})
		root := testutil.CopyFixture(t, "minimal")

		rep := runDoctor(context.Background(), root, doctorOptions{})
		c := checkByName(t, rep, "llm budget")
		if !c.OK {
			t.Fatalf("llm budget check = %+v, want OK kept true for a warn", c)
		}
		if !c.Warn {
			t.Fatalf("llm budget check = %+v, want Warn: true below the floor", c)
		}
		wantDetail := "llm.max_tokens = 8192 is below 16000; thinking models can spend a whole round reasoning and stop before acting"
		if c.Detail != wantDetail {
			t.Errorf("detail = %q, want %q", c.Detail, wantDetail)
		}
		wantRemedy := "lw config set llm.max_tokens 32768"
		if c.Remedy != wantRemedy {
			t.Errorf("remedy = %q, want %q", c.Remedy, wantRemedy)
		}
		if rep.failed() {
			t.Errorf("report failed() = true; a warn is a pass and must not change doctor's exit")
		}

		// The same story through the real dispatch path: exit 0.
		_, stderr, code := captureRun(t, func() int {
			return run([]string{"doctor", "--vault", root})
		})
		if code != 0 {
			t.Fatalf("lw doctor exit = %d, want 0 (a warn is a pass); stderr=%q", code, stderr)
		}
	})

	t.Run("default_budget_is_ok", func(t *testing.T) {
		// A config carrying only the api_key: no max_tokens in the file, so
		// the 32768 default applies — and the whole report is hermetic
		// (C-809), not just the check under test.
		budgetEnv(t, "[llm]\napi_key = \"env:"+budgetKeyEnv+"\"\n")
		root := testutil.CopyFixture(t, "minimal")

		c := checkByName(t, runDoctor(context.Background(), root, doctorOptions{}), "llm budget")
		if !c.OK {
			t.Fatalf("llm budget check = %+v, want OK at the default budget", c)
		}
		if c.Warn {
			t.Errorf("llm budget check = %+v, want no warn at the default budget", c)
		}
		if c.Detail != "llm.max_tokens = 32768" {
			t.Errorf("detail = %q, want %q", c.Detail, "llm.max_tokens = 32768")
		}
		if c.Remedy != "" {
			t.Errorf("remedy = %q, want empty for a passing check", c.Remedy)
		}
	})

	t.Run("no_key_remedy_names_lw_api_key", func(t *testing.T) {
		c := checkConfig(configWithKey(""), nil)
		if !strings.Contains(c.Remedy, "env:LW_API_KEY") {
			t.Errorf("remedy = %q, want it to name env:LW_API_KEY", c.Remedy)
		}
		if strings.Contains(c.Remedy, "DEEPSEEK") {
			t.Errorf("remedy = %q, want no DEEPSEEK left in the remedy", c.Remedy)
		}
	})
}
