package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/awepo-pro/lw/internal/vaultsync"
)

var syncInstallGOOS = runtime.GOOS

const watchLockName = "watch.lock"

type watchLoop struct {
	quiet, maxWait, interval time.Duration // 3s, 30s, 15s by default
	now                      func() time.Time
	dirty                    func() (vaultsync.DirtyState, error)
	remote                   func() (changed bool, err error) // true when RemoteHead's sha != local
	cycle                    func() error                     // one full sync, as plain `lw sync`
	logf                     func(format string, a ...any)    // one line per call

	firstDirty      time.Time
	lastRemoteCheck time.Time
	nextRemoteCheck time.Time
	backoffUntil    time.Time
	backoffDuration time.Duration
	lastCycleErr    string
	lastDirtyErr    string
	lastRemoteErr   string
	heldDeletions   bool
}

func (w *watchLoop) step() {
	if w.quiet == 0 {
		w.quiet = 3 * time.Second
	}
	if w.maxWait == 0 {
		w.maxWait = 30 * time.Second
	}
	if w.interval == 0 {
		w.interval = 15 * time.Second
	}
	nowFn := time.Now
	if w.now != nil {
		nowFn = w.now
	}
	currentTime := nowFn()

	st, err := w.dirty()
	if err != nil {
		errStr := err.Error()
		if errStr != w.lastDirtyErr {
			w.logf("sync: %v", err)
			w.lastDirtyErr = errStr
		}
		return
	}
	w.lastDirtyErr = ""

	// W6 deletion guard
	if st.Deleted > 20 && st.Deleted*4 > st.Tracked {
		if !w.heldDeletions {
			w.logf("%d tracked files are deleted - not syncing that unattended; run lw sync to confirm, or restore them", st.Deleted)
			w.heldDeletions = true
		}
		return
	}
	w.heldDeletions = false

	// Local dirty check
	localTrigger := false
	if len(st.Paths) > 0 {
		if w.firstDirty.IsZero() {
			w.firstDirty = currentTime
		}
		if !st.Newest.IsZero() && !currentTime.Before(st.Newest) && currentTime.Sub(st.Newest) >= w.quiet {
			localTrigger = true
		} else if st.Newest.IsZero() && currentTime.Sub(w.firstDirty) >= w.quiet {
			localTrigger = true
		} else if currentTime.Sub(w.firstDirty) >= w.maxWait {
			localTrigger = true
		}
	} else {
		w.firstDirty = time.Time{}
	}

	// Remote check (W4)
	remoteTrigger := false
	remoteDue := w.nextRemoteCheck.IsZero() || !currentTime.Before(w.nextRemoteCheck)
	if !localTrigger && remoteDue {
		w.nextRemoteCheck = currentTime.Add(w.interval)
		changed, rerr := w.remote()
		if rerr != nil {
			errStr := rerr.Error()
			if errStr != w.lastRemoteErr {
				w.logf("sync: %v", rerr)
				w.lastRemoteErr = errStr
			}
			return
		}
		w.lastRemoteErr = ""
		if changed {
			remoteTrigger = true
		}
	}

	if !localTrigger && !remoteTrigger {
		return
	}

	// Backoff check (W5)
	if !w.backoffUntil.IsZero() && currentTime.Before(w.backoffUntil) {
		return
	}

	// Run cycle
	err = w.cycle()
	w.nextRemoteCheck = currentTime.Add(w.interval)
	if err != nil {
		errStr := err.Error()
		if errStr != w.lastCycleErr {
			w.logf("sync: %v", err)
			w.lastCycleErr = errStr
		}
		if w.backoffDuration == 0 {
			w.backoffDuration = 2 * w.interval
		} else {
			w.backoffDuration *= 2
			if w.backoffDuration > 5*time.Minute {
				w.backoffDuration = 5 * time.Minute
			}
		}
		w.backoffUntil = currentTime.Add(w.backoffDuration)
		return
	}

	// Success
	if w.lastCycleErr != "" || w.backoffDuration > 0 {
		w.logf("sync: recovered")
	}
	w.lastCycleErr = ""
	w.backoffDuration = 0
	w.backoffUntil = time.Time{}
	w.firstDirty = time.Time{}
}

func lockWatch(root string) (release func(), err error) {
	dir := filepath.Join(root, stateDirName, "tmp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("watch lock: %w", err)
	}
	path := filepath.Join(dir, watchLockName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("watch lock: %w", err)
	}
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			b, _ := os.ReadFile(path)
			pid := strings.TrimSpace(string(b))
			f.Close()
			fmt.Fprintf(os.Stderr, "lw sync watch: already running for this vault (pid %s)\n", pid)
			return nil, &exitError{code: 1}
		}
		f.Close()
		return nil, fmt.Errorf("watch lock: %w", err)
	}
	_ = f.Truncate(0)
	_, _ = f.Seek(0, 0)
	fmt.Fprintf(f, "%d\n", os.Getpid())
	_ = f.Sync()

	var once sync.Once
	return func() {
		once.Do(func() {
			syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			f.Close()
		})
	}, nil
}

