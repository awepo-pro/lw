package main

// doctor_baseurl_test.go pins 061 D9's two `cmd/lw` surfaces for the
// base_url footgun (s47: the user pasted OpenRouter's full chat-completions
// URL, lw appended the path again and the provider answered a bare 404):
// `lw doctor` fails the config check with the config package's own text, and
// `lw config set llm.base_url` refuses the value before it reaches the file.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/testutil"
)

// baseURLFixText is config.ValidateBaseURL's frozen refusal for a value that
// ends in /chat/completions, with the API root the fix names.
func baseURLFixText(root string) string {
	return "config: llm.base_url ends in /chat/completions, but lw adds that path itself — set it to the API root: lw config set llm.base_url " + root
}

// TestDoctorBaseURL: a base_url ending in /chat/completions fails the config
// line (✗) with the same text lw config set and the client give, and a
// remedy that is the command that repairs it; a healthy base_url leaves the
// line passing.
func TestDoctorBaseURL(t *testing.T) {
	doctorTestEnv(t)
	root := testutil.CopyFixture(t, "minimal")

	t.Run("bad", func(t *testing.T) {
		writeConfigFile(t, os.Getenv("XDG_CONFIG_HOME"),
			"[llm]\nbase_url = \"https://openrouter.ai/api/v1/chat/completions/\"\n\n[extract]\ncommand = \""+fakeDoclingCommand(t)+"\"\n")

		rep := runDoctor(context.Background(), root, doctorOptions{})
		c := checkByName(t, rep, "config")
		if c.OK {
			t.Fatalf("config check passed for a base_url ending in /chat/completions: %+v", c)
		}
		if want := baseURLFixText("https://openrouter.ai/api/v1"); !strings.Contains(c.Detail, want) {
			t.Errorf("detail = %q, want it to carry %q", c.Detail, want)
		}
		wantFailed(t, c, "lw config set llm.base_url https://openrouter.ai/api/v1")

		var sb strings.Builder
		rep.writeText(&sb)
		var line string
		for _, l := range strings.Split(sb.String(), "\n") {
			if strings.HasPrefix(l, "✗ config") {
				line = l
			}
		}
		if line == "" {
			t.Fatalf("doctor output has no ✗ config line:\n%s", sb.String())
		}
		if !strings.Contains(line, baseURLFixText("https://openrouter.ai/api/v1")) {
			t.Errorf("the ✗ config line %q does not carry the refusal text", line)
		}
	})

	t.Run("good", func(t *testing.T) {
		writeConfigFile(t, os.Getenv("XDG_CONFIG_HOME"),
			"[llm]\nbase_url = \"https://openrouter.ai/api/v1\"\n\n[extract]\ncommand = \""+fakeDoclingCommand(t)+"\"\n")

		rep := runDoctor(context.Background(), root, doctorOptions{})
		if c := checkByName(t, rep, "config"); !c.OK {
			t.Errorf("config check failed for a plain API root: %+v", c)
		}
	})
}

// TestConfigSetBaseURLRefused: the setter applies config.ValidateBaseURL
// before anything is written — a usage error (exit 2) carrying the refusal
// text, the file byte for byte as it was — and still stores the API root.
func TestConfigSetBaseURLRefused(t *testing.T) {
	dir := configTestEnv(t)
	path := writeConfigFile(t, dir, "[llm]\nbase_url = \"https://old.example/v1\"\n")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	for _, tc := range []struct{ value, root string }{
		{"https://x/v1/chat/completions", "https://x/v1"},
		{"https://x/v1/Chat/Completions/", "https://x/v1"},
	} {
		stdout, stderr, code := runConfig(t, "set", "llm.base_url", tc.value)
		if code != 2 {
			t.Fatalf("set llm.base_url %s: exit %d, want 2; stdout=%q stderr=%q", tc.value, code, stdout, stderr)
		}
		if want := baseURLFixText(tc.root); !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want it to carry %q", stderr, want)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read config: %v", err)
		}
		if string(after) != string(before) {
			t.Fatalf("the refused value changed the config file:\nbefore: %q\nafter:  %q", before, after)
		}
	}

	stdout, stderr, code := runConfig(t, "set", "llm.base_url", "https://x/v1")
	if code != 0 {
		t.Fatalf("set llm.base_url https://x/v1: exit %d, want 0; stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "llm.base_url set to https://x/v1") {
		t.Errorf("stdout = %q, want the confirmation", stdout)
	}
	got, err := config.Load()
	if err != nil {
		t.Fatalf("Load saved config: %v", err)
	}
	if got.LLM.BaseURL != "https://x/v1" {
		t.Errorf("saved base_url = %q, want %q", got.LLM.BaseURL, "https://x/v1")
	}
}

// TestBaseURLWhitespace (FIX2 R9): a value pasted with a trailing space is
// still a value ending in /chat/completions — the check ignores surrounding
// white space, the repair command names a root without it, and `lw config set`
// stores what it accepts trimmed.
func TestBaseURLWhitespace(t *testing.T) {
	t.Run("doctor", func(t *testing.T) {
		doctorTestEnv(t)
		root := testutil.CopyFixture(t, "minimal")
		writeConfigFile(t, os.Getenv("XDG_CONFIG_HOME"),
			"[llm]\nbase_url = \"https://openrouter.ai/api/v1/chat/completions \"\n\n[extract]\ncommand = \""+fakeDoclingCommand(t)+"\"\n")

		c := checkByName(t, runDoctor(context.Background(), root, doctorOptions{}), "config")
		if want := baseURLFixText("https://openrouter.ai/api/v1"); !strings.Contains(c.Detail, want) {
			t.Errorf("detail = %q, want it to carry %q", c.Detail, want)
		}
		wantFailed(t, c, "lw config set llm.base_url https://openrouter.ai/api/v1")
		if want := "lw config set llm.base_url https://openrouter.ai/api/v1"; c.Remedy != want {
			t.Errorf("remedy = %q, want exactly %q", c.Remedy, want)
		}
	})

	t.Run("config set refuses a suffix hidden behind a space", func(t *testing.T) {
		dir := configTestEnv(t)
		path := writeConfigFile(t, dir, "[llm]\nbase_url = \"https://old.example/v1\"\n")
		before, _ := os.ReadFile(path)
		_, stderr, code := runConfig(t, "set", "llm.base_url", "https://x/v1/chat/completions ")
		if code != 2 || !strings.Contains(stderr, baseURLFixText("https://x/v1")) {
			t.Fatalf("exit %d, stderr %q, want exit 2 with the refusal text", code, stderr)
		}
		if after, _ := os.ReadFile(path); string(after) != string(before) {
			t.Errorf("the refused value changed the config file")
		}
	})

	t.Run("config set stores the trimmed value", func(t *testing.T) {
		configTestEnv(t)
		if _, stderr, code := runConfig(t, "set", "llm.base_url", "  https://x/v1 \n"); code != 0 {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		got, err := config.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got.LLM.BaseURL != "https://x/v1" {
			t.Errorf("saved base_url = %q, want %q", got.LLM.BaseURL, "https://x/v1")
		}
	})
}
