package vaultsync

// objects.go is the payload store beside the git remote (066, replacing 062's
// SyncStorage). git carries the catalog (.llmwiki/storage.json: name -> sha256
// and size); the bytes travel here, addressed by their hash, the shape git-LFS,
// git-annex and DVC share:
//
//	<remote>.storage/objects/<sha[0:2]>/<sha[2:]>
//
// A name never maps to remote bytes, so a replacement is a new object (B2: the
// same-size replace that was lost; plan M2) and two PCs cannot overwrite each
// other. An object is written once, through a temp name whose byte count is
// checked, and is never deleted here (a removal is a catalog change,
// recoverable like git; plan M4). Files directly under <remote>.storage/ —
// 062's name layout — are never read or touched (R10).
//
// Content before pointer (plan M5, B6) is the caller's order, not this file's:
// cmd/lw asks HaveObjects and calls PutObject for what is missing before every
// git push, so a catalog entry never reaches another PC ahead of its bytes.
//
// The remote needs POSIX sh, test, mkdir, cat, wc and mv, nothing else: no
// rsync, no lw, no git (Q2). The same three scripts run for a local remote (a
// directory, a mounted NAS) and for an ssh remote, so the ssh path is tested
// against a real sh. Every ssh call goes through the runner's own invocation
// (sshArgs, sshCall): the options, multiplexing and BatchMode git gets.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Object names one content-addressed storage payload.
type Object struct {
	SHA256 string
	Size   int64
}

// ErrObjectMissing is what GetObject returns, wrapped, when every remote that
// answered lacks the object: the payload has not been uploaded yet (B6), which
// is a state to wait in, not damage.
var ErrObjectMissing = errors.New("storage object not on the remote")

// shaRe is the only form of hash that reaches a path: the catalog's lower-case
// hex. It also keeps a hostile "sha" from walking out of objects/ or into a
// shell.
var shaRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (o Object) validate() error {
	if !shaRe.MatchString(o.SHA256) {
		return fmt.Errorf("vaultsync: object sha256 %q is not 64 lower-case hex digits", o.SHA256)
	}
	if o.Size < 0 {
		return fmt.Errorf("vaultsync: object %s has a negative size", o.SHA256[:12])
	}
	return nil
}

// objRel is the object's place under <remote>.storage/objects/.
func objRel(o Object) string { return o.SHA256[:2] + "/" + o.SHA256[2:] }

// storeRoot is <remote>.storage — where 062 kept its mirror, and where 066 keeps
// objects/ beside it. A local remote is the directory's absolute path (a
// file:// URL its decoded path); an ssh remote is the path as written, so the
// remote shell resolves ~ and a relative path from the login directory. A
// trailing slash is dropped for both: git treats host:vault.git/ and
// host:vault.git as the same repository, and so must the store.
func (rem remote) storeRoot() string {
	p := rem.path
	if rem.kind == kindLocal && !strings.HasPrefix(rem.arg, "file://") {
		p = rem.arg
	}
	return strings.TrimSuffix(p, "/") + ".storage"
}

// The remote scripts. They hold no single quote — they travel inside
// sh -c '...' on the remote login shell's command line — and no "!", which an
// interactive csh-family shell would expand. Values reach them as positional
// parameters, never spliced into the text, so a path with a space, a quote or a
// $ in it stays one literal word.
const (
	// haveScript: $1 = store root; stdin = one object path per line. Prints
	// the paths whose object exists. The question travels on stdin so one round
	// trip answers any number of objects without meeting an argument-length limit.
	haveScript = `r=$1
while read -r p; do
  if [ -f "$r/objects/$p" ]; then echo "$p"; fi
done
exit 0
`

	// putScript: $1 = store root, $2 = object path, $3 = size, $4 = unique temp
	// suffix; stdin = the bytes. An existing object is left as it is (write-once).
	// Otherwise the bytes go to a temp name beside the final one and are renamed
	// into place only when the count equals $3, so a cut connection or a wrong
	// stream leaves no object; the trap removes the temp on every exit path.
	putScript = `r=$1; p=$2; n=$3
f=$r/objects/$p
d=${f%/*}
t=$d/.tmp.$4
if [ -f "$f" ]; then exit 0; fi
mkdir -p -- "$d" || exit 1
cleanup() { rm -f -- "$t"; }
trap cleanup EXIT
trap "exit 1" HUP INT TERM
cat > "$t" || exit 1
c=$(wc -c < "$t") || exit 1
if [ "$c" -ne "$n" ]; then
  echo "object has $c bytes, expected $n" >&2
  exit 2
fi
if [ -e "$f" ]; then exit 0; fi
mv -f -- "$t" "$f" || exit 1
`

	// getScript: $1 = store root, $2 = object path. Streams the object to
	// stdout, or exits missingExit when there is none.
	getScript = `f=$1/objects/$2
if [ -f "$f" ]; then exec cat -- "$f"; fi
exit 44
`

	// missingExit is getScript's "no such object". ssh passes the remote
	// command's status through (its own failures are 255).
	missingExit = 44
)

