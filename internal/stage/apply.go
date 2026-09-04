// apply.go implements Engine.Commit and Engine.Recover — backbone §5.4's
// ten-step Commit Contract and its Recover/RecoveryReport fence. Owned by
// S2-T4.
//
// POSIX gives this package no cross-file atomicity: each target path is
// written (or moved) independently via its own temp-file-then-rename, so a
// crash between two of those per-file operations can leave some paths at
// their post-commit content and others still at their pre-commit content
// (backbone §5.4 D-G). This file does not claim otherwise — Recover exists
// precisely to make that window inspectable (Applied vs Pending) and
// fixable (Fixable, backed by the CAS per D-BN), not to paper over it.
package stage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/vault"
)

// logRotateThreshold mirrors internal/lint/check_log_rotate.go's own
// constant of the same name and value: log.md is rotated to log-<year>.md
// once it would hold more than 500 "- " entries. Duplicated rather than
// imported because internal/lint's constant is unexported and this
// subtask does not own internal/lint — kept honest by
// TestLogRotateMatchesLintThreshold, which runs the real check against a
// log.md this package just wrote.
const logRotateThreshold = 500

// Commit applies the currently open changeset to the working tree,
// following backbone §5.4's ten steps in order, and returns the new
// commit's id.
func (e *Engine) Commit(message string) (string, error) {
	// Step 1.
	unlock, err := AcquireLock(e.llmwikiDir())
	if err != nil {
		return "", err
	}
	e.unlock = unlock

	// Step 2. Refresh re-hashes the tree and flips any now-stale op; a
	// changeset the review screen has not yet reconciled is refused
	// outright rather than partially applied.
	if err := e.Refresh(); err != nil {
		e.Close()
		return "", fmt.Errorf("stage: commit: %w", err)
	}
	c := e.open
	if hasStaleOp(c.Ops) {
		e.Close()
		return "", ErrStale
	}

	now := e.now().UTC()
	commitID, err := nextCommitID(filepath.Join(e.llmwikiDir(), "snapshots"))
	if err != nil {
		e.Close()
		return "", fmt.Errorf("stage: commit: %w", err)
	}

	live := c.Live()
	retractedDate := now.Format("2006-01-02")
	m, err := e.buildCommitMaterialization(live, retractedDate)
	if err != nil {
		e.Close()
		return "", fmt.Errorf("stage: commit: %w", err)
	}
	paths := commitTargetPaths(m)

	// Step 3. The full target-path list is journaled before a single byte
	// of the vault is touched.
	if err := e.journal.Append(Event{
		TS:        now,
		Kind:      EvCommitBegin,
		Changeset: c.ID,
		Commit:    commitID,
		Actor:     c.Author,
		Paths:     paths,
		Message:   message,
	}); err != nil {
		return "", fmt.Errorf("stage: commit: %w", err)
	}
	if err := e.checkFault("3"); err != nil {
		return "", err
	}

	// Step 4. Every pre-image and materialized post-image lands in the CAS
	// before step 5 writes its first byte (D-BN) — synthesized tombstone
	// bodies and the pre-image of every rename_page/merge_pages/
	// split_page/retract source included.
	if err := e.storeCommitMaterialization(m); err != nil {
		return "", fmt.Errorf("stage: commit: %w", err)
	}
	if err := e.checkFault("4"); err != nil {
		return "", err
	}

	// Step 4a. The baseline snapshot (MASTER §9 D-BU, §10 OR-9). Revert
	// diffs snapshot(N) against snapshot(N-1) (backbone §5.8) — but the
	// FIRST commit has no N-1: nextCommitID yields "000001" on an empty
	// snapshots/ and nothing ever writes "000000.tree", so Revert("000001")
	// — the one revert gate G2 runs — had no predecessor to diff against.
	// Capturing the pre-commit tree here, BEFORE step 5 writes a byte,
	// gives it one. Written only when absent, never overwriting: commit N's
	// own snapshots/<N-1>.tree was taken at ITS step 6, before ITS step 8
	// appended to log.md, and rewriting it here would silently rewrite that
	// recorded history. In practice this fires exactly once per vault.
	baselineID, err := predecessorCommitID(commitID)
	if err != nil {
		return "", fmt.Errorf("stage: commit: %w", err)
	}
	snapshotsDir := filepath.Join(e.llmwikiDir(), "snapshots")
	if _, statErr := os.Stat(filepath.Join(snapshotsDir, baselineID+".tree")); os.IsNotExist(statErr) {
		baseline, err := e.buildSnapshot()
		if err != nil {
			return "", fmt.Errorf("stage: commit: baseline snapshot: %w", err)
		}
		if err := WriteSnapshot(snapshotsDir, baselineID, baseline); err != nil {
			return "", fmt.Errorf("stage: commit: baseline snapshot: %w", err)
		}
	}

	// Step 5. Per-file write-temp/fsync/rename, in sorted path order.
	// Disposal is per kind and never os.Remove.
	if err := applyMaterialization(e.root, e.llmwikiDir(), commitID, m); err != nil {
		return "", fmt.Errorf("stage: commit: %w", err)
	}
	if err := e.checkFault("5"); err != nil {
		return "", err
	}

	// Step 6. The whole-vault snapshot, read fresh from disk (never
	// through e.vault, which is not reloaded until step 7 and would
	// otherwise describe the pre-commit tree).
	snap, err := e.buildSnapshot()
	if err != nil {
		return "", fmt.Errorf("stage: commit: %w", err)
	}
	if err := WriteSnapshot(filepath.Join(e.llmwikiDir(), "snapshots"), commitID, snap); err != nil {
		return "", fmt.Errorf("stage: commit: %w", err)
	}
	if err := e.checkFault("6"); err != nil {
		return "", err
	}

	// Step 7. The reload is not optional (backbone §5.4): index.Update
	// deletes any path Vault does not currently know, so without it every
	// create_page commit would delete the page it just created from the
	// index instead of adding it.
	if err := e.vault.Reload(); err != nil {
		return "", fmt.Errorf("stage: commit: reload vault: %w", err)
	}
	e.index.Update(e.vault, paths)
	if err := e.index.Save(filepath.Join(e.llmwikiDir(), "index.gob")); err != nil {
		return "", fmt.Errorf("stage: commit: save index: %w", err)
	}
	if err := e.checkFault("7"); err != nil {
		return "", err
	}

	// Step 8.
	creates, edits := countPagesAndEdits(live)
	if err := e.appendLog(commitID, c.Intent, creates, edits, now); err != nil {
		return "", fmt.Errorf("stage: commit: %w", err)
	}
	if err := e.checkFault("8"); err != nil {
		return "", err
	}

	// Step 9. commit_end carries the lint baseline for D-AG's regression
	// check (D-BO: Commit computes it itself, over the vault reloaded in
	// step 7), then the changeset moves from open/ to committed/ — the
	// move nothing in this package ever performs via a delete.
	data, err := commitEndData(e.vaultLintContext())
	if err != nil {
		return "", fmt.Errorf("stage: commit: %w", err)
	}
	if err := e.journal.Append(Event{
		TS:        now,
		Kind:      EvCommitEnd,
		Changeset: c.ID,
		Commit:    commitID,
		Actor:     c.Author,
		Message:   message,
		Data:      data,
	}); err != nil {
		return "", fmt.Errorf("stage: commit: %w", err)
	}
	committedDir := filepath.Join(e.llmwikiDir(), "changesets", "committed")
	if err := os.MkdirAll(committedDir, 0o755); err != nil {
		return "", fmt.Errorf("stage: commit: %w", err)
	}
	if err := os.Rename(filepath.Join(e.changesetOpenDir(), c.ID), filepath.Join(committedDir, c.ID)); err != nil {
		return "", fmt.Errorf("stage: commit: %w", err)
	}
	if err := e.checkFault("9"); err != nil {
		return "", err
	}

	// Step 10.
	e.open = nil
	e.nextOp = 0
	if err := e.Close(); err != nil {
		return commitID, fmt.Errorf("stage: commit: release lock: %w", err)
	}
	return commitID, nil
}

