package main

// sync_mux_test.go pins, at the verb level, what internal/vaultsync's mux
// tests pin at the call level (042 S3c): the ssh connections `lw sync` and
// auto-sync open are multiplexed — one authenticated session shared by the
// fetch, the push and the next verb — unless the user's ssh configuration
// already names a ControlPath.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSyncMultiplexesSSH: through the fake ssh, the explicit verbs (interactive)
// and the auto steps (batch) all carry ControlMaster=auto, a ControlPath under
// the cache directory and ControlPersist=600; with a ControlPath of the user's
// own, none of them do.
func TestSyncMultiplexesSSH(t *testing.T) {
	// transport returns the logged calls that carry pack protocol.
	transport := func(log string) []string {
		var out []string
		for _, l := range strings.Split(log, "\n") {
			if strings.Contains(l, "git-upload-pack") || strings.Contains(l, "git-receive-pack") {
				out = append(out, l)
			}
		}
		return out
	}
	mux := func(l string) bool {
		return strings.Contains(l, "[ControlMaster=auto]") && strings.Contains(l, "[ControlPersist=600]") && strings.Contains(l, "[ControlPath=")
	}
	anyMux := func(l string) bool {
		return strings.Contains(l, "[ControlMaster=") || strings.Contains(l, "[ControlPath=") || strings.Contains(l, "[ControlPersist=")
	}

	run3 := func(t *testing.T, a *syncPC, logPath string) (explicit, auto []string) {
		t.Helper()
		a.write("notes/20261009-130000-x.md", "x\n")
		if err := os.WriteFile(logPath, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, stderr, code := a.lw("sync"); code != 0 {
			t.Fatalf("sync: exit %d stderr %q", code, stderr)
		}
		explicit = transport(readFileOrEmpty(logPath))
		if err := os.WriteFile(logPath, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		noteAt(t, 0)
		if _, stderr, code := a.lw("note", "-m", "y", "--vault", a.root); code != 0 {
			t.Fatalf("note: exit %d stderr %q", code, stderr)
		}
		return explicit, transport(readFileOrEmpty(logPath))
	}
	setup := func(t *testing.T, controlPath string) (*syncPC, string) {
		syncHermetic(t)
		logPath := installFakeSSH(t)
		// Before the first ssh call: lw asks ssh -G once per process and host.
		t.Setenv("FAKE_SSH_CONTROLPATH", controlPath)
		a := newSyncPC(t, "a")
		a.useFixture()
		if _, stderr, code := a.lw("sync", "init", "box:remote-vault", "--vault", a.root); code != 0 {
			t.Fatalf("init: exit %d stderr %q", code, stderr)
		}
		return a, logPath
	}

	t.Run("by default", func(t *testing.T) {
		a, logPath := setup(t, "")
		explicit, auto := run3(t, a, logPath)
		if len(explicit) == 0 || len(auto) == 0 {
			t.Fatalf("no ssh transport calls were logged: explicit %v auto %v", explicit, auto)
		}
		for _, l := range append(append([]string{}, explicit...), auto...) {
			if !mux(l) {
				t.Errorf("an ssh call is not multiplexed: %s", l)
			}
		}
		for _, l := range explicit {
			if strings.Contains(l, "BatchMode") {
				t.Errorf("explicit sync was made batch: %s", l)
			}
		}
		for _, l := range auto {
			if !strings.Contains(l, "[BatchMode=yes]") {
				t.Errorf("auto-sync lost its batch options: %s", l)
			}
		}
		cache := os.Getenv("XDG_CACHE_HOME")
		if info, err := os.Stat(filepath.Join(cache, "lw", "ssh")); err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("control directory: %v %v; want one with mode 0700", info, err)
		}
	})

	t.Run("not over the user's own ControlPath", func(t *testing.T) {
		a, logPath := setup(t, "/home/u/.ssh/cm-%C")
		explicit, auto := run3(t, a, logPath)
		for _, l := range append(append([]string{}, explicit...), auto...) {
			if anyMux(l) {
				t.Errorf("lw multiplexed over the user's ControlPath: %s", l)
			}
		}
	})
}

// readFileOrEmpty is the file's text, "" when it is not there.
func readFileOrEmpty(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}
