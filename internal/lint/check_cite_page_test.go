package lint_test

import (
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/vault"
)

// pagedRawSource renders a raw/ source whose body carries a `<!-- page N -->`
// anchor per given page, with a frontmatter sha256 matching that body — the
// shape 034's PDF ingest writes, so cite-page's anchor arithmetic is
// exercised against real ingest output, not a hand-faked body.
func pagedRawSource(pages ...int) string {
	var b strings.Builder
	b.WriteString("# Paged Fixture\n\n")
	for _, n := range pages {
		fmt.Fprintf(&b, "<!-- page %d -->\n\nPage %d text.\n\n", n, n)
	}
	return "---\nsource_url: https://example.org/paged\ningested: 2026-01-01\nsha256: " +
		vault.BodySHA256(b.String()) + "\n---\n\n" + b.String()
}

// citePageBody builds a wiki page whose body is body, with sources: fed
// from sources (omitted entirely when empty) — so each TestCitePage/
// TestCiteSource case spells only the part it is about.
func citePageBody(sources []string, body string) string {
	fm := "---\ntitle: Cite Page\ncreated: 2026-01-01\nupdated: 2026-01-02\ntype: concept\n"
	if len(sources) > 0 {
		fm += "sources: [" + strings.Join(sources, ", ") + "]\n"
	}
	return fm + "---\n\n" + body
}

// TestCitePage pins check 17's four messages (034 T2) — one case per
// message, plus the two shapes that must stay silent: a clean paged cite,
// and a legacy unpaged cite of a missing source. Messages are asserted
// verbatim: each one names the marker exactly as written and states the
// fix, the same contract every other check's message carries.
func TestCitePage(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string // the vault beyond the citing page
		sources []string          // the citing page's sources: list
		body    string            // the citing page's body
		want    []string          // expected messages, in Run's Path/Line/Check order
	}{
		{
			name: "malformed marker reports the parse error",
			files: map[string]string{
				"raw/papers/paged.md": pagedRawSource(1, 2, 3),
			},
			sources: []string{"raw/papers/paged.md"},
			body:    "# Cite Page\n\nA claim.^[raw/papers/paged.md p.two]\n",
			want: []string{
				`^[raw/papers/paged.md p.two]: malformed page citation "^[raw/papers/paged.md p.two]": write ^[<source> p.N] or ^[<source> p.N-M]`,
			},
		},
		{
			name:    "paged cite of a source that is not in the vault",
			files:   map[string]string{},
			sources: nil,
			body:    "# Cite Page\n\nA claim.^[raw/papers/ghost.md p.3]\n",
			want: []string{
				`^[raw/papers/ghost.md p.3]: raw/papers/ghost.md does not exist`,
			},
		},
		{
			name: "source without page anchors",
			files: map[string]string{
				"raw/papers/plain.md": rawSourceFixture,
			},
			sources: []string{"raw/papers/plain.md"},
			body:    "# Cite Page\n\nA claim.^[raw/papers/plain.md p.2]\n",
			want: []string{
				`^[raw/papers/plain.md p.2]: raw/papers/plain.md has no page anchors; cite it without a page`,
			},
		},
		{
			name: "page past the last anchor, single and ranged",
			files: map[string]string{
				"raw/papers/paged.md": pagedRawSource(1, 2, 3),
			},
			sources: []string{"raw/papers/paged.md"},
			body: "# Cite Page\n\nA claim.^[raw/papers/paged.md p.4]\n\n" +
				"Another.^[raw/papers/paged.md p.2-5]\n",
			want: []string{
				`^[raw/papers/paged.md p.4]: raw/papers/paged.md has pages 1-3`,
				`^[raw/papers/paged.md p.2-5]: raw/papers/paged.md has pages 1-3`,
			},
		},
		{
			name: "clean paged cites stay silent",
			files: map[string]string{
				"raw/papers/paged.md": pagedRawSource(1, 2, 3),
			},
			sources: []string{"raw/papers/paged.md"},
			body: "# Cite Page\n\nA claim.^[raw/papers/paged.md p.2]\n\n" +
				"Another.^[raw/papers/paged.md p.1-3]\n",
			want: nil,
		},
		{
			name:    "legacy unpaged cite of a missing source stays silent",
			files:   map[string]string{},
			sources: nil,
			body:    "# Cite Page\n\nA claim.^[raw/papers/ghost.md]\n",
			want:    nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{
				"wiki/concepts/cited.md": citePageBody(tc.sources, tc.body),
			}
			for rel, content := range tc.files {
				files[rel] = content
			}
			ctx := buildVault(t, files)

			report := lint.Run(ctx, []string{"cite-page"})
			if len(report.Findings) != len(tc.want) {
				t.Fatalf("got %d findings, want %d: %+v", len(report.Findings), len(tc.want), report.Findings)
			}
			for i, want := range tc.want {
				f := report.Findings[i]
				if f.Check != "cite-page" {
					t.Errorf("finding %d Check = %q, want cite-page", i, f.Check)
				}
				if f.Path != "wiki/concepts/cited.md" {
					t.Errorf("finding %d Path = %q, want wiki/concepts/cited.md", i, f.Path)
				}
				if f.Severity != lint.SevWarn {
					t.Errorf("finding %d Severity = %v, want SevWarn", i, f.Severity)
				}
				if f.Message != want {
					t.Errorf("finding %d Message = %q, want %q", i, f.Message, want)
				}
			}
		})
	}
}

