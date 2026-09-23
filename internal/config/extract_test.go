package config

// extract_test.go pins the [extract] table end to end (007 T3, F.X1–F.X4):
// the defaults, the merge's treatment of a partial table, Load's validation
// of a bad duration, and the Save round trip — including the omission of an
// empty timeout, which must not gain a key on Save. The frozen regression
// test ships with the subtask and stays forever (D-10C).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

func TestExtractConfig(t *testing.T) {
	t.Run("defaults_docling_300s", func(t *testing.T) {
		if DefaultExtractTimeout != 300*time.Second {
			t.Fatalf("DefaultExtractTimeout = %s, want 300s", DefaultExtractTimeout)
		}
		want := Extract{Command: "docling"}
		if got := Default().Extract; got != want {
			t.Fatalf("Default().Extract = %+v, want %+v", got, want)
		}
		if got := Default().Extract.Argv(); len(got) != 1 || got[0] != "docling" {
			t.Fatalf("Default().Extract.Argv() = %q, want [docling]", got)
		}
		if got := Default().Extract.TimeoutDuration(); got != DefaultExtractTimeout {
			t.Fatalf("Default().Extract.TimeoutDuration() = %s, want %s", got, DefaultExtractTimeout)
		}

		// A missing config file resolves to Default, so a fresh install gets
		// the same extraction backend (F.X1).
		withConfigDir(t)
		got, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got.Extract != want {
			t.Fatalf("Load() with no config file: Extract = %+v, want %+v", got.Extract, want)
		}
		if d := got.Extract.TimeoutDuration(); d != DefaultExtractTimeout {
			t.Fatalf("Load() with no config file: TimeoutDuration() = %s, want %s", d, DefaultExtractTimeout)
		}
	})

	t.Run("argv_splits_on_whitespace", func(t *testing.T) {
		// The uvx form the workflow pins: an argv prefix of 4, so the
		// caller can append the PDF path as the last argument.
		x := Extract{Command: "uvx --from docling==2.130.0 docling"}
		got := x.Argv()
		want := []string{"uvx", "--from", "docling==2.130.0", "docling"}
		if len(got) != len(want) {
			t.Fatalf("Argv() = %q, want %q", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("Argv() = %q, want %q", got, want)
			}
		}

		// An empty command still names a backend: it falls back to docling,
		// the same default Default() ships (frozen API).
		if got := (Extract{}).Argv(); len(got) != 1 || got[0] != "docling" {
			t.Fatalf("Extract{}.Argv() = %q, want [docling]", got)
		}

		// The duration mapping mirrors StallTimeoutDuration: "" is the key
		// absent and resolves to the default, a set value parses.
		if got := (Extract{Timeout: "90s"}).TimeoutDuration(); got != 90*time.Second {
			t.Fatalf("Extract{Timeout: \"90s\"}.TimeoutDuration() = %s, want 90s", got)
		}
		if got := (Extract{}).TimeoutDuration(); got != DefaultExtractTimeout {
			t.Fatalf("Extract{}.TimeoutDuration() = %s, want %s", got, DefaultExtractTimeout)
		}
	})

	t.Run("partial_table_keeps_default_command", func(t *testing.T) {
		// A file naming only extract.timeout must keep the default command —
		// the [web] merge pattern: a key the file omits is not a key the
		// file zeroed (F.X2).
		dir := withConfigDir(t)
		writeConfig(t, dir, `[extract]
timeout = "600s"
`)
		got, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		want := Extract{Command: "docling", Timeout: "600s"}
		if got.Extract != want {
			t.Fatalf("Load() Extract = %+v, want %+v", got.Extract, want)
		}
		if d := got.Extract.TimeoutDuration(); d != 600*time.Second {
			t.Fatalf("TimeoutDuration() = %s, want 600s", d)
		}

		// The same assertion against mergeOverDefault directly, so the spot
		// the merge handles the table is pinned independently of Load's
		// plumbing: a file with no [extract] keys must not overwrite the
		// defaults, even though fromShadow of it decodes to a zero Extract.
		var s shadowConfig
		md, err := toml.Decode("model = \"x\"\n", &s)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		merged := mergeOverDefault(Default(), fromShadow(s), md)
		if merged.Extract != (Extract{Command: "docling"}) {
			t.Fatalf("mergeOverDefault with no [extract] keys: Extract = %+v, want {docling \"\"}", merged.Extract)
		}
	})

	t.Run("load_rejects_zero_negative_unparsable", func(t *testing.T) {
		// F.X3, the llm.stall_timeout validator shape — but zero is also
		// rejected here: stall_timeout treats "0" as "no bound", while an
		// extraction that never runs is a defect, not a mode.
		for _, bad := range []string{"0s", "-1s", "x"} {
			dir := withConfigDir(t)
			writeConfig(t, dir, `[extract]
timeout = "`+bad+`"
`)
			_, err := Load()
			if err == nil {
				t.Fatalf("Load with extract.timeout = %q: got nil error, want one naming extract.timeout", bad)
			}
			if !strings.HasPrefix(err.Error(), "config: extract.timeout:") {
				t.Fatalf("Load with extract.timeout = %q: error %q does not have the config: extract.timeout: shape", bad, err)
			}
		}
	})

	t.Run("configured_value_round_trips", func(t *testing.T) {
		dir := withConfigDir(t)
		writeConfig(t, dir, `[llm]
model = "custom-model"

[extract]
command = "uvx --from docling==2.130.0 docling"
timeout  = "600s"
`)
		got, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		want := Extract{Command: "uvx --from docling==2.130.0 docling", Timeout: "600s"}
		if got.Extract != want {
			t.Fatalf("Load() Extract = %+v, want %+v", got.Extract, want)
		}

		// Save writes the command and timeout exactly as held, and the
		// saved file reads back identically (F.X4).
		if err := got.Save(); err != nil {
			t.Fatalf("Save: %v", err)
		}
		b, err := os.ReadFile(filepath.Join(dir, "lw", "config.toml"))
		if err != nil {
			t.Fatalf("read config file: %v", err)
		}
		for _, want := range []string{
			"[extract]",
			`command = "uvx --from docling==2.130.0 docling"`,
			`timeout = "600s"`,
		} {
			if !strings.Contains(string(b), want) {
				t.Fatalf("saved config lost %s:\n%s", want, b)
			}
		}
		again, err := Load()
		if err != nil {
			t.Fatalf("Load after Save: %v", err)
		}
		if again.Extract != want {
			t.Fatalf("Extract round trip mismatch:\n got  %+v\n want %+v", again.Extract, want)
		}
	})

	t.Run("empty_timeout_not_written", func(t *testing.T) {
		// Timeout == "" is not written (same treatment as stall_timeout):
		// the 300s lives in TimeoutDuration, not on disk, so a config that
		// never named the key must not gain one on Save.
		dir := withConfigDir(t)
		writeConfig(t, dir, `[extract]
command = "docling"
`)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if err := cfg.Save(); err != nil {
			t.Fatalf("Save: %v", err)
		}
		b, err := os.ReadFile(filepath.Join(dir, "lw", "config.toml"))
		if err != nil {
			t.Fatalf("read config file: %v", err)
		}
		if strings.Contains(string(b), "timeout") {
			t.Fatalf("Save wrote extract.timeout for an empty value:\n%s", b)
		}
	})
}
