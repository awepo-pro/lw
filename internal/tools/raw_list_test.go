package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// emptyRawVault builds a vault directory with a SCHEMA.md and no raw/ file.
func emptyRawVault(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SCHEMA.md"), []byte(testSchemaMD), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func rawListCall(t *testing.T, reg *Registry, args string) string {
	t.Helper()
	res, err := reg.Call(context.Background(), "raw.list", json.RawMessage(args))
	if err != nil {
		t.Fatalf("raw.list(%s): %v", args, err)
	}
	if res.IsError {
		t.Fatalf("raw.list(%s) IsError: %s", args, res.Content)
	}
	return res.Content
}

// wantCommittedRow builds the exact row line contract §4.2 prescribes for
// one committed source, from the vault's own view of it.
func wantCommittedRow(r *vault.RawSource) string {
	return strings.Join([]string{
		r.Path,
		r.Title,
		r.SourceURL,
		"ingested " + r.Ingested.String(),
		"sha " + r.SHA256[:8],
	}, " — ")
}

func TestRawList(t *testing.T) {
	t.Run("committed_rows_sorted_by_path", func(t *testing.T) {
		deps := newTestDeps(t, testutil.CopyFixture(t, "minimal"))
		reg := NewRegistry(deps)
		got := rawListCall(t, reg, `{}`)
		lines := strings.Split(got, "\n")

		sources := deps.Vault.RawSources()
		if len(lines) != 1+len(sources) {
			t.Fatalf("raw.list = %q, want a header plus %d rows", got, len(sources))
		}
		if want := fmt.Sprintf("%d raw source(s)", len(sources)); lines[0] != want {
			t.Errorf("header = %q, want %q", lines[0], want)
		}
		for i, r := range sources {
			if lines[i+1] != wantCommittedRow(r) {
				t.Errorf("row %d = %q, want %q", i+1, lines[i+1], wantCommittedRow(r))
			}
		}
	})

	t.Run("empty_vault_says_no_raw_sources", func(t *testing.T) {
		reg := NewRegistry(newTestDeps(t, emptyRawVault(t)))
		if got := rawListCall(t, reg, `{}`); got != "no raw sources" {
			t.Fatalf("raw.list = %q, want exactly %q", got, "no raw sources")
		}
	})

	t.Run("limit_caps_and_counts_the_rest", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		for _, rel := range []string{"raw/articles/z1.md", "raw/articles/z2.md", "raw/articles/z3.md"} {
			writeRawSource(t, dir, rel, "Limit filler body for "+rel+".\n")
		}
		reg := NewRegistry(newTestDeps(t, dir))
		got := rawListCall(t, reg, `{"limit":2}`)
		lines := strings.Split(got, "\n")

		if lines[0] != "5 raw source(s)" {
			t.Errorf("header = %q, want the matched count before the limit", lines[0])
		}
		if len(lines) != 4 {
			t.Fatalf("raw.list returned %d lines, want header + 2 rows + the more line:\n%s", len(lines), got)
		}
		if lines[3] != "(and 3 more)" {
			t.Errorf("last line = %q, want %q", lines[3], "(and 3 more)")
		}

		// An oversized limit is capped at the schema maximum, never more
		// than 200 rows out.
		capped := rawListCall(t, reg, `{"limit":500}`)
		if n := strings.Count(capped, "\n"); n > 200 {
			t.Errorf("limit 500 produced %d lines, want at most 201 (200 rows + header)", n)
		}
	})

	t.Run("query_is_case_insensitive_on_title", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		writeRawSource(t, dir, "raw/articles/glm.md", "# GLM-5.2: 744B on M3 Ultra\n\nBody text.\n")
		reg := NewRegistry(newTestDeps(t, dir))

		for _, query := range []string{"GLM", "glm 5.2"} {
			got := rawListCall(t, reg, `{"query":"`+query+`"}`)
			lines := strings.Split(got, "\n")
			if len(lines) != 2 {
				t.Fatalf("query %q returned %d lines, want header + 1 row:\n%s", query, len(lines), got)
			}
			if want := `1 raw source(s) matching "` + query + `"`; lines[0] != want {
				t.Errorf("header = %q, want %q", lines[0], want)
			}
			if !strings.Contains(lines[1], "GLM-5.2: 744B on M3 Ultra") {
				t.Errorf("row = %q, want the GLM source", lines[1])
			}
		}
	})

	t.Run("query_requires_every_term", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		writeRawSource(t, dir, "raw/articles/glm.md", "# GLM-5.2: 744B on M3 Ultra\n\nBody text.\n")
		reg := NewRegistry(newTestDeps(t, dir))

		got := rawListCall(t, reg, `{"query":"glm banana"}`)
		if got != `no raw sources matching "glm banana"` {
			t.Fatalf("raw.list = %q, want an every-term miss", got)
		}
	})

	t.Run("query_with_no_match", func(t *testing.T) {
		reg := NewRegistry(newTestDeps(t, testutil.CopyFixture(t, "minimal")))
		got := rawListCall(t, reg, `{"query":"zzz"}`)
		if got != `no raw sources matching "zzz"` {
			t.Fatalf("raw.list = %q, want exactly the no-match line", got)
		}
	})

	t.Run("staged_rows_are_marked", func(t *testing.T) {
		doc := &extract.Doc{
			Title:     "Gemini",
			SourceURL: "https://example.test/gemini",
			Markdown:  "# Gemini\n\nA staged body raw.list must list.\n",
			Kind:      "article",
			Extractor: "test",
		}
		reg, e, _ := engineRegistry(t, &anyExtractor{docs: []*extract.Doc{doc}})
		nameOpen(t, reg)
		if r := nameIngest(t, reg, "gemini.md"); r.IsError {
			t.Fatalf("ingest refused: %s", r.Content)
		}
		cs, err := e.Current()
		if err != nil {
			t.Fatal(err)
		}

		got := rawListCall(t, reg, `{}`)
		lines := strings.Split(got, "\n")
		if lines[0] != "3 raw source(s)" {
			t.Errorf("header = %q, want %q", lines[0], "3 raw source(s)")
		}
		// raw/articles/gemini.md sorts before the fixture's two committed
		// sources, so the staged row is the first one.
		staged := "raw/articles/gemini.md — Gemini — https://example.test/gemini — staged in " + cs.ID
		if lines[1] != staged {
			t.Errorf("row 1 = %q, want %q", lines[1], staged)
		}
	})

	t.Run("untitled_placeholder", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		writeRawSource(t, dir, "raw/articles/no-title.md", "No heading anywhere in this body.\n")
		deps := newTestDeps(t, dir)
		reg := NewRegistry(deps)

		var want string
		for _, r := range deps.Vault.RawSources() {
			if r.Path == "raw/articles/no-title.md" {
				want = strings.Join([]string{r.Path, "(untitled)", r.SourceURL, "ingested " + r.Ingested.String(), "sha " + r.SHA256[:8]}, " — ")
			}
		}
		if want == "" {
			t.Fatal("the written source is missing from RawSources()")
		}
		if got := rawListCall(t, reg, `{"query":"no-title"}`); !strings.Contains(got, "\n"+want) {
			t.Fatalf("raw.list = %q, want the (untitled) row %q", got, want)
		}
	})
}

