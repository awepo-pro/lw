// move_raw.go implements the move_raw op kind: relocating a committed raw
// source to another raw/ path — "raw/notes/x.md" -> "raw/articles/x.md" —
// when it was filed in the wrong folder.
//
// raw/ is immutable in the sense that its text is never edited: a move_raw
// keeps the body byte for byte (its sha256 is unchanged, so dedupe and
// src-integrity keep working) and touches only where the file lives. What
// moves with it:
//
//   - its original, when it has one (the "original:" file beside it, e.g.
//     "raw/notes/x.md.orig" -> "raw/articles/x.md.orig"), and the one
//     "original:" frontmatter line that names it;
//   - every wiki page that cites it: each ^[raw/...] marker, markdown link
//     and sources: entry naming the old path is rewritten, as a cascade of
//     patch_page sub-ops the reviewer sees hunk by hunk, exactly like a
//     rename_page cascade rewrites inbound wikilinks.
//
// log.md and other raw sources are history and are never rewritten. Like
// rename_page, Append builds the content and the cascade itself from
// From/To; a proposer supplies only those two paths and a rationale.
package stage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/awepo-pro/lw/internal/vault"
)

// rawMoveDirs are the raw/ subfolders a raw source may be moved into. assets/
// holds images, never a source.
var rawMoveDirs = map[string]bool{"articles": true, "notes": true, "papers": true, "transcripts": true}

// rawPathPattern returns a regexp matching p as a whole path token: not
// preceded by a path character other than "/" (so "../../raw/x.md" still
// matches) and not followed by one (so "x.md.orig" and "x.md-2" do not).
func rawPathPattern(p string) *regexp.Regexp {
	return regexp.MustCompile(`(^|[^A-Za-z0-9_.\-])` + regexp.QuoteMeta(p) + `($|[^A-Za-z0-9_.\-/])`)
}

// replaceRawPath replaces every whole-token occurrence of from in s with to.
func replaceRawPath(s, from, to string) string {
	re := rawPathPattern(from)
	// Two passes: adjacent matches share a boundary character, which one
	// ReplaceAll pass consumes.
	for i := 0; i < 2; i++ {
		s = re.ReplaceAllString(s, "${1}"+strings.ReplaceAll(to, "$", "$$")+"${2}")
	}
	return s
}

// originalSibling returns where an original moves: the new md path plus the
// part of the old original's name after the old md path ("x.md.orig" keeps
// ".orig"), or, when the old original does not start with the md's name
// ("x.pdf" beside "x.md"), the same name in the new folder with the new stem.
func originalSibling(fromMD, toMD, fromOrig string) string {
	if strings.HasPrefix(fromOrig, fromMD) {
		return toMD + strings.TrimPrefix(fromOrig, fromMD)
	}
	ext := path.Ext(fromOrig)
	return strings.TrimSuffix(toMD, ".md") + ext
}

// rewriteOriginalLine rewrites the "original:" line inside raw's leading
// frontmatter block from oldOrig to newOrig, and nothing else.
func rewriteOriginalLine(raw []byte, oldOrig, newOrig string) ([]byte, error) {
	s := string(raw)
	if !strings.HasPrefix(s, "---\n") {
		return nil, fmt.Errorf("no frontmatter block")
	}
	end := strings.Index(s[4:], "\n---")
	if end < 0 {
		return nil, fmt.Errorf("frontmatter block is never closed")
	}
	fm := s[:4+end]
	re := regexp.MustCompile(`(?m)^original:[ \t]*` + regexp.QuoteMeta(oldOrig) + `[ \t]*$`)
	if len(re.FindAllStringIndex(fm, -1)) != 1 {
		return nil, fmt.Errorf("frontmatter has no single \"original: %s\" line", oldOrig)
	}
	fm = re.ReplaceAllLiteralString(fm, "original: "+newOrig)
	return []byte(fm + s[4+end:]), nil
}

