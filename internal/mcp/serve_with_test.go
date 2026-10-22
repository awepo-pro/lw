package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/trace"
)

type mcpTestClient struct {
	inWriter  *io.PipeWriter
	outReader *bufio.Reader
	done      chan error
	cancel    context.CancelFunc
}

func startMCPTestClient(t *testing.T, runFunc func(ctx context.Context, in io.Reader, out io.Writer) error) *mcpTestClient {
	t.Helper()
	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runFunc(ctx, inReader, outWriter)
	}()
	return &mcpTestClient{
		inWriter:  inWriter,
		outReader: bufio.NewReader(outReader),
		done:      done,
		cancel:    cancel,
	}
}

func (c *mcpTestClient) send(t *testing.T, req string) {
	t.Helper()
	if !strings.HasSuffix(req, "\n") {
		req += "\n"
	}
	if _, err := io.WriteString(c.inWriter, req); err != nil {
		t.Fatalf("send request: %v", err)
	}
}

func (c *mcpTestClient) readLine(t *testing.T) []byte {
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

func (c *mcpTestClient) close(t *testing.T) {
	t.Helper()
	_ = c.inWriter.Close()
	c.cancel()
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Fatal("MCP server did not terminate")
	}
}

func TestServeWithZeroOptionsByteIdentical(t *testing.T) {
	reg := tools.NewRegistry(tools.Deps{})
	version := "v2.52.0"

	const initReq = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}` + "\n"
	const listReq = `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}` + "\n"

	// Transcript 1: ServeVersion
	c1 := startMCPTestClient(t, func(ctx context.Context, in io.Reader, out io.Writer) error {
		return ServeVersion(ctx, reg, version, in, out)
	})
	c1.send(t, initReq)
	resp1Init := c1.readLine(t)
	c1.send(t, listReq)
	resp1List := c1.readLine(t)
	c1.close(t)
	transcript1 := append(resp1Init, resp1List...)

	// Transcript 2: ServeWith(..., ServeOptions{}, ...)
	c2 := startMCPTestClient(t, func(ctx context.Context, in io.Reader, out io.Writer) error {
		return ServeWith(ctx, reg, version, ServeOptions{}, in, out)
	})
	c2.send(t, initReq)
	resp2Init := c2.readLine(t)
	c2.send(t, listReq)
	resp2List := c2.readLine(t)
	c2.close(t)
	transcript2 := append(resp2Init, resp2List...)

	if !bytes.Equal(transcript1, transcript2) {
		t.Fatalf("transcripts differ:\nServeVersion:\n%s\nServeWith:\n%s", transcript1, transcript2)
	}

	if bytes.Contains(transcript1, []byte(`"instructions"`)) {
		t.Fatalf("transcript unexpectedly contains \"instructions\":\n%s", transcript1)
	}
	if bytes.Contains(transcript2, []byte(`"instructions"`)) {
		t.Fatalf("transcript unexpectedly contains \"instructions\":\n%s", transcript2)
	}
}

func TestServeWithToolFilter(t *testing.T) {
	reg := tools.NewRegistry(tools.Deps{})
	// A name the registry lacks is skipped silently.
	filter := append(append([]string(nil), tools.AskToolNames()...), "nonexistent.tool")
	opts := ServeOptions{Tools: filter}

	client := startMCPTestClient(t, func(ctx context.Context, in io.Reader, out io.Writer) error {
		return ServeWith(ctx, reg, "v2.52.0", opts, in, out)
	})
	defer client.close(t)

	client.send(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	_ = client.readLine(t)

	client.send(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	listBytes := client.readLine(t)

	var listResp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(listBytes, &listResp); err != nil {
		t.Fatalf("unmarshal tools/list: %v\n%s", err, listBytes)
	}

	var gotNames []string
	for _, tool := range listResp.Result.Tools {
		gotNames = append(gotNames, tool.Name)
	}

	var wantNames []string
	for _, n := range tools.AskToolNames() {
		wantNames = append(wantNames, MCPName(n))
	}

	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("tools/list names =\n%v\nwant\n%v", gotNames, wantNames)
	}
	if len(gotNames) != 8 {
		t.Fatalf("tools/list count = %d, want 8", len(gotNames))
	}

	// tools/call stage_open -> an MCP error (unknown tool), not a dispatch.
	client.send(t, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"stage_open","arguments":{"intent":"test"}}}`)
	callBytes := client.readLine(t)

	var callResp struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result *json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(callBytes, &callResp); err != nil {
		t.Fatalf("unmarshal tools/call: %v\n%s", err, callBytes)
	}

	if callResp.Error == nil {
		t.Fatalf("tools/call stage_open expected error, got result: %s", string(callBytes))
	}
	if !strings.Contains(callResp.Error.Message, "unknown tool") {
		t.Errorf("error message = %q, want containing \"unknown tool\"", callResp.Error.Message)
	}
}

