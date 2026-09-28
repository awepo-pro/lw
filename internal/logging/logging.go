// Package logging gives lw a file-only debug log (010 contract §0, D-10B).
//
// Init installs a *slog.Logger as slog.Default that appends text records
// to <dir>/lw.log and never writes to a terminal — a TUI or MCP session
// keeps its stdout/stderr untouched. Attribute values whose key is
// api_key, authorization, token or password are mechanically replaced with
// [REDACTED] before they reach disk, and lw.log rotates to lw.log.1
// (overwriting) when a write would push it past maxLogBytes. Init failure
// is never fatal: it reports the error and leaves the previous default
// logger installed.
//
// Rotation is per-process: each lw process tracks its own view of lw.log's
// size and renames its own lw.log → lw.log.1, so concurrent lw processes
// on one vault (`lw tui` alongside `lw mcp`) can clobber each other's
// lw.log.1. Single-writer-per-vault is the supported posture; a debug log
// losing a back-generation to a second writer is an accepted cost, not a
// coordination problem this package solves.
package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// fileName is the active log file under Dir(root).
	fileName = "lw.log"
	// rotatedTo is what fileName becomes when it rotates (overwriting any
	// previous generation — exactly one back-generation is kept).
	rotatedTo = "lw.log.1"
	// dirPerm is the mode Dir and its parents are created with.
	dirPerm = 0o755
	// filePerm is the mode lw.log is created with.
	filePerm = 0o644
	// redacted is what every secret-valued attribute's value becomes.
	redacted = "[REDACTED]"
)

// maxLogBytes is the size a single write may not push lw.log past: the
// file then renames to lw.log.1 and logging starts fresh. It is a var only
// so the rotation test can shrink it (010 contract §0 names it the
// package's one white-box knob); production code never writes it.
var maxLogBytes int64 = 5 << 20

// Dir returns the log directory under a vault root: <root>/.llmwiki/logs.
func Dir(root string) string {
	return filepath.Join(root, ".llmwiki", "logs")
}

// LevelFromEnv reads $LW_LOG — "debug", "info", "warn" or "error", case
// and surrounding space ignored — and defaults to info when it is unset or
// unrecognized.
func LevelFromEnv() slog.Level {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LW_LOG"))) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Init creates dir (parents included) and lw.log under it, then installs a
// slog.Default that appends text records there at the given level. An
// unwritable dir or file returns an error and leaves the previous default
// logger installed — Init never fails a command on its own.
func Init(dir string, level slog.Level) error {
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("logging: create %s: %w", dir, err)
	}
	w, err := openAppend(filepath.Join(dir, fileName))
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(newHandler(w, level)))
	return nil
}

// newHandler wraps w in the package's one handler shape: slog text
// records, filtered to level, with secret attributes redacted on the way
// to the file, and the ctx's turn id (038 T3) stamped onto every record
// that carries one. There is deliberately no second handler and no
// fallback writer — the file is the only destination.
func newHandler(w io.Writer, level slog.Level) slog.Handler {
	text := slog.NewTextHandler(w, &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: redact,
	})
	return turnHandler{inner: text, base: text}
}

// turnHandler stamps the agent turn id (WithTurn, 038 T3) onto every
// record as turn=<id>, which is what joins a lw.log line to the turn's
// trace. The inner TextHandler writes WithAttrs values after the logger's
// own preformatted attrs but before the record's, so re-entering Handle
// through inner.WithAttrs places turn exactly there — first attr after
// msg — without a second formatter, and redaction still sees the record
// through the inner handler's ReplaceAttr. Records whose ctx carries no
// turn go through untouched, byte-identical to the pre-038 handler.
//
// Groups are the one wrinkle: attrs layered onto a grouped handler land
// inside the group, and turn must stay top-level — `turn=`, not `g.turn=`
// — whatever the logger's group state. So the handler remembers the
// WithAttrs values that came before the first WithGroup (pre) and the
// state layered after it (post), and for a grouped logger rebuilds the
// chain at Handle time with turn spliced into the top level. That path is
// the cold one; the common ungrouped case is the single WithAttrs above.
// The wrapped handlers are immutable, so sharing base across copies is
// safe and every derived handler sees the same redaction.
type turnHandler struct {
	inner slog.Handler // the logger's full state, as slog built it
	base  slog.Handler // the bare text handler, no With/WithGroup state
	pre   []slog.Attr  // WithAttrs values before the first WithGroup
	post  []turnState  // everything from the first WithGroup on
}