// checkFault invokes e.faultAfter(step) when the test-only hook is set and
// returns its error unchanged. It is called after each of steps 3 through
// 9 completes, so a test can simulate a process kill at that exact
// boundary: Commit returns immediately, without releasing the lock,
// mirroring what a real crash would leave behind (a stale lock is cleared
// by AcquireLock's own liveness check, or lw doctor --unlock).
func (e *Engine) checkFault(step string) error {
	if e.faultAfter == nil {
		return nil
	}
	return e.faultAfter(step)
}

// hasStaleOp reports whether any live op in ops — or, recursively, any
// live entry of its Cascade — carries StateStale. A dropped or rejected op
// is skipped: Live() already excludes it from what Commit writes, so its
// staleness cannot block a commit.
func hasStaleOp(ops []Op) bool {
	for _, op := range ops {
		if op.State == StateDropped || op.State == StateRejected {
			continue
		}
		if op.State == StateStale {
			return true
		}
		if hasStaleOp(op.Cascade) {
			return true
		}
	}
	return false
}

// --- step 4/5 materialization -------------------------------------------

// commitMaterialization is Commit's own in-memory plan for steps 4 and 5:
// every path written in place, every path moved out of the vault into
// tombstones/<commitID>/, and any additional pre-image bytes that must
// land in the CAS but are not disk targets themselves — a retract's
// original page, overwritten in place by its tombstone.
type commitMaterialization struct {
	writes         map[string][]byte
	moves          map[string][]byte
	extraPreImages [][]byte
}

