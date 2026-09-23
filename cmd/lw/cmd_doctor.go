// cmd_doctor.go implements `lw doctor` (backbone §13, stage S6-T1): a
// read-only health check over a vault's index, object store, journal,
// interrupted applies, lock, configuration and provider. Every failure it
// prints names the fix. Doctor only diagnoses — the three repairs it
// performs are the ones the stage files sanction: --unlock and
// --rebuild-index (S6-T1), and --discard-changeset (TD-7), which moves a
// stuck open changeset to changesets/rejected/ without deleting anything.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/extract/cache"
	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/vault"
)

// The on-disk state layout doctor inspects (backbone §14). Paths printed to
// the user are vault-relative and slash-separated, built from these names.
const (
	stateDirName     = ".llmwiki"
	objectsDirName   = "objects"
	snapshotsDirName = "snapshots"
	indexFileName    = "index.gob"
	journalFileName  = "journal.ndjson"
	lockFileName     = "lock"
)

const (
	stateRel     = stateDirName
	objectsRel   = stateDirName + "/" + objectsDirName
	snapshotsRel = stateDirName + "/" + snapshotsDirName
	indexRel     = stateDirName + "/" + indexFileName
	journalRel   = stateDirName + "/" + journalFileName
	lockRel      = stateDirName + "/" + lockFileName

	openChangesetsRel     = stateDirName + "/changesets/open"
	rejectedChangesetsRel = stateDirName + "/changesets/rejected"
	cacheRel              = stateDirName + "/cache/extract"
)

// doctorProbeTimeout bounds the provider probe, and the PDF sidecar's
// --version with it (007 F.W7): a hung endpoint or sidecar must not hang a
// health check.
const doctorProbeTimeout = 20 * time.Second

// doclingTestedVersion is the Docling release lw's PDF backend is tested
// against (007 T1's probe measured it, 2026-09-23); doctor warns when the
// installed sidecar differs.
const doclingTestedVersion = "2.130.0"

// probeProvider runs the provider health check behind a package-level var —
// the same seam cmd_ingest.go's newAgent uses, because the seam inside
// internal/llm (the Client's http client) is unexported and lives in another
// package. cmdDoctor installs the real probe; tests substitute a fake so the
// suite never touches the network.
//
// llm.Client.Probe caps its own request at 256 tokens (backbone §8, C-101,
// D-CP): a one-token probe is cut off before the model can emit a tool call
// and would report a fully capable provider as incapable.
var probeProvider = func(ctx context.Context, cfg *config.Config) llm.ProbeResult {
	apiKey, err := cfg.ResolveAPIKey()
	if err != nil {
		return llm.ProbeResult{Reachable: false, Model: cfg.LLM.Model, Err: err}
	}
	client := llm.New(llm.Config{
		BaseURL:     cfg.LLM.BaseURL,
		Model:       cfg.LLM.Model,
		APIKey:      apiKey,
		Temperature: cfg.LLM.Temperature,
		MaxTokens:   cfg.LLM.MaxTokens,
		Thinking:    cfg.LLM.Thinking,
		Timeout:     doctorProbeTimeout,
	})
	return client.Probe(ctx)
}

// probeExtractorVersion runs the PDF sidecar's version check behind a
// package-level var — the same seam shape as probeProvider above, because
// extract.PDFVersion shells out to the configured sidecar and no test may
// depend on a real Docling install or pay its ~4 s --version (007 F.W7).
var probeExtractorVersion = extract.PDFVersion

// doctorCheck is the result of one health check. Detail states what is true;
// Remedy, empty when OK, states the fix and never restates the symptom.
type doctorCheck struct {
	Name    string
	OK      bool
	Skipped bool

	// Warn marks a condition that is true but that lw deliberately refuses
	// to fix (005 contract §7: a git-tracked .llmwiki/). A warn check keeps
	// OK: true, so failed() and the exit code treat it as a pass; writeText
	// marks it visibly and prints its remedy the way it prints a failure's.
	Warn bool

	Detail string
	Remedy string
}

// doctorReport is the whole run: the vault it inspected, the repairs the
// --unlock/--rebuild-index/--discard-changeset flags performed, and one
// entry per check.
type doctorReport struct {
	Vault   string
	Actions []string
	Checks  []doctorCheck

	// discardNothing records that --discard-changeset was asked for and
	// found no open changeset to discard. cmdDoctor exits 1 on it, so a
	// scripted caller can tell "discarded" from "there was nothing there" —
	// the same distinction --unlock does not need, because removing a lock
	// that is not held is what removing a lock means.
	discardNothing bool
}

// failed reports whether any check failed; a skipped check is not a failure
// (its reason is reported by the check that did fail).
func (r doctorReport) failed() bool {
	for _, c := range r.Checks {
		if !c.OK {
			return true
		}
	}
	return false
}

// writeText renders the human-readable form: one line per check, with the
// remedy indented under any failure.
func (r doctorReport) writeText(w io.Writer) {
	fmt.Fprintf(w, "lw doctor — %s\n", r.Vault)
	for _, a := range r.Actions {
		fmt.Fprintln(w, a)
	}
	failed := 0
	for _, c := range r.Checks {
		mark := "✓"
		switch {
		case c.Skipped:
			mark = "-"
		case !c.OK:
			mark = "✗"
			failed++
		case c.Warn:
			mark = "!"
		}
		fmt.Fprintf(w, "%s %-8s %s\n", mark, c.Name, c.Detail)
		if (!c.OK || c.Warn) && c.Remedy != "" {
			fmt.Fprintf(w, "  fix: %s\n", c.Remedy)
		}
	}
	if failed > 0 {
		fmt.Fprintf(w, "%d of %d check(s) failed\n", failed, len(r.Checks))
	}
}

