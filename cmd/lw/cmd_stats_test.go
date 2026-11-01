package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vaultstats"
)

func TestStats_FixtureCopy(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	before := snapshotVault(t, root)

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"stats", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("run stats exit code = %d, want 0; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "pages") {
		t.Errorf("stdout does not contain 'pages':\n%s", stdout)
	}
	if !strings.Contains(stdout, "distinct page pairs") {
		t.Errorf("stdout does not contain 'distinct page pairs':\n%s", stdout)
	}

	assertVaultUnchanged(t, root, before)
}

func TestStats_JSON(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"stats", "--vault", root, "--json"})
	})
	if code != 0 {
		t.Fatalf("run stats --json exit code = %d, want 0; stderr:\n%s", code, stderr)
	}

	var s vaultstats.Stats
	if err := json.Unmarshal([]byte(stdout), &s); err != nil {
		t.Fatalf("failed to parse JSON stats: %v\nstdout was:\n%s", err, stdout)
	}
	if s.Pages == 0 {
		t.Errorf("s.Pages = 0 in minimal fixture")
	}
	if s.DistinctEdges == 0 {
		t.Errorf("s.DistinctEdges = 0 in minimal fixture")
	}

	var rawJSON map[string]any
	if err := json.Unmarshal([]byte(stdout), &rawJSON); err != nil {
		t.Fatal(err)
	}
	if _, ok := rawJSON["distinct_edges"]; !ok {
		t.Errorf("JSON output missing 'distinct_edges' key")
	}
}

func TestStats_StaleDaysInvalid(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	_, stderr, code := captureRun(t, func() int {
		return run([]string{"stats", "--vault", root, "--stale-days", "0"})
	})
	if code != 2 {
		t.Errorf("exit code = %d, want 2 for --stale-days 0", code)
	}
	if !strings.Contains(stderr, "--stale-days must be >= 1") {
		t.Errorf("stderr does not mention '--stale-days must be >= 1': %s", stderr)
	}
}
