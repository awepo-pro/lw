package e2e

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// streamcut_test.go pins 035's stream-integrity recovery end to end, through
// the real lw binary against fakeLLM (035 T4). T1 (internal/llm) classifies
// a stream that ended before [DONE] or a finish_reason as
// llm.ErrStreamTruncated; T2 (internal/agent) recovers the round —
// CONTINUING when a tool call was already dispatched (its tool already ran,
// so re-sending the request would stage the same op twice), RETRYING the
// identical request once when nothing was dispatched, and FAILING the turn
// when the re-send is cut too. These scenarios prove that policy survives
// the whole ingest pipeline: what is asserted here is not the error value
// (stream_test and the cmd-level tests own that) but what a user ends up
// with — the right number of staged ops, the right log trail, and the
// partial text the provider abandoned nowhere on disk.
//
// Every cut fixture is a real GLM-shaped SSE body whose file ends WITHOUT a
// trailing newline, so the last "data:" line arrives exactly as a provider
// cut delivers it: an unterminated final token, the R4 shape of
// consumeStreamTimed's classification.

// streamcutSource is the local source file every streamcut scenario ingests;
// the provenance the fixtures name mirrors ingest_round1.sse's, which
// validation accepts today.
func streamcutSource(t *testing.T, e *env) string {
	t.Helper()
	return e.writeSource(t, "sources/streamcut.md", "# Stream Integrity\n\nA local source whose agent turn a scripted provider stream cuts off.\n")
}

// vaultLog reads a vault's lw.log — the file the ingest subprocess wrote
// under logging.Dir(vault) — as one string. The process has exited by the
// time a scenario calls this, so everything it logged is on disk; the
// default LW_LOG level is info, and WARN is above it, so the 035 recovery
// records need no level bump to be present.
func vaultLog(t *testing.T, vault string) string {
	t.Helper()

	b, err := os.ReadFile(filepath.Join(vault, ".llmwiki", "logs", "lw.log"))
	if err != nil {
		t.Fatalf("e2e: read vault lw.log: %v", err)
	}
	return string(b)
}

// logLinesWith returns every line of log that carries substr, for assertions
// that must read the attributes of one record without matching them in the
// rest of the file.
func logLinesWith(log, substr string) []string {
	var lines []string
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, substr) {
			lines = append(lines, line)
		}
	}
	return lines
}

// assertStreamcutLog asserts the WARN trail one recovered streamcut leaves:
// exactly one content-free "llm stream truncated" record (R7 — the whole
// diagnostic, never a payload byte) and exactly one "agent round cut" record
// carrying action=want and neither of the other two actions.
func assertStreamcutLog(t *testing.T, log string, truncatedN int, want string) {
	t.Helper()

	if got := strings.Count(log, "llm stream truncated"); got != truncatedN {
		t.Errorf("lw.log holds %d \"llm stream truncated\" lines, want %d\n%s", got, truncatedN, log)
	}
	cuts := logLinesWith(log, "agent round cut")
	if len(cuts) != 1 {
		t.Fatalf("lw.log holds %d \"agent round cut\" lines, want 1\n%s", len(cuts), log)
	}
	if !strings.Contains(cuts[0], "action="+want) {
		t.Errorf("agent round cut line carries %q, want action=%s\n%s", cuts[0], want, cuts[0])
	}
	for _, other := range []string{"continue", "retry", "fail"} {
		if other != want && strings.Contains(log, "action="+other) {
			t.Errorf("lw.log carries action=%s, want only action=%s", other, want)
		}
	}
}

