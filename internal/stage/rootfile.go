// rootfile.go implements OQ-9's phased allow-list for patch_page ops that
// target a vault-root bookkeeping file — index.md, curator-memory.md,
// log.md, SCHEMA.md — none of which is a vault.Page (§2.8: the vault
// reads them, but loadPages/BuildGraph walk wiki/ and raw/ only).
//
// Read .dev-notes/issues/OQ-9-patch-vault-root-files.md §7 and §9 for the
// specification this file exists to satisfy; this comment only orients
// the code, it does not restate the decision.
//
// Phase 1 (S4-T0) populated patchableRootFiles with exactly one entry,
// curator-memory.md — the one root file with no automatic writer (OQ-9
// §7's measurement table: derivation never touches it, and a cascade
// touches it only when it happens to wikilink a renamed page). Phase 2
// (this subtask, S4-T7, after the review screen shipped) adds index.md by
// appending one more entry to that slice and changing nothing else about
// L1/L2/L4 — that was the entire point of building them generally, in
// phase 1, against the file where a bug was cheap.
//
// Phase 2 also splits one predicate that was quietly answering two
// different questions (D-CW, s4-tui.md C-110): isPatchableRootFile asks
// "may an op PROPOSE a patch_page against this path" (OQ-9's allow-list —
// index.md: yes, now); isRevertableRootFile asks "can Revert RECONSTRUCT
// this path's previous content from a stored CAS blob" (index.md: no —
// derive.go computes its bytes on every commit but never stores them
// keyed by the sha a snapshot records, so there is nothing in the CAS for
// a revert to read back). See isRevertableRootFile's own doc comment.
package stage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/awepo-pro/lw/internal/vault"
)

// patchableRootFiles is OQ-9's L1 allow-list: the vault-root files a
// top-level patch_page may target. Same "var slice as constant set"
// pattern op.go's cascadeRoots already uses — Go has no const slice, and
// keeping the two spellings consistent matters more than the keyword.
//
// Phase 2 (S4-T7) adds "index.md" here and changes nothing else in this
// file: isPatchableRootFile, checkRootFileOneWriter and
// validateNonPagePatch are all written against this slice, not against a
// literal "curator-memory.md" string.
var patchableRootFiles = []string{
	"curator-memory.md",
	"index.md", // S4-T7, OQ-9 phase 2 — added after S4-T3's review screen shipped.
}

// forbiddenRootFiles are vault-root files a patch_page may never target,
// with the reason OQ-9 §9's phase 1 table requires — never a bare "does
// not exist" message, because that is not why they are refused.
var forbiddenRootFiles = map[string]string{
	"log.md":    "log.md is append-only and rotates (Commit appends to it at step 8, then rotates at 500 lines); a patch_page against it would be invalidated by the very next commit",
	"SCHEMA.md": "SCHEMA.md is the rules the validator itself reads (frontmatter schema, tag taxonomy); patch_page cannot be used to rewrite the rules it is being checked against",
}

// revertableRootFiles is patchableRootFiles narrowed to the subset
// buildRevertOps (revert.go) can actually reconstruct from a stored CAS
// blob — a different question from "may a patch_page target this path"
// (D-CW, s4-tui.md C-110).
//
// A root file's bytes only ever reach the CAS by being someone's
// post-image write in storeCommitMaterialization (apply.go): a direct
// patch_page's Content (op.go's storeOpContent, and Commit's own
// per-op postImage write), OR a cascade sub-op's post-image when a
// rename/merge rewrites it. index.md has a THIRD writer neither of those
// covers — create_page's index derivation (deriveIndex,
// buildCommitMaterialization) computes its post-commit content straight
// into the commit's materialized tree. That path DOES store the new
// post-image (m.writes["index.md"] is stored like any other write), but
// the vault's very first index.md — whatever shipped in the vault before
// any commit ever ran a derivation over it — was never itself the
// product of a stored write, so a revert reaching back past the first
// commit that changed it (or the genesis snapshot itself) can hit a sha
// with no corresponding blob. Distinguishing "this specific historical
// sha happens to have been stored" from "this path's bytes are
// mechanically guaranteed to be stored" is not worth the complexity for
// one root file with two writers, so index.md is excluded from this set
// entirely and left to buildRevertOps' honest "skipped, not dropped" path
// — exactly its pre-S4-T7 behaviour. curator-memory.md has no automatic
// writer at all (OQ-9 §7's measurement table), so every sha it has ever
// carried came from an explicit patch_page whose pre-image
// storeOpContent stored — always reconstructable.
var revertableRootFiles = []string{
	"curator-memory.md",
}