// turnState is one WithAttrs/WithGroup step recorded after the first
// WithGroup, so a grouped chain can be rebuilt with turn at the top.
type turnState struct {
	group string
	attrs []slog.Attr
}

// Enabled reports the inner handler's level decision.
func (h turnHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

// Handle writes r through the inner handler, adding turn=<id> when ctx
// carries one — top-level even when the logger has groups open.
func (h turnHandler) Handle(ctx context.Context, r slog.Record) error {
	id := TurnFrom(ctx)
	if id == "" {
		return h.inner.Handle(ctx, r)
	}
	turn := []slog.Attr{slog.String("turn", id)}
	if len(h.post) == 0 {
		return h.inner.WithAttrs(turn).Handle(ctx, r)
	}
	top := h.base.WithAttrs(append(append([]slog.Attr{}, h.pre...), turn...))
	for _, s := range h.post {
		if s.group != "" {
			top = top.WithGroup(s.group)
		} else {
			top = top.WithAttrs(s.attrs)
		}
	}
	return top.Handle(ctx, r)
}

// WithAttrs returns a turn handler whose inner handler carries attrs.
func (h turnHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	copied := append([]slog.Attr{}, attrs...)
	n := h
	n.inner = h.inner.WithAttrs(attrs)
	if len(n.post) == 0 {
		n.pre = append(n.pre, copied...)
	} else {
		n.post = append(n.post, turnState{attrs: copied})
	}
	return n
}

// WithGroup returns a turn handler whose inner handler opens name.
func (h turnHandler) WithGroup(name string) slog.Handler {
	n := h
	n.inner = h.inner.WithGroup(name)
	n.post = append(n.post, turnState{group: name})
	return n
}

// redact is the handler's ReplaceAttr: any attribute whose key is on the
// contract's floor list has its value swapped for [REDACTED], whatever
// group it sits in, before the record is formatted.
func redact(_ []string, a slog.Attr) slog.Attr {
	switch a.Key {
	case "api_key", "authorization", "token", "password":
		return slog.String(a.Key, redacted)
	}
	return a
}

// openAppend opens path for appending — creating it and its current size
// with it, so a re-Init on an existing log still rotates at the cap — and
// wraps it in a rotWriter rooted at dir.
func openAppend(path string) (*rotWriter, error) {
	f, size, err := openLogFile(path)
	if err != nil {
		return nil, fmt.Errorf("logging: open %s: %w", path, err)
	}
	return &rotWriter{dir: filepath.Dir(path), f: f, size: size}, nil
}

// openLogFile opens path for appending (creating it if absent) and reports
// its size as opened.
func openLogFile(path string) (*os.File, int64, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, filePerm)
	if err != nil {
		return nil, 0, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, info.Size(), nil
}

// rotWriter is the io.Writer under the log handler. It appends whole
// records to lw.log and, when a write would push the file past
// maxLogBytes, renames lw.log to lw.log.1 first and continues into a fresh
// lw.log. Rotation is best-effort: a failed rename or reopen leaves the
// current file in place and growing — a log must never take the process
// down.
type rotWriter struct {
	mu   sync.Mutex
	dir  string
	f    *os.File
	size int64
}

// Write appends p, rotating first when it would push the file past
// maxLogBytes. The mutex is what makes a rotation and the write it
// guards atomic; slog's TextHandler already serializes records, so this
// never contends in practice.
func (w *rotWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.size+int64(len(p)) > maxLogBytes {
		w.rotate()
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// rotate renames the active lw.log to lw.log.1 — overwriting the previous
// generation, which is what a plain rename does — and opens a fresh
// lw.log. Called with mu held. If the rename or the reopen fails, the old
// handle stays and the next oversized write retries; on Unix the rename
// also succeeds while this process still holds the file open, so no
// special unlink dance is needed. A rename failing with ENOENT is the one
// non-retry: it means lw.log is already gone under that name — the stale
// state a rename-ok/reopen-fail rotation leaves behind, with w.f writing
// into lw.log.1 — and retrying that rename would fail forever. The
// openLogFile below then re-creates lw.log, which is the rotation the
// failed attempt wanted; if that open fails too, this write simply skips
// rotation.
func (w *rotWriter) rotate() {
	old := filepath.Join(w.dir, fileName)
	if err := os.Rename(old, filepath.Join(w.dir, rotatedTo)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return // keep writing into the current file; retry on a later write
	}
	f, size, err := openLogFile(old)
	if err != nil {
		return // keep writing into the current file; retry on a later write
	}
	w.f.Close()
	w.f = f
	w.size = size
}
