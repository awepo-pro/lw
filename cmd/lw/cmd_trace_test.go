// cmd_trace_test.go covers `lw trace` (038 T5): the wiring helper
// traceLoopConfig, the one-line-per-turn list, and show's three output
// modes — rendered (golden), --body byte-exact, and --json verbatim — plus
// the two surfaces tracing touches elsewhere: session show's turn rule
// title and the `lw config` trace.keep_mb row. Every fixture turn is
// recorded through the real trace.Start/Recorder API, so the tests pin what
// a real turn writes, not a hand-shaped events.ndjson. No test reads the
// wall clock: turn ids are fixed, and nothing the tests assert comes from a
// timestamp (00-conventions.md §3).
package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/trace"
)

// traceTestDir is the traces directory `lw trace` reads under a vault root —
// the same join traceLoopConfig produces for a keeping config.
func traceTestDir(root string) string {
	return filepath.Join(root, stateDirName, "traces")
}

// traceFixtureVault is a bare vault root for the trace fixtures.
func traceFixtureVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "SCHEMA.md"), []byte(testSchema), 0o644); err != nil {
		t.Fatalf("write SCHEMA.md: %v", err)
	}
	return root
}

// bodyN returns a deterministic []byte of exactly n bytes built from tag —
// the exact-size request bodies the --body test compares byte for byte.
func bodyN(tag string, n int) []byte {
	b := make([]byte, 0, n)
	for len(b) < n {
		b = append(b, tag...)
	}
	return b[:n]
}

// showFixtureMeta is the turn event the show fixtures record: every Meta
// field cmd/lw fills, with values chosen to survive into the golden header.
func showFixtureMeta() trace.Meta {
	return trace.Meta{
		Verb: "query", Session: "query", Version: "v0.12.0",
		Model: "glm-4.7", Server: "api.z.ai", Thinking: "off",
		MaxRounds: 24, ContextTokens: 96000,
	}
}

// longToolArgs is a tool-call argument over the 120-rune cut: the golden
// must show its head and the ellipsis, never the whole thing.
const longToolArgs = `{"path":"raw/articles/kv-cache.md","quote":"PagedAttention stores the KV cache in fixed-size blocks, so long sequences stop crowding out short ones in memory."}`

// recordShowFixture records the one turn the show tests render: a retry
// (round 1 attempt 1 cut, attempt 2 re-sent), a tool error, an elision
// before round 2, and a >120-rune tool argument. Bodies are exact-sized so
// the --body test can compare bytes.
func recordShowFixture(t *testing.T, root string) (dir, id string) {
	t.Helper()
	dir = traceTestDir(root)
	id = "20260928T101502Z-3f9a"
	_, rec := trace.Start(context.Background(), dir, id, showFixtureMeta(), 0)
	if rec == nil {
		t.Fatal("trace.Start failed for the show fixture")
	}

	rec.BeginRequest(1, 1, 3, 5)
	rec.Request(bodyN("round-1-attempt-1 ", 900))
	rec.Response(trace.Response{
		Round: 1, Attempt: 1, Cut: true,
		FirstByteMS: 210, FirstDeltaMS: 400, StreamMS: 900,
		Text: "partial thought from the cut stream",
	})
	rec.Retry(1, 1, "stream ended early")

	rec.BeginRequest(1, 2, 3, 5)
	rec.Request(bodyN("round-1-attempt-2 ", 944))
	rec.Response(trace.Response{
		Round: 1, Attempt: 2, Finish: "tool_calls",
		FirstByteMS: 230, FirstDeltaMS: 510, StreamMS: 3100,
		Reasoning: "look up the kv cache page before answering\nthe vault may hold several",
		Text:      "Let me look that up.",
		ToolCalls: []trace.ToolCall{{ID: "call_01", Name: "raw.get", Arguments: longToolArgs}},
		Usage:     &llm.Usage{InputTokens: 1200, OutputTokens: 210, CachedTokens: 300, ReasoningTokens: 40},
	})
	rec.Tool(trace.Tool{Round: 1, ID: "call_01", Name: "raw.get", IsError: true, MS: 5, ResultBytes: 89})

	rec.Elide(2, 3, 12500)
	rec.BeginRequest(2, 1, 7, 5)
	rec.Request(bodyN("round-2-attempt-1 ", 2048))
	rec.Response(trace.Response{
		Round: 2, Attempt: 1, Finish: "stop",
		FirstByteMS: 180, FirstDeltaMS: 420, StreamMS: 2050,
		Text:  "The page defines kv caching as storing attention keys and values between tokens.",
		Usage: &llm.Usage{InputTokens: 71034, OutputTokens: 2701, CachedTokens: 57700, ReasoningTokens: 1764},
	})
	rec.Done(trace.Done{Reason: "stop", Rounds: 2, WallMS: 58712})
	return dir, id
}

