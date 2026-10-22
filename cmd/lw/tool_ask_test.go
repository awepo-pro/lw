package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/mcp"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/trace"
)

// TestToolAskNoChangeset pins 068 D4.1:
// with no open changeset, lw tool --ask wiki.search '{"q":"<fixture word>"}'
// exits 0, stdout == registry Call Content under VerbQuery + "\n".
// No .llmwiki/tmp/tool.lock is created. Vault tree is identical.
func TestToolAskNoChangeset(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	reg := tools.NewRegistry(askToolDeps(e))
	ctx := trace.WithVerb(context.Background(), trace.VerbQuery)
	wantRes, err := reg.Call(ctx, "wiki.search", []byte(`{"q":"kv"}`))
	if err != nil {
		t.Fatalf("direct Call: %v", err)
	}
	e.Close()

	snapBefore := vaultTreeSnapshot(t, root)

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"tool", "--ask", "--vault", root, "wiki.search", `{"q":"kv"}`})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	wantOut := wantRes.Content + "\n"
	if stdout != wantOut {
		t.Fatalf("stdout mismatch:\ngot:\n%s\nwant:\n%s", stdout, wantOut)
	}

	lockPath := filepath.Join(root, ".llmwiki", "tmp", "tool.lock")
	if _, err := os.Stat(lockPath); err == nil {
		t.Error("tool.lock unexpectedly exists on ask path")
	}

	snapAfter := vaultTreeSnapshot(t, root)
	assertSnapshotsEqual(t, snapBefore, snapAfter)
}

// TestToolAskSeesStagedWithNote pins 068 D4.1:
// an open changeset stages a create_page; waitingNoteNow fixed.
// lw tool --ask wiki.search for a word only in that page -> stdout has hit plus
// (staged in <id>, unreviewed), stderr == stagedReadNoteLine(cs, now).
// lw tool --ask wiki.get -> stdout starts with citable prefix, stderr has same note.
// Committed-only hit -> no stderr.
func TestToolAskSeesStagedWithNote(t *testing.T) {
	fixedNow := time.Date(2026, 10, 23, 14, 0, 0, 0, time.UTC)
	origWaitingNoteNow := waitingNoteNow
	waitingNoteNow = func() time.Time { return fixedNow }
	t.Cleanup(func() { waitingNoteNow = origWaitingNoteNow })

	root := testutil.CopyFixture(t, "minimal")
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	cs, err := e.OpenChangeset("staged note test", stage.Author{Kind: "user"})
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	_, err = e.Append(stage.Op{
		Kind:       stage.OpCreatePage,
		Path:       "wiki/concepts/echidna.md",
		Content:    []byte("---\ntitle: Echidna Concept\ncreated: 2026-08-30\nupdated: 2026-08-30\ntype: concept\ntags: [inference, memory]\nsources: [raw/articles/kv-cache-explained.md]\nconfidence: medium\n---\n\n# Echidna Concept\n\nUnique monotreme echidna content.^[raw/articles/kv-cache-explained.md]\n\n## Related\n\n- [[kv-cache]]\n- [[flash-attention]]\n"),
		Rationale:  "staged note test",
		Provenance: []string{"raw/articles/kv-cache-explained.md"},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	curCS, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	e.Close()

	wantNote := stagedReadNoteLine(curCS, fixedNow)

	// 1. wiki.search for staged-only word
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"tool", "--ask", "--vault", root, "wiki.search", `{"q":"echidna"}`})
	})
	if code != 0 {
		t.Fatalf("wiki.search exit code = %d, want 0; stderr = %q", code, stderr)
	}
	tagLine := fmt.Sprintf("(staged in %s, unreviewed)", cs.ID)
	if !strings.Contains(stdout, tagLine) {
		t.Fatalf("wiki.search stdout missing tag line %q; got:\n%s", tagLine, stdout)
	}
	if stderr != wantNote {
		t.Fatalf("wiki.search stderr mismatch:\ngot:\n%s\nwant:\n%s", stderr, wantNote)
	}

	// 2. wiki.get of staged page
	stdout, stderr, code = captureRun(t, func() int {
		return run([]string{"tool", "--ask", "--vault", root, "wiki.get", `{"page":"wiki/concepts/echidna.md"}`})
	})
	if code != 0 {
		t.Fatalf("wiki.get exit code = %d, want 0; stderr = %q", code, stderr)
	}
	if !strings.HasPrefix(stdout, "(staged in the open changeset, not yet committed") {
		t.Fatalf("wiki.get stdout missing citable prefix; got:\n%s", stdout)
	}
	if stderr != wantNote {
		t.Fatalf("wiki.get stderr mismatch:\ngot:\n%s\nwant:\n%s", stderr, wantNote)
	}

	// 3. Committed-only hit -> no stderr
	stdout, stderr, code = captureRun(t, func() int {
		return run([]string{"tool", "--ask", "--vault", root, "wiki.search", `{"q":"bandwidth"}`})
	})
	if code != 0 {
		t.Fatalf("wiki.search committed exit code = %d, want 0; stderr = %q", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("wiki.search committed hit unexpectedly emitted stderr: %q", stderr)
	}
}