// futureRootFiles are root files OQ-9 will allow-list in a later phase —
// refused now with a message that says so, never "forbidden forever" and
// never a bare "does not exist" (OQ-9 §9 phase 1 table). Empty as of
// phase 2 (S4-T7): OQ-9 §9 defines exactly two phases, and both have now
// shipped (curator-memory.md in phase 1, index.md here) — kept, rather
// than deleted, as the one place a future phase 3 entry would go, and so
// isKnownRootFile's three-way check (allow-listed / forbidden / future)
// does not need a fourth "phase not started yet" case invented later.
var futureRootFiles = map[string]string{}

// isPatchableRootFile reports whether p is on the OQ-9 allow-list.
func isPatchableRootFile(p string) bool {
	for _, f := range patchableRootFiles {
		if f == p {
			return true
		}
	}
	return false
}

// isRevertableRootFile reports whether p is a root file buildRevertOps
// (revert.go) may rebuild as an inverse patch_page from a stored CAS
// blob — a strictly narrower question than isPatchableRootFile (D-CW,
// s4-tui.md C-110). An agent MAY propose a patch_page against every
// entry in patchableRootFiles; revert can only RECONSTRUCT the ones with
// no automatic writer computing their bytes out-of-band, i.e. those in
// revertableRootFiles. index.md fails this (create_page's index
// derivation writes it without every historical sha being guaranteed
// stored — see revertableRootFiles' doc comment); curator-memory.md
// passes (no automatic writer at all, so every byte it has ever held
// came from a stored patch_page pre/post-image).
func isRevertableRootFile(p string) bool {
	for _, f := range revertableRootFiles {
		if f == p {
			return true
		}
	}
	return false
}

// isKnownRootFile reports whether p is an exact, case-sensitive match for
// one of OQ-9's four named vault-root files — allow-listed, forbidden, or
// future — regardless of phase. ValidateOp (validate.go, S4-T0 repair-1
// secondary fix) dispatches a patch_page whose Path passes this check
// straight to validatePatchPage, before the generic path-shape gate that
// would otherwise reject SCHEMA.md (uppercase; vaultPathFilenameRE
// requires lowercase) with an unrelated message and make OQ-9's
// SCHEMA.md-specific refusal unreachable through the public entry point.
func isKnownRootFile(p string) bool {
	if isPatchableRootFile(p) {
		return true
	}
	if _, ok := forbiddenRootFiles[p]; ok {
		return true
	}
	if _, ok := futureRootFiles[p]; ok {
		return true
	}
	return false
}

