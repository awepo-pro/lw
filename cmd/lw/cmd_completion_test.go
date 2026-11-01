package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompletion_Bash(t *testing.T) {
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"completion", "bash"})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr)
	}

	if !strings.Contains(stdout, "complete -F _lw lw") {
		t.Errorf("stdout does not contain 'complete -F _lw lw'")
	}

	for _, v := range verbs {
		if !strings.Contains(stdout, v.name) {
			t.Errorf("bash completion script missing verb %q", v.name)
		}
	}

	// If bash is on PATH, run bash -n on the generated script
	if bashPath, err := exec.LookPath("bash"); err == nil && bashPath != "" {
		tmpDir := t.TempDir()
		scriptFile := filepath.Join(tmpDir, "completion.bash")
		if err := os.WriteFile(scriptFile, []byte(stdout), 0o644); err != nil {
			t.Fatalf("write script: %v", err)
		}
		cmd := exec.Command(bashPath, "-n", scriptFile)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("bash -n failed: %v\nOutput:\n%s", err, string(out))
		}
	}
}

func TestCompletion_Zsh(t *testing.T) {
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"completion", "zsh"})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr)
	}

	if !strings.HasPrefix(stdout, "#compdef lw") {
		t.Errorf("zsh completion does not start with '#compdef lw', starts with: %q", stdout[:min(len(stdout), 50)])
	}

	for _, v := range verbs {
		if !strings.Contains(stdout, v.name) {
			t.Errorf("zsh completion script missing verb %q", v.name)
		}
	}
}

func TestCompletion_Fish(t *testing.T) {
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"completion", "fish"})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr)
	}

	if !strings.Contains(stdout, "complete -c lw") {
		t.Errorf("fish completion does not contain 'complete -c lw'")
	}

	for _, v := range verbs {
		if !strings.Contains(stdout, v.name) {
			t.Errorf("fish completion script missing verb %q", v.name)
		}
	}
}

func TestCompletion_Errors(t *testing.T) {
	t.Run("no arg", func(t *testing.T) {
		_, _, code := captureRun(t, func() int {
			return run([]string{"completion"})
		})
		if code != 2 {
			t.Errorf("exit code = %d, want 2", code)
		}
	})

	t.Run("tcsh", func(t *testing.T) {
		_, _, code := captureRun(t, func() int {
			return run([]string{"completion", "tcsh"})
		})
		if code != 2 {
			t.Errorf("exit code = %d, want 2", code)
		}
	})
}
