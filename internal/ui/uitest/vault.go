// vault.go implements contract §6's Vault, PublicVault and CopyVault:
// fixture vaults opened through the public stage.Engine API, with the
// changeset the screens' tests and the conformance gate stage on top.
//
// Everything here runs through Engine's public surface — OpenEngine,
// OpenChangeset, Append, DropHunk — the same calls cmd/lw and the agent
// tools make, so a screen test exercises exactly the persisted state a
// human reviewer would see.
package uitest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

// Vault is a staged fixture vault opened with a fixed clock and pinned
// changeset-id entropy (contract §6 note 2, as amended by C24): every
// engine PublicVault and CopyVault opens is built with
// stage.WithClock(testutil.FixedClock()) and stage.WithEntropy over a
// deterministic reader, so two runs stage byte-identical changesets —
// same id, same OpenedAt, same journal timestamps — and the screens'
// goldens can show the id and the Log rows without masking.
type Vault struct {
	Root   string // absolute; its base name is the header's vault name
	Engine *stage.Engine
}

// fixtureVaultDir is the public fixture, relative to this package's
// directory (the working directory go test sets for a package's tests).
const fixtureVaultDir = "testdata/vault"

// PublicVault copies internal/ui/uitest/testdata/vault into t.TempDir()/<name>
// and stages its fixture changeset (§6 note 2). name is the directory base
// name (the header shows it). Fails the test on error.
func PublicVault(t *testing.T, name string) *Vault {
	t.Helper()
	root := copyTree(t, fixtureVaultDir, filepath.Join(t.TempDir(), name))
	e := openEngine(t, root)
	stageFixtureChangeset(t, e)
	return &Vault{Root: root, Engine: e}
}

// CopyVault copies an existing vault directory (e.g. $LW_MOCKUP_VAULT) into
// t.TempDir()/<base name> and opens it.
func CopyVault(t *testing.T, src string) *Vault {
	t.Helper()
	root := copyTree(t, src, filepath.Join(t.TempDir(), filepath.Base(src)))
	return &Vault{Root: root, Engine: openEngine(t, root)}
}

