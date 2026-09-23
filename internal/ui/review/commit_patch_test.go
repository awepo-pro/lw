// commit_patch_test.go is 029 T1's F3 evidence. U5: a changeset of one
// ingest_source plus chained patch_page ops is page work — the first C
// commits it, instead of arming the raw-only two-press warning the old
// create-only count produced. And because the footer clips status at w-10
// (internal/ui/frame.go:318), the raw-only text leads with the action
// (A-029-2) so a two-press commit instruction survives the clip.
package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// tilelangRawPath is the raw path from the live 029 reproduction — long
// enough that any footer text leading with anything but the action is
// clipped into uselessness on an 80-column terminal.
const tilelangRawPath = "raw/papers/tilelang-a-composable-tiled-programming-model-for-ai-systemsthanks-mathsection-equal-contributions.md"

// kvCachePatchOp builds one patch_page op on the fixture's kv-cache page in
// the harness_test.go appendTwoHunkPatch idiom: Before is base's sha, and
// content carries oldLine rewritten to newLine. For a chained second op the
// caller passes the first op's After as before (the 020 chain) and content
// derived from the first op's post-image.
func kvCachePatchOp(path, before string, content []byte, oldLine, newLine string) stage.Op {
	return stage.Op{
		Kind:      stage.OpPatchPage,
		Path:      path,
		Section:   "## Related",
		Before:    before,
		Content:   content,
		Rationale: "029 U5: chained page edits beside one ingest",
		Hunks:     []stage.Hunk{{ID: "h1", Path: path, Del: []string{oldLine}, Add: []string{newLine}}},
	}
}

func TestPatchChangesetCommitsFirstC(t *testing.T) {
	const (
		kvCachePath = "wiki/concepts/kv-cache.md"
		oldFlash    = "- [[flash-attention]] — a kernel design that reduces the memory-bandwidth cost"
		midFlash    = "- [[flash-attention]] — a kernel design that reduces the memory-bandwidth cost, sharpened"
		newFlash    = "- [[flash-attention]] — a kernel design that reduces the memory-bandwidth cost, sharpened twice"
	)

	t.Run("ingest_plus_chained_patches", func(t *testing.T) {
		d, e, root := newTestDeps(t, "minimal")
		if _, err := e.OpenChangeset("ingest plus chained patches", stage.Author{Kind: "agent", Model: "test"}); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		appendRawIngest(t, e, tilelangRawPath, "# TileLang\n\nA tiled programming model.\n")

		page, ok := e.Vault().Page(kvCachePath)
		if !ok {
			t.Fatalf("fixture missing %s", kvCachePath)
		}
		if !strings.Contains(page.Body, oldFlash) {
			t.Fatalf("fixture body does not contain the hunk's old line:\n%s", page.Body)
		}

		// Op A: the first dependent edit, Before = the committed sha
		// (harness_test.go:140-165 idiom).
		aContent := []byte(strings.Replace(string(page.Serialize()), oldFlash, midFlash, 1))
		opA, err := e.Append(kvCachePatchOp(page.Path, page.SHA256(), aContent, oldFlash, midFlash))
		if err != nil {
			t.Fatalf("Append patch A: %v", err)
		}

		// Op B chains on A (the 020 chain): its Before is A's After, read
		// back from the open changeset, and its content derives from A's
		// projected post-image.
		bContent := []byte(strings.Replace(string(aContent), midFlash, newFlash, 1))
		cs, err := e.Current()
		if err != nil {
			t.Fatalf("Current after patch A: %v", err)
		}
		aOp, found := cs.Op(opA)
		if !found {
			t.Fatalf("changeset lost %s", opA)
		}
		if _, err := e.Append(kvCachePatchOp(page.Path, aOp.After, bContent, midFlash, newFlash)); err != nil {
			t.Fatalf("Append patch B (chained Before): %v", err)
		}
		m := initModel(t, d)

		m = send(t, m, keyPress('C')) // the FIRST C: page work commits outright

		msg, level := statusOf(t, m)
		if !strings.HasPrefix(msg, "committed ") || level != ui.StatusGood {
			t.Errorf("Status after first C = (%q, %v), want \"committed <id>\" at StatusGood", msg, level)
		}
		if strings.Contains(msg, "raw source(s) only") {
			t.Errorf("status carries the raw-only warning for ingest+patches: %q", msg)
		}

		// The commit actually landed, and landed op B's bytes.
		onDisk, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(page.Path)))
		if err != nil {
			t.Fatalf("read committed page: %v", err)
		}
		if string(onDisk) != string(bContent) {
			t.Errorf("committed page on disk is not op B's post-image:\n%s", onDisk)
		}
	})

	t.Run("clip_keeps_the_action", func(t *testing.T) {
		d, e, _ := newTestDeps(t, "minimal")
		if _, err := e.OpenChangeset("raw only, clipped footer", stage.Author{Kind: "agent", Model: "test"}); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		appendRawIngest(t, e, tilelangRawPath, "# TileLang\n\nRaw-only, and a very long path.\n")
		m := initModel(t, d)

		m = send(t, m, keyPress('C'))

		msg, _ := statusOf(t, m)
		// 70 = an 80-column terminal's w-10, the frame's footer clip
		// (internal/ui/frame.go:318). The action must lead the text.
		if got := ui.Clip(msg, 70); !strings.HasPrefix(got, "press C again to commit") {
			t.Errorf("Clip(status, 70) = %q, want it to keep the two-press action", got)
		}
	})

	t.Run("dropped_patch_is_raw_only", func(t *testing.T) {
		d, e, root := newTestDeps(t, "minimal")
		if _, err := e.OpenChangeset("ingest plus a dropped patch", stage.Author{Kind: "agent", Model: "test"}); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		appendRawIngest(t, e, tilelangRawPath, "# TileLang\n\nIts page edit gets dropped below.\n")
		page, ok := e.Vault().Page(kvCachePath)
		if !ok {
			t.Fatalf("fixture missing %s", kvCachePath)
		}
		content := []byte(strings.Replace(string(page.Serialize()), oldFlash, midFlash, 1))
		opID, err := e.Append(kvCachePatchOp(page.Path, page.SHA256(), content, oldFlash, midFlash))
		if err != nil {
			t.Fatalf("Append patch: %v", err)
		}
		if err := e.DropOp(opID); err != nil {
			t.Fatalf("DropOp: %v", err)
		}
		m := initModel(t, d)

		m = send(t, m, keyPress('C'))

		// With the patch gone, the changeset IS raw-only: warn with
		// A-029-2's text, commit nothing.
		want := "press C again to commit — raw source(s) only, no page changes: " + tilelangRawPath
		if msg, level := statusOf(t, m); msg != want || level != ui.StatusWarn {
			t.Errorf("Status after C = (%q, %v), want (%q, StatusWarn)", msg, level, want)
		}
		if journalHasCommitBegin(t, root) {
			t.Error("journal recorded a commit_begin — the raw-only warning must not commit")
		}
	})
}
