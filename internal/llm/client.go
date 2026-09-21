package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

// Client is an OpenAI-compatible chat-completions client bound to one
// endpoint, model and set of generation parameters (backbone §8).
// Construct with New; the zero value has no BaseURL and is not usable.
type Client struct {
	cfg        Config
	httpClient *http.Client
}

// New returns a Client configured from cfg. If cfg.Timeout is positive it
// bounds every HTTP round trip the Client makes, including the full
// duration of a streaming read; a zero Timeout leaves the underlying
// *http.Client unbounded and relies entirely on the caller's ctx for
// cancellation.
func New(cfg Config) *Client {
	hc := &http.Client{}
	if cfg.Timeout > 0 {
		hc.Timeout = cfg.Timeout
	}
	return &Client{cfg: cfg, httpClient: hc}
}

// endpoint returns the fully-qualified chat-completions URL for c's
// configured BaseURL, tolerating an optional trailing slash so both
// "https://api.example.com/v1" and "https://api.example.com/v1/" work.
func (c *Client) endpoint() string {
	return strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
}

// wireRequest is the JSON body Stream and Probe POST to
// {BaseURL}/chat/completions. Request itself carries no json tags
// (backbone §8) because it is not this wire shape directly: a ToolDef has
// no OpenAI "type":"function" wrapper, so buildRequestBody constructs one
// here instead of exporting it onto ToolDef.
type wireRequest struct {
	Model       string        `json:"model"`
	Messages    []Message     `json:"messages"`
	Tools       []wireTool    `json:"tools,omitempty"`
	Temperature float64       `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Thinking    *wireThinking `json:"thinking,omitempty"`
	Stream      bool          `json:"stream"`
}

// wireThinking is the GLM thinking-mode switch on the wire (022): z.ai's
// OpenAI-compatible API takes thinking:{"type":"enabled"|"disabled"}, with
// enabled as its own default. The pointer is nil — and the whole key omitted
// — unless Config.Thinking asked for one of the explicit values, so the
// "default" escape hatch leaves the provider's choice in force and no
// existing body moves a byte.
type wireThinking struct {
	Type string `json:"type"`
}

// wireTool and wireFunction are the OpenAI-compatible wire shape for one
// advertised tool, converted from a ToolDef by buildRequestBody.
type wireTool struct {
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

type wireFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// buildRequestBody converts req into the wire JSON body for c's configured
// model and generation parameters, always requesting "stream": true
// (backbone §8's Stream contract).
func (c *Client) buildRequestBody(req Request) ([]byte, error) {
	wr := wireRequest{
		Model:       c.cfg.Model,
		Messages:    req.Messages,
		Temperature: c.cfg.Temperature,
		MaxTokens:   c.cfg.MaxTokens,
		Stream:      true,
	}
	if len(req.Tools) > 0 {
		wr.Tools = make([]wireTool, len(req.Tools))
		for i, t := range req.Tools {
			wr.Tools[i] = wireTool{
				Type: "function",
				Function: wireFunction{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  t.Parameters,
				},
			}
		}
	}
	// Config.Thinking maps onto the GLM thinking switch (022): the explicit
	// values send their exact wire shape, "default" and "" leave the key out
	// so the provider's default applies. Stream and Probe share this body,
	// so one mapping covers both.
	switch c.cfg.Thinking {
	case "off":
		wr.Thinking = &wireThinking{Type: "disabled"}
	case "on":
		wr.Thinking = &wireThinking{Type: "enabled"}
	}
	b, err := json.Marshal(wr)
	if err != nil {
		return nil, fmt.Errorf("llm: encode request: %w", err)
	}
	return b, nil
}

// newHTTPRequest builds the POST for req against c's endpoint. The body is
// a *bytes.Reader, so http.NewRequestWithContext populates req.GetBody
// automatically — that is what lets do (below) replay the body on the one
// permitted transport-level retry.
func (c *Client) newHTTPRequest(ctx context.Context, req Request) (*http.Request, error) {
	body, err := c.buildRequestBody(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("llm: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if c.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	return httpReq, nil
}

// do sends httpReq and retries exactly once, and only on a transport-level
// failure — no response came back at all: connection refused, reset, or
// timed out. It never retries an HTTP-level error status; that is left for
// the caller to decide (backbone §8: "no retries beyond a single
// transport-level retry"). A retry after ctx is already done, or when the
// body cannot be replayed, is skipped in favor of returning the original
// error.
func (c *Client) do(httpReq *http.Request) (*http.Response, error) {
	// The file log's request line (010 contract §0): model, endpoint and
	// the body's byte count — never the body, which carries the prompt and
	// the API key's Authorization header stays out of it entirely.
	slog.Info("llm request", "model", c.cfg.Model, "url", httpReq.URL.String(), "prompt_bytes", httpReq.ContentLength)
	resp, err := c.httpClient.Do(httpReq)
	if err == nil {
		slog.Info("llm response", "status", resp.StatusCode)
		return resp, nil
	}
	if httpReq.Context().Err() != nil || httpReq.GetBody == nil {
		return nil, err
	}
	body, gbErr := httpReq.GetBody()
	if gbErr != nil {
		return nil, err
	}
	httpReq.Body = body
	resp, err = c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	slog.Info("llm response", "status", resp.StatusCode)
	return resp, nil
}
