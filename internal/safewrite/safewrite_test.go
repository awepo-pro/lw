package safewrite

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// W1: a write to a fresh path outside the vault succeeds and the bytes match.
func TestW1_FreshPath(t *testing.T) {
	vaultDir := t.TempDir()
	outDir := t.TempDir()

	g, err := NewGuard(vaultDir)
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}

	target := filepath.Join(outDir, "fresh.txt")
	payload := []byte("fresh content for W1")
	if err := g.WriteFile(target, payload); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("got %q, want %q", got, payload)
	}
}

// W2: a write whose target is a symlink to a vault file is refused and the vault file is byte-identical afterwards.
func TestW2_SymlinkToVaultFileRefused(t *testing.T) {
	vaultDir := t.TempDir()
	outDir := t.TempDir()

	vaultFile := filepath.Join(vaultDir, "secret.md")
	original := []byte("critical vault content")
	if err := os.WriteFile(vaultFile, original, 0644); err != nil {
		t.Fatalf("write vault file: %v", err)
	}

	linkPath := filepath.Join(outDir, "symlink-to-secret")
	if err := os.Symlink(vaultFile, linkPath); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	g, err := NewGuard(vaultDir)
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}

	err = g.WriteFile(linkPath, []byte("overwritten malicious payload"))
	if err == nil {
		t.Fatalf("WriteFile through symlink to vault file succeeded, want refusal")
	}

	after, err := os.ReadFile(vaultFile)
	if err != nil {
		t.Fatalf("read vault file: %v", err)
	}
	if !bytes.Equal(after, original) {
		t.Errorf("vault file was modified: got %q, want %q", after, original)
	}
}

// W3: a symlink to a file OUTSIDE the vault is ALSO refused (no write through any symlink).
func TestW3_SymlinkOutsideVaultRefused(t *testing.T) {
	vaultDir := t.TempDir()
	outDir := t.TempDir()
	outsideDir := t.TempDir()

	outsideFile := filepath.Join(outsideDir, "outside.txt")
	original := []byte("outside file original")
	if err := os.WriteFile(outsideFile, original, 0644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	linkPath := filepath.Join(outDir, "symlink-outside")
	if err := os.Symlink(outsideFile, linkPath); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	g, err := NewGuard(vaultDir)
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}

	err = g.WriteFile(linkPath, []byte("overwritten payload"))
	if err == nil {
		t.Fatalf("WriteFile through symlink outside vault succeeded, want refusal")
	}

	after, err := os.ReadFile(outsideFile)
	if err != nil {
		t.Fatalf("read outside file: %v", err)
	}
	if !bytes.Equal(after, original) {
		t.Errorf("outside file was modified: got %q, want %q", after, original)
	}
}

// W4: a target inside a symlinked directory that resolves into the vault is refused.
func TestW4_TargetInsideSymlinkedDirToVaultRefused(t *testing.T) {
	vaultDir := t.TempDir()
	outDir := t.TempDir()

	vaultSub := filepath.Join(vaultDir, "wiki")
	if err := os.MkdirAll(vaultSub, 0755); err != nil {
		t.Fatalf("mkdir vaultSub: %v", err)
	}

	linkDir := filepath.Join(outDir, "wiki-symlink")
	if err := os.Symlink(vaultSub, linkDir); err != nil {
		t.Fatalf("create symlink dir: %v", err)
	}

	g, err := NewGuard(vaultDir)
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}

	target := filepath.Join(linkDir, "intruder.md")
	err = g.WriteFile(target, []byte("payload inside vault through symlinked dir"))
	if err == nil {
		t.Fatalf("WriteFile inside symlinked dir pointing into vault succeeded, want refusal")
	}

	realTarget := filepath.Join(vaultSub, "intruder.md")
	if _, err := os.Stat(realTarget); !os.IsNotExist(err) {
		t.Errorf("file %s was created inside vault", realTarget)
	}
}

