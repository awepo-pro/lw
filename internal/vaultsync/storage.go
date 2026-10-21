package vaultsync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SyncStorage synchronizes non-wiki storage files between the local vault and the remote.
func SyncStorage(ctx context.Context, o Options) error {
	if len(o.Remotes) == 0 {
		return nil
	}
	r, err := newRunner(o)
	if err != nil {
		return err
	}

	localStorageDir := filepath.Join(r.o.Dir, "storage")
	localManifest := filepath.Join(r.o.Dir, ".llmwiki", "storage.json")
	localHasStorage := isDir(localStorageDir)
	localHasManifest := isRegularFile(localManifest)

	var lastErr error
	for _, spec := range r.o.Remotes {
		rem, err := parseRemote(spec)
		if err != nil {
			lastErr = err
			continue
		}

		if !localHasStorage && !localHasManifest {
			hasRemote, err := r.remoteHasStorage(ctx, rem)
			if err != nil {
				lastErr = err
				continue
			}
			if !hasRemote {
				return nil
			}
		}

		switch rem.kind {
		case kindLocal:
			err = r.syncLocalStorage(ctx, rem)
			if err != nil {
				lastErr = err
				continue
			}
			return nil
		case kindSSH:
			err = r.syncSSHStorage(ctx, rem)
			if err != nil {
				lastErr = err
				continue
			}
			return nil
		default:
			return nil
		}
	}
	return lastErr
}

func (r *runner) remoteHasStorage(ctx context.Context, rem remote) (bool, error) {
	switch rem.kind {
	case kindLocal:
		remoteStorageDir := rem.arg
		if strings.HasPrefix(remoteStorageDir, "file://") {
			remoteStorageDir = strings.TrimPrefix(remoteStorageDir, "file://")
		}
		remoteStorageDir = strings.TrimSuffix(remoteStorageDir, "/") + ".storage"
		return isDir(remoteStorageDir), nil
	case kindSSH:
		remoteStorageDir := rem.path + ".storage"
		var args []string
		if !r.o.Interactive {
			args = append(args, "-o", "BatchMode=yes", "-o", "ConnectTimeout=5")
		}
		if rem.port != "" {
			args = append(args, "-p", rem.port)
		}
		args = append(args, "--", rem.dest, "test -d "+remoteStorageDir)
		_, _, err := r.runSSH(ctx, args)
		if err != nil {
			return false, nil
		}
		return true, nil
	default:
		return false, nil
	}
}

func (r *runner) syncLocalStorage(ctx context.Context, rem remote) error {
	remotePath := rem.arg
	if strings.HasPrefix(remotePath, "file://") {
		remotePath = strings.TrimPrefix(remotePath, "file://")
	}
	remoteStorageDir := strings.TrimSuffix(remotePath, "/") + ".storage"
	localStorageDir := filepath.Join(r.o.Dir, "storage")

	localExists := isDir(localStorageDir)
	remoteExists := isDir(remoteStorageDir)

	if !localExists && !remoteExists {
		return nil
	}

	if localExists {
		if err := copyMissingOrDifferent(ctx, localStorageDir, remoteStorageDir); err != nil {
			return err
		}
	}

	if isDir(remoteStorageDir) {
		if err := copyMissingOrDifferent(ctx, remoteStorageDir, localStorageDir); err != nil {
			return err
		}
	}
	return nil
}

func (r *runner) syncSSHStorage(ctx context.Context, rem remote) error {
	rsyncPath, err := exec.LookPath("rsync")
	if err != nil {
		return fmt.Errorf("storage sync: rsync not found on PATH: %w", err)
	}

	remoteStorageDir := rem.path + ".storage"

	var sshOpts []string
	if !r.o.Interactive {
		sshOpts = append(sshOpts, "-o", "BatchMode=yes", "-o", "ConnectTimeout=5")
	}
	if rem.port != "" {
		sshOpts = append(sshOpts, "-p", rem.port)
	}

	// Ensure remote directory exists
	var mkdirArgs []string
	mkdirArgs = append(mkdirArgs, sshOpts...)
	mkdirArgs = append(mkdirArgs, "--", rem.dest, "mkdir -p -- "+remoteStorageDir)
	if _, _, err := r.runSSH(ctx, mkdirArgs); err != nil {
		return fmt.Errorf("storage sync: ensure remote dir: %w", err)
	}

	localStorageDir := filepath.Join(r.o.Dir, "storage")
	if err := os.MkdirAll(localStorageDir, 0o755); err != nil {
		return fmt.Errorf("storage sync: create local storage dir: %w", err)
	}

	sshBase := r.sshCommand(ctx)
	var eParts []string
	eParts = append(eParts, sshBase)
	if !r.o.Interactive {
		eParts = append(eParts, "-o BatchMode=yes -o ConnectTimeout=5")
	}
	if rem.port != "" {
		eParts = append(eParts, "-p "+rem.port)
	}
	sshEOpt := strings.Join(eParts, " ")

	// Pull missing remote files
	pullArgs := []string{"-a", "--ignore-existing", "-e", sshEOpt, rem.dest + ":" + remoteStorageDir + "/", filepath.Clean(localStorageDir) + "/"}
	if _, _, err := r.procErr(ctx, rsyncPath, call{args: pullArgs, net: true}); err != nil {
		return fmt.Errorf("storage sync pull: %w", err)
	}

	// Push local files
	pushArgs := []string{"-a", "--ignore-existing", "-e", sshEOpt, filepath.Clean(localStorageDir) + "/", rem.dest + ":" + remoteStorageDir + "/"}
	if _, _, err := r.procErr(ctx, rsyncPath, call{args: pushArgs, net: true}); err != nil {
		return fmt.Errorf("storage sync push: %w", err)
	}

	return nil
}

func copyMissingOrDifferent(ctx context.Context, srcDir, destDir string) error {
	return filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		name := d.Name()
		if d.IsDir() {
			if path != srcDir && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".tmp") {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}

		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}

		srcInfo, err := d.Info()
		if err != nil {
			return err
		}

		destPath := filepath.Join(destDir, rel)
		destInfo, err := os.Stat(destPath)
		if err == nil {
			if destInfo.Size() == srcInfo.Size() {
				return nil
			}
			if !srcInfo.ModTime().After(destInfo.ModTime()) {
				return nil
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}

		return copyFileAtomic(path, destPath)
	})
}

func copyFileAtomic(srcPath, destPath string) error {
	destDir := filepath.Dir(destPath)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("create dir %s: %w", destDir, err)
	}

	tmp, err := os.CreateTemp(destDir, ".storage-tmp-*")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", destDir, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()

	src, err := os.Open(srcPath)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("open %s: %w", srcPath, err)
	}
	defer src.Close()

	var mtime time.Time
	if info, err := src.Stat(); err == nil {
		_ = os.Chmod(tmpName, info.Mode().Perm())
		mtime = info.ModTime()
	}

	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("copy %s: %w", srcPath, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}

	if err := os.Rename(tmpName, destPath); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmpName, destPath, err)
	}
	tmpName = ""
	if !mtime.IsZero() {
		_ = os.Chtimes(destPath, mtime, mtime)
	}
	return nil
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func isRegularFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}
