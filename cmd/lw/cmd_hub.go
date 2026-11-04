package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/hub"
	"github.com/awepo-pro/lw/internal/hubserver"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/vaultsync"
)

var (
	lastHubRefresh time.Time
	hubRefreshMu   sync.Mutex
)

// resetHubRefreshTimer forces the next Refresh to pull by clearing the rate-limit timestamp.
func resetHubRefreshTimer() {
	hubRefreshMu.Lock()
	defer hubRefreshMu.Unlock()
	lastHubRefresh = time.Time{}
}

func hubUsage(w io.Writer) {
	fmt.Fprint(w, `usage: lw hub serve [--vault P] [--port N]
       lw hub status [--vault P]
       lw hub devices [--json]
       lw hub approve <slug|id>
       lw hub revoke <slug|id>
       lw hub install --print [--vault P]

lw hub is the phone sync service for lw vaults. An Obsidian plugin talks
HTTPS over Tailscale to lw hub serve running on loopback (127.0.0.1).

Verbs:
  serve       run loopback HTTP service behind tailscale serve
  status      show hub configuration, device counts, and diverged state
  devices     list registered devices and their status
  approve     approve a pending device by name/slug or UUID
  revoke      revoke an approved device by name/slug or UUID
  install     print systemd user service unit and tailscale serve commands
`)
}

func cmdHub(args []string) error {
	if len(args) == 0 || wantsHelp(args) {
		hubUsage(os.Stdout)
		return nil
	}

	switch sub, rest := args[0], args[1:]; sub {
	case "serve":
		return cmdHubServe(rest)
	case "status":
		return cmdHubStatus(rest)
	case "devices":
		return cmdHubDevices(rest)
	case "approve":
		return cmdHubApprove(rest)
	case "revoke":
		return cmdHubRevoke(rest)
	case "install":
		return cmdHubInstall(rest)
	default:
		fmt.Fprintf(os.Stderr, "lw hub: unknown command %q\n", sub)
		hubUsage(os.Stderr)
		return &exitError{code: 2}
	}
}

func hubDivergedMarkerPath(root string) string {
	return filepath.Join(root, ".git", "lw-hub-diverged")
}

func isHubDiverged(root string) bool {
	_, err := os.Stat(hubDivergedMarkerPath(root))
	return err == nil
}

func markHubDiverged(root, reason string) {
	marker := hubDivergedMarkerPath(root)
	content := fmt.Sprintf("%s\n%s\n", time.Now().UTC().Format(time.RFC3339), reason)
	_ = os.WriteFile(marker, []byte(content), 0644)
}

func clearHubDiverged(root string) {
	_ = os.Remove(hubDivergedMarkerPath(root))
}

func lockVaultWait(root string, timeout time.Duration) (func(), error) {
	deadline := time.Now().Add(timeout)
	backoff := 25 * time.Millisecond
	for {
		release, err := lockVault(root)
		if err == nil {
			return release, nil
		}
		if !errors.Is(err, errSyncBusy) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(backoff)
		if backoff < 250*time.Millisecond {
			backoff *= 2
		}
	}
}

