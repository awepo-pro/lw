// original_line_test.go is 033 T3's review-side evidence: an ingest_source
// op that travels with its original shows the OpDiff's one-line original
// description (`original (<size>, sha256 <12 hex>): raw/…/<name>.pdf`)
// directly under the op's header in the Diff panel — display-only: no
// cursor stop of its own, and no line at all when the op carries no
// original. Every assertion renders the loaded pane's real Detail content
// through a real engine on the minimal fixture.
package review

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/awepo-pro/lw/internal/stage"
)

// The 033 T3 test shape: one raw ingest plus a small fake PDF beside it.
const (
	origLineRawPath = "raw/papers/attention-is-boring.md"
	origLinePdfPath = "raw/papers/attention-is-boring.pdf"
	origLineBody    = "# Attention Is Boring\n\nThe extracted body.\n"
)

// origLineBlob returns a small fake PDF body.
func origLineBlob() []byte {
	blob := make([]byte, 512)
	copy(blob, "%PDF-1.6 fake pdf for the 033 T3 test\n")
	for i := range blob {
		blob[i] = byte('a' + i%26)
	}
	copy(blob, "%PDF-1.6 fake pdf for the 033 T3 test\n")
	return blob
}

// origLineSha is blob's full sha256 hex.
func origLineSha(blob []byte) string {
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:])
}

// origLineRawFile renders the raw source the ingest stages: frontmatter
// whose original pair declares the blob beside it — the pair
// validateIngestSource checks the op fields against.
func origLineRawFile(blob []byte) []byte {
	sum := sha256.Sum256([]byte(origLineBody))
	return []byte(fmt.Sprintf("---\nsource_url: https://example.test/attention-is-boring\ningested: 2026-09-24\nsha256: %s\noriginal: %s\noriginal_sha256: %s\n---\n\n%s",
		hex.EncodeToString(sum[:]), origLinePdfPath, origLineSha(blob), origLineBody))
}

