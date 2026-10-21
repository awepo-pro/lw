package vaultsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

// 066 S2: the content-addressed object store that replaces 062's name mirror
// (D3). Every test that moves bytes runs against a local remote (a directory)
// and against the fake ssh from helpers_test.go, which runs the remote command
// in a real sh — so the POSIX scripts a server would run are the ones tested.

// objOf describes content as the catalog would: its sha256 and length.
func objOf(content string) Object {
	sum := sha256.Sum256([]byte(content))
	return Object{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(content))}
}

// objFile is where the store keeps obj under root = <remote>.storage.
func objFile(root string, o Object) string {
	return filepath.Join(root, "objects", o.SHA256[:2], o.SHA256[2:])
}

// srcFile writes content to a fresh file and returns its path.
func srcFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "src.bin")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// bigContent is deterministic, larger than a pipe buffer and not periodic at
// any power of two, so a stream that drops or repeats a block cannot pass.
func bigContent() string {
	b := make([]byte, 300_000)
	for i := range b {
		b[i] = byte(i*7 + i/251)
	}
	return string(b)
}

// storeCase is one way of reaching a remote: a directory or the fake ssh.
type storeCase struct {
	name string
	o    Options
	spec string // the remote exactly as written
	root string // <remote>.storage as the file system sees it
	log  string // the fake ssh's log; "" for a local remote
}

func localStore(t *testing.T) storeCase {
	t.Helper()
	hermetic(t)
	spec := filepath.Join(t.TempDir(), "remote.git")
	return storeCase{name: "local", o: opts(t.TempDir(), spec), spec: spec, root: spec + ".storage"}
}

// sshStore reaches the remote through the fake ssh. The remote path is given
// as the user would write it after "host:"; root is where it lands under HOME.
func sshStore(t *testing.T, remotePath string, rootUnderHome ...string) storeCase {
	t.Helper()
	home := hermetic(t)
	log := installFakeSSH(t)
	spec := "fakehost:" + remotePath
	return storeCase{
		name: "ssh",
		o:    opts(t.TempDir(), spec),
		spec: spec,
		root: filepath.Join(append([]string{home}, rootUnderHome...)...),
		log:  log,
	}
}

// eachStore runs fn against a local remote and an ssh remote whose path has a
// space and a single quote in it — the characters an unquoted path breaks on.
func eachStore(t *testing.T, fn func(t *testing.T, c storeCase)) {
	t.Helper()
	t.Run("local", func(t *testing.T) { fn(t, localStore(t)) })
	t.Run("ssh", func(t *testing.T) {
		fn(t, sshStore(t, "remote dir/it's remote.git", "remote dir", "it's remote.git.storage"))
	})
}

// noTemp fails the test when anything under root is a temp file: a transfer
// that did not finish must leave nothing behind but what it was asked to keep.
func noTemp(t *testing.T, root string) {
	t.Helper()
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.Contains(d.Name(), "tmp") {
			t.Errorf("temp file left behind: %s", p)
		}
		return nil
	})
}

// noObjects fails the test when root holds any object file.
func noObjects(t *testing.T, root string) {
	t.Helper()
	filepath.WalkDir(filepath.Join(root, "objects"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			t.Errorf("object published: %s", p)
		}
		return nil
	})
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// roundTrip puts, asks for, and gets back several objects: an empty one, a
// short one and one far larger than a pipe buffer.
func roundTrip(t *testing.T, c storeCase) {
	ctx := t.Context()
	contents := []string{"", "alpha\n", "beta\nwith a second line\n", bigContent()}
	var objs []Object
	for _, s := range contents {
		objs = append(objs, objOf(s))
	}
	missing := objOf("never uploaded")

	// A question changes nothing: no store directory appears.
	have, remote, err := HaveObjects(ctx, c.o, append([]Object{missing}, objs...))
	if err != nil {
		t.Fatalf("HaveObjects on an empty remote: %v", err)
	}
	if len(have) != 0 || remote != c.spec {
		t.Errorf("empty remote: have=%v remote=%q, want none and %q", have, remote, c.spec)
	}
	if exists(c.root) {
		t.Errorf("HaveObjects created %s", c.root)
	}

	for i, s := range contents {
		if err := PutObject(ctx, c.o, objs[i], srcFile(t, s)); err != nil {
			t.Fatalf("PutObject(%d bytes): %v", len(s), err)
		}
		// The layout is the contract: <root>/objects/<sha[0:2]>/<sha[2:]>.
		if got, err := os.ReadFile(objFile(c.root, objs[i])); err != nil || string(got) != s {
			t.Fatalf("object file for %d bytes: err=%v, %d bytes", len(s), err, len(got))
		}
	}
	noTemp(t, c.root)

	have, remote, err = HaveObjects(ctx, c.o, append([]Object{missing}, objs...))
	if err != nil {
		t.Fatalf("HaveObjects: %v", err)
	}
	if remote != c.spec {
		t.Errorf("remote = %q, want %q", remote, c.spec)
	}
	if len(have) != len(objs) || have[missing.SHA256] {
		t.Errorf("have = %v, want exactly the %d uploaded objects", have, len(objs))
	}
	for _, o := range objs {
		if !have[o.SHA256] {
			t.Errorf("object %s (%d bytes) reported missing", o.SHA256[:8], o.Size)
		}
	}

	for i, s := range contents {
		var buf bytes.Buffer
		if err := GetObject(ctx, c.o, objs[i], &buf); err != nil {
			t.Fatalf("GetObject(%d bytes): %v", len(s), err)
		}
		if buf.String() != s {
			t.Errorf("GetObject(%d bytes) returned %d different bytes", len(s), buf.Len())
		}
	}

	// A second upload is a no-op, not an error — also for a payload far larger
	// than a pipe buffer, where the remote answers before the sender has finished.
	for _, i := range []int{1, 3} {
		if err := PutObject(ctx, c.o, objs[i], srcFile(t, contents[i])); err != nil {
			t.Errorf("second PutObject(%d bytes): %v", len(contents[i]), err)
		}
		if got := readFile(t, objFile(c.root, objs[i])); got != contents[i] {
			t.Errorf("second PutObject(%d bytes) changed the object", len(contents[i]))
		}
	}
	noTemp(t, c.root)
}