// doctorJSONCheck and doctorJSONReport are the --json shape: the same
// findings, machine-readable, with no field carrying a secret value.
type doctorJSONCheck struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Skipped bool   `json:"skipped,omitempty"`
	Warn    bool   `json:"warn,omitempty"`
	Detail  string `json:"detail"`
	Remedy  string `json:"remedy,omitempty"`
}

type doctorJSONReport struct {
	Vault   string            `json:"vault"`
	OK      bool              `json:"ok"`
	Actions []string          `json:"actions,omitempty"`
	Checks  []doctorJSONCheck `json:"checks"`
}

// writeJSON renders the scripting form. Keys are struct fields in a fixed
// order (00-conventions.md §3), so the same state always prints the same
// bytes.
func (r doctorReport) writeJSON(w io.Writer) error {
	out := doctorJSONReport{Vault: r.Vault, OK: !r.failed(), Actions: r.Actions}
	for _, c := range r.Checks {
		out.Checks = append(out.Checks, doctorJSONCheck{
			Name:    c.Name,
			OK:      c.OK,
			Skipped: c.Skipped,
			Warn:    c.Warn,
			Detail:  c.Detail,
			Remedy:  c.Remedy,
		})
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("encode doctor report: %w", err)
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write doctor report: %w", err)
	}
	return nil
}

// cmdDoctor checks a vault's health and prints one ✓/✗ line per check, each
// failure followed by the fix. It exits 0 when every check passed and 1 when
// any failed; its own stdout is the result, so a failure is signalled with
// exitError rather than a bare error (backbone §13, MASTER §9 D-AC).
func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	unlock := fs.Bool("unlock", false, "remove a stale lock before checking")
	rebuild := fs.Bool("rebuild-index", false, "rebuild the search index from the vault before checking")
	discard := fs.Bool("discard-changeset", false, "move the open changeset to changesets/rejected/ before checking (never deleted)")
	asJSON := fs.Bool("json", false, "emit machine-readable JSON instead of text")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "lw doctor: unexpected argument %q\n", fs.Arg(0))
		return &exitError{code: 2}
	}

	root, err := findVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	attachLoggingAt(root) // read-only: join the trail, never create it

	rep := runDoctor(context.Background(), root, doctorOptions{
		unlock:           *unlock,
		rebuildIndex:     *rebuild,
		discardChangeset: *discard,
		probe:            true,
	})

	if *asJSON {
		if err := rep.writeJSON(os.Stdout); err != nil {
			return err
		}
	} else {
		rep.writeText(os.Stdout)
	}
	if rep.discardNothing {
		// TD-7: a caller asking to clear a stuck changeset has to be able to
		// tell "cleared" from "there was nothing to clear", so an empty
		// changesets/open is exit 1 even though every check passed.
		return &exitError{code: 1}
	}
	if rep.failed() {
		return &exitError{code: 1}
	}
	return nil
}

// doctorOptions are the parts of the command line runDoctor acts on.
type doctorOptions struct {
	unlock           bool // remove a stale lock (stage.BreakLock) before checking
	rebuildIndex     bool // rebuild and save the index before checking
	discardChangeset bool // move the open changeset to changesets/rejected before checking
	probe            bool // run the provider check (false in tests)
}

// runDoctor performs the repairs the flags ask for, then runs every check in
// the stage file's order: index, objects, journal, recovery, lock, git,
// config, provider. The index check reads .llmwiki/index.gob directly rather than
// through stage.OpenEngine, because opening the engine rebuilds that cache
// as a side effect and would silently repair the very fault doctor exists to
// report.
func runDoctor(ctx context.Context, root string, o doctorOptions) doctorReport {
	rep := doctorReport{Vault: root}

	v, err := vault.Open(root)
	if err != nil {
		rep.Checks = append(rep.Checks, doctorCheck{
			Name:   "vault",
			Detail: fmt.Sprintf("the vault at %s could not be opened: %v", root, err),
			Remedy: fmt.Sprintf("repair %s — lw cannot read the vault without it", filepath.ToSlash(filepath.Join(root, "SCHEMA.md"))),
		})
		return rep
	}

	if o.unlock {
		if err := stage.BreakLock(filepath.Join(root, stateDirName)); err != nil {
			rep.Actions = append(rep.Actions, "unlock failed: "+err.Error())
		} else {
			rep.Actions = append(rep.Actions, "removed "+lockRel)
		}
	}
	if o.rebuildIndex {
		line, err := rebuildIndex(root, v)
		if err != nil {
			rep.Actions = append(rep.Actions, "rebuild failed: "+err.Error())
		} else {
			rep.Actions = append(rep.Actions, line)
		}
	}

	rep.Checks = append(rep.Checks, checkIndex(root, v))

	if e, err := stage.OpenEngine(root); err != nil {
		rep.Checks = append(rep.Checks, doctorCheck{
			Name:   "state",
			Detail: fmt.Sprintf("%s could not be opened: %v", stateRel, err),
			Remedy: "check the permissions on " + stateRel + "; lw creates it on first use",
		})
		if o.discardChangeset {
			rep.Actions = append(rep.Actions, fmt.Sprintf("discard failed: %s could not be opened: %v", stateRel, err))
		}
	} else {
		defer e.Close()
		if o.discardChangeset {
			res := discardChangeset(e, root)
			rep.Actions = append(rep.Actions, res.line)
			rep.discardNothing = res.nothing
		}
		rep.Checks = append(rep.Checks,
			checkObjects(root, e),
			checkJournal(root),
			checkRecovery(e),
			checkLock(root),
		)
	}

	rep.Checks = append(rep.Checks, checkTracked(root))

	cfg, cfgErr := config.Load()
	rep.Checks = append(rep.Checks, checkConfig(cfg, cfgErr))
	if c := checkWeb(cfg, cfgErr); c != nil {
		rep.Checks = append(rep.Checks, *c)
	}
	if c := checkPDFExtractor(ctx, cfg, cfgErr, root, o.probe); c != nil {
		rep.Checks = append(rep.Checks, *c)
	}
	rep.Checks = append(rep.Checks, checkLLMBudget(cfg, cfgErr))
	if o.probe {
		rep.Checks = append(rep.Checks, checkProvider(ctx, cfg))
	}
	return rep
}

