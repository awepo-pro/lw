package hub_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	home := t.TempDir()

	gitCmd := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+home)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\noutput: %s", args, err, out)
		}
	}

	gitCmd("init")
	gitCmd("config", "user.name", "Hub Tester")
	gitCmd("config", "user.email", "hub-tester@example.com")
	gitCmd("config", "commit.gpgsign", "false")

	initFile := filepath.Join(dir, ".gitkeep")
	if err := os.WriteFile(initFile, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd("add", ".gitkeep")
	gitCmd("commit", "-m", "initial commit")

	return dir
}

func initEmptyGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	home := t.TempDir()

	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git init failed: %v\noutput: %s", err, out)
	}
	return dir
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	home := t.TempDir()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s failed: %v\noutput: %s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}
