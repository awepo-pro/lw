// rows.go renders one journal event as a row of the Events panel
// (s2-screens.md T10): time `YYYY-MM-DD HH:MM` (muted, always UTC — MASTER
// §8 C29), two spaces, the kind glyph, a space, the kind (muted, padded to
// the longest in the list, max 16), two spaces, the summary (fg, clipped
// by the panel to the inner width).
package logview

import (
	"strings"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// timeLayout is the row's time format (T10), rendered in UTC — never
// time.Local, so a golden rendered on a UTC CI machine and on a UTC+8
// laptop are byte-identical (MASTER §8 C29).
const timeLayout = "2006-01-02 15:04"

// maxKindWidth caps the kind column (T10: "padded to the longest, max
// 16"); a longer kind is clipped to the column with ui.Clip.
const maxKindWidth = 16

// kindWidth returns the Events panel's kind-column width: the longest kind
// among events, capped at maxKindWidth. It is computed over the whole
// filtered list, not the visible window, so the column does not reshuffle
// while the reviewer scrolls.
func kindWidth(events []stage.Event) int {
	longest := 0
	for _, ev := range events {
		if n := len(ev.Kind); n > longest {
			longest = n
		}
	}
	return min(longest, maxKindWidth)
}

// eventGlyph is the row's kind glyph and its colour (T10): `●` commit
// good · `✗` rejected bad · `↺` revert warn · `·` other muted.
func eventGlyph(ev stage.Event, t ui.Theme) (string, lipgloss.Style) {
	switch ev.Kind {
	case stage.EvCommitBegin, stage.EvCommitEnd:
		return "●", t.Good
	case stage.EvChangesetRejected:
		return "✗", t.Bad
	case stage.EvReverted:
		return "↺", t.Warn
	default:
		return "·", t.Muted
	}
}

// eventRow renders one journal event as one styled panel line at the given
// kind-column width.
func eventRow(ev stage.Event, t ui.Theme, kindW int) string {
	glyph, glyphStyle := eventGlyph(ev, t)
	kind := t.Muted.Render(ui.Pad(ui.Clip(string(ev.Kind), kindW), kindW))
	return t.Muted.Render(ev.TS.UTC().Format(timeLayout)) + "  " +
		glyphStyle.Render(glyph) + " " + kind + "  " +
		t.Fg.Render(eventSummary(ev))
}

// eventSummary composes a row's summary: whichever of the event's message,
// op, hunk, commit and paths it carries, most-informative first
// (backbone §5.7's Event fields). The kind column and the `agent`/`human`
// filters already say what kind of thing happened and to whom, so the
// summary carries only the payload.
func eventSummary(ev stage.Event) string {
	var parts []string
	if ev.Message != "" {
		parts = append(parts, ev.Message)
	}
	if ev.Commit != "" && ev.Message == "" {
		parts = append(parts, "commit "+ev.Commit)
	}
	if ev.Op != "" {
		parts = append(parts, ev.Op)
	}
	if ev.Hunk != "" {
		parts = append(parts, ev.Hunk)
	}
	if len(ev.Paths) > 0 {
		parts = append(parts, strings.Join(ev.Paths, ", "))
	}
	if len(parts) == 0 {
		return string(ev.Kind)
	}
	return strings.Join(parts, " · ")
}

// emptyListRow is the one content row an empty journal (or an empty
// filter result) shows.
func emptyListRow(t ui.Theme) string {
	return t.Faint.Render("no matching events")
}

// loadingRow is the one content row shown before the first query returns.
func loadingRow(t ui.Theme) string {
	return t.Faint.Render("loading journal…")
}

// loadErrRow is the one content row a failed query shows. Rendered
// visibly, never panicking (S4-T2's constructible-headless contract).
func loadErrRow(err error, t ui.Theme) string {
	return t.Bad.Render("log: " + err.Error())
}

// eventRows renders every event in events at kindW. Kept next to kindWidth
// so the column and its users cannot drift apart.
func eventRows(events []stage.Event, t ui.Theme, kindW int) []string {
	lines := make([]string, len(events))
	for i, ev := range events {
		lines[i] = eventRow(ev, t, kindW)
	}
	return lines
}