// shQuote single-quotes s for a POSIX shell, whatever it holds.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// tildeUser is a tilde prefix the remote shell expands: ~ or ~name.
var tildeUser = regexp.MustCompile(`^~[A-Za-z0-9._-]*$`)

// remoteWord is p as one word of the remote login shell's command line: quoted
// so every byte stays literal, except that a leading ~/ or ~user/ is left for
// the shell to expand — a remote written host:~/vault.git means the home
// directory, and quoting the tilde would make it a directory called "~". A
// bare ~ or a ~ that is not a tilde prefix stays literal.
func remoteWord(p string) string {
	if strings.HasPrefix(p, "~") {
		if i := strings.IndexByte(p, '/'); i > 0 && tildeUser.MatchString(p[:i]) {
			return p[:i+1] + shQuote(p[i+1:])
		}
	}
	return shQuote(p)
}

// minTransferRate is the slowest sustained speed a payload transfer is assumed
// to run at; maxTransferExtra caps what the size adds to the bound, so a
// mistaken size can neither make it effectively unlimited nor overflow.
const (
	minTransferRate  = 128 << 10 // bytes per second
	maxTransferExtra = 6 * time.Hour
)

// transferTimeout bounds one call that moves size payload bytes: Options.Timeout
// (the connect and round-trip allowance every network call gets) plus the time
// size bytes need at minTransferRate. 30 s flat, fine for a git call, would cut
// a 200 MB PDF off half way.
func (r *runner) transferTimeout(size int64) time.Duration {
	extra := maxTransferExtra
	if secs := size / minTransferRate; secs < int64(maxTransferExtra/time.Second) {
		extra = time.Duration(secs) * time.Second
	}
	return r.timeout() + extra
}

// putSeq numbers the uploads of this process, so concurrent ones never share a
// temp name.
var putSeq atomic.Uint64

// tmpSuffix is unique per upload across processes and PCs: host, pid, counter.
func tmpSuffix() string {
	return fmt.Sprintf("%s.%d.%d", commitHost(), os.Getpid(), putSeq.Add(1))
}

// finalError ends the failover through o.Remotes: the failure is not about the
// remote that returned it (the local source is wrong, or part of the payload
// has already been delivered), so asking the next remote cannot help.
type finalError struct{ err error }

func (e *finalError) Error() string { return e.err.Error() }
func (e *finalError) Unwrap() error { return e.err }

