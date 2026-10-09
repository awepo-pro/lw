package vaultsync

// mux_test.go pins 042 S3c: every ssh connection lw sync opens is multiplexed
// (ControlMaster=auto, a ControlPath under the user's cache directory,
// ControlPersist=600) unless the user's own ssh configuration already names a
// ControlPath, interactive or not, appended to the user's own ssh command.
// The fake ssh answers `ssh -G` with "controlpath none" (or
// FAKE_SSH_CONTROLPATH) and logs those calls apart, in $FAKE_SSH_LOG.G.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// muxEnv is a hermetic environment with the fake ssh installed and the mux
// seams pointed at short directories. It returns the log path and the cache
// directory ssh control sockets go under (the sockets themselves live in
// <cache>/lw/ssh).
func muxEnv(t *testing.T) (logPath, cache string) {
	t.Helper()
	hermetic(t)
	logPath = installFakeSSH(t)
	resetSSHConfigAnswers()
	t.Cleanup(resetSSHConfigAnswers)
	cache = shortTemp(t)
	cacheOrig, tmpOrig := userCacheDir, tempDir
	userCacheDir = func() (string, error) { return cache, nil }
	tempDir = func() string { return shortTemp(t) }
	t.Cleanup(func() { userCacheDir, tempDir = cacheOrig, tmpOrig })
	return logPath, cache
}

// transportLine returns the logged ssh call that carries pack protocol —
// git's own transport — or "" when there was none.
func transportLine(t *testing.T, log string) string {
	t.Helper()
	for _, l := range strings.Split(log, "\n") {
		if strings.Contains(l, "git-upload-pack") || strings.Contains(l, "git-receive-pack") {
			return l
		}
	}
	return ""
}

// order fails the test unless the bracketed words appear in line in order.
func order(t *testing.T, line string, words ...string) {
	t.Helper()
	from := 0
	for _, w := range words {
		i := strings.Index(line[from:], "["+w+"]")
		if i < 0 {
			t.Fatalf("ssh argv lacks [%s] (in order %v): %s", w, words, line)
		}
		from += i + len(w) + 2
	}
}

// anyMux reports whether line carries any multiplexing option (as an ssh word:
// the test names, which end up in paths, contain the same letters).
func anyMux(line string) bool {
	return strings.Contains(line, "[ControlMaster=") || strings.Contains(line, "[ControlPath=") || strings.Contains(line, "[ControlPersist=")
}

// hasMux reports whether line carries all three multiplexing options.
func hasMux(line string) bool {
	return strings.Contains(line, "[ControlMaster=auto]") && strings.Contains(line, "[ControlPersist=600]") &&
		strings.Contains(line, "[ControlPath=")
}

// TestMuxOptionsAppearWhenSSHSaysNone: with `ssh -G` reporting no ControlPath,
// git's transport carries the three options after the batch options, and the
// control directory exists with mode 0700.
func TestMuxOptionsAppearWhenSSHSaysNone(t *testing.T) {
	logPath, cache := muxEnv(t)
	p := newPair(t)
	if _, err := Status(t.Context(), opts(p.a, "fake:"+p.bare)); err != nil {
		t.Fatalf("Status: %v", err)
	}
	line := transportLine(t, readFile(t, logPath))
	dir := filepath.Join(cache, "lw", "ssh")
	order(t, line, "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "-o", "ControlMaster=auto", "-o", "ControlPath="+dir+"/%C", "-o", "ControlPersist=600")
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("no control directory: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("control directory mode = %v, want 0700", info.Mode().Perm())
	}
	if g := readFile(t, logPath+".G"); !strings.HasPrefix(g, "argv: [-G] [fake]\n") {
		t.Errorf("ssh -G log = %q, want a single probe of the destination", g)
	}
}

// TestMuxIsInteractiveToo: an interactive call (lw sync, init, clone) gets the
// options as well — and no batch options.
func TestMuxIsInteractiveToo(t *testing.T) {
	logPath, cache := muxEnv(t)
	p := newPair(t)
	o := opts(p.a, "fake:"+p.bare)
	o.Interactive = true
	if _, err := Status(t.Context(), o); err != nil {
		t.Fatalf("Status: %v", err)
	}
	line := transportLine(t, readFile(t, logPath))
	if !hasMux(line) || !strings.Contains(line, "[ControlPath="+filepath.Join(cache, "lw", "ssh")+"/%C]") {
		t.Errorf("an interactive transport call lacks the multiplexing options: %s", line)
	}
	if strings.Contains(line, "BatchMode") {
		t.Errorf("an interactive call was made batch: %s", line)
	}
}

// TestMuxAbsentWhenTheUserHasAControlPath: ssh -G reporting any controlpath
// but none means the user's own configuration multiplexes (or chose how); lw
// adds nothing and creates nothing.
func TestMuxAbsentWhenTheUserHasAControlPath(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		name := map[bool]string{false: "batch", true: "interactive"}[interactive]
		t.Run(name, func(t *testing.T) {
			logPath, cache := muxEnv(t)
			t.Setenv("FAKE_SSH_CONTROLPATH", "/home/u/.ssh/cm-%r@%h:%p")
			p := newPair(t)
			o := opts(p.a, "fake:"+p.bare)
			o.Interactive = interactive
			if _, err := Status(t.Context(), o); err != nil {
				t.Fatalf("Status: %v", err)
			}
			line := transportLine(t, readFile(t, logPath))
			if line == "" {
				t.Fatal("the fake ssh was never used for the transport")
			}
			if anyMux(line) {
				t.Errorf("lw added multiplexing over the user's own ControlPath: %s", line)
			}
			if _, err := os.Stat(filepath.Join(cache, "lw")); err == nil {
				t.Error("lw created a control directory it will not use")
			}
		})
	}
}

