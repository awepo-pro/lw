package tools

// page_hygiene.go holds 050's two write-time repairs for the lint warns an
// ingest turn left on the pages it staged (measured on the 049 baseline:
// 23 duplicate-section and 23 cite-source among 51 new warns in 27 runs).
// Both are the model's input mistake corrected where the mistake is
// unambiguous, and both say so on the tool result so the model learns the
// shape instead of silently relying on the repair:
//
//   - an echoed section heading: append_section / replace_section content
//     that opens with the target section's own heading line, which the model
//     sends because it confuses a section's content with the section;
//   - sources that drift from the body: a body that cites a raw/ source the
//     frontmatter sources: list never gained, because patch_page cannot edit
//     frontmatter and the model never listed it on create_page.

import (
	"strings"

	"github.com/awepo-pro/lw/internal/cite"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/vault"
)

// stripEchoedHeading drops a leading echo of sec's own heading from content
// (050 D1). The echo is the first non-blank line, when it is an ATX heading
// of sec's level whose text equals sec's heading text after TrimSpace and
// case folding; that line and the blank lines right after it go, and so do
// any blank lines before it. line is the dropped heading line, trimmed, for
// the note. Anything else — a heading of another level or text, a first
// line that is prose — comes back untouched with ok false: only a heading
// that would duplicate the section is an echo, and a different heading is
// the model's content.
//
// sec is the section as page.Section resolved it, never the section
// argument string: "## related" asked for and "## Related" found must
// compare against the found one, the heading that would be duplicated.
// The caller runs the 043 guards on the stripped content, so a shrink or a
// lost subsection is measured on what will actually be written.
func stripEchoedHeading(sec vault.Section, content string) (stripped, line string, ok bool) {
	lines := strings.Split(content, "\n")
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i == len(lines) {
		return content, "", false
	}
	level, text, isHeading := atxHeading(lines[i])
	if !isHeading || level != sec.Level || !strings.EqualFold(text, strings.TrimSpace(sec.Title)) {
		return content, "", false
	}
	j := i + 1
	for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
		j++
	}
	return strings.Join(lines[j:], "\n"), strings.TrimSpace(lines[i]), true
}

// atxHeading parses line as a CommonMark ATX heading: at most three spaces
// of indentation, one to six "#", then a space or tab (a "#" run glued to
// its text is a paragraph, not a heading). text is what follows, trimmed.
// The closing "#" sequence CommonMark allows is not stripped: vault's own
// Section.Title keeps it too, so the two sides compare alike.
func atxHeading(line string) (level int, text string, ok bool) {
	line = strings.TrimRight(line, "\r")
	indent := 0
	for indent < len(line) && indent < 3 && line[indent] == ' ' {
		indent++
	}
	rest := line[indent:]
	for level < len(rest) && rest[level] == '#' {
		level++
	}
	if level == 0 || level > 6 {
		return 0, "", false
	}
	rest = rest[level:]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return 0, "", false
	}
	return level, strings.TrimSpace(rest), true
}

// echoNote is the frozen result line of a call whose content lost its
// echoed heading (050 D1). The heading goes in verbatim between plain
// quotes, not %q: a heading with a quote or a backslash must read as the
// model wrote it.
func echoNote(line, op string) string {
	return `note: dropped the leading "` + line + `" from content; ` + op + ` content is the section body without its heading.`
}

// citedSources returns listed with every raw/ source the body cites that
// listed lacks and that resolves appended, in first-citation order, and
// the sources it appended (050 D2). cite.Scan decides what the body cites —
// fenced code and inline code excluded, a page suffix ignored, exactly the
// scan the cite-source lint check runs — so a source this adds is a source
// that check would have flagged.
//
// A source resolves when the committed vault holds it or a live
// ingest_source op of the open changeset stages it: an ingest turn cites
// sources it has only just staged, which is the whole case. One that
// resolves nowhere is left alone — existing staging checks or lint report
// it, and listing a path nobody has would hide the broken cite. Nothing is
// ever removed from listed, and listed itself is never written through: with
// nothing to add it is returned as it came, so a caller keeps today's bytes,
// and otherwise the result is a fresh slice (listed may be a committed
// page's own FM.Sources).
func citedSources(d Deps, listed []string, body string) (merged, added []string) {
	have := make(map[string]bool, len(listed))
	for _, s := range listed {
		have[s] = true
	}
	var staged map[string]bool // built on the first raw/ cite the vault does not hold
	for _, c := range cite.Scan(body) {
		src := c.Source
		if !strings.HasPrefix(src, "raw/") || have[src] {
			continue
		}
		have[src] = true // a source cited twice is decided once, resolved or not
		if d.Vault != nil {
			if _, ok := d.Vault.RawSource(src); ok {
				added = append(added, src)
				continue
			}
		}
		if staged == nil {
			staged = stagedIngestPaths(d)
		}
		if staged[src] {
			added = append(added, src)
		}
	}
	if len(added) == 0 {
		return listed, nil
	}
	merged = make([]string, 0, len(listed)+len(added))
	merged = append(merged, listed...)
	merged = append(merged, added...)
	return merged, added
}

// stagedIngestPaths is the set of raw paths the open changeset's live
// ingest_source ops write. No engine, no open changeset, or a changeset that
// cannot be read yields the empty set: a source that cannot be proven staged
// does not resolve.
func stagedIngestPaths(d Deps) map[string]bool {
	out := map[string]bool{}
	if d.Engine == nil {
		return out
	}
	cs, err := d.Engine.Current()
	if err != nil {
		return out
	}
	for _, op := range cs.Live() {
		if op.Kind == stage.OpIngestSource {
			out[op.Path] = true
		}
	}
	return out
}

// sourcesNote is the frozen result line of a call that added sources to
// sources: (050 D2).
func sourcesNote(added []string) string {
	return "note: added " + strings.Join(added, ", ") + " to sources: (the body cites them)."
}

// withNotes returns res with each note on a line of its own after its
// content, in the order given — the repair a call made is the last thing the
// model reads. Only a call that staged gets here: a refusal carries its own
// text and no note.
func withNotes(res Result, notes []string) Result {
	for _, n := range notes {
		res.Content += "\n" + n
	}
	return res
}
