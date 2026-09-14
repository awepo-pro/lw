package e2e

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeRequest is one chat-completions POST the fake server received.
type fakeRequest struct {
	Path string // request path, e.g. "/v1/chat/completions"
	Auth string // the Authorization header, verbatim ("Bearer ..." or "")
	Body []byte // the JSON request body, for assertions on what lw sent
}

// fakeRound is one scripted response: Body is replayed byte-for-byte as the
// SSE payload of exactly one chat-completions response, in the order rounds
// were handed to newFakeLLM. Name only labels failure messages.
type fakeRound struct {
	Name string // which scenario this round belongs to
	Body string // the exact SSE payload, "data: ..." lines and all
}

// fakeLLM is an OpenAI-compatible chat-completions server that answers each
// POST /chat/completions with the next scripted round and records every
// request. When the script is exhausted it replies 500, so an agent loop
// that runs more rounds than were scripted fails loudly instead of looping
// against a server that silently repeats its last answer.
type fakeLLM struct {
	srv    *httptest.Server
	mu     sync.Mutex
	rounds []fakeRound
	served int
	reqs   []fakeRequest
}

// newFakeLLM starts the fake server with the given script; it closes when
// the test ends.
func newFakeLLM(t *testing.T, rounds ...fakeRound) *fakeLLM {
	t.Helper()

	f := &fakeLLM{rounds: rounds}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// URL is the base URL a config's base_url points at. The client appends
// "/chat/completions" (llm.Client.endpoint), so a base_url of URL()+"/v1"
// reaches this server at /v1/chat/completions.
func (f *fakeLLM) URL() string { return f.srv.URL }

// Served reports how many requests the server has answered, counted as each
// one arrives — including any that hit the exhausted-script 500.
func (f *fakeLLM) Served() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.served
}

// Requests returns every request received so far, in arrival order.
func (f *fakeLLM) Requests() []fakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeRequest(nil), f.reqs...)
}

// serve is the handler behind every response the fake gives.
func (f *fakeLLM) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	f.mu.Lock()
	i := f.served
	f.served++
	f.reqs = append(f.reqs, fakeRequest{Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Body: body})
	rounds := len(f.rounds)
	var round fakeRound
	if i < rounds {
		round = f.rounds[i]
	}
	f.mu.Unlock()

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if i >= rounds {
		http.Error(w, fmt.Sprintf("fake LLM: no scripted round for request %d (%d scripted)", i+1, rounds), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, round.Body)
}

// sseFixture loads one scripted round from this package's testdata directory,
// verbatim.
func sseFixture(t *testing.T, name string) fakeRound {
	t.Helper()

	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("e2e: read SSE fixture %s: %v", name, err)
	}
	return fakeRound{Name: name, Body: string(b)}
}

// harnessFakellmIngestRoundtrip runs a real ingest against the fake LLM: a
// two-round script (one stage_create_page tool call, then a stop) drives
// `lw ingest` to a validated, staged op that `lw status` and `lw diff` both
// see — with the subprocess never reaching a network.
func harnessFakellmIngestRoundtrip(t *testing.T) {
	t.Helper()

	e := newEnv(t)
	vault := newVault(t)
	fake := newFakeLLM(t,
		sseFixture(t, "ingest_round1.sse"),
		sseFixture(t, "ingest_round2.sse"),
	)
	writeConfig(t, e.config, fake.URL()+"/v1")

	src := e.writeSource(t, "sources/e2e-round-trip.md", "# E2E Round Trip\n\nA local markdown source, extracted by the passthrough extractor and\nhanded to the curator agent as scratch.\n")

	ingest := runLW(t, e, "ingest", "--vault", vault, "--kind", "article", src)
	if ingest.Code != 0 {
		t.Fatalf("lw ingest: exit %d, want 0\n%s", ingest.Code, ingest.Output)
	}

	// The scripted conversation was consumed exactly: one POST per round,
	// both carrying the literal key the harness config wrote.
	if got, want := fake.Served(), 2; got != want {
		t.Fatalf("fake LLM served %d requests, want %d", got, want)
	}
	reqs := fake.Requests()
	if len(reqs) != 2 {
		t.Fatalf("fake LLM recorded %d requests, want 2", len(reqs))
	}
	for i, req := range reqs {
		if want := "Bearer " + fakeAPIKey; req.Auth != want {
			t.Errorf("request %d: Authorization = %q, want %q", i+1, req.Auth, want)
		}
		if !strings.HasSuffix(req.Path, "/chat/completions") {
			t.Errorf("request %d: path = %q, want a /chat/completions path", i+1, req.Path)
		}
	}

	status := runLW(t, e, "status", "--vault", vault)
	if status.Code != 0 {
		t.Fatalf("lw status: exit %d, want 0\n%s", status.Code, status.Output)
	}
	m := openOpsRE.FindStringSubmatch(status.Output)
	if m == nil {
		t.Fatalf("lw status: no open changeset line in output\n%s", status.Output)
	}
	if ops := m[1]; ops == "0" {
		t.Errorf("lw status: open changeset reports 0 op(s), want at least 1\n%s", status.Output)
	}

	diff := runLW(t, e, "diff", "--vault", vault, "--stat")
	if diff.Code != 0 {
		t.Fatalf("lw diff --stat: exit %d, want 0\n%s", diff.Code, diff.Output)
	}
	if want := "wiki/concepts/e2e-round-trip.md"; !strings.Contains(diff.Output, want) {
		t.Errorf("lw diff --stat: output does not mention the staged page %s\n%s", want, diff.Output)
	}
}