// buildCommitMaterialization computes, from live (already Refreshed,
// already confirmed non-stale) top-level ops, the complete disposition of
// every path Commit will act on. No file is written or moved yet — this
// is the in-memory step that step 3's journal entry and step 4's CAS
// storage both read from.
//
// After the per-op loop, one S2-T8 rule (a) derivation pass runs over the
// whole live op set for every live create_page — never per op, which is
// exactly what clobbers two whole-file writers of the same path (C-63,
// derive.go's file header). Seeded from m.writes["index.md"] when a
// cascade already rewrote it in the loop above, so a rename cascade's link
// rewrite and a create's new line land in the same write; from the working
// tree otherwise. A vault with no index.md at all derives nothing — not an
// error.
func (e *Engine) buildCommitMaterialization(live []Op, retractedDate string) (*commitMaterialization, error) {
	m := &commitMaterialization{writes: map[string][]byte{}, moves: map[string][]byte{}}
	for _, op := range live {
		if err := e.planOp(m, op, retractedDate); err != nil {
			return nil, err
		}
	}

	if creates := liveCreatePages(live); len(creates) > 0 {
		running, ok := m.writes["index.md"]
		if !ok {
			if b, err := e.vault.Read("index.md"); err == nil {
				running, ok = b, true
			}
		}
		if ok {
			updated, err := deriveIndex(running, creates, e.postImage)
			if err != nil {
				return nil, fmt.Errorf("commit: derive index: %w", err)
			}
			m.writes["index.md"] = updated
		}
	}

	return m, nil
}

// planOp adds op's own disposition to m, then recurses into op.Cascade —
// every cascade sub-op is itself a patch_page write, handled uniformly by
// the OpPatchPage case regardless of which op's Cascade it came from.
//
// Contract (backbone §5.4 step 4/5, per-kind disposal, D-BL/D-BM): a
// retract's target is rewritten in place with a synthesized tombstone,
// whose original pre-image is also captured (extraPreImages) so Revert
// (S2-T6) has something to restore. A rename_page/merge_pages source is
// moved, never written over.
//
// split_page's source is NOT moved to tombstones/ (superseding this
// method's earlier, wave-4 treatment of it as rename/merge-shaped): S2-T8
// rule (c) leaves a disambiguation stub in place instead, the same
// written-in-place shape retract already has, so every inbound link to the
// split source keeps resolving with no engine-authored guess at which
// product it now means (derive.go's splitStub doc comment). Its original
// pre-image is captured the same way retract's is, for Revert.
//
// retractedDate is also split_page's stub date (splitStub's splitDate):
// one commit-time date, reused rather than re-read from the clock, for
// both of Commit's own Op-synthesized page shapes.
func (e *Engine) planOp(m *commitMaterialization, op Op, retractedDate string) error {
	if op.State == StateDropped || op.State == StateRejected {
		return nil
	}

	switch op.Kind {
	case OpCreatePage, OpPatchPage, OpIngestSource:
		b, err := e.postImage(op)
		if err != nil {
			return fmt.Errorf("commit %s: %w", op.ID, err)
		}
		m.writes[op.Path] = b

	case OpRenamePage:
		content, ok := canonicalContent(e.vault, op.From)
		if !ok {
			return fmt.Errorf("commit: rename_page: source %s is no longer readable", op.From)
		}
		m.moves[op.From] = content
		m.writes[op.To] = content

	case OpMergePages:
		for _, src := range op.Sources {
			content, ok := canonicalContent(e.vault, src)
			if !ok {
				return fmt.Errorf("commit: merge_pages: source %s is no longer readable", src)
			}
			m.moves[src] = content
		}
		// To's content is authored by a sibling create_page/patch_page op
		// in the same changeset (backbone §5.4 D-AZ) and lands in
		// m.writes when that sibling op is planned.

	case OpSplitPage:
		page, ok := e.vault.Page(op.Path)
		if !ok {
			return fmt.Errorf("commit: split_page: source %s is no longer readable", op.Path)
		}
		m.extraPreImages = append(m.extraPreImages, page.Serialize())
		m.writes[op.Path] = splitStub(page, op.Sources, retractedDate)
		// Its resulting pages are sibling create_page ops (backbone §5.4
		// D-AZ) and land in m.writes when those are planned.

	case OpRetract:
		page, ok := e.vault.Page(op.Path)
		if !ok {
			return fmt.Errorf("commit: retract: %s is no longer readable", op.Path)
		}
		m.extraPreImages = append(m.extraPreImages, page.Serialize())
		m.writes[op.Path] = retractTombstone(page, op.Rationale, retractedDate)

	case OpAddLink:
		// Content-free marker (D-AK); its edits are sibling patch_page ops.
	}

	for _, sub := range op.Cascade {
		if err := e.planOp(m, sub, retractedDate); err != nil {
			return err
		}
	}
	return nil
}

