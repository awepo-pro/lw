package vault

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Page is one wiki page: frontmatter, the markdown body that follows it, and
// the sections and wikilinks parsed out of that body.
type Page struct {
	Path     string // vault-relative, slash-separated, e.g. "wiki/concepts/kv-cache.md"
	FM       Frontmatter
	Body     string     // markdown after the frontmatter; always ends with exactly one "\n"
	Sections []Section  // filled by ParsePage
	Links    []Wikilink // filled by ParsePage
}

// ParsePage parses b — the full contents of a wiki page file at path — into
// a Page.
//
// Contract (backbone §2.3): ParseFrontmatter, then ParseSections, then
// ParseWikilinks, over the resulting body, in that order. Body is
// normalized to the §2.3 rule before Sections/Links are derived from it: a
// non-empty body always ends with exactly one "\n"; an empty body is left
// as "" rather than growing a manufactured newline (MASTER §9 D-U).
func ParsePage(path string, b []byte) (*Page, error) {
	fm, rawBody, err := ParseFrontmatter(b)
	if err != nil {
		return nil, fmt.Errorf("vault: parse page %s: %w", path, err)
	}

	body := normalizeTrailingNewline(string(rawBody))
	return &Page{
		Path:     path,
		FM:       fm,
		Body:     body,
		Sections: ParseSections(body),
		Links:    ParseWikilinks(body),
	}, nil
}

// Serialize renders p as canonical bytes: FM.Encode(), a blank-line
// separator, then Body.
//
// Contract (backbone §2.3): byte-stable and idempotent —
// Canonical(Canonical(b)) == Canonical(b). The empty-body case (MASTER §9
// D-U) falls out of this directly: when Body == "", the output ends
// "---\n\n" and nothing after, because Encode() already ends in "\n" and
// the extra "\n" appended here is the separator blank line, not a
// manufactured body.
func (p *Page) Serialize() []byte {
	out := p.FM.Encode()
	out = append(out, '\n')
	out = append(out, []byte(p.Body)...)
	return out
}

// Section returns the Section whose Heading — the full raw heading line,
// e.g. "## Related" — exactly matches heading, and whether one was found.
func (p *Page) Section(heading string) (Section, bool) {
	for _, s := range p.Sections {
		if s.Heading == heading {
			return s, true
		}
	}
	return Section{}, false
}

// SHA256 returns the hex sha256 of p.Serialize().
func (p *Page) SHA256() string {
	sum := sha256.Sum256(p.Serialize())
	return hex.EncodeToString(sum[:])
}

// Canonical parses b at path and immediately re-serializes it — ParsePage
// then Serialize — the single operation that defines what "canonical" means
// throughout this package.
func Canonical(path string, b []byte) ([]byte, error) {
	p, err := ParsePage(path, b)
	if err != nil {
		return nil, err
	}
	return p.Serialize(), nil
}
