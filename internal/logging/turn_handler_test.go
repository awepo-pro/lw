package logging

// turn_handler_test.go pins the turn attr's place in the line (038 T3, L1):
// a record whose ctx carries a turn id gets turn=<id> immediately after msg
// and before the record's own attrs — after a With-logger's preformatted
// attrs, since those are what the text handler writes first. Records without
// a turn must stay byte-identical to the pre-038 handler, and redaction
// keeps applying on turn-bearing records.

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// oldHandler is the pre-038 handler shape, kept verbatim as the byte-equality
// baseline for records that carry no turn: if the turn wrapper ever changes a
// turnless line, this test catches it.
func oldHandler(w *bytes.Buffer, level slog.Level) slog.Handler {
	return slog.NewTextHandler(w, &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: redact,
	})
}

// lineBody strips the leading time field from a formatted line and returns
// the rest ("level=… msg=… …"), so assertions do not depend on the clock.
func lineBody(line string) string {
	_, rest, ok := strings.Cut(line, " level=")
	if !ok {
		return line
	}
	return "level=" + rest
}

// record is the one record the position tests format: the llm request line
// with a model attr, at a fixed time.
func record() slog.Record {
	r := slog.NewRecord(time.Date(2026, 9, 28, 10, 15, 2, 0, time.UTC), slog.LevelInfo, "llm request", 0)
	r.AddAttrs(slog.String("model", "m"))
	return r
}

// handle runs r through h — whose writer is buf, since newHandler bakes the
// writer in at construction — and returns the line written, newline
// stripped.
func handle(t *testing.T, buf *bytes.Buffer, h slog.Handler, ctx context.Context, r slog.Record) string {
	t.Helper()
	if err := h.Handle(ctx, r.Clone()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	return strings.TrimRight(buf.String(), "\n")
}

func TestTurnAttrFirst(t *testing.T) {
	ctx := WithTurn(context.Background(), "20260928T101502Z-3f9a")

	var buf bytes.Buffer
	line := handle(t, &buf, newHandler(&buf, slog.LevelInfo), ctx, record())

	want := `level=INFO msg="llm request" turn=20260928T101502Z-3f9a model=m`
	if got := lineBody(line); got != want {
		t.Fatalf("turn record =\n  %s\nwant\n  %s", got, want)
	}
}

func TestNoTurnUnchanged(t *testing.T) {
	ctx := context.Background()

	var got, want bytes.Buffer
	h := newHandler(&got, slog.LevelInfo)
	old := oldHandler(&want, slog.LevelInfo)
	if err := h.Handle(ctx, record().Clone()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if err := old.Handle(ctx, record().Clone()); err != nil {
		t.Fatalf("old Handle: %v", err)
	}

	if got.String() != want.String() {
		t.Fatalf("turnless record changed:\n got  %q\n want %q", got.String(), want.String())
	}
}

func TestTurnWithAttrsLogger(t *testing.T) {
	ctx := WithTurn(context.Background(), "20260928T101502Z-3f9a")

	var buf bytes.Buffer
	logger := slog.New(newHandler(&buf, slog.LevelInfo)).With("k", "v")
	logger.InfoContext(ctx, "x", "a", "b")

	line := strings.TrimRight(buf.String(), "\n")
	want := `level=INFO msg=x k=v turn=20260928T101502Z-3f9a a=b`
	if got := lineBody(line); got != want {
		t.Fatalf("With-logger record =\n  %s\nwant\n  %s", got, want)
	}
}

func TestTurnWithGroupTopLevel(t *testing.T) {
	ctx := WithTurn(context.Background(), "20260928T101502Z-3f9a")

	var buf bytes.Buffer
	logger := slog.New(newHandler(&buf, slog.LevelInfo)).WithGroup("g")
	logger.InfoContext(ctx, "x", "a", "b")

	line := strings.TrimRight(buf.String(), "\n")
	want := `level=INFO msg=x turn=20260928T101502Z-3f9a g.a=b`
	if got := lineBody(line); got != want {
		t.Fatalf("grouped record =\n  %s\nwant\n  %s (turn stays top-level, not g.turn)", got, want)
	}
}

func TestTurnRedactionKept(t *testing.T) {
	ctx := WithTurn(context.Background(), "20260928T101502Z-3f9a")

	var buf bytes.Buffer
	h := newHandler(&buf, slog.LevelInfo)
	r := slog.NewRecord(time.Date(2026, 9, 28, 10, 15, 2, 0, time.UTC), slog.LevelInfo, "llm request", 0)
	r.AddAttrs(slog.String("api_key", "sk-secret"))
	if err := h.Handle(ctx, r); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	line := strings.TrimRight(buf.String(), "\n")
	if strings.Contains(line, "sk-secret") {
		t.Fatalf("turn record leaked the key:\n%s", line)
	}
	want := `level=INFO msg="llm request" turn=20260928T101502Z-3f9a api_key=[REDACTED]`
	if got := lineBody(line); got != want {
		t.Fatalf("redacted turn record =\n  %s\nwant\n  %s", got, want)
	}
}