func TestTraceLoopConfig(t *testing.T) {
	root := traceFixtureVault(t)
	cfg := config.Default()
	cfg.LLM.BaseURL = "https://api.z.ai/api/coding/paas/v4"

	dir, keep, meta := traceLoopConfig(root, cfg)
	if dir != filepath.Join(root, ".llmwiki", "traces") {
		t.Errorf("dir = %q, want %q", dir, filepath.Join(root, ".llmwiki", "traces"))
	}
	if keep != 268435456 {
		t.Errorf("keep = %d, want 268435456 (256 MiB)", keep)
	}
	if meta.Server != "api.z.ai" {
		t.Errorf("Server = %q, want the base_url host only, api.z.ai", meta.Server)
	}
	if meta.Version != version {
		t.Errorf("Version = %q, want lw's own version string", meta.Version)
	}
	if meta.Model != cfg.LLM.Model {
		t.Errorf("Model = %q, want the configured %q", meta.Model, cfg.LLM.Model)
	}
	if meta.Thinking != cfg.LLM.Thinking {
		t.Errorf("Thinking = %q, want the configured %q", meta.Thinking, cfg.LLM.Thinking)
	}
	// The verb is deliberately absent here: it rides the ctx (trace.WithVerb
	// at the four Send call sites), because newAgent is shared by query,
	// lint and the TUI and cannot tell them apart (038 C-3).
	if meta.Verb != "" {
		t.Errorf("Verb = %q, want it empty — the verb rides the ctx", meta.Verb)
	}

	// keep_mb = 0 turns tracing off: no directory at all, not an empty one.
	off := config.Default()
	zero := 0
	off.Trace.KeepMB = &zero
	dir, keep, _ = traceLoopConfig(root, off)
	if dir != "" {
		t.Errorf("dir with keep_mb = 0 = %q, want %q — tracing is off", dir, "")
	}
	if keep != 0 {
		t.Errorf("keep with keep_mb = 0 = %d, want 0", keep)
	}
}

