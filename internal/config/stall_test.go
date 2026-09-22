package config

// stall_test.go pins the llm.stall_timeout key end to end (026 T3, F.K1–F.K3):
// the duration-string mapping and its default, Load's validation of an
// unparsable or negative value, and the Save round trip — including the
// byte-identity of a config without the key, which must not gain one on
// Save. The frozen regression test ships with the subtask and stays forever
// (D-10C).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStallTimeoutConfig(t *testing.T) {
	t.Run("default_keyless_120s", func(t *testing.T) {
		if DefaultStallTimeout != 120*time.Second {
			t.Fatalf("DefaultStallTimeout = %s, want 120s", DefaultStallTimeout)
		}
		// Default ships the key empty: the 120s lives in
		// StallTimeoutDuration, not on disk, so Save never writes it unless
		// the user set it (F.K3's byte identity).
		if got := Default().LLM.StallTimeout; got != "" {
			t.Fatalf("Default().LLM.StallTimeout = %q, want \"\"", got)
		}
		if got := Default().LLM.StallTimeoutDuration(); got != DefaultStallTimeout {
			t.Fatalf("Default().LLM.StallTimeoutDuration() = %s, want %s", got, DefaultStallTimeout)
		}

		// A missing config file resolves to Default, so a fresh install gets
		// the same bound.
		withConfigDir(t)
		got, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if d := got.LLM.StallTimeoutDuration(); d != DefaultStallTimeout {
			t.Fatalf("Load() with no config file: StallTimeoutDuration() = %s, want %s", d, DefaultStallTimeout)
		}
	})

	t.Run("duration_mapping", func(t *testing.T) {
		cases := []struct {
			raw  string
			want time.Duration
		}{
			{"", DefaultStallTimeout}, // key absent → default
			{"0", 0},                  // explicit off
			{"0s", 0},                 // explicit off, unit form
			{"45s", 45 * time.Second},
			{"2m", 2 * time.Minute},
		}
		for _, tc := range cases {
			l := LLM{StallTimeout: tc.raw}
			if got := l.StallTimeoutDuration(); got != tc.want {
				t.Errorf("LLM{StallTimeout: %q}.StallTimeoutDuration() = %s, want %s", tc.raw, got, tc.want)
			}
		}
	})

	t.Run("load_rejects_unparsable_and_negative", func(t *testing.T) {
		for _, bad := range []string{"bogus", "-5s", "soon"} {
			dir := withConfigDir(t)
			writeConfig(t, dir, `[llm]
model = "m"
stall_timeout = "`+bad+`"
`)
			_, err := Load()
			if err == nil {
				t.Fatalf("Load with stall_timeout = %q: got nil error, want one naming llm.stall_timeout", bad)
			}
			if !strings.Contains(err.Error(), "llm.stall_timeout") {
				t.Fatalf("Load with stall_timeout = %q: error %q does not name llm.stall_timeout", bad, err)
			}
		}
	})

	t.Run("keyless_config_saves_byte_identically", func(t *testing.T) {
		dir := withConfigDir(t)
		writeConfig(t, dir, `[llm]
model = "custom-model"
`)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if err := cfg.Save(); err != nil {
			t.Fatalf("Save: %v", err)
		}
		read := func() string {
			t.Helper()
			b, err := os.ReadFile(filepath.Join(dir, "lw", "config.toml"))
			if err != nil {
				t.Fatalf("read saved config: %v", err)
			}
			return string(b)
		}
		// The key is omitted when empty: a config that never named
		// stall_timeout must not gain one on Save (F.K3), and Save must be a
		// byte fixed point over it — Save→Load→Save writes the same bytes.
		first := read()
		if strings.Contains(first, "stall_timeout") {
			t.Fatalf("Save wrote stall_timeout for an empty value:\n%s", first)
		}
		cfg2, err := Load()
		if err != nil {
			t.Fatalf("re-Load: %v", err)
		}
		if err := cfg2.Save(); err != nil {
			t.Fatalf("second Save: %v", err)
		}
		if second := read(); second != first {
			t.Fatalf("Save is not a fixed point:\n--- first\n%s\n--- second\n%s", first, second)
		}
	})

	t.Run("configured_value_round_trips", func(t *testing.T) {
		dir := withConfigDir(t)
		writeConfig(t, dir, `[llm]
model = "custom-model"
stall_timeout = "45s"
`)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if d := cfg.LLM.StallTimeoutDuration(); d != 45*time.Second {
			t.Fatalf("StallTimeoutDuration() = %s, want 45s", d)
		}
		if err := cfg.Save(); err != nil {
			t.Fatalf("Save: %v", err)
		}
		b, err := os.ReadFile(filepath.Join(dir, "lw", "config.toml"))
		if err != nil {
			t.Fatalf("read saved config: %v", err)
		}
		if !strings.Contains(string(b), `stall_timeout = "45s"`) {
			t.Fatalf("saved config lost stall_timeout:\n%s", b)
		}
		cfg2, err := Load()
		if err != nil {
			t.Fatalf("re-Load: %v", err)
		}
		if d := cfg2.LLM.StallTimeoutDuration(); d != 45*time.Second {
			t.Fatalf("after Save→Load: StallTimeoutDuration() = %s, want 45s", d)
		}
	})
}
