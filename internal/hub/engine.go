package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var ledgerDayRegex = regexp.MustCompile(`^ledger/[0-9]{4}/[0-9]{2}/[0-9]{4}-[0-9]{2}-[0-9]{2}\.md$`)

// Engine implements Service.
type Engine struct {
	opts Options
}

// New validates that o.Dir is a git work tree and returns a new *Engine.
func New(o Options) (*Engine, error) {
	cmd := exec.Command("git", "-C", o.Dir, "rev-parse", "--show-toplevel")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("dir %q is not a git work tree: %w (output: %s)", o.Dir, err, strings.TrimSpace(string(out)))
	}
	return &Engine{opts: o}, nil
}

func (e *Engine) getHead() string {
	cmd := exec.Command("git", "-C", e.opts.Dir, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Hello answers GET /v1/hello.
func (e *Engine) Hello(ctx context.Context) (Hello, error) {
	return Hello{
		Protocol:    ProtocolVersion,
		LWVersion:   e.opts.LWVersion,
		VaultFormat: e.opts.VaultFormat,
		Head:        e.getHead(),
		Device:      "approved",
	}, nil
}

// Read returns the bytes at path and their hex SHA-256.
func (e *Engine) Read(ctx context.Context, p string) ([]byte, string, error) {
	if err := CheckPath(p); err != nil {
		return nil, "", err
	}
	if err := checkSymlinksAndParents(e.opts.Dir, p); err != nil {
		return nil, "", err
	}
	full := filepath.Join(e.opts.Dir, filepath.FromSlash(p))
	data, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", &Error{Code: CodeNotFound, Message: "file not found"}
		}
		return nil, "", err
	}
	return data, hexSHA256(data), nil
}

// Manifest answers GET /v1/manifest.
func (e *Engine) Manifest(ctx context.Context) (Manifest, error) {
	if e.opts.Refresh != nil {
		if err := e.opts.Refresh(ctx); err != nil {
			var hubErr *Error
			if errors.As(err, &hubErr) {
				return Manifest{}, hubErr
			}
			// Other errors ignored
		}
	}
	files, err := e.collectManifestFiles()
	if err != nil {
		return Manifest{}, err
	}
	return Manifest{
		Head:  e.getHead(),
		Files: files,
	}, nil
}

func (e *Engine) collectManifestFiles() ([]FileEntry, error) {
	files := make([]FileEntry, 0)
	err := filepath.WalkDir(e.opts.Dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != e.opts.Dir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(e.opts.Dir, p)
		if err != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if CheckPath(relSlash) != nil {
			return nil
		}
		fi, err := d.Info()
		if err != nil || !fi.Mode().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		files = append(files, FileEntry{
			Path:   relSlash,
			SHA256: hexSHA256(data),
			Size:   fi.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})
	return files, nil
}

// Changes answers GET /v1/changes.
func (e *Engine) Changes(ctx context.Context, since string) (Changes, error) {
	if e.opts.Refresh != nil {
		if err := e.opts.Refresh(ctx); err != nil {
			var hubErr *Error
			if errors.As(err, &hubErr) {
				return Changes{}, hubErr
			}
			// Other errors ignored
		}
	}

	isFull := false
	if since == "" || len(since) < 4 || len(since) > 64 || !isHex(since) {
		isFull = true
	} else {
		cmd := exec.Command("git", "-C", e.opts.Dir, "cat-file", "-e", since+"^{commit}")
		if err := cmd.Run(); err != nil {
			isFull = true
		}
	}

	if isFull {
		files, err := e.collectManifestFiles()
		if err != nil {
			return Changes{}, err
		}
		return Changes{
			Head:    e.getHead(),
			Full:    true,
			Changed: make([]Change, 0),
			Files:   files,
		}, nil
	}

	cmd := exec.Command("git", "-C", e.opts.Dir, "diff", "--name-only", "-z", "--no-renames", since, "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return Changes{}, err
	}

	changed := make([]Change, 0)
	parts := bytes.Split(out, []byte{0})
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		p := string(part)
		if CheckPath(p) != nil {
			continue
		}
		full := filepath.Join(e.opts.Dir, filepath.FromSlash(p))
		data, err := os.ReadFile(full)
		if err == nil {
			sha := hexSHA256(data)
			changed = append(changed, Change{Path: p, SHA256: &sha})
		} else if os.IsNotExist(err) {
			changed = append(changed, Change{Path: p, SHA256: nil})
		}
	}

	sort.Slice(changed, func(i, j int) bool {
		return changed[i].Path < changed[j].Path
	})

	return Changes{
		Head:    e.getHead(),
		Full:    false,
		Changed: changed,
		Files:   make([]FileEntry, 0),
	}, nil
}

