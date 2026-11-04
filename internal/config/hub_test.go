package config

import (
	"reflect"
	"testing"
)

func TestHubConfig(t *testing.T) {
	t.Run("absent hub table", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)
		writeConfig(t, dir, preSyncDefaultBytes)
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.Hub != nil {
			t.Fatalf("Load invented hub: %+v", c.Hub)
		}
		if err := c.Save(); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if got := configBytes(t, dir); got != preSyncDefaultBytes {
			t.Errorf("round trip mutated file without hub:\n got:\n%s\n want:\n%s", got, preSyncDefaultBytes)
		}
	})

	t.Run("port and users parse and round trip", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)
		toml := preSyncDefaultBytes + "\n[hub]\nport = 8780\nusers = [\"a@b\"]\n"
		writeConfig(t, dir, toml)
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.Hub == nil {
			t.Fatalf("Hub is nil")
		}
		if c.Hub.Port != 8780 {
			t.Errorf("Port = %d, want 8780", c.Hub.Port)
		}
		if want := []string{"a@b"}; !reflect.DeepEqual(c.Hub.Users, want) {
			t.Errorf("Users = %v, want %v", c.Hub.Users, want)
		}
		if err := c.Save(); err != nil {
			t.Fatalf("Save: %v", err)
		}
		again, err := Load()
		if err != nil {
			t.Fatalf("Load again: %v", err)
		}
		if !reflect.DeepEqual(again.Hub, c.Hub) {
			t.Errorf("round trip changed Hub: got %+v, want %+v", again.Hub, c.Hub)
		}
	})

	t.Run("default port when omitted", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)
		toml := preSyncDefaultBytes + "\n[hub]\nusers = [\"phone\"]\n"
		writeConfig(t, dir, toml)
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.Hub == nil {
			t.Fatalf("Hub is nil")
		}
		if c.Hub.Port != 8779 {
			t.Errorf("Port = %d, want 8779", c.Hub.Port)
		}
	})
}