// TestTraceListFormat pins the one-line-per-turn list, newest first, two
// spaces between columns. The query fixture's four responses carry usage
// pieces summing to in 71234 / cached 57700 / out 2911 / thinking 1804, so
// the token line is the exact `in 71.2k (81% cached)  out 2.9k (thinking
// 1.8k)` the frozen block states. The ingest fixture has no done event and
// no usage, and carries one tool error, so its line ends
// `incomplete  no usage  0.0s  1 tool error(s)`.
func TestTraceListFormat(t *testing.T) {
	root := traceFixtureVault(t)
	dir := traceTestDir(root)

	// The older turn: a complete 4-round query. Usage per round:
	// in 1200+60000+7004+3030 = 71234, cached 300+52000+2700+2700 = 57700,
	// out 210+2200+301+200 = 2911, thinking 40+1500+164+100 = 1804.
	_, rec := trace.Start(context.Background(), dir, "20260928T101502Z-3f9a",
		trace.Meta{Verb: "query"}, 0)
	usages := []llm.Usage{
		{InputTokens: 1200, CachedTokens: 300, OutputTokens: 210, ReasoningTokens: 40},
		{InputTokens: 60000, CachedTokens: 52000, OutputTokens: 2200, ReasoningTokens: 1500},
		{InputTokens: 7004, CachedTokens: 2700, OutputTokens: 301, ReasoningTokens: 164},
		{InputTokens: 3030, CachedTokens: 2700, OutputTokens: 200, ReasoningTokens: 100},
	}
	for r, u := range usages {
		rec.BeginRequest(r+1, 1, 3, 5)
		rec.Request(bodyN("list-query ", 64))
		u := u
		rec.Response(trace.Response{Round: r + 1, Attempt: 1, Finish: "tool_calls",
			FirstByteMS: 200, FirstDeltaMS: 400, StreamMS: 14000, Usage: &u})
	}
	rec.Done(trace.Done{Reason: "stop", Rounds: 4, WallMS: 58712})

	// The newer turn: an ingest killed before it finished — no done event,
	// no usage, one tool error.
	_, rec = trace.Start(context.Background(), dir, "20260928T101503Z-0001",
		trace.Meta{Verb: "ingest"}, 0)
	rec.BeginRequest(1, 1, 2, 5)
	rec.Request(bodyN("list-ingest ", 64))
	rec.Response(trace.Response{Round: 1, Attempt: 1, Cut: true, FirstByteMS: 90, FirstDeltaMS: 200, StreamMS: 300})
	rec.Tool(trace.Tool{Round: 1, ID: "call_1", Name: "raw.get", IsError: true, MS: 3, ResultBytes: 12})
	rec.Close()

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"trace", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}

	// The footer's MB figure is the dir's real byte size, which the fixtures'
	// gzipped bodies make impractical to hand-compute; the test takes it from
	// the same Size call the verb makes and pins the shape around it.
	_, sizeBytes, err := trace.Size(dir)
	if err != nil {
		t.Fatalf("Size: %v", err)
	}
	want := "20260928T101503Z-0001  ingest  1 round  incomplete  no usage  0.0s  1 tool error(s)\n" +
		"20260928T101502Z-3f9a  query   4 rounds  stop        in 71.2k (81% cached)  out 2.9k (thinking 1.8k)  58.7s\n" +
		"2 turn(s) · " + fmtMB(sizeBytes) + " MB of 256 MB cap · .llmwiki/traces\n"
	if stdout != want {
		t.Errorf("stdout =\n%q\nwant\n%q", stdout, want)
	}

	// -n cuts the listing, never the footer's dir-level facts.
	stdout, _, code = captureRun(t, func() int { return run([]string{"trace", "--vault", root, "-n", "1"}) })
	if code != 0 {
		t.Fatalf("-n 1: exit code = %d, want 0", code)
	}
	want = "20260928T101503Z-0001  ingest  1 round  incomplete  no usage  0.0s  1 tool error(s)\n" +
		"2 turn(s) · " + fmtMB(sizeBytes) + " MB of 256 MB cap · .llmwiki/traces\n"
	if stdout != want {
		t.Errorf("-n 1: stdout =\n%q\nwant\n%q", stdout, want)
	}
}

// fmtMB renders a byte count the way the list footer does, so the expected
// text above can carry the fixtures' real on-disk size.
func fmtMB(b int64) string {
	return strconv.FormatFloat(float64(b)/(1<<20), 'f', 1, 64)
}