// discardReason is the message every --discard-changeset rejection is
// journalled with, so `lw log --rejected` says why the changeset moved.
const discardReason = "discarded by lw doctor --discard-changeset"

// discardResult is what one --discard-changeset run did: the action line the
// report prints, and whether there was nothing there to discard.
type discardResult struct {
	line    string
	nothing bool
}

// discardChangeset is the --discard-changeset repair (TD-7): the open
// changeset moves to changesets/rejected/ — never deleted — so a stuck
// changeset stops refusing every future one while staying inspectable, with
// its ops and its session transcript travelling with it.
//
// Engine.Reject is the path to take whenever it can: it renames the whole
// directory and journals changeset_rejected against the changeset's own
// author. It cannot see a directory whose changeset.json is missing or
// unreadable — the shape the C-118 session-create race fabricates, which
// until now was the one stuck state no verb could clear — so on a read
// failure the directory is renamed here instead, exactly as §5.4's Reject
// renames it, with the same rejection event journalled through the engine's
// own journal against the human actor this verb runs as. Nothing is ever
// deleted, and nothing under the vault working tree is touched: a changeset
// that cannot be read has applied nothing.
func discardChangeset(e *stage.Engine, root string) discardResult {
	cs, err := e.Current()
	switch {
	case err == nil:
		if rejErr := e.Reject(discardReason); rejErr != nil {
			return discardResult{line: "discard failed: " + rejErr.Error()}
		}
		return discardResult{line: fmt.Sprintf("discarded open changeset %s (%s -> %s)",
			cs.ID, openChangesetsRel, rejectedChangesetsRel)}

	case errors.Is(err, stage.ErrNoChangeset):
		return discardResult{line: "no open changeset: nothing to discard", nothing: true}

	default:
		id, movErr := discardUnreadable(root)
		if movErr != nil {
			return discardResult{line: fmt.Sprintf("discard failed: the open changeset cannot be read (%v) and could not be moved: %v", err, movErr)}
		}
		if jErr := e.Journal().Append(stage.Event{
			TS:        time.Now().UTC(),
			Kind:      stage.EvChangesetRejected,
			Changeset: id,
			Actor:     stage.Author{Kind: "human"}, // the curator who ran lw doctor
			Message:   discardReason + " (the changeset could not be read)",
		}); jErr != nil {
			return discardResult{line: fmt.Sprintf("discard failed: %s moved to %s but journalling it did not: %v", id, rejectedChangesetsRel, jErr)}
		}
		return discardResult{line: fmt.Sprintf("discarded unreadable open changeset %s (%s -> %s); it had no readable changeset.json",
			id, openChangesetsRel, rejectedChangesetsRel)}
	}
}

// discardUnreadable renames the one changeset directory under
// changesets/open/ into changesets/rejected/, and returns its id. It is the
// half of Reject that needs no changeset.json; anything richer than exactly
// one directory there is refused rather than guessed at, and reported for a
// human to look at.
func discardUnreadable(root string) (string, error) {
	openDir := filepath.Join(root, stateDirName, "changesets", "open")
	entries, err := os.ReadDir(openDir)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", openChangesetsRel, err)
	}
	var ids []string
	for _, ent := range entries {
		if ent.IsDir() {
			ids = append(ids, ent.Name())
		}
	}
	if len(ids) != 1 {
		return "", fmt.Errorf("want exactly one changeset directory under %s, found %d", openChangesetsRel, len(ids))
	}
	id := ids[0]
	dst := filepath.Join(root, stateDirName, "changesets", "rejected", id)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", rejectedChangesetsRel, err)
	}
	if err := os.Rename(filepath.Join(openDir, id), dst); err != nil {
		return "", fmt.Errorf("move %s/%s: %w", openChangesetsRel, id, err)
	}
	return id, nil
}

