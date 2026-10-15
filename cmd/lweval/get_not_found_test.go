package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/eval"
)

// writeNotFoundRun writes a scored run of one joined ingest case whose runs
// each made the given numbers of not-found wiki.get calls (060): the results.json
// `show` and `compare` read, with the one metric a test of the tables needs.
func writeNotFoundRun(t *testing.T, id string, notFound ...float64) string {
	t.Helper()
	res := eval.Results{
		Run: eval.RunInfo{
			ID: id, LW: "lw", LWVersion: "lw v9.9.9-test", N: len(notFound), Parallel: 1,
			SetSHA256:      "1111111111111111111111111111111111111111111111111111111111111111",
			SnapshotSHA256: "2222222222222222222222222222222222222222222222222222222222222222",
			EnvKeys:        []string{}, Started: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC), Finished: time.Date(2026, 10, 13, 12, 30, 0, 0, time.UTC),
		},
		SetSHA256: "3333333333333333333333333333333333333333333333333333333333333333",
		Results:   []eval.CaseResult{},
	}
	for i, n := range notFound {
		res.Results = append(res.Results, eval.CaseResult{
			Case: "http-joined", Index: i + 1, Verb: "ingest", Kind: "ingest",
			Metrics: map[string]float64{eval.MetricFactRecall: 1, eval.MetricGetNotFound: n, eval.MetricSearchRefusals: 0},
		})
	}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "results.json"), append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestShowCompareGetNotFound pins that get_not_found (060) reaches the two
// commands a person reads a run with: `show` prints a row for it, and `compare`
// of a control that was told "not found" against a candidate that was not prints
// the row with both means and the drop — the number the 060 acceptance reads.
func TestShowCompareGetNotFound(t *testing.T) {
	control := writeNotFoundRun(t, "20261013T120000Z-control", 2, 4, 3)
	candidate := writeNotFoundRun(t, "20261013T123000Z-candidate", 0, 0, 0)

	a, out, errb := newApp(nil, nil)
	if code := a.run(context.Background(), []string{"show", control}); code != 0 {
		t.Fatalf("show exit = %d: %s", code, errb.String())
	}
	t.Logf("lweval show:\n%s", out.String())
	if !regexp.MustCompile(`(?m)^get_not_found\s+3\.000\b`).Match(out.Bytes()) {
		t.Errorf("show has no get_not_found row with the mean 3.000:\n%s", out.String())
	}

	a, out, errb = newApp(nil, nil)
	if code := a.run(context.Background(), []string{"compare", control, candidate}); code != 0 {
		t.Fatalf("compare exit = %d: %s", code, errb.String())
	}
	t.Logf("lweval compare:\n%s", out.String())
	if !regexp.MustCompile(`(?m)^get_not_found\s+3\.000\s+0\.000\s+-3\.000\b`).Match(out.Bytes()) {
		t.Errorf("compare has no get_not_found row going 3.000 -> 0.000 (delta -3.000):\n%s", out.String())
	}
}
