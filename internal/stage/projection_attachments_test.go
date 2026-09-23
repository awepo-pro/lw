package stage

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/vault"
)

// The 033 scaling tests for the projection: committed attachment bytes
// never enter the projected tree, and no staging op ever reads them — the
// projection resolves attachments to shas through the disk vault's
// persistent cache instead. A 100-PDF vault must cost O(changed files),
// not O(vault bytes), per staging operation.

// newAttachmentVault returns an engine whose vault has three committed
// 256 KiB attachments under raw/papers/, each with a committed raw source
// whose frontmatter declares the correct original pair, plus the three
// attachment paths.
func newAttachmentVault(t *testing.T) (*Engine, string, []string) {
	t.Helper()
	e, dir := newTestEngine(t)
	paths := make([]string, 0, 3)
	for _, name := range []string{"alpha", "beta", "gamma"} {
		blob := make([]byte, 256*1024)
		copy(blob, "%PDF-1.6 "+name+"\n")
		for i := len(name) + 10; i < len(blob); i++ {
			blob[i] = byte('a' + i%26)
		}
		commitAttachmentIngest(t, e, name, blob)
		paths = append(paths, "raw/papers/"+name+".pdf")
	}
	return e, dir, paths
}

// attachmentRawFile renders the whole raw/ file for one committed
// attachment: the 033 frontmatter (original pair included) over a body
// that names the source, so every committed raw hashes differently and
// ingest dedupe never fires between them.
func attachmentRawFile(name string, blob []byte) []byte {
	body := "# " + name + "\n\nThe extracted body of " + name + ".\n"
	pdfPath := "raw/papers/" + name + ".pdf"
	return []byte("---\n" +
		"source_url: https://example.test/" + name + "\n" +
		"ingested: 2026-09-24\n" +
		"sha256: " + vault.BodySHA256(body) + "\n" +
		"original: " + pdfPath + "\n" +
		"original_sha256: " + blobSHA(blob) + "\n" +
		"---\n\n" +
		body)
}

// commitAttachmentIngest stages and commits one ingest_source whose raw
// declares the correct original pair, leaving blob at the original path.
func commitAttachmentIngest(t *testing.T, e *Engine, name string, blob []byte) {
	t.Helper()
	if _, err := e.OpenChangeset("ingest "+name, testAuthor); err != nil {
		t.Fatalf("OpenChangeset %s: %v", name, err)
	}
	if _, err := e.Append(Op{
		Kind:            OpIngestSource,
		Path:            "raw/papers/" + name + ".md",
		Extractor:       "docling/pdf",
		Content:         attachmentRawFile(name, blob),
		OriginalPath:    "raw/papers/" + name + ".pdf",
		Original:        blobSHA(blob),
		OriginalContent: blob,
	}); err != nil {
		t.Fatalf("Append %s: %v", name, err)
	}
	if _, err := e.Commit("commit " + name); err != nil {
		t.Fatalf("Commit %s: %v", name, err)
	}
}

// TestProjectionCarriesNoAttachmentBytes: the projected tree a Checks /
// ProjectedReport run is built from holds NO bytes for any raw/ non-.md
// path — committed or staged — yet the projected lint stays clean under
// src-integrity and Exists reports true for every attachment, exactly as
// when the bytes travelled in the tree.
func TestProjectionCarriesNoAttachmentBytes(t *testing.T) {
	e, _, committed := newAttachmentVault(t)
	livePDF := originalPDFPath
	stageOriginalIngest(t, e)

	c, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	// The same tree + staged-attachment pair computeChecks builds its
	// projection from.
	tree, staged, err := e.project(c.Live())
	if err != nil {
		t.Fatalf("project: %v", err)
	}

	all := append(append([]string{}, committed...), livePDF)
	for _, p := range all {
		if b, ok := tree[p]; ok {
			t.Errorf("projected tree carries %s (%d bytes); attachments must travel as shas, never as bytes", p, len(b))
		}
	}
	for p := range tree {
		if strings.HasPrefix(p, "raw/") && path.Ext(p) != ".md" {
			t.Errorf("projected tree carries attachment bytes at %s (%d bytes)", p, len(tree[p]))
		}
	}

	pv, err := e.openProjection(tree, staged)
	if err != nil {
		t.Fatalf("openProjection: %v", err)
	}

	report := lintProjection(pv)
	for _, f := range report.Findings {
		if f.Check == "src-integrity" {
			t.Errorf("projected lint reports src-integrity without attachment bytes in the tree: %+v", f)
		}
	}

	for _, p := range all {
		if !pv.Exists(p) {
			t.Errorf("projection Exists(%s) = false, want true (committed and staged attachments must both resolve)", p)
		}
	}
}