// storeEach runs try against o.Remotes in order, like git does, and returns the
// remote that succeeded. A remote that cannot hold objects (https://, git://)
// or does not answer is recorded and skipped; if none works the error is a
// *RemoteError naming each.
func (r *runner) storeEach(ctx context.Context, try func(rem remote) error) (string, error) {
	var tried []string
	var errs []error
	for _, spec := range r.o.Remotes {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		rem, err := parseRemote(spec)
		if err == nil && rem.kind == kindOther {
			err = errors.New("object storage needs an ssh or local path remote")
		}
		if err == nil {
			err = try(rem)
		}
		if err == nil {
			return spec, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var fe *finalError
		if errors.As(err, &fe) {
			return "", fe.err
		}
		tried = append(tried, spec)
		errs = append(errs, err)
	}
	return "", &RemoteError{Tried: tried, Errs: errs}
}

// objRun runs one of the scripts where rem's store lives and returns its
// stdout. args follow the store root as $2...; in feeds stdin, out (when set)
// receives stdout as a stream. size is the payload the call moves, for the
// timeout. A local remote runs the script in a local sh; an ssh remote runs it
// as sh -c '<script>' sh <root> <args> on the server.
func (r *runner) objRun(ctx context.Context, rem remote, script string, args []string, in io.Reader, out io.Writer, size int64) (string, error) {
	c := call{stdin: in, stdout: out, timeout: r.transferTimeout(size)}
	root := rem.storeRoot()
	switch rem.kind {
	case kindLocal:
		c.net = true
		c.args = append([]string{"-c", script, "sh", root}, args...)
		stdout, _, err := r.procErr(ctx, "sh", c)
		return stdout, err
	case kindSSH:
		var b strings.Builder
		b.WriteString("sh -c '")
		b.WriteString(script)
		b.WriteString("' sh ")
		b.WriteString(remoteWord(root))
		for _, a := range args {
			b.WriteByte(' ')
			b.WriteString(shQuote(a))
		}
		c.args = r.sshArgs(ctx, rem, b.String())
		// Named so a call that times out resets the multiplexed master
		// (procErr), as it does for git.
		c.sshDest, c.sshPort = rem.dest, rem.port
		stdout, _, err := r.sshCall(ctx, c)
		return stdout, err
	}
	return "", fmt.Errorf("remote %s: object storage needs an ssh or local path remote", rem.raw)
}

// HaveObjects reports which of objs the first usable remote in o.Remotes holds.
// One process/ssh round trip per call for an ssh remote, whatever len(objs) is.
//
// The map has an entry (true) for each held object, keyed by SHA256; an absent
// key means not held. remote is the o.Remotes entry that answered. Asking
// changes nothing on the remote: a remote with no store answers "none" and no
// directory appears. With no objs there is nothing to ask and nothing is run.
// With no remote that answers, the error is a *RemoteError.
func HaveObjects(ctx context.Context, o Options, objs []Object) (have map[string]bool, remote string, err error) {
	byRel := make(map[string]string, len(objs)) // object path -> sha256, asked once each
	var ask strings.Builder
	for _, obj := range objs {
		if err := obj.validate(); err != nil {
			return nil, "", err
		}
		rel := objRel(obj)
		if _, dup := byRel[rel]; !dup {
			byRel[rel] = obj.SHA256
			ask.WriteString(rel)
			ask.WriteByte('\n')
		}
	}
	if len(byRel) == 0 {
		return map[string]bool{}, "", nil
	}
	r, err := newRunner(o)
	if err != nil {
		return nil, "", err
	}
	return r.haveObjects(ctx, byRel, ask.String())
}

// haveObjects asks the remotes in order; byRel maps each asked object path to
// its sha256 and ask is the question, one path per line.
func (r *runner) haveObjects(ctx context.Context, byRel map[string]string, ask string) (map[string]bool, string, error) {
	var found map[string]bool
	remote, err := r.storeEach(ctx, func(rem remote) error {
		out, err := r.objRun(ctx, rem, haveScript, nil, strings.NewReader(ask), nil, 0)
		if err != nil {
			return err
		}
		found = map[string]bool{}
		for _, line := range strings.Split(out, "\n") {
			// Anything else on stdout (a login banner) names no object and is ignored.
			if sha, ok := byRel[strings.TrimSpace(line)]; ok {
				found[sha] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return found, remote, nil
}

// PutObject uploads the file at path as obj. Remote side: mkdir -p, write a temp
// name, check the byte count equals obj.Size, rename into place. An existing
// object is left as is.
//
// The sender also checks what it sends (R9: every object is verified before it
// becomes one). The remote can count bytes but has no portable sha256, so the
// source is hashed as it streams and its last byte is withheld until the whole
// file has matched obj.SHA256 and obj.Size; on any mismatch the stream ends one
// byte short, the remote's count check fails, and nothing is published. A file
// edited since the catalog recorded it can therefore never be stored under the
// old name (B2 from the other side), and a source of the wrong length — or a
// connection cut mid-stream — leaves no object and no temp file. The error
// names what was wrong with the source.
func PutObject(ctx context.Context, o Options, obj Object, path string) error {
	if err := obj.validate(); err != nil {
		return err
	}
	r, err := newRunner(o)
	if err != nil {
		return err
	}
	args := func() []string { return []string{objRel(obj), strconv.FormatInt(obj.Size, 10), tmpSuffix()} }
	_, err = r.storeEach(ctx, func(rem remote) error {
		f, err := os.Open(path)
		if err != nil {
			return &finalError{fmt.Errorf("put %s: %w", obj.SHA256[:12], err)}
		}
		defer f.Close()
		src := newVerifyReader(f, obj)
		_, err = r.objRun(ctx, rem, putScript, args(), src, nil, obj.Size)
		if src.err != nil {
			// The source, not the remote, is at fault; its error is the useful one.
			return &finalError{fmt.Errorf("put %s: %s: %w", obj.SHA256[:12], path, src.err)}
		}
		return err
	})
	return err
}

// GetObject streams obj from the remote into w. The caller verifies (storage.Materialize).
// A missing object returns an error wrapping ErrObjectMissing.
//
// w may hold a partial payload when GetObject fails after writing: the caller
// discards it. Once bytes have gone to w the next remote is not tried (they
// would be appended to the first one's), and a remote object whose length is
// not obj.Size is an error. ErrObjectMissing is returned only when every remote
// that was tried answered "no such object"; if one was unreachable the object
// may be there, and the error is a *RemoteError that does not claim otherwise.
func GetObject(ctx context.Context, o Options, obj Object, w io.Writer) error {
	if err := obj.validate(); err != nil {
		return err
	}
	r, err := newRunner(o)
	if err != nil {
		return err
	}
	cw := &countWriter{w: w}
	_, err = r.storeEach(ctx, func(rem remote) error {
		_, err := r.objRun(ctx, rem, getScript, []string{objRel(obj)}, nil, cw, obj.Size)
		var ce *cmdError
		switch {
		case err == nil:
			if cw.n != obj.Size {
				return &finalError{fmt.Errorf("get %s: the remote object has %d bytes, the catalog says %d", obj.SHA256[:12], cw.n, obj.Size)}
			}
			return nil
		case cw.n == 0 && errors.As(err, &ce) && ce.exitCode() == missingExit:
			return ErrObjectMissing
		case cw.n > 0:
			return &finalError{fmt.Errorf("get %s: %w", obj.SHA256[:12], err)}
		}
		return err
	})
	if err == nil {
		return nil
	}
	var re *RemoteError
	if errors.As(err, &re) {
		allMissing := len(re.Errs) > 0
		for _, e := range re.Errs {
			allMissing = allMissing && errors.Is(e, ErrObjectMissing)
		}
		if allMissing {
			return fmt.Errorf("%w: %s", ErrObjectMissing, obj.SHA256[:12])
		}
		// Mixed: an unreachable remote might hold it, so the missing ones must
		// not make errors.Is(err, ErrObjectMissing) true.
		for i, e := range re.Errs {
			if errors.Is(e, ErrObjectMissing) {
				re.Errs[i] = errors.New("no such object")
			}
		}
	}
	return err
}

// countWriter counts the bytes it passes on.
type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// verifyReader passes a source through to PutObject's stdin while checking it
// is exactly obj: not longer than obj.Size, and at its end the right length and
// the right sha256. The last byte is held back until that end check passes; on
// failure it is never delivered, err is set, and Read returns the error, so the
// consumer sees a stream one byte short of what it was promised and its own
// size check refuses it. err stays readable after the consumer is done.
type verifyReader struct {
	src  io.Reader
	obj  Object
	h    hash.Hash
	n    int64  // bytes read from src so far
	hold []byte // the last byte read, withheld
	out  []byte // checked bytes ready to hand out
	buf  []byte
	eof  bool  // src ended and matched; hold has been released
	err  error // why src is not obj (or src's own read error)
}

func newVerifyReader(src io.Reader, o Object) *verifyReader {
	return &verifyReader{src: src, obj: o, h: sha256.New(), buf: make([]byte, 32<<10)}
}

func (v *verifyReader) Read(p []byte) (int, error) {
	for {
		if len(v.out) > 0 {
			n := copy(p, v.out)
			v.out = v.out[n:]
			return n, nil
		}
		if v.err != nil {
			return 0, v.err
		}
		if v.eof {
			return 0, io.EOF
		}
		if len(p) == 0 {
			return 0, nil
		}
		n, err := v.src.Read(v.buf)
		if n > 0 {
			v.h.Write(v.buf[:n])
			v.n += int64(n)
			if v.n > v.obj.Size {
				v.err = fmt.Errorf("source is longer than the %d bytes the catalog says", v.obj.Size)
				continue
			}
			chunk := make([]byte, 0, len(v.hold)+n)
			chunk = append(chunk, v.hold...)
			chunk = append(chunk, v.buf[:n]...)
			v.out, v.hold = chunk[:len(chunk)-1], chunk[len(chunk)-1:]
		}
		switch {
		case err == io.EOF:
			switch got := hex.EncodeToString(v.h.Sum(nil)); {
			case v.n != v.obj.Size:
				v.err = fmt.Errorf("source has %d bytes, the catalog says %d", v.n, v.obj.Size)
			case got != v.obj.SHA256:
				v.err = fmt.Errorf("source sha256 %s does not match the catalog's %s", got, v.obj.SHA256)
			default:
				v.out = append(v.out, v.hold...)
				v.hold = nil
				v.eof = true
			}
		case err != nil:
			v.err = err
		}
	}
}
