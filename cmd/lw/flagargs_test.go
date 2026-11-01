package main

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

func TestParseInterspersed(t *testing.T) {
	t.Run("basic interspersed", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		n := fs.Int("n", 0, "")
		pos, err := parseInterspersed(fs, []string{"a", "-n", "3", "b"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if *n != 3 {
			t.Errorf("n = %d, want 3", *n)
		}
		wantPos := []string{"a", "b"}
		if !reflect.DeepEqual(pos, wantPos) {
			t.Errorf("pos = %v, want %v", pos, wantPos)
		}
	})

	t.Run("leading flag", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		n := fs.Int("n", 0, "")
		pos, err := parseInterspersed(fs, []string{"-n", "3", "a"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if *n != 3 {
			t.Errorf("n = %d, want 3", *n)
		}
		wantPos := []string{"a"}
		if !reflect.DeepEqual(pos, wantPos) {
			t.Errorf("pos = %v, want %v", pos, wantPos)
		}
	})

	t.Run("literal double dash after positional", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		n := fs.Int("n", 0, "")
		pos, err := parseInterspersed(fs, []string{"a", "--", "-n"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if *n != 0 {
			t.Errorf("n = %d, want 0", *n)
		}
		wantPos := []string{"a", "-n"}
		if !reflect.DeepEqual(pos, wantPos) {
			t.Errorf("pos = %v, want %v", pos, wantPos)
		}
	})

	t.Run("bool flag after positional", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		b := fs.Bool("b", false, "")
		pos, err := parseInterspersed(fs, []string{"a", "-b", "c"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !*b {
			t.Errorf("b = %v, want true", *b)
		}
		wantPos := []string{"a", "c"}
		if !reflect.DeepEqual(pos, wantPos) {
			t.Errorf("pos = %v, want %v", pos, wantPos)
		}
	})

	t.Run("unknown flag returns error", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		_, err := parseInterspersed(fs, []string{"a", "-unknown", "b"})
		if err == nil {
			t.Fatalf("expected error for unknown flag, got nil")
		}
	})

	t.Run("bare dash is positional", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		n := fs.Int("n", 0, "")
		pos, err := parseInterspersed(fs, []string{"-", "-n", "4", "b"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if *n != 4 {
			t.Errorf("n = %d, want 4", *n)
		}
		wantPos := []string{"-", "b"}
		if !reflect.DeepEqual(pos, wantPos) {
			t.Errorf("pos = %v, want %v", pos, wantPos)
		}
	})
}

func TestVerbsAcceptTrailingFlags(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	tempDir := t.TempDir()

	tests := []struct {
		name      string
		args      []string
		checkFunc func(t *testing.T, stdout, stderr string, code int)
	}{
		{
			name: "search trailing -n",
			args: []string{"search", "attention", "-n", "1", "--vault", root},
			checkFunc: func(t *testing.T, stdout, stderr string, code int) {
				if code != 0 {
					t.Fatalf("search exit %d, stderr: %s", code, stderr)
				}
				// With -n 1 on query "attention", only 1 hit is returned (flash-attention)
				if !strings.Contains(stdout, "flash-attention.md") {
					t.Errorf("expected flash-attention.md in stdout:\n%s", stdout)
				}
				if strings.Contains(stdout, "kv-cache.md") {
					t.Errorf("expected only 1 hit with -n 1, got stdout:\n%s", stdout)
				}
			},
		},
		{
			name: "show trailing --json",
			args: []string{"show", "kv-cache", "--json", "--vault", root},
			checkFunc: func(t *testing.T, stdout, stderr string, code int) {
				if code != 0 {
					t.Fatalf("show exit %d, stderr: %s", code, stderr)
				}
				var parsed map[string]any
				if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
					t.Fatalf("expected valid JSON, got err %v; stdout:\n%s", err, stdout)
				}
				if parsed["path"] != "wiki/concepts/kv-cache.md" {
					t.Errorf("unexpected json path: %v", parsed["path"])
				}
			},
		},
		{
			name: "graph trailing --format json",
			args: []string{"graph", "--vault", root, "--format", "json"},
			checkFunc: func(t *testing.T, stdout, stderr string, code int) {
				if code != 0 {
					t.Fatalf("graph exit %d, stderr: %s", code, stderr)
				}
				var parsed map[string]any
				if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
					t.Fatalf("expected valid JSON from graph, got err %v; stdout:\n%s", err, stdout)
				}
			},
		},
		{
			name: "stats trailing --json",
			args: []string{"stats", "--vault", root, "--json"},
			checkFunc: func(t *testing.T, stdout, stderr string, code int) {
				if code != 0 {
					t.Fatalf("stats exit %d, stderr: %s", code, stderr)
				}
				var parsed map[string]any
				if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
					t.Fatalf("expected valid JSON from stats, got err %v; stdout:\n%s", err, stdout)
				}
			},
		},
		{
			name: "export trailing --force after positionals",
			args: []string{"export", "html", filepath.Join(tempDir, "export1"), "--force", "--vault", root},
			checkFunc: func(t *testing.T, stdout, stderr string, code int) {
				if code != 0 {
					t.Fatalf("export html exit %d, stderr: %s", code, stderr)
				}
				if !strings.Contains(stdout, "exported") {
					t.Errorf("expected 'exported' in stdout, got:\n%s", stdout)
				}
			},
		},
		{
			name: "export leading --force before positionals",
			args: []string{"export", "--force", "html", filepath.Join(tempDir, "export2"), "--vault", root},
			checkFunc: func(t *testing.T, stdout, stderr string, code int) {
				if code != 0 {
					t.Fatalf("export html exit %d, stderr: %s", code, stderr)
				}
				if !strings.Contains(stdout, "exported") {
					t.Errorf("expected 'exported' in stdout, got:\n%s", stdout)
				}
			},
		},
		{
			name: "related trailing -n 1 after page positional",
			args: []string{"related", "kv-cache", "-n", "1", "--vault", root},
			checkFunc: func(t *testing.T, stdout, stderr string, code int) {
				if code != 0 {
					t.Fatalf("related exit %d, stderr: %s", code, stderr)
				}
				if !strings.Contains(stdout, "flash-attention.md") {
					t.Errorf("expected flash-attention.md in related stdout:\n%s", stdout)
				}
				// Should only have 1 match
				if strings.Contains(stdout, "gpt-4.md") {
					t.Errorf("expected only 1 match with -n 1, got stdout:\n%s", stdout)
				}
			},
		},
		{
			name: "dupes trailing --threshold",
			args: []string{"dupes", "--vault", root, "--threshold", "0.01", "-n", "1"},
			checkFunc: func(t *testing.T, stdout, stderr string, code int) {
				if code != 0 {
					t.Fatalf("dupes exit %d, stderr: %s", code, stderr)
				}
			},
		},
		{
			name: "digest trailing -o",
			args: []string{"digest", "--vault", root, "--since", "365d", "-o", filepath.Join(tempDir, "digest.md")},
			checkFunc: func(t *testing.T, stdout, stderr string, code int) {
				if code != 0 {
					t.Fatalf("digest exit %d, stderr: %s", code, stderr)
				}
			},
		},
		{
			name: "cards trailing --format json",
			args: []string{"cards", "--vault", root, "-o", filepath.Join(tempDir, "cards.json"), "--format", "json"},
			checkFunc: func(t *testing.T, stdout, stderr string, code int) {
				if code != 0 {
					t.Fatalf("cards exit %d, stderr: %s", code, stderr)
				}
			},
		},
		{
			name: "completion trailing argument",
			args: []string{"completion", "bash"},
			checkFunc: func(t *testing.T, stdout, stderr string, code int) {
				if code != 0 {
					t.Fatalf("completion exit %d, stderr: %s", code, stderr)
				}
				if !strings.Contains(stdout, "_lw()") {
					t.Errorf("completion output missing _lw():\n%s", stdout)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := captureRun(t, func() int {
				return run(tc.args)
			})
			tc.checkFunc(t, stdout, stderr, code)
		})
	}
}
