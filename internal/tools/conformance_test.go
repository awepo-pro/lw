package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

type conformanceStep struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

type conformanceScript struct {
	Name               string            `json:"name"`
	Steps              []conformanceStep `json:"steps"`
	Golden             []string          `json:"golden"`
	CommitMessage      string            `json:"commit_message"`
	ExpectPaths        []string          `json:"expect_paths"`
	ExpectErrors       int               `json:"expect_errors"`
	ExpectCascadeHunks int               `json:"expect_cascade_hunks"`
}

type conformanceExtractor struct {
	doc *extract.Doc
}

func (e conformanceExtractor) CanHandle(uri string) bool { return strings.HasSuffix(uri, ".md") }

func (e conformanceExtractor) Extract(context.Context, string) (*extract.Doc, error) {
	doc := *e.doc
	return &doc, nil
}

var dynamicIDRE = regexp.MustCompile(`(?:cs-[0-9a-f]{16}|op[0-9]+)`)

func TestConformance(t *testing.T) {
	cases := []string{"orient-search-get", "ingest-and-create", "rename-cascade", "rejection"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			runConformanceScript(t, name)
		})
	}
}

func runConformanceScript(t *testing.T, name string) {
	t.Helper()
	script := readConformanceScript(t, name)
	if script.Name != "" && script.Name != name {
		t.Fatalf("script name = %q, want %q", script.Name, name)
	}

	var extractor extract.Extractor
	if name == "ingest-and-create" {
		extractor = conformanceExtractor{doc: &extract.Doc{
			Title:     "Conformance Source",
			SourceURL: "https://example.test/conformance",
			Markdown:  "# Conformance Source\n\nA deterministic source for the tool transcript.\n",
			Kind:      "article",
			Extractor: "test",
		}}
	}
	reg, engine, dir := engineRegistry(t, extractor)
	ctx := context.Background()
	startingTree := vaultBytes(t, dir)

	var transcript []string
	var errorCount int
	for i, step := range script.Steps {
		beforeChangeset := openChangesetBytes(t, dir)
		res, err := reg.Call(ctx, step.Tool, step.Args)
		if err != nil {
			t.Fatalf("step %d %s: %v", i+1, step.Tool, err)
		}
		transcript = append(transcript, normalizeConformanceContent(res.Content))
		if res.IsError {
			errorCount++
			if !bytes.Equal(beforeChangeset, openChangesetBytes(t, dir)) {
				t.Fatalf("step %d %s rejected but changed changeset.json", i+1, step.Tool)
			}
		}
		assertTreeEqual(t, startingTree, vaultBytes(t, dir), fmt.Sprintf("after step %d (%s)", i+1, step.Tool))
	}

	if script.ExpectErrors != errorCount {
		t.Fatalf("validation errors = %d, want %d", errorCount, script.ExpectErrors)
	}
	if name == "rejection" {
		cs, err := engine.Current()
		if err != nil {
			t.Fatalf("Current after rejected proposals: %v", err)
		}
		if len(cs.Ops) != 0 {
			t.Fatalf("rejected proposals changed changeset: %d ops present", len(cs.Ops))
		}
	}
	if len(script.Golden) == 0 {
		t.Fatalf("script has no golden transcript; got %q", transcript)
	}
	if len(script.Golden) != len(transcript) {
		t.Fatalf("transcript length = %d, want %d\n got: %q", len(transcript), len(script.Golden), transcript)
	}
	for i := range transcript {
		if transcript[i] != script.Golden[i] {
			j := 0
			for j < len(transcript[i]) && j < len(script.Golden[i]) && transcript[i][j] == script.Golden[i][j] {
				j++
			}
			lo := j - 30
			if lo < 0 {
				lo = 0
			}
			hi := j + 60
			if hi > len(transcript[i]) {
				hi = len(transcript[i])
			}
			t.Fatalf("transcript[%d] differs at byte %d (len got=%d want=%d): got around=%q want around=%q", i, j, len(transcript[i]), len(script.Golden[i]), transcript[i][lo:hi], script.Golden[i][lo:minInt(hi, len(script.Golden[i]))])
		}
	}

	if script.ExpectCascadeHunks > 0 {
		cs, err := engine.Current()
		if err != nil {
			t.Fatalf("Current: %v", err)
		}
		got := 0
		for _, op := range cs.Ops {
			if op.Kind != stage.OpRenamePage {
				continue
			}
			for _, sub := range op.Cascade {
				got += len(sub.Hunks)
			}
		}
		if got != script.ExpectCascadeHunks {
			t.Fatalf("cascade hunks = %d, want %d", got, script.ExpectCascadeHunks)
		}
	}

	// A read-only or all-rejected script leaves the changeset with no live
	// op; Commit refuses exactly that with ErrNothingToCommit (008 contract
	// §3, C-802) and writes nothing, so the harness asserts the refusal
	// instead of expecting a commit id. Any other script commits as before.
	csBeforeCommit, csErr := engine.Current()
	zeroLive := csErr == nil && len(csBeforeCommit.Live()) == 0

	commitID, err := engine.Commit(script.CommitMessage)
	if zeroLive {
		if !errors.Is(err, stage.ErrNothingToCommit) {
			t.Fatalf("Commit of a changeset with no live ops = %v, want an error matching stage.ErrNothingToCommit", err)
		}
	} else {
		if err != nil {
			t.Fatalf("Commit: %v", err)
		}
		if commitID == "" {
			t.Fatal("Commit returned an empty commit id")
		}
	}
	for _, path := range script.ExpectPaths {
		if info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(path))); err != nil {
			t.Errorf("expected committed path %s: %v", path, err)
		} else if info.IsDir() {
			t.Errorf("expected committed path %s is a directory", path)
		}
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func readConformanceScript(t *testing.T, name string) conformanceScript {
	t.Helper()
	path := filepath.Join(testutil.FixtureRoot(t), "conformance", name+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read conformance script %s: %v", path, err)
	}
	var script conformanceScript
	if err := json.Unmarshal(b, &script); err != nil {
		t.Fatalf("parse conformance script %s: %v", path, err)
	}
	return script
}

func normalizeConformanceContent(content string) string {
	return dynamicIDRE.ReplaceAllString(content, "<id>")
}

func openChangesetBytes(t *testing.T, dir string) []byte {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, ".llmwiki", "changesets", "open"))
	if err != nil {
		t.Fatalf("read open changesets: %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, ".llmwiki", "changesets", "open", entry.Name(), "changeset.json"))
		if err != nil {
			t.Fatalf("read changeset %s: %v", entry.Name(), err)
		}
		return b
	}
	return nil
}

func assertTreeEqual(t *testing.T, want, got map[string][]byte, label string) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("working tree changed %s: file count %d, want %d", label, len(got), len(want))
	}
	for path, wantBytes := range want {
		gotBytes, ok := got[path]
		if !ok {
			t.Fatalf("working tree changed %s: missing %s", label, path)
		}
		if !bytes.Equal(wantBytes, gotBytes) {
			t.Fatalf("working tree changed %s: %s differs", label, path)
		}
	}
}
