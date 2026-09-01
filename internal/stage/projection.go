// projection.go builds the in-memory whole-vault tree Append,
// OpenChangeset, DropHunk, DropOp and Refresh use to recompute Checks
// (backbone §5.4 "materializing the projected tree in memory", MASTER §9
// D-AX, D-AZ). It never writes a file anywhere, not even to a scratch
// directory (Gotcha 7).
package stage

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"testing/fstest"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/vault"
)

// walkWholeTree walks os.DirFS(root) and returns every *.md file's
// vault-relative path mapped to its raw bytes, skipping any directory
// (and everything under it) whose name begins with "." — which is what
// excludes .llmwiki/.
//
// Contract (backbone §5.4 "how the projection is seeded", MASTER §9
// D-AX). Pages() and RawSources() cover only wiki/ and raw/, but
// SCHEMA.md, index.md, log.md and curator-memory.md live at the vault
// root: omitting SCHEMA.md makes vault.Reload — and therefore every
// single Append — fail outright, and omitting index.md or log.md is
// worse, because check_index_sync.go and check_log_rotate.go both return
// nil when Vault.Read fails, so Checks.Lint would silently never fail on
// index or log drift again. A whole-tree walk cannot omit a file by
// oversight.
func walkWholeTree(root string) (map[string][]byte, error) {
	fsys := os.DirFS(root)
	tree := map[string][]byte{}

	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != "." && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if path.Ext(p) != ".md" {
			return nil
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		tree[p] = b
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("stage: walk vault tree: %w", err)
	}
	return tree, nil
}

// postImage returns op's stored post-image bytes: Store.Get(After), or
// Store.Get(SHA256) for ingest_source. op.Content is consulted only as a
// defensive fallback for a caller that has not yet gone through Append's
// content-storage step (Content is always cleared by the time a live op
// sits in a persisted Changeset).
func (e *Engine) postImage(op Op) ([]byte, error) {
	sha := op.After
	if op.Kind == OpIngestSource {
		sha = op.SHA256
	}
	if sha == "" {
		if op.Content != nil {
			return op.Content, nil
		}
		return nil, fmt.Errorf("stage: op %s has no post-image content", op.ID)
	}
	return e.store.Get(sha)
}

// applyOp overrides tree's paths with op's projected effect, per the
// backbone §5.4 per-kind projection table (MASTER §9 D-AZ), then recurses
// into op.Cascade so every cascade sub-op's own rewrite is applied too.
// No tombstone is synthesized here — that is Commit's job (a later wave)
// and its content deliberately does not round-trip through changeset.json.
func (e *Engine) applyOp(tree map[string][]byte, op Op) error {
	switch op.Kind {
	case OpCreatePage, OpPatchPage, OpIngestSource:
		b, err := e.postImage(op)
		if err != nil {
			return err
		}
		tree[op.Path] = b
	case OpRenamePage:
		delete(tree, op.From)
		if p, ok := e.vault.Page(op.From); ok {
			tree[op.To] = p.Serialize()
		}
	case OpRetract:
		delete(tree, op.Path)
	case OpMergePages:
		for _, src := range op.Sources {
			delete(tree, src)
		}
		// To's content comes from a sibling patch_page/create_page op in
		// the same changeset (D-AZ); nothing to project here directly.
	case OpSplitPage:
		delete(tree, op.Path)
		// Its products come from the sibling create_page ops D-AK mandates.
	case OpAddLink:
		// Content-free marker (D-AK); its edits are sibling patch_page ops.
	}
	for _, sub := range op.Cascade {
		if err := e.applyOp(tree, sub); err != nil {
			return err
		}
	}
	return nil
}

// projectedTree returns the whole-vault, in-memory projection: every *.md
// file on disk, overridden by the post-image of every op in ops (backbone
// §5.4, MASTER §9 D-AX point 2 — every live op, not just the newest one).
func (e *Engine) projectedTree(ops []Op) (map[string][]byte, error) {
	tree, err := walkWholeTree(e.root)
	if err != nil {
		return nil, err
	}
	for _, op := range ops {
		if err := e.applyOp(tree, op); err != nil {
			return nil, err
		}
	}
	return tree, nil
}

// computeChecks builds the fs.FS view of tree, opens it as a Vault (never
// touching disk), lints it, and returns the four Checks fields per
// backbone §5.3's Checks Contract.
func computeChecks(tree map[string][]byte) (Checks, error) {
	mfs := make(fstest.MapFS, len(tree))
	for p, b := range tree {
		mfs[p] = &fstest.MapFile{Data: b, Mode: 0o644}
	}

	pv, err := vault.OpenFS(mfs)
	if err != nil {
		return Checks{}, fmt.Errorf("stage: recompute checks: %w", err)
	}

	schemaResult := "pass"
	for _, p := range pv.Pages() {
		if err := p.FM.Validate(pv.Schema()); err != nil {
			schemaResult = "fail"
			break
		}
	}

	idx := index.Build(pv)
	report := lint.Run(&lint.Context{Vault: pv, Index: idx, Graph: pv.Graph()}, nil)
	lintResult := "pass"
	if !report.Clean() {
		lintResult = "fail"
	}

	return Checks{
		Schema:      schemaResult,
		Lint:        lintResult,
		Orphans:     len(pv.Graph().Orphans()),
		BrokenLinks: len(pv.Graph().Broken()),
	}, nil
}

// recomputeChecks is the single code path OpenChangeset, Append, DropHunk,
// DropOp and Refresh all share: project the whole tree overridden by
// c.Live(), then compute Checks over it (backbone §5.4 Contract).
func (e *Engine) recomputeChecks(c *Changeset) (Checks, error) {
	tree, err := e.projectedTree(c.Live())
	if err != nil {
		return Checks{}, err
	}
	return computeChecks(tree)
}
