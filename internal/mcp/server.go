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
	"github.com/awepo-pro/lw/internal/trace"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServeOptions narrows what a server offers. The zero value is exactly the server NewServer / ServeVersion build.
type ServeOptions struct {
	Tools        []string // canonical names to offer, in registry order; nil = every registered tool. A name the registry lacks is skipped.
	Verb         string   // non-empty: every tool call runs under trace.WithVerb(ctx, Verb)
	Instructions string   // the initialize result's "instructions"; "" = the field is absent
}

// NewServer binds the current registry to an MCP server. The registry is the
// sole source of tool definitions: MCP only translates names and transports
// calls, so it cannot add an out-of-band filesystem capability.
func NewServer(r *tools.Registry, version string) *sdk.Server {
	return NewServerWith(r, version, ServeOptions{})
}

// NewServerWith binds the current registry to an MCP server, filtered and
// configured according to opts (068 D2).
func NewServerWith(r *tools.Registry, version string, opts ServeOptions) *sdk.Server {
	if r == nil {
		panic("mcp: nil registry")
	}

	serverOpts := &sdk.ServerOptions{
		Logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
	if opts.Instructions != "" {
		serverOpts.Instructions = opts.Instructions
	}

	// Keep SDK diagnostics off the protocol stream. The CLI uses the same
	// server constructor, so all diagnostics are sent to stderr.
	server := sdk.NewServer(
		&sdk.Implementation{Name: "lw", Version: version},
		serverOpts,
	)

	var offered []tools.Tool
	if opts.Tools == nil {
		offered = r.List()
	} else {
		want := make(map[string]bool, len(opts.Tools))
		for _, name := range opts.Tools {
			want[name] = true
		}
		for _, tool := range r.List() {
			if want[tool.Name] {
				offered = append(offered, tool)
			}
		}
	}

	for _, definition := range offered {
		tool := definition
		server.AddTool(&sdk.Tool{
			Name:        MCPName(tool.Name),
			Description: tool.Description,
			InputSchema: json.RawMessage(tool.Schema),
		}, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			// Capture the canonical name per iteration. The callback is invoked
			// asynchronously after registration, so it must not consult a later
			// loop value.
			callCtx := ctx
			if opts.Verb != "" {
				callCtx = trace.WithVerb(ctx, opts.Verb)
			}
			result, err := r.Call(callCtx, tool.Name, req.Params.Arguments)
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
// os.Stdin and os.Stdout. The server reports the unknown-version fallback;
// callers that know their build version use ServeVersion.
func Serve(ctx context.Context, r *tools.Registry, in io.Reader, out io.Writer) error {
	return ServeVersion(ctx, r, "0.0.0-unknown", in, out)
}

// ServeVersion is Serve with the serverInfo.version made explicit. Serve's
// shape is frozen (backbone §7), so the stamped build version enters here:
// cmd/lw passes the same -ldflags string `lw version` prints.
func ServeVersion(ctx context.Context, r *tools.Registry, version string, in io.Reader, out io.Writer) error {
	return ServeWith(ctx, r, version, ServeOptions{}, in, out)
}

// ServeWith runs an MCP server configured with opts over newline-delimited JSON
// on in and out (068 D2).
func ServeWith(ctx context.Context, r *tools.Registry, version string, opts ServeOptions, in io.Reader, out io.Writer) error {
	if in == nil || out == nil {
		return fmt.Errorf("mcp: nil stdio stream")
	}
	server := NewServerWith(r, version, opts)
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
