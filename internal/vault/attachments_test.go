package vault

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// The 033 scaling tests: attachment hashes are answered from a persistent
// stat cache so lint and every staging op cost O(changed files), not
// O(vault bytes).

// attachmentCacheBlob is the fake original the cache tests hash — big
// enough that an accidental read-through would be visible in a profile,
// small enough that the test stays instant.
var attachmentCacheBlob = bytes.Repeat([]byte("%PDF-1.6 attachment-cache probe\n"), 64)

// TestAttachmentSHA256Cached pins the cache contract: the first call
// reads and hashes, a second call with unchanged size+mtime answers from
// the persistent cache WITHOUT opening the file, a real edit (size
// change) is detected, and the cache lives at .llmwiki/cache/attachments.json.
func TestAttachmentSHA256Cached(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	if err := os.MkdirAll(filepath.Join(dir, "raw"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "raw", "probe.pdf")
	if err := os.WriteFile(p, attachmentCacheBlob, 0o644); err != nil {
		t.Fatal(err)
	}

	v, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	sum := sha256.Sum256(attachmentCacheBlob)
	want := hex.EncodeToString(sum[:])
	got, err := v.AttachmentSHA256("raw/probe.pdf")
	if err != nil {
		t.Fatalf("first AttachmentSHA256: %v", err)
	}
	if got != want {
		t.Fatalf("first sha = %s, want %s", got, want)
	}

	// Prove the second call is answered without opening the file: mode 000
	// makes any read-through fail with a permission error, while a cached
	// lookup (stat + size/mtime match) still succeeds. Root ignores file
	// modes, so there the assertion would prove nothing.
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		t.Log("running as root; chmod 000 cannot make the file unreadable, skipping the no-open assertion")
	} else {
		got, err = v.AttachmentSHA256("raw/probe.pdf")
		if err != nil {
			t.Fatalf("cached AttachmentSHA256 after chmod 000: %v — the cache did not answer without a read", err)
		}
		if got != want {
			t.Fatalf("cached sha = %s, want %s", got, want)
		}
	}

	// A real edit — size changes when a byte is appended — must move the
	// sha even though mtime alone would also have flagged it.
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	edited := append(append([]byte{}, attachmentCacheBlob...), 'X')
	if err := os.WriteFile(p, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	sum2 := sha256.Sum256(edited)
	got, err = v.AttachmentSHA256("raw/probe.pdf")
	if err != nil {
		t.Fatalf("post-edit AttachmentSHA256: %v", err)
	}
	if want2 := hex.EncodeToString(sum2[:]); got != want2 {
		t.Fatalf("post-edit sha = %s, want %s (a size change must invalidate the cached entry)", got, want2)
	}

	if _, err := os.Stat(filepath.Join(dir, ".llmwiki", "cache", "attachments.json")); err != nil {
		t.Fatalf("persistent cache not at .llmwiki/cache/attachments.json: %v", err)
	}
}

// TestAttachmentSHA256Missing: a path with no file behind it is an
// fs.ErrNotExist, from both a disk vault and a plain FS vault.
func TestAttachmentSHA256Missing(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")

	disk, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := disk.AttachmentSHA256("raw/nope.pdf"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("disk vault missing attachment = %v, want fs.ErrNotExist", err)
	}

	fsv, err := OpenFS(os.DirFS(dir))
	if err != nil {
		t.Fatalf("OpenFS: %v", err)
	}
	if _, err := fsv.AttachmentSHA256("raw/nope.pdf"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("FS vault missing attachment = %v, want fs.ErrNotExist", err)
	}
}