// TestMuxAbsentWhenSSHCannotSay: an ssh that fails -G (too old, broken) gets
// nothing added — failing safe.
func TestMuxAbsentWhenSSHCannotSay(t *testing.T) {
	logPath, _ := muxEnv(t)
	t.Setenv("FAKE_SSH_G_FAIL", "1")
	p := newPair(t)
	if _, err := Status(t.Context(), opts(p.a, "fake:"+p.bare)); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if line := transportLine(t, readFile(t, logPath)); line == "" || anyMux(line) {
		t.Errorf("transport = %q; want a call without multiplexing", line)
	}
}

// TestMuxWhenSSHOmitsTheControlPathLine pins the live finding (042 acceptance,
// 2026-10-09): real OpenSSH prints no controlpath line when none is set, so a
// well-formed -G answer without one must still multiplex — while output that is
// not a -G answer at all (no hostname line) is not trusted and adds nothing.
func TestMuxWhenSSHOmitsTheControlPathLine(t *testing.T) {
	for _, tc := range []struct {
		mode    string
		wantMux bool
	}{{"omit", true}, {"garbage", false}} {
		t.Run(tc.mode, func(t *testing.T) {
			logPath, _ := muxEnv(t)
			t.Setenv("FAKE_SSH_CONTROLPATH", tc.mode)
			p := newPair(t)
			if _, err := Status(t.Context(), opts(p.a, "fake:"+p.bare)); err != nil {
				t.Fatalf("Status: %v", err)
			}
			line := transportLine(t, readFile(t, logPath))
			if line == "" || anyMux(line) != tc.wantMux {
				t.Errorf("-G %s: transport = %q; want multiplexing %v", tc.mode, line, tc.wantMux)
			}
		})
	}
}

// TestMuxNotForLocalRemotesOrGITSSH: a local-path remote has no ssh to share,
// and a GIT_SSH program is not an ssh command line to append to.
func TestMuxNotForLocalRemotesOrGITSSH(t *testing.T) {
	logPath, cache := muxEnv(t)
	p := newPair(t)
	if _, err := Status(t.Context(), opts(p.a, p.bare)); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if g := readFile(t, logPath+".G"); g != "" {
		t.Errorf("ssh -G was run for a local remote: %q", g)
	}
	if _, err := os.Stat(filepath.Join(cache, "lw")); err == nil {
		t.Error("a local remote made a control directory")
	}

	t.Setenv("GIT_SSH", "/bin/true")
	if _, err := Status(t.Context(), opts(p.a, "fake:"+p.bare)); err == nil {
		// /bin/true is not a transport: the fetch fails, which is fine — only
		// the absence of the probe matters.
		t.Log("fetch through GIT_SSH=/bin/true succeeded")
	}
	if g := readFile(t, logPath+".G"); g != "" {
		t.Errorf("ssh -G was run although GIT_SSH owns the transport: %q", g)
	}
}