func TestRawListRegistration(t *testing.T) {
	t.Run("registry_has_18_tools", func(t *testing.T) {
		reg := minimalRegistry(t)
		list := reg.List()
		if len(list) != 18 {
			t.Fatalf("registry exposes %d tools, want 18", len(list))
		}
		for i, name := range wantToolNames {
			if list[i].Name != name {
				t.Errorf("List()[%d] = %q, want %q", i, list[i].Name, name)
			}
		}
	})

	t.Run("wire_name_round_trips", func(t *testing.T) {
		if got := WireName("raw.list"); got != "raw_list" {
			t.Errorf("WireName(raw.list) = %q, want raw_list", got)
		}
		if got := CanonicalName("raw_list"); got != "raw.list" {
			t.Errorf("CanonicalName(raw_list) = %q, want raw.list", got)
		}
		reg := minimalRegistry(t)
		for _, tool := range reg.List() {
			if tool.Name != "raw.list" {
				continue
			}
			if got := CanonicalName(WireName(tool.Name)); got != tool.Name {
				t.Errorf("CanonicalName(WireName(%q)) = %q", tool.Name, got)
			}
		}
	})

	t.Run("read_only_and_no_side_effects", func(t *testing.T) {
		doc := &extract.Doc{
			Title:     "Gemini",
			SourceURL: "https://example.test/gemini",
			Markdown:  "# Gemini\n\nBody.\n",
			Kind:      "article",
			Extractor: "test",
		}
		reg, e, dir := engineRegistry(t, &anyExtractor{docs: []*extract.Doc{doc}})
		tool, ok := reg.Get("raw.list")
		if !ok {
			t.Fatal("raw.list is not registered")
		}
		if !tool.ReadOnly {
			t.Fatal("raw.list must be ReadOnly")
		}

		// A staged op exercises the StagedFile row path, the read with the
		// most machinery behind it.
		nameOpen(t, reg)
		if r := nameIngest(t, reg, "gemini.md"); r.IsError {
			t.Fatalf("ingest refused: %s", r.Content)
		}
		if _, err := e.Current(); err != nil {
			t.Fatal(err)
		}

		journal := filepath.Join(dir, ".llmwiki", "journal.ndjson")
		beforeJournal, err := os.Stat(journal)
		if err != nil {
			t.Fatal(err)
		}
		beforeCS := openChangesetBytes(t, dir)

		rawListCall(t, reg, `{}`)
		rawListCall(t, reg, `{"query":"gemini"}`)

		afterJournal, err := os.Stat(journal)
		if err != nil {
			t.Fatal(err)
		}
		if afterJournal.Size() != beforeJournal.Size() {
			t.Errorf("journal size changed: %d -> %d", beforeJournal.Size(), afterJournal.Size())
		}
		if after := openChangesetBytes(t, dir); string(after) != string(beforeCS) {
			t.Error("the open changeset changed during a raw.list call")
		}
	})

	t.Run("raw_get_description_points_to_raw_list", func(t *testing.T) {
		reg := minimalRegistry(t)
		tool, ok := reg.Get("raw.get")
		if !ok {
			t.Fatal("raw.get is not registered")
		}
		want := "If you do not know a raw source's exact path, call raw.list first."
		if !strings.Contains(tool.Description, want) {
			t.Errorf("raw.get description = %q, want it to contain %q", tool.Description, want)
		}
	})

	t.Run("not_found_message_suggests_raw_list", func(t *testing.T) {
		reg := minimalRegistry(t)
		res, err := reg.Call(context.Background(), "raw.get", json.RawMessage(`{"source": "raw/does-not-exist.md"}`))
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError {
			t.Fatal("raw.get on an unknown source must be IsError")
		}
		want := "; call raw.list to see every raw source"
		if !strings.Contains(res.Content, want) {
			t.Fatalf("not-found message = %q, want it to contain %q", res.Content, want)
		}

		// With staged sources, the raw.list pointer comes BEFORE the
		// staged-paths suffix.
		doc := &extract.Doc{
			Title:     "Gemini",
			SourceURL: "https://example.test/gemini",
			Markdown:  "# Gemini\n\nBody.\n",
			Kind:      "article",
			Extractor: "test",
		}
		reg2, _, _ := engineRegistry(t, &anyExtractor{docs: []*extract.Doc{doc}})
		nameOpen(t, reg2)
		if r := nameIngest(t, reg2, "gemini.md"); r.IsError {
			t.Fatalf("ingest refused: %s", r.Content)
		}
		res2, err := reg2.Call(context.Background(), "raw.get", json.RawMessage(`{"source": "raw/articles/wrong-guess.md"}`))
		if err != nil {
			t.Fatal(err)
		}
		if !res2.IsError {
			t.Fatal("raw.get on a wrong guess must be IsError")
		}
		pointer := strings.Index(res2.Content, want)
		staged := strings.Index(res2.Content, "staged raw sources in the open changeset")
		if pointer < 0 || staged < 0 {
			t.Fatalf("not-found message = %q, want both the raw.list pointer and the staged list", res2.Content)
		}
		if pointer > staged {
			t.Errorf("not-found message = %q, want the raw.list pointer before the staged-paths suffix", res2.Content)
		}
	})
}