// rebuildIndex rebuilds the search index from the vault and saves it — the
// one repair --rebuild-index performs. It is the same rebuild
// stage.OpenEngine runs when the cache is absent or stale (backbone §5.4),
// made explicit so the report describes state a later `lw doctor` will find
// on disk.
func rebuildIndex(root string, v *vault.Vault) (string, error) {
	dir := filepath.Join(root, stateDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", stateRel, err)
	}
	ix := index.Build(v)
	if err := ix.Save(filepath.Join(dir, indexFileName)); err != nil {
		return "", fmt.Errorf("save %s: %w", indexRel, err)
	}
	return fmt.Sprintf("rebuilt %s (%d document(s))", indexRel, ix.Len()), nil
}

// checkIndex reports whether the index cache exists and matches the vault.
// A vault with no .llmwiki state at all passes: a fresh vault has no cache
// to be stale, and the first engine open builds one.
func checkIndex(root string, v *vault.Vault) doctorCheck {
	const name = "index"
	path := filepath.Join(root, stateDirName, indexFileName)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if _, err := os.Stat(filepath.Join(root, stateDirName)); errors.Is(err, os.ErrNotExist) {
			return doctorCheck{Name: name, OK: true, Detail: fmt.Sprintf("no %s yet; the index is built on first use", stateRel)}
		}
		return doctorCheck{
			Name:   name,
			Detail: fmt.Sprintf("index missing: %s does not exist", indexRel),
			Remedy: "run lw doctor --rebuild-index to rebuild it from the vault",
		}
	}
	ix, err := index.Load(path)
	if err != nil {
		return doctorCheck{
			Name:   name,
			Detail: fmt.Sprintf("index unreadable: %s: %v", indexRel, err),
			Remedy: "run lw doctor --rebuild-index to rebuild it from the vault",
		}
	}
	// A version mismatch is not staleness (028 review nit): every page SHA
	// can match and the file still cannot be trusted, because the older
	// lw's layout means something different by the same bytes. Say so —
	// "written by an older lw", with both format numbers — instead of the
	// generic stale wording that would blame the vault's content.
	if s := ix.Schema(); s != index.SchemaVersion {
		return doctorCheck{
			Name:   name,
			Detail: fmt.Sprintf("%s was written by an older lw (index format %d, want %d); %d document(s) indexed", indexRel, s, index.SchemaVersion, ix.Len()),
			Remedy: "run lw doctor --rebuild-index to rebuild it in the current format",
		}
	}
	if ix.StaleAgainst(v) {
		return doctorCheck{
			Name:   name,
			Detail: fmt.Sprintf("%s is stale against the vault (%d document(s) indexed)", indexRel, ix.Len()),
			Remedy: "run lw doctor --rebuild-index to re-index the pages as they are now",
		}
	}
	return doctorCheck{Name: name, OK: true, Detail: fmt.Sprintf("%d document(s) indexed in %s", ix.Len(), indexRel)}
}

// objectRef is one sha the object store is expected to hold, with the place
// that references it, so the remedy can name both.
type objectRef struct {
	sha  string
	from string
}

// appendOpObjectRefs appends every sha op and its cascade sub-ops reference.
// Empty sha fields are skipped: Before is "" for a new file, and add_link is
// a content-free marker.
func appendOpObjectRefs(refs []objectRef, csID string, op stage.Op) []objectRef {
	for _, f := range []struct{ field, sha string }{
		{"before", op.Before},
		{"after", op.After},
		{"sha256", op.SHA256},
	} {
		if f.sha != "" {
			refs = append(refs, objectRef{sha: f.sha, from: fmt.Sprintf("open changeset %s op %s %s", csID, op.ID, f.field)})
		}
	}
	for _, sha := range op.SourceSHAs {
		if sha != "" {
			refs = append(refs, objectRef{sha: sha, from: fmt.Sprintf("open changeset %s op %s source_shas", csID, op.ID)})
		}
	}
	for _, sub := range op.Cascade {
		refs = appendOpObjectRefs(refs, csID, sub)
	}
	return refs
}

