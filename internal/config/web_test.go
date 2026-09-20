package config

// web_test.go pins the [web] table end to end (010 contract §4, C-1001):
// the defaults, the merge's treatment of a file without one, and the TOML
// round trip. The frozen regression test ships with the subtask and stays
// forever (D-10C).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestWebConfig(t *testing.T) {
	t.Run("defaults_tavily_empty_key_max5", func(t *testing.T) {
		want := Web{Provider: "tavily", APIKey: "", MaxResults: 5}
		if got := Default().Web; got != want {
			t.Fatalf("Default().Web = %+v, want %+v", got, want)
		}

		// A missing config file resolves to Default, so a fresh install has
		// the same web configuration as the documented defaults.
		withConfigDir(t)
		got, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got.Web != want {
			t.Fatalf("Load() with no config file: Web = %+v, want %+v", got.Web, want)
		}
	})

	t.Run("merge_preserves_web", func(t *testing.T) {
		// A hand-written file that omits [web] entirely must keep the
		// defaults after mergeOverDefault — the same rule the llm keys
		// follow: a key the file does not name is not a key the file zeroed.
		dir := withConfigDir(t)
		writeConfig(t, dir, `[llm]
model = "custom-model"
`)

		got, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		want := Default().Web
		if got.Web != want {
			t.Fatalf("Load() over a file without [web]: Web = %+v, want the defaults %+v", got.Web, want)
		}

		// The same assertion against mergeOverDefault directly, so the spot
		// the merge handles the table is pinned independently of Load's
		// plumbing: a file with no [web] keys must not overwrite the
		// defaults, even though fromShadow of it decodes to a zero Web.
		var s shadowConfig
		md, err := toml.Decode("model = \"x\"\n", &s)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		merged := mergeOverDefault(Default(), fromShadow(s), md)
		if merged.Web != want {
			t.Fatalf("mergeOverDefault with no [web] keys: Web = %+v, want %+v", merged.Web, want)
		}
	})

	t.Run("toml_round_trip", func(t *testing.T) {
		dir := withConfigDir(t)
		writeConfig(t, dir, `[llm]
model = "custom-model"

[web]
provider    = "tavily"
api_key     = "env:TAVILY_API_KEY"
max_results = 7
`)

		got, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		want := Web{Provider: "tavily", APIKey: "env:TAVILY_API_KEY", MaxResults: 7}
		if got.Web != want {
			t.Fatalf("Load() Web = %+v, want %+v", got.Web, want)
		}

		// Save writes the reference exactly as held — never the resolved
		// value — and the saved file reads back identically.
		if err := got.Save(); err != nil {
			t.Fatalf("Save: %v", err)
		}
		b, err := os.ReadFile(filepath.Join(dir, "lw", "config.toml"))
		if err != nil {
			t.Fatalf("read config file: %v", err)
		}
		if !strings.Contains(string(b), `[web]`) {
			t.Fatalf("saved config has no [web] table:\n%s", b)
		}
		if strings.Contains(string(b), "TAVILY_API_KEY=tvly-") || strings.Contains(string(b), `api_key = "tvly-`) {
			t.Fatalf("saved config holds a literal web key:\n%s", b)
		}

		again, err := Load()
		if err != nil {
			t.Fatalf("Load after Save: %v", err)
		}
		if again.Web != want {
			t.Fatalf("Web round trip mismatch:\n got  %+v\n want %+v", again.Web, want)
		}
	})
}