// commitTargetPaths returns the sorted, deduped union of m's write and
// move paths — the "full list of target paths" step 3 journals.
func commitTargetPaths(m *commitMaterialization) []string {
	set := make(map[string]bool, len(m.writes)+len(m.moves))
	for p := range m.writes {
		set[p] = true
	}
	for p := range m.moves {
		set[p] = true
	}
	return sortedSet(set)
}

// storeCommitMaterialization is step 4: every pre-image and materialized
// post-image m holds lands in the CAS. Store.Put is idempotent, so
// re-storing a post-image Append already stored (create_page, patch_page,
// ingest_source, cascade sub-ops) costs a lookup and nothing else; what it
// makes newly durable is exactly what nothing else stores —
// buildCommitMaterialization's move targets and retract's synthesized
// tombstone plus its original pre-image (D-BN).
func (e *Engine) storeCommitMaterialization(m *commitMaterialization) error {
	for _, b := range m.extraPreImages {
		if _, err := e.store.Put(b); err != nil {
			return fmt.Errorf("store pre-image: %w", err)
		}
	}
	for _, b := range m.writes {
		if _, err := e.store.Put(b); err != nil {
			return fmt.Errorf("store post-image: %w", err)
		}
	}
	for _, b := range m.moves {
		if _, err := e.store.Put(b); err != nil {
			return fmt.Errorf("store pre-image: %w", err)
		}
	}
	return nil
}

// applyMaterialization is step 5: every path in m, in sorted order, is
// either written in place (temp file + fsync + rename) or moved out of the
// vault into tombstones/<commitID>/ — never os.Remove.
func applyMaterialization(root, llmwikiDir, commitID string, m *commitMaterialization) error {
	for _, p := range commitTargetPaths(m) {
		if content, ok := m.writes[p]; ok {
			if err := writeFileAtomic(filepath.Join(root, filepath.FromSlash(p)), content); err != nil {
				return err
			}
			continue
		}
		if err := moveToTombstone(root, llmwikiDir, commitID, p); err != nil {
			return err
		}
	}
	return nil
}