func setupHubWiring(vaultPath string, overridePort int, devicesPath string) (root string, engine *hub.Engine, handler http.Handler, pusher *syncPusher, port int, users []string, err error) {
	// 1. Vault must be under lw sync
	root, o, err := syncSetup(vaultPath, syncModeWrite)
	if err != nil {
		return "", nil, nil, nil, 0, nil, fmt.Errorf("vault is not under lw sync (run lw sync init first): %w", err)
	}
	attachLoggingAt(root)

	// 2. [hub] users must be configured
	cfg, err := config.Load()
	if err != nil {
		return "", nil, nil, nil, 0, nil, fmt.Errorf("load config: %w", err)
	}
	if cfg.Hub == nil || len(cfg.Hub.Users) == 0 {
		return "", nil, nil, nil, 0, nil, errors.New("[hub] users is empty or not configured in config.toml")
	}

	port = 8779
	if overridePort > 0 {
		port = overridePort
	} else if cfg.Hub.Port > 0 {
		port = cfg.Hub.Port
	}

	if devicesPath == "" {
		devicesPath = filepath.Join(config.ConfigDir(), "hub-devices.json")
	}

	syncOpts := o
	syncOpts.Interactive = false
	syncOpts.Stderr = io.Discard
	syncOpts.Quiesce = quiesceVault(root)
	syncOpts.AppendOnly = syncAppendOnly

	doPull := func(ctx context.Context) error {
		if err := guardVault(root); err != nil {
			return err
		}
		if _, err := vaultsync.Recover(ctx, syncOpts); err != nil {
			return err
		}
		st, err := syncPull(ctx, syncOpts, stage.FormatVersion)
		if errors.Is(err, vaultsync.ErrDiverged) || st.Diverged() {
			markHubDiverged(root, fmt.Sprintf("diverged from %s: %d ahead, %d behind", st.Remote, st.Ahead, st.Behind))
			return &hub.Error{Code: hub.CodeDiverged, Message: "hub vault diverged from the remote; run lw sync on the hub host"}
		}
		if err != nil {
			return err
		}
		clearHubDiverged(root)
		if st.Pulled > 0 {
			_, _ = rebuildIndexIfStale(root)
		}
		return nil
	}

	runPush := func(ctx context.Context) syncPushResult {
		logResult := func(res syncPushResult) syncPushResult {
			slog.Info("hub: push", "remote", res.Remote, "pushed", res.Pushed, "pulled", res.Pulled, "rebased", res.Rebased, "err", res.Err, "storage_err", res.StorageErr)
			return res
		}

		if err := guardVault(root); err != nil {
			return logResult(syncPushResult{Err: err})
		}
		if _, err := vaultsync.Recover(ctx, syncOpts); err != nil {
			return logResult(syncPushResult{Err: err})
		}
		if _, err := commitWorkLocked(root, syncOpts, "hub: sweep"); err != nil {
			return logResult(syncPushResult{Err: err})
		}
		storageErr := storageUpload(ctx, root, syncOpts)
		if storageErr != nil {
			slog.Warn("hub: storage upload", "err", storageErr)
		}
		stPull, errPull := syncPull(ctx, syncOpts, stage.FormatVersion)
		if errors.Is(errPull, vaultsync.ErrDiverged) || stPull.Diverged() {
			markHubDiverged(root, fmt.Sprintf("diverged from %s: %d ahead, %d behind", stPull.Remote, stPull.Ahead, stPull.Behind))
			return logResult(syncPushResult{Remote: stPull.Remote, Err: errPull, StorageErr: storageErr})
		}
		if errPull != nil {
			return logResult(syncPushResult{Remote: stPull.Remote, Err: errPull, StorageErr: storageErr})
		}
		clearHubDiverged(root)
		if stPull.Pulled > 0 {
			_, _ = rebuildIndexIfStale(root)
		}
		stPush, errPush := syncPush(ctx, syncOpts)
		if errors.Is(errPush, vaultsync.ErrDiverged) || stPush.Diverged() {
			markHubDiverged(root, fmt.Sprintf("diverged from %s: %d ahead, %d behind", stPush.Remote, stPush.Ahead, stPush.Behind))
			return logResult(syncPushResult{Remote: stPush.Remote, Err: errPush, StorageErr: storageErr})
		}
		if errPush != nil {
			return logResult(syncPushResult{Remote: stPush.Remote, Err: errPush, StorageErr: storageErr})
		}
		return logResult(syncPushResult{Remote: stPush.Remote, Pushed: stPush.Pushed, Pulled: stPull.Pulled, Rebased: stPull.Rebased, StorageErr: storageErr})
	}

	pusher = newSyncPusher(runPush, 1*time.Second)

	hubOpts := hub.Options{
		Dir: root,
		Lock: func() (func(), error) {
			if isHubDiverged(root) {
				return nil, &hub.Error{Code: hub.CodeDiverged, Message: "hub vault diverged from the remote; run lw sync on the hub host"}
			}
			release, err := lockVaultWait(root, 15*time.Second)
			if err != nil {
				return nil, err
			}
			if isHubDiverged(root) {
				release()
				return nil, &hub.Error{Code: hub.CodeDiverged, Message: "hub vault diverged from the remote; run lw sync on the hub host"}
			}
			return release, nil
		},
		Commit: func(message string) error {
			if isHubDiverged(root) {
				return &hub.Error{Code: hub.CodeDiverged, Message: "hub vault diverged from the remote; run lw sync on the hub host"}
			}
			release, err := lockVaultWait(root, 15*time.Second)
			if err != nil {
				return err
			}
			defer release()
			if isHubDiverged(root) {
				return &hub.Error{Code: hub.CodeDiverged, Message: "hub vault diverged from the remote; run lw sync on the hub host"}
			}
			if err := noInterruptedCommit(root); err != nil {
				return err
			}
			committed, err := syncCommitWork(syncOpts, message)
			if err != nil {
				return err
			}
			if committed {
				pusher.Trigger()
			}
			return nil
		},
		Refresh: func(ctx context.Context) error {
			hubRefreshMu.Lock()
			elapsed := time.Since(lastHubRefresh)
			if elapsed < 10*time.Second {
				hubRefreshMu.Unlock()
				if isHubDiverged(root) {
					return &hub.Error{Code: hub.CodeDiverged, Message: "hub vault diverged from the remote; run lw sync on the hub host"}
				}
				return nil
			}
			lastHubRefresh = time.Now()
			hubRefreshMu.Unlock()

			err := doPull(ctx)
			var hubErr *hub.Error
			if errors.As(err, &hubErr) {
				return hubErr
			}
			if err != nil {
				return err
			}
			if isHubDiverged(root) {
				return &hub.Error{Code: hub.CodeDiverged, Message: "hub vault diverged from the remote; run lw sync on the hub host"}
			}
			return nil
		},
		ImportReceipts: func(paths []string) error {
			return importReceipts(nil, root, false)
		},
		LWVersion:   version,
		VaultFormat: stage.FormatVersion,
	}

	engine, err = hub.New(hubOpts)
	if err != nil {
		return "", nil, nil, nil, 0, nil, fmt.Errorf("create hub engine: %w", err)
	}

	cfgServer := hubserver.Config{
		Users:       cfg.Hub.Users,
		DevicesPath: devicesPath,
		Log: func(format string, args ...any) {
			slog.Info(fmt.Sprintf(format, args...))
		},
	}
	handler, err = hubserver.New(engine, cfgServer)
	if err != nil {
		return "", nil, nil, nil, 0, nil, fmt.Errorf("create hub handler: %w", err)
	}

	return root, engine, handler, pusher, port, cfg.Hub.Users, nil
}

