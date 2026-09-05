package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEndpointTrimsTrailingSlash(t *testing.T) {
	tests := []struct {
		baseURL string
		want    string
	}{
		{"https://api.example.com/v1", "https://api.example.com/v1/chat/completions"},
		{"https://api.example.com/v1/", "https://api.example.com/v1/chat/completions"},
	}
	for _, tt := range tests {
		c := New(Config{BaseURL: tt.baseURL})
		if got := c.endpoint(); got != tt.want {
			t.Errorf("endpoint() for BaseURL %q = %q, want %q", tt.baseURL, got, tt.want)
		}
	}
}

func TestBuildRequestBody(t *testing.T) {
	c := New(Config{
		BaseURL:     "http://example.com",
		Model:       "test-model",
		Temperature: 0.5,
		MaxTokens:   256,
	})
	req := Request{
		Messages: []Message{{Role: "user", Content: "hello"}},
		Tools: []ToolDef{{
			Name:        "wiki.search",
			Description: "search the wiki",
			Parameters:  json.RawMessage(`{"type":"object"}`),
		}},
	}
	b, err := c.buildRequestBody(req)
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}

	var wr wireRequest
	if err := json.Unmarshal(b, &wr); err != nil {
		t.Fatalf("decode wire body: %v", err)
	}
	if wr.Model != "test-model" {
		t.Errorf("Model = %q, want test-model", wr.Model)
	}
	if !wr.Stream {
		t.Error("Stream = false, want true — Stream must always request stream:true")
	}
	if wr.Temperature != 0.5 || wr.MaxTokens != 256 {
		t.Errorf("Temperature/MaxTokens = %v/%v, want 0.5/256", wr.Temperature, wr.MaxTokens)
	}
	if len(wr.Messages) != 1 || wr.Messages[0].Content != "hello" {
		t.Errorf("Messages = %+v", wr.Messages)
	}
	if len(wr.Tools) != 1 {
		t.Fatalf("Tools = %+v, want 1 entry", wr.Tools)
	}
	if wr.Tools[0].Type != "function" {
		t.Errorf("Tools[0].Type = %q, want function", wr.Tools[0].Type)
	}
	if wr.Tools[0].Function.Name != "wiki.search" {
		t.Errorf("Tools[0].Function.Name = %q, want wiki.search", wr.Tools[0].Function.Name)
	}
}

func TestBuildRequestBodyNoTools(t *testing.T) {
	c := New(Config{BaseURL: "http://example.com", Model: "m"})
	b, err := c.buildRequestBody(Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := raw["tools"]; ok {
		t.Error(`wire body has a "tools" key with no tools requested; want it omitted`)
	}
}

// flakyTransport fails the first N RoundTrips at the transport level (no
// response, just an error) and delegates the rest to inner — used to test
// Client.do's single-retry behavior without a real network failure.
type flakyTransport struct {
	failFirst int
	calls     int
	inner     http.RoundTripper
}

func (f *flakyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.calls++
	if f.calls <= f.failFirst {
		return nil, errors.New("simulated transport failure")
	}
	return f.inner.RoundTrip(req)
}

func TestClientSingleTransportRetry(t *testing.T) {
	srv := sseServer(t, loadFixture(t, "simple.sse"), nil)
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "test-model"})
	ft := &flakyTransport{failFirst: 1, inner: http.DefaultTransport}
	c.httpClient.Transport = ft

	ch, err := c.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v (want the single retry to succeed)", err)
	}
	_ = collect(t, ch)

	if ft.calls != 2 {
		t.Errorf("transport RoundTrip called %d times, want exactly 2 (1 failure + 1 retry)", ft.calls)
	}
}

func TestClientRetryExhausted(t *testing.T) {
	c := New(Config{BaseURL: "http://127.0.0.1:1", Model: "test-model"})
	ft := &flakyTransport{failFirst: 10, inner: http.DefaultTransport}
	c.httpClient.Transport = ft

	_, err := c.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("Stream: got nil error, want the exhausted retry's error")
	}
	if ft.calls != 2 {
		t.Errorf("transport RoundTrip called %d times, want exactly 2 (no more than one retry)", ft.calls)
	}
}

func TestClientAuthorizationHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(loadFixture(t, "simple.sse"))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "test-model", APIKey: "secret-key"})
	ch, err := c.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	_ = collect(t, ch)

	if gotAuth != "Bearer secret-key" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer secret-key")
	}
}
