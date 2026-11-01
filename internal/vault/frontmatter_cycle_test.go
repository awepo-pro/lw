package vault

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
	"gopkg.in/yaml.v3"
)

func runCycleSubprocess(t *testing.T, envKey, testName string, directFn func()) {
	t.Helper()
	if os.Getenv(envKey) == "1" {
		directFn()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^"+testName+"$", "-test.v", "-test.count=1")
	cmd.Env = append(os.Environ(), envKey+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("subprocess failed: %v\noutput:\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "PASS") {
		t.Fatalf("subprocess did not pass:\n%s", string(out))
	}
	directFn()
}

// E1: cyclic_list: &loop\n  - *loop -> error containing "alias cycle" and "cyclic_list".
func TestFrontmatterCycle_E1_List(t *testing.T) {
	runCycleSubprocess(t, "TEST_E1_SUB", "TestFrontmatterCycle_E1_List", func() {
		in := "---\n" +
			"cyclic_list: &loop\n" +
			"  - *loop\n" +
			"---\n\n" +
			"body\n"
		_, _, err := ParseFrontmatter([]byte(in))
		if err == nil {
			t.Fatalf("expected error for cyclic list, got nil")
		}
		if !strings.Contains(err.Error(), "alias cycle") {
			t.Errorf("expected error containing 'alias cycle', got: %v", err)
		}
		if !strings.Contains(err.Error(), "cyclic_list") {
			t.Errorf("expected error containing 'cyclic_list', got: %v", err)
		}
	})
}

// E2: cyclic_map: &m\n  self: *m -> error containing "alias cycle" and "cyclic_map".
func TestFrontmatterCycle_E2_Map(t *testing.T) {
	runCycleSubprocess(t, "TEST_E2_SUB", "TestFrontmatterCycle_E2_Map", func() {
		in := "---\n" +
			"cyclic_map: &m\n" +
			"  self: *m\n" +
			"---\n\n" +
			"body\n"
		_, _, err := ParseFrontmatter([]byte(in))
		if err == nil {
			t.Fatalf("expected error for cyclic map, got nil")
		}
		if !strings.Contains(err.Error(), "alias cycle") {
			t.Errorf("expected error containing 'alias cycle', got: %v", err)
		}
		if !strings.Contains(err.Error(), "cyclic_map") {
			t.Errorf("expected error containing 'cyclic_map', got: %v", err)
		}
	})
}

// E3: a reused anchor (base: &b {k: v} and other: [*b, *b]) -> no error,
// the rewritten page keeps both keys and other expands to two {k: v} entries.
func TestFrontmatterCycle_E3_ReusedAnchor(t *testing.T) {
	in := "---\n" +
		"base: &b {k: v}\n" +
		"other: [*b, *b]\n" +
		"---\n\n" +
		"body\n"
	fm, _, err := ParseFrontmatter([]byte(in))
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}
	if fm.ExtraYAML["base"] == "" {
		t.Fatalf("fm.ExtraYAML[base] is empty")
	}
	if fm.ExtraYAML["other"] == "" {
		t.Fatalf("fm.ExtraYAML[other] is empty")
	}

	encoded := fm.Encode()
	var m map[string]any
	if err := yaml.Unmarshal(encoded, &m); err != nil {
		t.Fatalf("yaml.Unmarshal: %v\nencoded:\n%s", err, string(encoded))
	}
	wantBase := map[string]any{"k": "v"}
	if !reflect.DeepEqual(m["base"], wantBase) {
		t.Errorf("m[base] = %#v, want %#v", m["base"], wantBase)
	}
	wantOther := []any{
		map[string]any{"k": "v"},
		map[string]any{"k": "v"},
	}
	if !reflect.DeepEqual(m["other"], wantOther) {
		t.Errorf("m[other] = %#v, want %#v", m["other"], wantOther)
	}
}

// E4: 101 nested flow lists -> error containing "too deeply nested"; 99 nested -> no error.
func TestFrontmatterCycle_E4_DepthLimit(t *testing.T) {
	// 101 nested flow lists -> error containing "too deeply nested"
	in101 := "---\n" +
		"list: " + strings.Repeat("[", 101) + strings.Repeat("]", 101) + "\n" +
		"---\n\n" +
		"body\n"
	_, _, err101 := ParseFrontmatter([]byte(in101))
	if err101 == nil {
		t.Fatalf("expected error for 101 nested flow lists, got nil")
	}
	if !strings.Contains(err101.Error(), "too deeply nested") {
		t.Errorf("expected error containing 'too deeply nested', got: %v", err101)
	}

	// 99 nested flow lists -> no error
	in99 := "---\n" +
		"list: " + strings.Repeat("[", 99) + strings.Repeat("]", 99) + "\n" +
		"---\n\n" +
		"body\n"
	_, _, err99 := ParseFrontmatter([]byte(in99))
	if err99 != nil {
		t.Fatalf("expected no error for 99 nested flow lists, got: %v", err99)
	}
}

// E5: open a vault (vault.Open on a t.TempDir copy of a minimal vault)
// holding one page with E1's frontmatter -> Open succeeds, that page is in ParseErrors, the other pages load.
func TestFrontmatterCycle_E5_VaultOpenParseError(t *testing.T) {
	runCycleSubprocess(t, "TEST_E5_SUB", "TestFrontmatterCycle_E5_VaultOpenParseError", func() {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		t.Setenv("LW_VAULT", "")
		dir := testutil.CopyFixture(t, "minimal")
		cyclicContent := "---\n" +
			"title: Cyclic Page\n" +
			"created: 2026-08-01\n" +
			"updated: 2026-08-01\n" +
			"type: concept\n" +
			"tags: [alpha]\n" +
			"cyclic_list: &loop\n" +
			"  - *loop\n" +
			"---\n\n" +
			"# Cyclic Page\n\nBody.\n"
		cyclicPath := filepath.Join(dir, "wiki", "concepts", "cyclic.md")
		if err := os.WriteFile(cyclicPath, []byte(cyclicContent), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		v, err := Open(dir)
		if err != nil {
			t.Fatalf("Open failed: %v", err)
		}
		if got := len(v.Pages()); got != 4 {
			t.Errorf("len(Pages()) = %d, want 4", got)
		}
		parseErrs := v.ParseErrors()
		if len(parseErrs) == 0 {
			t.Fatalf("expected ParseErrors, got none")
		}
		found := false
		for _, pe := range parseErrs {
			if strings.Contains(pe.Path, "cyclic.md") {
				found = true
				if pe.Err == nil {
					t.Errorf("pe.Err is nil for cyclic.md")
					continue
				}
				errMsg := pe.Err.Error()
				if !strings.Contains(errMsg, "alias cycle") || !strings.Contains(errMsg, "cyclic_list") {
					t.Errorf("ParseError for cyclic.md should contain 'alias cycle' and 'cyclic_list', got: %v", errMsg)
				}
			}
		}
		if !found {
			t.Errorf("expected cyclic.md in ParseErrors, got: %v", parseErrs)
		}
	})
}
