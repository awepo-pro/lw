package config

// open_test.go pins the [open] table end to end (034 T5): the viewer
// template compiler (Open.Argv), the empty-template refusal, and the
// load/save/merge treatment of a table that ships absent — the [extract]
// pattern (007 T3, F.X1–F.X4) with {file}/{page} substitution in place of
// an argv prefix. The frozen regression test ships with the subtask and
// stays forever (D-10C).

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// TestOpenArgv pins Open.Argv, the function that turns the open.pdf
// template into the argv a PDF viewer is exec'd with (034 T5).
func TestOpenArgv(t *testing.T) {
	t.Run("page_then_file", func(t *testing.T) {
		got, err := (Open{PDF: "papers -i {page} {file}"}).Argv("/v/raw/papers/x.pdf", 12)
		if err != nil {
			t.Fatalf("Argv: %v", err)
		}
		want := []string{"papers", "-i", "12", "/v/raw/papers/x.pdf"}
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("Argv() = %q, want %q", got, want)
		}
	})

	t.Run("file_then_page", func(t *testing.T) {
		got, err := (Open{PDF: "mupdf {file} {page}"}).Argv("/v/raw/papers/x.pdf", 12)
		if err != nil {
			t.Fatalf("Argv: %v", err)
		}
		want := []string{"mupdf", "/v/raw/papers/x.pdf", "12"}
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("Argv() = %q, want %q", got, want)
		}
	})

	t.Run("no_file_placeholder_appends", func(t *testing.T) {
		// A template with no {file} field gets the file appended, in the
		// argv order the template names — zathura takes the document last.
		got, err := (Open{PDF: "zathura"}).Argv("/v/raw/papers/x.pdf", 12)
		if err != nil {
			t.Fatalf("Argv: %v", err)
		}
		want := []string{"zathura", "/v/raw/papers/x.pdf"}
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("Argv() = %q, want %q", got, want)
		}
	})

	t.Run("placeholders_inside_fields", func(t *testing.T) {
		// Substitution happens inside a field, not on whole fields only:
		// viewers that spell the page as an option value (--page=12) are
		// exactly the one-line configs 034 T5 wants to be enough.
		got, err := (Open{PDF: "evince --page-label={page} {file}"}).Argv("/v/raw/papers/x.pdf", 7)
		if err != nil {
			t.Fatalf("Argv: %v", err)
		}
		want := []string{"evince", "--page-label=7", "/v/raw/papers/x.pdf"}
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("Argv() = %q, want %q", got, want)
		}
	})

	t.Run("page_below_one_becomes_one", func(t *testing.T) {
		// An unpaged marker reaches the picker as page 0; the viewer still
		// needs a real page, and 1 is the only honest default (the first
		// physical page), not a silent skip of the substitution.
		got, err := (Open{PDF: "papers -i {page} {file}"}).Argv("/v/raw/papers/x.pdf", 0)
		if err != nil {
			t.Fatalf("Argv: %v", err)
		}
		want := []string{"papers", "-i", "1", "/v/raw/papers/x.pdf"}
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("Argv() = %q, want %q", got, want)
		}
	})

	t.Run("empty_template", func(t *testing.T) {
		for _, pdf := range []string{"", "   "} {
			got, err := (Open{PDF: pdf}).Argv("/v/raw/papers/x.pdf", 12)
			if !errors.Is(err, ErrNoPDFViewer) {
				t.Fatalf("Open{PDF:%q}.Argv() error = %v, want ErrNoPDFViewer", pdf, err)
			}
			if got != nil {
				t.Fatalf("Open{PDF:%q}.Argv() = %q, want nil argv with the error", pdf, got)
			}
		}
	})
}

// TestOpenConfigTable pins the [open] table's load/save/merge treatment:
// absent from Default(), merged per key like [extract], written only when
// the user set it, and readable back into a working Argv.
func TestOpenConfigTable(t *testing.T) {
	t.Run("defaults_absent", func(t *testing.T) {
		if got := (Default().Open); got != (Open{}) {
			t.Fatalf("Default().Open = %+v, want the zero Open (no viewer ships)", got)
		}
		withConfigDir(t)
		got, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got.Open != (Open{}) {
			t.Fatalf("Load() with no config file: Open = %+v, want the zero Open", got.Open)
		}
	})

	t.Run("file_value_loaded", func(t *testing.T) {
		dir := withConfigDir(t)
		writeConfig(t, dir, `[open]
pdf = "papers -i {page} {file}"
`)
		got, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got.Open.PDF != "papers -i {page} {file}" {
			t.Fatalf("Load() Open.PDF = %q, want the file's template", got.Open.PDF)
		}
	})

	t.Run("partial_file_keeps_default_absent", func(t *testing.T) {
		// A file that never names [open] is not a file that zeroed it — the
		// [extract] merge rule (F.X2), pinned against mergeOverDefault
		// directly so the merge itself is what is under test.
		dir := withConfigDir(t)
		writeConfig(t, dir, "model = \"x\"\n")
		got, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got.Open != (Open{}) {
			t.Fatalf("Load() Open = %+v, want the zero Open", got.Open)
		}
		var s shadowConfig
		md, err := toml.Decode("model = \"x\"\n", &s)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		merged := mergeOverDefault(Default(), fromShadow(s), md)
		if merged.Open != (Open{}) {
			t.Fatalf("mergeOverDefault with no [open] keys: Open = %+v, want zero", merged.Open)
		}
	})

	t.Run("round_trips_through_the_file", func(t *testing.T) {
		dir := withConfigDir(t)
		writeConfig(t, dir, `[llm]
model = "custom-model"

[open]
pdf = "papers -i {page} {file}"
`)
		got, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if err := got.Save(); err != nil {
			t.Fatalf("Save: %v", err)
		}
		b, err := os.ReadFile(filepath.Join(dir, "lw", "config.toml"))
		if err != nil {
			t.Fatalf("read config file: %v", err)
		}
		for _, want := range []string{"[open]", `pdf = "papers -i {page} {file}"`} {
			if !strings.Contains(string(b), want) {
				t.Fatalf("saved config lost %s:\n%s", want, b)
			}
		}
		again, err := Load()
		if err != nil {
			t.Fatalf("Load after Save: %v", err)
		}
		if again.Open.PDF != "papers -i {page} {file}" {
			t.Fatalf("Open round trip mismatch: got %q", again.Open.PDF)
		}
	})

	t.Run("empty_open_not_written", func(t *testing.T) {
		// A config that never named a viewer must not gain an [open] table
		// on Save — the stall_timeout omitempty rule (026 T3, F.K3) carried
		// up to the whole table, so keyless configs keep round-tripping
		// byte-identically.
		dir := withConfigDir(t)
		writeConfig(t, dir, "model = \"custom-model\"\n")
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
		if strings.Contains(string(b), "[open]") || strings.Contains(string(b), "pdf") {
			t.Fatalf("Save wrote an [open] table for an unset viewer:\n%s", b)
		}
		// And Save is a byte fixed point over the whole file, not merely
		// [open]-free on the first pass (review pass, 034 T5).
		cfg2, err := Load()
		if err != nil {
			t.Fatalf("re-Load: %v", err)
		}
		if err := cfg2.Save(); err != nil {
			t.Fatalf("second Save: %v", err)
		}
		b2, err := os.ReadFile(filepath.Join(dir, "lw", "config.toml"))
		if err != nil {
			t.Fatalf("re-read config file: %v", err)
		}
		if string(b2) != string(b) {
			t.Fatalf("Save is not a byte fixed point:\nfirst:\n%s\nsecond:\n%s", b, b2)
		}
	})
}