// TestProjectionDoesNotReadCommittedAttachments: once the persistent
// cache holds a committed attachment's sha, computing Checks over a new
// op must not open that attachment — with the files made unreadable
// (mode 000) the recompute still succeeds and the projected lint stays
// clean under src-integrity.
func TestProjectionDoesNotReadCommittedAttachments(t *testing.T) {
	e, dir, committed := newAttachmentVault(t)

	// Prime the cache: one hash per committed attachment, so every later
	// lookup can be answered from stat alone.
	for _, p := range committed {
		if _, err := e.Vault().AttachmentSHA256(p); err != nil {
			t.Fatalf("prime cache for %s: %v", p, err)
		}
	}

	asRoot := os.Geteuid() == 0
	for _, p := range committed {
		if err := os.Chmod(filepath.Join(dir, filepath.FromSlash(p)), 0o000); err != nil {
			t.Fatalf("chmod 000 %s: %v", p, err)
		}
	}
	if asRoot {
		t.Log("running as root; chmod 000 cannot make the attachments unreadable, the no-read assertion is vacuous here")
	}

	// A new live ingest forces Append's Checks recompute over the whole
	// projection; no committed attachment may be opened on the way.
	stageOriginalIngest(t, e)

	report, err := e.ProjectedReport()
	if err != nil {
		t.Fatalf("ProjectedReport: %v", err)
	}
	for _, f := range report.Findings {
		if f.Check == "src-integrity" {
			t.Errorf("projected lint reports src-integrity with unreadable committed attachments: %+v", f)
		}
	}
}

// TestDroppedOpWritesNoAttachment pins the dropped-op half of applyOp's
// contract: an op marked StateDropped writes NOTHING into the projection —
// no tree bytes, no atts sha — so an original whose ingest op was dropped
// does not resolve in the projected vault, exactly as if the op had never
// been staged. The committed vault is untouched by a drop, so the path
// must answer fs.ErrNotExist there too, via the disk-vault delegate.
func TestDroppedOpWritesNoAttachment(t *testing.T) {
	e, _ := newTestEngine(t)
	id := stageOriginalIngest(t, e)
	if err := e.DropOp(id); err != nil {
		t.Fatalf("DropOp: %v", err)
	}

	c, err := e.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	// project c.Ops — INCLUDING the dropped one — not c.Live(): the point
	// is that applyOp's dropped guard writes nothing even when handed the
	// op directly.
	tree, atts, err := e.project(c.Ops)
	if err != nil {
		t.Fatalf("project: %v", err)
	}

	if sha, ok := atts[originalPDFPath]; ok {
		t.Errorf("dropped op staged a sha for %s in the projection (%s); dropped ops write nothing", originalPDFPath, sha)
	}
	for _, p := range []string{originalPDFPath, originalRawPath} {
		if b, ok := tree[p]; ok {
			t.Errorf("dropped op wrote %s into the projected tree (%d bytes); dropped ops write nothing", p, len(b))
		}
	}

	pv, err := e.openProjection(tree, atts)
	if err != nil {
		t.Fatalf("openProjection: %v", err)
	}
	if pv.Exists(originalPDFPath) {
		t.Errorf("projection Exists(%s) = true after the ingest op was dropped; a dropped op's original must not resolve", originalPDFPath)
	}
	for _, f := range lintProjection(pv).Findings {
		if f.Check == "src-integrity" {
			t.Errorf("projected lint reports src-integrity after the op was dropped (no raw source may remain to name the original): %+v", f)
		}
	}

	// The committed disk vault never saw the original either.
	if _, err := e.Vault().AttachmentSHA256(originalPDFPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("disk vault AttachmentSHA256 for the never-committed original = %v, want fs.ErrNotExist", err)
	}
}
