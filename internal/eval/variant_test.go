package eval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
			src:  "[llm.limits]\nmax_tool_rounds = 12\n",
			sets: []string{"llm.limits.max_tool_rounds=lots"},
			err:  `--set-config llm.limits.max_tool_rounds: want an integer, got "lots"`,
		},
		{
			// A-037-7: a key that may hold a secret never has its value echoed.
			// (max_tokens matches "token", so it is treated as one.)
			name: "an integer key with a secret-looking name refuses without echoing the value",
			src:  "[llm]\nmax_tokens = 4096\n",
			sets: []string{"llm.max_tokens=lots"},
			err:  `--set-config llm.max_tokens: want an integer`,
		},
		{
			name: "a bool stays bare",
			src:  "[web]\nenabled = true\n",
			sets: []string{"web.enabled=false"},
			want: "[web]\nenabled = false\n",
		},
		{
			name: "a bool key with a secret-looking name refuses without echoing the value",
			src:  "[web]\nauth_enabled = true\n",
			sets: []string{"web.auth_enabled=hunter2"},
			err:  `--set-config web.auth_enabled: want true or false`,
		},
		{
			name: "a float key with a secret-looking name refuses without echoing the value",
			src:  "[web]\nsecret_ratio = 0.5\n",
			sets: []string{"web.secret_ratio=hunter2"},
			err:  `--set-config web.secret_ratio: want a number`,
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
			err:  "--set-config: want key=value", // A-037-7: a malformed pair is never echoed back
		},
		{
			name: "an empty key",
			src:  "[llm]\nthinking = \"off\"\n",
			sets: []string{"=on"},
			err:  "--set-config: want key=value",
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

// TestVariantNote pins the default run note (A-037-7): "set-config:" and the
// key=value pairs — but a key that may hold a secret is listed by NAME only.
// The note lands in run.json and the scorecard, which get pasted into
// reports; an API key must not ride along (037 T3).
func TestVariantNote(t *testing.T) {
	tests := []struct {
		sets []string
		want string
	}{
		{[]string{"llm.thinking=on"}, "set-config: llm.thinking=on"},
		{[]string{"llm.thinking=on", "llm.model=big"}, "set-config: llm.thinking=on llm.model=big"},
		{[]string{"llm.api_key=sk-123"}, "set-config: llm.api_key"},
		{[]string{"llm.thinking=on", "web.api_key=tvly-abc", "llm.model=m"}, "set-config: llm.thinking=on web.api_key llm.model=m"},
		{[]string{"web.API_KEY=x"}, "set-config: web.API_KEY"},
		{[]string{"a.password=x"}, "set-config: a.password"},
		{[]string{"a.Secret=x"}, "set-config: a.Secret"},
		{[]string{"a.auth_header=x"}, "set-config: a.auth_header"},
		{[]string{"llm.max_tokens=16000"}, "set-config: llm.max_tokens"}, // "token" matches: the rule is by name
		{[]string{"llm.limits.max_tool_rounds=20"}, "set-config: llm.limits.max_tool_rounds=20"},
		{[]string{"theme=light"}, "set-config: theme=light"},
		{nil, ""},
	}
	for _, tc := range tests {
		if got := VariantNote(tc.sets); got != tc.want {
			t.Errorf("VariantNote(%q) = %q, want %q", tc.sets, got, tc.want)
		}
	}
}

func TestSecretKey(t *testing.T) {
	for key, want := range map[string]bool{
		"llm.api_key": true, "web.api_key": true, "web.API_KEY": true, "llm.max_tokens": true,
		"a.password": true, "a.PassWord": true, "a.secret": true, "llm.auth": true, "x.authorization": true,
		"keyring":      true,
		"llm.thinking": false, "llm.model": false, "llm.base_url": false, "llm.limits.max_tool_rounds": false,
		"theme": false, "open.pdf": false, "llm.temperature": false,
	} {
		if got := SecretKey(key); got != want {
			t.Errorf("SecretKey(%q) = %v, want %v", key, got, want)
		}
	}
}

// TestRewriteConfigNeverEchoesSecrets pins A-037-7 on the error paths: for a
// key whose name looks secret, no refusal contains the value that was
// offered, whatever shape of key or value caused it (037 T3).
func TestRewriteConfigNeverEchoesSecrets(t *testing.T) {
	const value = "SUPER-secret-value-123"
	for _, tc := range []struct{ name, src, set string }{
		{"int", "[llm]\nmax_tokens = 1\n", "llm.max_tokens=" + value},
		{"bool", "[web]\nauth_enabled = true\n", "web.auth_enabled=" + value},
		{"float", "[web]\nsecret_ratio = 0.5\n", "web.secret_ratio=" + value},
		{"array", "[web]\napi_keys = [\"a\"]\n", "web.api_keys=" + value},
		{"unknown key", "[web]\nprovider = \"x\"\n", "web.api_key=" + value},
		{"unknown table", "[llm]\nmodel = \"x\"\n", "secrets.api_key=" + value},
		{"no equals sign", "[llm]\nmodel = \"x\"\n", value},
		{"an empty key", "[llm]\nmodel = \"x\"\n", "=" + value},
		{"given twice", "[llm]\napi_key = \"x\"\n", "llm.api_key=" + value},
	} {
		sets := []string{tc.set}
		if tc.name == "given twice" {
			sets = []string{tc.set, "llm.api_key=" + value + "2"}
		}
		_, err := RewriteConfig([]byte(tc.src), sets)
		if err == nil {
			t.Errorf("%s: no error", tc.name)
			continue
		}
		if strings.Contains(err.Error(), value) {
			t.Errorf("%s: the error echoes the value: %v", tc.name, err)
		}
	}
	// A string secret rewrites fine, and the value is in the copy, nowhere else.
	out, err := RewriteConfig([]byte("[llm]\napi_key = \"old\"\n"), []string{"llm.api_key=" + value})
	if err != nil || !strings.Contains(string(out), value) {
		t.Errorf("rewriting a string secret: %v %q", err, out)
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

// TestSweepVariantDirs pins the startup sweep (A-037-7). A variant run that
// was SIGKILLed — which no handler can catch — leaves its 0700 temp
// directory, with a copy of the user's config (and API key) in it, in
// $TMPDIR for good. A new run clears the ones that are the user's own,
// really directories, named like ours, and untouched for over an hour. Every
// condition is a way the sweep could delete something that is not its to
// delete (037 T3).
func TestSweepVariantDirs(t *testing.T) {
	now := time.Now()
	uid := os.Getuid()
	mkdir := func(tmp, name string, age time.Duration) string {
		dir := filepath.Join(tmp, name)
		if err := os.MkdirAll(filepath.Join(dir, "lw"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "lw", "config.toml"), []byte("[llm]\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		when := now.Add(-age)
		if err := os.Chtimes(dir, when, when); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	exists := func(p string) bool { _, err := os.Lstat(p); return err == nil }

	t.Run("removes only stale directories of ours", func(t *testing.T) {
		tmp := t.TempDir()
		stale := mkdir(tmp, "lweval-config-111", 2*time.Hour)
		stale2 := mkdir(tmp, "lweval-config-9", 90*time.Minute)
		fresh := mkdir(tmp, "lweval-config-222", 10*time.Minute)
		justUnder := mkdir(tmp, "lweval-config-333", 59*time.Minute)
		other := mkdir(tmp, "something-else", 5*time.Hour)
		lookalike := mkdir(tmp, "lweval-config-abc", 5*time.Hour)
		prefixOnly := mkdir(tmp, "lweval-config-", 5*time.Hour)
		file := filepath.Join(tmp, "lweval-config-444")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		when := now.Add(-5 * time.Hour)
		if err := os.Chtimes(file, when, when); err != nil {
			t.Fatal(err)
		}

		removed, err := sweepVariantDirs(tmp, now, time.Hour, uid)
		if err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if want := []string{stale, stale2}; !reflect.DeepEqual(removed, want) {
			t.Errorf("removed %v, want %v (sorted by name)", removed, want)
		}
		for _, p := range []string{stale, stale2} {
			if exists(p) {
				t.Errorf("%s is stale and survived", p)
			}
		}
		for _, p := range []string{fresh, justUnder, other, lookalike, prefixOnly, file} {
			if !exists(p) {
				t.Errorf("%s was removed but is not a stale lweval-config dir", p)
			}
		}
	})

	t.Run("a directory owned by someone else is left alone", func(t *testing.T) {
		tmp := t.TempDir()
		stale := mkdir(tmp, "lweval-config-111", 2*time.Hour)
		removed, err := sweepVariantDirs(tmp, now, time.Hour, uid+1)
		if err != nil || len(removed) != 0 || !exists(stale) {
			t.Errorf("removed %v (err %v), exists %v; want nothing removed", removed, err, exists(stale))
		}
	})

	t.Run("a symlink is never followed or removed", func(t *testing.T) {
		tmp := t.TempDir()
		target := mkdir(t.TempDir(), "lweval-config-777", 5*time.Hour)
		link := filepath.Join(tmp, "lweval-config-555")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		// Make everything look old by moving "now" forward.
		removed, err := sweepVariantDirs(tmp, now.Add(10*time.Hour), time.Hour, uid)
		if err != nil || len(removed) != 0 {
			t.Errorf("removed %v (err %v) through a symlink", removed, err)
		}
		if !exists(link) || !exists(filepath.Join(target, "lw", "config.toml")) {
			t.Errorf("the symlink or its target was touched")
		}
	})

	t.Run("a read-only directory inside still goes", func(t *testing.T) {
		tmp := t.TempDir()
		stale := mkdir(tmp, "lweval-config-888", 3*time.Hour)
		if err := os.Chmod(filepath.Join(stale, "lw"), 0o500); err != nil {
			t.Fatal(err)
		}
		if _, err := sweepVariantDirs(tmp, now, time.Hour, uid); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if exists(stale) {
			t.Error("the stale directory with a read-only child survived")
		}
	})

	t.Run("a missing temp directory is not an error", func(t *testing.T) {
		if removed, err := sweepVariantDirs(filepath.Join(t.TempDir(), "gone"), now, time.Hour, uid); err != nil || len(removed) != 0 {
			t.Errorf("removed %v, err %v", removed, err)
		}
	})

	t.Run("the exported sweep reads TMPDIR and the current user", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("TMPDIR", tmp)
		stale := mkdir(tmp, "lweval-config-111", 2*time.Hour)
		fresh := mkdir(tmp, "lweval-config-222", time.Minute)
		removed, err := SweepVariantDirs(now)
		if err != nil || !reflect.DeepEqual(removed, []string{stale}) || exists(stale) || !exists(fresh) {
			t.Errorf("removed %v err %v; stale exists %v fresh exists %v", removed, err, exists(stale), exists(fresh))
		}
	})
}

// TestWithVariantHeartbeat pins that a live variant is never mistaken for a
// stale one: a run longer than the sweep's age (a full N=3 eval can be) keeps
// touching its directory, so another lweval starting meanwhile leaves it
// alone (A-037-7, 037 T3).
func TestWithVariantHeartbeat(t *testing.T) {
	cfg, tmp := varFixture(t)
	old := variantHeartbeat
	variantHeartbeat = 5 * time.Millisecond
	t.Cleanup(func() { variantHeartbeat = old })

	err := WithVariant(context.Background(), cfg, []string{"llm.thinking=on"}, func(env []string) error {
		dir := strings.TrimPrefix(env[0], "XDG_CONFIG_HOME=")
		ancient := time.Now().Add(-3 * time.Hour)
		if err := os.Chtimes(dir, ancient, ancient); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if fi, err := os.Stat(dir); err == nil && time.Since(fi.ModTime()) < time.Minute {
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
		removed, err := sweepVariantDirs(tmp, time.Now(), time.Hour, os.Getuid())
		if err != nil || len(removed) != 0 {
			t.Errorf("a live variant dir was swept: removed %v, err %v", removed, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithVariant: %v", err)
	}
}