// Put answers PUT /v1/file.
func (e *Engine) Put(ctx context.Context, dev Device, p, base string, body []byte) (PutResult, error) {
	if err := CheckPath(p); err != nil {
		return PutResult{}, err
	}
	if !isValidBase(base) {
		return PutResult{}, &Error{Code: CodeBadRequest, Message: "invalid base"}
	}
	if len(body) > MaxBodyBytes {
		return PutResult{}, &Error{Code: CodeTooLarge, Message: "body too large"}
	}
	if err := checkSymlinksAndParents(e.opts.Dir, p); err != nil {
		return PutResult{}, err
	}
	if err := checkCaseCollision(e.opts.Dir, p); err != nil {
		return PutResult{}, err
	}

	var unlock func()
	if e.opts.Lock != nil {
		u, err := e.opts.Lock()
		if err != nil {
			return PutResult{}, err
		}
		unlock = u
	}
	releaseLock := func() {
		if unlock != nil {
			unlock()
			unlock = nil
		}
	}
	defer releaseLock()

	fullPath := filepath.Join(e.opts.Dir, filepath.FromSlash(p))
	currentBytes, readErr := os.ReadFile(fullPath)
	exists := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return PutResult{}, readErr
	}

	var currentSHA string
	if exists {
		currentSHA = hexSHA256(currentBytes)
	}
	bodySHA := hexSHA256(body)

	var result string
	var conflictPath string
	var finalSHA string
	var commitMsg string
	var needImportReceipts bool

	isReceipt := strings.HasPrefix(p, "ledger/receipts/")
	isDay := isLedgerDayFile(p)

	if base == BaseNew {
		if !exists {
			if err := writeAtomic(fullPath, body); err != nil {
				return PutResult{}, err
			}
			result = ResultApplied
			finalSHA = bodySHA
			if isReceipt {
				needImportReceipts = true
				commitMsg = fmt.Sprintf("hub: %s receipt %s", dev.Name, p)
			} else {
				commitMsg = fmt.Sprintf("hub: %s put %s", dev.Name, p)
			}
		} else {
			if currentSHA == bodySHA {
				result = ResultApplied
				finalSHA = bodySHA
			} else {
				if isDay {
					mergedBytes, changed := mergeLedgerDay(currentBytes, body)
					result = ResultMerged
					if changed {
						if err := writeAtomic(fullPath, mergedBytes); err != nil {
							return PutResult{}, err
						}
						finalSHA = hexSHA256(mergedBytes)
						commitMsg = fmt.Sprintf("hub: %s merge %s", dev.Name, p)
					} else {
						finalSHA = currentSHA
					}
				} else {
					cPath, err := e.createConflictCopy(dev, p, body)
					if err != nil {
						return PutResult{}, err
					}
					result = ResultConflictCopy
					conflictPath = cPath
					finalSHA = currentSHA
					commitMsg = fmt.Sprintf("hub: %s conflict %s -> %s", dev.Name, p, cPath)
				}
			}
		}
	} else {
		if !exists {
			if err := writeAtomic(fullPath, body); err != nil {
				return PutResult{}, err
			}
			result = ResultResurrected
			finalSHA = bodySHA
			if isReceipt {
				needImportReceipts = true
				commitMsg = fmt.Sprintf("hub: %s receipt %s", dev.Name, p)
			} else {
				commitMsg = fmt.Sprintf("hub: %s resurrect %s", dev.Name, p)
			}
		} else {
			if base == currentSHA {
				if err := writeAtomic(fullPath, body); err != nil {
					return PutResult{}, err
				}
				result = ResultApplied
				finalSHA = bodySHA
				if isReceipt {
					needImportReceipts = true
					commitMsg = fmt.Sprintf("hub: %s receipt %s", dev.Name, p)
				} else {
					commitMsg = fmt.Sprintf("hub: %s put %s", dev.Name, p)
				}
			} else {
				if isDay {
					mergedBytes, changed := mergeLedgerDay(currentBytes, body)
					result = ResultMerged
					if changed {
						if err := writeAtomic(fullPath, mergedBytes); err != nil {
							return PutResult{}, err
						}
						finalSHA = hexSHA256(mergedBytes)
						commitMsg = fmt.Sprintf("hub: %s merge %s", dev.Name, p)
					} else {
						finalSHA = currentSHA
					}
				} else {
					cPath, err := e.createConflictCopy(dev, p, body)
					if err != nil {
						return PutResult{}, err
					}
					result = ResultConflictCopy
					conflictPath = cPath
					finalSHA = currentSHA
					commitMsg = fmt.Sprintf("hub: %s conflict %s -> %s", dev.Name, p, cPath)
				}
			}
		}
	}

	releaseLock()

	if needImportReceipts && e.opts.ImportReceipts != nil {
		if err := e.opts.ImportReceipts([]string{p}); err != nil {
			return PutResult{}, err
		}
	}

	var commitErr error
	if commitMsg != "" && e.opts.Commit != nil {
		commitErr = e.opts.Commit(commitMsg)
	}

	res := PutResult{
		Result:       result,
		Path:         p,
		SHA256:       finalSHA,
		ConflictPath: conflictPath,
		Head:         e.getHead(),
	}

	if commitErr != nil {
		var hubErr *Error
		if errors.As(commitErr, &hubErr) {
			return res, hubErr
		}
		return res, commitErr
	}
	return res, nil
}