// writeFileAtomic writes content to finalPath via finalPath+".lw-tmp",
// fsync, then rename — POSIX's only atomicity guarantee, per-file (D-G).
// The temp path is always named tmpPath at its cleanup site, per this
// subtask's brief: gate G2's os.Remove grep exempts os.Remove(tmpPath) by
// that exact spelling.
func writeFileAtomic(finalPath string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		return fmt.Errorf("write %s: %w", finalPath, err)
	}

	tmpPath := finalPath + ".lw-tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("write %s: %w", finalPath, err)
	}

	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmpPath)
		}
	}()

	if _, err := f.Write(content); err != nil {
		return fmt.Errorf("write %s: %w", finalPath, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("write %s: %w", finalPath, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write %s: %w", finalPath, err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return fmt.Errorf("write %s: %w", finalPath, err)
	}
	ok = true
	return nil
}

// moveToTombstone moves the vault file at root/relPath to
// llmwikiDir/tombstones/commitID/relPath with os.Rename — never
// os.Remove. The file is intact afterward, just no longer inside the
// vault working tree Vault.Pages() walks.
func moveToTombstone(root, llmwikiDir, commitID, relPath string) error {
	src := filepath.Join(root, filepath.FromSlash(relPath))
	dst := filepath.Join(llmwikiDir, "tombstones", commitID, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("tombstone %s: %w", relPath, err)
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("tombstone %s: %w", relPath, err)
	}
	return nil
}

// --- retract tombstone synthesis ----------------------------------------

// retractTombstone synthesizes a retract op's Commit-time-only tombstone
// page.
//
// Contract (backbone §5.4's retract-tombstone Contract, MASTER §9 D-BL):
// frontmatter is page's, preserved — title, created, type, tags,
// confidence, updated and every Extra key pass through unchanged — plus
// one new Extra key "retracted": retractedDate; sources is dropped, since
// the body no longer carries its provenance markers. type: retracted is
// never used: PageType.Valid() forbids it (C-43), so the original,
// already-valid Type is what survives. Body is "# <title>", a
// "> **Retracted.** <rationale>" block, and the original body's
// "## Related" section copied verbatim when present.
func retractTombstone(page *vault.Page, rationale, retractedDate string) []byte {
	fm := page.FM
	fm.Sources = nil
	extra := make(map[string]string, len(page.FM.Extra)+1)
	for k, v := range page.FM.Extra {
		extra[k] = v
	}
	extra["retracted"] = retractedDate
	fm.Extra = extra

	var body strings.Builder
	body.WriteString("# ")
	body.WriteString(fm.Title)
	body.WriteString("\n\n> **Retracted.** ")
	body.WriteString(rationale)
	body.WriteString("\n")
	if sec, ok := page.Section("## Related"); ok {
		body.WriteString("\n")
		body.WriteString(page.Body[sec.Start:sec.End])
	}

	tomb := vault.Page{Path: page.Path, FM: fm, Body: ensureTrailingNewline(body.String())}
	return tomb.Serialize()
}

// ensureTrailingNewline trims every trailing "\n" from s and appends
// exactly one back. Unlike vault's own normalizeTrailingNewline (private
// to internal/vault), a retract tombstone's body is never empty — it
// always carries at least "# <title>" — so there is no "empty stays
// empty" case to preserve here.
func ensureTrailingNewline(s string) string {
	return strings.TrimRight(s, "\n") + "\n"
}

// --- step 6: snapshot ----------------------------------------------------

// buildSnapshot walks the vault working tree as it stands after step 5
// and returns its whole-vault path -> canonical-sha256 manifest.
//
// It reads every *.md file fresh from disk via walkWholeTree
// (projection.go) rather than through e.vault — e.vault is not reloaded
// until step 7, so it still describes the pre-commit tree at this point —
// and canonicalizes each entry the same way canonicalContent (op.go)
// already does throughout this package: Page.Serialize() for a page under
// wiki/, raw bytes otherwise (backbone §5.1 D-AE; §2 defines no canonical
// form for a vault-root file, and raw/ sources fall back the same way
// canonicalContent already does — see this subtask's report).
func (e *Engine) buildSnapshot() (Snapshot, error) {
	tree, err := walkWholeTree(e.root)
	if err != nil {
		return nil, fmt.Errorf("build snapshot: %w", err)
	}
	snap := make(Snapshot, len(tree))
	for p, b := range tree {
		snap[p] = canonicalSHAFromDiskBytes(p, b)
	}
	return snap, nil
}

// canonicalSHAFromDiskBytes canonicalizes b as the file freshly read from
// path p would be, without consulting any *vault.Vault (which may not
// reflect p's current on-disk content yet). A wiki/ page that fails to
// parse falls back to its raw bytes, mirroring canonicalContent's own
// graceful behavior for a pre-existing malformed page this commit did not
// touch.
func canonicalSHAFromDiskBytes(p string, b []byte) string {
	if strings.HasPrefix(p, "wiki/") {
		if canon, err := vault.Canonical(p, b); err == nil {
			return sha256Hex(canon)
		}
	}
	return sha256Hex(b)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// --- step 8: log.md -------------------------------------------------------

// appendLog appends one human-readable entry to log.md — "- YYYY-MM-DD
// HH:MM <commit> <intent> (+N pages, ~M edits)" — rotating log.md's
// existing entries out to log-<year>.md when the append would leave it
// holding more than logRotateThreshold entries (/PLAN.md §6). The
// rotation boundary is the one internal/lint/check_log_rotate.go judges:
// after rotation, log.md holds zero entries, so that check never fires on
// the file this method just wrote.
func (e *Engine) appendLog(commitID, intent string, creates, edits int, now time.Time) error {
	logPath := filepath.Join(e.root, "log.md")
	existing, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read log.md: %w", err)
	}

	header, entries := splitLog(string(existing))
	line := fmt.Sprintf("- %s %s %s (+%d pages, ~%d edits)",
		now.Format("2006-01-02 15:04"), commitID, intent, creates, edits)
	entries = append(entries, line)

	if len(entries) > logRotateThreshold {
		year := entryYear(entries[0])
		archive := filepath.Join(e.root, "log-"+year+".md")
		if err := writeFileAtomic(archive, []byte(header+strings.Join(entries, "\n")+"\n")); err != nil {
			return fmt.Errorf("rotate log.md: %w", err)
		}
		entries = nil
	}

	content := header
	if len(entries) > 0 {
		content += strings.Join(entries, "\n") + "\n"
	}
	return writeFileAtomic(logPath, []byte(content))
}

// splitLog splits content into its leading header (every line before the
// first "- "-prefixed entry line, e.g. "# Log\n\n") and its entry lines
// (each without a trailing "\n"). An empty/absent log.md yields the
// standard "# Log\n\n" header this package's own fixtures use.
func splitLog(content string) (header string, entries []string) {
	if content == "" {
		return "# Log\n\n", nil
	}

	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	i := 0
	var headerLines []string
	for i < len(lines) && !strings.HasPrefix(lines[i], "- ") {
		headerLines = append(headerLines, lines[i])
		i++
	}
	entries = append(entries, lines[i:]...)

	header = strings.Join(headerLines, "\n")
	if header != "" {
		header += "\n"
	}
	return header, entries
}

// entryYear extracts the "YYYY" that begins a "- YYYY-MM-DD ..." log
// entry line — the same line[2:6] offset internal/lint's log-rotate check
// uses to name a rotation target.
func entryYear(line string) string {
	if len(line) >= 6 {
		return line[2:6]
	}
	return "unknown"
}

// countPagesAndEdits returns the log.md summary line's two counts: the
// number of top-level live create_page ops ("+N pages") and the number of
// every other write this commit makes — every other top-level live op,
// plus every live cascade sub-op, each a distinct file rewritten ("~M
// edits"). Not specified further by the backbone; see this subtask's
// report for the reasoning.
func countPagesAndEdits(live []Op) (creates, edits int) {
	for _, op := range live {
		if op.Kind == OpCreatePage {
			creates++
		} else {
			edits++
		}
		edits += countLiveCascade(op.Cascade)
	}
	return creates, edits
}

func countLiveCascade(cascade []Op) int {
	n := 0
	for _, sub := range cascade {
		if sub.State == StateDropped || sub.State == StateRejected {
			continue
		}
		n++
		n += countLiveCascade(sub.Cascade)
	}
	return n
}

// --- step 9: lint baseline -------------------------------------------------

// vaultLintContext builds the lint.Context Commit's own step 9 lints —
// the vault and index as they stand after step 7's reload, per D-BO.
func (e *Engine) vaultLintContext() *lint.Context {
	return &lint.Context{Vault: e.vault, Index: e.index, Graph: e.vault.Graph()}
}

// commitEndData runs lint.Run over ctx and returns commit_end's Data
// payload: {"lint_errors":N,"lint_warns":M} (backbone §5.7 D-AG).
func commitEndData(ctx *lint.Context) (json.RawMessage, error) {
	report := lint.Run(ctx, nil)
	b, err := json.Marshal(struct {
		LintErrors int `json:"lint_errors"`
		LintWarns  int `json:"lint_warns"`
	}{report.Errors, report.Warns})
	if err != nil {
		return nil, fmt.Errorf("marshal commit_end data: %w", err)
	}
	return b, nil
}

// --- Recover ---------------------------------------------------------------

// RecoveryReport describes what an interrupted Commit left behind
// (backbone §5.4's Recover Contract, MASTER §9 D-N). lw doctor (S6-T1) is
// its consumer.
type RecoveryReport struct {
	Interrupted bool     // true iff the last commit_begin has no matching commit_end
	Commit      string   // that commit id; "" when !Interrupted
	Applied     []string // target paths whose on-disk sha already equals the post-image
	Pending     []string // target paths not yet written
	Fixable     bool     // true iff Store.Has holds for every Pending path's post-image sha
	Unmoved     string   // changeset id whose commit completed but which is still in changesets/open/ (D-BP)
}

// Recover inspects the journal for the most recent commit_begin and
// reports what an interrupted apply left behind.
//
// Contract (backbone §5.4, MASTER §9 D-N): reads the journal's last
// EvCommitBegin. If a matching EvCommitEnd follows it, Interrupted is
// false and the rest of the struct is zero. Otherwise Interrupted is true,
// Commit is that event's commit id, and Applied/Pending partition its
// Paths by whether the file's current on-disk sha already equals the
// projected post-image. Recover never writes to the vault — it only
// reports.
//
// Contract — Unmoved (MASTER §9 D-BP): step 9 journals commit_end and then
// os.Renames changesets/open/<id> to changesets/committed/<id>; a crash
// between those two syscalls leaves a commit that is complete in every
// observable way while the changeset never left open/. Interrupted stays
// false in that case — the last commit_begin does have a matching
// commit_end — so when the two are matched, Recover checks
// changesets/open/<begin.Changeset>/ fresh on disk (never through any
// cached engine state) and sets Unmoved to that changeset id when the
// directory still exists; "" otherwise. Recover still does not perform
// the move — lw doctor (S6-T1) does.
//
// Every disk read here is a fresh os.ReadFile/os.Lstat against the
// filesystem, never through e.vault: a same-process Recover call, run
// immediately after a simulated crash, must see exactly what Commit
// actually wrote so far, not a snapshot of the tree taken before Commit
// began (e.vault is not reloaded until step 7).
func (e *Engine) Recover() (RecoveryReport, error) {
	begin, resolved, err := e.lastCommitBegin()
	if err != nil {
		return RecoveryReport{}, err
	}
	if begin == nil {
		return RecoveryReport{}, nil
	}
	if resolved {
		openPath := filepath.Join(e.changesetOpenDir(), begin.Changeset)
		if _, statErr := os.Stat(openPath); statErr == nil {
			return RecoveryReport{Unmoved: begin.Changeset}, nil
		} else if !os.IsNotExist(statErr) {
			return RecoveryReport{}, fmt.Errorf("stage: recover: stat %s: %w", openPath, statErr)
		}
		return RecoveryReport{}, nil
	}

	csPath := filepath.Join(e.changesetOpenDir(), begin.Changeset, "changeset.json")
	c, err := loadChangesetFile(csPath)
	if err != nil {
		return RecoveryReport{}, fmt.Errorf("stage: recover: %w", err)
	}

	dateStr := begin.TS.UTC().Format("2006-01-02")
	targets := map[string]recoverTarget{}
	for _, op := range c.Live() {
		collectRecoverTargets(targets, op, dateStr)
	}

	report := RecoveryReport{Interrupted: true, Commit: begin.Commit}
	fixable := true
	for _, p := range begin.Paths {
		t, ok := targets[p]
		if !ok {
			continue
		}
		applied, expectedSHA, err := e.resolveRecoverTarget(p, t)
		if err != nil {
			return RecoveryReport{}, err
		}
		if applied {
			report.Applied = append(report.Applied, p)
			continue
		}
		report.Pending = append(report.Pending, p)
		if expectedSHA == "" || !e.store.Has(expectedSHA) {
			fixable = false
		}
	}
	sort.Strings(report.Applied)
	sort.Strings(report.Pending)
	report.Fixable = fixable
	return report, nil
}

// lastCommitBegin returns the journal's most recent commit_begin event and
// whether a matching commit_end (the same Commit id, appearing after it in
// file order) follows it. begin is nil when the journal holds no
// commit_begin at all.
func (e *Engine) lastCommitBegin() (begin *Event, resolved bool, err error) {
	events, err := e.journal.Query(Filter{Kinds: []EventKind{EvCommitBegin, EvCommitEnd}})
	if err != nil {
		return nil, false, fmt.Errorf("stage: recover: query journal: %w", err)
	}

	lastIdx := -1
	for i, ev := range events {
		if ev.Kind == EvCommitBegin {
			lastIdx = i
		}
	}
	if lastIdx == -1 {
		return nil, false, nil
	}

	b := events[lastIdx]
	for _, ev := range events[lastIdx+1:] {
		if ev.Kind == EvCommitEnd && ev.Commit == b.Commit {
			return &b, true, nil
		}
	}
	return &b, false, nil
}

// loadChangesetFile reads and parses the changeset.json at path.
func loadChangesetFile(path string) (*Changeset, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load changeset %s: %w", path, err)
	}
	var c Changeset
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse changeset %s: %w", path, err)
	}
	return &c, nil
}