// checkObjects reports whether the CAS holds every object the vault's state
// needs: the open changeset's shas, and the pre- and post-images of the
// paths the most recent commit wrote — what Diff, Commit and Revert read
// back. A missing object is damage doctor cannot repair, so the remedy names
// the first missing sha and says so.
func checkObjects(root string, e *stage.Engine) doctorCheck {
	const name = "objects"
	store, err := stage.OpenStore(filepath.Join(root, stateDirName, objectsDirName))
	if err != nil {
		return doctorCheck{
			Name:   name,
			Detail: fmt.Sprintf("%s could not be opened: %v", objectsRel, err),
			Remedy: "check the permissions on " + objectsRel,
		}
	}

	var refs []objectRef
	csID := ""
	csRefs := 0
	if cs, err := e.Current(); err == nil {
		csID = cs.ID
		for _, op := range cs.Ops {
			before := len(refs)
			refs = appendOpObjectRefs(refs, cs.ID, op)
			csRefs += len(refs) - before
		}
	} else if !errors.Is(err, stage.ErrNoChangeset) {
		return doctorCheck{
			Name:   name,
			Detail: fmt.Sprintf("the open changeset could not be read: %v", err),
			Remedy: "inspect " + stateRel + "/changesets/open by hand; its projected content cannot be verified until it reads",
		}
	}

	// The snapshot half of the check. Only the paths the most recent commit
	// wrote are required — its post-image, and the pre-image Revert would
	// restore from. A .tree manifests the whole vault, so its remaining
	// entries are content shas of files whose bytes were never blobs; on the
	// minimal fixture that is 11 of 11 "missing" on a vault that is
	// perfectly healthy, so they are counted, not required.
	snapshotsDir := filepath.Join(root, stateDirName, snapshotsDirName)
	ids, err := snapshotIDs(snapshotsDir)
	if err != nil {
		return doctorCheck{
			Name:   name,
			Detail: fmt.Sprintf("%s could not be read: %v", snapshotsRel, err),
			Remedy: "check the permissions on " + snapshotsRel,
		}
	}
	snapRefs := 0
	snapEntries := 0
	if len(ids) > 0 {
		newestID := ids[len(ids)-1]
		newest, err := stage.ReadSnapshot(snapshotsDir, newestID)
		if err != nil {
			return doctorCheck{
				Name:   name,
				Detail: fmt.Sprintf("snapshot %s could not be read: %v", newestID, err),
				Remedy: fmt.Sprintf("snapshot %s is what lw revert reconstructs from — restore %s/%s.tree from a backup, or re-commit", newestID, snapshotsRel, newestID),
			}
		}
		snapEntries = len(newest)

		paths, err := lastCommitPaths(e)
		if err != nil {
			return doctorCheck{
				Name:   name,
				Detail: fmt.Sprintf("the journal's last commit could not be read: %v", err),
				Remedy: fmt.Sprintf("inspect %s by hand; lw cannot tell which objects the last commit made durable", journalRel),
			}
		}
		sort.Strings(paths)
		for _, p := range paths {
			if sha, ok := newest[p]; ok && sha != "" {
				refs = append(refs, objectRef{sha: sha, from: fmt.Sprintf("snapshot %s %s (post-image)", newestID, p)})
				snapRefs++
			}
			if len(ids) < 2 {
				continue
			}
			prevID := ids[len(ids)-2]
			prev, err := stage.ReadSnapshot(snapshotsDir, prevID)
			if err != nil {
				continue
			}
			if sha, ok := prev[p]; ok && sha != "" {
				refs = append(refs, objectRef{sha: sha, from: fmt.Sprintf("snapshot %s %s (pre-image)", prevID, p)})
				snapRefs++
			}
		}
	}

	if len(refs) == 0 {
		return doctorCheck{Name: name, OK: true, Detail: "no objects referenced yet (no open changeset, no snapshot)"}
	}

	missing := make([]objectRef, 0, 1)
	for _, r := range refs {
		if !store.Has(r.sha) {
			missing = append(missing, r)
		}
	}
	if len(missing) > 0 {
		first := missing[0]
		return doctorCheck{
			Name:   name,
			Detail: fmt.Sprintf("%d of %d required object(s) missing; first: %s (%s)", len(missing), len(refs), first.sha, first.from),
			Remedy: fmt.Sprintf("restore object %s from a backup, or re-stage the change that wrote it; lw doctor cannot repair %s", first.sha, objectsRel),
		}
	}
	var parts []string
	if csID != "" {
		parts = append(parts, fmt.Sprintf("open changeset %s: %d", csID, csRefs))
	}
	if len(ids) > 0 {
		parts = append(parts, fmt.Sprintf("snapshots: %d", snapRefs))
	}
	detail := fmt.Sprintf("%d object(s) present (%s)", len(refs), strings.Join(parts, ", "))
	if len(ids) > 0 {
		detail += fmt.Sprintf("; snapshot %s holds %d entries", ids[len(ids)-1], snapEntries)
	}
	return doctorCheck{Name: name, OK: true, Detail: detail}
}

// snapshotIDs returns the commit ids of every snapshot in dir, ascending.
func snapshotIDs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var ids []string
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, ".tree") {
			continue
		}
		id := strings.TrimSuffix(name, ".tree")
		if len(id) != 6 {
			continue
		}
		if _, err := strconv.Atoi(id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

// lastCommitPaths returns the target paths of the journal's most recent
// commit_begin — the full list Commit journals before touching a byte
// (backbone §5.4 step 3), and the same list an interrupted apply partitions
// into applied and pending. Those are the paths whose pre- and post-images
// Commit made durable in the CAS (backbone §5.4, D-BN).
func lastCommitPaths(e *stage.Engine) ([]string, error) {
	events, err := e.Journal().Query(stage.Filter{
		Kinds: []stage.EventKind{stage.EvCommitBegin},
		Limit: 1,
	})
	if err != nil {
		return nil, fmt.Errorf("query journal: %w", err)
	}
	if len(events) == 0 {
		return nil, nil
	}
	return events[0].Paths, nil
}

// checkJournal reports whether every line of journal.ndjson parses as an
// event. The journal's own readers skip bad lines silently (backbone §5.7,
// MASTER §9 D-AV), so this is the only place the damage becomes visible.
// The journal is append-only and nothing in lw ever rewrites it, so the
// remedy is the offending line number and offset, not a repair.
func checkJournal(root string) doctorCheck {
	const name = "journal"
	f, err := os.Open(filepath.Join(root, stateDirName, journalFileName))
	if errors.Is(err, os.ErrNotExist) {
		return doctorCheck{Name: name, OK: true, Detail: "no journal yet"}
	}
	if err != nil {
		return doctorCheck{
			Name:   name,
			Detail: fmt.Sprintf("%s could not be opened: %v", journalRel, err),
			Remedy: "check the permissions on " + journalRel,
		}
	}
	defer f.Close()

	r := bufio.NewReader(f)
	var lineNo, offset, events int
	for {
		line, rerr := r.ReadString('\n')
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			return doctorCheck{
				Name:   name,
				Detail: fmt.Sprintf("%s could not be read: %v", journalRel, rerr),
				Remedy: "check the permissions on " + journalRel,
			}
		}
		if line != "" {
			lineNo++
			var ev stage.Event
			if err := json.Unmarshal([]byte(strings.TrimSuffix(line, "\n")), &ev); err != nil {
				return doctorCheck{
					Name:   name,
					Detail: fmt.Sprintf("line %d (byte offset %d) does not parse: %v", lineNo, offset, err),
					Remedy: fmt.Sprintf("%s is append-only and lw never rewrites it — repair or remove line %d by hand; readers skip it, so the history on either side is intact", journalRel, lineNo),
				}
			}
			events++
			offset += len(line)
		}
		if rerr != nil {
			break
		}
	}
	return doctorCheck{Name: name, OK: true, Detail: fmt.Sprintf("%d event(s) parse cleanly", events)}
}

