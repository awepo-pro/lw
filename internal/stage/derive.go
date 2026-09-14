// derive.go implements S2-T8's three engine-derived rules (MASTER §9 D-CA,
// mechanism D-CB) that keep a vault the engine produces lint-clean:
//
//	(a) create_page derives an index.md insertion.
//	(b) retract removes nothing from index.md (no code — see the note at
//	    apply.go's OpRetract arm in planOp).
//	(c) split_page leaves a disambiguation stub at its source path instead
//	    of moving it out to .llmwiki/tombstones/.
//
// Every algorithm lives here, pure and independent of *Engine where
// avoidable, so it is testable in isolation. apply.go (buildCommitMaterialization,
// planOp), projection.go (applyOp, projectedTree) and diff.go
// (fileDiffsForOp's split_page arm) only call it — transferred to this
// subtask at wave 7 the same way D-AQ transferred op.go's cascade builder
// to S2-T2 at wave 1.
//
// ⚠️ The index.md line is derived in ONE PASS over the whole live op set
// (deriveIndex), never attached to a single op and never written per op.
// Two ops that each carry a whole-file post-image of the same path clobber
// each other — planOp does m.writes[p] = postImage(op) and applyOp does
// tree[p] = postImage(op), both last-write-wins (C-63, measured on two
// rename_page ops in one changeset; pre-existing, out of scope, recorded as
// OQ-10). A per-op index.md cascade would be a second instance of exactly
// that bug, and the frozen schema cannot express one for create_page
// anyway (create_page's branch is additionalProperties:false with no
// cascade property — C-64). This file's design avoids creating a second
// instance of C-63 rather than inheriting it.
package stage

import (
	"fmt"
	"path"
	"strings"

	"github.com/awepo-pro/lw/internal/vault"
)

// indexSectionFor returns the index.md "## <Section>" heading a
// create_page op of type t inserts its line under (S2-T8 rule (a)).
//
// Pinned as an explicit switch, not vault.PageType.Dir() title-cased: the
// two coincide today (entity -> wiki/entities -> "## Entities", etc.), but
// a derivation must not silently follow a future Dir() change — the two
// concerns are independent facts that happen to agree, not one definition.
func indexSectionFor(t vault.PageType) string {
	switch t {
	case vault.TypeEntity:
		return "## Entities"
	case vault.TypeConcept:
		return "## Concepts"
	case vault.TypeComparison:
		return "## Comparisons"
	case vault.TypeQuery:
		return "## Queries"
	case vault.TypeSummary:
		return "## Summaries"
	default:
		return ""
	}
}

// basenameNoExt returns p's final path element with a trailing ".md"
// removed. path, not filepath — vault paths are slash-separated regardless
// of host OS (00-conventions.md §3), the same reason op.go's
// reverseAddressing uses path throughout.
func basenameNoExt(p string) string {
	return strings.TrimSuffix(path.Base(p), ".md")
}

// insertIndexLine returns indexContent with a new
// "- [[<basename>]] — <title>" entry inserted under section (S2-T8 rule
// (a), insertion rules 1-5). No trailing period: title is a noun phrase
// pulled verbatim from the page the model already wrote, not a sentence
// this engine authors (index-sync never reads the text, only the
// wikilink).
//
// Idempotent (rule 5): a "[[<basename>]]" link already present anywhere in
// indexContent means this create was already indexed by an earlier pass
// over the same running content, so indexContent is returned byte-for-byte
// unchanged — not even trailing-newline-normalized, since "unchanged"
// means exactly that.
func insertIndexLine(indexContent, section, basename, title string) string {
	marker := "[[" + basename + "]]"
	if strings.Contains(indexContent, marker) {
		return indexContent
	}
	entry := "- " + marker + " — " + title

	lines := strings.Split(indexContent, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		// strings.Split's sole artefact of a trailing "\n": drop it so
		// line indices below address real lines only. ensureTrailingNewline
		// restores the invariant (rule 4) once, at the very end.
		lines = lines[:n-1]
	}

	headingIdx := -1
	for i, l := range lines {
		if l == section {
			headingIdx = i
			break
		}
	}

	if headingIdx == -1 {
		return ensureTrailingNewline(insertNewSection(lines, section, entry))
	}
	return ensureTrailingNewline(insertIntoSection(lines, headingIdx, entry))
}

// insertNewSection implements rule 2 (section absent): append at end of
// file — one blank line (omitted when lines is itself empty, so a
// from-scratch index.md does not start with a manufactured leading blank),
// the heading, one blank line, the entry.
func insertNewSection(lines []string, section, entry string) string {
	out := lines
	if len(out) > 0 {
		out = append(out, "")
	}
	out = append(out, section, "", entry)
	return strings.Join(out, "\n")
}