func TestObjectStoreLocalRoundTrip(t *testing.T) {
	roundTrip(t, localStore(t))
}

// fake ssh, a remote path with a space and a ' in it: every path must be
// single-quote escaped or the remote shell splits it (the bug an unquoted
// "mkdir -p "+path would have).
func TestObjectStoreSSHRoundTrip(t *testing.T) {
	c := sshStore(t, "remote dir/it's remote.git", "remote dir", "it's remote.git.storage")
	roundTrip(t, c)
	log := readFile(t, c.log)
	if !strings.Contains(log, `it'\''s remote.git.storage`) {
		t.Errorf("the remote path reached ssh unescaped:\n%s", log)
	}
	// Same ssh invocation as git's: non-interactive, multiplexed, "--" before the host.
	for _, want := range []string{"[BatchMode=yes]", "[ConnectTimeout=5]", "[ControlMaster=auto]", "[--] [fakehost]"} {
		if !strings.Contains(log, want) {
			t.Errorf("ssh argv lacks %s:\n%s", want, log)
		}
	}
}

// The forms of remote a user writes all name the same <remote>.storage.
func TestObjectStoreRemoteForms(t *testing.T) {
	obj := objOf("form check")
	t.Run("ssh", func(t *testing.T) {
		for _, tc := range []struct {
			remotePath string
			under      []string
		}{
			{"plain.git", []string{"plain.git.storage"}},
			{"plain.git/", []string{"plain.git.storage"}}, // git treats a trailing slash as the same repository
			{"~/tilde dir/it's.git", []string{"tilde dir", "it's.git.storage"}},
			{"nested/deeper/repo.git", []string{"nested", "deeper", "repo.git.storage"}},
		} {
			t.Run(tc.remotePath, func(t *testing.T) {
				c := sshStore(t, tc.remotePath, tc.under...)
				if err := PutObject(t.Context(), c.o, obj, srcFile(t, "form check")); err != nil {
					t.Fatalf("PutObject: %v", err)
				}
				if got, err := os.ReadFile(objFile(c.root, obj)); err != nil || string(got) != "form check" {
					t.Errorf("object not at %s: %v", objFile(c.root, obj), err)
				}
			})
		}
	})
	t.Run("ssh:// url", func(t *testing.T) {
		home := hermetic(t)
		installFakeSSH(t)
		o := opts(t.TempDir(), "ssh://fakehost:2222/~/url dir/r.git")
		if err := PutObject(t.Context(), o, obj, srcFile(t, "form check")); err != nil {
			t.Fatalf("PutObject: %v", err)
		}
		if got, err := os.ReadFile(objFile(filepath.Join(home, "url dir", "r.git.storage"), obj)); err != nil || string(got) != "form check" {
			t.Errorf("object not under HOME/url dir: %v", err)
		}
	})
	t.Run("local", func(t *testing.T) {
		hermetic(t)
		base := t.TempDir()
		for _, spec := range []string{
			filepath.Join(base, "bare.git"),
			filepath.Join(base, "bare.git") + "/",
			"file://" + filepath.Join(base, "bare.git"),
		} {
			if err := PutObject(t.Context(), opts(t.TempDir(), spec), obj, srcFile(t, "form check")); err != nil {
				t.Fatalf("PutObject(%s): %v", spec, err)
			}
			if got, err := os.ReadFile(objFile(filepath.Join(base, "bare.git.storage"), obj)); err != nil || string(got) != "form check" {
				t.Errorf("remote %q: object not at bare.git.storage: %v", spec, err)
			}
		}
	})
	t.Run("GIT_SSH_COMMAND", func(t *testing.T) {
		// A user's own ssh command line runs through sh -c '<cmd> "$@"'; the
		// remote command, its quoting and the object bytes on stdin must survive.
		c := sshStore(t, "remote dir/it's remote.git", "remote dir", "it's remote.git.storage")
		t.Setenv("GIT_SSH_COMMAND", "ssh -o StrictHostKeyChecking=no")
		roundTrip(t, c)
	})
}