// checkRecovery reports what Engine.Recover found: an interrupted apply, a
// completed commit whose changeset never left changesets/open/, or neither.
// Doctor prints the applied/pending split and how the commit could be rolled
// forward; it never runs the repair — the engine owns every write.
func checkRecovery(e *stage.Engine) doctorCheck {
	const name = "recovery"
	rr, err := e.Recover()
	if err != nil {
		return doctorCheck{
			Name:   name,
			Detail: fmt.Sprintf("looking for an interrupted apply: %v", err),
			Remedy: fmt.Sprintf("inspect %s by hand; lw could not tell whether the last commit completed", journalRel),
		}
	}

	switch {
	case rr.Interrupted:
		detail := fmt.Sprintf("commit %s began but never completed: %d path(s) applied, %d pending", rr.Commit, len(rr.Applied), len(rr.Pending))
		var parts []string
		if len(rr.Applied) > 0 {
			parts = append(parts, "applied: "+strings.Join(rr.Applied, ", "))
		}
		if len(rr.Pending) > 0 {
			parts = append(parts, "pending: "+strings.Join(rr.Pending, ", "))
		}
		if len(parts) > 0 {
			detail += " (" + strings.Join(parts, "; ") + ")"
		}
		if rr.Fixable {
			return doctorCheck{
				Name:   name,
				Detail: detail,
				Remedy: fmt.Sprintf("every pending path's content is in %s, so commit %s can be rolled forward from the CAS — finish it by hand; lw doctor does not repair an apply", objectsRel, rr.Commit),
			}
		}
		return doctorCheck{
			Name:   name,
			Detail: detail,
			Remedy: fmt.Sprintf("a pending path has no object in %s and cannot be rolled forward — restore %s from a backup", objectsRel, stateRel),
		}
	case rr.Unmoved != "":
		return doctorCheck{
			Name:   name,
			Detail: fmt.Sprintf("changeset %s completed its commit but is still in %s — every future changeset will be refused until it moves", rr.Unmoved, stateRel+"/changesets/open"),
			Remedy: fmt.Sprintf("move %s/changesets/open/%s to %s/changesets/committed/%s by hand; the commit itself is already complete", stateRel, rr.Unmoved, stateRel, rr.Unmoved),
		}
	}
	return doctorCheck{Name: name, OK: true, Detail: "no interrupted apply"}
}

// checkLock reports the state of .llmwiki/lock: absent, held by a live
// process, or stale. Only a stale lock fails — a live one is another
// process's commit in progress.
func checkLock(root string) doctorCheck {
	const name = "lock"
	data, err := os.ReadFile(filepath.Join(root, stateDirName, lockFileName))
	if errors.Is(err, os.ErrNotExist) {
		return doctorCheck{Name: name, OK: true, Detail: "no lock held"}
	}
	if err != nil {
		return doctorCheck{
			Name:   name,
			Detail: fmt.Sprintf("%s could not be read: %v", lockRel, err),
			Remedy: "check the permissions on " + lockRel,
		}
	}
	stale := func(detail string) doctorCheck {
		return doctorCheck{
			Name:   name,
			Detail: fmt.Sprintf("stale lock: %s (%s)", detail, lockRel),
			Remedy: "run lw doctor --unlock to remove it",
		}
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return stale("the file is empty")
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return stale(fmt.Sprintf("unparsable pid %q", fields[0]))
	}
	if !pidAlive(pid) {
		return stale(fmt.Sprintf("pid %d recorded there is not running", pid))
	}
	since := ""
	if len(fields) > 1 {
		since = " since " + fields[1]
	}
	return doctorCheck{Name: name, OK: true, Detail: fmt.Sprintf("lock held by live pid %d%s", pid, since)}
}

