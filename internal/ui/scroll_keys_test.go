// scroll_keys_test.go pins the six content-scrolling bindings (W5 F2/D-3W,
// contract §4): the compiled-in defaults, and a hotkeys.toml rebind.
package ui

import (
	"reflect"
	"testing"

	"charm.land/bubbles/v2/key"
)

func TestScrollKeys(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		setConfigDir(t)
		km, err := LoadKeys()
		if err != nil {
			t.Fatalf("LoadKeys() error = %v", err)
		}
		if len(km.Warnings) != 0 {
			t.Fatalf("Warnings = %v, want none", km.Warnings)
		}

		// Help texts are the contract §4 labels: the merged pair label on
		// the up binding, the singular label on the down one.
		for _, tc := range []struct {
			name     string
			b        key.Binding
			keys     []string
			helpKey  string
			helpDesc string
		}{
			{"ScrollPageUp", km.ScrollPageUp, []string{"pgup"}, "pgup/pgdn", "page"},
			{"ScrollPageDown", km.ScrollPageDown, []string{"pgdown"}, "pgdown", "page down"},
			{"ScrollHalfUp", km.ScrollHalfUp, []string{"ctrl+u"}, "ctrl+u/d", "half page"},
			{"ScrollHalfDown", km.ScrollHalfDown, []string{"ctrl+d"}, "ctrl+d", "half page down"},
			{"ScrollTop", km.ScrollTop, []string{"home"}, "home/end", "top / bottom"},
			{"ScrollBottom", km.ScrollBottom, []string{"end"}, "end", "bottom"},
		} {
			if got := tc.b.Keys(); !reflect.DeepEqual(got, tc.keys) {
				t.Errorf("%s.Keys() = %v, want %v", tc.name, got, tc.keys)
			}
			h := tc.b.Help()
			if h.Key != tc.helpKey || h.Desc != tc.helpDesc {
				t.Errorf("%s.Help() = {%s %s}, want {%s %s}", tc.name, h.Key, h.Desc, tc.helpKey, tc.helpDesc)
			}
		}
	})

	t.Run("hotkeys_toml_overrides", func(t *testing.T) {
		configDir := setConfigDir(t)
		writeConfigFile(t, configDir, "hotkeys.toml", `
scroll_page_down = ["J"]
`)

		km, err := LoadKeys()
		if err != nil {
			t.Fatalf("LoadKeys() error = %v", err)
		}
		if len(km.Warnings) != 0 {
			t.Fatalf("Warnings = %v, want none", km.Warnings)
		}

		if got, want := km.ScrollPageDown.Keys(), []string{"J"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("ScrollPageDown.Keys() = %v, want %v", got, want)
		}
		// The rebind moves the keystroke, not the label: help survives
		// (SetKeys leaves it alone), so the overlay's Scroll group still
		// reads "pgdown page down".
		if got := km.ScrollPageDown.Help(); got.Key != "pgdown" || got.Desc != "page down" {
			t.Fatalf("ScrollPageDown.Help() = {%s %s}, want {pgdown page down}", got.Key, got.Desc)
		}
		// The un-named bindings stayed at their defaults.
		if got, want := km.ScrollPageUp.Keys(), []string{"pgup"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("ScrollPageUp.Keys() = %v, want %v (unrelated key was changed)", got, want)
		}
	})
}
