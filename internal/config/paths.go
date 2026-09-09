package config

import (
	"os"
	"path/filepath"
)

// ConfigDir returns the directory lw's configuration lives in:
// $XDG_CONFIG_HOME/lw when that variable is set and non-empty, else
// ~/.config/lw.
func ConfigDir() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "lw")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "lw")
}

// DataDir returns the directory lw's data files live in:
// $XDG_DATA_HOME/lw when that variable is set and non-empty, else
// ~/.local/share/lw.
func DataDir() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "lw")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "lw")
}