// harnessStreamcutRetry runs the streamcut_retry scenario (035 T4): round 1
// streams reasoning, text and a cut final line holding ZEBRAPAYLOAD — no
// tool call, so T2's policy (B) discards the round whole and re-sends the
// IDENTICAL request once; round 1′ answers with a whole stage_create_page,
// and round 2 stops. The user-visible contract: exit 0, exactly one staged
// op, one truncated + one retry record in lw.log, and the abandoned partial
// text nowhere in the log — ZEBRAPAYLOAD was discarded with the round, not
// recorded with it.
func harnessStreamcutRetry(t *testing.T) {
	t.Helper()

	e := newEnv(t)
	vault := newVault(t)
	fake := newFakeLLM(t,
		sseFixture(t, "streamcut_retry_round1.sse"),
		sseFixture(t, "streamcut_retry_round1retry.sse"),
		sseFixture(t, "streamcut_retry_round2.sse"),
	)
	writeConfig(t, e.config, fake.URL()+"/v1")
	src := streamcutSource(t, e)

	ingest := runLW(t, e, "ingest", "--vault", vault, "--kind", "article", src)
	if ingest.Code != 0 {
		t.Fatalf("lw ingest (streamcut_retry): exit %d, want 0\n%s", ingest.Code, ingest.Output)
	}
	if got, want := fake.Served(), 3; got != want {
		t.Fatalf("fake LLM served %d requests, want %d", got, want)
	}

	// The retry re-sent the identical request — byte for byte, not merely
	// semantically: nothing was dispatched, so nothing in the messages
	// moved between the two attempts (loop.go's streamLoop invariant).
	reqs := fake.Requests()
	if len(reqs) != 3 {
		t.Fatalf("fake LLM recorded %d requests, want 3", len(reqs))
	}
	if !bytes.Equal(reqs[0].Body, reqs[1].Body) {
		t.Errorf("retried request differs from the cut one:\nfirst:  %s\nsecond: %s", reqs[0].Body, reqs[1].Body)
	}

	status := runLW(t, e, "status", "--vault", vault)
	if status.Code != 0 {
		t.Fatalf("lw status: exit %d, want 0\n%s", status.Code, status.Output)
	}
	m := openOpsRE.FindStringSubmatch(status.Output)
	if m == nil {
		t.Fatalf("lw status: no open changeset line in output\n%s", status.Output)
	}
	if ops := m[1]; ops != "1" {
		t.Errorf("lw status: open changeset reports %s op(s), want exactly 1\n%s", ops, status.Output)
	}

	assertStreamcutLog(t, vaultLog(t, vault), 1, "retry")
	if log := vaultLog(t, vault); strings.Contains(log, "ZEBRAPAYLOAD") {
		t.Errorf("lw.log carries the abandoned partial text ZEBRAPAYLOAD; the retry must discard it with the round")
	}
}

