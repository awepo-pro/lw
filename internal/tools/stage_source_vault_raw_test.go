package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// 055 S1d (review M2): the recompile turn message says "do not call
// stage.ingest_source" — each source already has the raw/ path given — but
// nothing enforced it. A probe staged raw/articles/recompiled-long-2.md from the
// raw's own absolute path: a raw-of-a-raw, whose body still carries the first
// one's frontmatter. raw is immutable and a raw is never ingested twice, so the
// tool itself refuses a local uri that lies under the vault's raw/, in every
// registry.

// vaultRawRefusal is the frozen refusal text for uri.
func vaultRawRefusal(uri string) string {
	return "stage.ingest_source: " + uri + " is already in the vault as a raw source — read it with raw.get and cite its raw/ path; a raw is never ingested twice"
}

// ingestFromRaw opens a changeset on reg, calls stage.ingest_source over uri and
// returns the result and the changeset's live ops.
func ingestFromRaw(t *testing.T, reg *Registry, e *stage.Engine, uri string) (Result, []stage.Op) {
	t.Helper()
	ctx := context.Background()
	if _, err := e.Current(); err != nil {
		if r, err := reg.Call(ctx, "stage.open", json.RawMessage(`{"intent":"ingest a source"}`)); err != nil || r.IsError {
			t.Fatalf("stage.open: %+v %v", r, err)
		}
	}
	args, err := json.Marshal(map[string]string{"uri": uri})
	if err != nil {
		t.Fatal(err)
	}
	res, err := reg.Call(ctx, "stage.ingest_source", args)
	if err != nil {
		t.Fatalf("stage.ingest_source: %v", err)
	}
	cs, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	return res, cs.Live()
}

// rawIngestDoc is a document no committed raw in the minimal fixture holds, so
// the only thing that can refuse it is the uri.
var rawIngestDoc = ingestDoc{Title: "Fresh Source", SourceURL: "https://example.test/fresh", Markdown: "# Fresh Source\n\nFresh body that no raw holds.\n", Kind: "article"}

// TestIngestSourceRefusesVaultRaw: a local uri under the vault's raw/ is refused
// with the exact text and no op is appended — however it is spelled: absolute,
// relative to the vault root (the working directory here), or through a
// symlink to the directory or to the file.
func TestIngestSourceRefusesVaultRaw(t *testing.T) {
	const rawRel = "raw/articles/kv-cache-explained.md"
	tests := []struct {
		name  string
		setup func(t *testing.T, root string) (uri string)
	}{
		{"absolute", func(t *testing.T, root string) string {
			return filepath.Join(root, filepath.FromSlash(rawRel))
		}},
		{"relative_to_the_root", func(t *testing.T, root string) string {
			t.Chdir(root)
			return rawRel
		}},
		{"dot_slash_and_dotdot", func(t *testing.T, root string) string {
			t.Chdir(root)
			return "./raw/papers/../articles/kv-cache-explained.md"
		}},
		{"through_a_symlinked_vault_root", func(t *testing.T, root string) string {
			link := filepath.Join(t.TempDir(), "vault-link")
			if err := os.Symlink(root, link); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(link, filepath.FromSlash(rawRel))
		}},
		{"through_a_symlink_to_the_file", func(t *testing.T, root string) string {
			link := filepath.Join(t.TempDir(), "alias.md")
			if err := os.Symlink(filepath.Join(root, filepath.FromSlash(rawRel)), link); err != nil {
				t.Fatal(err)
			}
			return link
		}},
		{"a_raw_the_fixture_has_in_another_kind_dir", func(t *testing.T, root string) string {
			return filepath.Join(root, "raw", "papers", "leviathan-2023.md")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg, e, root := engineRegistry(t, fakeExtractor{doc: rawIngestDoc.toExtract()})
			uri := tc.setup(t, root)
			before := vaultBytes(t, root)

			res, ops := ingestFromRaw(t, reg, e, uri)
			if !res.IsError || res.Content != vaultRawRefusal(uri) {
				t.Errorf("result = %+v, want IsError with %q", res, vaultRawRefusal(uri))
			}
			if len(ops) != 0 {
				t.Errorf("changeset has %d live op(s) %+v, want 0", len(ops), ops)
			}
			after := vaultBytes(t, root)
			if len(after) != len(before) {
				t.Errorf("vault has %d file(s), had %d", len(after), len(before))
			}
			for p, b := range before {
				if string(after[p]) != string(b) {
					t.Errorf("%s changed", p)
				}
			}
		})
	}
}

// TestIngestSourceOutsideRawUnchanged: only a path under raw/ is refused. A file
// outside the vault, one under the vault's wiki/ or a sibling directory that
// merely starts with "raw", and an http(s) URL whose path says /raw/ all stage
// exactly as before.
func TestIngestSourceOutsideRawUnchanged(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, root string) (uri string)
	}{
		{"temp_dir_file", func(t *testing.T, root string) string {
			p := filepath.Join(t.TempDir(), "note.md")
			if err := os.WriteFile(p, []byte("# Note\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return p
		}},
		{"vault_wiki_page", func(t *testing.T, root string) string {
			return filepath.Join(root, "wiki", "concepts", "kv-cache.md")
		}},
		{"directory_that_starts_with_raw", func(t *testing.T, root string) string {
			return filepath.Join(root, "raw-notes", "n.md")
		}},
		{"relative_with_the_vault_elsewhere", func(t *testing.T, root string) string {
			t.Chdir(t.TempDir())
			return "raw/articles/kv-cache-explained.md"
		}},
		{"https_url_with_raw_in_its_path", func(t *testing.T, root string) string {
			// From inside the vault's raw/, a URL resolved as a relative path
			// would land under it ("raw/https:/example.test/…"): it must not.
			t.Chdir(filepath.Join(root, "raw"))
			return "https://example.test/raw/articles/kv-cache-explained.md"
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg, e, root := engineRegistry(t, fakeExtractor{doc: rawIngestDoc.toExtract()})
			uri := tc.setup(t, root)

			res, ops := ingestFromRaw(t, reg, e, uri)
			if res.IsError {
				t.Fatalf("result = %+v, want a staged ingest", res)
			}
			if len(ops) != 1 || ops[0].Kind != stage.OpIngestSource {
				t.Errorf("live ops = %+v, want one ingest_source", ops)
			}
			if strings.Contains(res.Content, "already in the vault as a raw source") {
				t.Errorf("content = %q carries the raw refusal", res.Content)
			}
		})
	}
}

// TestIngestSourceVaultRawNeedsAVaultDir: a vault loaded without a directory
// (vault.OpenFS: Root() is "") has no raw/ on disk to compare a path against, so
// the rule has nothing to say and the tool behaves as before.
func TestIngestSourceVaultRawNeedsAVaultDir(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	e, err := stage.OpenEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	v, err := vault.OpenFS(os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry(Deps{Vault: v, Index: index.Build(v), Engine: e, Extract: fakeExtractor{doc: rawIngestDoc.toExtract()}, Author: stage.Author{Kind: "agent", Model: "test"}})

	res, ops := ingestFromRaw(t, reg, e, filepath.Join(dir, "raw", "articles", "kv-cache-explained.md"))
	if res.IsError || len(ops) != 1 {
		t.Errorf("result = %+v, ops = %+v, want a staged ingest: a vault with no directory cannot say what lies under its raw/", res, ops)
	}
}
