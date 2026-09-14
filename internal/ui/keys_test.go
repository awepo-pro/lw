package ui

import (
	"reflect"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

func TestLoadKeysDefaultsWithNoFilePresent(t *testing.T) {
	setConfigDir(t)

	km, err := LoadKeys()
	if err != nil {
		t.Fatalf("LoadKeys() error = %v", err)
	}
	if len(km.Warnings) != 0 {
		t.Fatalf("Warnings = %v, want none", km.Warnings)
	}

	// Every key /PLAN.md §9 fixes for review, plus the shell keys, is bound.
	cases := []struct {
		name string
		b    interface{ Keys() []string }
		want []string
	}{
		{"AcceptHunk", km.AcceptHunk, []string{"y"}},
		{"DropHunk", km.DropHunk, []string{"n"}},
		{"SplitHunk", km.SplitHunk, []string{"s"}},
		{"AcceptAll", km.AcceptAll, []string{"A"}},
		{"Reject", km.Reject, []string{"X"}},
		{"Commit", km.Commit, []string{"C"}},
		{"MoveDown", km.MoveDown, []string{"j", "down"}},
		{"MoveUp", km.MoveUp, []string{"k", "up"}},
		{"Top", km.Top, []string{"g"}},
		{"Bottom", km.Bottom, []string{"G"}},
		{"NextPane", km.NextPane, []string{"tab"}},
		{"Quit", km.Quit, []string{"q", "ctrl+c"}},
		{"Help", km.Help, []string{"?"}},
	}
	for _, c := range cases {
		if got := c.b.Keys(); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s.Keys() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestLoadKeysPartialOverrideOnlyChangesNamedKeys(t *testing.T) {
	configDir := setConfigDir(t)
	writeConfigFile(t, configDir, "hotkeys.toml", `
quit = ["Q"]
`)

	km, err := LoadKeys()
	if err != nil {
		t.Fatalf("LoadKeys() error = %v", err)
	}
	if len(km.Warnings) != 0 {
		t.Fatalf("Warnings = %v, want none", km.Warnings)
	}

	if got, want := km.Quit.Keys(), []string{"Q"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Quit.Keys() = %v, want %v", got, want)
	}
	// Everything else stayed at its default.
	if got, want := km.AcceptHunk.Keys(), []string{"y"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("AcceptHunk.Keys() = %v, want %v (unrelated key was changed)", got, want)
	}
	if got, want := km.MoveDown.Keys(), []string{"j", "down"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("MoveDown.Keys() = %v, want %v (unrelated key was changed)", got, want)
	}
	// Help text for the overridden binding survives (SetKeys leaves it alone).
	if got, want := km.Quit.Help().Desc, "quit"; got != want {
		t.Fatalf("Quit.Help().Desc = %q, want %q", got, want)
	}
}

func TestLoadKeysUnknownKeyWarnsButStillUsable(t *testing.T) {
	configDir := setConfigDir(t)
	writeConfigFile(t, configDir, "hotkeys.toml", `
quit = ["Q"]
totally_bogus_key = ["z"]
`)

	km, err := LoadKeys()
	if err != nil {
		t.Fatalf("LoadKeys() error = %v", err)
	}
	if len(km.Warnings) != 1 {
		t.Fatalf("Warnings = %v, want exactly one", km.Warnings)
	}
	if !strings.Contains(km.Warnings[0], "totally_bogus_key") {
		t.Fatalf("Warnings[0] = %q, want it to name the unknown key", km.Warnings[0])
	}

	if got, want := km.Quit.Keys(), []string{"Q"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Quit.Keys() = %v, want %v (override should still apply)", got, want)
	}
	if !km.AcceptHunk.Enabled() {
		t.Fatal("AcceptHunk is disabled; KeyMap was not usable after a warning")
	}
}

func TestLoadKeysMissingConfigDirEntirelyIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir+"/does-not-exist")

	km, err := LoadKeys()
	if err != nil {
		t.Fatalf("LoadKeys() error = %v, want nil for a missing config dir", err)
	}
	if len(km.Warnings) != 0 {
		t.Fatalf("Warnings = %v, want none", km.Warnings)
	}
}

// TestLoadKeysRebindsMoveKeys is S6-T4's proof that hotkeys.toml actually
// rebinds — the stage file's worked example, `j`/`k` moved onto `n`/`p` —
// and that the rebinding is visible to the matcher screens use
// (key.Matches against a tea.KeyPressMsg), not just to Keys().
func TestLoadKeysRebindsMoveKeys(t *testing.T) {
	configDir := setConfigDir(t)
	writeConfigFile(t, configDir, "hotkeys.toml", `
move_down = ["n"]
move_up = ["p"]
`)

	km, err := LoadKeys()
	if err != nil {
		t.Fatalf("LoadKeys() error = %v", err)
	}
	if len(km.Warnings) != 0 {
		t.Fatalf("Warnings = %v, want none", km.Warnings)
	}

	if got, want := km.MoveDown.Keys(), []string{"n"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("MoveDown.Keys() = %v, want %v", got, want)
	}
	if got, want := km.MoveUp.Keys(), []string{"p"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("MoveUp.Keys() = %v, want %v", got, want)
	}

	// The binding a screen matches with must now fire on the new key...
	down := tea.KeyPressMsg{Code: 'n', Text: "n"}
	up := tea.KeyPressMsg{Code: 'p', Text: "p"}
	if !key.Matches(down, km.MoveDown) {
		t.Error("key.Matches(n, MoveDown) = false, want true after the rebind")
	}
	if !key.Matches(up, km.MoveUp) {
		t.Error("key.Matches(p, MoveUp) = false, want true after the rebind")
	}
	// ...and not on the old one, nor on each other's key.
	if key.Matches(tea.KeyPressMsg{Code: 'j', Text: "j"}, km.MoveDown) {
		t.Error("key.Matches(j, MoveDown) = true, want false after the rebind")
	}
	if key.Matches(down, km.MoveUp) || key.Matches(up, km.MoveDown) {
		t.Error("n and p both match one binding; the rebind crossed over")
	}

	// Help text survives the rebind, so the footer still says what the
	// action is rather than what key it used to be on.
	if got, want := km.MoveDown.Help().Desc, "down"; got != want {
		t.Fatalf("MoveDown.Help().Desc = %q, want %q", got, want)
	}
}
