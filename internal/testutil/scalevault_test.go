package testutil

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/vault"
)

// scaleTestNotes is the note count every subtest builds at: big enough to
// exercise the stride-based link scheme and the index/log derivation, small
// enough that five builds stay cheap.
const scaleTestNotes = 25

// TestScaleVault covers the NewScaleVault contract: the tree is
// byte-for-byte reproducible, vault.Open parses it with no errors, it holds
// exactly the n notes asked for, every wikilink resolves, and its SCHEMA.md
// parses with the taxonomy the generator draws tags from.
func TestScaleVault(t *testing.T) {
	t.Run("deterministic", func(t *testing.T) {
		a, b := t.TempDir(), t.TempDir()
		NewScaleVault(t, a, scaleTestNotes)
		NewScaleVault(t, b, scaleTestNotes)

		da, db := scaleTreeDigest(t, a), scaleTreeDigest(t, b)
		if da != db {
			t.Fatalf("two builds at n=%d differ: digest %s vs %s", scaleTestNotes, da, db)
		}
	})

	t.Run("no_parse_errors", func(t *testing.T) {
		v := scaleOpenVault(t, scaleTestNotes)

		if errs := v.ParseErrors(); len(errs) != 0 {
			var msgs []string
			for _, pe := range errs {
				msgs = append(msgs, pe.Error())
			}
			t.Fatalf("vault.Open reported %d parse errors:\n%s", len(errs), strings.Join(msgs, "\n"))
		}
	})

	t.Run("note_count", func(t *testing.T) {
		dir, v := scaleBuildVault(t, scaleTestNotes)

		if got := len(v.Pages()); got != scaleTestNotes {
			t.Fatalf("vault.Pages() = %d pages, want exactly %d", got, scaleTestNotes)
		}
		if got := scaleCountMarkdown(t, filepath.Join(dir, "wiki")); got != scaleTestNotes {
			t.Fatalf("wiki/ holds %d .md files, want exactly %d", got, scaleTestNotes)
		}
		for i, p := range v.Pages() {
			if want := ScaleNotePath(i); p.Path != want {
				t.Fatalf("page %d is at %s, want %s", i, p.Path, want)
			}
		}
	})

	t.Run("links_resolve", func(t *testing.T) {
		v := scaleOpenVault(t, scaleTestNotes)

		// Every wikilink in the vault resolves to a real page — the
		// generator only ever writes [[note-NNNN]] targets, so a broken
		// link here means the strides or the naming drifted.
		for _, p := range v.Pages() {
			for _, l := range p.Links {
				if _, ok := vault.Resolve(v, l.Target); !ok {
					t.Errorf("%s:%d [[%s]] does not resolve to any page", p.Path, l.Line, l.Target)
				}
			}
		}

		// Each note carries exactly scaleLinksPerNote links, to the three
		// (i+stride)%n targets — the shape that gives every note out-links
		// and at least one in-link.
		for i := 0; i < scaleTestNotes; i++ {
			p, ok := v.Page(ScaleNotePath(i))
			if !ok {
				t.Fatalf("vault has no page at %s", ScaleNotePath(i))
			}
			want := map[string]bool{}
			for _, stride := range scaleLinkStrides {
				want[ScaleNotePath((i+stride)%scaleTestNotes)] = true
			}
			got := map[string]bool{}
			for _, l := range p.Links {
				if target, ok := vault.Resolve(v, l.Target); ok {
					got[target] = true
				}
			}
			if len(got) != len(want) {
				t.Errorf("%s links to %d distinct pages, want %d (%v)", p.Path, len(got), len(want), want)
				continue
			}
			for target := range want {
				if !got[target] {
					t.Errorf("%s does not link to %s; its targets are %v", p.Path, target, got)
					break
				}
			}
		}

		// No orphans: the +1 stride cycle reaches every note, so every page
		// has at least one inbound ref.
		if orphans := v.Graph().Orphans(); len(orphans) != 0 {
			t.Fatalf("%d orphan pages with no inbound link: %v", len(orphans), orphans)
		}
	})

	t.Run("schema_parses", func(t *testing.T) {
		v := scaleOpenVault(t, scaleTestNotes)

		s := v.Schema()
		if s == nil {
			t.Fatal("vault.Schema() = nil, want a parsed SCHEMA.md")
		}
		if s.Domain == "" {
			t.Error("Schema.Domain is empty")
		}
		if len(s.Tags) != len(scaleTaxonomy) {
			t.Fatalf("Schema.Tags has %d tags, want %d (the taxonomy the generator draws from)", len(s.Tags), len(scaleTaxonomy))
		}
		for _, tag := range scaleTaxonomy {
			if !s.HasTag(tag) {
				t.Errorf("taxonomy tag %q is missing from the parsed SCHEMA.md", tag)
			}
		}

		// Every tag on every generated page is in the shipped taxonomy.
		for _, p := range v.Pages() {
			for _, tag := range p.FM.Tags {
				if !s.HasTag(tag) {
					t.Errorf("%s: tag %q is not in SCHEMA.md", p.Path, tag)
				}
			}
		}
	})
}

// scaleBuildVault generates a scale vault of n notes into a fresh temp dir
// and returns both the directory and the opened vault.
func scaleBuildVault(t *testing.T, n int) (string, *vault.Vault) {
	t.Helper()

	dir := t.TempDir()
	NewScaleVault(t, dir, n)

	v, err := vault.Open(dir)
	if err != nil {
		t.Fatalf("vault.Open(%s): %v", dir, err)
	}
	return dir, v
}

// scaleOpenVault is scaleBuildVault for callers that only need the vault.
func scaleOpenVault(t *testing.T, n int) *vault.Vault {
	t.Helper()

	_, v := scaleBuildVault(t, n)
	return v
}

// scaleTreeDigest returns the SHA-256 of the sorted set of per-file SHA-256
// sums under root — one stable fingerprint of the whole generated tree.
func scaleTreeDigest(t *testing.T, root string) string {
	t.Helper()

	var lines []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		lines = append(lines, fmt.Sprintf("%s %s", filepath.ToSlash(rel), hex.EncodeToString(sum[:])))
		return nil
	})
	if err != nil {
		t.Fatalf("testutil: walk %s: %v", root, err)
	}

	sort.Strings(lines)
	digest := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(digest[:])
}

// scaleCountMarkdown returns how many .md files sit anywhere under dir.
func scaleCountMarkdown(t *testing.T, dir string) int {
	t.Helper()

	count := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".md") {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("testutil: walk %s: %v", dir, err)
	}
	return count
}
