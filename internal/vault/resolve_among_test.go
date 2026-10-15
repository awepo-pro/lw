package vault

// resolve_among_test.go freezes 060 D1: ResolveAmong is backbone §2.9's four
// resolution steps over an arbitrary path set, so wiki.get can resolve a page
// that exists only in the open changeset the way it resolves a committed one
// (the 10-08 joined recompile turn hit max_rounds after six not-found
// wiki.get calls on pages it had staged itself). snapshot.resolve is NOT
// refactored onto it — the committed hot path stays byte-identical — so
// TestResolveAmongMatchesResolve pins parity differentially instead.

import (
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// pagePaths lists v's committed page paths, the set a differential test
// hands to ResolveAmong.
func pagePaths(v *Vault) []string {
	var out []string
	for _, p := range v.Pages() {
		out = append(out, p.Path)
	}
	return out
}

// TestResolveAmongMatchesResolve is the parity pin: over every page of the
// minimal fixture, each spelling a model uses for it — full path, path
// without ".md", basename with and without ".md", an upper-cased basename,
// "<parent>/<basename>", a name nothing answers to and the empty target —
// resolves through ResolveAmong exactly as vault.Resolve resolves it, both
// return values.
func TestResolveAmongMatchesResolve(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	v, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	all := pagePaths(v)
	if len(all) == 0 {
		t.Fatal("minimal fixture has no pages")
	}
	for _, p := range all {
		base := strings.TrimSuffix(path.Base(p), ".md")
		targets := []string{
			p,
			strings.TrimSuffix(p, ".md"),
			base,
			base + ".md",
			strings.ToUpper(base),
			path.Base(path.Dir(p)) + "/" + base,
			"no-such-page",
			"",
		}
		for _, target := range targets {
			wantP, wantOK := Resolve(v, target)
			gotP, gotOK := ResolveAmong(all, target)
			if gotP != wantP || gotOK != wantOK {
				t.Errorf("page %s, target %q: ResolveAmong = (%q, %v), Resolve = (%q, %v)",
					p, target, gotP, gotOK, wantP, wantOK)
			}
		}
	}
}

// TestResolveAmongMatchesResolveDirStep extends the parity pin to the one
// step the minimal fixture cannot reach: "<dir>/<target>.md". It needs a
// page directly under wiki/ (so a target such as "concepts/kv-cache" is
// "<dir>/<target>.md" for dir "wiki") and two pages whose basenames differ
// only in case (so the basename step is ambiguous while exactly one
// directory holds the exact-case file).
func TestResolveAmongMatchesResolveDirStep(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	v, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	const page = "---\ntitle: Extra\ncreated: 2026-01-01\nupdated: 2026-01-01\ntype: concept\n---\n\n# Extra\n\n- [[kv-cache]]\n"
	for _, rel := range []string{"wiki/overview.md", "wiki/misc/Zed.md", "wiki/other/zed.md"} {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := writeTestFile(t, full, page); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	if err := v.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	all := pagePaths(v)
	for _, tc := range []struct {
		target string
		want   string
		ok     bool
	}{
		{"concepts/kv-cache", "wiki/concepts/kv-cache.md", true}, // dir "wiki" + target
		{"entities/gpt-4.md", "wiki/entities/gpt-4.md", true},
		{"zed", "wiki/other/zed.md", true}, // basename ambiguous by case, one exact-case file
		{"Zed", "wiki/misc/Zed.md", true},  // the other spelling picks the other file
		{"overview", "wiki/overview.md", true},
		{"nope/kv-cache", "", false},
	} {
		wantP, wantOK := Resolve(v, tc.target)
		if wantP != tc.want || wantOK != tc.ok {
			t.Fatalf("fixture drift: Resolve(%q) = (%q, %v), want (%q, %v)", tc.target, wantP, wantOK, tc.want, tc.ok)
		}
		gotP, gotOK := ResolveAmong(all, tc.target)
		if gotP != wantP || gotOK != wantOK {
			t.Errorf("target %q: ResolveAmong = (%q, %v), Resolve = (%q, %v)", tc.target, gotP, gotOK, wantP, wantOK)
		}
	}
}

// TestResolveAmongAmbiguous: two pages sharing a basename make the basename
// step fail — never guess — while each full path still answers.
func TestResolveAmongAmbiguous(t *testing.T) {
	paths := []string{"wiki/concepts/foo.md", "wiki/entities/foo.md"}
	if got, ok := ResolveAmong(paths, "foo"); ok {
		t.Errorf(`ResolveAmong(%v, "foo") = (%q, true), want a miss`, paths, got)
	}
	for _, p := range paths {
		if got, ok := ResolveAmong(paths, p); !ok || got != p {
			t.Errorf("ResolveAmong(%v, %q) = (%q, %v), want (%q, true)", paths, p, got, ok, p)
		}
	}
}

// TestResolveAmongDuplicatePaths: one path listed twice is still one page,
// so its basename is not ambiguous. A caller may well pass the same staged
// path twice (a create_page and a later op on it both list it).
func TestResolveAmongDuplicatePaths(t *testing.T) {
	paths := []string{"wiki/a/x.md", "wiki/a/x.md"}
	got, ok := ResolveAmong(paths, "x")
	if !ok || got != "wiki/a/x.md" {
		t.Errorf(`ResolveAmong(%v, "x") = (%q, %v), want ("wiki/a/x.md", true)`, paths, got, ok)
	}
}

// TestResolveAmongSteps pins each step on a hand-built set, so a step that
// goes missing fails here by name rather than only through the fixture.
func TestResolveAmongSteps(t *testing.T) {
	paths := []string{"wiki/a/X.md", "wiki/b/x.md", "wiki/top.md", "wiki/concepts/paged-attention.md"}
	for _, tc := range []struct {
		name   string
		paths  []string
		target string
		want   string
		ok     bool
	}{
		{"exact path", paths, "wiki/top.md", "wiki/top.md", true},
		{"path without .md", paths, "wiki/top", "wiki/top.md", true},
		{"basename", paths, "paged-attention", "wiki/concepts/paged-attention.md", true},
		{"basename with .md", paths, "paged-attention.md", "wiki/concepts/paged-attention.md", true},
		{"basename, other case", paths, "Paged-Attention", "wiki/concepts/paged-attention.md", true},
		{"basename ambiguous by case, dir step picks the exact-case file", paths, "x", "wiki/b/x.md", true},
		{"dir step with a nested target", paths, "a/X", "wiki/a/X.md", true},
		{"dir step is exact-case", paths, "a/x", "", false},
		{"unknown", paths, "no-such-page", "", false},
		{"empty target", paths, "", "", false},
		{"empty set", nil, "x", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ResolveAmong(tc.paths, tc.target)
			if got != tc.want || ok != tc.ok {
				t.Errorf("ResolveAmong(%v, %q) = (%q, %v), want (%q, %v)", tc.paths, tc.target, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestResolveAmongNestedDirsCountOnce: sorted paths do not keep one
// directory's files together — "wiki/a/c/d.md" sorts between two files of
// wiki/a — so a directory holding several files must still be tried once by
// the dir step. Tried per file, wiki/a would hit "wiki/a/c/d.md" twice and
// the single match would read as an ambiguity.
func TestResolveAmongNestedDirsCountOnce(t *testing.T) {
	paths := []string{"wiki/a/a.md", "wiki/a/c/d.md", "wiki/a/z.md"}
	if got, ok := ResolveAmong(paths, "c/d"); !ok || got != "wiki/a/c/d.md" {
		t.Errorf(`ResolveAmong(%v, "c/d") = (%q, %v), want ("wiki/a/c/d.md", true)`, paths, got, ok)
	}
}

// TestResolveAmongDuplicatePathsMisCased: the duplicate pin that the dir step
// cannot rescue. With the exact-case spelling the basename step may fail on
// a duplicate and "<dir>/<target>.md" still finds the page; a mis-cased
// target has no exact-case file, so only a deduplicated basename step
// answers.
func TestResolveAmongDuplicatePathsMisCased(t *testing.T) {
	paths := []string{"wiki/a/X.md", "wiki/a/X.md"}
	got, ok := ResolveAmong(paths, "x")
	if !ok || got != "wiki/a/X.md" {
		t.Errorf(`ResolveAmong(%v, "x") = (%q, %v), want ("wiki/a/X.md", true)`, paths, got, ok)
	}
}
