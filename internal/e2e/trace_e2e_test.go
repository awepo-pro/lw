package e2e

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// trace_e2e_test.go pins 038's turn trace end to end, through the real lw
// binary against fakeLLM (038 T6): the bytes under
// <vault>/.llmwiki/traces/<turn-id>/ are the bytes lw POSTed (trace bytes ==
// wire bytes), tracing off changes nothing on the wire, one turn id joins
// lw.log, the session transcript and the trace, and the API key reaches
// neither. The scripted rounds use the real z.ai chunk shape — every chunk
// an object chat.completion.chunk, and usage riding the finish_reason chunk
// (runs/ground/glm-real-usage-round.sse) — because the DeepSeek shape lw
// answered before 038 put usage on its own trailing chunk, and T1's parse of
// the finish-chunk shape deserves a wire-level witness too.

// turnDirRE is the shape of a turn directory (trace.NewID's output). e2e is
// deliberately a black box for the trace package, so the shape is spelled
// here instead of imported: a trace dir that stopped matching this regexp
// must fail these tests at the assertion, not compile-time-drift into a
// different contract.
var turnDirRE = regexp.MustCompile(`^\d{8}T\d{6}Z-[0-9a-f]{4}$`)

// tracesRel is a vault's traces directory, relative to the vault root — the
// one path every trace assertion in this file starts from.
const tracesRel = ".llmwiki/traces"

// traceSecret is the fake provider key TestTraceNoKeyLeak plants in the
// subprocess environment. It is deliberately pronounceable and unique, so a
// substring count of 0 over every trace byte and log byte means the key
// never landed there — not merely that a common word failed to match.
const traceSecret = "ZEBRAKEY-038-secret"

// traceSecretEnv is the environment variable traceSecret travels in, and the
// reference the scenario's config writes as api_key = "env:..." — the same
// env: resolution path the DeepSeek default config uses, exercised end to
// end so the leak check guards the shape real users run, not a literal key
// the config file itself would carry.
const traceSecretEnv = "LW_E2E_TRACE_KEY"

// queryQuestion is the read-only question the query scenarios ask — the same
// question coldstart's query scenarios use, so a context-builder change that
// alters the request shows up here as a byte difference too.
const traceQueryQuestion = "What does this vault say about attention?"

// usageJSON renders the z.ai usage object: the shape
// glm-real-usage-round.sse carries on its finish chunk, with the four
// numbers a scenario wants spelled out rather than hidden in a fixture file.
func usageJSON(in, out, cached, reasoning int) string {
	return `"usage":{"prompt_tokens":` + strconv.Itoa(in) +
		`,"completion_tokens":` + strconv.Itoa(out) +
		`,"total_tokens":` + strconv.Itoa(in+out) +
		`,"prompt_tokens_details":{"cached_tokens":` + strconv.Itoa(cached) +
		`},"completion_tokens_details":{"reasoning_tokens":` + strconv.Itoa(reasoning) + `}}`
}

// traceChunk renders one z.ai-shaped chat.completion.chunk as a "data: "
// line. finish "" is the streaming null; usage "" omits the object, as every
// non-final chunk of the real stream does.
func traceChunk(id, delta, finish, usage string) string {
	b := `{"id":"chatcmpl-` + id + `","created":1790502755,"object":"chat.completion.chunk","model":"glm-5.3-flash","choices":[{"index":0,"finish_reason":`
	if finish == "" {
		b += `null`
	} else {
		b += `"` + finish + `"`
	}
	b += `,"delta":` + delta + `}]`
	if usage != "" {
		b += `,` + usage
	}
	b += `}`
	return "data: " + b
}

// traceRound scripts one round as the real z.ai stream is shaped: the given
// deltas, then a finish chunk (carrying usage when usage != ""), then
// [DONE].
func traceRound(id string, deltas []string, finish, usage string) fakeRound {
	var b strings.Builder
	for _, d := range deltas {
		b.WriteString(traceChunk(id, d, "", "") + "\n\n")
	}
	b.WriteString(traceChunk(id, "{}", finish, usage) + "\n\n")
	b.WriteString("data: [DONE]\n")
	return fakeRound{Name: "trace_" + id, Body: b.String()}
}