// TestMuxPathLengthFallback: the socket path must fit sun_path. The cache
// directory is used when dir + "/" + %C(40) + ssh's 17-byte temporary suffix is
// at most 100 bytes; otherwise the temp directory's lw-ssh-<uid>; otherwise no
// multiplexing.
func TestMuxPathLengthFallback(t *testing.T) {
	// padded returns a directory path under a fresh temp dir whose full length,
	// with "/lw/ssh" appended, is n bytes.
	padded := func(t *testing.T, n int) string {
		t.Helper()
		root := shortTemp(t)
		k := n - len("/lw/ssh") - len(root) - 1
		if k < 1 {
			t.Fatalf("cannot pad %s to %d", root, n)
		}
		return filepath.Join(root, strings.Repeat("p", k))
	}
	limit := maxControlPath - controlSocketLen // 42: the longest control directory

	t.Run("exactly at the limit uses the cache directory", func(t *testing.T) {
		muxEnv(t)
		base := padded(t, limit)
		userCacheDir = func() (string, error) { return base, nil }
		dir, ok := controlDir()
		if !ok || dir != filepath.Join(base, "lw", "ssh") || len(dir) != limit {
			t.Fatalf("controlDir() = %q, %v; want the cache directory at %d bytes", dir, ok, limit)
		}
	})
	t.Run("one byte over falls back to the temp directory", func(t *testing.T) {
		muxEnv(t)
		base := padded(t, limit+1)
		userCacheDir = func() (string, error) { return base, nil }
		tmp := shortTemp(t)
		tempDir = func() string { return tmp }
		dir, ok := controlDir()
		want := filepath.Join(tmp, "lw-ssh-"+strconv.Itoa(os.Getuid()))
		if !ok || dir != want {
			t.Fatalf("controlDir() = %q, %v; want %q", dir, ok, want)
		}
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("fallback directory: %v, mode %v; want 0700", err, info)
		}
		if _, err := os.Stat(base); err == nil {
			t.Error("the too-long cache directory was created anyway")
		}
	})
	t.Run("both too long means no multiplexing", func(t *testing.T) {
		logPath, _ := muxEnv(t)
		userCacheDir = func() (string, error) { return padded(t, limit+1), nil }
		long := padded(t, limit+30)
		tempDir = func() string { return long }
		if dir, ok := controlDir(); ok {
			t.Fatalf("controlDir() = %q, true; want none", dir)
		}
		p := newPair(t)
		if _, err := Status(t.Context(), opts(p.a, "fake:"+p.bare)); err != nil {
			t.Fatalf("Status: %v", err)
		}
		if line := transportLine(t, readFile(t, logPath)); line == "" || anyMux(line) {
			t.Errorf("transport = %q; want a call without multiplexing", line)
		}
	})
	t.Run("no user cache directory falls back to the temp directory", func(t *testing.T) {
		muxEnv(t)
		userCacheDir = func() (string, error) { return "", os.ErrNotExist }
		tmp := shortTemp(t)
		tempDir = func() string { return tmp }
		if dir, ok := controlDir(); !ok || !strings.HasPrefix(dir, tmp) {
			t.Fatalf("controlDir() = %q, %v", dir, ok)
		}
	})
}

// TestMuxDirectoryIsPrivate: the control directory is the user's, a real
// directory, mode 0700 — an existing looser one is tightened, and a symlink or
// a file in its place is not used.
func TestMuxDirectoryIsPrivate(t *testing.T) {
	hermetic(t)
	root := shortTemp(t)

	loose := filepath.Join(root, "loose")
	if err := os.Mkdir(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	if !privateDir(loose) {
		t.Fatal("a directory we own was refused")
	}
	if info, _ := os.Stat(loose); info.Mode().Perm() != 0o700 {
		t.Errorf("mode = %v, want the 0755 directory tightened to 0700", info.Mode().Perm())
	}

	target := filepath.Join(root, "elsewhere")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if privateDir(link) {
		t.Error("a symlink was accepted as the control directory")
	}

	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if privateDir(file) {
		t.Error("a file was accepted as the control directory")
	}

	// Not ours: pretend to be another user.
	orig := getuid
	getuid = func() int { return os.Getuid() + 1 }
	defer func() { getuid = orig }()
	if privateDir(loose) {
		t.Error("a directory owned by someone else was accepted")
	}
}

// TestMuxUserSSHCommandIsKept: the user's GIT_SSH_COMMAND / core.sshCommand
// stays the prefix — options, batch options, then ours — and `ssh -G` is run
// with it too, so a -F config file counts.
func TestMuxUserSSHCommandIsKept(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T)
	}{
		{"GIT_SSH_COMMAND", func(t *testing.T) { t.Setenv("GIT_SSH_COMMAND", "ssh -i /fake/key-env") }},
		{"core.sshCommand", func(t *testing.T) {
			writeGlobalGitConfig(t, "[core]\n\tsshCommand = ssh -i /fake/key-env\n")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath, cache := muxEnv(t)
			p := newPair(t)
			tc.setup(t)
			dir := filepath.Join(cache, "lw", "ssh")

			if _, err := Status(t.Context(), opts(p.a, "fake:"+p.bare)); err != nil {
				t.Fatal(err)
			}
			order(t, transportLine(t, readFile(t, logPath)), "-i", "/fake/key-env", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "-o", "ControlMaster=auto", "-o", "ControlPath="+dir+"/%C")
			order(t, strings.SplitN(readFile(t, logPath+".G"), "\n", 2)[0], "-i", "/fake/key-env", "-G", "fake")

			// Interactive: the user's command, then ours, and no batch options.
			if err := os.WriteFile(logPath, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			o := opts(p.a, "fake:"+p.bare)
			o.Interactive = true
			if _, err := Status(t.Context(), o); err != nil {
				t.Fatal(err)
			}
			line := transportLine(t, readFile(t, logPath))
			order(t, line, "-i", "/fake/key-env", "-o", "ControlMaster=auto")
			if strings.Contains(line, "BatchMode") {
				t.Errorf("an interactive call was made batch: %s", line)
			}
		})
	}
}