// harnessStreamcutContinue runs the streamcut_continue scenario (035 T4):
// round 1 dispatches a whole stage_create_page (call index 0) and is cut
// while call index 1 is still assembling — a tool call is already out, so
// T2's policy (A) ends the round as if it had finished and the turn
// CONTINUES, never re-sending a request whose tool already ran. Round 2
// re-sends the lost second page and round 3 stops. The user-visible
// contract: exit 0, exactly 2 ops, the index-0 page staged exactly once,
// the continued request carrying call 0's tool result, and no RetryEv
// anywhere — nothing was thrown away, so there is nothing to disown.
func harnessStreamcutContinue(t *testing.T) {
	t.Helper()

	e := newEnv(t)
	vault := newVault(t)
	fake := newFakeLLM(t,
		sseFixture(t, "streamcut_continue_round1.sse"),
		sseFixture(t, "streamcut_continue_round2.sse"),
		sseFixture(t, "streamcut_continue_round3.sse"),
	)
	writeConfig(t, e.config, fake.URL()+"/v1")
	src := streamcutSource(t, e)

	ingest := runLW(t, e, "ingest", "--vault", vault, "--kind", "article", src)
	if ingest.Code != 0 {
		t.Fatalf("lw ingest (streamcut_continue): exit %d, want 0\n%s", ingest.Code, ingest.Output)
	}
	if got, want := fake.Served(), 3; got != want {
		t.Fatalf("fake LLM served %d requests, want %d (a round re-sent after a dispatched call would desync the script)", got, want)
	}

	// The continued round's request carries call 0's tool result: the
	// dispatched call's outcome joined the conversation, exactly as a
	// finished round's would.
	reqs := fake.Requests()
	if len(reqs) != 3 {
		t.Fatalf("fake LLM recorded %d requests, want 3", len(reqs))
	}
	if !strings.Contains(string(reqs[1].Body), `"tool_call_id":"call_035c0"`) {
		t.Errorf("continued request does not carry call 0's tool result (no tool_call_id call_035c0)\n%s", reqs[1].Body)
	}

	status := runLW(t, e, "status", "--vault", vault)
	if status.Code != 0 {
		t.Fatalf("lw status: exit %d, want 0\n%s", status.Code, status.Output)
	}
	m := openOpsRE.FindStringSubmatch(status.Output)
	if m == nil {
		t.Fatalf("lw status: no open changeset line in output\n%s", status.Output)
	}
	if ops := m[1]; ops != "2" {
		t.Errorf("lw status: open changeset reports %s op(s), want exactly 2\n%s", ops, status.Output)
	}

	// Page A — the call that was already out when the stream was cut — is
	// staged exactly once. A round re-sent after its call dispatched would
	// run the tool a second time, and that is the corruption policy (A)
	// exists to prevent.
	diff := runLW(t, e, "diff", "--vault", vault, "--stat")
	if diff.Code != 0 {
		t.Fatalf("lw diff --stat: exit %d, want 0\n%s", diff.Code, diff.Output)
	}
	for _, want := range []string{"wiki/concepts/stream-cut-continue-a.md", "wiki/concepts/stream-cut-continue-b.md"} {
		if !strings.Contains(diff.Output, want) {
			t.Errorf("lw diff --stat: output does not mention the staged page %s\n%s", want, diff.Output)
		}
	}
	if got := strings.Count(diff.Output, "stream-cut-continue-a.md"); got != 1 {
		t.Errorf("lw diff --stat: page A appears %d times, want exactly 1 (staged once, never twice)\n%s", got, diff.Output)
	}

	// Policy (A) logs the continue and emits no RetryEv: no consumer was
	// asked to drop anything, and the CLI's retry marker — the RetryEv
	// witness — never appears in the ingest output. The cut record names
	// one dispatched tool call: the continue is exactly the already-out
	// call talking, not a round that happened to end early.
	assertStreamcutLog(t, vaultLog(t, vault), 1, "continue")
	if cuts := logLinesWith(vaultLog(t, vault), "agent round cut"); len(cuts) == 1 && !strings.Contains(cuts[0], "tool_calls=1") {
		t.Errorf("agent round cut line does not record the dispatched call (tool_calls=1)\n%s", cuts[0])
	}
	if strings.Contains(ingest.Output, "[stream cut by the provider") {
		t.Errorf("ingest output carries the RetryEv retry marker; a continued round must not emit one\n%s", ingest.Output)
	}
}

// harnessStreamcutTwice runs the streamcut_twice scenario (035 T4): rounds
// 1 and 1′ are both cut with no tool call dispatched — the provider failed
// the identical request twice in a row, so T2's policy (C) fails the turn
// rather than loop. lw exits non-zero with the sentence the ingest verb
// owes its user: the round got its one retry, and the changeset was
// rejected with it.
func harnessStreamcutTwice(t *testing.T) {
	t.Helper()

	e := newEnv(t)
	vault := newVault(t)
	fake := newFakeLLM(t,
		sseFixture(t, "streamcut_twice_round1.sse"),
		sseFixture(t, "streamcut_twice_round1.sse"),
	)
	writeConfig(t, e.config, fake.URL()+"/v1")
	src := streamcutSource(t, e)

	ingest := runLW(t, e, "ingest", "--vault", vault, "--kind", "article", src)
	if ingest.Code == 0 {
		t.Fatalf("lw ingest (streamcut_twice): exit 0, want non-zero\n%s", ingest.Output)
	}
	if want := "the round was retried once; the changeset was rejected"; !strings.Contains(ingest.Output, want) {
		t.Errorf("lw ingest (streamcut_twice): output does not carry %q\n%s", want, ingest.Output)
	}
	assertNoPanic(t, ingest.Output)
	assertNoOpenChangeset(t, e, vault)
}