// TestToolAskDuringPreparedIngest pins 068 D4.1:
// after lw ingest --prepare, lw tool --ask raw.get leaves session.ndjson
// byte-identical. Subsequent ingest lw tool call records as usual.
func TestToolAskDuringPreparedIngest(t *testing.T) {
	pv := prepareVault(t, 100)
	sessionPath := filepath.Join(pv.root, ".llmwiki", "changesets", "open", pv.csID, "session.ndjson")
	sessBefore, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatalf("read session before: %v", err)
	}

	rawArgs := fmt.Sprintf(`{"source":%q,"chunk":1}`, pv.raw)
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"tool", "--ask", "--vault", pv.root, "raw.get", rawArgs})
	})
	if code != 0 {
		t.Fatalf("tool --ask exit code = %d, want 0; stderr = %q stdout = %q", code, stderr, stdout)
	}

	sessAfter, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatalf("read session after ask: %v", err)
	}
	if !bytes.Equal(sessBefore, sessAfter) {
		t.Fatalf("session.ndjson was modified during tool --ask:\nbefore:\n%s\nafter:\n%s", string(sessBefore), string(sessAfter))
	}

	// Ingest-path lw tool call records its call in the session.
	stdout, stderr, code = captureRun(t, func() int {
		return run([]string{"tool", "--vault", pv.root, "raw.get", rawArgs})
	})
	if code != 0 {
		t.Fatalf("ingest tool call exit code = %d, want 0; stderr = %q stdout = %q", code, stderr, stdout)
	}
	sessFinal, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatalf("read session after ingest tool: %v", err)
	}
	if len(sessFinal) <= len(sessAfter) {
		t.Fatalf("ingest tool call did not append to session.ndjson: before %d bytes, after %d bytes", len(sessAfter), len(sessFinal))
	}
}

// TestToolAskRefusesNonAskTool pins 068 D4.1:
// stage.* and web.search calls to lw tool --ask exit 2 with exact error string,
// and leave the vault tree identical.
func TestToolAskRefusesNonAskTool(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	snapBefore := vaultTreeSnapshot(t, root)

	toolsToTest := []string{
		"stage.create_page",
		"stage_create_page",
		"stage.open",
		"stage.close",
		"web.search",
	}

	for _, name := range toolsToTest {
		t.Run(name, func(t *testing.T) {
			stdout, stderr, code := captureRun(t, func() int {
				return run([]string{"tool", "--ask", "--vault", root, name})
			})
			if code != 2 {
				t.Fatalf("exit code = %d, want 2; stdout = %q, stderr = %q", code, stdout, stderr)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want empty", stdout)
			}
			wantStderr := fmt.Sprintf("lw: tool: %s is not offered for ask (lw tool --ask --list shows the tools)\n", name)
			if stderr != wantStderr {
				t.Fatalf("stderr mismatch:\ngot:\n%s\nwant:\n%s", stderr, wantStderr)
			}
		})
	}

	snapAfter := vaultTreeSnapshot(t, root)
	assertSnapshotsEqual(t, snapBefore, snapAfter)
}