// Delete answers DELETE /v1/file.
func (e *Engine) Delete(ctx context.Context, dev Device, p, base string) (DeleteResult, error) {
	if err := CheckPath(p); err != nil {
		return DeleteResult{}, err
	}
	if strings.HasPrefix(p, "ledger/receipts/") {
		return DeleteResult{}, &Error{Code: CodeOutOfScope, Message: "DELETE under ledger/receipts/ is not allowed"}
	}
	if !isValidBase(base) {
		return DeleteResult{}, &Error{Code: CodeBadRequest, Message: "invalid base"}
	}
	if err := checkSymlinksAndParents(e.opts.Dir, p); err != nil {
		return DeleteResult{}, err
	}
	if err := checkCaseCollision(e.opts.Dir, p); err != nil {
		return DeleteResult{}, err
	}

	var unlock func()
	if e.opts.Lock != nil {
		u, err := e.opts.Lock()
		if err != nil {
			return DeleteResult{}, err
		}
		unlock = u
	}
	releaseLock := func() {
		if unlock != nil {
			unlock()
			unlock = nil
		}
	}
	defer releaseLock()

	fullPath := filepath.Join(e.opts.Dir, filepath.FromSlash(p))
	currentBytes, readErr := os.ReadFile(fullPath)
	exists := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return DeleteResult{}, readErr
	}

	var result string
	var commitMsg string

	if !exists {
		result = ResultAlreadyGone
	} else {
		currentSHA := hexSHA256(currentBytes)
		if base == currentSHA {
			if err := os.Remove(fullPath); err != nil {
				return DeleteResult{}, err
			}
			result = ResultDeleted
			commitMsg = fmt.Sprintf("hub: %s delete %s", dev.Name, p)
		} else {
			result = ResultRefusedChanged
		}
	}

	releaseLock()

	var commitErr error
	if commitMsg != "" && e.opts.Commit != nil {
		commitErr = e.opts.Commit(commitMsg)
	}

	res := DeleteResult{
		Result: result,
		Head:   e.getHead(),
	}

	if commitErr != nil {
		var hubErr *Error
		if errors.As(commitErr, &hubErr) {
			return res, hubErr
		}
		return res, commitErr
	}
	return res, nil
}

