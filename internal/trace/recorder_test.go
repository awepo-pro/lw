package trace

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/logging"
)

// testID mints a turn id in the frozen shape, distinct per n, so tests can
// pin reader behaviour without depending on NewID's randomness.
func testID(n int) string {
	return fmt.Sprintf("20260101T0000%02dZ-%04x", n, n)
}

// tsRE matches the RFC3339Nano UTC timestamp W4 requires inside a JSON line.
var tsRE = regexp.MustCompile(`"ts":"([^"]*)"`)

// withTS replaces a line's ts value with the literal TS and returns the
// original ts, so an expected line can be compared byte for byte while the
// timestamp itself is only shape-checked.
func withTS(t *testing.T, line string) (string, string) {
	t.Helper()
	m := tsRE.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("event line has no ts: %s", line)
	}
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$`).MatchString(m[1]) {
		t.Fatalf("ts %q is not RFC3339Nano UTC", m[1])
	}
	return tsRE.ReplaceAllString(line, `"ts":TS`), m[1]
}

// eventLines reads events.ndjson back as one string per line, requiring a
// trailing newline on every line but the last.
func eventLines(t *testing.T, turnDir string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(turnDir, "events.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// groundBody returns the exact request bytes lw once sent a live z.ai stream
// (038's glm-real-usage-round.sse), copied byte-identical into testdata.
func groundBody(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "glm-real-request-01.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// gunzip decompresses a recorded request file.
func gunzip(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(z)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestStartLayoutAndPerms pins W1: the traces dir and the turn dir are 0700,
// events.ndjson is 0600, and the first line is exactly W5's turn event for
// the given Meta (ts shape-checked, everything else byte-checked).
func TestStartLayoutAndPerms(t *testing.T) {
	// A dir Start must create itself — an existing dir keeps its mode, and
	// what W1 pins is the mode Start creates with.
	dir := filepath.Join(t.TempDir(), "traces")
	id := testID(1)
	meta := Meta{
		Verb: "query", Session: "cs-7f3a", Version: "v0.12.0",
		Model: "glm-5.3-flash", Server: "api.z.ai", Thinking: "off",
		MaxRounds: 6, ContextTokens: 32000,
	}
	ctx, rec := Start(context.Background(), dir, id, meta, 0)
	if rec == nil {
		t.Fatal("Start returned a nil Recorder")
	}
	if rec.ID() != id {
		t.Fatalf("rec.ID() = %q, want %q", rec.ID(), id)
	}
	if FromContext(ctx) != rec {
		t.Fatal("Start did not put the Recorder on the ctx")
	}
	for _, check := range []struct {
		path string
		want os.FileMode
	}{
		{dir, 0o700},
		{filepath.Join(dir, id), 0o700},
		{filepath.Join(dir, id, "events.ndjson"), 0o600},
	} {
		info, err := os.Stat(check.path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != check.want {
			t.Errorf("%s mode = %o, want %o", check.path, got, check.want)
		}
	}
	lines := eventLines(t, filepath.Join(dir, id))
	if len(lines) != 1 {
		t.Fatalf("after Start events.ndjson has %d lines, want 1", len(lines))
	}
	got, _ := withTS(t, lines[0])
	want := `{"turn":"` + id + `","ts":TS,"kind":"turn","verb":"query","session":"cs-7f3a",` +
		`"lw_version":"v0.12.0","gen_ai.request.model":"glm-5.3-flash","server.address":"api.z.ai",` +
		`"thinking":"off","max_rounds":6,"context_tokens":32000}`
	if got != want {
		t.Fatalf("turn line\n got %s\nwant %s", got, want)
	}
}

// TestRequestFileExactGzip pins W3: Request stores a gzip of exactly the
// body bytes it was handed — the ground-truth GLM request — under
// req-RR.json.gz for attempt 1 and req-RR-A.json.gz beyond, with the
// request event carrying the plain length and sha256 of the body.
func TestRequestFileExactGzip(t *testing.T) {
	dir := t.TempDir()
	_, rec := Start(context.Background(), dir, testID(2), Meta{}, 0)
	body := groundBody(t)

	rec.BeginRequest(1, 1, 5, 18)
	rec.Request(body)
	turnDir := filepath.Join(dir, testID(2))
	if got := gunzip(t, filepath.Join(turnDir, "req-01.json.gz")); !bytes.Equal(got, body) {
		t.Fatal("req-01.json.gz does not gunzip to the exact request bytes")
	}
	lines := eventLines(t, turnDir)
	var ev map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &ev); err != nil {
		t.Fatal(err)
	}
	wantFile := map[string]any{
		"kind": "request", "round": float64(1), "attempt": float64(1),
		"file": "req-01.json.gz", "bytes": float64(len(body)),
		"sha256":   fmt.Sprintf("%x", sha256.Sum256(body)),
		"messages": float64(5), "tools": float64(18),
	}
	for k, want := range wantFile {
		if ev[k] != want {
			t.Errorf("request event %s = %v, want %v", k, ev[k], want)
		}
	}

	rec.BeginRequest(1, 2, 5, 18)
	rec.Request(body)
	if got := gunzip(t, filepath.Join(turnDir, "req-01-2.json.gz")); !bytes.Equal(got, body) {
		t.Fatal("req-01-2.json.gz does not gunzip to the exact request bytes")
	}
}

// TestEventLinesKeyOrder pins W5: one line per event, keys in exactly the
// frozen order, omitempty keys absent when empty, and usage under its four
// gen_ai names. The ts is shape-checked and replaced with TS first.
func TestEventLinesKeyOrder(t *testing.T) {
	dir := t.TempDir()
	id := testID(3)
	_, rec := Start(context.Background(), dir, id, Meta{}, 0)
	body := []byte(`{"model":"m"}`)

	rec.BeginRequest(1, 1, 2, 3)
	rec.Request(body)
	rec.Response(Response{
		Round: 2, Attempt: 1, Finish: "stop",
		FirstByteMS: 120, FirstDeltaMS: 340, StreamMS: 5600,
		Reasoning: "hmm", Text: "hi",
		ToolCalls: []ToolCall{{ID: "c1", Name: "t", Arguments: "{}"}},
		Usage:     &llm.Usage{InputTokens: 1, OutputTokens: 2, CachedTokens: 3, ReasoningTokens: 4},
		Cut:       true, Error: "boom",
	})
	rec.Response(Response{Round: 2, Attempt: 2, Finish: "tool_calls", FirstByteMS: 1, FirstDeltaMS: 2, StreamMS: 3})
	rec.Tool(Tool{Round: 1, ID: "call-1", Name: "wiki.search", IsError: true, MS: 12, ResultBytes: 340})
	rec.Elide(1, 0, 0) // a zero-count elision is a no-op: no line for it
	rec.Elide(2, 3, 900)
	rec.Retry(1, 2, "stream truncated")
	rec.Done(Done{Reason: "max_rounds", Rounds: 2, Error: "boom", WallMS: 1234})

	sha := fmt.Sprintf("%x", sha256.Sum256(body))
	prefix := `{"turn":"` + id + `","ts":TS,`
	want := []string{
		prefix + `"kind":"turn","verb":"","session":"","lw_version":"",` +
			`"gen_ai.request.model":"","server.address":"","thinking":"",` +
			`"max_rounds":0,"context_tokens":0}`,
		prefix + `"kind":"request","round":1,"attempt":1,"file":"req-01.json.gz",` +
			`"bytes":` + fmt.Sprint(len(body)) + `,"sha256":"` + sha + `","messages":2,"tools":3}`,
		prefix + `"kind":"response","round":2,"attempt":1,"finish":"stop",` +
			`"first_byte_ms":120,"first_delta_ms":340,"stream_ms":5600,` +
			`"reasoning":"hmm","text":"hi",` +
			`"tool_calls":[{"id":"c1","name":"t","arguments":"{}"}],` +
			`"usage":{"gen_ai.usage.input_tokens":1,"gen_ai.usage.output_tokens":2,` +
			`"gen_ai.usage.cache_read.input_tokens":3,"gen_ai.usage.reasoning.output_tokens":4},` +
			`"cut":true,"error":"boom"}`,
		prefix + `"kind":"response","round":2,"attempt":2,"finish":"tool_calls",` +
			`"first_byte_ms":1,"first_delta_ms":2,"stream_ms":3}`,
		prefix + `"kind":"tool","round":1,"id":"call-1","name":"wiki.search",` +
			`"is_error":true,"ms":12,"result_bytes":340}`,
		prefix + `"kind":"elide","round":2,"count":3,"bytes":900}`,
		prefix + `"kind":"retry","round":1,"attempt":2,"reason":"stream truncated"}`,
		prefix + `"kind":"done","reason":"max_rounds","rounds":2,"error":"boom","wall_ms":1234}`,
	}
	lines := eventLines(t, filepath.Join(dir, id))
	if len(lines) != len(want) {
		t.Fatalf("events.ndjson has %d lines, want %d", len(lines), len(want))
	}
	for i, line := range lines {
		got, _ := withTS(t, line)
		if got != want[i] {
			t.Errorf("line %d\n got %s\nwant %s", i, got, want[i])
		}
	}
}

// TestNilRecorderNoop pins W2's nil-receiver half: a nil *Recorder — what
// FromContext yields when no turn is being traced — and the Observer with a
// bare ctx do nothing at all rather than panic.
func TestNilRecorderNoop(t *testing.T) {
	var rec *Recorder
	rec.BeginRequest(1, 1, 1, 1)
	rec.Request([]byte(`{}`))
	rec.Response(Response{Round: 1, Attempt: 1})
	rec.Tool(Tool{Round: 1, ID: "c", Name: "n"})
	rec.Elide(1, 1, 1)
	rec.Elide(1, 0, 0)
	rec.Retry(1, 2, "why")
	rec.Done(Done{Reason: "stop"})
	if err := rec.Close(); err != nil {
		t.Fatalf("nil Close = %v", err)
	}
	if rec.ID() != "" {
		t.Fatalf("nil ID = %q", rec.ID())
	}
	Observer{}.OnRequest(context.Background(), []byte(`{}`))
}

// TestWriteFailureWarnsOnce pins W2's failure half: the FIRST write failure
// writes exactly one `trace write failed` WARN and disables the Recorder;
// every later call is a silent no-op.
func TestWriteFailureWarnsOnce(t *testing.T) {
	logDir := t.TempDir()
	if err := logging.Init(logDir, slog.LevelInfo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil))) })

	dir := t.TempDir()
	id := testID(5)
	_, rec := Start(context.Background(), dir, id, Meta{}, 0)
	if err := os.RemoveAll(filepath.Join(dir, id)); err != nil {
		t.Fatal(err)
	}

	rec.BeginRequest(1, 1, 1, 1)
	rec.Request([]byte(`{}`)) // its file cannot be created: the dir is gone
	rec.Tool(Tool{Round: 1, ID: "c", Name: "n"})
	rec.Response(Response{Round: 1, Attempt: 1})
	rec.Done(Done{Reason: "stop"})
	if err := rec.Close(); err != nil {
		t.Fatalf("Close after failure = %v", err)
	}

	b, err := os.ReadFile(filepath.Join(logDir, "lw.log"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), "trace write failed"); n != 1 {
		t.Fatalf("lw.log has %d `trace write failed` records, want 1:\n%s", n, b)
	}
}

// TestObserverUsesContextRecorder pins the Observer's contract: it records
// through whatever Recorder the ctx carries, and does nothing — not panics —
// when the ctx carries none.
func TestObserverUsesContextRecorder(t *testing.T) {
	dir := t.TempDir()
	id := testID(6)
	ctx, _ := Start(context.Background(), dir, id, Meta{}, 0)
	body := []byte(`{"observed":true}`)
	Observer{}.OnRequest(ctx, body)
	turnDir := filepath.Join(dir, id)
	if got := gunzip(t, filepath.Join(turnDir, "req-00.json.gz")); !bytes.Equal(got, body) {
		t.Fatal("OnRequest did not record the exact body (round 0, attempt 1)")
	}
	Observer{}.OnRequest(context.Background(), body) // must not panic
}

// TestConcurrentMethods pins W2's goroutine safety: 800 concurrent tool
// events land as 800 separate, individually valid JSON lines.
func TestConcurrentMethods(t *testing.T) {
	dir := t.TempDir()
	id := testID(7)
	_, rec := Start(context.Background(), dir, id, Meta{}, 0)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				rec.Tool(Tool{
					Round: g + 1, ID: fmt.Sprintf("call-%d-%d", g, i),
					Name: "wiki.search", MS: int64(i), ResultBytes: i,
				})
			}
		}(g)
	}
	wg.Wait()
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	lines := eventLines(t, filepath.Join(dir, id))
	if len(lines) != 801 {
		t.Fatalf("events.ndjson has %d lines, want 801 (turn + 800 tools)", len(lines))
	}
	tools := 0
	for _, line := range lines {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line is not valid JSON: %v\n%s", err, line)
		}
		if ev["kind"] == "tool" {
			tools++
		}
	}
	if tools != 800 {
		t.Fatalf("saw %d tool events, want 800", tools)
	}
}
