// projection.go builds the in-memory whole-vault tree Append,
// OpenChangeset, DropHunk, DropOp and Refresh use to recompute Checks
// (backbone §5.4 "materializing the projected tree in memory", MASTER §9
// D-AX, D-AZ). It never writes a file anywhere, not even to a scratch
// directory (Gotcha 7).
package stage

import (
	"errors"
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

// notesInboxDir is the vault-root directory `lw note` writes to (047 S1).
// cmd/lw owns the verb and stage cannot import it, so the name is spelled
// here too; TestNotesInvisibleToHistory pins that the two agree.
const notesInboxDir = "notes"

// walkWholeTree walks os.DirFS(root) and returns every *.md file's
// vault-relative path mapped to its raw bytes, skipping any directory
// (and everything under it) whose name begins with "." — which is what
// excludes .llmwiki/ — and the vault-root notes/ directory.
//
// notes/ is `lw note`'s inbox (047 S1): raw thoughts that sit outside the
// wiki and outside review, so no tree built here may contain them. The walk
// seeds both the projection and every commit's snapshot manifest, and a
// note in a manifest is a file lw would own history for — a note taken
// between two commits would be an ADDED path in the next one's delta, and
// `lw revert` would list it as "skipped: notes/…" in a changeset about wiki
// pages. The skip is this one explicit directory name at the root, never a
// wider rule: a directory called notes deeper in the tree is ordinary vault
// content.
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
			if p == notesInboxDir {
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

// walkAttachments is gone (033 scaling fix): it used to read EVERY
// committed raw/ attachment's bytes into the projection seed, so a vault
// with ~100 multi-MB PDFs re-read hundreds of megabytes on every Append,
// Refresh and Checks computation. The projection vault now answers
// attachment questions through the WithAttachments delegate
// (resolveAttachments, below) instead: a live op's staged original
// resolves to the sha Append already recorded on the op — no CAS read —
// and a committed one to the disk vault's persistent cached
// AttachmentSHA256. Exists stays true for both, src-integrity keeps
// comparing real shas, and not one attachment byte is read for a file no
// live op touched. Snapshots stay .md-only as before (buildSnapshot keeps
// walkWholeTree): the revert world sees neither attachment bytes nor
// attachment shas.

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
//
// retract and split_page project the SAME Commit-time-only page shape
// Commit itself will write (retractTombstone / splitStub, both apply.go —
// a call, not a second copy of the bytes), not a deletion (S2-T8 C-65,
// D-CB step 3). Before this fix the projection deleted both paths outright
// while apply.go's planOp wrote a page in place, so the review surface
// reported Lint:fail / BrokenLinks>0 for a retract or split whose actual
// commit was clean — measured three ways: a retract, a split, and a
// revert-of-create. A path that no longer parses as a live page (should
// not happen — ValidateOp requires it to exist at Append time) falls back
// to the old delete-outright behavior rather than guessing at content.
//
// Dropped and Rejected ops are skipped together with their own Cascade —
// for cascade sub-ops the live-list filters above this call remove
// nothing, so this guard is the only one — exactly as planOp (apply.go)
// and fileDiffsForOp (diff.go) skip them: a dropped cascade rewrite the
// projection still applied was landing baked into a dependent patch with
// no hunk describing it, while the pre-commit lint gate described a tree
// Commit would not write (020 fix wave 3a, G3 review finding 1).
func (e *Engine) applyOp(tree map[string][]byte, atts map[string]string, op Op) error {
	if op.State == StateDropped || op.State == StateRejected {
		return nil
	}
	switch op.Kind {
	case OpIngestSource:
		b, err := e.postImage(op)
		if err != nil {
			return err
		}
		tree[op.Path] = b
		// 033: the projected tree does NOT carry the original's bytes — a
		// live op's original resolves through atts to the sha Append
		// recorded on the op (the CAS key the bytes hash to), which is
		// exactly what src-integrity compares original_sha256 against.
		// Without an answer the projected vault a Checks/lint run reads
		// would report src-integrity "original missing" for the very
		// changeset the reviewer is being asked to approve — and D-AG's
		// regression check would refuse the commit.
		if op.OriginalPath != "" && op.Original != "" {
			atts[op.OriginalPath] = op.Original
		}
	case OpCreatePage, OpPatchPage:
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
		if page, ok := e.vault.Page(op.Path); ok {
			tree[op.Path] = retractTombstone(page, op.Rationale, e.now().UTC().Format("2006-01-02"))
		} else {
			delete(tree, op.Path)
		}
	case OpMergePages:
		for _, src := range op.Sources {
			delete(tree, src)
		}
		// To's content comes from a sibling patch_page/create_page op in
		// the same changeset (D-AZ); nothing to project here directly.
	case OpSplitPage:
		if page, ok := e.vault.Page(op.Path); ok {
			tree[op.Path] = splitStub(page, op.Sources, e.now().UTC().Format("2006-01-02"))
		} else {
			delete(tree, op.Path)
		}
		// Its products come from the sibling create_page ops D-AK mandates.
	case OpAddLink:
		// Content-free marker (D-AK); its edits are sibling patch_page ops.
	}
	for _, sub := range op.Cascade {
		if err := e.applyOp(tree, atts, sub); err != nil {
			return err
		}
	}
	return nil
}

// projectedTree returns the tree half of project — the shape tests and
// the diff surfaces already consume. New callers want project itself.
func (e *Engine) projectedTree(ops []Op) (map[string][]byte, error) {
	tree, _, err := e.project(ops)
	return tree, err
}

// project returns the whole-vault, in-memory projection as two maps: tree,
// every *.md file on disk overridden by the post-image of every op in ops
// (backbone §5.4, MASTER §9 D-AX point 2 — every live op, not just the
// newest one), and atts, the raw/ attachments those live ops stage as
// path → sha256-hex pairs. Committed attachments appear in NEITHER map:
// since the 033 scaling fix the projection carries no attachment bytes,
// and the vault opened from this projection resolves committed originals
// through the disk vault's persistent cache instead (resolveAttachments).
//
// After the override loop, the identical S2-T8 rule (a) derivation pass
// buildCommitMaterialization runs at Commit time (apply.go) runs here too,
// so the changeset's projected Checks and what Commit actually writes
// never disagree (D-CB). walkWholeTree already seeded tree["index.md"]
// from disk, and any cascade rewrite the loop above performed already
// landed there — applyOp writes into tree in place — so the running seed
// this derivation starts from is simply whatever tree currently holds. A
// tree with no "index.md" entry at all derives nothing.
func (e *Engine) project(ops []Op) (tree map[string][]byte, atts map[string]string, err error) {
	tree, err = walkWholeTree(e.root)
	if err != nil {
		return nil, nil, err
	}
	atts = map[string]string{}
	for _, op := range ops {
		if err := e.applyOp(tree, atts, op); err != nil {
			return nil, nil, err
		}
	}

	if creates := liveCreatePages(ops); len(creates) > 0 {
		if running, ok := tree["index.md"]; ok {
			updated, err := deriveIndex(running, creates, e.postImage)
			if err != nil {
				return nil, nil, err
			}
			tree["index.md"] = updated
		}
	}

	return tree, atts, nil
}

// resolveAttachments builds the WithAttachments delegate a projection
// vault is opened with: staged (live-op) originals answer from atts' op-
// recorded shas with no read at all, and everything else — the committed
// attachments — from the engine's disk vault, whose AttachmentSHA256 is
// backed by the persistent stat cache under .llmwiki/cache/. Paths that
// are not raw/ non-.md files answer fs.ErrNotExist without touching the
// cache: .md content lives in the tree, and nothing else is an attachment.
func (e *Engine) resolveAttachments(atts map[string]string) func(string) (string, bool, error) {
	return func(p string) (string, bool, error) {
		if sha, ok := atts[p]; ok {
			return sha, true, nil
		}
		if !strings.HasPrefix(p, "raw/") || path.Ext(p) == ".md" || e.vault == nil {
			return "", false, fs.ErrNotExist
		}
		sha, err := e.vault.AttachmentSHA256(p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return "", false, fs.ErrNotExist
			}
			return "", false, err
		}
		return sha, true, nil
	}
}

// openProjection builds the fs.FS view of tree and opens it as a Vault,
// never touching disk, with atts wired in as the attachment delegate. The
// single seam computeChecks, cascadeBase and ProjectedReport share, so the
// tree a Checks verdict describes and the tree a lint.Report counts can
// never drift apart (they are the same call).
func (e *Engine) openProjection(tree map[string][]byte, atts map[string]string) (*vault.Vault, error) {
	mfs := make(fstest.MapFS, len(tree))
	for p, b := range tree {
		mfs[p] = &fstest.MapFile{Data: b, Mode: 0o644}
	}
	return vault.OpenFS(mfs, vault.WithAttachments(e.resolveAttachments(atts)))
}

// lintProjection runs the 18 checks (page-abstract added by 014,
// duplicate-section by 020, cite-page + cite-source by 034) over an
// opened projection.
func lintProjection(pv *vault.Vault) lint.Report {
	return lint.Run(&lint.Context{Vault: pv, Index: index.Build(pv), Graph: pv.Graph()}, nil)
}

// computeChecks builds the fs.FS view of tree, opens it as a Vault with
// atts as the attachment delegate (never touching disk), lints it, and
// returns the four Checks fields per backbone §5.3's Checks Contract.
func (e *Engine) computeChecks(tree map[string][]byte, atts map[string]string) (Checks, error) {
	pv, err := e.openProjection(tree, atts)
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

	report := lintProjection(pv)
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

// ProjectedReport returns the whole lint.Report of the open changeset's
// projected tree — what `lw lint` would print if the changeset were
// committed right now. ErrNoChangeset when none is open.
//
// Contract (backbone §5.4, MASTER §9 D-CC): this exists because
// Changeset.Checks carries only Lint "pass"/"fail", while D-AG's
// regression check is Report.Regresses(prev), which compares Errors
// counts — so `lw commit` could not perform the check the CLI contract
// names. Measured at wave-8 entry across create_page (clean and
// dangling-link), rename_page, retract and split_page: this report equals
// the post-commit report exactly, which is what makes it a sound
// pre-commit predictor. That equality is only true after S2-T8 (C-65)
// aligned the projection with what Commit actually writes.
//
// It takes no lock and writes nothing — like Diff, it only reads e.vault,
// the working tree and the changeset already on disk.
func (e *Engine) ProjectedReport() (lint.Report, error) {
	c, err := e.currentOpen()
	if err != nil {
		return lint.Report{}, err
	}
	tree, atts, err := e.project(c.Live())
	if err != nil {
		return lint.Report{}, err
	}
	pv, err := e.openProjection(tree, atts)
	if err != nil {
		return lint.Report{}, fmt.Errorf("stage: projected report: %w", err)
	}
	return lintProjection(pv), nil
}

// recomputeChecks is the single code path OpenChangeset, Append, DropHunk,
// DropOp and Refresh all share: project the whole tree overridden by
// c.Live(), then compute Checks over it (backbone §5.4 Contract).
func (e *Engine) recomputeChecks(c *Changeset) (Checks, error) {
	tree, atts, err := e.project(c.Live())
	if err != nil {
		return Checks{}, err
	}
	return e.computeChecks(tree, atts)
}