// validateNonPagePatch validates a patch_page op whose Path is not a
// vault.Page — called from validatePatchPage's non-Page branch
// (validate.go), the one hook OQ-9 phase 1 (S4-T0) owns. op.Path is
// either an OQ-9 allow-listed vault-root file or a path that plain does
// not exist; everything else here applies only to the former.
func validateNonPagePatch(op Op, v *vault.Vault) error {
	if !isPatchableRootFile(op.Path) {
		if reason, ok := forbiddenRootFiles[op.Path]; ok {
			return fmt.Errorf("%w: patch_page: %s: %s", ErrValidation, op.Path, reason)
		}
		if reason, ok := futureRootFiles[op.Path]; ok {
			return fmt.Errorf("%w: patch_page: %s: %s", ErrValidation, op.Path, reason)
		}
		return fmt.Errorf("%w: patch_page: %s does not exist", ErrValidation, op.Path)
	}

	// L4 — no section targeting: whole-file or hunks only. A root file has
	// no vault.Page/ParseSections structure a Section name could address,
	// and index.md's "## <Section>" headings are engine-generated by
	// indexSectionFor (derive.go), not a human-addressable taxonomy
	// (OQ-9 §7 L4, §9 phase 1 table).
	if op.Section != "" {
		return fmt.Errorf("%w: patch_page: %s: section %q: a root-file patch accepts whole-file or hunk edits only, never a section target", ErrValidation, op.Path, op.Section)
	}

	// Before is a sha over the RAW bytes (v.Read), never page.SHA256() —
	// a root file carries no frontmatter/body structure to Serialize()
	// (OQ-9 §9 phase 1 table, backbone §5.5). canonicalContent (op.go)
	// already encodes exactly this fallback — v.Page miss -> v.Read — so
	// this reuses it rather than reimplementing the hash.
	cur, ok := canonicalSHA(v, op.Path)
	if !ok {
		return fmt.Errorf("%w: patch_page: %s: could not read the current content", ErrValidation, op.Path)
	}
	if op.Before != cur {
		return fmt.Errorf("%w: patch_page: %s: before does not match the current content", ErrValidation, op.Path)
	}

	// Frontmatter validation is SKIPPED HERE, VISIBLY: a root file has no
	// "---\n...\n---\n" frontmatter block for vault.ParsePage/FM.Validate
	// to parse in the first place (curator-memory.md and index.md are
	// prose/lists, not vault.Page bodies), so the create_page/patch_page
	// bullet that validates frontmatter against the schema simply does
	// not apply to this branch. This comment IS the skip — OQ-9 §9 phase
	// 1 table: "a skipped check that reads like a passed check is how
	// bugs ship" — there is deliberately no vault.ParsePage/FM.Validate
	// call anywhere in this function.

	// L2 — the one-writer guard.
	return checkRootFileOneWriter(v, op.Path)
}

// checkRootFileOneWriter is OQ-9's L2, forward direction: refuse when an
// automatic writer — a rename_page/merge_pages cascade (cascadeRoots,
// op.go) or a create_page's index.md derivation (derive.go rule (a)) —
// already live in the SAME open changeset also targets path. The message
// names the other op's id and kind (OQ-9 §7 L2): a refusal the reviewer
// cannot act on is a worse defect than the collision.
//
// Reading the changeset straight off disk, via v.Root(), is this
// function's only option: ValidateOp's signature is frozen at
// (op Op, v *vault.Vault, s *vault.Schema) — no Changeset parameter — and
// Append's per-kind choice of which *vault.Vault to validate a patch_page
// against (engine_changeset.go) is outside this subtask's file list, so
// there is no channel by which "the changeset's other live ops" can reach
// this function except reading the same changeset.json Append itself
// reads and writes. When Append calls ValidateOp for this new op, the new
// op has not yet been appended to the in-memory changeset or persisted,
// so the on-disk file still holds exactly the prior live ops — reading it
// here is equivalent to being handed c.Live() directly. When no .llmwiki
// exists at all (e.g. a bare vault.Open() in a test with no Engine), or
// no changeset is open, there is nothing to collide with: nil, nil.
//
// v is always e.vault (never a projection) for this call, because
// Append's cv-selection switch (engine_changeset.go) only overrides cv
// for OpRenamePage/OpMergePages — a patch_page's Before must match the
// literal working tree, not a projected view — so v.Root() is reliable
// here on every call, regardless of what else is already live.
//
// Direction (S4-T0 repair-1, closing the gap runs/S4-T0-rootfile-patch/
// report.md flagged): this is one half of OQ-9's L2. The other half —
// the automatic writer proposed AFTER the root-file patch already landed
// — is checkNewWriterOneWriter below, hooked into validateCascade and
// validateCreatePage. Both halves share one collision predicate,
// writesRootFile, so "these two ops collide" is defined once.
func checkRootFileOneWriter(v *vault.Vault, path string) error {
	ops, err := liveOpsInOpenChangeset(v.Root())
	if err != nil {
		return err
	}
	for _, top := range ops {
		if err := rootFileWriterConflict(top, path); err != nil {
			return err
		}
	}
	return nil
}