// recoverTarget is what Recover expects to eventually find at one
// commit_begin-journaled path.
//
// Every op kind except retract and split_page already carries its
// expected sha directly in changeset.json — After/SHA256 for a write,
// SourceSHAs for a moved source — so Recover reads it from there rather
// than the live vault (which may already reflect a partial step 5). A
// retract's tombstone, and (since this repair) a split_page's disambiguation
// stub, are synthesized only at Commit time and deliberately do not
// round-trip through changeset.json (D-AZ); isRetract/isSplit mark a
// target that must be resolved by re-synthesizing it instead.
//
// Repair note (R1): a split_page source used to be move: true, mirroring
// rename_page/merge_pages — correct through wave 6, wrong from S2-T8
// onward, when Commit stopped moving it to tombstones/ and started writing
// a stub in place (rule (c)). Under the old move:true handling,
// resolveRecoverTarget's move branch reports "applied" only when the path
// is ABSENT — which it never is again, so the source sat in Pending
// forever after any interrupted split commit, and Fixable was computed
// against the ORIGINAL page's sha (SourceSHAs[0]) rather than the stub
// Commit actually needs to finish writing. Recover gates OpenChangeset in
// the step-9 Unmoved window (D-BP), so a permanently-Pending, wrongly-
// Fixable split is a vault that never fully recovers. Fixed by giving
// split_page the identical isRetract-shaped treatment retract already had.
type recoverTarget struct {
	move      bool   // true: source moved out of the vault to tombstones/
	sha       string // expected content sha; unused when isRetract/isSplit
	isRetract bool
	rationale string // set only when isRetract
	isSplit   bool
	products  []string // set only when isSplit: op.Sources, for splitStub
	dateStr   string   // set only when isRetract/isSplit: the commit_begin event's own date
}

