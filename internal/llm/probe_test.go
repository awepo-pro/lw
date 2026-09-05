package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestProbeDetectsToolCalling(t *testing.T) {
	var sawMaxTokens int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var sent wireRequest
		_ = json.NewDecoder(r.Body).Decode(&sent)
		sawMaxTokens = sent.MaxTokens
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(loadFixture(t, "split_tool_call.sse"))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "probe-model", MaxTokens: 8192})
	result := c.Probe(context.Background())

	if !result.Reachable {
		t.Errorf("Reachable = false, want true: %v", result.Err)
	}
	if !result.ToolCalling {
		t.Error("ToolCalling = false, want true — the fixture returns a complete tool call")
	}
	if result.Model != "probe-model" {
		t.Errorf("Model = %q, want probe-model", result.Model)
	}
	if result.Err != nil {
		t.Errorf("Err = %v, want nil", result.Err)
	}
	if sawMaxTokens != probeMaxTokens {
		t.Errorf("Probe sent max_tokens=%d, want %d", sawMaxTokens, probeMaxTokens)
	}
	if c.cfg.MaxTokens != 8192 {
		t.Errorf("Probe mutated the client's own MaxTokens to %d; it must only affect its own request", c.cfg.MaxTokens)
	}
}

// TestProbeSendsLargerMaxTokens pins the wire body Probe actually sends: a
// one-token budget cuts a model off before it can emit a function call
// (backbone §8, C-101, D-CP), so this is the regression that would
// otherwise silently come back — the value is invisible from ProbeResult,
// only the wire body proves it.
func TestProbeSendsLargerMaxTokens(t *testing.T) {
	var sawMaxTokens int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var sent wireRequest
		_ = json.NewDecoder(r.Body).Decode(&sent)
		sawMaxTokens = sent.MaxTokens
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(loadFixture(t, "split_tool_call.sse"))
	}))
	defer srv.Close()

	// The client's own MaxTokens is deliberately tiny (1) to prove Probe
	// does not inherit it — it must send its own, larger budget.
	c := New(Config{BaseURL: srv.URL, Model: "probe-model", MaxTokens: 1})
	c.Probe(context.Background())

	if sawMaxTokens != probeMaxTokens {
		t.Errorf("Probe sent max_tokens=%d, want %d — a 1-token budget cuts the model off before it can emit a function call", sawMaxTokens, probeMaxTokens)
	}
	if sawMaxTokens < 32 {
		t.Errorf("Probe sent max_tokens=%d, which the measured table in backbone §8 shows is too small for a tool call to complete", sawMaxTokens)
	}
}

func TestProbeNoToolCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(loadFixture(t, "simple.sse"))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "probe-model"})
	result := c.Probe(context.Background())

	if !result.Reachable {
		t.Errorf("Reachable = false, want true: %v", result.Err)
	}
	if result.ToolCalling {
		t.Error("ToolCalling = true, want false — the endpoint never returned a tool call")
	}
}

func TestProbeUnreachable(t *testing.T) {
	c := New(Config{BaseURL: "http://127.0.0.1:0", Model: "probe-model"})
	result := c.Probe(context.Background())

	if result.Reachable {
		t.Error("Reachable = true, want false — nothing is listening on that address")
	}
	if result.Err == nil {
		t.Error("Err = nil, want a non-nil error explaining the failure")
	}
	if result.ToolCalling {
		t.Error("ToolCalling = true, want false when the endpoint could not be reached")
	}
}

// TestProbeLive is the one legitimate network test in this package
// (00-conventions.md §6): it is skipped unless DEEPSEEK_API_KEY is set, per
// backbone §8 and the OQ-4 provider decision.
func TestProbeLive(t *testing.T) {
	key := os.Getenv("DEEPSEEK_API_KEY")
	if key == "" {
		t.Skip("DEEPSEEK_API_KEY not set; skipping live provider probe")
	}

	c := New(Config{
		BaseURL: "https://api.deepseek.com/v1",
		Model:   "deepseek-chat",
		APIKey:  key,
	})
	result := c.Probe(context.Background())

	if !result.Reachable {
		t.Fatalf("live Probe: Reachable = false: %v", result.Err)
	}
	t.Logf("live Probe result: %+v", result)
}
