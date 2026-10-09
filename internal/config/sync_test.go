package config

// sync_test.go pins 042 D1: the [vault] and [sync] tables. The promise that
// matters most is the negative one — a config that names neither table is
// written back byte for byte as it was before 042 existed — because every
// user without a remote vault still has exactly the file they had.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// preSyncDefaultBytes is what Default().Save() wrote before 042 added the
// [vault] and [sync] tables. It is copied from that build, not derived: the
// test below must fail if the new tables ever leak into a config that never
// named them.
const preSyncDefaultBytes = `theme = ""

[llm]
  base_url = "https://api.deepseek.com/v1"
  model = "deepseek-v4-flash"
  api_key = "env:DEEPSEEK_API_KEY"
  temperature = 0.2
  max_tokens = 32768
  thinking = "off"
  [llm.limits]
    max_tool_rounds = 24
    context_tokens = 96000

[web]
  provider = "tavily"
  api_key = ""
  max_results = 5

[extract]
  command = "docling"
`

// preSyncRichBytes is the same, with every optional table 038/034/026/007
// added — the worst case for "nothing new appears".
const preSyncRichBytes = `theme = "nord"

[llm]
  base_url = "https://api.deepseek.com/v1"
  model = "deepseek-v4-flash"
  api_key = "env:DEEPSEEK_API_KEY"
  temperature = 0.2
  max_tokens = 32768
  thinking = "off"
  stall_timeout = "2m"
  [llm.limits]
    max_tool_rounds = 24
    context_tokens = 96000

[web]
  provider = "tavily"
  api_key = ""
  max_results = 5

[extract]
  command = "docling"
  timeout = "90s"

[open]
  pdf = "zathura"

[trace]
  keep_mb = 64
`

// configBytes returns the config file Save last wrote.
func configBytes(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "lw", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	return string(b)
}

func boolPtr(b bool) *bool { return &b }