// traceQueryRounds is the 2-round query script: round 1 thinks, calls
// raw_list and finishes tool_calls with usage on that finish chunk; round 2
// answers and finishes stop with usage the same way. Two rounds is the
// smallest script that proves a trace holds MORE than one request — req-01
// and req-02, in one turn dir, each matching its own wire body.
func traceQueryRounds() []fakeRound {
	return []fakeRound{
		traceRound("t6r1",
			[]string{
				`{"role":"assistant","reasoning_content":"The user asks about raw sources; I should list them."}`,
				`{"role":"assistant","tool_calls":[{"index":0,"id":"call_t6raw","type":"function","function":{"name":"raw_list","arguments":"{\"query\":\"ATTENTION\"}"}}]}`,
			},
			"tool_calls", usageJSON(128, 32, 0, 20)),
		traceRound("t6r2",
			[]string{
				`{"role":"assistant","content":"The vault holds one raw source about attention."}`,
			},
			"stop", usageJSON(256, 16, 128, 4)),
	}
}

// writeConfigWith is this file's own config writer: the harness's writeConfig
// body plus a suffix for the tables a trace scenario needs ([trace] with a
// keep_mb, or an env: api_key reference). It lives here rather than editing
// harness_test.go because 038 T6 may only ADD files to internal/e2e — and
// the duplication it costs is one TOML block, pinned by the same assertions
// the original carries.
func writeConfigWith(t *testing.T, dir, baseURL, suffix string) string {
	t.Helper()

	path := filepath.Join(dir, "lw", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("e2e: create %s: %v", filepath.Dir(path), err)
	}
	config := "[llm]\n" +
		"base_url = " + tomlQuote(baseURL) + "\n" +
		"model = \"e2e-fake\"\n" +
		"api_key = \"" + fakeAPIKey + "\"\n" +
		"\n[llm.limits]\n" +
		"max_tool_rounds = 24\n" +
		"context_tokens = 96000\n" +
		suffix
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatalf("e2e: write %s: %v", path, err)
	}
	return path
}

// writeConfigEnvKey is writeConfigWith with the api_key resolved from an
// environment variable — the reference form TestTraceNoKeyLeak needs, since
// a literal key in the config file could only be leaked by the config
// reader, not by the request path the leak test guards.
func writeConfigEnvKey(t *testing.T, dir, baseURL, envVar string) string {
	t.Helper()

	path := writeConfigWith(t, dir, baseURL, "")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("e2e: read %s: %v", path, err)
	}
	config := strings.Replace(string(b),
		"api_key = \""+fakeAPIKey+"\"",
		"api_key = \"env:"+envVar+"\"", 1)
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatalf("e2e: write %s: %v", path, err)
	}
	return path
}

// runLWEnv is runLW with named extra environment variables layered onto the
// sandbox's four. The extra names are the point of the helper —
// TestTraceNoKeyLeak must hand the subprocess its api_key env var — while
// everything else stays exactly as minimal: never os.Environ(), so no real
// secret can ride along.
func runLWEnv(t *testing.T, e *env, extraEnv []string, args ...string) lwResult {
	t.Helper()

	if lwBin == "" {
		t.Fatal("e2e: lw binary was not built")
	}

	ctx, cancel := context.WithTimeout(context.Background(), lwDeadline)
	defer cancel()

	cmd := exec.CommandContext(ctx, lwBin, args...)
	cmd.Dir = e.work
	cmd.Env = append(e.environ(), extraEnv...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if ctx.Err() == nil {
			t.Fatalf("e2e: run lw %s: %v", strings.Join(args, " "), err)
		}
		if ctx.Err() != nil {
			code = -1
			stderr.WriteString("e2e: lw " + strings.Join(args, " ") + " did not exit within " + lwDeadline.String() + "\n")
		}
	}

	return lwResult{
		Code:   code,
		Stdout: stdout.String(),
		Stderr: stderr.String(),
		Output: stdout.String() + stderr.String(),
	}
}