// TestMuxProbeIsCachedPerHost: ssh -G runs once per destination per process,
// not once per git call.
func TestMuxProbeIsCachedPerHost(t *testing.T) {
	logPath, _ := muxEnv(t)
	p := newPair(t)
	ctx := t.Context()
	for i := 0; i < 3; i++ {
		if _, err := Status(ctx, opts(p.a, "fake:"+p.bare)); err != nil {
			t.Fatal(err)
		}
	}
	put(t, p.a, "wiki/alpha.md", "changed\n")
	if _, err := CommitWork(opts(p.a, "fake:"+p.bare), "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := Push(ctx, opts(p.a, "fake:"+p.bare)); err != nil {
		t.Fatal(err)
	}
	g := readFile(t, logPath+".G")
	if n := strings.Count(g, "argv:"); n != 1 {
		t.Errorf("ssh -G ran %d times for one host, want 1:\n%s", n, g)
	}
	// Another destination is another question.
	if _, err := Status(ctx, opts(p.a, "other:"+p.bare)); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(readFile(t, logPath+".G"), "argv:"); n != 2 {
		t.Errorf("ssh -G ran %d times for two hosts, want 2", n)
	}
	// And a port is part of the destination.
	if _, err := Status(ctx, opts(p.a, "ssh://fake:2222"+p.bare)); err != nil {
		t.Fatal(err)
	}
	if g := readFile(t, logPath+".G"); !strings.Contains(g, "[-G] [-p] [2222] [fake]") {
		t.Errorf("the port did not reach ssh -G:\n%s", g)
	}
}

// TestMuxInitsOwnSSHCall: lw sync init's own ssh call — the remote-side script
// — is multiplexed too, whether plain ssh is run directly or the user's command
// is run through sh.
func TestMuxInitsOwnSSHCall(t *testing.T) {
	for _, custom := range []bool{false, true} {
		name := map[bool]string{false: "plain ssh", true: "the user's command"}[custom]
		t.Run(name, func(t *testing.T) {
			logPath, cache := muxEnv(t)
			if custom {
				t.Setenv("GIT_SSH_COMMAND", "ssh -i /fake/key-env")
			}
			remote := filepath.Join(shortTemp(t), "r.git")
			if _, err := Init(t.Context(), opts(makeVault(t), "fake:"+remote)); err != nil {
				t.Fatalf("Init: %v", err)
			}
			// The script is one multi-line argument: split the log by call.
			var initLine string
			for _, rec := range strings.Split("\n"+readFile(t, logPath), "\nargv:") {
				if strings.Contains(rec, "git init --bare") {
					initLine = rec
				}
			}
			if initLine == "" {
				t.Fatal("the init script never went through ssh")
			}
			dir := filepath.Join(cache, "lw", "ssh")
			order(t, initLine, "-o", "BatchMode=yes", "-o", "ControlMaster=auto", "-o", "ControlPath="+dir+"/%C", "-o", "ControlPersist=600", "--", "fake")
			if custom {
				order(t, initLine, "-i", "/fake/key-env", "-o", "BatchMode=yes")
			}
		})
	}
}

// TestMuxQuotesAPathWithSpaces: GIT_SSH_COMMAND is parsed by sh, so a control
// directory with a space (or a quote) in it must arrive at ssh as one word.
func TestMuxQuotesAPathWithSpaces(t *testing.T) {
	logPath, _ := muxEnv(t)
	weird := filepath.Join(shortTemp(t), "it's a dir")
	userCacheDir = func() (string, error) { return weird, nil }
	p := newPair(t)
	if _, err := Status(t.Context(), opts(p.a, "fake:"+p.bare)); err != nil {
		t.Fatalf("Status: %v", err)
	}
	want := "[ControlPath=" + filepath.Join(weird, "lw", "ssh") + "/%C]"
	if line := transportLine(t, readFile(t, logPath)); !strings.Contains(line, want) {
		t.Errorf("transport = %s\nwant the control path as one word: %s", line, want)
	}
	for in, want := range map[string]string{
		"plain/path-1.2_x%C": "plain/path-1.2_x%C",
		"a b":                "'a b'",
		"it's":               `'it'\''s'`,
		"$HOME":              "'$HOME'",
	} {
		if got := shellWord(in); got != want {
			t.Errorf("shellWord(%q) = %q, want %q", in, got, want)
		}
	}
}