// collectRecoverTargets walks op — and, recursively, every live entry of
// its Cascade — into targets, mirroring buildCommitMaterialization's own
// per-kind switch but reading expected shas from the changeset rather
// than computing fresh content from the (possibly stale, possibly
// already-mutated) live vault.
func collectRecoverTargets(targets map[string]recoverTarget, op Op, dateStr string) {
	if op.State == StateDropped || op.State == StateRejected {
		return
	}

	switch op.Kind {
	case OpCreatePage, OpPatchPage:
		targets[op.Path] = recoverTarget{sha: op.After}
	case OpIngestSource:
		targets[op.Path] = recoverTarget{sha: op.SHA256}
	case OpRenamePage:
		if len(op.SourceSHAs) > 0 {
			targets[op.From] = recoverTarget{move: true, sha: op.SourceSHAs[0]}
			targets[op.To] = recoverTarget{sha: op.SourceSHAs[0]}
		}
	case OpMergePages:
		for i, src := range op.Sources {
			if i < len(op.SourceSHAs) {
				targets[src] = recoverTarget{move: true, sha: op.SourceSHAs[i]}
			}
		}
	case OpSplitPage:
		// In place, not moved (R1 repair note above): op.Sources is the
		// same products list splitStub needs, already persisted on the
		// changeset op, so no captured sha is required here at all.
		targets[op.Path] = recoverTarget{isSplit: true, products: op.Sources, dateStr: dateStr}
	case OpRetract:
		targets[op.Path] = recoverTarget{isRetract: true, rationale: op.Rationale, dateStr: dateStr}
	case OpAddLink:
		// Content-free marker; sibling patch_page ops populate their own
		// entries.
	}

	for _, sub := range op.Cascade {
		collectRecoverTargets(targets, sub, dateStr)
	}
}

