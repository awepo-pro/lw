// deletemark.go decides when Review warns that a patch_page op deletes text
// (047 S3, the Review half of 043's "Not done"). 043's guards refuse the
// losses a new call would cause, but they cannot see an op already staged: a
// reviewer looking at op8 of the session-36 changeset saw three paragraphs
// of '-' lines among the '+' lines and no sign that, unlike every other
// '-' in the block, nothing put them back. This file turns that into a
// number — how many lines, how many bytes the op takes out and does not
// re-add — so the Diff panel can say it in one line under the op's header
// and the Ops row can carry a `−` marker. Pure functions over the op and
// its OpDiff windows; nothing here reads the engine.
package review

import (
	"fmt"
	"strings"

	"github.com/awepo-pro/lw/internal/stage"
)

// The two ways an op earns the mark: it removes at least this many lines
// of text, or at least this many bytes of it. Either is enough — three
// one-line bullets and one dense 200-byte paragraph are both losses a
// reviewer should be told about, while a two-line, 100-byte tidy-up is the
// routine edit the mark would drown in noise.
const (
	deleteMarkLines = 3
	deleteMarkBytes = 200
)

// deletedText counts what op's windows remove and never put back: the '-'
// lines whose trimmed text is not empty and equals no '+' line's trimmed
// text anywhere in the same op, and the sum of those lines' raw lengths. The
// test is "re-added", not "adjacent to an add": a paragraph moved to another
// window, re-indented or re-wrapped by whitespace alone is not a loss, which
// is why a rename-only patch stays unmarked however large it is.
//
// Only windows the reviewer has not dropped take part. A dropped window will
// not land, so it deletes nothing and re-adds nothing — the same rule
// addedTexts applies to the Preview's change gutter — and dropping the hunk
// that deletes is exactly how a reviewer clears the mark. Chained patches
// count too: their windows carry no hunk id (the 030 debt) but are still
// the op's windows. marked is true only for a patch_page op that is not
// itself presented as dropped and crosses a threshold.
func deletedText(op stage.Op, files []stage.FileOpDiff) (lines, bytes int, marked bool) {
	if op.Kind != stage.OpPatchPage || opIsDropped(op, files) {
		return 0, 0, false
	}

	readded := make(map[string]struct{})
	for _, f := range files {
		for _, w := range f.Hunks {
			if w.Dropped {
				continue
			}
			for _, dl := range w.Lines {
				if dl.Kind == '+' {
					readded[strings.TrimSpace(dl.Text)] = struct{}{}
				}
			}
		}
	}
	for _, f := range files {
		for _, w := range f.Hunks {
			if w.Dropped {
				continue
			}
			for _, dl := range w.Lines {
				if dl.Kind != '-' {
					continue
				}
				text := strings.TrimSpace(dl.Text)
				if text == "" {
					continue
				}
				if _, ok := readded[text]; ok {
					continue
				}
				lines++
				bytes += len(dl.Text)
			}
		}
	}
	return lines, bytes, lines >= deleteMarkLines || bytes >= deleteMarkBytes
}

// deleteMarkText is the Detail line, fact first so a narrow panel's clip
// eats the explanation and never the count: "deletes 3 lines, 412 B — text
// this op does not re-add". The size is stage.HumanSize, the rendering the
// ingest original line uses, so one review screen never writes the same
// quantity two ways (054: this package kept a same-output copy before).
func deleteMarkText(lines, bytes int) string {
	return fmt.Sprintf("deletes %d line%s, %s — text this op does not re-add", lines, plural(lines), stage.HumanSize(bytes))
}