// soleTraceDir returns the single turn directory under vault's traces dir,
// failing when there are none or more than one: a one-turn scenario that
// left two turn dirs would make "the trace" ambiguous, and the frozen join
// contract names exactly one.
func soleTraceDir(t *testing.T, vault string) string {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join(vault, tracesRel))
	if err != nil {
		t.Fatalf("e2e: read %s: %v", tracesRel, err)
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() && turnDirRE.MatchString(e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	if len(ids) != 1 {
		t.Fatalf("%s holds %d turn dir(s) %v, want exactly 1", tracesRel, len(ids), ids)
	}
	return ids[0]
}

// traceReq is one request event read back out of a turn's events.ndjson,
// together with the gunzipped body of the file the event names.
type traceReq struct {
	Round   int
	Attempt int
	File    string
	SHA256  string
	Body    []byte
}

// traceEventLine is the slice of events.ndjson's request events these
// scenarios read; every other kind is skipped by Kind.
type traceEventLine struct {
	Kind    string `json:"kind"`
	Round   int    `json:"round"`
	Attempt int    `json:"attempt"`
	File    string `json:"file"`
	SHA256  string `json:"sha256"`
}

// readTraceRequests reads a turn dir's request events in order and returns
// each one's recorded shape plus the gunzipped body of its file. A missing
// file or a bad gzip fails the test here: both mean the trace's own bytes
// are not the replayable record the contract promises, and the later
// equality assertions would only restate the failure less precisely.
func readTraceRequests(t *testing.T, turnDir string) []traceReq {
	t.Helper()

	b, err := os.ReadFile(filepath.Join(turnDir, "events.ndjson"))
	if err != nil {
		t.Fatalf("e2e: read events.ndjson: %v", err)
	}
	var reqs []traceReq
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			continue
		}
		var ev traceEventLine
		if err := json.Unmarshal([]byte(line), &ev); err != nil || ev.Kind != "request" {
			continue
		}
		body := gunzipFile(t, filepath.Join(turnDir, ev.File))
		reqs = append(reqs, traceReq{
			Round: ev.Round, Attempt: ev.Attempt, File: ev.File,
			SHA256: ev.SHA256, Body: body,
		})
	}
	return reqs
}

// gunzipFile reads a gzip file back to its exact original bytes.
func gunzipFile(t *testing.T, path string) []byte {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("e2e: open %s: %v", path, err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("e2e: gzip %s: %v", path, err)
	}
	defer z.Close()
	b, err := io.ReadAll(z)
	if err != nil {
		t.Fatalf("e2e: read %s: %v", path, err)
	}
	return b
}

// assertTurnLogSlice asserts the frozen log-join contract over one vault's
// lw.log: from the turn's "agent turn" line through its "agent done" line,
// every line carries turn=<id> — the id that names the turn's trace
// directory. Exactly one agent turn ran in each scenario, so the slice is
// unambiguous; two would mean a scenario leaked a second Send in.
func assertTurnLogSlice(t *testing.T, vault, id string) {
	t.Helper()

	log := vaultLog(t, vault)
	lines := strings.Split(log, "\n")
	start, end := -1, -1
	for i, line := range lines {
		switch {
		case strings.Contains(line, "agent turn") && start == -1:
			start = i
		case strings.Contains(line, "agent done"):
			end = i
		}
	}
	if start == -1 || end == -1 || end < start {
		t.Fatalf("lw.log has no agent turn..agent done span (start %d, end %d)\n%s", start, end, log)
	}
	for _, line := range lines[start : end+1] {
		if line == "" {
			continue
		}
		if !strings.Contains(line, "turn="+id) {
			t.Errorf("lw.log line inside the turn does not carry turn=%s:\n%s", id, line)
		}
	}
}

// assertTraceMatchesWire is the heart of TestTraceQueryE2E, shared with the
// off scenario's counterpart: the turn's request events — req-01 and req-02,
// round N attempt 1 — gunzip byte-equal to the Nth body the fake server
// recorded, and every sha256 field is the hash of that same body. Trace
// bytes == wire bytes, on both paths the bytes travel (file and event).
func assertTraceMatchesWire(t *testing.T, turnDir string, wire []fakeRequest) {
	t.Helper()

	reqs := readTraceRequests(t, turnDir)
	if len(reqs) != len(wire) {
		t.Fatalf("trace holds %d request event(s), wire holds %d", len(reqs), len(wire))
	}
	for i, req := range reqs {
		if want := fmt.Sprintf("req-%02d.json.gz", i+1); req.File != want {
			t.Errorf("request %d: file = %q, want %q", i+1, req.File, want)
		}
		if req.Round != i+1 || req.Attempt != 1 {
			t.Errorf("request %d: round %d attempt %d, want round %d attempt 1", i+1, req.Round, req.Attempt, i+1)
		}
		if !bytes.Equal(req.Body, wire[i].Body) {
			t.Errorf("request %d: trace body differs from the wire body\ntrace: %s\nwire:  %s", i+1, req.Body, wire[i].Body)
		}
		sum := sha256.Sum256(wire[i].Body)
		if req.SHA256 != hex.EncodeToString(sum[:]) {
			t.Errorf("request %d: sha256 = %s, want %s", i+1, req.SHA256, hex.EncodeToString(sum[:]))
		}
	}
}

