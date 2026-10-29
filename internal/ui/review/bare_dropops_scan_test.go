package review

import (
	"bufio"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoProductionBareDropOps pins 069 S2: no production Go file in the
// repository may call bare DropOps( (internal/stage/drop_cascade.go's wrapper
// declaration and test files are exempt). All callers must use DropOpsWith
// to record the drop actor and reason.
func TestNoProductionBareDropOps(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}

	var violations []string

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "testdata" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}

		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		// internal/stage/drop_cascade.go is the engine's own DropOps wrapper declaration.
		if filepath.ToSlash(rel) == "internal/stage/drop_cascade.go" {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		scanner := bufio.NewScanner(bytes.NewReader(data))
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()
			// Check for call to DropOps(
			if strings.Contains(line, "DropOps(") {
				violations = append(violations, rel+":"+strings.TrimSpace(line))
			}
		}
		return scanner.Err()
	})

	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}

	if len(violations) > 0 {
		t.Errorf("found bare DropOps( calls in production files:\n%s", strings.Join(violations, "\n"))
	}
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
