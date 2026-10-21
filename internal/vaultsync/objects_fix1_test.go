package vaultsync

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 066 FIX1 (X-4, F-7; X-7, F-4): what the object store says about an object that
// is there but wrong, and which remotes can hold objects at all.
//
// An object is addressed by its sha256 and written through a temp name whose
// byte count is checked, so a file at its path can only be the wrong length if
// something other than lw put it there (a full disk the kernel reported late, a
// restore from a half-copied backup, a person). The first version counted any
// file at the path as "the remote has it": HaveObjects said yes, PutObject left
// it alone, every PC that fetched it failed the length check, and nothing could
// repair it.

// plantObject writes body at obj's place under root, bypassing the store.
func plantObject(t *testing.T, root string, obj Object, body string) string {
	t.Helper()
	p := objFile(root, obj)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestHaveObjectsChecksSize: an object whose byte count is not the catalog's is
// not a copy. The remote answers with the paths whose files have the asked
// length, so a cut object is "missing" and the next upload replaces it.
func TestHaveObjectsChecksSize(t *testing.T) {
	eachStore(t, func(t *testing.T, c storeCase) {
		ctx := t.Context()
		whole, cut, absent := objOf("0123456789"), objOf("abcdefghij"), objOf("never uploaded")
		plantObject(t, c.root, whole, "0123456789")
		plantObject(t, c.root, cut, "abcd") // 4 of 10 bytes
		have, _, err := HaveObjects(ctx, c.o, []Object{whole, cut, absent})
		if err != nil {
			t.Fatalf("HaveObjects: %v", err)
		}
		if !have[whole.SHA256] {
			t.Error("an object of the right length was reported missing")
		}
		if have[cut.SHA256] {
			t.Error("an object of the wrong length (4 of 10 bytes) was reported as held (F-7: a damaged object counted as a copy)")
		}
		if have[absent.SHA256] {
			t.Error("an absent object was reported held")
		}
	})
}

// TestPutReplacesWrongSizeObject: the repair for the same damage. A wrong-length
// object is replaced through the temp name and mv, never edited in place, and a
// right-length one is still left exactly as it is (write-once, M2).
func TestPutReplacesWrongSizeObject(t *testing.T) {
	eachStore(t, func(t *testing.T, c storeCase) {
		ctx := t.Context()
		obj := objOf("the real bytes")
		p := plantObject(t, c.root, obj, "cut")
		if err := PutObject(ctx, c.o, obj, srcFile(t, "the real bytes")); err != nil {
			t.Fatalf("PutObject over a wrong-size object: %v", err)
		}
		if got := readFile(t, p); got != "the real bytes" {
			t.Errorf("the wrong-size object was not replaced: %q (F-7: it stayed damaged for every PC)", got)
		}
		noTemp(t, c.root)
		have, _, err := HaveObjects(ctx, c.o, []Object{obj})
		if err != nil || !have[obj.SHA256] {
			t.Errorf("HaveObjects after the repair = %v, %v; want held", have, err)
		}

		// A right-length object is a copy, whatever it holds: it is not touched.
		same := plantObject(t, c.root, objOf("kept as it is!"), "SAME LENGTH XX") // 14 bytes each
		past := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
		if err := os.Chtimes(same, past, past); err != nil {
			t.Fatal(err)
		}
		if err := PutObject(ctx, c.o, objOf("kept as it is!"), srcFile(t, "kept as it is!")); err != nil {
			t.Fatalf("PutObject over a right-size object: %v", err)
		}
		if fi, err := os.Stat(same); err != nil || readFile(t, same) != "SAME LENGTH XX" || !fi.ModTime().Equal(past) {
			t.Errorf("a right-length object was rewritten: %q (err %v)", readFile(t, same), err)
		}
		noTemp(t, c.root)
	})
}

// TestHaveScriptChecksSize and TestPutScriptReplacesWrongSize pin the same two
// rules at the script, the part that runs on a server.
func TestHaveScriptChecksSize(t *testing.T) {
	root := filepath.Join(t.TempDir(), "st ore's root")
	whole, cut := objOf("0123456789"), objOf("abcdefghij")
	plantObject(t, root, whole, "0123456789")
	plantObject(t, root, cut, "abcd")
	ask := objRel(whole) + " 10\n" + objRel(cut) + " 10\n"
	out, code := runScript(t, haveScript, ask, root)
	if code != 0 || out != objRel(whole)+"\n" {
		t.Errorf("have with sizes printed %q (exit %d), want only %q", out, code, objRel(whole))
	}
	// A question with no size is the old question: existence alone.
	out, code = runScript(t, haveScript, objRel(whole)+"\n"+objRel(cut)+"\n", root)
	if code != 0 || out != objRel(whole)+"\n"+objRel(cut)+"\n" {
		t.Errorf("have without sizes printed %q (exit %d), want both", out, code)
	}
}

func TestPutScriptReplacesWrongSize(t *testing.T) {
	root := filepath.Join(t.TempDir(), "st ore's root")
	obj := objOf("hello")
	final := plantObject(t, root, obj, "hel") // 3 of 5 bytes
	run := func(stdin string) int {
		_, code := runScript(t, putScript, stdin, root, objRel(obj), "5", "tmpname.1")
		return code
	}
	if code := run("hello"); code != 0 || readFile(t, final) != "hello" {
		t.Errorf("a wrong-size object: exit %d, object %q; want 0 and hello", code, readFile(t, final))
	}
	noTemp(t, root)

	// The replacement is still checked: a stream that is not 5 bytes leaves the
	// damaged object where it was (nothing is made worse) and no temp file.
	if err := os.WriteFile(final, []byte("hel"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run("hell"); code == 0 || readFile(t, final) != "hel" {
		t.Errorf("a short replacement: exit %d, object %q; want a failure and the old object kept", code, readFile(t, final))
	}
	noTemp(t, root)
}

// TestGetObjectDamagedIsLabelled: a remote object of the wrong length is not "not
// there yet" (ErrObjectMissing) and not an unreachable remote: it is damaged,
// and a caller that can say so (cmd/lw's fetch) must be able to tell.
func TestGetObjectDamagedIsLabelled(t *testing.T) {
	eachStore(t, func(t *testing.T, c storeCase) {
		obj := objOf("0123456789")
		plantObject(t, c.root, obj, "0123")
		err := GetObject(t.Context(), c.o, obj, io.Discard)
		if !errors.Is(err, ErrObjectDamaged) {
			t.Errorf("GetObject of a truncated object = %v, want errors.Is ErrObjectDamaged", err)
		}
		if errors.Is(err, ErrObjectMissing) {
			t.Errorf("a damaged object was reported as not arrived yet: %v", err)
		}
		if err != nil && !strings.Contains(err.Error(), "has 4 bytes") {
			t.Errorf("error lost the length detail: %v", err)
		}
	})
}

// TestHoldsObjects: https:// and git:// remotes carry git and nothing else
// (X-7). The check is the same one the object store makes before it runs a script.
func TestHoldsObjects(t *testing.T) {
	cases := []struct {
		spec string
		want bool
	}{
		{"/srv/vault.git", true},
		{"relative/vault.git", true},
		{"file:///srv/vault.git", true},
		{"host:vault.git", true},
		{"user@host:~/vault.git", true},
		{"ssh://user@host:2222/srv/vault.git", true},
		{"https://example.com/vault.git", false},
		{"http://example.com/vault.git", false},
		{"git://example.com/vault.git", false},
		{"", false},
		{"-oProxyCommand=evil", false},
	}
	for _, tc := range cases {
		if got := HoldsObjects(tc.spec); got != tc.want {
			t.Errorf("HoldsObjects(%q) = %v, want %v", tc.spec, got, tc.want)
		}
	}
}

// TestHaveObjectsAsksSizes: the size travels with the question, so one round trip
// still answers any number of objects.
func TestHaveObjectsAsksSizes(t *testing.T) {
	c := sshStore(t, "remote dir/it's remote.git", "remote dir", "it's remote.git.storage")
	ctx := t.Context()
	var objs []Object
	for i := 0; i < 4; i++ {
		body := "object " + strconv.Itoa(i)
		o := objOf(body)
		objs = append(objs, o)
		if i%2 == 0 {
			plantObject(t, c.root, o, body)
		} else {
			plantObject(t, c.root, o, body[:3]) // there, but cut
		}
	}
	if err := os.WriteFile(c.log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	have, _, err := HaveObjects(ctx, c.o, objs)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(readFile(t, c.log), "argv:"); n != 1 {
		t.Errorf("HaveObjects made %d ssh calls, want 1", n)
	}
	for i, o := range objs {
		if have[o.SHA256] != (i%2 == 0) {
			t.Errorf("object %d: held=%v, want %v", i, have[o.SHA256], i%2 == 0)
		}
	}
}

// TestHaveScriptNumericSizeCompare: the size is compared as a number (wc output
// is padded on some systems, BSD among them), and a different one is not a copy.
func TestHaveScriptNumericSizeCompare(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	root := filepath.Join(t.TempDir(), "r")
	o := objOf("padded")
	plantObject(t, root, o, "padded")
	out, code := runScript(t, haveScript, objRel(o)+" 6\n", root)
	if code != 0 || out != objRel(o)+"\n" {
		t.Errorf("have printed %q (exit %d)", out, code)
	}
	out, code = runScript(t, haveScript, objRel(o)+" 7\n", root)
	if code != 0 || out != "" {
		t.Errorf("have for a wrong size printed %q (exit %d), want nothing", out, code)
	}
}
