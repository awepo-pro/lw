package agent

// logging_test.go pins 010 contract §0's agent log lines (MASTER §5,
// frozen TestAgentLogsTurns): a turn's start line carries the round cap, a
// dispatched tool call leaves a call line with its name and a result line
// with its error flag, and a clean turn ends with a done line carrying the
// reason and the round count. The scripted fake streamer drives the loop
// exactly as the rest of this suite does; the logger under test is what
// logging.Init installed as slog.Default, restored at cleanup.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/logging"
)

// installFileLog points slog.Default at <tmp>/lw.log through logging.Init
// and restores the previous default when the test ends.
func installFileLog(t *testing.T) string {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	dir := t.TempDir()
	if err := logging.Init(dir, slog.LevelDebug); err != nil {
		t.Fatalf("logging.Init: %v", err)
	}
	return filepath.Join(dir, "lw.log")
}

// readLog returns the whole log file at path.
func readLog(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log %s: %v", path, err)
	}
	return string(b)
}

func TestAgentLogsTurns(t *testing.T) {
	t.Run("done_line_carries_reason_and_rounds", func(t *testing.T) {
		logPath := installFileLog(t)
		l, fx, _ := newTestLoop(t, [][]llm.Chunk{
			{{Text: "42."}, {Finish: "stop"}},
		}, LoopConfig{})

		out := make(chan Event, 64)
		if err := l.Send(context.Background(), fx.csID, "answer me", out); err != nil {
			t.Fatalf("Send: %v", err)
		}
		drain(out)

		log := readLog(t, logPath)
		for _, want := range []string{`msg="agent done"`, "reason=stop", "rounds=1"} {
			if !strings.Contains(log, want) {
				t.Errorf("log missing %q:\n%s", want, log)
			}
		}
	})

	t.Run("tool_call_and_result_lines", func(t *testing.T) {
		logPath := installFileLog(t)
		args := "" // stage.close takes no arguments; Send dispatches "{}"
		l, fx, _ := newTestLoop(t, [][]llm.Chunk{
			{toolCallChunk("call-1", "stage.close", args), {Finish: "tool_calls"}},
			{{Text: "All done."}, {Finish: "stop"}},
		}, LoopConfig{})

		out := make(chan Event, 64)
		if err := l.Send(context.Background(), fx.csID, "close it out", out); err != nil {
			t.Fatalf("Send: %v", err)
		}
		drain(out)

		log := readLog(t, logPath)
		for _, want := range []string{
			`msg="agent tool call"`,
			"name=stage.close",
			fmt.Sprintf("args_bytes=%d", len(args)), // raw payload size, "" included
			`msg="agent tool result"`,
			"is_error=false",
		} {
			if !strings.Contains(log, want) {
				t.Errorf("log missing %q:\n%s", want, log)
			}
		}
	})

	t.Run("turn_start_line", func(t *testing.T) {
		logPath := installFileLog(t)
		l, fx, _ := newTestLoop(t, [][]llm.Chunk{
			{{Text: "42."}, {Finish: "stop"}},
		}, LoopConfig{})

		out := make(chan Event, 64)
		if err := l.Send(context.Background(), fx.csID, "answer me", out); err != nil {
			t.Fatalf("Send: %v", err)
		}
		drain(out)

		log := readLog(t, logPath)
		for _, want := range []string{`msg="agent turn"`, "rounds_max=24"} {
			if !strings.Contains(log, want) {
				t.Errorf("log missing %q:\n%s", want, log)
			}
		}
	})
}