// checkNewWriterOneWriter is OQ-9's L2, mirror direction (S4-T0
// repair-1): before a new automatic-writer op (newOp — rename_page,
// merge_pages, or create_page, with its Cascade already built when this
// runs) is allowed into the changeset, refuse it if a live patch_page in
// the SAME open changeset already targets an allow-listed root file that
// newOp would also write. The order the two ops were proposed in must
// not matter — OQ-9 §9 L2 says "any automatic writer", not "any automatic
// writer proposed first" — so this is checkRootFileOneWriter's mirror,
// built from the same v.Root() disk read and the same writesRootFile
// predicate, just walked from the other op's side of the collision.
//
// Unlike checkRootFileOneWriter's v, this call's v CAN be a projected,
// disk-less vault (Root() == "") — Append's cascadeBase only returns
// e.vault when the changeset has zero other live ops yet, which is
// exactly the case where there is nothing for newOp to collide with; the
// moment a live op DOES exist to collide with, cascadeBase must project,
// and this v.Root() read is a defensive no-op precisely then (see this
// package's report.md, "known gap" section, for why closing that fully
// needs a seam this subtask's file list does not include). Hooking this
// check anyway — rather than skipping it — is still correct and still
// required: it fires for real the moment v is not a projection (the
// create_page path, and any rename_page/merge_pages that is validated
// against the real vault), it is unit-tested directly against
// newWriterConflict/writesRootFile for the cases a projection currently
// hides, and it is the one-line hook S4-T7 needs once index.md joins the
// allow-list.
func checkNewWriterOneWriter(v *vault.Vault, newOp Op) error {
	ops, err := liveOpsInOpenChangeset(v.Root())
	if err != nil {
		return err
	}
	for _, live := range ops {
		if err := newWriterConflict(newOp, live); err != nil {
			return err
		}
	}
	return nil
}

// writesRootFile is OQ-9 L2's single "do these two ops collide"
// predicate: it reports whether writer — a rename_page, merge_pages, or
// create_page op — automatically writes path. Both directions of the
// one-writer guard reduce to this one call (rootFileWriterConflict below,
// checking a proposed root-file patch against earlier live writers, and
// newWriterConflict, checking a proposed writer against an earlier live
// root-file patch), so the two directions cannot describe "collides" two
// different ways and drift apart.
//
// Retract and split_page derive nothing into index.md themselves
// (derive.go's header comment rules (b)/(c)); split_page's own
// create_page products are separate top-level ops already covered by the
// OpCreatePage case. Neither gets a case here — OQ-9 §7's prose lists
// "create/split/retract" together, but the code they describe does not,
// and this predicate follows the code (ratified in
// runs/S4-T0-rootfile-patch/report.md: "do not add split_page or retract
// arms").
func writesRootFile(writer Op, path string) bool {
	switch writer.Kind {
	case OpRenamePage, OpMergePages:
		for _, sub := range writer.Cascade {
			if sub.State == StateDropped || sub.State == StateRejected {
				continue
			}
			if sub.Path == path {
				return true
			}
		}
	case OpCreatePage:
		return path == "index.md"
	}
	return false
}

// autoWriterMechanism names, for a refusal message, HOW kind writes a
// root file automatically — "cascade" for rename_page/merge_pages,
// "index derivation" for create_page — so rootFileWriterConflict and
// newWriterConflict describe the same writer the same way instead of
// inlining the label twice and letting the two wordings drift apart.
func autoWriterMechanism(kind OpKind) string {
	switch kind {
	case OpRenamePage, OpMergePages:
		return "cascade"
	case OpCreatePage:
		return "index derivation"
	default:
		return "write"
	}
}