// An interactive call (lw sync run by hand, a password or Access login may be
// asked for) drops BatchMode and the timeout but moves the same bytes.
func TestObjectStoreInteractive(t *testing.T) {
	c := sshStore(t, "remote dir/it's remote.git", "remote dir", "it's remote.git.storage")
	var stderr bytes.Buffer
	c.o.Interactive, c.o.Stderr = true, &stderr
	roundTrip(t, c)
	log := readFile(t, c.log)
	if strings.Contains(log, "BatchMode") || strings.Contains(log, "ConnectTimeout") {
		t.Errorf("an interactive call carried the non-interactive options:\n%s", log)
	}
	if !strings.Contains(log, "[ControlMaster=auto]") {
		t.Errorf("an interactive call is not multiplexed:\n%s", log)
	}
}

func TestStoreRoot(t *testing.T) {
	cases := []struct{ spec, want string }{
		{"/srv/vault.git", "/srv/vault.git.storage"},
		{"/srv/vault.git/", "/srv/vault.git.storage"},
		{"file:///srv/vault.git", "/srv/vault.git.storage"},
		{"file:///srv/my%20vault.git", "/srv/my vault.git.storage"},
		{"host:vault.git", "vault.git.storage"},
		{"host:vault.git/", "vault.git.storage"},
		{"host:/srv/vault.git", "/srv/vault.git.storage"},
		{"host:~/vault.git", "~/vault.git.storage"},
		{"ssh://host/srv/vault.git", "/srv/vault.git.storage"},
		{"ssh://host/~/vault.git", "~/vault.git.storage"},
	}
	for _, tc := range cases {
		rem, err := parseRemote(tc.spec)
		if err != nil {
			t.Fatalf("parseRemote(%q): %v", tc.spec, err)
		}
		if got := rem.storeRoot(); got != tc.want {
			t.Errorf("storeRoot(%q) = %q, want %q", tc.spec, got, tc.want)
		}
	}
}

