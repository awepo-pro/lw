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
	"unicode/utf8"
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
	topLevel := strings.TrimSpace(string(out))

	absDir, err := filepath.Abs(o.Dir)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path of %q: %w", o.Dir, err)
	}

	realTop, err := filepath.EvalSymlinks(topLevel)
	if err != nil {
		return nil, fmt.Errorf("eval symlinks %q: %w", topLevel, err)
	}
	realDir, err := filepath.EvalSymlinks(absDir)
	if err != nil {
		return nil, fmt.Errorf("eval symlinks %q: %w", absDir, err)
	}

	if filepath.Clean(realTop) != filepath.Clean(realDir) {
		return nil, fmt.Errorf("dir %q is not a git work tree root (top-level is %q)", o.Dir, topLevel)
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
		if err := checkSymlinksAndParents(e.opts.Dir, relSlash); err != nil {
			return nil
		}
		fi, err := os.Lstat(p)
		if err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
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
		fi, lstatErr := os.Lstat(full)
		if lstatErr == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				continue
			}
			if err := checkSymlinksAndParents(e.opts.Dir, p); err != nil {
				continue
			}
			if !fi.Mode().IsRegular() {
				continue
			}
			data, err := os.ReadFile(full)
			if err == nil {
				sha := hexSHA256(data)
				changed = append(changed, Change{Path: p, SHA256: &sha})
			}
		} else if os.IsNotExist(lstatErr) {
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

	if err := checkSymlinksAndParents(e.opts.Dir, p); err != nil {
		return PutResult{}, err
	}
	if err := checkCaseCollision(e.opts.Dir, p); err != nil {
		return PutResult{}, err
	}

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

	if exists && currentSHA == bodySHA {
		result = ResultApplied
		finalSHA = bodySHA
	} else if base == BaseNew {
		if !exists {
			if err := writeAtomic(e.opts.Dir, p, body); err != nil {
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
					if err := writeAtomic(e.opts.Dir, p, mergedBytes); err != nil {
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
	} else {
		if !exists {
			if err := writeAtomic(e.opts.Dir, p, body); err != nil {
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
				if err := writeAtomic(e.opts.Dir, p, body); err != nil {
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
						if err := writeAtomic(e.opts.Dir, p, mergedBytes); err != nil {
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

	var importErr error
	if needImportReceipts && e.opts.ImportReceipts != nil {
		importErr = e.opts.ImportReceipts([]string{p})
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

	if importErr != nil {
		return PutResult{}, importErr
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
	if p == "ledger/receipts" || strings.HasPrefix(p, "ledger/receipts/") {
		return DeleteResult{}, &Error{Code: CodeOutOfScope, Message: "DELETE under ledger/receipts is not allowed"}
	}
	if !isValidBase(base) {
		return DeleteResult{}, &Error{Code: CodeBadRequest, Message: "invalid base"}
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

	if err := checkSymlinksAndParents(e.opts.Dir, p); err != nil {
		return DeleteResult{}, err
	}
	if err := checkCaseCollision(e.opts.Dir, p); err != nil {
		return DeleteResult{}, err
	}

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

func truncateUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	var lastValid int
	for offset := range s {
		if offset > maxBytes {
			break
		}
		lastValid = offset
	}
	_, size := utf8.DecodeRuneInString(s[lastValid:])
	if lastValid+size <= maxBytes {
		return s[:lastValid+size]
	}
	return s[:lastValid]
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

	targetDir := filepath.Join(e.opts.Dir, filepath.FromSlash(dir))
	entries, _ := os.ReadDir(targetDir)

	isTaken := func(name, full string) bool {
		for _, entry := range entries {
			if strings.EqualFold(entry.Name(), name) {
				return true
			}
		}
		if _, err := os.Lstat(full); !os.IsNotExist(err) {
			return true
		}
		return false
	}

	prefix := ""
	if dir != "." && dir != "" {
		prefix = dir + "/"
	}

	buildCandidate := func(seq int) (relCandidate string, candidateName string, full string) {
		var suffix string
		if seq == 1 {
			suffix = fmt.Sprintf(".conflict-%s-%s%s", dev.Name, timeStr, ext)
		} else {
			suffix = fmt.Sprintf(".conflict-%s-%s-%d%s", dev.Name, timeStr, seq, ext)
		}
		maxStemLen := 512 - len(prefix) - len(suffix)
		shortStem := truncateUTF8(stem, maxStemLen)
		candidateName = shortStem + suffix
		relCandidate = prefix + candidateName
		full = filepath.Join(e.opts.Dir, filepath.FromSlash(relCandidate))
		return relCandidate, candidateName, full
	}

	relCandidate, candidateName, full := buildCandidate(1)
	if isTaken(candidateName, full) {
		for i := 2; ; i++ {
			relCandidate, candidateName, full = buildCandidate(i)
			if !isTaken(candidateName, full) {
				break
			}
		}
	}

	if err := CheckPath(relCandidate); err != nil {
		return "", err
	}

	if err := writeAtomic(e.opts.Dir, relCandidate, body); err != nil {
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
		line = strings.TrimPrefix(line, "\xef\xbb\xbf")
		if line != "" {
			yLines[line] = true
		}
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
		line = strings.TrimPrefix(line, "\xef\xbb\xbf")
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

func writeAtomic(dir, relPath string, data []byte) error {
	segments := strings.Split(relPath, "/")
	curr := dir
	for i := 0; i < len(segments)-1; i++ {
		curr = filepath.Join(curr, segments[i])
		if err := os.Mkdir(curr, 0o755); err != nil && !os.IsExist(err) {
			return err
		}
		fi, err := os.Lstat(curr)
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return &Error{Code: CodeOutOfScope, Message: "symlinks are not permitted"}
		}
		if !fi.IsDir() {
			return &Error{Code: CodeOutOfScope, Message: "parent component is not a directory"}
		}
	}
	parent := curr
	fi, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return &Error{Code: CodeOutOfScope, Message: "symlinks are not permitted"}
	}
	if !fi.IsDir() {
		return &Error{Code: CodeOutOfScope, Message: "parent component is not a directory"}
	}

	targetPath := filepath.Join(parent, segments[len(segments)-1])
	if fi, err := os.Lstat(targetPath); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return &Error{Code: CodeOutOfScope, Message: "symlinks are not permitted"}
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