// TestTraceListEmptyAndOff pins the two whole-output shapes: a vault with
// no traces yet, and the off line a trace.keep_mb = 0 config adds.
func TestTraceListEmptyAndOff(t *testing.T) {
	const emptyText = "no traces yet — every agent turn (ingest, ask, query, lint --fix) records one under .llmwiki/traces\n"

	t.Run("empty", func(t *testing.T) {
		root := traceFixtureVault(t)
		stdout, stderr, code := captureRun(t, func() int { return run([]string{"trace", "--vault", root}) })
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		if stdout != emptyText {
			t.Errorf("stdout =\n%q\nwant\n%q", stdout, emptyText)
		}
	})

	t.Run("off", func(t *testing.T) {
		root := traceFixtureVault(t)
		dir := configTestEnv(t)
		writeConfigFile(t, dir, "[trace]\nkeep_mb = 0\n")

		stdout, stderr, code := captureRun(t, func() int { return run([]string{"trace", "--vault", root}) })
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		want := emptyText + "tracing is off (trace.keep_mb = 0)\n"
		if stdout != want {
			t.Errorf("stdout =\n%q\nwant\n%q", stdout, want)
		}
	})
}

// TestTraceShowRendered pins show's rendered form against a golden authored
// from the frozen rules: the header, the retry's two attempt lines, the
// folded thinking line, the >120-rune tool argument cut with an ellipsis,
// the tool error, the elision before round 2, and the done line.
func TestTraceShowRendered(t *testing.T) {
	root := traceFixtureVault(t)
	recordShowFixture(t, root)

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"trace", "show", "--vault", root, "20260928T101502Z-3f9a"})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	testutil.GoldenString(t, "testdata/trace_show.golden", stdout)

	// --thinking unfolds the same turn's reasoning in place of the fold line.
	stdout, _, code = captureRun(t, func() int {
		return run([]string{"trace", "show", "--vault", root, "--thinking", "20260928T101502Z-3f9a"})
	})
	if code != 0 {
		t.Fatalf("--thinking: exit code = %d, want 0", code)
	}
	for _, want := range []string{
		"thought: look up the kv cache page before answering",
		"    the vault may hold several",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("--thinking output lacks %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "--thinking to show") {
		t.Errorf("--thinking output still carries the fold hint:\n%s", stdout)
	}
}

// TestTraceShowBodyExact pins --body: the stdout bytes are the recorded
// request bytes and nothing else — `R` is round R's first attempt, `R.A`
// names the attempt.
func TestTraceShowBodyExact(t *testing.T) {
	root := traceFixtureVault(t)
	recordShowFixture(t, root)

	t.Run("round_1", func(t *testing.T) {
		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"trace", "show", "--vault", root, "--body", "1"})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		if !bytes.Equal([]byte(stdout), bodyN("round-1-attempt-1 ", 900)) {
			t.Errorf("--body 1 is not the recorded request bytes (got %d bytes)", len(stdout))
		}
	})

	t.Run("round_1_attempt_2", func(t *testing.T) {
		stdout, _, code := captureRun(t, func() int {
			return run([]string{"trace", "show", "--vault", root, "--body", "1.2"})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if !bytes.Equal([]byte(stdout), bodyN("round-1-attempt-2 ", 944)) {
			t.Errorf("--body 1.2 is not the recorded request bytes (got %d bytes)", len(stdout))
		}
	})
}

// TestTraceShowJSONVerbatim pins --json: events.ndjson's own bytes, unknown
// fields included — verbatim means verbatim, as session show --json does.
func TestTraceShowJSONVerbatim(t *testing.T) {
	root := traceFixtureVault(t)
	dir, id := recordShowFixture(t, root)

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"trace", "show", "--vault", root, id, "--json"})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	b, err := os.ReadFile(filepath.Join(dir, id, "events.ndjson"))
	if err != nil {
		t.Fatalf("read events.ndjson: %v", err)
	}
	if stdout != string(b) {
		t.Errorf("--json stdout is not events.ndjson verbatim (got %d bytes, file has %d)", len(stdout), len(b))
	}
}