// pidAlive reports whether pid is running, by the same rule internal/stage's
// lock uses (backbone §5.2, MASTER §9 D-AS): Kill(pid, 0) returning nil or
// EPERM means alive — EPERM is a live process owned by another user — and
// anything else, notably ESRCH, means stale.
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// checkConfig reports whether the configuration loaded and whether the API
// key reference resolved — as `env:NAME (set|missing)`, never as the value
// itself. A literal key passes: it works, and the detail says why env: is
// preferred.
func checkConfig(cfg *config.Config, err error) doctorCheck {
	const name = "config"
	if err != nil {
		return doctorCheck{
			Name:   name,
			Detail: err.Error(),
			Remedy: "run lw config to inspect or repair the configuration file",
		}
	}
	about := fmt.Sprintf(" · model %s @ %s", cfg.LLM.Model, cfg.LLM.BaseURL)
	switch ref := cfg.LLM.APIKey; {
	case ref == "":
		return doctorCheck{
			Name:   name,
			Detail: "no api_key configured" + about,
			Remedy: "set it to an environment reference, e.g. lw config set llm.api_key env:LW_API_KEY, and export that variable",
		}
	case strings.HasPrefix(ref, "env:"):
		envName := strings.TrimPrefix(ref, "env:")
		if v, ok := os.LookupEnv(envName); !ok || v == "" {
			return doctorCheck{
				Name:   name,
				Detail: fmt.Sprintf("api_key %s (missing)%s", ref, about),
				Remedy: fmt.Sprintf("export %s, or point llm.api_key at another variable with lw config set llm.api_key env:NAME", envName),
			}
		}
		return doctorCheck{Name: name, OK: true, Detail: fmt.Sprintf("api_key %s (set)%s", ref, about)}
	case strings.HasPrefix(ref, "keyring:"):
		return doctorCheck{
			Name:   name,
			Detail: fmt.Sprintf("api_key %s: keyring references are not supported yet%s", ref, about),
			Remedy: "store the key in the environment and reference it, e.g. lw config set llm.api_key env:NAME",
		}
	default:
		return doctorCheck{
			Name:   name,
			OK:     true,
			Detail: fmt.Sprintf("api_key literal (set)%s — prefer env:NAME so the value is never stored in the config file", about),
		}
	}
}

// checkWeb reports the [web] lookup configuration (010 contract §4, C-1001)
// as its own `web:` line: the provider name and the api_key reference —
// `env:NAME (set|missing)`, or the literal/keyring forms as the config check
// words them — never the key value itself. No configuration, no line: with
// [web] absent or api_key empty, web.search is simply not offered and there
// is nothing to report, so the check is nil rather than skipped. An unknown
// provider, or a named key that cannot resolve, is a warn, not a failure:
// the vault itself is healthy, and what is lost is one optional verb, with
// the fix spelled out under the warning line.
func checkWeb(cfg *config.Config, cfgErr error) *doctorCheck {
	const name = "web"
	if cfgErr != nil {
		// The config check already reports the load failure with its
		// remedy; whether a [web] table was meant to be there is not
		// knowable from bytes that did not parse, so no web line.
		return nil
	}
	if cfg.Web.Provider != "tavily" {
		return &doctorCheck{
			Name:   name,
			OK:     true,
			Warn:   true,
			Detail: fmt.Sprintf("unknown provider %q", cfg.Web.Provider),
			Remedy: "tavily is the only built-in provider — set it with lw config set web.provider tavily",
		}
	}
	about := "provider " + cfg.Web.Provider
	switch ref := cfg.Web.APIKey; {
	case ref == "":
		return nil
	case strings.HasPrefix(ref, "env:"):
		envName := strings.TrimPrefix(ref, "env:")
		if v, ok := os.LookupEnv(envName); !ok || v == "" {
			remedy := fmt.Sprintf("export %s, or point web.api_key at another variable with lw config set web.api_key env:NAME", envName)
			if envName == "" {
				// An `env:` reference with no name has nothing to export;
				// name the broken reference rather than rendering `export ,`.
				remedy = fmt.Sprintf("%q names no environment variable — point web.api_key at a set one: lw config set web.api_key env:NAME", ref)
			}
			return &doctorCheck{
				Name:   name,
				OK:     true,
				Warn:   true,
				Detail: fmt.Sprintf("%s, %s (missing)", about, ref),
				Remedy: remedy,
			}
		}
		return &doctorCheck{Name: name, OK: true, Detail: fmt.Sprintf("%s, %s (set)", about, ref)}
	case strings.HasPrefix(ref, "keyring:"):
		return &doctorCheck{
			Name:   name,
			OK:     true,
			Warn:   true,
			Detail: fmt.Sprintf("%s, %s: keyring references are not supported yet", about, ref),
			Remedy: "store the key in the environment and reference it, e.g. lw config set web.api_key env:NAME",
		}
	default:
		return &doctorCheck{
			Name: name,
			OK:   true,
			// ref itself IS the reference the file holds; the resolved
			// value is never printed.
			Detail: fmt.Sprintf("%s, api_key literal (set)", about),
		}
	}
}