// insertIntoSection implements rules 1 and 3 for the section whose heading
// already sits at lines[headingIdx].
//
// Rule 1 (section has content): insert immediately after the section's
// last non-blank line — before the blank line that separates it from the
// next heading (or the end of the file), not at the very end of the block
// and not at the top. Whatever originally separated the last content line
// from the next heading is preserved after the new entry, untouched.
//
// Rule 3 (section present but empty): there is no "last non-blank line" to
// insert after. The naive "trim trailing blanks back to the heading" loop
// eats the required separator and leaves zero blank lines between heading
// and entry — measured, wrong. Instead this keeps exactly one blank line
// after the heading, then the entry, then whatever blank run originally
// separated the (empty) section from the next heading — which, for the
// single-blank-line convention every fixture in this vault uses, doubles
// as the blank line the next heading still needs before it.
func insertIntoSection(lines []string, headingIdx int, entry string) string {
	nextHeadingIdx := len(lines)
	for i := headingIdx + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "#") {
			nextHeadingIdx = i
			break
		}
	}

	lastContentIdx := -1
	for i := headingIdx + 1; i < nextHeadingIdx; i++ {
		if lines[i] != "" {
			lastContentIdx = i
		}
	}

	var out []string
	out = append(out, lines[:headingIdx+1]...)
	if lastContentIdx == -1 {
		out = append(out, "", entry)
		out = append(out, lines[headingIdx+1:nextHeadingIdx]...)
	} else {
		out = append(out, lines[headingIdx+1:lastContentIdx+1]...)
		out = append(out, entry)
		out = append(out, lines[lastContentIdx+1:nextHeadingIdx]...)
	}
	out = append(out, lines[nextHeadingIdx:]...)

	return strings.Join(out, "\n")
}

// liveCreatePages returns every live create_page op in ops, walking each
// op's own Cascade too and skipping StateDropped/StateRejected at every
// level — the identical per-node check apply.go's planOp and diff.go's
// fileDiffsForOp already apply at every level of a cascade tree, so a
// dropped or rejected node never derives an index.md line for content
// Commit will not actually write.
//
// In practice no create_page ever appears inside a Cascade: buildCascade
// (op.go) emits only patch_page sub-ops (D-AK). The walk still recurses so
// a future cascade shape cannot silently bypass rule (a) by nesting one.
func liveCreatePages(ops []Op) []Op {
	var out []Op
	for _, op := range ops {
		if op.State == StateDropped || op.State == StateRejected {
			continue
		}
		if op.Kind == OpCreatePage {
			out = append(out, op)
		}
		out = append(out, liveCreatePages(op.Cascade)...)
	}
	return out
}

// deriveIndex applies rules 6-7: every op in creates gets an index.md
// insertion, in the order creates is given (rule 6 — liveCreatePages walks
// in Changeset.Ops order, the order a human reviews) — each one folded
// into what the previous insertion produced (rule 7), starting from
// running rather than from disk for every op. This is what makes the
// result compose with a cascade rewrite already present in running (e.g. a
// rename_page cascade's link rewrite landing in the same changeset):
// running already carries it, and every insertion below only ever appends
// a line, never touches one it did not just add.
//
// post resolves an op's post-image bytes — e.g. e.postImage in production
// — injected so this stays a pure function testable against a fake.
func deriveIndex(running []byte, creates []Op, post func(Op) ([]byte, error)) ([]byte, error) {
	content := string(running)
	for _, op := range creates {
		b, err := post(op)
		if err != nil {
			return nil, fmt.Errorf("stage: derive index: %s: %w", op.ID, err)
		}
		page, err := vault.ParsePage(op.Path, b)
		if err != nil {
			return nil, fmt.Errorf("stage: derive index: %s: %w", op.ID, err)
		}
		section := indexSectionFor(page.FM.Type)
		content = insertIndexLine(content, section, basenameNoExt(op.Path), page.FM.Title)
	}
	return []byte(content), nil
}

// splitStub synthesizes a split_page op's Commit-time-only disambiguation
// stub (S2-T8 rule (c)): left in place at the source path rather than
// moved out to .llmwiki/tombstones/ the way a rename_page/merge_pages
// source is, because retargeting an inbound link after a split ("which
// product did this link mean?") is judgement that /docs/design.md §1 reserves for
// the model — a Go guess here is the exact D-Y silent-corruption class.
// Leaving the stub in place lets every inbound link keep resolving with no
// guess at all.
//
// Shaped like retractTombstone (apply.go), deliberately: frontmatter is
// page's, preserved — Sources dropped (the stub carries no provenance of
// its own), Updated left unchanged — plus one new Extra key "split":
// splitDate. Body is "# <title>", a "> **Split.** This page was split into
// the pages below." block, and a "## Related" section listing
// "- [[<basename>]]" for every entry of products, in order, with no
// description text: the engine invents no prose, the title text index.md
// entries carry is the model's own, and a product list has none to copy.
func splitStub(page *vault.Page, products []string, splitDate string) []byte {
	fm := page.FM
	fm.Sources = nil
	extra := make(map[string]string, len(page.FM.Extra)+1)
	for k, v := range page.FM.Extra {
		extra[k] = v
	}
	extra["split"] = splitDate
	fm.Extra = extra

	var body strings.Builder
	body.WriteString("# ")
	body.WriteString(fm.Title)
	body.WriteString("\n\n> **Split.** This page was split into the pages below.\n\n## Related\n\n")
	for _, p := range products {
		body.WriteString("- [[")
		body.WriteString(basenameNoExt(p))
		body.WriteString("]]\n")
	}

	stub := vault.Page{Path: page.Path, FM: fm, Body: ensureTrailingNewline(body.String())}
	return stub.Serialize()
}