// prepareMoveRaw fills op's Content (the raw file as it will read at To) and
// its original pair (OriginalPath/OriginalContent/Original) when the source
// has one, from the committed vault v, and its Cascade (the citing wiki
// pages' rewrites) from pages — the projection of the open changeset, so a
// page another live op already rewrites composes with it (its Before is the
// staged sha, as for any second patch on a staged path). op.From and op.To
// must already be set.
func prepareMoveRaw(op *Op, v, pages *vault.Vault) error {
	r, ok := v.RawSource(op.From)
	if !ok {
		return fmt.Errorf("%w: move_raw: source %s is not a raw source", ErrValidation, op.From)
	}
	b, err := v.Read(op.From)
	if err != nil {
		return fmt.Errorf("%w: move_raw: read %s: %v", ErrValidation, op.From, err)
	}
	op.Content = b
	op.OriginalPath, op.Original, op.OriginalContent = "", "", nil
	if r.Original != "" {
		newOrig := originalSibling(op.From, op.To, r.Original)
		content, err := rewriteOriginalLine(b, r.Original, newOrig)
		if err != nil {
			return fmt.Errorf("%w: move_raw: %s: %v", ErrValidation, op.From, err)
		}
		orig, err := v.Read(r.Original)
		if err != nil {
			return fmt.Errorf("%w: move_raw: original %s: %v", ErrValidation, r.Original, err)
		}
		sum := sha256.Sum256(orig)
		op.Content = content
		op.OriginalPath = newOrig
		op.OriginalContent = orig
		op.Original = hex.EncodeToString(sum[:])
	}

	var cascade []Op
	for _, p := range pages.Pages() {
		cited := false
		for _, s := range p.FM.Sources {
			if s == op.From {
				cited = true
			}
		}
		if !cited && !rawPathPattern(op.From).MatchString(p.Body) {
			continue
		}
		rewritten := *p
		rewritten.Body = replaceRawPath(p.Body, op.From, op.To)
		srcs := make([]string, 0, len(p.FM.Sources))
		seen := map[string]bool{}
		for _, s := range p.FM.Sources {
			if s == op.From {
				s = op.To
			}
			if !seen[s] {
				seen[s] = true
				srcs = append(srcs, s)
			}
		}
		rewritten.FM.Sources = srcs
		before := p.Serialize()
		after := rewritten.Serialize()
		if string(before) == string(after) {
			continue
		}
		cascade = append(cascade, Op{
			Kind:    OpPatchPage,
			Path:    p.Path,
			Before:  p.SHA256(),
			Content: after,
			Hunks:   buildCascadeHunks(p.Path, string(before), string(after)),
		})
	}
	op.Cascade = cascade
	return nil
}

// validateMoveRaw enforces: From is an existing raw source under raw/, To is
// a free .md path in one of rawMoveDirs, the moved original's path is free,
// the body is unchanged (same body sha as From), no other raw source
// supersedes From (its supersedes: would dangle), and every cascade entry is
// a patch_page on an existing wiki page.
func validateMoveRaw(op Op, v *vault.Vault) error {
	if !strings.HasPrefix(op.From, "raw/") || strings.HasPrefix(op.From, "raw/assets/") {
		return fmt.Errorf("%w: move_raw: from %s must be a raw source under raw/", ErrValidation, op.From)
	}
	r, ok := v.RawSource(op.From)
	if !ok {
		return fmt.Errorf("%w: move_raw: source %s does not exist", ErrValidation, op.From)
	}
	parts := strings.Split(op.To, "/")
	if len(parts) != 3 || parts[0] != "raw" || !rawMoveDirs[parts[1]] {
		return fmt.Errorf("%w: move_raw: to %s must be raw/<articles|notes|papers|transcripts>/<name>.md", ErrValidation, op.To)
	}
	if op.To == op.From {
		return fmt.Errorf("%w: move_raw: to equals from", ErrValidation)
	}
	if v.Exists(op.To) {
		return fmt.Errorf("%w: move_raw: destination %s already exists", ErrValidation, op.To)
	}
	if r.Original != "" {
		if op.OriginalPath == "" || op.Original == "" {
			return fmt.Errorf("%w: move_raw: %s has an original (%s) the op does not carry", ErrValidation, op.From, r.Original)
		}
		if v.Exists(op.OriginalPath) {
			return fmt.Errorf("%w: move_raw: original destination %s already exists", ErrValidation, op.OriginalPath)
		}
	}
	if by, ok := v.SupersededBy(op.From); ok {
		return fmt.Errorf("%w: move_raw: %s is superseded by %s, whose supersedes: would dangle", ErrValidation, op.From, by)
	}
	for _, other := range v.RawSources() {
		if other.Supersedes == op.From {
			return fmt.Errorf("%w: move_raw: %s names it in supersedes:", ErrValidation, other.Path)
		}
	}
	if op.Content != nil {
		moved, err := vault.ParseRawSource(op.To, op.Content)
		if err != nil {
			return fmt.Errorf("%w: move_raw: moved content does not parse: %v", ErrValidation, err)
		}
		if vault.BodySHA256(moved.Body) != vault.BodySHA256(r.Body) {
			return fmt.Errorf("%w: move_raw: the body must move unchanged", ErrValidation)
		}
	}
	for _, sub := range op.Cascade {
		if sub.Kind != OpPatchPage {
			return fmt.Errorf("%w: move_raw: cascade entries must be patch_page", ErrValidation)
		}
		if _, ok := v.Page(sub.Path); !ok {
			return fmt.Errorf("%w: move_raw: cascade target %s is not a wiki page", ErrValidation, sub.Path)
		}
		if err := validateHunks(sub.Hunks); err != nil {
			return err
		}
	}
	return nil
}

// oldOriginalPath returns the original path From names right now in v, or ""
// when it has none — the file a move_raw commit moves out to tombstones.
func oldOriginalPath(v *vault.Vault, from string) string {
	if r, ok := v.RawSource(from); ok {
		return r.Original
	}
	return ""
}