// checkPDFExtractor reports the PDF extraction backend (007 F.W7): whether
// the sidecar is installed, its version against the one lw is tested with,
// and the extraction cache's size. It follows checkWeb's nil discipline — a
// config that did not load gets no line here, because checkConfig above
// already reports the load failure with its remedy — and it is a warn,
// never a failure, unless the version probe itself errors: a missing or
// merely newer sidecar turns PDF ingest off or makes it a variant, and the
// vault itself stays healthy. The probe runs only when o.probe is set, so
// an unprobing doctor makes no claim about the sidecar beyond LookPath.
func checkPDFExtractor(ctx context.Context, cfg *config.Config, cfgErr error, root string, probe bool) *doctorCheck {
	const name = "pdf extractor"
	if cfgErr != nil {
		return nil
	}
	argv := cfg.Extract.Argv()
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return &doctorCheck{
			Name:   name,
			OK:     true,
			Warn:   true,
			Detail: "not installed — PDF ingest is off; other sources are unaffected",
			Remedy: fmt.Sprintf("uv tool install docling==2.130.0, or lw config set extract.command %q", argv[0]),
		}
	}
	if !probe {
		return &doctorCheck{Name: name, OK: true, Detail: path + " (version not probed)"}
	}

	pctx, cancel := context.WithTimeout(ctx, doctorProbeTimeout)
	defer cancel()
	version, err := probeExtractorVersion(pctx, extract.PDFConfig{Command: argv, Timeout: cfg.Extract.TimeoutDuration()})
	if err != nil {
		return &doctorCheck{
			Name:   name,
			Detail: fmt.Sprintf("%s --version failed: %v", argv[0], err),
			Remedy: "reinstall: uv tool install --force docling==2.130.0",
		}
	}
	if version != doclingTestedVersion {
		return &doctorCheck{
			Name:   name,
			OK:     true,
			Warn:   true,
			Detail: fmt.Sprintf("docling %s at %s", version, path),
			Remedy: "lw is tested with Docling 2.130.0; another version may extract differently (a new raw source, not a duplicate)",
		}
	}

	// The cache is advisory (007 T2 degrades every cache failure), so a
	// Stat error is reported inside an OK row rather than failing the
	// check — the sidecar itself answered and that is what this row vouches
	// for.
	detail := fmt.Sprintf("docling %s at %s", version, path)
	if entries, bytes, serr := cache.Stat(filepath.Join(root, cacheRel)); serr != nil {
		detail += fmt.Sprintf("; cache unavailable: %v", serr)
	} else {
		detail += fmt.Sprintf("; cache %d entries, %d KB", entries, ingestKB(int(bytes)))
	}
	return &doctorCheck{Name: name, OK: true, Detail: detail}
}

// checkLLMBudget warns when llm.max_tokens sits below
// config.MinRecommendedMaxTokens (008 contract §6): thinking-mode models
// spend most of a round's budget reasoning — a live GLM ingest measured
// 5,247 reasoning tokens in one round (008 W0) — so a budget under the
// floor is how a run ends truncated with nothing proposed. Like the
// tracked-state check, a warn keeps OK true: doctor's exit is a pass, the
// condition is just made visible with its fix.
func checkLLMBudget(cfg *config.Config, cfgErr error) doctorCheck {
	const name = "llm budget"
	if cfgErr != nil {
		// The config check above already reports the load failure with its
		// remedy; this check has nothing to measure without a config.
		return doctorCheck{Name: name, OK: true, Skipped: true, Detail: "skipped: the configuration did not load"}
	}
	if cfg.LLM.MaxTokens < config.MinRecommendedMaxTokens {
		return doctorCheck{
			Name: name,
			OK:   true,
			Warn: true,
			Detail: fmt.Sprintf("llm.max_tokens = %d is below %d; thinking models can spend a whole round reasoning and stop before acting",
				cfg.LLM.MaxTokens, config.MinRecommendedMaxTokens),
			Remedy: fmt.Sprintf("lw config set llm.max_tokens %d", config.Default().LLM.MaxTokens),
		}
	}
	return doctorCheck{Name: name, OK: true, Detail: fmt.Sprintf("llm.max_tokens = %d", cfg.LLM.MaxTokens)}
}

// checkProvider reports whether the configured endpoint is reachable and
// actually returns tool calls — the failure /docs/design.md §11.2 wants visible at
// config time rather than mid-ingest. It is skipped, not failed, when no key
// resolves: the config check above already reports that, and a probe without
// credentials can only fail misleadingly.
func checkProvider(ctx context.Context, cfg *config.Config) doctorCheck {
	const name = "provider"
	apiKey, err := cfg.ResolveAPIKey()
	if err != nil {
		return doctorCheck{Name: name, OK: true, Skipped: true, Detail: "skipped: api_key did not resolve: " + err.Error()}
	}
	if apiKey == "" {
		return doctorCheck{Name: name, OK: true, Skipped: true, Detail: "skipped: llm.api_key is empty"}
	}

	pctx, cancel := context.WithTimeout(ctx, doctorProbeTimeout)
	defer cancel()
	res := probeProvider(pctx, cfg)

	switch {
	case res.Err != nil:
		return doctorCheck{
			Name:   name,
			Detail: fmt.Sprintf("%s at %s is unreachable: %v", cfg.LLM.Model, cfg.LLM.BaseURL, res.Err),
			Remedy: "check llm.base_url and the API key (lw config), and that the endpoint is reachable from this machine",
		}
	case !res.Reachable:
		return doctorCheck{
			Name:   name,
			Detail: fmt.Sprintf("%s at %s returned no response", cfg.LLM.Model, cfg.LLM.BaseURL),
			Remedy: "check llm.base_url and the API key (lw config), and that the endpoint is reachable from this machine",
		}
	case !res.ToolCalling:
		return doctorCheck{
			Name:   name,
			Detail: fmt.Sprintf("%s is reachable but returned no tool call", res.Model),
			Remedy: "tool calling is required for ingest, query and the TUI ask pane — configure a model that returns tool_calls, e.g. lw config set llm.model <name>",
		}
	}
	return doctorCheck{
		Name:   name,
		OK:     true,
		Detail: fmt.Sprintf("%s reachable, tool calling ok (%dms)", res.Model, res.Latency.Milliseconds()),
	}
}
