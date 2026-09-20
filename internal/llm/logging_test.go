package llm

// logging_test.go pins 010 contract §0's llm log lines (MASTER §5, frozen
// TestLLMLogsRequests): against the fake SSE server, Stream's turn leaves
// a request line carrying the model and the body's byte count (never the
// body), a response line carrying the HTTP status, and a finish line
// carrying the provider's finish reason. The logger under test is exactly
// what logging.Init installed as slog.Default — the seam cmd/lw wires —
// restored at cleanup so sibling tests never see it. Subtests run in the
// frozen block's order.

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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

// capturingSSEServer is sseServer plus a raw view of the request body:
// every request's bytes go on raw before the scripted SSE is replayed, so
// a test can assert the logged prompt_bytes equals the true wire size.
func capturingSSEServer(t *testing.T, body []byte, raw chan<- []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		raw <- b
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
}

func TestLLMLogsRequests(t *testing.T) {
	const bodyMarker = "SECRET-BODY-MARKER-never-logged"
	streamOne := func(t *testing.T, srv *httptest.Server) []Chunk {
		t.Helper()
		c := New(Config{BaseURL: srv.URL, Model: "test-model", Timeout: 5 * time.Second})
		ch, err := c.Stream(context.Background(), Request{
			Messages: []Message{{Role: "user", Content: bodyMarker}},
		})
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		return collect(t, ch)
	}

	t.Run("finish_line_carries_finish_reason", func(t *testing.T) {
		logPath := installFileLog(t)
		body := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"cut off\"},\"finish_reason\":null}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}]}\n\n" +
			"data: [DONE]\n\n")
		srv := sseServer(t, body, nil)
		defer srv.Close()

		chunks := streamOne(t, srv)
		if len(chunks) == 0 || chunks[len(chunks)-1].Finish != "length" {
			t.Fatalf("scripted stream did not finish with length: %#v", chunks)
		}

		log := readLog(t, logPath)
		if !strings.Contains(log, `msg="llm finish"`) || !strings.Contains(log, "finish=length") {
			t.Errorf("log missing the finish line with finish=length:\n%s", log)
		}
	})

	t.Run("request_line_carries_model_and_prompt_bytes", func(t *testing.T) {
		logPath := installFileLog(t)
		raw := make(chan []byte, 1)
		srv := capturingSSEServer(t, loadFixture(t, "simple.sse"), raw)
		defer srv.Close()

		streamOne(t, srv)
		sent := <-raw

		log := readLog(t, logPath)
		for _, want := range []string{
			`msg="llm request"`,
			"model=test-model",
			"prompt_bytes=" + strconv.Itoa(len(sent)),
		} {
			if !strings.Contains(log, want) {
				t.Errorf("log missing %q:\n%s", want, log)
			}
		}
		if strings.Contains(log, bodyMarker) {
			t.Errorf("log leaked the request body content:\n%s", log)
		}
	})

	t.Run("response_line_carries_status", func(t *testing.T) {
		logPath := installFileLog(t)
		srv := sseServer(t, loadFixture(t, "simple.sse"), nil)
		defer srv.Close()

		streamOne(t, srv)

		log := readLog(t, logPath)
		if !strings.Contains(log, `msg="llm response"`) || !strings.Contains(log, "status=200") {
			t.Errorf("log missing the response status line:\n%s", log)
		}
	})
}
