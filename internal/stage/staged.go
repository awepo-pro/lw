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
// own error path. When more than one live op targets path (e.g. a
// create_page later patched in the same changeset), the LAST such op in
// Changeset.Live() order wins — the same op whose After Commit will
// actually write. A real CAS or engine read failure is returned as an
// error.
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

	live := c.Live()
	var match *Op
	for i := range live {
		op := &live[i]
		if op.Path != path {
			continue
		}
		switch op.Kind {
		case OpIngestSource, OpCreatePage, OpPatchPage:
			match = op
		}
	}
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