// runTraceQuery drives one 2-round lw query over a fresh sandbox and vault:
// the scenario TestTraceQueryE2E, TestTraceOffIdenticalRequests and
// TestTraceNoKeyLeak all share, parameterized only by the config each
// writes and the environment the key scenario adds.
func runTraceQuery(t *testing.T, e *env, vault string) (lwResult, []fakeRequest) {
	t.Helper()

	fake := newFakeLLM(t, traceQueryRounds()...)
	writeConfig(t, e.config, fake.URL()+"/v1")

	res := runLW(t, e, "query", "--vault", vault, traceQueryQuestion)
	if res.Code != 0 {
		t.Fatalf("lw query: exit %d, want 0\n%s", res.Code, res.Output)
	}
	if got, want := fake.Served(), 2; got != want {
		t.Fatalf("fake LLM served %d requests, want %d", got, want)
	}
	return res, fake.Requests()
}

// TestTraceQueryE2E is the frozen trace contract (038 T6) over the real
// binary: a 2-round lw query leaves exactly one turn dir whose req-01 /
// req-02 gunzip byte-equal to the bodies the fake server recorded, with
// matching sha256 fields; `lw trace` lists the turn as a query that ran 2
// rounds and stopped; `lw trace show last --body 2` prints request 2's exact
// body; and every lw.log line from agent turn through agent done carries the
// turn id the directory is named for.
func TestTraceQueryE2E(t *testing.T) {
	e := newEnv(t)
	vault := newVault(t)

	_, wire := runTraceQuery(t, e, vault)

	id := soleTraceDir(t, vault)
	assertTraceMatchesWire(t, filepath.Join(vault, tracesRel, id), wire)
	assertTurnLogSlice(t, vault, id)

	t.Run("trace_list_line", func(t *testing.T) {
		list := runLW(t, e, "trace", "--vault", vault)
		if list.Code != 0 {
			t.Fatalf("lw trace: exit %d, want 0\n%s", list.Code, list.Output)
		}
		// The row shape cmd_trace.go renders: id, the verb, the round count,
		// the done reason — then the token bill, which the usage-on-finish
		// chunks of both rounds should have filled in (not "no usage").
		want := "^" + regexp.QuoteMeta(id) + `  query   2 rounds  stop  `
		line := regexp.MustCompile(want)
		if m := line.FindStringSubmatch(list.Stdout); m == nil {
			t.Errorf("lw trace: no row matches %q\n%s", want, list.Stdout)
		}
		if strings.Contains(list.Stdout, "no usage") {
			t.Errorf("lw trace: the turn's usage never reached the summary\n%s", list.Stdout)
		}
	})

	t.Run("trace_show_body", func(t *testing.T) {
		show := runLW(t, e, "trace", "show", "last", "--body", "2", "--vault", vault)
		if show.Code != 0 {
			t.Fatalf("lw trace show last --body 2: exit %d, want 0\n%s", show.Code, show.Output)
		}
		// stdout is the body and nothing else: the exact bytes, no header, no
		// trailing newline lw added on its own.
		if show.Stdout != string(wire[1].Body) {
			t.Errorf("lw trace show last --body 2: stdout differs from request 2's body\nshow: %s\nwire: %s", show.Stdout, wire[1].Body)
		}
		if show.Stderr != "" {
			t.Errorf("lw trace show last --body 2: stderr = %q, want empty", show.Stderr)
		}
	})
}