// appendOrigLineIngest opens a changeset on e and appends one ingest_source
// for the raw path. WithOriginal stages the blob through all three original
// fields, the way the tool does; without, it stages a plain raw-only ingest.
// It returns the op id.
func appendOrigLineIngest(t *testing.T, e *stage.Engine, withOriginal bool) string {
	t.Helper()
	if _, err := e.OpenChangeset("033 T3 review original line", stage.Author{Kind: "agent", Model: "test"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	op := stage.Op{
		Kind:      stage.OpIngestSource,
		Path:      origLineRawPath,
		Extractor: "docling/pdf",
		Rationale: "the T3 review line",
		Content:   origLineRawFile(origLineBlob()),
	}
	if withOriginal {
		op.OriginalPath = origLinePdfPath
		op.Original = origLineSha(origLineBlob())
		op.OriginalContent = origLineBlob()
	}
	id, err := e.Append(op)
	if err != nil {
		t.Fatalf("Append ingest (withOriginal=%v): %v", withOriginal, err)
	}
	return id
}

// origLineWant is the exact display text stage.OpDiff must have produced
// for the staged op's original.
func origLineWant(blob []byte) string {
	sha := origLineSha(blob)
	if len(sha) > 12 {
		sha = sha[:12]
	}
	return fmt.Sprintf("original (%d B, sha256 %s): %s", len(blob), sha, origLinePdfPath)
}

// origLineHeader finds the index of the ingest op's header row in the
// plain Detail lines — the only line naming the raw path.
func origLineHeader(t *testing.T, lines []panelLine) int {
	t.Helper()
	for i, l := range lines {
		if strings.Contains(ansi.Strip(l.text), origLineRawPath) {
			return i
		}
	}
	t.Fatalf("Diff panel has no op header naming %s", origLineRawPath)
	return -1
}

func TestReviewShowsOriginalLine(t *testing.T) {
	d, _, _ := newTestDeps(t, "minimal")
	appendOrigLineIngest(t, d.Engine, true)
	m, ok := initModel(t, d).(*Model)
	if !ok {
		t.Fatalf("pane is %T, want *review.Model", m)
	}

	// Wide enough that the line needs no clipping: the styled text must be
	// exactly the OriginalLine in the theme's Muted style — one line, no
	// more.
	const cw = 200
	_, _, lines := m.detailContent(cw)
	// hasChangeset must hold for detailContent to show the Diff at all.
	if !m.hasChangeset {
		t.Fatal("pane loaded without a changeset")
	}
	head := origLineHeader(t, lines)
	if head+1 >= len(lines) {
		t.Fatal("Diff panel has no line after the ingest op's header")
	}
	blob := origLineBlob()
	want := origLineWant(blob)
	if got := ansi.Strip(lines[head+1].text); got != want {
		t.Errorf("line under the op header = %q, want %q", got, want)
	}
	if styled := m.theme.Muted.Render(want); lines[head+1].text != styled {
		t.Errorf("line under the op header is not Muted-styled: got %q, want %q", lines[head+1].text, styled)
	}
}

func TestReviewShowsOriginalLineClipped(t *testing.T) {
	d, _, _ := newTestDeps(t, "minimal")
	appendOrigLineIngest(t, d.Engine, true)
	m, ok := initModel(t, d).(*Model)
	if !ok {
		t.Fatalf("pane is %T, want *review.Model", m)
	}

	// The same line at a panel width narrower than the text: clipped to
	// exactly the content width, never spilling a cell past it.
	const cw = 40
	_, _, lines := m.detailContent(cw)
	head := origLineHeader(t, lines)
	if head+1 >= len(lines) {
		t.Fatal("Diff panel has no line after the ingest op's header")
	}
	got := ansi.Strip(lines[head+1].text)
	if len([]rune(got)) > cw {
		t.Errorf("original line is %d cells at content width %d: %q", len([]rune(got)), cw, got)
	}
	wantHead := "original (512 B, sha256 " + origLineSha(origLineBlob())[:12] + "):"
	if !strings.HasPrefix(got, wantHead) || !strings.HasSuffix(got, "…") {
		t.Errorf("clipped original line lost its shape: %q (want head %q, trailing …)", got, wantHead)
	}
}

func TestReviewOriginalLineAddsNoCursorStop(t *testing.T) {
	blob := origLineBlob()

	// The same ingest twice — with and without the original — must offer
	// the reviewer exactly the same cursor stops: the original line is
	// display-only.
	loadStops := func(t *testing.T, withOriginal bool) int {
		t.Helper()
		d, _, _ := newTestDeps(t, "minimal")
		appendOrigLineIngest(t, d.Engine, withOriginal)
		m, ok := initModel(t, d).(*Model)
		if !ok {
			t.Fatalf("pane is %T, want *review.Model", m)
		}
		if !m.hasChangeset {
			t.Fatal("pane loaded without a changeset")
		}
		// The staged shape is otherwise identical: the op with the original
		// really does carry the display line this test's siblings assert.
		if withOriginal {
			found := false
			for _, fs := range m.opDiffs {
				for _, f := range fs {
					if f.OriginalLine == origLineWant(blob) {
						found = true
					}
				}
			}
			if !found {
				t.Fatalf("OpDiff carries no OriginalLine %q", origLineWant(blob))
			}
		}
		return len(m.stops)
	}

	withStops := loadStops(t, true)
	withoutStops := loadStops(t, false)
	if withStops != withoutStops {
		t.Errorf("ingest with original has %d cursor stops, want the original-less changeset's %d", withStops, withoutStops)
	}
}

// The dropped-op decision, pinned: stage's diff returns no file entries
// for a StateDropped op, so a dropped ingest renders no original line —
// the same block shape as its (absent) windows; `u` restores the op whole.
func TestReviewDroppedIngestShowsNoOriginalLine(t *testing.T) {
	d, _, _ := newTestDeps(t, "minimal")
	id := appendOrigLineIngest(t, d.Engine, true)
	m, ok := initModel(t, d).(*Model)
	if !ok {
		t.Fatalf("pane is %T, want *review.Model", m)
	}
	idx := -1
	for i := range m.ops {
		if m.ops[i].ID == id {
			m.ops[i].State = stage.StateDropped
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("pane has no op %q", id)
	}
	// What the loader holds for a dropped op: OpDiffs returns no entries.
	delete(m.opDiffs, id)

	for i, l := range m.opDiffLines(m.ops[idx], 200, true) {
		if i > 0 && strings.Contains(ansi.Strip(l.text), "original (") {
			t.Fatalf("dropped ingest renders an original line: %q", ansi.Strip(l.text))
		}
	}
	if head := m.opDiffLines(m.ops[idx], 200, true)[0].text; !strings.Contains(ansi.Strip(head), "dropped") {
		t.Fatalf("dropped ingest header lost its dropped note: %q", ansi.Strip(head))
	}
}
