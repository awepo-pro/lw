package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

// urlIngestRegistry is stage_source_test.go's harness with the REAL
// extractor chain cmd/lw wires (010 contract §3/§4): the go/html extractor
// over T-A's guarded house client, falling back to the local file
// extractor. No extractor fake — the URL subtests drive the real fetch.
func urlIngestRegistry(t *testing.T) (*Registry, *stage.Engine) {
	t.Helper()
	dir := testutil.CopyFixture(t, "minimal")
	e, err := stage.OpenEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	v := e.Vault()
	return NewRegistry(Deps{
		Vault:  v,
		Index:  index.Build(v),
		Engine: e,
		Extract: extract.Chain(
			extract.NewHTML(extract.NewHTTPClient(10*time.Second)),
			extract.NewFile(),
		),
		Author: stage.Author{Kind: "agent", Model: "test"},
	}), e
}

// openForIngest opens the changeset the way the model does, failing the
// test on anything but a clean open.
func openForIngest(t *testing.T, reg *Registry) {
	t.Helper()
	if r, err := reg.Call(context.Background(), "stage.open", json.RawMessage(`{"intent":"ingest a fetched page"}`)); err != nil || r.IsError {
		t.Fatalf("stage.open: %+v %v", r, err)
	}
}

// TestIngestSourceAcceptsURL is the 010 un-defer regression (MASTER block
// T-C): stage.ingest_source fetches an http(s) URL through the real
// extractor chain and stages it exactly as a local file's — same dedupe,
// naming, validator and Engine.Append — while a fetch failure comes back
// as an IsError result the model can recover from, never a turn abort.
// Permanent regression tests (D-10C).
func TestIngestSourceAcceptsURL(t *testing.T) {
	t.Run("http_uri_stages_raw", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<!doctype html><html><head><title>Quaternion Notes</title></head>" +
				"<body><h1>Quaternion Notes</h1><p>Quaternions extend the complex numbers.</p></body></html>"))
		}))
		defer srv.Close()

		reg, e := urlIngestRegistry(t)
		openForIngest(t, reg)
		r, err := reg.Call(context.Background(), "stage.ingest_source",
			json.RawMessage(`{"uri":"`+srv.URL+`/quaternion-notes.html"}`))
		if err != nil {
			t.Fatalf("stage.ingest_source: %v", err)
		}
		if r.IsError {
			t.Fatalf("ingest rejected: %s", r.Content)
		}

		fd := proposedRawDiff(t, e)
		if fd.Path != "raw/articles/quaternion-notes.md" {
			t.Errorf("staged path = %q, want raw/articles/quaternion-notes.md", fd.Path)
		}
		// The body must be the REAL fetched page, extracted by go/html —
		// an extractor fake in Deps.Extract could never produce this
		// sentence, which is what makes this subtest the fetch-path proof.
		if !strings.Contains(fd.New, "Quaternions extend the complex numbers.") {
			t.Errorf("staged body is not the fetched page's extracted markdown:\n%q", fd.New)
		}
		cs, err := e.Current()
		if err != nil {
			t.Fatal(err)
		}
		live := cs.Live()
		if len(live) != 1 || live[0].Kind != stage.OpIngestSource {
			t.Errorf("Engine.Append accepted %d live ops, want the 1 ingest_source", len(live))
		}
	})

	t.Run("fetch_failure_is_error_result", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "not found", http.StatusNotFound)
		}))
		defer srv.Close()

		reg, e := urlIngestRegistry(t)
		openForIngest(t, reg)
		r, err := reg.Call(context.Background(), "stage.ingest_source",
			json.RawMessage(`{"uri":"`+srv.URL+`/missing.html"}`))
		if err != nil {
			t.Fatalf("fetch failure returned a Go error %v; the turn must not abort", err)
		}
		if !r.IsError {
			t.Fatalf("404 result = %+v, want IsError", r)
		}
		if !strings.Contains(r.Content, "extract") {
			t.Errorf("Content = %q, want it to name the failing extract stage", r.Content)
		}
		cs, err := e.Current()
		if err != nil {
			t.Fatal(err)
		}
		if n := len(cs.Live()); n != 0 {
			t.Errorf("a failed fetch staged %d ops", n)
		}
	})

	t.Run("description_names_http", func(t *testing.T) {
		d := stageIngestSourceTool(Deps{}).Description
		if !strings.Contains(d, "http(s) URL") {
			t.Errorf("Description = %q, want it to name http(s) URL", d)
		}
		if strings.Contains(d, "Network URLs are rejected") {
			t.Errorf("Description still rejects network URLs: %q", d)
		}
	})
}
