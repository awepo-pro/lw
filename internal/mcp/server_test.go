package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/tools"
)

func TestServeInitializeSmoke(t *testing.T) {
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reg := tools.NewRegistry(tools.Deps{})
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, reg, inputReader, outputWriter)
	}()

	const request = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"gate","version":"0"}}}` + "\n"
	if _, err := io.WriteString(inputWriter, request); err != nil {
		t.Fatalf("write initialize request: %v", err)
	}

	responseCh := make(chan []byte, 1)
	go func() {
		line, err := bufio.NewReader(outputReader).ReadBytes('\n')
		if err != nil {
			responseCh <- []byte("read error: " + err.Error())
			return
		}
		responseCh <- line
	}()

	var line []byte
	select {
	case line = <-responseCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for initialize response")
	}
	if bytes.HasPrefix(line, []byte("read error:")) {
		t.Fatal(string(line))
	}
	var response struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatalf("initialize response is not JSON: %v\n%s", err, line)
	}
	if response.JSONRPC != "2.0" || response.ID != 1 {
		t.Fatalf("initialize response envelope = %#v, want JSON-RPC 2.0 id 1", response)
	}
	if response.Result.ServerInfo.Name == "" {
		t.Fatalf("initialize response has no serverInfo: %s", line)
	}
	if response.Result.ServerInfo.Version == "" {
		t.Fatalf("initialize response has no serverInfo.version: %s", line)
	}
	if strings.TrimSpace(string(line)) == "" {
		t.Fatal("initialize response is empty")
	}

	// Closing the input and cancelling the context lets the server terminate
	// without requiring a client-side initialized notification in this smoke
	// test. The response above is the only data written to the output pipe.
	_ = inputWriter.Close()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not terminate after cancellation")
	}
}
