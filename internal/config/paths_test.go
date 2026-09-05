package config

import (
	"path/filepath"
	"testing"
)

func TestConfigDirXDG(t *testing.T) {
	t.Run("honours XDG_CONFIG_HOME when set and non-empty", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "/xdg/config")
		if got, want := ConfigDir(), filepath.Join("/xdg/config", "lw"); got != want {
			t.Fatalf("ConfigDir() = %q, want %q", got, want)
		}
	})

	t.Run("falls back to ~/.config/lw when XDG_CONFIG_HOME is empty", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", "/home/tester")
		if got, want := ConfigDir(), filepath.Join("/home/tester", ".config", "lw"); got != want {
			t.Fatalf("ConfigDir() = %q, want %q", got, want)
		}
	})
}

func TestDataDirXDG(t *testing.T) {
	t.Run("honours XDG_DATA_HOME when set and non-empty", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", "/xdg/data")
		if got, want := DataDir(), filepath.Join("/xdg/data", "lw"); got != want {
			t.Fatalf("DataDir() = %q, want %q", got, want)
		}
	})

	t.Run("falls back to ~/.local/share/lw when XDG_DATA_HOME is empty", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", "")
		t.Setenv("HOME", "/home/tester")
		if got, want := DataDir(), filepath.Join("/home/tester", ".local", "share", "lw"); got != want {
			t.Fatalf("DataDir() = %q, want %q", got, want)
		}
	})
}