// TestCitePageLineIsMarkerBodyLine pins the Line convention (034 T2): the
// finding points at the marker's 1-based line within Page.Body, the same
// body-relative convention duplicate-section and Wikilink.Line use — here
// the third line, under a one-line heading and one blank line.
func TestCitePageLineIsMarkerBodyLine(t *testing.T) {
	ctx := buildVault(t, map[string]string{
		"wiki/concepts/cited.md": citePageBody(nil,
			"# Cite Page\n\nA claim.^[raw/papers/ghost.md p.3]\n"),
	})

	report := lint.Run(ctx, []string{"cite-page"})
	if len(report.Findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(report.Findings), report.Findings)
	}
	if got := report.Findings[0].Line; got != 3 {
		t.Fatalf("Line = %d, want 3", got)
	}
}

// TestCitePageReadsRawSourceThroughVault proves cite-page resolves the raw
// source through ctx.Vault itself, never the working tree (034 T2 review).
// Stage's projection lint (internal/stage projection.go: openProjection)
// hands lint a vault.OpenFS vault whose tree carries a raw/ source staged
// in the open changeset — the file may not exist on disk at all — so a page
// citing ^[raw/... p.N] in that same changeset must not draw "does not
// exist" from a disk miss. This builds exactly that shape: an in-memory FS,
// no raw/papers/paged.md anywhere on disk.
func TestCitePageReadsRawSourceThroughVault(t *testing.T) {
	mfs := fstest.MapFS{
		"SCHEMA.md":           &fstest.MapFile{Data: []byte(minimalSchema)},
		"raw/papers/paged.md": &fstest.MapFile{Data: []byte(pagedRawSource(1, 2, 3))},
		"wiki/concepts/cited.md": &fstest.MapFile{Data: []byte(citePageBody(
			[]string{"raw/papers/paged.md"},
			"# Cite Page\n\nA claim.^[raw/papers/paged.md p.2]\n"))},
	}
	v, err := vault.OpenFS(mfs)
	if err != nil {
		t.Fatalf("vault.OpenFS: %v", err)
	}

	report := lint.Run(&lint.Context{Vault: v, Index: index.Build(v), Graph: v.Graph()},
		[]string{"cite-page"})
	if len(report.Findings) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(report.Findings), report.Findings)
	}
}
