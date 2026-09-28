package config

// trace_test.go pins the [trace] table (038 T3, L2/L3): keep_mb is optional
// (nil = key absent = the 256 MiB default lives in DefaultTraceKeepMB, not
// on disk), an explicit 0 means off, out-of-range values are rejected with
// the pinned sentence, and a keyless file still round-trips byte-identically.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestTraceKeepDefault(t *testing.T) {
	dir := withConfigDir(t)
	writeConfig(t, dir, `[llm]
model = "deepseek-v4-flash"
`)

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Trace.KeepMB != nil {
		t.Fatalf("KeepMB = %d, want nil (key absent)", *got.Trace.KeepMB)
	}
	if b := got.TraceKeepBytes(); b != 256<<20 {
		t.Fatalf("TraceKeepBytes() = %d, want %d", b, 256<<20)
	}
}

func TestTraceKeepExplicitZero(t *testing.T) {
	dir := withConfigDir(t)
	writeConfig(t, dir, "[trace]\n  keep_mb = 0\n")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Trace.KeepMB == nil || *got.Trace.KeepMB != 0 {
		t.Fatalf("KeepMB = %v, want &0 (explicit zero is off, not the default)", got.Trace.KeepMB)
	}
	if b := got.TraceKeepBytes(); b != 0 {
		t.Fatalf("TraceKeepBytes() = %d, want 0 (off)", b)
	}
}

func TestTraceKeepRange(t *testing.T) {
	dir := withConfigDir(t)

	for _, mb := range []int{-1, 10241} {
		writeConfig(t, dir, "[trace]\n  keep_mb = "+strconv.Itoa(mb)+"\n")
		_, err := Load()
		want := "config: trace.keep_mb must be between 0 and 10240, got " + strconv.Itoa(mb)
		if err == nil {
			t.Fatalf("keep_mb = %d: Load accepted it", mb)
		}
		if err.Error() != want {
			t.Fatalf("keep_mb = %d:\n got  %s\n want %s", mb, err, want)
		}
	}
}

func TestTraceKeylessRoundTrip(t *testing.T) {
	withConfigDir(t)
	path := filepath.Join(ConfigDir(), configFileName)

	// Default carries no Trace, so its Save is exactly the keyless shape.
	if err := Default().Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("keyless round-trip changed the bytes:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
	if strings.Contains(string(after), "trace") {
		t.Fatalf("keyless file grew a [trace] table:\n%s", after)
	}
}

func TestTraceSaveExplicit(t *testing.T) {
	withConfigDir(t)
	path := filepath.Join(ConfigDir(), configFileName)

	keep := 64
	cfg := Default()
	cfg.Trace.KeepMB = &keep
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := "[trace]\n  keep_mb = 64\n"
	if !strings.Contains(string(b), want) {
		t.Fatalf("saved file does not carry %q:\n%s", want, b)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Trace.KeepMB == nil || *got.Trace.KeepMB != 64 {
		t.Fatalf("reloaded KeepMB = %v, want &64", got.Trace.KeepMB)
	}
}