// TestToolAskList pins 068 D4.1:
// lw tool --ask --list -> JSON array matching tools.AskToolNames() and registry schemas.
// lw tool --ask --brief -> exit 2.
func TestToolAskList(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"tool", "--ask", "--list", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}

	var listings []toolListing
	if err := json.Unmarshal([]byte(stdout), &listings); err != nil {
		t.Fatalf("unmarshal listings: %v", err)
	}

	wantNames := tools.AskToolNames()
	if len(listings) != len(wantNames) {
		t.Fatalf("got %d tool listings, want %d", len(listings), len(wantNames))
	}

	reg := tools.NewRegistry(tools.Deps{})
	for i, name := range wantNames {
		if listings[i].Name != name {
			t.Errorf("tool[%d].Name = %q, want %q", i, listings[i].Name, name)
		}
		expectedTool, ok := reg.Get(name)
		if !ok {
			t.Fatalf("tool %q not in registry", name)
		}
		if listings[i].Description != expectedTool.Description {
			t.Errorf("tool[%d].Description mismatch for %q", i, name)
		}
		var gotSchema, wantSchema any
		if err := json.Unmarshal(listings[i].InputSchema, &gotSchema); err != nil {
			t.Fatalf("unmarshal listings[%d].InputSchema: %v", i, err)
		}
		if err := json.Unmarshal(expectedTool.Schema, &wantSchema); err != nil {
			t.Fatalf("unmarshal expectedTool.Schema: %v", err)
		}
		if !reflect.DeepEqual(gotSchema, wantSchema) {
			t.Errorf("tool[%d].InputSchema mismatch for %q", i, name)
		}
	}

	// lw tool --ask --brief -> exit 2
	_, _, code = captureRun(t, func() int {
		return run([]string{"tool", "--ask", "--brief", "--vault", root})
	})
	if code != 2 {
		t.Fatalf("tool --ask --brief exit code = %d, want 2", code)
	}
}

// TestToolListUnchanged pins I2: lw tool --list stdout is byte-identical to v2.52.0 baseline.
func TestToolListUnchanged(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"tool", "--list", "--vault", root})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}

	goldenPath := filepath.Join("testdata", "068", "tool-list.json")
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden %s: %v", goldenPath, err)
	}
	if stdout != string(want) {
		t.Fatalf("tool --list mismatch against v2.52.0 golden:\ngot:\n%s\nwant:\n%s", stdout, string(want))
	}
}

type mcpStdioClient struct {
	inWriter  *os.File
	outReader *bufio.Reader
	done      chan int
	restoreIO func()
}

func startMCPCmdClient(t *testing.T, args []string) *mcpStdioClient {
	t.Helper()
	origIn, origOut := os.Stdin, os.Stdout
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe in: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe out: %v", err)
	}
	os.Stdin = inR
	os.Stdout = outW

	done := make(chan int, 1)
	go func() {
		code := run(args)
		outW.Close()
		done <- code
	}()

	client := &mcpStdioClient{
		inWriter:  inW,
		outReader: bufio.NewReader(outR),
		done:      done,
		restoreIO: func() {
			os.Stdin = origIn
			os.Stdout = origOut
		},
	}
	t.Cleanup(func() {
		client.restoreIO()
	})
	return client
}

func (c *mcpStdioClient) send(t *testing.T, req string) {
	t.Helper()
	if !strings.HasSuffix(req, "\n") {
		req += "\n"
	}
	if _, err := io.WriteString(c.inWriter, req); err != nil {
		t.Fatalf("send request: %v", err)
	}
}

func (c *mcpStdioClient) readLine(t *testing.T) []byte {
	t.Helper()
	type res struct {
		line []byte
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		line, err := c.outReader.ReadBytes('\n')
		ch <- res{line, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("readLine: %v", r.err)
		}
		return r.line
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for MCP response")
		return nil
	}
}

func (c *mcpStdioClient) close(t *testing.T) {
	t.Helper()
	_ = c.inWriter.Close()
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Fatal("MCP server did not exit on EOF")
	}
	c.restoreIO()
}

