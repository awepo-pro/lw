// cmd_config_open_test.go pins 034 T5's [open] seam on the CLI: open.pdf is
// settable like every other key, an empty value is refused with the fix
// spelled out, and the show table says (unset) while no viewer is
// configured. Runs the real dispatch path against a temp config dir, the
// same way cmd_config_test.go does.
package main

import (
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/config"
)

// TestConfigSetOpenPDF pins `lw config set open.pdf` (034 T5).
func TestConfigSetOpenPDF(t *testing.T) {
	t.Run("round_trips_through_the_file", func(t *testing.T) {
		configTestEnv(t)
		const want = "papers -i {page} {file}"
		stdout, stderr, code := runConfig(t, "set", "open.pdf", want)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		if !strings.Contains(stdout, "open.pdf set to "+want) {
			t.Fatalf("stdout = %q, want the confirmation to echo the template", stdout)
		}
		got, err := config.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got.Open.PDF != want {
			t.Fatalf("Open.PDF = %q, want %q (Save must round-trip the [open] table)", got.Open.PDF, want)
		}

		// `lw config` shows the row as (file), like every other set key.
		stdout, _, code = runConfig(t)
		if code != 0 {
			t.Fatalf("show: exit code = %d, want 0", code)
		}
		wantRow(t, stdout, "open.pdf", want, "(file)")
	})

	t.Run("empty_value_refused", func(t *testing.T) {
		configTestEnv(t)
		_, stderr, code := runConfig(t, "set", "open.pdf", "")
		if code != 2 {
			t.Fatalf("exit code = %d, want 2; stderr=%q", code, stderr)
		}
		if !strings.Contains(stderr, `open.pdf: want a viewer command, e.g. "papers -i {page} {file}" or "mupdf {file} {page}"`) {
			t.Fatalf("stderr = %q, want the refusal to carry the fix", stderr)
		}
	})

	t.Run("blank_value_refused", func(t *testing.T) {
		configTestEnv(t)
		_, stderr, code := runConfig(t, "set", "open.pdf", "   ")
		if code != 2 {
			t.Fatalf("exit code = %d, want 2; stderr=%q", code, stderr)
		}
		if !strings.Contains(stderr, "open.pdf: want a viewer command") {
			t.Fatalf("stderr = %q, want it to name open.pdf", stderr)
		}
	})

	t.Run("absent_shows_unset", func(t *testing.T) {
		configTestEnv(t)
		stdout, _, code := runConfig(t)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		wantRow(t, stdout, "open.pdf", "(unset)", "(default)")
	})

	t.Run("usage_lists_the_key", func(t *testing.T) {
		configTestEnv(t)
		_, stderr, code := runConfig(t, "set", "open.pd", "x")
		if code != 2 {
			t.Fatalf("exit code = %d, want 2", code)
		}
		if !strings.Contains(stderr, "open.pdf") {
			t.Errorf("usage on stderr = %q, want it to list open.pdf", stderr)
		}
	})
}
