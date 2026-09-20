// Package e2e drives the real lw binary end to end: TestMain builds it once
// into a temp directory shared by the whole package, harness_test.go runs it
// as a subprocess under a minimal environment with an isolated vault and
// config, and fakellm_test.go scripts the OpenAI-compatible SSE endpoint it
// talks to. Task 6's smoke scenarios build on these helpers.
//
// This is a test-only package — every file is a _test.go file — so nothing
// here can enter the product's build graph.
package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// lwBin is the lw binary runMain built, or "" when the build failed (runLW
// refuses to exec an empty path, so every test reports a clear failure
// instead of a confusing "no such file").
var lwBin string

// e2eVersion is the version stamped into the binary under test: the same
// `git describe --tags --always --dirty` string the Makefile's VERSION
// (Makefile:2) stamps — same value, same -X target — so the suite and
// `make build` produce byte-identical `lw --version` output. Both fall back
// to 0.0.0-unknown outside a git tree. Set in runMain before the build.
var e2eVersion string

// goTool is the absolute go toolchain path, resolved once at init: LookPath
// first, then GOROOT/bin/go. The old literal /usr/local/go/bin/go was this
// repo's tool-shell fallback — no tool-invoked shell here has go on PATH
// (CLAUDE.md), so lookup-by-name used to be a guaranteed miss — but it
// hardcoded one machine's install. GOROOT is always set for a real go
// binary, and bin/go under it is the same toolchain that is running these
// tests, so the binary built for the suite is built by the toolchain under
// test wherever the suite runs.
var goTool = func() string {
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	return filepath.Join(runtime.GOROOT(), "bin", "go")
}()

// gitDescribe mirrors the Makefile's VERSION: `git describe --tags --always
// --dirty` at the module root, or the shared 0.0.0-unknown fallback when git
// is unavailable or the tree has no history. Both stamping sites compute the
// same string, so no literal is pinned anywhere.
func gitDescribe(root string) string {
	git, err := exec.LookPath("git")
	if err != nil {
		return "0.0.0-unknown"
	}
	out, err := exec.Command(git, "-C", root, "describe", "--tags", "--always", "--dirty").Output()
	if err != nil {
		return "0.0.0-unknown"
	}
	return strings.TrimSpace(string(out))
}

// TestMain builds ./cmd/lw once, points lwBin at it, and runs the package.
func TestMain(m *testing.M) {
	os.Exit(runMain(m))
}

// runMain builds the binary into a temp directory shared by every test in
// the package, then runs m and removes the directory on the way out. A build
// failure prints the compiler's output and returns a non-zero exit code of
// its own.
func runMain(m *testing.M) int {
	root, err := moduleRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	tmp, err := os.MkdirTemp("", "lw-e2e-build-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: create build directory: %v\n", err)
		return 1
	}
	defer os.RemoveAll(tmp)

	bin := filepath.Join(tmp, "lw")
	// e2eVersion is the Makefile's VERSION computed the same way (gitDescribe):
	// stamping here is how the suite exercises the same -ldflags
	// "-X main.version=..." path `make build` takes, and the "lw "+e2eVersion
	// assertions in harness_test.go and coldstart_test.go depend on the two
	// staying equal.
	e2eVersion = gitDescribe(root)
	build := exec.Command(goTool, "build",
		"-ldflags", "-X main.version="+e2eVersion,
		"-o", bin, "./cmd/lw")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: build ./cmd/lw: %v\n%s", err, out)
		return 1
	}
	lwBin = bin

	return m.Run()
}

// moduleRoot returns the repository root — the nearest ancestor holding
// go.mod — so the harness always builds the same tree `go test` ran. It
// walks up from the working directory first (where `go test ./internal/e2e/`
// puts it) and falls back to this file's own directory, so invoking the
// compiled test binary from elsewhere still works.
func moduleRoot() (string, error) {
	var starts []string
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	if _, file, _, ok := runtime.Caller(0); ok {
		starts = append(starts, filepath.Dir(file))
	}

	for _, start := range starts {
		for dir := start; ; dir = filepath.Dir(dir) {
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				return dir, nil
			}
			if parent := filepath.Dir(dir); parent == dir {
				break
			}
		}
	}
	return "", fmt.Errorf("e2e: no go.mod found above %v", starts)
}