// resolveRecoverTarget reports whether relPath already carries its
// post-commit content, and the sha Fixable should check via Store.Has
// when it does not.
func (e *Engine) resolveRecoverTarget(relPath string, t recoverTarget) (applied bool, expectedSHA string, err error) {
	abs := filepath.Join(e.root, filepath.FromSlash(relPath))

	if t.move {
		if _, statErr := os.Lstat(abs); statErr != nil {
			if os.IsNotExist(statErr) {
				return true, t.sha, nil
			}
			return false, "", fmt.Errorf("stage: recover: stat %s: %w", relPath, statErr)
		}
		return false, t.sha, nil
	}

	b, readErr := os.ReadFile(abs)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			if t.isRetract || t.isSplit {
				// A retract's or split_page's target exists pre-commit
				// (ValidateOp requires it) and this package never removes
				// a vault file — its absence here is not a legal state.
				// Report unresolvable rather than guess at a sha.
				return false, "", nil
			}
			return false, t.sha, nil
		}
		return false, "", fmt.Errorf("stage: recover: read %s: %w", relPath, readErr)
	}

	if t.isRetract {
		page, parseErr := vault.ParsePage(relPath, b)
		if parseErr != nil {
			return false, "", nil
		}
		if page.FM.Extra["retracted"] != "" {
			// The write is atomic (temp file + rename): a page-shaped
			// file at this path already carrying the tombstone's marker
			// key can only be the complete tombstone, never a partial
			// one.
			return true, "", nil
		}
		expected := sha256Hex(retractTombstone(page, t.rationale, t.dateStr))
		return sha256Hex(b) == expected, expected, nil
	}

	if t.isSplit {
		page, parseErr := vault.ParsePage(relPath, b)
		if parseErr != nil {
			return false, "", nil
		}
		if page.FM.Extra["split"] != "" {
			// Same reasoning as the isRetract branch above, for the same
			// reason (the write is atomic): a page-shaped file at this
			// path already carrying the stub's "split" marker key can
			// only be the complete stub, never a partial one.
			return true, "", nil
		}
		expected := sha256Hex(splitStub(page, t.products, t.dateStr))
		return sha256Hex(b) == expected, expected, nil
	}

	return sha256Hex(b) == t.sha, t.sha, nil
}