func runningWatchPid(root string) string {
	path := filepath.Join(root, stateDirName, "tmp", watchLockName)
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return ""
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return ""
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		b, err := os.ReadFile(path)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	return ""
}

type timestampWriter struct {
	dst io.Writer
	buf bytes.Buffer
	mu  sync.Mutex
}

func (tw *timestampWriter) Write(p []byte) (n int, err error) {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	n = len(p)
	tw.buf.Write(p)
	for {
		line, err := tw.buf.ReadBytes('\n')
		if err != nil {
			tw.buf.Write(line)
			break
		}
		text := strings.TrimRight(string(line), "\r\n")
		if text != "" {
			ts := time.Now().Format(time.RFC3339)
			fmt.Fprintf(tw.dst, "%s %s\n", ts, text)
		}
	}
	return n, nil
}

func (tw *timestampWriter) Flush() {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.buf.Len() > 0 {
		text := strings.TrimRight(tw.buf.String(), "\r\n")
		if text != "" {
			ts := time.Now().Format(time.RFC3339)
			fmt.Fprintf(tw.dst, "%s %s\n", ts, text)
		}
		tw.buf.Reset()
	}
}

func watchLogf(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	ts := time.Now().Format(time.RFC3339)
	fmt.Fprintf(os.Stdout, "%s %s\n", ts, msg)
}

func cmdSyncWatch(args []string, vaultDefault string) error {
	fs := flag.NewFlagSet("sync watch", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { syncUsage(os.Stderr) }
	vaultPath := fs.String("vault", vaultDefault, "vault root")
	intervalFlag := fs.Duration("interval", 15*time.Second, "remote check interval (minimum 5s)")
	quietFlag := fs.Duration("quiet", 3*time.Second, "debounce quiet time (minimum 1s)")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "lw sync watch: unexpected argument %q\n", fs.Arg(0))
		return &exitError{code: 2}
	}
	if *intervalFlag < 5*time.Second {
		fmt.Fprintln(os.Stderr, "lw sync watch: --interval must be at least 5s")
		return &exitError{code: 2}
	}
	if *quietFlag < 1*time.Second {
		fmt.Fprintln(os.Stderr, "lw sync watch: --quiet must be at least 1s")
		return &exitError{code: 2}
	}

	root, o, err := syncSetup(*vaultPath, syncModeWrite)
	if err != nil {
		return err
	}
	o.Interactive = false

	releaseLock, err := lockWatch(root)
	if err != nil {
		return err
	}
	defer releaseLock()

	tw := &timestampWriter{dst: os.Stdout}
	cycle := func() error {
		cctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		defer tw.Flush()
		return runSyncCycle(cctx, root, o, tw, os.Stderr, false, true, nil)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	// Runs one cycle at start
	_ = cycle()

	select {
	case <-sigCh:
		return nil
	default:
	}

	w := &watchLoop{
		quiet:    *quietFlag,
		maxWait:  30 * time.Second,
		interval: *intervalFlag,
		now:      time.Now,
		dirty: func() (vaultsync.DirtyState, error) {
			return vaultsync.Dirty(context.Background(), o)
		},
		remote: func() (bool, error) {
			sha, local, _, err := vaultsync.RemoteHead(context.Background(), o)
			if err != nil {
				return false, err
			}
			return sha != "" && sha != local, nil
		},
		cycle: cycle,
		logf:  watchLogf,
	}
	w.nextRemoteCheck = time.Now().Add(*intervalFlag)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-sigCh:
			return nil
		case <-ticker.C:
			w.step()
			select {
			case <-sigCh:
				return nil
			default:
			}
		}
	}
}

func cmdSyncInstall(args []string, vaultDefault string) error {
	fs := flag.NewFlagSet("sync install", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { syncUsage(os.Stderr) }
	printFlag := fs.Bool("print", false, "print systemd user service or launchd plist")
	vaultPath := fs.String("vault", vaultDefault, "vault root")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "lw sync install: unexpected argument %q\n", fs.Arg(0))
		return &exitError{code: 2}
	}
	if !*printFlag {
		fmt.Fprintln(os.Stderr, "lw sync install: only --print is supported")
		return &exitError{code: 2}
	}

	root, err := findVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("executable path: %w", err)
	}
	absExe, err := filepath.Abs(exe)
	if err != nil {
		return err
	}

	if syncInstallGOOS == "darwin" {
		fmt.Printf(`# ~/Library/LaunchAgents/pro.awepo.lw-sync.plist
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>pro.awepo.lw-sync</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>sync</string>
		<string>watch</string>
		<string>--vault</string>
		<string>%s</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
</dict>
</plist>

# Commands to enable and run:
launchctl load -w ~/Library/LaunchAgents/pro.awepo.lw-sync.plist
`, absExe, absRoot)
		return nil
	}

	fmt.Printf(`# ~/.config/systemd/user/lw-sync.service
[Unit]
Description=llmwiki sync watch
After=network-online.target

[Service]
Type=simple
ExecStart=%s sync watch --vault %s
Restart=on-failure
RestartSec=10

[Install]
WantedBy=default.target

# Commands to enable and run:
systemctl --user daemon-reload
systemctl --user enable --now lw-sync.service
`, absExe, absRoot)
	return nil
}