// B6's transport half: HaveObjects is ONE ssh call for N objects. 062 asked
// per file; a Cloudflare-tunnelled connect costs seconds each.
func TestHaveObjectsOneRoundTrip(t *testing.T) {
	c := sshStore(t, "remote dir/it's remote.git", "remote dir", "it's remote.git.storage")
	ctx := t.Context()
	var objs []Object
	for i := 0; i < 5; i++ {
		o := objOf("object number " + strconv.Itoa(i))
		objs = append(objs, o)
		if i%2 == 0 { // 0, 2 and 4 are on the remote
			if err := PutObject(ctx, c.o, o, srcFile(t, "object number "+strconv.Itoa(i))); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A duplicate in the question is asked once.
	ask := append(append([]Object{}, objs...), objs[0])

	if err := os.WriteFile(c.log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	have, _, err := HaveObjects(ctx, c.o, ask)
	if err != nil {
		t.Fatalf("HaveObjects: %v", err)
	}
	if n := strings.Count(readFile(t, c.log), "argv:"); n != 1 {
		t.Errorf("HaveObjects for %d objects made %d ssh calls, want 1:\n%s", len(ask), n, readFile(t, c.log))
	}
	for i, o := range objs {
		if have[o.SHA256] != (i%2 == 0) {
			t.Errorf("object %d: have=%v, want %v", i, have[o.SHA256], i%2 == 0)
		}
	}

	// And it stays one call for a catalog of hundreds: the list travels on
	// stdin, not in the command line (a 128 KiB argument limit would cut it).
	many := make([]Object, 0, 3000)
	for i := 0; i < 3000; i++ {
		many = append(many, objOf("bulk "+strconv.Itoa(i)))
	}
	many = append(many, objs[0])
	if err := os.WriteFile(c.log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	have, _, err = HaveObjects(ctx, c.o, many)
	if err != nil {
		t.Fatalf("HaveObjects(3001): %v", err)
	}
	if n := strings.Count(readFile(t, c.log), "argv:"); n != 1 {
		t.Errorf("HaveObjects for %d objects made %d ssh calls, want 1", len(many), n)
	}
	if len(have) != 1 || !have[objs[0].SHA256] {
		t.Errorf("have = %d entries, want only objs[0]", len(have))
	}

	// PutObject and GetObject are one call each as well.
	if err := os.WriteFile(c.log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	extra := objOf("one more")
	if err := PutObject(ctx, c.o, extra, srcFile(t, "one more")); err != nil {
		t.Fatal(err)
	}
	if err := GetObject(ctx, c.o, extra, io.Discard); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(readFile(t, c.log), "argv:"); n != 2 {
		t.Errorf("Put+Get made %d ssh calls, want 2", n)
	}
}

// R9 / B6: a source that is not the bytes the catalog names publishes nothing.
func TestPutObjectShortReadLeavesNothing(t *testing.T) {
	eachStore(t, func(t *testing.T, c storeCase) {
		obj := objOf("0123456789")
		err := PutObject(t.Context(), c.o, obj, srcFile(t, "0123")) // 4 of 10 bytes
		if err == nil {
			t.Fatal("PutObject accepted a source shorter than Size")
		}
		if !strings.Contains(err.Error(), "4 bytes") {
			t.Errorf("error does not say how short the source was: %v", err)
		}
		noObjects(t, c.root)
		noTemp(t, c.root)
	})
}

func TestPutObjectLongerSourceLeavesNothing(t *testing.T) {
	eachStore(t, func(t *testing.T, c storeCase) {
		obj := objOf("0123456789")
		if err := PutObject(t.Context(), c.o, obj, srcFile(t, "0123456789 and more")); err == nil {
			t.Fatal("PutObject accepted a source longer than Size")
		}
		noObjects(t, c.root)
		noTemp(t, c.root)
	})
}

// The sender hashes what it sends: a file edited since the catalog recorded it
// (same size, other bytes) must not be published under the old name.
func TestPutObjectWrongBytesPublishesNothing(t *testing.T) {
	eachStore(t, func(t *testing.T, c storeCase) {
		obj := objOf("0123456789")
		err := PutObject(t.Context(), c.o, obj, srcFile(t, "ABCDEFGHIJ")) // right size, wrong sha
		if err == nil {
			t.Fatal("PutObject published bytes that do not hash to obj.SHA256")
		}
		var re *RemoteError
		if errors.As(err, &re) {
			t.Errorf("a wrong source was reported as a remote failure: %v", err)
		}
		if !strings.Contains(err.Error(), obj.SHA256) {
			t.Errorf("error does not name the expected sha256: %v", err)
		}
		noObjects(t, c.root)
		noTemp(t, c.root)
	})
}

func TestPutObjectMissingSource(t *testing.T) {
	eachStore(t, func(t *testing.T, c storeCase) {
		err := PutObject(t.Context(), c.o, objOf("x"), filepath.Join(t.TempDir(), "absent"))
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("PutObject of a missing file = %v, want an os.ErrNotExist error", err)
		}
		noObjects(t, c.root)
	})
}

// Objects are write-once (M2): an object already there is left exactly as it is.
func TestPutObjectExistingIsKept(t *testing.T) {
	eachStore(t, func(t *testing.T, c storeCase) {
		obj := objOf("the real bytes")
		p := objFile(c.root, obj)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("OLD"), 0o644); err != nil {
			t.Fatal(err)
		}
		past := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
		if err := os.Chtimes(p, past, past); err != nil {
			t.Fatal(err)
		}
		if err := PutObject(t.Context(), c.o, obj, srcFile(t, "the real bytes")); err != nil {
			t.Fatalf("PutObject over an existing object: %v", err)
		}
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, p); got != "OLD" || !fi.ModTime().Equal(past) {
			t.Errorf("an existing object was overwritten: %q, mtime %v", got, fi.ModTime())
		}
		noTemp(t, c.root)
	})
}

func TestGetObjectMissing(t *testing.T) {
	eachStore(t, func(t *testing.T, c storeCase) {
		ctx := t.Context()
		var buf bytes.Buffer
		err := GetObject(ctx, c.o, objOf("nothing here"), &buf)
		if !errors.Is(err, ErrObjectMissing) {
			t.Errorf("GetObject on an empty remote = %v, want ErrObjectMissing", err)
		}
		if buf.Len() != 0 {
			t.Errorf("a missing object wrote %d bytes", buf.Len())
		}
		// A store that has other objects is no different.
		other := objOf("something else")
		if err := PutObject(ctx, c.o, other, srcFile(t, "something else")); err != nil {
			t.Fatal(err)
		}
		if err := GetObject(ctx, c.o, objOf("still nothing"), &buf); !errors.Is(err, ErrObjectMissing) {
			t.Errorf("GetObject beside other objects = %v, want ErrObjectMissing", err)
		}
	})
}

// A remote object whose length is not the catalog's is an error, but not the
// "has not arrived" error: the caller must not wait for it.
func TestGetObjectWrongLength(t *testing.T) {
	eachStore(t, func(t *testing.T, c storeCase) {
		obj := objOf("0123456789")
		p := objFile(c.root, obj)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("0123"), 0o644); err != nil {
			t.Fatal(err)
		}
		err := GetObject(t.Context(), c.o, obj, io.Discard)
		if err == nil || errors.Is(err, ErrObjectMissing) {
			t.Errorf("GetObject of a truncated remote object = %v, want a length error", err)
		}
	})
}

// Once bytes have gone to w, the next remote is not tried: its payload would
// be appended to the first one's and the caller would verify a splice.
func TestGetObjectNoFailoverAfterBytes(t *testing.T) {
	hermetic(t)
	first := filepath.Join(t.TempDir(), "first.git")
	second := filepath.Join(t.TempDir(), "second.git")
	obj := objOf("0123456789")
	for root, body := range map[string]string{first + ".storage": "0123", second + ".storage": "0123456789"} {
		p := objFile(root, obj)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	err := GetObject(t.Context(), opts(t.TempDir(), first, second), obj, &buf)
	if err == nil {
		t.Fatalf("GetObject = nil with a truncated first remote, wrote %q", buf.String())
	}
	if buf.String() != "0123" {
		t.Errorf("w holds %q, want only the first remote's 4 bytes (no splice)", buf.String())
	}
	// A remote that has nothing written yet is a different matter: it falls through.
	buf.Reset()
	empty := filepath.Join(t.TempDir(), "empty.git")
	if err := GetObject(t.Context(), opts(t.TempDir(), empty, second), obj, &buf); err != nil || buf.String() != "0123456789" {
		t.Errorf("GetObject past a remote that lacks the object = %v, %q", err, buf.String())
	}
}

// R10: a v2.49.0 <remote>.storage/f.bin is neither read nor modified.
func TestLegacyNameLayoutUntouched(t *testing.T) {
	eachStore(t, func(t *testing.T, c storeCase) {
		ctx := t.Context()
		legacy := map[string]string{
			"f.bin":             "legacy payload of f.bin",
			"docs/paper.pdf":    "legacy nested payload",
			"objects-lookalike": "a name that is not the objects directory",
		}
		past := time.Date(2021, 5, 6, 7, 8, 9, 0, time.UTC)
		for name, body := range legacy {
			p := filepath.Join(c.root, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(p, past, past); err != nil {
				t.Fatal(err)
			}
		}
		check := func(when string) {
			t.Helper()
			for name, body := range legacy {
				p := filepath.Join(c.root, filepath.FromSlash(name))
				fi, err := os.Stat(p)
				if err != nil || readFile(t, p) != body || !fi.ModTime().Equal(past) {
					t.Errorf("%s: legacy %s changed (err=%v)", when, name, err)
				}
			}
		}

		// The legacy bytes are not an object: the store never reads them.
		legacyAsObject := objOf(legacy["f.bin"])
		have, _, err := HaveObjects(ctx, c.o, []Object{legacyAsObject})
		if err != nil || have[legacyAsObject.SHA256] {
			t.Errorf("HaveObjects served a legacy file as an object: have=%v err=%v", have, err)
		}
		if err := GetObject(ctx, c.o, legacyAsObject, io.Discard); !errors.Is(err, ErrObjectMissing) {
			t.Errorf("GetObject of legacy bytes = %v, want ErrObjectMissing", err)
		}
		check("after Have/Get")

		fresh := objOf("a brand new object")
		if err := PutObject(ctx, c.o, fresh, srcFile(t, "a brand new object")); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := GetObject(ctx, c.o, fresh, &buf); err != nil || buf.String() != "a brand new object" {
			t.Errorf("GetObject after Put: %v, %q", err, buf.String())
		}
		check("after Put/Get")
	})
}

func TestObjectRejectsBadDescription(t *testing.T) {
	c := sshStore(t, "r.git", "r.git.storage")
	for _, o := range []Object{
		{SHA256: "", Size: 1},
		{SHA256: "../../etc/passwd", Size: 1},
		{SHA256: strings.Repeat("g", 64), Size: 1},
		{SHA256: strings.Repeat("A", 64), Size: 1}, // upper case: catalog shas are lower case
		{SHA256: strings.Repeat("a", 63), Size: 1},
		{SHA256: strings.Repeat("a", 64), Size: -1},
	} {
		if _, _, err := HaveObjects(t.Context(), c.o, []Object{o}); err == nil {
			t.Errorf("HaveObjects accepted %+v", o)
		}
		if err := PutObject(t.Context(), c.o, o, srcFile(t, "x")); err == nil {
			t.Errorf("PutObject accepted %+v", o)
		}
		if err := GetObject(t.Context(), c.o, o, io.Discard); err == nil {
			t.Errorf("GetObject accepted %+v", o)
		}
	}
	if strings.Contains(readFile(t, c.log), "argv:") {
		t.Errorf("a malformed object description reached ssh:\n%s", readFile(t, c.log))
	}
}

func TestObjectNoUsableRemote(t *testing.T) {
	hermetic(t)
	obj := objOf("x")
	for name, o := range map[string]Options{
		"no remotes":      {Dir: t.TempDir()},
		"https remote":    opts(t.TempDir(), "https://example.invalid/vault.git"),
		"unparsable spec": opts(t.TempDir(), "-oProxyCommand=evil"),
	} {
		t.Run(name, func(t *testing.T) {
			var re *RemoteError
			if _, _, err := HaveObjects(t.Context(), o, []Object{obj}); !errors.As(err, &re) {
				t.Errorf("HaveObjects = %v, want a *RemoteError", err)
			}
			if err := PutObject(t.Context(), o, obj, srcFile(t, "x")); !errors.As(err, &re) {
				t.Errorf("PutObject = %v, want a *RemoteError", err)
			}
			if err := GetObject(t.Context(), o, obj, io.Discard); !errors.As(err, &re) || errors.Is(err, ErrObjectMissing) {
				t.Errorf("GetObject = %v, want a *RemoteError that is not ErrObjectMissing", err)
			}
		})
	}
	// Nothing to ask is nothing to do, with or without a remote.
	if have, remote, err := HaveObjects(t.Context(), Options{Dir: t.TempDir()}, nil); err != nil || len(have) != 0 || remote != "" {
		t.Errorf("HaveObjects(nil) = %v, %q, %v", have, remote, err)
	}
}

// Remotes are tried in order, like git's: an unreachable first one does not
// stop the second from answering.
func TestObjectFailsOverToNextRemote(t *testing.T) {
	hermetic(t)
	installFakeSSH(t)
	good := filepath.Join(t.TempDir(), "second.git")
	o := opts(t.TempDir(), "deadhost:down.git", good)
	obj := objOf("fail over")
	ctx := t.Context()

	if err := PutObject(ctx, o, obj, srcFile(t, "fail over")); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	if got := readFile(t, objFile(good+".storage", obj)); got != "fail over" {
		t.Errorf("object not on the second remote: %q", got)
	}
	have, remote, err := HaveObjects(ctx, o, []Object{obj})
	if err != nil || !have[obj.SHA256] || remote != good {
		t.Errorf("HaveObjects = %v, %q, %v; want the second remote to answer", have, remote, err)
	}
	var buf bytes.Buffer
	if err := GetObject(ctx, o, obj, &buf); err != nil || buf.String() != "fail over" {
		t.Errorf("GetObject = %v, %q", err, buf.String())
	}

	// Every remote down: the error names each and is a *RemoteError.
	down := opts(t.TempDir(), "deadhost:a.git", "deadhost2:b.git")
	var re *RemoteError
	if _, _, err := HaveObjects(ctx, down, []Object{obj}); !errors.As(err, &re) || len(re.Tried) != 2 {
		t.Errorf("HaveObjects with every remote down = %v, want a *RemoteError naming both", err)
	}
}

func TestObjectCancelled(t *testing.T) {
	c := localStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := PutObject(ctx, c.o, objOf("x"), srcFile(t, "x")); !errors.Is(err, context.Canceled) {
		t.Errorf("PutObject with a cancelled context = %v, want context.Canceled", err)
	}
	if _, _, err := HaveObjects(ctx, c.o, []Object{objOf("x")}); !errors.Is(err, context.Canceled) {
		t.Errorf("HaveObjects with a cancelled context = %v, want context.Canceled", err)
	}
}

// A transfer is not a git call: 30 s would cut a 200 MB PDF off, so the bound
// grows with the size.
func TestTransferTimeout(t *testing.T) {
	r := &runner{o: Options{Timeout: 10 * time.Second}}
	if got := r.transferTimeout(0); got != 10*time.Second {
		t.Errorf("transferTimeout(0) = %v, want the call timeout", got)
	}
	if small, big := r.transferTimeout(1<<10), r.transferTimeout(512<<20); !(big > small+10*time.Minute) {
		t.Errorf("transferTimeout(512 MiB) = %v, not much more than (1 KiB) = %v", big, small)
	}
	if got := r.transferTimeout(1 << 62); got <= 0 || got > 24*time.Hour {
		t.Errorf("transferTimeout(huge) = %v, want a bounded positive duration", got)
	}
}

// The remote scripts travel inside sh -c '...' on the remote shell's command
// line, so they must hold no single quote (and no "!", which an interactive
// csh-family shell would expand).
func TestObjectScriptsAreQuoteFree(t *testing.T) {
	for name, s := range map[string]string{"have": haveScript, "put": putScript, "get": getScript} {
		if strings.ContainsAny(s, "'!") {
			t.Errorf("%sScript contains a single quote or '!'", name)
		}
	}
}

// runScript runs a remote script locally, as the remote sh would.
func runScript(t *testing.T, script, stdin string, args ...string) (stdout string, code int) {
	t.Helper()
	cmd := exec.Command("sh", append([]string{"-c", script, "sh"}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.String(), ee.ExitCode()
	}
	if err != nil {
		t.Fatal(err)
	}
	return out.String(), 0
}

// The remote half of the put: the byte count is checked there, so a stream
// the connection cut short can never become an object, whatever the sender did.
func TestPutScriptChecksByteCount(t *testing.T) {
	root := filepath.Join(t.TempDir(), "st ore's root")
	obj := objOf("hello")
	rel := objRel(obj)
	final := filepath.Join(root, "objects", filepath.FromSlash(rel))
	run := func(stdin string, size int) int {
		_, code := runScript(t, putScript, stdin, root, rel, strconv.Itoa(size), "tmpname.1")
		return code
	}

	if code := run("hell", 5); code == 0 || exists(final) {
		t.Errorf("a short stream: exit %d, object exists=%v; want a failure and no object", code, exists(final))
	}
	noTemp(t, root)
	if code := run("hello!", 5); code == 0 || exists(final) {
		t.Errorf("a long stream: exit %d, object exists=%v; want a failure and no object", code, exists(final))
	}
	noTemp(t, root)
	if code := run("hello", 5); code != 0 || readFile(t, final) != "hello" {
		t.Errorf("an exact stream: exit %d, object %q; want 0 and hello", code, readFile(t, final))
	}
	noTemp(t, root)

	// Existing object: untouched, exit 0.
	if err := os.WriteFile(final, []byte("OLD!!"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run("hello", 5); code != 0 || readFile(t, final) != "OLD!!" {
		t.Errorf("an existing object: exit %d, object %q; want 0 and OLD!!", code, readFile(t, final))
	}
	// ...and is decided before any byte is read: a stream that would fail the
	// count check is not even looked at, so a re-upload costs a stat, not a transfer.
	if code := run("", 5); code != 0 || readFile(t, final) != "OLD!!" {
		t.Errorf("an existing object with a bad stream: exit %d, object %q; want 0 and OLD!!", code, readFile(t, final))
	}
	noTemp(t, root)

	// The empty object is a real object.
	empty := objOf("")
	if _, code := runScript(t, putScript, "", root, objRel(empty), "0", "tmpname.2"); code != 0 || !exists(filepath.Join(root, "objects", filepath.FromSlash(objRel(empty)))) {
		t.Errorf("the empty object: exit %d", code)
	}
	noTemp(t, root)
}

// Two PCs can upload the same object at once. The one that finishes second
// finds the object in place and leaves it alone (write-once), rather than
// replacing it, and cleans its own temp file up.
func TestPutScriptLosesRaceGracefully(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	obj := objOf("hello")
	rel := objRel(obj)
	final := filepath.Join(root, "objects", filepath.FromSlash(rel))

	cmd := exec.Command("sh", "-c", putScript, "sh", root, rel, "5", "racer.1")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(in, "hel"); err != nil {
		t.Fatal(err)
	}
	// The script is in cat > temp: its temp file exists. Now the other uploader wins.
	tmp := filepath.Join(filepath.Dir(final), ".tmp.racer.1")
	deadline := time.Now().Add(5 * time.Second)
	for !exists(tmp) {
		if time.Now().After(deadline) {
			t.Fatal("the script never created its temp file")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := os.WriteFile(final, []byte("WINNER"), 0o644); err != nil {
		t.Fatal(err)
	}
	io.WriteString(in, "lo")
	in.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the losing uploader failed: %v", err)
	}
	if got := readFile(t, final); got != "WINNER" {
		t.Errorf("the object was replaced by the late uploader: %q", got)
	}
	noTemp(t, root)
}

func TestHaveAndGetScripts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "st ore's root")
	a, b := objOf("aaa"), objOf("bbb")
	for _, o := range []Object{a, b} {
		p := filepath.Join(root, "objects", filepath.FromSlash(objRel(o)))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("body of "+o.SHA256[:4]), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	absent := objOf("ccc")
	out, code := runScript(t, haveScript, objRel(a)+"\n"+objRel(absent)+"\n"+objRel(b)+"\n", root)
	if code != 0 {
		t.Errorf("have exit %d", code)
	}
	if want := objRel(a) + "\n" + objRel(b) + "\n"; out != want {
		t.Errorf("have printed %q, want %q", out, want)
	}
	if out, code := runScript(t, haveScript, "", root); code != 0 || out != "" {
		t.Errorf("have with nothing asked: %q, exit %d", out, code)
	}

	if out, code := runScript(t, getScript, "", root, objRel(a)); code != 0 || out != "body of "+a.SHA256[:4] {
		t.Errorf("get present: %q, exit %d", out, code)
	}
	if out, code := runScript(t, getScript, "", root, objRel(absent)); code != missingExit || out != "" {
		t.Errorf("get absent: %q, exit %d, want exit %d", out, code, missingExit)
	}
}

// remoteWord must survive the remote login shell exactly: spaces, quotes and
// $ stay literal, and a leading ~/ still means the home directory.
func TestRemoteWord(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"with space/it's", "with space/it's"},
		{"/abs/dir with $dollar and `tick` and \"dq\"", "/abs/dir with $dollar and `tick` and \"dq\""},
		{"~/vault.git.storage", home + "/vault.git.storage"},
		{"~/a b/it's", home + "/a b/it's"},
		{"~", "~"}, // a bare ~ would be a path component named ~.storage once suffixed; kept literal
		{"~notauser x/y", "~notauser x/y"},
		{"\n", "\n"},
		{"", ""},
	}
	for _, tc := range cases {
		out, err := exec.Command("sh", "-c", "printf %s "+remoteWord(tc.in)).Output()
		if err != nil {
			t.Errorf("remoteWord(%q) = %s: %v", tc.in, remoteWord(tc.in), err)
			continue
		}
		if string(out) != tc.want {
			t.Errorf("the shell reads remoteWord(%q) = %s as %q, want %q", tc.in, remoteWord(tc.in), out, tc.want)
		}
	}
}

func TestVerifyReader(t *testing.T) {
	const payload = "0123456789abcdef"
	obj := objOf(payload)
	read := func(src io.Reader, o Object) (string, error, error) {
		v := newVerifyReader(src, o)
		out, err := io.ReadAll(v)
		return string(out), err, v.err
	}

	t.Run("exact", func(t *testing.T) {
		for name, src := range map[string]io.Reader{
			"whole":    strings.NewReader(payload),
			"one byte": iotest.OneByteReader(strings.NewReader(payload)),
			"half":     iotest.HalfReader(strings.NewReader(payload)),
			"data+eof": iotest.DataErrReader(strings.NewReader(payload)),
		} {
			out, err, verr := read(src, obj)
			if err != nil || verr != nil || out != payload {
				t.Errorf("%s: out=%q err=%v v.err=%v", name, out, err, verr)
			}
		}
	})
	t.Run("empty object", func(t *testing.T) {
		out, err, _ := read(strings.NewReader(""), objOf(""))
		if err != nil || out != "" {
			t.Errorf("out=%q err=%v", out, err)
		}
	})
	t.Run("short withholds the last byte and fails", func(t *testing.T) {
		out, err, verr := read(strings.NewReader(payload[:10]), obj)
		if err == nil || verr == nil || !strings.Contains(err.Error(), "10 bytes") {
			t.Errorf("err=%v v.err=%v, want a failure naming 10 bytes", err, verr)
		}
		if len(out) >= len(payload) {
			t.Errorf("a short source delivered %d bytes, want fewer than the %d promised", len(out), len(payload))
		}
	})
	t.Run("long stops early", func(t *testing.T) {
		out, err, _ := read(strings.NewReader(payload+strings.Repeat("x", 100000)), obj)
		if err == nil || len(out) >= len(payload) {
			t.Errorf("err=%v, delivered %d bytes; want a failure before the promised %d", err, len(out), len(payload))
		}
	})
	t.Run("wrong sha withholds the last byte and fails", func(t *testing.T) {
		out, err, _ := read(strings.NewReader(strings.ToUpper(payload)), obj)
		if err == nil || !strings.Contains(err.Error(), obj.SHA256) {
			t.Errorf("err=%v, want a failure naming the expected sha256", err)
		}
		if len(out) != len(payload)-1 {
			t.Errorf("delivered %d bytes, want %d (all but the last)", len(out), len(payload)-1)
		}
	})
	t.Run("read error passes through", func(t *testing.T) {
		boom := errors.New("disk fell off")
		_, err, _ := read(io.MultiReader(strings.NewReader(payload[:5]), iotest.ErrReader(boom)), obj)
		if !errors.Is(err, boom) {
			t.Errorf("err=%v, want the source's error", err)
		}
	})
}