// rootFileWriterConflict reports the ErrValidation-wrapping, id-and-kind
// naming refusal when top is a live automatic writer of path (per
// writesRootFile). nil when top does not write path at all.
func rootFileWriterConflict(top Op, path string) error {
	if !writesRootFile(top, path) {
		return nil
	}
	return fmt.Errorf("%w: patch_page: %s is already written by %s (%s)'s %s in this changeset; resolve or drop %s before patching %s directly",
		ErrValidation, path, top.ID, top.Kind, autoWriterMechanism(top.Kind), top.ID, path)
}

// newWriterConflict is rootFileWriterConflict's mirror: live is an
// already-live op read off disk; nil unless live is a patch_page
// targeting one of OQ-9's four known root files (isKnownRootFile, not
// isPatchableRootFile — mirroring rootFileWriterConflict itself, which
// carries no phase gate of its own either; the phase gate lives in
// validateNonPagePatch, the ONE place a root-file patch_page can ever
// become live in the first place, so a live op targeting a
// not-yet-allow-listed name such as index.md in phase 1 can never
// actually reach this function outside a direct unit test such as
// TestOneWriterGuardReverseOrderCreateDerivation — exactly the case S4-T7
// needs already proven correct) that newOp (not yet live, not yet
// assigned an id — Append assigns ids after ValidateOp succeeds) would
// also write per writesRootFile, in which case it names live's id and
// kind — the same "id and kind, and what to do next" contract
// rootFileWriterConflict's message carries, read from the other side of
// the same collision.
func newWriterConflict(newOp Op, live Op) error {
	if live.Kind != OpPatchPage || !isKnownRootFile(live.Path) {
		return nil
	}
	if !writesRootFile(newOp, live.Path) {
		return nil
	}
	return fmt.Errorf("%w: %s: %s is already written by %s (%s) in this changeset; resolve or drop %s before completing this %s's %s",
		ErrValidation, newOp.Kind, live.Path, live.ID, live.Kind, live.ID, newOp.Kind, autoWriterMechanism(newOp.Kind))
}

// liveOpsInOpenChangeset returns the top-level live ops of whatever
// changeset sits under root/.llmwiki/changesets/open/ — nil, nil when
// root is "" (a projected, disk-less *vault.Vault — checkNewWriterOneWriter's
// caller can pass one of these; see its own doc comment), when .llmwiki
// does not exist, when changesets/open/ holds no changeset directory, or
// when the one directory there holds no changeset.json yet (OpenChangeset's
// own D-BA contract says it always writes one before returning, but a
// defensive nil-nil here costs nothing and avoids ever turning a
// validation call into a spurious hard error over a timing window that
// should not exist). Any other read/parse failure is a real error,
// returned rather than swallowed.
//
// The root == "" guard is deliberate, not redundant with os.IsNotExist
// below: filepath.Join("", ".llmwiki", ...) produces a RELATIVE path,
// which os.ReadDir would resolve against the process's cwd rather than
// fail outright — in the one real caller where this matters
// (checkNewWriterOneWriter, S4-T0 repair-1), that cwd is unrelated to any
// vault this validation call is about, and could coincidentally contain
// an unrelated .llmwiki (e.g. lw invoked from inside a vault, validating
// a projection of a DIFFERENT vault it is not currently rooted in). This
// makes that impossible to hit by construction rather than by the luck of
// which directory a process happens to be running from.
func liveOpsInOpenChangeset(root string) ([]Op, error) {
	if root == "" {
		return nil, nil
	}
	openDir := filepath.Join(root, ".llmwiki", "changesets", "open")
	entries, err := os.ReadDir(openDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stage: read open changesets: %w", err)
	}

	var id string
	for _, ent := range entries {
		if ent.IsDir() {
			id = ent.Name()
			break
		}
	}
	if id == "" {
		return nil, nil
	}

	b, err := os.ReadFile(filepath.Join(openDir, id, "changeset.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stage: read open changeset %s: %w", id, err)
	}

	var c Changeset
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("stage: parse open changeset %s: %w", id, err)
	}
	return c.Live(), nil
}