// TestTraceOffIdenticalRequests pins tracing being observation only (038 T6):
// with trace.keep_mb = 0 the same 2-round query writes no .llmwiki/traces at
// all, and the bodies lw POSTed are byte-identical to the traced scenario's
// — same vault scaffold, same question, same binary, so any byte that moved
// is a byte the tracer (or the off switch) moved. A trace that changed the
// request would corrupt every diagnosis it exists to support.
func TestTraceOffIdenticalRequests(t *testing.T) {
	// Scenario 1 again — the traced run whose requests are the baseline. It
	// runs in its own sandbox so the two scenarios share nothing but the
	// script and the binary.
	eOn := newEnv(t)
	vaultOn := newVault(t)
	_, wireOn := runTraceQuery(t, eOn, vaultOn)

	e := newEnv(t)
	vault := newVault(t)
	fake := newFakeLLM(t, traceQueryRounds()...)
	writeConfigWith(t, e.config, fake.URL()+"/v1", "\n[trace]\nkeep_mb = 0\n")

	res := runLW(t, e, "query", "--vault", vault, traceQueryQuestion)
	if res.Code != 0 {
		t.Fatalf("lw query (trace off): exit %d, want 0\n%s", res.Code, res.Output)
	}
	if got, want := fake.Served(), 2; got != want {
		t.Fatalf("fake LLM served %d requests, want %d", got, want)
	}

	if _, err := os.Stat(filepath.Join(vault, tracesRel)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s exists after a keep_mb = 0 turn (stat err %v); tracing must write nothing when it is off", tracesRel, err)
	}
	// The off line is the proof keep_mb = 0 was the config in force, not a
	// scenario whose config failed to load and silently defaulted.
	off := runLW(t, e, "trace", "--vault", vault)
	if off.Code != 0 {
		t.Fatalf("lw trace (trace off): exit %d, want 0\n%s", off.Code, off.Output)
	}
	if !strings.Contains(off.Output, "tracing is off (trace.keep_mb = 0)") {
		t.Errorf("lw trace does not say tracing is off\n%s", off.Output)
	}

	wireOff := fake.Requests()
	if len(wireOff) != len(wireOn) {
		t.Fatalf("off scenario sent %d request(s), traced scenario sent %d", len(wireOff), len(wireOn))
	}
	for i := range wireOn {
		if !bytes.Equal(wireOn[i].Body, wireOff[i].Body) {
			t.Errorf("request %d differs with tracing off:\non:  %s\noff: %s", i+1, wireOn[i].Body, wireOff[i].Body)
		}
	}
}

// TestTraceIngestSessionJoin pins the one-id join across all three surfaces
// (038 T6): an ingest's session.ndjson records, its trace directory and its
// lw.log lines all carry the same turn id — and `lw session show` titles the
// user record with it, so a transcript line leads a human to `lw trace show
// <id>` without copying anything by hand.
func TestTraceIngestSessionJoin(t *testing.T) {
	e := newEnv(t)
	vault := newVault(t)
	// The proven ingest script — stage_create_page, then a stop — drives the
	// whole session surface: user, tool (staged) and assistant records.
	fake := newFakeLLM(t,
		sseFixture(t, "ingest_round1.sse"),
		sseFixture(t, "ingest_round2.sse"),
	)
	writeConfig(t, e.config, fake.URL()+"/v1")
	src := e.writeSource(t, "sources/join/session-join.md", "# Session Join\n\nA small source whose ingest turn the session must join to its trace.\n")

	ingest := runLW(t, e, "ingest", "--vault", vault, "--kind", "article", src)
	if ingest.Code != 0 {
		t.Fatalf("lw ingest: exit %d, want 0\n%s", ingest.Code, ingest.Output)
	}

	id := soleTraceDir(t, vault)

	// The open changeset's session.ndjson: every record this turn appended
	// carries the turn id — the user record the turn wrote first, the tool
	// record its dispatched call produced, and the assistant record the
	// stopping round wrote.
	matches, err := filepath.Glob(filepath.Join(vault, ".llmwiki", "changesets", "open", "*", "session.ndjson"))
	if err != nil {
		t.Fatalf("e2e: glob session.ndjson: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("found %d open session.ndjson file(s) %v, want exactly 1", len(matches), matches)
	}
	b, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("e2e: read session.ndjson: %v", err)
	}
	var sessionRecord struct {
		Role string `json:"role"`
		Turn string `json:"turn"`
	}
	records := 0
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			continue
		}
		if err := json.Unmarshal([]byte(line), &sessionRecord); err != nil {
			t.Fatalf("session.ndjson line does not parse as a record: %v\n%s", err, line)
		}
		records++
		if sessionRecord.Turn != id {
			t.Errorf("session record (role %s): turn = %q, want the trace dir %q", sessionRecord.Role, sessionRecord.Turn, id)
		}
	}
	if records == 0 {
		t.Fatalf("session.ndjson holds no records\n%s", b)
	}

	assertTurnLogSlice(t, vault, id)

	show := runLW(t, e, "session", "show", "--vault", vault)
	if show.Code != 0 {
		t.Fatalf("lw session show: exit %d, want 0\n%s", show.Code, show.Output)
	}
	if want := "you · turn " + id; !strings.Contains(show.Output, want) {
		t.Errorf("lw session show: output does not title the user record %q\n%s", want, show.Output)
	}
}

