package main

import (
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

func TestExportUsage(t *testing.T) {
	cases := [][]string{
		{"export"},
		{"export", "pdf"},
		{"export", "html"},
	}
	for _, tc := range cases {
		_, _, code := captureRun(t, func() int {
			return run(tc)
		})
		if code != 2 {
			t.Errorf("run(%v) = %d, want exit 2", tc, code)
		}
	}
}

func TestExportSuccess(t *testing.T) {
	fixtureDir := testutil.CopyFixture(t, "minimal")
	outDir := t.TempDir()

	before := snapshotVault(t, fixtureDir)

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"export", "html", "--vault", fixtureDir, outDir})
	})
	if code != 0 {
		t.Fatalf("run export html returned %d (stderr: %s)", code, stderr)
	}
	if !strings.Contains(stdout, "exported") {
		t.Errorf("stdout does not contain 'exported': %s", stdout)
	}
	if !strings.Contains(stdout, "open") {
		t.Errorf("stdout does not contain 'open': %s", stdout)
	}

	assertVaultUnchanged(t, fixtureDir, before)
}
