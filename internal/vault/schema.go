package vault

import (
	"fmt"
	"sort"
	"strings"
)

// Schema is the parsed SCHEMA.md: the domain definition and tag taxonomy.
type Schema struct {
	Domain      string
	Tags        []string // sorted, lowercase; 10-20 of them per /docs/design.md §6
	Conventions []string
}

// ParseSchema parses b, the contents of SCHEMA.md, into a Schema.
//
// Contract (backbone §2.6): the taxonomy is the bullet list under the
// "## Tags" heading; each bullet's first backticked token, or its first
// word if there is no backtick, is the tag. Domain is the text under
// "## Domain". A missing "## Tags" section is an error.
func ParseSchema(b []byte) (*Schema, error) {
	sections := splitH2Sections(string(b))

	tagsBlock, ok := sections["tags"]
	if !ok {
		return nil, fmt.Errorf(`vault: SCHEMA.md has no "## Tags" section`)
	}

	tags := parseTagBullets(tagsBlock)
	sort.Strings(tags)

	return &Schema{
		Domain:      strings.TrimSpace(sections["domain"]),
		Tags:        tags,
		Conventions: parseBullets(sections["conventions"]),
	}, nil
}

// HasTag reports whether tag is present in s's taxonomy, case-insensitively.
func (s *Schema) HasTag(tag string) bool {
	tag = strings.ToLower(tag)
	for _, t := range s.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// splitH2Sections splits markdown text into the bodies of its "## Heading"
// sections, keyed by the lowercased, trimmed heading text. Text before the
// first "## " heading (e.g. a "# Title" line) is discarded.
func splitH2Sections(text string) map[string]string {
	sections := map[string]string{}
	var current string
	var body strings.Builder

	flush := func() {
		if current != "" {
			sections[current] = body.String()
		}
		body.Reset()
	}

	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "## ") {
			flush()
			current = strings.ToLower(strings.TrimSpace(line[len("## "):]))
			continue
		}
		if current != "" {
			body.WriteString(line)
			body.WriteByte('\n')
		}
	}
	flush()
	return sections
}

// parseBullets returns the trimmed text of every top-level "- " bullet in
// block, in order.
func parseBullets(block string) []string {
	var out []string
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		out = append(out, strings.TrimSpace(strings.TrimPrefix(line, "-")))
	}
	return out
}

// parseTagBullets extracts one tag per bullet in block: the bullet's first
// backticked token, lowercased, or its first word if it has no backtick.
func parseTagBullets(block string) []string {
	var tags []string
	for _, item := range parseBullets(block) {
		tags = append(tags, strings.ToLower(firstTagToken(item)))
	}
	return tags
}

// firstTagToken extracts the tag token from one taxonomy bullet's text.
func firstTagToken(item string) string {
	if strings.HasPrefix(item, "`") {
		if end := strings.Index(item[1:], "`"); end >= 0 {
			return item[1 : 1+end]
		}
	}
	fields := strings.Fields(item)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}
