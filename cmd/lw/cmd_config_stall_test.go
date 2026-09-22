// cmd_config_stall_test.go pins the `lw config` seam for 026 T3's
// llm.stall_timeout (F.K): the key is CLI-settable like every other [llm]
// key, the setter refuses a value config.Load would refuse — not a duration,
// or negative — and the stored value shows in `lw config`'s table. Runs the
// real dispatch path against a temp config dir, same as cmd_config_test.go.
package main

import (
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/config"
)

// TestConfigSetStallTimeout sets a plain duration, checks the saved file
// carries it, and checks `lw config` shows the row.
func TestConfigSetStallTimeout(t *testing.T) {
	configTestEnv(t)

	stdout, stderr, code := runConfig(t, "set", "llm.stall_timeout", "45s")
	if code != 0 {
		t.Fatalf("set llm.stall_timeout 45s: exit %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "llm.stall_timeout set to 45s") {
		t.Fatalf("set confirmation missing from stdout:\n%s", stdout)
	}

	got, err := config.Load()
	if err != nil {
		t.Fatalf("Load saved config: %v", err)
	}
	if got.LLM.StallTimeout != "45s" {
		t.Fatalf("saved stall_timeout = %q, want %q", got.LLM.StallTimeout, "45s")
	}

	stdout, _, code = runConfig(t)
	if code != 0 {
		t.Fatalf("config: exit %d", code)
	}
	wantRow(t, stdout, "llm.stall_timeout", "45s")
}

// TestConfigSetStallTimeoutZero pins "0" = off: accepted, stored, and Load
// keeps it (StallTimeoutDuration maps it to no bound).
func TestConfigSetStallTimeoutZero(t *testing.T) {
	configTestEnv(t)

	_, stderr, code := runConfig(t, "set", "llm.stall_timeout", "0")
	if code != 0 {
		t.Fatalf("set llm.stall_timeout 0: exit %d, stderr: %s", code, stderr)
	}
	got, err := config.Load()
	if err != nil {
		t.Fatalf("Load saved config: %v", err)
	}
	if got.LLM.StallTimeout != "0" {
		t.Fatalf("saved stall_timeout = %q, want %q", got.LLM.StallTimeout, "0")
	}
	if d := got.LLM.StallTimeoutDuration(); d != 0 {
		t.Fatalf("StallTimeoutDuration(%q) = %v, want 0 (bound off)", got.LLM.StallTimeout, d)
	}
}

// TestConfigSetStallTimeoutRejects pins the setter's rejection of anything
// config.Load would refuse: not a duration, or negative. Both errors must
// name the key, so the fix the user needs is in the message.
func TestConfigSetStallTimeoutRejects(t *testing.T) {
	configTestEnv(t)

	for _, tc := range []struct {
		value string
		why   string
	}{
		{"soon", "not a duration"},
		{"-5s", "negative"},
	} {
		_, stderr, code := runConfig(t, "set", "llm.stall_timeout", tc.value)
		if code != 2 {
			t.Fatalf("set llm.stall_timeout %s (%s): exit %d, want 2; stderr: %s", tc.value, tc.why, code, stderr)
		}
		if !strings.Contains(stderr, "llm.stall_timeout") {
			t.Fatalf("set llm.stall_timeout %s (%s): stderr does not name the key:\n%s", tc.value, tc.why, stderr)
		}
	}

	// Nothing was written: the config file stays absent, so a rejected set
	// leaves the previous configuration — or the defaults — in force.
	if _, err := config.Load(); err != nil {
		t.Fatalf("Load after rejected sets: %v", err)
	}
}
