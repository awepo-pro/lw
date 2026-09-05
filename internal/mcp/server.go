package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/awepo-pro/lw/internal/tools"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewServer binds the current registry to an MCP server. The registry is the
// sole source of tool definitions: MCP only translates names and transports
// calls, so it cannot add an out-of-band filesystem capability.
func NewServer(r *tools.Registry, version string) *sdk.Server {
	if r == nil {
		panic("mcp: nil registry")
	}

	// Keep SDK diagnostics off the protocol stream. The CLI uses the same
	// server constructor, so all diagnostics are sent to stderr.
	server := sdk.NewServer(
		&sdk.Implementation{Name: "lw", Version: version},
		&sdk.ServerOptions{Logger: slog.New(slog.NewTextHandler(os.Stderr, nil))},
	)
	for _, definition := range r.List() {
		tool := definition
		server.AddTool(&sdk.Tool{
			Name:        MCPName(tool.Name),
			Description: tool.Description,
			InputSchema: json.RawMessage(tool.Schema),
		}, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			// Capture the canonical name per iteration. The callback is invoked
			// asynchronously after registration, so it must not consult a later
			// loop value.
			result, err := r.Call(ctx, tool.Name, req.Params.Arguments)
			if err != nil {
				return nil, err
			}
			return &sdk.CallToolResult{
				Content: []sdk.Content{&sdk.TextContent{Text: result.Content}},
				IsError: result.IsError,
			}, nil
		})
	}
	return server
}

// Serve runs an MCP server over newline-delimited JSON on in and out. The
// SDK's IOTransport lets tests supply pipes or buffers while the CLI supplies
// os.Stdin and os.Stdout.
func Serve(ctx context.Context, r *tools.Registry, in io.Reader, out io.Writer) error {
	if in == nil || out == nil {
		return fmt.Errorf("mcp: nil stdio stream")
	}
	server := NewServer(r, "0.1.0-dev")
	state := &stdioState{response: make(chan struct{})}
	transport := &sdk.IOTransport{
		Reader: newInputReader(in, state),
		Writer: nopCloserWriter{Writer: responseWriter{Writer: out, state: state}},
	}
	err := server.Run(ctx, transport)
	// A peer closing stdin is the normal end of a one-shot stdio probe. The
	// SDK wraps that EOF in its server-closing sentinel; do not turn it into a
	// CLI error after the response has already been delivered.
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

type nopCloserReader struct {
	io.Reader
}

func (nopCloserReader) Close() error { return nil }

type forwardingReader struct {
	io.Reader
	io.Closer
}

func newInputReader(in io.Reader, state *stdioState) io.ReadCloser {
	r := eofGuardReader{Reader: in, state: state}
	if closer, ok := in.(io.Closer); ok {
		return forwardingReader{Reader: r, Closer: closer}
	}
	return nopCloserReader{Reader: r}
}

type nopCloserWriter struct {
	io.Writer
}

func (nopCloserWriter) Close() error { return nil }

// stdioState coordinates the reader and writer around the SDK's EOF handling.
// The SDK cancels in-flight requests when its reader reports EOF, so a
// one-shot client that closes stdin immediately after initialize needs a short
// grace period for the response to be written first.
type stdioState struct {
	response chan struct{}
	once     sync.Once
}

func (s *stdioState) markResponse() {
	s.once.Do(func() { close(s.response) })
}

type responseWriter struct {
	io.Writer
	state *stdioState
}

func (w responseWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if err == nil && n > 0 {
		w.state.markResponse()
	}
	return n, err
}

type eofGuardReader struct {
	io.Reader
	state *stdioState
}

func (r eofGuardReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err != io.EOF || n != 0 {
		return n, err
	}

	// Wait for a response when this is a one-shot request. The timeout keeps a
	// notification-only or malformed stream from hanging forever.
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-r.state.response:
	case <-timer.C:
	}
	return 0, io.EOF
}