func (e *Engine) createConflictCopy(dev Device, relPath string, body []byte) (string, error) {
	now := time.Now().UTC()
	if e.opts.Now != nil {
		now = e.opts.Now().UTC()
	}
	timeStr := now.Format("20060102-150405")
	dir := path.Dir(relPath)
	base := path.Base(relPath)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)

	candidateName := fmt.Sprintf("%s.conflict-%s-%s%s", stem, dev.Name, timeStr, ext)
	var relCandidate string
	if dir == "." || dir == "" {
		relCandidate = candidateName
	} else {
		relCandidate = path.Join(dir, candidateName)
	}

	full := filepath.Join(e.opts.Dir, filepath.FromSlash(relCandidate))
	if _, err := os.Lstat(full); !os.IsNotExist(err) {
		for i := 2; ; i++ {
			candidateName = fmt.Sprintf("%s.conflict-%s-%s-%d%s", stem, dev.Name, timeStr, i, ext)
			if dir == "." || dir == "" {
				relCandidate = candidateName
			} else {
				relCandidate = path.Join(dir, candidateName)
			}
			full = filepath.Join(e.opts.Dir, filepath.FromSlash(relCandidate))
			if _, err := os.Lstat(full); os.IsNotExist(err) {
				break
			}
		}
	}

	if err := writeAtomic(full, body); err != nil {
		return "", err
	}
	return relCandidate, nil
}

func isLedgerDayFile(p string) bool {
	return ledgerDayRegex.MatchString(p)
}

func mergeLedgerDay(current, phone []byte) ([]byte, bool) {
	yStr := string(current)
	yLines := make(map[string]bool)
	for _, line := range strings.Split(yStr, "\n") {
		line = strings.TrimSuffix(line, "\r")
		yLines[line] = true
	}

	result := yStr
	if len(result) > 0 && !strings.HasSuffix(result, "\n") {
		result += "\n"
	}

	phoneLines := strings.Split(string(phone), "\n")
	seenPhone := make(map[string]bool)
	appendedCount := 0

	for _, rawLine := range phoneLines {
		line := strings.TrimSuffix(rawLine, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if yLines[line] {
			continue
		}
		if seenPhone[line] {
			continue
		}
		seenPhone[line] = true
		result += line + "\n"
		appendedCount++
	}

	if appendedCount == 0 {
		return current, false
	}
	return []byte(result), true
}

func writeAtomic(targetPath string, data []byte) error {
	parent := filepath.Dir(targetPath)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(parent, ".hub-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, targetPath); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func checkSymlinksAndParents(dir, relPath string) error {
	segments := strings.Split(relPath, "/")
	curr := dir
	for i, seg := range segments {
		curr = filepath.Join(curr, seg)
		fi, err := os.Lstat(curr)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return &Error{Code: CodeOutOfScope, Message: "symlinks are not permitted"}
		}
		if i < len(segments)-1 {
			if !fi.IsDir() {
				return &Error{Code: CodeOutOfScope, Message: "parent component is not a directory"}
			}
		} else {
			if fi.IsDir() {
				return &Error{Code: CodeOutOfScope, Message: "target path is a directory"}
			}
		}
	}
	return nil
}

func checkCaseCollision(dir, relPath string) error {
	segments := strings.Split(relPath, "/")
	curr := dir
	for _, seg := range segments {
		entries, err := os.ReadDir(curr)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return nil
		}
		for _, entry := range entries {
			name := entry.Name()
			if strings.EqualFold(name, seg) && name != seg {
				return &Error{
					Code:    CodeCaseCollision,
					Message: fmt.Sprintf("case collision: %q conflicts with existing %q", seg, name),
				}
			}
		}
		curr = filepath.Join(curr, seg)
	}
	return nil
}

func isValidBase(base string) bool {
	if base == BaseNew {
		return true
	}
	if len(base) != 64 {
		return false
	}
	for i := 0; i < len(base); i++ {
		c := base[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func hexSHA256(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