func cmdHubServe(args []string) error {
	fs := flag.NewFlagSet("hub serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { hubUsage(os.Stderr) }
	vaultPath := fs.String("vault", "", "vault root")
	portFlag := fs.Int("port", 0, "port to listen on (default: from [hub] or 8779)")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}

	root, _, handler, pusher, port, users, err := setupHubWiring(*vaultPath, *portFlag, "")
	if err != nil {
		return err
	}
	attachLoggingAt(root)

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}

	srv := &http.Server{
		Handler: handler,
	}

	fmt.Printf("lw hub: serving %s on http://%s (users: %s)\n", root, addr, strings.Join(users, ", "))

	stopTicker := make(chan struct{})
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				resetHubRefreshTimer()
			case <-stopTicker:
				return
			}
		}
	}()

	sigChan := make(chan os.Signal, 2)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- srv.Serve(ln)
	}()

	select {
	case <-sigChan:
		close(stopTicker)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = srv.Shutdown(shutdownCtx)
		cancel()
		pusher.Flush(20*time.Second, nil)
		return nil
	case err := <-serverErr:
		close(stopTicker)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func cmdHubStatus(args []string) error {
	fs := flag.NewFlagSet("hub status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { hubUsage(os.Stderr) }
	vaultPath := fs.String("vault", "", "vault root")
	if err := fs.Parse(args); err != nil {
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

	cfg, _ := config.Load()
	port := 8779
	var users []string
	if cfg != nil && cfg.Hub != nil {
		if cfg.Hub.Port > 0 {
			port = cfg.Hub.Port
		}
		users = cfg.Hub.Users
	}

	devicesPath := filepath.Join(config.ConfigDir(), "hub-devices.json")
	d, err := hubserver.OpenDevices(devicesPath)
	var approved, pending int
	if err == nil {
		records, _ := d.List()
		for _, r := range records {
			switch r.State {
			case hub.DeviceApproved:
				approved++
			case hub.DevicePending:
				pending++
			}
		}
	}

	fmt.Printf("port: %d\n", port)
	fmt.Printf("users: %s\n", strings.Join(users, ", "))
	fmt.Printf("devices: %d approved, %d pending\n", approved, pending)

	if isHubDiverged(absRoot) {
		fmt.Println("remote: diverged from remote: run lw sync on the hub host")
	} else {
		fmt.Println("remote: in sync")
	}
	return nil
}

func cmdHubDevices(args []string) error {
	fs := flag.NewFlagSet("hub devices", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { hubUsage(os.Stderr) }
	asJSON := fs.Bool("json", false, "emit machine-readable JSON instead of text")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}

	devicesPath := filepath.Join(config.ConfigDir(), "hub-devices.json")
	d, err := hubserver.OpenDevices(devicesPath)
	if err != nil {
		return fmt.Errorf("open devices: %w", err)
	}

	records, err := d.List()
	if err != nil {
		return fmt.Errorf("list devices: %w", err)
	}

	if *asJSON {
		b, err := json.MarshalIndent(records, "", "  ")
		if err != nil {
			return fmt.Errorf("json marshal: %w", err)
		}
		fmt.Println(string(b))
		return nil
	}

	sort.Slice(records, func(i, j int) bool {
		return records[i].Slug < records[j].Slug
	})

	fmt.Println("NAME  STATE  LAST SEEN  ADDR  PLUGIN  LAST ERROR")
	for _, r := range records {
		name := r.Slug
		if name == "" {
			name = "-"
		}
		state := r.State
		if state == "" {
			state = "-"
		}
		lastSeen := r.LastSeen
		if lastSeen == "" {
			lastSeen = "-"
		}
		addr := r.LastAddr
		if addr == "" {
			addr = "-"
		}
		plugin := "-"
		lastErr := "-"
		if r.Status != nil {
			if r.Status.PluginVersion != "" {
				plugin = r.Status.PluginVersion
			}
			if r.Status.LastError != "" {
				lastErr = r.Status.LastError
			}
		}
		fmt.Printf("%s  %s  %s  %s  %s  %s\n", name, state, lastSeen, addr, plugin, lastErr)
	}
	return nil
}

func cmdHubApprove(args []string) error {
	if len(args) == 0 || wantsHelp(args) {
		hubUsage(os.Stderr)
		return &exitError{code: 2}
	}
	target := args[0]
	devicesPath := filepath.Join(config.ConfigDir(), "hub-devices.json")
	d, err := hubserver.OpenDevices(devicesPath)
	if err != nil {
		return fmt.Errorf("open devices: %w", err)
	}
	rec, err := d.Approve(target)
	if err != nil {
		return fmt.Errorf("unknown device %q", target)
	}
	fmt.Printf("approved %s\n", rec.Slug)
	return nil
}

func cmdHubRevoke(args []string) error {
	if len(args) == 0 || wantsHelp(args) {
		hubUsage(os.Stderr)
		return &exitError{code: 2}
	}
	target := args[0]
	devicesPath := filepath.Join(config.ConfigDir(), "hub-devices.json")
	d, err := hubserver.OpenDevices(devicesPath)
	if err != nil {
		return fmt.Errorf("open devices: %w", err)
	}
	rec, err := d.Revoke(target)
	if err != nil {
		return fmt.Errorf("unknown device %q", target)
	}
	fmt.Printf("revoked %s\n", rec.Slug)
	return nil
}

func cmdHubInstall(args []string) error {
	fs := flag.NewFlagSet("hub install", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { hubUsage(os.Stderr) }
	printFlag := fs.Bool("print", false, "print systemd user unit and tailscale serve commands")
	vaultPath := fs.String("vault", "", "vault root")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}

	if !*printFlag {
		fmt.Fprintln(os.Stderr, "lw hub install: only --print is supported")
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

	cfg, _ := config.Load()
	port := 8779
	if cfg != nil && cfg.Hub != nil && cfg.Hub.Port > 0 {
		port = cfg.Hub.Port
	}

	userName := os.Getenv("USER")
	if u, err := user.Current(); err == nil && u.Username != "" {
		userName = u.Username
	}
	if userName == "" {
		userName = "user"
	}

	fmt.Printf(`# ~/.config/systemd/user/lw-hub.service
[Unit]
Description=llmwiki phone sync hub
After=network.target

[Service]
Type=simple
ExecStart=%s hub serve --vault %s
Restart=on-failure

[Install]
WantedBy=default.target

# Commands to enable and run:
tailscale serve --bg --https=%d http://127.0.0.1:%d
systemctl --user daemon-reload
systemctl --user enable --now lw-hub.service
loginctl enable-linger %s
`, absExe, absRoot, port, port, userName)

	return nil
}
