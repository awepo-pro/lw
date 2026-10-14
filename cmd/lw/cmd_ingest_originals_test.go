package main

// cmd_ingest_originals_test.go pins 058 FIX1's F4: `lw ingest` keeps a
// verbatim original of every kind of source, end to end. The internal/tools
// tests prove stage.ingest_source stages an original when the Doc carries
// one; this proves the Doc still carries it by the time it gets there — lw
// ingest extracts each source itself, writes only the markdown to a scratch
// file, and hands the tools a preExtracted copy of the Doc (C-123), so a copy
// that drops OriginalBytes would silently lose every html, text and markdown
// original while every tools-level test stayed green.

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// TestIngestCLIKeepsOriginals: a folder of a .HTM page, a CRLF .md note, a
// .txt file and a .markdown file, plus an http URL, ingested through run()
// with the tool-calling fake agent — each source's staged original has the
// source's name beside its raw and exactly the source's bytes.
func TestIngestCLIKeepsOriginals(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	const remotePage = "<html><body><h1>Remote Probe</h1><p>served over http</p></body></html>\r\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, remotePage)
	}))
	defer srv.Close()

	src := t.TempDir()
	files := map[string]string{
		"Page.HTM":     "<html><body><h1>Cli Page</h1><p>from a saved page</p></body></html>\r\n",
		"note.md":      "# Cli Note\r\n\r\nfrom a markdown note\r\n\r\n- one\r\n",
		"plain.txt":    "plain words only, no heading at all\n",
		"doc.markdown": "# Mk Doc\n\nthe long markdown extension\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(src, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	withFakeIngestAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
		return newToolCallingFakeAgent(e, cfg, sessions, ex), nil
	})
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--vault", root, src, srv.URL})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}

	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()
	cs, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	store, err := stage.OpenStore(filepath.Join(root, ".llmwiki", "objects"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	d, err := e.Diff()
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	want := map[string]struct {
		original string
		bytes    string
	}{
		"raw/articles/cli-page.md":     {"raw/articles/cli-page.htm", files["Page.HTM"]},
		"raw/articles/cli-note.md":     {"raw/articles/cli-note.md.orig", files["note.md"]},
		"raw/articles/plain.md":        {"raw/articles/plain.txt", files["plain.txt"]},
		"raw/articles/mk-doc.md":       {"raw/articles/mk-doc.markdown", files["doc.markdown"]},
		"raw/articles/remote-probe.md": {"raw/articles/remote-probe.html", remotePage},
	}
	seen := 0
	for _, op := range cs.Ops {
		if op.Kind != stage.OpIngestSource {
			continue
		}
		seen++
		w, ok := want[op.Path]
		if !ok {
			t.Errorf("unexpected staged raw %s (original %q)", op.Path, op.OriginalPath)
			continue
		}
		if op.OriginalPath != w.original {
			t.Errorf("%s: original path = %q, want %q", op.Path, op.OriginalPath, w.original)
			continue
		}
		got, err := store.Get(op.Original)
		if err != nil {
			t.Errorf("%s: original bytes not in the CAS under %q: %v", op.Path, op.Original, err)
			continue
		}
		if !bytes.Equal(got, []byte(w.bytes)) {
			t.Errorf("%s: original = %q, want the source's bytes %q", op.Path, got, w.bytes)
		}
		raw, err := vault.ParseRawSource(op.Path, []byte(findFileDiffNew(t, d, op.Path)))
		if err != nil {
			t.Errorf("%s: staged raw does not parse: %v", op.Path, err)
			continue
		}
		if raw.Original != w.original || raw.OriginalSHA256 != op.Original {
			t.Errorf("%s: frontmatter original = %q / %q, want %q / %q", op.Path, raw.Original, raw.OriginalSHA256, w.original, op.Original)
		}
		if strings.Contains(raw.Body, "\r") {
			t.Errorf("%s: raw body kept a carriage return, want it normalized", op.Path)
		}
	}
	if seen != len(want) {
		t.Errorf("staged %d ingest ops, want %d (stdout=%q)", seen, len(want), stdout)
	}
}