// TestTraceNoKeyLeak pins the privacy half of the contract (038 T6): a turn
// whose key resolves from the environment — the form every default config
// ships — leaves zero occurrences of that key in the traces (gunzipped) and
// in lw.log. The Authorization check first proves the key was really on the
// wire, so a zero count can only mean the key never reached disk, not that
// the turn never used it.
func TestTraceNoKeyLeak(t *testing.T) {
	e := newEnv(t)
	vault := newVault(t)
	fake := newFakeLLM(t, traceQueryRounds()...)
	writeConfigEnvKey(t, e.config, fake.URL()+"/v1", traceSecretEnv)

	res := runLWEnv(t, e, []string{traceSecretEnv + "=" + traceSecret},
		"query", "--vault", vault, traceQueryQuestion)
	if res.Code != 0 {
		t.Fatalf("lw query (env key): exit %d, want 0\n%s", res.Code, res.Output)
	}

	// The key really was the key of record: the fake saw it in the
	// Authorization header, which is exactly the bytes the rest of this test
	// proves never landed on disk.
	reqs := fake.Requests()
	if len(reqs) != 2 {
		t.Fatalf("fake LLM recorded %d requests, want 2", len(reqs))
	}
	if want := "Bearer " + traceSecret; reqs[0].Auth != want {
		t.Fatalf("request 1: Authorization = %q, want %q — the env key was not the key used, so the leak check below would prove nothing", reqs[0].Auth, want)
	}

	// Every byte the turn left in the vault's trace and log surfaces: each
	// trace file gunzipped where it is a gzip, lw.log verbatim. A count
	// above zero is the leak, wherever it hid.
	occurrences := 0
	turns, err := os.ReadDir(filepath.Join(vault, tracesRel))
	if err != nil {
		t.Fatalf("e2e: read %s: %v", tracesRel, err)
	}
	for _, turn := range turns {
		files, err := os.ReadDir(filepath.Join(vault, tracesRel, turn.Name()))
		if err != nil {
			t.Fatalf("e2e: read turn dir %s: %v", turn.Name(), err)
		}
		for _, f := range files {
			path := filepath.Join(vault, tracesRel, turn.Name(), f.Name())
			var content []byte
			if strings.HasSuffix(f.Name(), ".gz") {
				content = gunzipFile(t, path)
			} else {
				content, err = os.ReadFile(path)
				if err != nil {
					t.Fatalf("e2e: read %s: %v", path, err)
				}
			}
			occurrences += strings.Count(string(content), traceSecret)
		}
	}
	for _, name := range []string{"lw.log", "lw.log.1"} {
		b, err := os.ReadFile(filepath.Join(vault, ".llmwiki", "logs", name))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			t.Fatalf("e2e: read %s: %v", name, err)
		}
		occurrences += strings.Count(string(b), traceSecret)
	}
	if occurrences != 0 {
		t.Errorf("the provider key %q appears %d time(s) across .llmwiki/traces and lw.log, want 0", traceSecret, occurrences)
	}

	assertTraceMatchesWire(t, filepath.Join(vault, tracesRel, soleTraceDir(t, vault)), reqs)
	assertTurnLogSlice(t, vault, soleTraceDir(t, vault))
}
