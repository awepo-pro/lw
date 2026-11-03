package hubserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/hub"
)

func TestDevicesStoreDirect(t *testing.T) {
	dir := t.TempDir()
	devPath := filepath.Join(dir, "devices.json")

	// 1. Missing file = empty store
	d, err := OpenDevices(devPath)
	if err != nil {
		t.Fatalf("OpenDevices failed: %v", err)
	}
	list, err := d.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty list, got %d", len(list))
	}

	// 2. Register devices
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	r1, err := d.RegisterPending("id-11111111", "Pixel 8 Pro!", "user@test", "100.1.2.3", now)
	if err != nil {
		t.Fatalf("RegisterPending failed: %v", err)
	}
	if r1.Slug != "pixel-8-pro" || r1.State != hub.DevicePending {
		t.Fatalf("unexpected r1: %+v", r1)
	}

	// Mode 0600
	fi, err := os.Stat(devPath)
	if err != nil {
		t.Fatalf("os.Stat failed: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Fatalf("expected mode 0600, got %04o", perm)
	}

	// Duplicate name creates -2 slug
	r2, err := d.RegisterPending("id-22222222", "Pixel 8 Pro!", "user@test", "100.1.2.4", now)
	if err != nil {
		t.Fatalf("RegisterPending 2 failed: %v", err)
	}
	if r2.Slug != "pixel-8-pro-2" {
		t.Fatalf("expected pixel-8-pro-2, got %q", r2.Slug)
	}

	// Third device: custom name
	r3, err := d.RegisterPending("id-33333333", "Alpha", "user@test", "100.1.2.5", now)
	if err != nil {
		t.Fatalf("RegisterPending 3 failed: %v", err)
	}
	if r3.Slug != "alpha" {
		t.Fatalf("expected alpha, got %q", r3.Slug)
	}

	// List sorted by slug: alpha, pixel-8-pro, pixel-8-pro-2
	list, err = d.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 devices, got %d", len(list))
	}
	if list[0].Slug != "alpha" || list[1].Slug != "pixel-8-pro" || list[2].Slug != "pixel-8-pro-2" {
		t.Fatalf("incorrect sort order: %+v", list)
	}

	// Approve by ID
	app1, err := d.Approve("id-11111111")
	if err != nil {
		t.Fatalf("Approve by ID failed: %v", err)
	}
	if app1.State != hub.DeviceApproved {
		t.Fatalf("expected approved state, got %s", app1.State)
	}

	// Approve by Slug
	app2, err := d.Approve("pixel-8-pro-2")
	if err != nil {
		t.Fatalf("Approve by Slug failed: %v", err)
	}
	if app2.State != hub.DeviceApproved {
		t.Fatalf("expected approved state, got %s", app2.State)
	}

	// Revoke by ID
	rev1, err := d.Revoke("id-11111111")
	if err != nil {
		t.Fatalf("Revoke by ID failed: %v", err)
	}
	if rev1.State != hub.DeviceRevoked {
		t.Fatalf("expected revoked state, got %s", rev1.State)
	}

	// Revoke by Slug
	rev2, err := d.Revoke("pixel-8-pro-2")
	if err != nil {
		t.Fatalf("Revoke by Slug failed: %v", err)
	}
	if rev2.State != hub.DeviceRevoked {
		t.Fatalf("expected revoked state, got %s", rev2.State)
	}

	// Error on unknown device
	_, err = d.Approve("non-existent")
	if err == nil || !strings.Contains(err.Error(), "non-existent") {
		t.Fatalf("expected error naming input 'non-existent', got %v", err)
	}
	_, err = d.Revoke("missing-device")
	if err == nil || !strings.Contains(err.Error(), "missing-device") {
		t.Fatalf("expected error naming input 'missing-device', got %v", err)
	}
}

func TestSlugRules(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"Pixel 8 Pro!", "pixel-8-pro"},
		{"phone", "phone"},
		{"", "device"},
		{"   ---   ", "device"},
		{"!!!@@@###", "device"},
		{"A-B-C", "a-b-c"},
		{"Long device name with many words and letters", "long-device-name-with-ma"},
	}

	for _, tc := range cases {
		got := cleanSlug(tc.input)
		if got != tc.expected {
			t.Errorf("cleanSlug(%q) = %q, expected %q", tc.input, got, tc.expected)
		}
		if len(got) > 24 {
			t.Errorf("cleanSlug(%q) length %d > 24", tc.input, len(got))
		}
	}
}