func TestServeWithVerb(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	e, err := stage.OpenEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	author := stage.Author{Kind: "agent", Model: "test"}
	reg := tools.NewRegistry(tools.Deps{
		Vault:   e.Vault(),
		Index:   e.Index(),
		Engine:  e,
		Author:  author,
		Extract: extract.NewFile(),
	})

	cs, err := e.OpenChangeset("staged test", author)
	if err != nil {
		t.Fatal(err)
	}
	_ = cs

	body := "# Hack The Box\n\nHTB is an online platform for cybersecurity and penetration testing training.\n\n## Related\n\n- [[kv-cache]]\n- [[flash-attention]]\n"
	args := map[string]any{
		"path":       "wiki/entities/hack-the-box.md",
		"title":      "Hack The Box",
		"type":       "entity",
		"tags":       []string{"inference"},
		"sources":    []string{"raw/articles/kv-cache-explained.md"},
		"confidence": "high",
		"contested":  false,
		"body":       body,
		"rationale":  "test staged create",
	}
	argBytes, _ := json.Marshal(args)
	res, err := reg.Call(context.Background(), "stage.create_page", argBytes)
	if err != nil || res.IsError {
		t.Fatalf("stage.create_page: %v, content=%s", err, res.Content)
	}

	const callVaultOrient = `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"vault_orient","arguments":{}}}` + "\n"

	// 1. Through ServeWith{Verb: "query"}, vault_orient contains "## Open changeset (unreviewed)"
	cWith := startMCPTestClient(t, func(ctx context.Context, in io.Reader, out io.Writer) error {
		return ServeWith(ctx, reg, "v2.52.0", ServeOptions{Verb: trace.VerbQuery}, in, out)
	})
	cWith.send(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	_ = cWith.readLine(t)
	cWith.send(t, callVaultOrient)
	respWithBytes := cWith.readLine(t)
	cWith.close(t)

	var respWith struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respWithBytes, &respWith); err != nil {
		t.Fatalf("unmarshal ServeWith response: %v\n%s", err, respWithBytes)
	}
	if len(respWith.Result.Content) == 0 {
		t.Fatalf("ServeWith response has no content: %s", respWithBytes)
	}
	textWith := respWith.Result.Content[0].Text
	if !strings.Contains(textWith, "## Open changeset (unreviewed)") {
		t.Fatalf("ServeWith(Verb: query) vault_orient output missing open changeset section:\n%s", textWith)
	}

	// 2. Through ServeVersion, vault_orient does not contain "## Open changeset (unreviewed)"
	cVer := startMCPTestClient(t, func(ctx context.Context, in io.Reader, out io.Writer) error {
		return ServeVersion(ctx, reg, "v2.52.0", in, out)
	})
	cVer.send(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	_ = cVer.readLine(t)
	cVer.send(t, callVaultOrient)
	respVerBytes := cVer.readLine(t)
	cVer.close(t)

	var respVer struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respVerBytes, &respVer); err != nil {
		t.Fatalf("unmarshal ServeVersion response: %v\n%s", err, respVerBytes)
	}
	if len(respVer.Result.Content) == 0 {
		t.Fatalf("ServeVersion response has no content: %s", respVerBytes)
	}
	textVer := respVer.Result.Content[0].Text
	if strings.Contains(textVer, "## Open changeset (unreviewed)") {
		t.Fatalf("ServeVersion vault_orient output unexpectedly contains open changeset section:\n%s", textVer)
	}
}

func TestServeWithInstructions(t *testing.T) {
	reg := tools.NewRegistry(tools.Deps{})
	opts := ServeOptions{Instructions: "X\nY"}

	client := startMCPTestClient(t, func(ctx context.Context, in io.Reader, out io.Writer) error {
		return ServeWith(ctx, reg, "v2.52.0", opts, in, out)
	})
	defer client.close(t)

	client.send(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	initBytes := client.readLine(t)

	var initResp struct {
		Result struct {
			Instructions string `json:"instructions"`
		} `json:"result"`
	}
	if err := json.Unmarshal(initBytes, &initResp); err != nil {
		t.Fatalf("unmarshal initialize: %v\n%s", err, initBytes)
	}

	if initResp.Result.Instructions != "X\nY" {
		t.Fatalf("initialize instructions = %q, want %q", initResp.Result.Instructions, "X\nY")
	}
}