// TestTraceShowResolveErrors pins the two bad-reference shapes: an unknown
// ref and an ambiguous prefix both exit 1 with the resolver's own text.
func TestTraceShowResolveErrors(t *testing.T) {
	root := traceFixtureVault(t)
	dir := traceTestDir(root)
	for _, id := range []string{"20260928T101502Z-3f9a", "20260928T101503Z-b2c1"} {
		if err := os.MkdirAll(filepath.Join(dir, id), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", id, err)
		}
	}

	t.Run("ambiguous", func(t *testing.T) {
		_, stderr, code := captureRun(t, func() int {
			return run([]string{"trace", "show", "--vault", root, "2026"})
		})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
		if want := `lw: trace: "2026" matches 2 turns; give more of the id` + "\n"; stderr != want {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		_, stderr, code := captureRun(t, func() int {
			return run([]string{"trace", "show", "--vault", root, "zzz"})
		})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
		if want := `lw: trace: no turn matches "zzz"` + "\n"; stderr != want {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	})
}

// TestSessionShowTurnTitle pins K6: a user record that carries a turn id is
// titled `you · turn <id>`, joining the transcript line to its trace. A
// record without one keeps the bare "you" (the existing show goldens pin
// that side and are unchanged).
func TestSessionShowTurnTitle(t *testing.T) {
	root := newSessionVault(t)
	r := srec("2026-09-28T10:15:01Z", "user", "what is kv caching?")
	r.Turn = "20260928T101502Z-3f9a"
	writeSession(t, root, "open", "cs-turn000000000001",
		changesetJSON("cs-turn000000000001", "2026-09-28T10:15:00Z"), r)

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"session", "show", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if want := "you · turn 20260928T101502Z-3f9a"; !strings.Contains(stdout, want) {
		t.Errorf("stdout lacks the rule title %q:\n%s", want, stdout)
	}
}

// TestConfigTraceKeepRow pins the `lw config` row and the bounded setter:
// the default shows 256 (default), a set value shows (file), and the bound
// 0..10240 is refused outside — the same message style as web.max_results.
func TestConfigTraceKeepRow(t *testing.T) {
	configTestEnv(t)

	stdout, _, code := runConfig(t)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	wantRow(t, stdout, "trace.keep_mb", "256", "(default)")

	// A set value round-trips through the file and shows as (file).
	if _, stderr, code := runConfig(t, "set", "trace.keep_mb", "64"); code != 0 {
		t.Fatalf("set trace.keep_mb 64: exit code = %d, want 0; stderr=%q", code, stderr)
	}
	got, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Trace.KeepMB == nil || *got.Trace.KeepMB != 64 {
		t.Fatalf("Trace.KeepMB = %v, want 64", got.Trace.KeepMB)
	}
	stdout, _, code = runConfig(t)
	if code != 0 {
		t.Fatalf("show: exit code = %d, want 0", code)
	}
	wantRow(t, stdout, "trace.keep_mb", "64", "(file)")

	// 0 is legal — it switches tracing off — and shows as (file), since the
	// file said so even though 0 is not the default.
	if _, stderr, code := runConfig(t, "set", "trace.keep_mb", "0"); code != 0 {
		t.Fatalf("set trace.keep_mb 0: exit code = %d, want 0; stderr=%q", code, stderr)
	}
	stdout, _, _ = runConfig(t)
	wantRow(t, stdout, "trace.keep_mb", "0", "(file)")

	// Past the bound the set is refused and nothing is written.
	_, stderr, code := runConfig(t, "set", "trace.keep_mb", "10241")
	if code != 2 {
		t.Fatalf("set trace.keep_mb 10241: exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr, "want 0..10240") {
		t.Errorf("stderr = %q, want the bounded-int message", stderr)
	}
	got, err = config.Load()
	if err != nil {
		t.Fatalf("Load after refusal: %v", err)
	}
	if got.Trace.KeepMB == nil || *got.Trace.KeepMB != 0 {
		t.Errorf("Trace.KeepMB = %v, want the refused set to have changed nothing", got.Trace.KeepMB)
	}
}
