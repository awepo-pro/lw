package eval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

// TestRewriteConfig pins the line-based rewrite: only the value of the named
// key changes, in the right table, with its quoting, spacing and trailing
// comment intact; every other byte of the file is left alone, and anything
// the rewrite cannot do faithfully is refused (037 T3).
func TestRewriteConfig(t *testing.T) {
	tests := []struct {
		name string
		src  string
		sets []string
		want string // the whole rewritten file
		err  string // the whole error text, when the rewrite must be refused
	}{
		{
			name: "the frozen config: only thinking changes",
			src:  "[llm]\nmodel = \"m\"\nthinking = \"off\"\napi_key = \"k\"\n",
			sets: []string{"llm.thinking=on"},
			want: "[llm]\nmodel = \"m\"\nthinking = \"on\"\napi_key = \"k\"\n",
		},
		{
			name: "spacing and a trailing comment stay",
			src:  "[llm]\nthinking   =   \"off\"   # stays\nmodel = \"m\"\n",
			sets: []string{"llm.thinking=on"},
			want: "[llm]\nthinking   =   \"on\"   # stays\nmodel = \"m\"\n",
		},
		{
			name: "an indented key and a header comment",
			src:  "[llm] # the endpoint\n  thinking = \"off\"\n",
			sets: []string{"llm.thinking=default"},
			want: "[llm] # the endpoint\n  thinking = \"default\"\n",
		},
		{
			name: "an integer stays bare",
			src:  "[llm]\nmax_tokens = 4096 # tokens\n",
			sets: []string{"llm.max_tokens=8192"},
			want: "[llm]\nmax_tokens = 8192 # tokens\n",
		},
		{
			name: "a negative integer",
			src:  "[llm]\nmax_tokens = 4096\n",
			sets: []string{"llm.max_tokens=-1"},
			want: "[llm]\nmax_tokens = -1\n",
		},
		{
			name: "an integer key refuses a word",
			src:  "[llm]\nmax_tokens = 4096\n",
			sets: []string{"llm.max_tokens=lots"},
			err:  `--set-config llm.max_tokens: want an integer, got "lots"`,
		},
		{
			name: "a bool stays bare",
			src:  "[web]\nenabled = true\n",
			sets: []string{"web.enabled=false"},
			want: "[web]\nenabled = false\n",
		},
		{
			name: "a bool key refuses yes",
			src:  "[web]\nenabled = true\n",
			sets: []string{"web.enabled=yes"},
			err:  `--set-config web.enabled: want true or false, got "yes"`,
		},
		{
			name: "a float stays bare",
			src:  "[llm]\ntemperature = 0.2\n",
			sets: []string{"llm.temperature=0.7"},
			want: "[llm]\ntemperature = 0.7\n",
		},
		{
			name: "a float key refuses a word",
			src:  "[llm]\ntemperature = 0.2\n",
			sets: []string{"llm.temperature=hot"},
			err:  `--set-config llm.temperature: want a number, got "hot"`,
		},
		{
			name: "the same key in another table is not touched",
			src:  "[web]\nmodel = \"w\"\n\n[llm]\nmodel = \"m\"\n",
			sets: []string{"llm.model=x"},
			want: "[web]\nmodel = \"w\"\n\n[llm]\nmodel = \"x\"\n",
		},
		{
			name: "a nested table",
			src:  "[llm]\nmodel = \"m\"\n\n[llm.limits]\nmax_tool_rounds = 12\n",
			sets: []string{"llm.limits.max_tool_rounds=20"},
			want: "[llm]\nmodel = \"m\"\n\n[llm.limits]\nmax_tool_rounds = 20\n",
		},
		{
			name: "a root key",
			src:  "theme = \"dark\"\n[llm]\nmodel = \"m\"\n",
			sets: []string{"theme=light"},
			want: "theme = \"light\"\n[llm]\nmodel = \"m\"\n",
		},
		{
			name: "two keys",
			src:  "[llm]\nmodel = \"m\"\nthinking = \"off\"\n",
			sets: []string{"llm.thinking=on", "llm.model=big"},
			want: "[llm]\nmodel = \"big\"\nthinking = \"on\"\n",
		},
		{
			name: "quotes and backslashes are escaped",
			src:  "[llm]\nmodel = \"m\"\n",
			sets: []string{`llm.model=a"b\c`},
			want: "[llm]\nmodel = \"a\\\"b\\\\c\"\n",
		},
		{
			name: "an empty value",
			src:  "[llm]\nmodel = \"m\"\n",
			sets: []string{"llm.model="},
			want: "[llm]\nmodel = \"\"\n",
		},
		{
			name: "a literal string stays literal",
			src:  "[llm]\nmodel = 'm'\n",
			sets: []string{"llm.model=x"},
			want: "[llm]\nmodel = 'x'\n",
		},
		{
			name: "a literal string that cannot hold the value becomes a basic one",
			src:  "[llm]\nmodel = 'm'\n",
			sets: []string{"llm.model=it's"},
			want: "[llm]\nmodel = \"it's\"\n",
		},
		{
			name: "a hash inside the old string is not a comment",
			src:  "[llm]\nmodel = \"a#b\"  # why\n",
			sets: []string{"llm.model=c"},
			want: "[llm]\nmodel = \"c\"  # why\n",
		},
		{
			name: "CRLF line endings stay",
			src:  "[llm]\r\nmodel = \"m\"\r\nthinking = \"off\"\r\n",
			sets: []string{"llm.thinking=on"},
			want: "[llm]\r\nmodel = \"m\"\r\nthinking = \"on\"\r\n",
		},
		{
			name: "no trailing newline",
			src:  "[llm]\nthinking = \"off\"",
			sets: []string{"llm.thinking=on"},
			want: "[llm]\nthinking = \"on\"",
		},
		{
			name: "a key-looking line inside a multi-line string is not the key",
			src:  "[llm]\nnote = \"\"\"\nthinking = \"off\"\n[web]\n\"\"\"\nthinking = \"off\"\n",
			sets: []string{"llm.thinking=on"},
			want: "[llm]\nnote = \"\"\"\nthinking = \"off\"\n[web]\n\"\"\"\nthinking = \"on\"\n",
		},
		{
			name: "a key-looking line inside a multi-line array is not the key",
			src:  "[llm]\nlist = [\n  [1],\n  [2]\n]\nthinking = \"off\"\n",
			sets: []string{"llm.thinking=on"},
			want: "[llm]\nlist = [\n  [1],\n  [2]\n]\nthinking = \"on\"\n",
		},
		{
			name: "an unknown key",
			src:  "[llm]\nmodel = \"m\"\nthinking = \"off\"\n",
			sets: []string{"llm.nope=1"},
			err:  "--set-config llm.nope: no such key in config.toml",
		},
		{
			name: "an unknown table",
			src:  "[llm]\nmodel = \"m\"\n",
			sets: []string{"nope.model=1"},
			err:  "--set-config nope.model: no such key in config.toml",
		},
		{
			name: "a commented-out key is not a key",
			src:  "[llm]\n# thinking = \"off\"\nmodel = \"m\"\n",
			sets: []string{"llm.thinking=on"},
			err:  "--set-config llm.thinking: no such key in config.toml",
		},
		{
			name: "a table is not a value",
			src:  "[llm]\nmodel = \"m\"\n",
			sets: []string{"llm=x"},
			err:  "--set-config llm: no such key in config.toml",
		},
		{
			name: "an array value cannot be rewritten",
			src:  "[x]\ntags = [\"a\"]\n",
			sets: []string{"x.tags=b"},
			err:  "--set-config x.tags: the value is an array; only strings, integers, floats and booleans can be rewritten",
		},
		{
			name: "a multi-line string cannot be rewritten",
			src:  "[x]\nnote = \"\"\"\nhi\n\"\"\"\n",
			sets: []string{"x.note=b"},
			err:  "--set-config x.note: a multi-line string cannot be rewritten",
		},
		{
			name: "the same key twice",
			src:  "[llm]\nthinking = \"off\"\n",
			sets: []string{"llm.thinking=on", "llm.thinking=off"},
			err:  "--set-config llm.thinking: given twice",
		},
		{
			name: "no equals sign",
			src:  "[llm]\nthinking = \"off\"\n",
			sets: []string{"llm.thinking"},
			err:  "--set-config llm.thinking: want key=value",
		},
		{
			name: "an empty key",
			src:  "[llm]\nthinking = \"off\"\n",
			sets: []string{"=on"},
			err:  "--set-config =on: want key=value",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RewriteConfig([]byte(tc.src), tc.sets)
			if tc.err != "" {
				if err == nil || err.Error() != tc.err {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				if got != nil {
					t.Errorf("a refused rewrite returned bytes: %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("RewriteConfig: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("rewritten\n got %q\nwant %q", got, tc.want)
			}
			var m map[string]any
			if _, err := toml.Decode(string(got), &m); err != nil {
				t.Errorf("the rewritten file is not valid TOML: %v", err)
			}
		})
	}
}

// TestRewriteConfigValueDecodes pins that what is written is what a TOML
// reader reads back, including for values that need escaping (037 T3).
func TestRewriteConfigValueDecodes(t *testing.T) {
	for _, v := range []string{"plain", `a"b`, `back\slash`, "tab\there", "new\nline", "unié", `'quoted'`} {
		got, err := RewriteConfig([]byte("[llm]\nmodel = \"m\"\n"), []string{"llm.model=" + v})
		if err != nil {
			t.Fatalf("%q: %v", v, err)
		}
		var cfg struct {
			LLM struct{ Model string } `toml:"llm"`
		}
		if _, err := toml.Decode(string(got), &cfg); err != nil {
			t.Fatalf("%q: %v\n%s", v, err, got)
		}
		if cfg.LLM.Model != v {
			t.Errorf("model decodes to %q, want %q (file %q)", cfg.LLM.Model, v, got)
		}
	}
}

func TestVariantNote(t *testing.T) {
	if got := VariantNote([]string{"llm.thinking=on"}); got != "llm.thinking=on" {
		t.Errorf("note = %q", got)
	}
	if got := VariantNote([]string{"llm.thinking=on", "llm.model=x"}); got != "llm.thinking=on llm.model=x" {
		t.Errorf("note = %q", got)
	}
	if got := VariantNote(nil); got != "" {
		t.Errorf("note = %q, want empty", got)
	}
}

func TestDefaultConfigPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x/cfg")
	if got := DefaultConfigPath(); got != filepath.Join("/x/cfg", "lw", "config.toml") {
		t.Errorf("path = %q", got)
	}
}

// varFixture writes a config under a temp dir and points TMPDIR at another
// temp dir, so the variant's own scratch lands somewhere the test can list.
func varFixture(t *testing.T) (cfgPath, tmp string) {
	t.Helper()
	tmp = t.TempDir()
	t.Setenv("TMPDIR", tmp)
	cfgPath = filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[llm]\nmodel = \"m\"\nthinking = \"off\"\napi_key = \"k\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath, tmp
}

func gone(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}

// TestWithVariant pins the private config: a 0700 directory holding a 0600
// config.toml with only the named key changed, handed to fn as
// XDG_CONFIG_HOME, and removed on EVERY way out — return, error, panic and a
// cancelled context, even one fn is slow to honour (037 T3).
func TestWithVariant(t *testing.T) {
	t.Run("the copy, its modes, and removal on return", func(t *testing.T) {
		cfg, tmp := varFixture(t)
		var dir string
		err := WithVariant(context.Background(), cfg, []string{"llm.thinking=on"}, func(env []string) error {
			if len(env) != 1 || !strings.HasPrefix(env[0], "XDG_CONFIG_HOME=") {
				t.Fatalf("env = %v, want exactly XDG_CONFIG_HOME=<dir>", env)
			}
			dir = strings.TrimPrefix(env[0], "XDG_CONFIG_HOME=")
			if filepath.Dir(dir) != tmp {
				t.Errorf("the variant dir %s is not under TMPDIR %s", dir, tmp)
			}
			if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
				t.Errorf("variant dir: %v, mode %v; want 0700", err, fi)
			}
			file := filepath.Join(dir, "lw", "config.toml")
			b, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if want := "[llm]\nmodel = \"m\"\nthinking = \"on\"\napi_key = \"k\"\n"; string(b) != want {
				t.Errorf("config copy = %q, want %q", b, want)
			}
			if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 {
				t.Errorf("config copy: %v, mode %v; want 0600", err, fi)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("WithVariant: %v", err)
		}
		if !gone(t, dir) {
			t.Errorf("the variant dir %s survived WithVariant", dir)
		}
		// The original is untouched.
		if b, _ := os.ReadFile(cfg); !strings.Contains(string(b), `thinking = "off"`) {
			t.Errorf("the user's config was modified: %q", b)
		}
	})

	t.Run("an error from fn is returned and the dir is removed", func(t *testing.T) {
		cfg, _ := varFixture(t)
		boom := errors.New("boom")
		var dir string
		err := WithVariant(context.Background(), cfg, []string{"llm.thinking=on"}, func(env []string) error {
			dir = strings.TrimPrefix(env[0], "XDG_CONFIG_HOME=")
			return boom
		})
		if !errors.Is(err, boom) {
			t.Errorf("err = %v, want fn's error", err)
		}
		if !gone(t, dir) {
			t.Errorf("the variant dir survived an error")
		}
	})

	t.Run("a panic in fn still removes the dir", func(t *testing.T) {
		cfg, _ := varFixture(t)
		var dir string
		func() {
			defer func() {
				if recover() == nil {
					t.Error("the panic did not propagate")
				}
			}()
			_ = WithVariant(context.Background(), cfg, []string{"llm.thinking=on"}, func(env []string) error {
				dir = strings.TrimPrefix(env[0], "XDG_CONFIG_HOME=")
				panic("fn panics")
			})
		}()
		if !gone(t, dir) {
			t.Errorf("the variant dir survived a panic")
		}
	})

	t.Run("a cancelled context", func(t *testing.T) {
		cfg, _ := varFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		var dir string
		err := WithVariant(ctx, cfg, []string{"llm.thinking=on"}, func(env []string) error {
			dir = strings.TrimPrefix(env[0], "XDG_CONFIG_HOME=")
			cancel()
			<-ctx.Done()
			return ctx.Err()
		})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
		if !gone(t, dir) {
			t.Errorf("the variant dir survived a cancelled context")
		}
	})

	t.Run("a cancel removes the dir even while fn is still winding down", func(t *testing.T) {
		cfg, _ := varFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		removedWhileRunning := false
		var dir string
		err := WithVariant(ctx, cfg, []string{"llm.thinking=on"}, func(env []string) error {
			dir = strings.TrimPrefix(env[0], "XDG_CONFIG_HOME=")
			cancel()
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if gone(t, dir) {
					removedWhileRunning = true
					break
				}
				time.Sleep(2 * time.Millisecond)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("WithVariant: %v", err)
		}
		if !removedWhileRunning {
			t.Errorf("the dir was not removed by the cancel itself: fn had to return first")
		}
	})

	t.Run("an unknown key is refused before any directory exists", func(t *testing.T) {
		cfg, tmp := varFixture(t)
		called := false
		err := WithVariant(context.Background(), cfg, []string{"llm.nope=1"}, func([]string) error {
			called = true
			return nil
		})
		if err == nil || err.Error() != "--set-config llm.nope: no such key in config.toml" {
			t.Errorf("err = %v", err)
		}
		if called {
			t.Error("fn ran for a refused variant")
		}
		if kids, _ := os.ReadDir(tmp); len(kids) != 0 {
			t.Errorf("TMPDIR holds %d entries after a refusal", len(kids))
		}
	})

	t.Run("a missing config file", func(t *testing.T) {
		_, tmp := varFixture(t)
		missing := filepath.Join(t.TempDir(), "nope", "config.toml")
		err := WithVariant(context.Background(), missing, []string{"llm.thinking=on"}, func([]string) error { return nil })
		if err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("err = %v, want one naming %s", err, missing)
		}
		if kids, _ := os.ReadDir(tmp); len(kids) != 0 {
			t.Errorf("TMPDIR holds %d entries after a refusal", len(kids))
		}
	})

	t.Run("no pairs is not a variant", func(t *testing.T) {
		cfg, _ := varFixture(t)
		err := WithVariant(context.Background(), cfg, nil, func([]string) error { return nil })
		if err == nil {
			t.Error("WithVariant with no pairs succeeded")
		}
	})
}