// W5: a path whose parent is the vault root (via ..) is refused.
func TestW5_ParentIsVaultRootViaDotDotRefused(t *testing.T) {
	vaultDir := t.TempDir()
	subDir := filepath.Join(vaultDir, "sub")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("mkdir subDir: %v", err)
	}

	g, err := NewGuard(vaultDir)
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}

	target := filepath.Join(subDir, "..", "escaped.md")
	err = g.WriteFile(target, []byte("payload"))
	if err == nil {
		t.Fatalf("WriteFile with parent resolving to vault root succeeded, want refusal")
	}

	expectedPath := filepath.Join(vaultDir, "escaped.md")
	if _, err := os.Stat(expectedPath); !os.IsNotExist(err) {
		t.Errorf("file %s was created in vault", expectedPath)
	}
}

// W6: "/tmp/v2/x" is allowed when the vault is "/tmp/v" (prefix trap).
func TestW6_PrefixTrapAllowed(t *testing.T) {
	baseDir := t.TempDir()
	vaultDir := filepath.Join(baseDir, "v")
	v2Dir := filepath.Join(baseDir, "v2")

	if err := os.MkdirAll(vaultDir, 0755); err != nil {
		t.Fatalf("mkdir vaultDir: %v", err)
	}
	if err := os.MkdirAll(v2Dir, 0755); err != nil {
		t.Fatalf("mkdir v2Dir: %v", err)
	}

	g, err := NewGuard(vaultDir)
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}

	target := filepath.Join(v2Dir, "x")
	payload := []byte("v2 payload")
	if err := g.WriteFile(target, payload); err != nil {
		t.Fatalf("WriteFile to v2/x failed: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("got %q, want %q", got, payload)
	}
}

// W7: overwrite of an existing regular file outside the vault works and leaves no temp files behind.
func TestW7_OverwriteExistingRegularFile(t *testing.T) {
	vaultDir := t.TempDir()
	outDir := t.TempDir()

	target := filepath.Join(outDir, "existing.txt")
	if err := os.WriteFile(target, []byte("initial data"), 0644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}

	g, err := NewGuard(vaultDir)
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}

	updated := []byte("updated data")
	if err := g.WriteFile(target, updated); err != nil {
		t.Fatalf("WriteFile overwrite failed: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if !bytes.Equal(got, updated) {
		t.Errorf("got %q, want %q", got, updated)
	}

	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatalf("read outDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "existing.txt" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("unexpected files left in outDir: %v", names)
	}
}

// W8: the target is a directory -> refused.
func TestW8_TargetIsDirectoryRefused(t *testing.T) {
	vaultDir := t.TempDir()
	outDir := t.TempDir()

	subDir := filepath.Join(outDir, "sub")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("mkdir subDir: %v", err)
	}

	g, err := NewGuard(vaultDir)
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}

	err = g.WriteFile(subDir, []byte("payload"))
	if err == nil {
		t.Fatalf("WriteFile to directory succeeded, want refusal")
	}

	fi, err := os.Stat(subDir)
	if err != nil {
		t.Fatalf("stat subDir: %v", err)
	}
	if !fi.IsDir() {
		t.Errorf("subDir is no longer a directory")
	}
}

func TestCheckDir(t *testing.T) {
	vaultDir := t.TempDir()
	outDir := t.TempDir()

	g, err := NewGuard(vaultDir)
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}

	if err := g.CheckDir(outDir); err != nil {
		t.Errorf("CheckDir(outDir) failed: %v", err)
	}

	if err := g.CheckDir(vaultDir); err == nil {
		t.Errorf("CheckDir(vaultDir) succeeded, want refusal")
	}

	vaultSub := filepath.Join(vaultDir, "sub")
	if err := os.MkdirAll(vaultSub, 0755); err != nil {
		t.Fatal(err)
	}
	if err := g.CheckDir(vaultSub); err == nil {
		t.Errorf("CheckDir(vaultSub) succeeded, want refusal")
	}

	linkToVault := filepath.Join(outDir, "link-to-vault")
	if err := os.Symlink(vaultDir, linkToVault); err != nil {
		t.Fatal(err)
	}
	if err := g.CheckDir(linkToVault); err == nil {
		t.Errorf("CheckDir(symlink to vault) succeeded, want refusal")
	}
}