// openEngine opens root with the harness's pinned clock and entropy
// (contract §6 note 2, C24) and registers Close with t, so a test's temp
// vault never keeps the engine's journal or lock past the test. Every
// engine the harness opens goes through here, so PublicVault and
// CopyVault are deterministic identically.
func openEngine(t *testing.T, root string) *stage.Engine {
	t.Helper()
	e, err := stage.OpenEngine(root,
		stage.WithClock(testutil.FixedClock()),
		stage.WithEntropy(fixtureEntropy{}))
	if err != nil {
		t.Fatalf("uitest: open engine at %s: %v", root, err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

// fixtureEntropy is the deterministic entropy source every harness engine
// injects: an io.Reader that fills each read with the same fixed pattern
// and never returns EOF. OpenChangeset draws 8 bytes per id candidate and
// may retry (its maxIDAttempts loop), so the source must be inexhaustible
// — a finite buffer would fail the second changeset, and a varying one
// would make the changeset id differ run to run. With the fixed clock the
// same 8 bytes yield the same candidate id every draw, which is what
// makes two PublicVault calls produce the same changeset.
type fixtureEntropy struct{}

func (fixtureEntropy) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(0x5A ^ i)
	}
	return len(p), nil
}

// The fixture changeset's identity. The ingested date and the ops' paths
// are literals, and openEngine pins the clock and the id entropy, so the
// staged changeset — id included — is byte-identical run to run.
const (
	fixtureIntent       = "uitest: add baker percentages to the bread wiki"
	fixtureSourcePath   = "raw/articles/baker-percentages-explained.md"
	fixtureSourceURL    = "https://example.org/articles/baker-percentages-explained"
	fixtureIngestedDate = "2026-08-29"
)

// op3Rationale is deliberately long: it wraps to more than three lines at 76
// columns, so the Review screen's rationale pane exercises its wrap path.
const op3Rationale = `The rest line undersold the point of the page: once the flour is fully
hydrated, the water percentage decides how far the gluten can stretch before
it tears. Point that line at the baker-percentages page so a reader who wants
the arithmetic behind the rest can follow it.`

// stageFixtureChangeset stages contract §6 note 2's four-op changeset on e's
// already-open engine: ingest_source, create_page (with provenance),
// patch_page (1 hunk, long rationale), patch_page (1 hunk), then DropHunk on
// op4's only hunk. The result is the same op states as the mockup vault:
// three live proposed ops and one whose single hunk is dropped.
func stageFixtureChangeset(t *testing.T, e *stage.Engine) {
	t.Helper()

	if _, err := e.OpenChangeset(fixtureIntent, stage.Author{Kind: "agent", Model: "uitest"}); err != nil {
		t.Fatalf("uitest: OpenChangeset: %v", err)
	}

	// op1 — ingest_source: a second raw article, the source the new page
	// cites. Extractor "passthrough" is extract.NewFile's kind; the
	// frontmatter sha256 is the body's own hash, what src-integrity checks.
	op1 := stage.Op{
		Kind:      stage.OpIngestSource,
		Path:      fixtureSourcePath,
		Extractor: "passthrough",
		Rationale: "raw source for the new baker-percentages page",
		Content:   rawSourceBytes(fixtureSourceURL, fixtureIngestedDate, fixtureSourceBody),
	}
	if _, err := e.Append(op1); err != nil {
		t.Fatalf("uitest: Append ingest_source: %v", err)
	}

	// op2 — create_page with provenance pointing at op1's raw source. The
	// page links two existing pages, so the projected tree stays
	// orphan-free once op3 adds its inbound link.
	op2 := stage.Op{
		Kind:       stage.OpCreatePage,
		Path:       "wiki/concepts/baker-percentages.md",
		Rationale:  "the raw article's ratios section deserves its own page",
		Provenance: []string{fixtureSourcePath},
		Content:    []byte(bakerPercentagesPage),
	}
	if _, err := e.Append(op2); err != nil {
		t.Fatalf("uitest: Append create_page: %v", err)
	}

	// op3 — patch_page: one del-anchored hunk on autolyse's "Why it
	// matters" section, carrying the inbound [[baker-percentages]] link the
	// created page needs, with a rationale that wraps past three lines.
	const (
		op3Page = "wiki/concepts/autolyse.md"
		op3Old  = "- Lets enzymes begin breaking down starches while the dough simply rests."
		op3New  = "- Lets enzymes begin breaking down starches while the dough rests, and the [[baker-percentages]] behind the flour-water ratio starts to matter."
	)
	op3 := patchOp(t, e, op3Page, op3Old, op3New, op3Rationale)
	if _, err := e.Append(op3); err != nil {
		t.Fatalf("uitest: Append patch_page (op3): %v", err)
	}

	// op4 — patch_page: one hunk the reviewer then drops, so the changeset
	// carries a dropped hunk exactly as the mockup vault does.
	const (
		op4Page = "wiki/entities/banneton.md"
		op4Old  = "- Wicks a little moisture from the dough's surface, so it releases cleanly."
		op4New  = "- Wicks moisture from the dough's surface so the loaf releases cleanly, even after a cold overnight proof."
	)
	op4 := patchOp(t, e, op4Page, op4Old, op4New, "tighten the release line while the page is open")
	op4ID, err := e.Append(op4)
	if err != nil {
		t.Fatalf("uitest: Append patch_page (op4): %v", err)
	}
	if err := e.DropHunk(op4ID, "h1"); err != nil {
		t.Fatalf("uitest: DropHunk(op4, h1): %v", err)
	}
}

// patchOp builds the patch_page op that replaces oldLine with newLine in
// path: Before hashes the page's current content, Content is the canonical
// serialization with the line replaced, and the single hunk pairs the old
// line with the new one — the shape applyHunks anchors by.
func patchOp(t *testing.T, e *stage.Engine, path, oldLine, newLine, rationale string) stage.Op {
	t.Helper()
	page, ok := e.Vault().Page(path)
	if !ok {
		t.Fatalf("uitest: fixture page %s is missing", path)
	}
	if !strings.Contains(page.Body, oldLine) {
		t.Fatalf("uitest: %s body does not contain the hunk's old line %q", path, oldLine)
	}
	rewritten := *page
	rewritten.Body = strings.Replace(page.Body, oldLine, newLine, 1)
	return stage.Op{
		Kind:      stage.OpPatchPage,
		Path:      page.Path,
		Section:   "## Why it matters",
		Before:    page.SHA256(),
		Content:   rewritten.Serialize(),
		Rationale: rationale,
		Hunks: []stage.Hunk{
			{ID: "h1", Path: page.Path, Del: []string{oldLine}, Add: []string{newLine}},
		},
	}
}

// rawSourceBytes renders a raw source the way RawSource.Serialize does:
// three frontmatter keys in order, the closing delimiter, one blank line,
// then the body — with sha256 set to the body's own hash so the staged
// source passes src-integrity in the projected tree.
func rawSourceBytes(sourceURL, ingested, body string) []byte {
	sum := sha256.Sum256([]byte(body))
	return []byte(fmt.Sprintf("---\nsource_url: %s\ningested: %s\nsha256: %s\n---\n\n%s",
		sourceURL, ingested, hex.EncodeToString(sum[:]), body))
}

// fixtureSourceBody is op1's raw article. It ends with exactly one newline,
// which is the hash rawSourceBytes records in the frontmatter.
const fixtureSourceBody = `# Baker Percentages, Explained

Professional bakers rarely write a recipe as a list of ingredient weights.
They write it as a set of percentages of the flour weight, a convention
called baker percentages. Flour is always one hundred percent, and every
other ingredient is measured against it, so a dough at seventy percent
hydration carries water equal to seven tenths of its flour.

## Why bakers use it

- A formula written in percentages scales cleanly from one loaf to twenty.
- Hydration stands out immediately, and hydration drives most of how a dough
  behaves in the bowl and in the oven.
- Two recipes become directly comparable, whatever loaf sizes they were
  written for.
`

// bakerPercentagesPage is op2's created page, in the canonical
// serialization shape (frontmatter keys in struct order, flow-style lists,
// one trailing newline) so the engine's reconstruction of the projection
// from hunks stays byte-exact.
const bakerPercentagesPage = `---
title: Baker Percentages
created: 2026-08-29
updated: 2026-08-29
type: concept
tags: [method, hydration]
sources: [raw/articles/baker-percentages-explained.md]
confidence: high
---

# Baker Percentages

Baker percentages describe a dough as a set of percentages of the flour
weight, with the flour itself at one hundred percent. Written this way, a
formula states its hydration directly and scales to any amount of flour
without surprises.^[raw/articles/baker-percentages-explained.md]

## Why it matters

- Makes two recipes comparable at a glance, whatever loaf size each was
  written for.
- Puts hydration, the number that shapes a dough's behaviour most, in the
  open instead of inside gram counts.

## Related

- [[autolyse]] — the rest where hydration does its quietest work.
- [[windowpane-test]] — how a wetter dough tells you it is ready.
`

// copyTree copies src to dst, preserving relative paths and refusing to
// follow symlinks, so a copied vault is a private, faithful stand-in for its
// original.
func copyTree(t *testing.T, src, dst string) string {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("uitest: refusing to follow symlink %s", path)
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target, d)
	})
	if err != nil {
		t.Fatalf("uitest: copy vault %s: %v", src, err)
	}
	return dst
}

// copyFile copies one regular file, creating its parent directory first.
func copyFile(src, dst string, d fs.DirEntry) error {
	info, err := d.Info()
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