// TestConfigVaultSyncRoundTrip: a config without [vault]/[sync] round-trips
// byte-identically through Save; with them it parses, survives Save and Load
// unchanged, and AutoSync() answers for its three cases.
func TestConfigVaultSyncRoundTrip(t *testing.T) {
	t.Run("without the tables the bytes are the pre-042 bytes", func(t *testing.T) {
		for _, tc := range []struct{ name, bytes string }{
			{"default", preSyncDefaultBytes},
			{"every optional table", preSyncRichBytes},
		} {
			t.Run(tc.name, func(t *testing.T) {
				dir := withConfigDir(t)
				writeConfig(t, dir, tc.bytes)
				c, err := Load()
				if err != nil {
					t.Fatalf("Load: %v", err)
				}
				if c.Sync != nil || c.Vault.Path != "" {
					t.Fatalf("Load invented a table: Vault %+v Sync %+v", c.Vault, c.Sync)
				}
				if err := c.Save(); err != nil {
					t.Fatalf("Save: %v", err)
				}
				if got := configBytes(t, dir); got != tc.bytes {
					t.Errorf("Save changed a config that names neither table:\n got  %q\n want %q", got, tc.bytes)
				}
			})
		}
	})

	t.Run("Default saves the pre-042 bytes", func(t *testing.T) {
		dir := withConfigDir(t)
		if err := Default().Save(); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if got := configBytes(t, dir); got != preSyncDefaultBytes {
			t.Errorf("Default().Save() =\n%q\nwant\n%q", got, preSyncDefaultBytes)
		}
	})

	t.Run("an empty Sync value is not written", func(t *testing.T) {
		// A caller that clears the table (&Sync{}) must not leave an empty
		// [sync] header behind: omitempty has to see through the pointer.
		dir := withConfigDir(t)
		c := Default()
		c.Sync = &Sync{}
		if err := c.Save(); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if got := configBytes(t, dir); got != preSyncDefaultBytes {
			t.Errorf("an empty Sync wrote bytes:\n%q", got)
		}
	})

	t.Run("with the tables they parse and round-trip", func(t *testing.T) {
		dir := withConfigDir(t)
		writeConfig(t, dir, `[vault]
path = "~/Downloads/ml-notes"

[sync]
remotes = ["home:lw-vault", "home-remote:lw-vault"]
auto = false
`)
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.Vault.Path != "~/Downloads/ml-notes" {
			t.Errorf("Vault.Path = %q, want the file's ~/Downloads/ml-notes (expansion is ResolvedPath's job)", c.Vault.Path)
		}
		if want := []string{"home:lw-vault", "home-remote:lw-vault"}; c.Sync == nil || !reflect.DeepEqual(c.Sync.Remotes, want) {
			t.Fatalf("Sync = %+v, want remotes %q in file order", c.Sync, want)
		}
		if c.Sync.Auto == nil || *c.Sync.Auto {
			t.Errorf("Sync.Auto = %v, want a pointer to false (the key is present)", c.Sync.Auto)
		}
		// Keys the file omits keep their defaults, as everywhere else.
		if c.LLM.Model != Default().LLM.Model {
			t.Errorf("LLM.Model = %q, want the default", c.LLM.Model)
		}

		if err := c.Save(); err != nil {
			t.Fatalf("Save: %v", err)
		}
		first := configBytes(t, dir)
		for _, want := range []string{"[vault]", `path = "~/Downloads/ml-notes"`, "[sync]", `remotes = ["home:lw-vault", "home-remote:lw-vault"]`, "auto = false"} {
			if !strings.Contains(first, want) {
				t.Errorf("saved config lacks %q:\n%s", want, first)
			}
		}
		again, err := Load()
		if err != nil {
			t.Fatalf("Load after Save: %v", err)
		}
		if again.Vault != c.Vault || !reflect.DeepEqual(again.Sync, c.Sync) {
			t.Errorf("round trip changed the tables:\n got  %+v %+v\n want %+v %+v", again.Vault, again.Sync, c.Vault, c.Sync)
		}
		if err := again.Save(); err != nil {
			t.Fatalf("second Save: %v", err)
		}
		if second := configBytes(t, dir); second != first {
			t.Errorf("Save is not stable:\n first  %q\n second %q", first, second)
		}
	})

	t.Run("an absent auto key stays absent", func(t *testing.T) {
		dir := withConfigDir(t)
		writeConfig(t, dir, "[sync]\nremotes = [\"home:v\"]\n")
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.Sync == nil || c.Sync.Auto != nil {
			t.Fatalf("Sync = %+v, want Auto nil", c.Sync)
		}
		if err := c.Save(); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if got := configBytes(t, dir); strings.Contains(got, "auto") {
			t.Errorf("Save wrote an auto key the file never named:\n%s", got)
		}
	})

	t.Run("AutoSync", func(t *testing.T) {
		cases := []struct {
			name string
			s    *Sync
			want bool
		}{
			{"no table", nil, false},
			{"empty table", &Sync{}, false},
			{"remotes, auto absent: on by default", &Sync{Remotes: []string{"home:v"}}, true},
			{"remotes, auto true", &Sync{Remotes: []string{"home:v"}, Auto: boolPtr(true)}, true},
			{"remotes, auto false: off", &Sync{Remotes: []string{"home:v"}, Auto: boolPtr(false)}, false},
			{"auto true but no remotes: nothing to sync with", &Sync{Auto: boolPtr(true)}, false},
			{"empty remotes list", &Sync{Remotes: []string{}}, false},
		}
		for _, tc := range cases {
			if got := tc.s.AutoSync(); got != tc.want {
				t.Errorf("%s: AutoSync() = %v, want %v", tc.name, got, tc.want)
			}
		}
		// Reached through a Config the way every caller does.
		c := &Config{}
		if c.Sync.AutoSync() {
			t.Error("a Config with no Sync reports AutoSync")
		}
		c.Sync = &Sync{Remotes: []string{"home:v"}}
		if !c.Sync.AutoSync() {
			t.Error("a Config with remotes does not report AutoSync")
		}
	})

	t.Run("RemoteList is nil-safe", func(t *testing.T) {
		var s *Sync
		if got := s.RemoteList(); len(got) != 0 {
			t.Errorf("nil Sync RemoteList = %q, want none", got)
		}
		s = &Sync{Remotes: []string{"a:b", "c:d"}}
		if got := s.RemoteList(); !reflect.DeepEqual(got, []string{"a:b", "c:d"}) {
			t.Errorf("RemoteList = %q", got)
		}
		// A copy: a caller that appends to it must not edit the config.
		got := s.RemoteList()
		got[0] = "mutated"
		if s.Remotes[0] != "a:b" {
			t.Error("RemoteList aliases the config's slice")
		}
	})
}

// TestVaultResolvedPath: "~" and "~/..." in [vault] path expand against the
// home directory; everything else is the user's word as written.
func TestVaultResolvedPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cases := []struct{ in, want string }{
		{"", ""},
		{"~", home},
		{"~/", home},
		{"~/Downloads/ml-notes", filepath.Join(home, "Downloads", "ml-notes")},
		{"/srv/vault", "/srv/vault"},
		{"relative/vault", "relative/vault"},
		{"~other/vault", "~other/vault"}, // another user's home is not ours to guess
		{"a/~/b", "a/~/b"},
	}
	for _, tc := range cases {
		if got := (Vault{Path: tc.in}).ResolvedPath(); got != tc.want {
			t.Errorf("ResolvedPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
