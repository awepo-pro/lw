package main

// main_test.go holds the package's one TestMain (A-059-2).
//
// Since 042 the developer's real ~/.config/lw/config.toml carries a [vault]
// path and a [sync] remotes list, and almost every verb reads it: a writing
// verb that finds remotes but a vault outside lw sync adds a "not syncing"
// line to stderr (eight existing cmd/lw tests assert stderr byte for byte and
// failed on that alone), and a test that forgets --vault resolves the
// developer's REAL vault through [vault] path and, with auto-sync on, pushes
// it. So the whole process runs against an empty config directory. Tests that
// need a config t.Setenv their own XDG_CONFIG_HOME and keep working; HOME is
// left alone because go, git and docling are found through it.

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "lw-test-config-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: %v\n", err)
		os.Exit(1)
	}
	os.Setenv("XDG_CONFIG_HOME", dir)
	os.Unsetenv("LW_VAULT")
	code := m.Run()
	os.RemoveAll(dir) // os.Exit skips defers
	os.Exit(code)
}