// TestMCPDefaultToolsListUnchanged pins I2:
// plain lw mcp tools/list response == cmd/lw/testdata/068/mcp-tools-list.json.
// The initialize response has no "instructions".
func TestMCPDefaultToolsListUnchanged(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	client := startMCPCmdClient(t, []string{"mcp", "--vault", root})
	defer client.close(t)

	const initReq = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
	client.send(t, initReq)
	line1 := client.readLine(t)
	if bytes.Contains(line1, []byte(`"instructions"`)) {
		t.Fatalf("initialize response unexpectedly contains 'instructions': %s", string(line1))
	}

	const listReq = `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`
	client.send(t, listReq)
	line2 := client.readLine(t)

	goldenPath := filepath.Join("testdata", "068", "mcp-tools-list.json")
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden %s: %v", goldenPath, err)
	}
	if string(bytes.TrimSpace(line2)) != string(bytes.TrimSpace(want)) {
		t.Fatalf("mcp tools/list mismatch against v2.52.0 golden:\ngot:\n%s\nwant:\n%s", string(line2), string(want))
	}
}

// TestMCPAsk pins 068 D4.2:
// lw mcp --ask -> initialize instructions == agent.AskSystemPrompt(),
// tools/list == the 8 ask tools (MCP names), wiki_search over a staged-only
// page -> the tag line. Works with no config file.
func TestMCPAsk(t *testing.T) {
	prepHermetic(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // empty config dir: no config file

	root := testutil.CopyFixture(t, "minimal")
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	cs, err := e.OpenChangeset("mcp ask test", stage.Author{Kind: "user"})
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	_, err = e.Append(stage.Op{
		Kind:       stage.OpCreatePage,
		Path:       "wiki/concepts/platypus.md",
		Content:    []byte("---\ntitle: Platypus Concept\ncreated: 2026-08-30\nupdated: 2026-08-30\ntype: concept\ntags: [inference, memory]\nsources: [raw/articles/kv-cache-explained.md]\nconfidence: medium\n---\n\n# Platypus Concept\n\nMonotreme platypus research.^[raw/articles/kv-cache-explained.md]\n\n## Related\n\n- [[kv-cache]]\n- [[flash-attention]]\n"),
		Rationale:  "staged page test",
		Provenance: []string{"raw/articles/kv-cache-explained.md"},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	e.Close()

	client := startMCPCmdClient(t, []string{"mcp", "--ask", "--vault", root})
	defer client.close(t)

	const initReq = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
	client.send(t, initReq)
	line1 := client.readLine(t)
	var initResp struct {
		Result struct {
			Instructions string `json:"instructions"`
		} `json:"result"`
	}
	if err := json.Unmarshal(line1, &initResp); err != nil {
		t.Fatalf("unmarshal init response: %v", err)
	}
	if initResp.Result.Instructions != agent.AskSystemPrompt() {
		t.Fatalf("instructions mismatch:\ngot:\n%s\nwant:\n%s", initResp.Result.Instructions, agent.AskSystemPrompt())
	}

	const listReq = `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`
	client.send(t, listReq)
	line2 := client.readLine(t)
	var listResp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(line2, &listResp); err != nil {
		t.Fatalf("unmarshal list response: %v", err)
	}
	if len(listResp.Result.Tools) != 8 {
		t.Fatalf("got %d tools, want 8", len(listResp.Result.Tools))
	}
	for i, name := range tools.AskToolNames() {
		mcpName := mcp.MCPName(name)
		if listResp.Result.Tools[i].Name != mcpName {
			t.Errorf("tool[%d] = %q, want %q", i, listResp.Result.Tools[i].Name, mcpName)
		}
	}

	callReq := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"wiki_search","arguments":{"q":"platypus"}}}`
	client.send(t, callReq)
	line3 := client.readLine(t)
	var callResp struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(line3, &callResp); err != nil {
		t.Fatalf("unmarshal call response: %v", err)
	}
	if callResp.Result.IsError || len(callResp.Result.Content) == 0 {
		t.Fatalf("call failed: %s", string(line3))
	}
	tagLine := fmt.Sprintf("(staged in %s, unreviewed)", cs.ID)
	if !strings.Contains(callResp.Result.Content[0].Text, tagLine) {
		t.Fatalf("search result missing tag line %q; got:\n%s", tagLine, callResp.Result.Content[0].Text)
	}
}
