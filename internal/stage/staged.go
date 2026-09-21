// staged.go implements Engine.StagedFile — the read-only seam
// internal/tools uses so a handler never touches the filesystem itself
// (backbone §6) while still being able to read bytes that exist only
// inside a proposed op, not yet committed to the working tree.
//
// Added S6-C121 (gate G6): stage.ingest_source stages a raw source's bytes
// in the CAS via Append, but nothing exported let a tool read them back
// before commit — raw.get read only Deps.Vault.RawSource, the committed
// vault, so an agent that just staged a source could never read it, and
// every "create pages from what you just ingested" ingest ended with zero
// pages proposed.
package stage

import "errors"

// StagedFile returns the projected content of path from the currently open
// changeset's live ops.
//
// Contract: no changeset is open (ErrNoChangeset) or no live
// OpIngestSource, OpCreatePage or OpPatchPage op in the changeset targets
// path -> (nil, false, nil) — not found is not an error, so a tool handler
// can turn it straight into a Result{IsError:true} without inventing its
// own error path. The scan walks the ops in Changeset.Live() order and,
// within each op, the op itself then its Cascade entries depth-first — the
// same order projection.go's applyOp applies them — skipping Dropped and
// Rejected entries (only ever cascade sub-ops here, since Live() filters
// the top level) exactly as planOp and fileDiffsForOp skip them, so the
// bytes a tool reads are always bytes Commit would actually write — and
// the LAST
// content-op targeting path wins, so a path a rename/merge cascade rewrote
// (020 FIX-1) resolves to the rewrite, not to the committed page the
// projection has already moved past. When more than one live op targets
// path (e.g. a create_page later patched in the same changeset), that last
// op is likewise the one whose After Commit will actually write. Cascade
// sub-ops' After shas sit in the CAS (storeOpContent recurses), so the
// store.Get below works unchanged for them. A real CAS or engine read
// failure is returned as an error.
//
// StagedFile takes the same lock/read discipline as Current and Diff: it
// never acquires the vault lock (§5.2 names those two explicitly; Append,
// DropOp, DropHunk, UndropHunk and Refresh join them for the same reason —
// only Commit takes the lock), and it touches no file beyond the
// changeset.json Current() already reads to rehydrate e.open.
func (e *Engine) StagedFile(path string) ([]byte, bool, error) {
	c, err := e.currentOpen()
	if err != nil {
		if errors.Is(err, ErrNoChangeset) {
			return nil, false, nil
		}
		return nil, false, err
	}

	var match *Op
	var scan func(ops []Op)
	scan = func(ops []Op) {
		for i := range ops {
			op := &ops[i]
			// A Dropped or Rejected entry — reachable here only as a
			// cascade sub-op, since Live() filters the top level — is
			// skipped with its whole subtree, exactly as planOp (apply.go)
			// and fileDiffsForOp (diff.go) skip it: serving a dropped
			// rewrite would let a tool compute its Before from bytes the
			// commit will discard (020 fix wave 3a, G3 review finding 1).
			if op.State == StateDropped || op.State == StateRejected {
				continue
			}
			switch op.Kind {
			case OpIngestSource, OpCreatePage, OpPatchPage:
				if op.Path == path {
					match = op
				}
			}
			scan(op.Cascade)
		}
	}
	scan(c.Live())
	if match == nil {
		return nil, false, nil
	}

	sha := match.After
	if sha == "" {
		return nil, false, nil
	}

	b, err := e.store.Get(sha)
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}
