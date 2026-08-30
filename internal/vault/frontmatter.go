package vault

import (
	"bytes"
	"fmt"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Frontmatter is the YAML block of a wiki page. Field order here IS the
// canonical emit order (see Encode in serialize.go).
type Frontmatter struct {
	Title      string
	Created    Date
	Updated    Date
	Type       PageType
	Tags       []string
	Sources    []string
	Confidence Confidence
	Contested  bool
	Extra      map[string]string // unknown keys, preserved verbatim, emitted sorted
}

// openDelim is the required opening line of every frontmatter block.
const openDelim = "---\n"

// ParseFrontmatter parses the YAML frontmatter block that opens b.
//
// Contract: b must open with "---\n"; the block ends at the first line that
// is exactly "---". It returns the parsed frontmatter, the remaining body
// (everything after that closing line, with leading blank lines stripped),
// and an error if the delimiters are missing or the YAML is malformed.
// Unknown keys land in Extra with their raw scalar text. Parsed with
// yaml.v3 — never emitted with it; see Encode.
func ParseFrontmatter(b []byte) (Frontmatter, []byte, error) {
	if !bytes.HasPrefix(b, []byte(openDelim)) {
		return Frontmatter{}, nil, fmt.Errorf("vault: frontmatter must open with %q", openDelim)
	}
	rest := b[len(openDelim):]

	yamlEnd, bodyStart, ok := findClosingDelimiter(rest)
	if !ok {
		return Frontmatter{}, nil, fmt.Errorf(`vault: frontmatter has no closing "---" line`)
	}
	yamlBytes := rest[:yamlEnd]
	body := stripLeadingBlankLines(rest[bodyStart:])

	fm, err := decodeFrontmatter(yamlBytes)
	if err != nil {
		return Frontmatter{}, nil, err
	}
	return fm, body, nil
}

// findClosingDelimiter scans rest (everything after the opening "---\n")
// line by line for the first line that is exactly "---". It returns the
// byte offset where that line starts (the end of the YAML block) and the
// offset just past that line's newline (where the body begins), or
// ok == false if no such line exists.
func findClosingDelimiter(rest []byte) (yamlEnd, bodyStart int, ok bool) {
	pos := 0
	for pos <= len(rest) {
		idx := bytes.IndexByte(rest[pos:], '\n')
		var line []byte
		var next int
		if idx == -1 {
			line = rest[pos:]
			next = len(rest)
		} else {
			line = rest[pos : pos+idx]
			next = pos + idx + 1
		}
		if string(line) == "---" {
			return pos, next, true
		}
		if idx == -1 {
			break
		}
		pos = next
	}
	return 0, 0, false
}

// stripLeadingBlankLines drops leading "\n" bytes from body, one blank line
// at a time, per the ParseFrontmatter body contract.
func stripLeadingBlankLines(body []byte) []byte {
	for len(body) > 0 && body[0] == '\n' {
		body = body[1:]
	}
	return body
}

// decodeFrontmatter parses yamlBytes (the text between the two "---" lines)
// into a Frontmatter, routing known keys into their struct fields and
// everything else into Extra.
func decodeFrontmatter(yamlBytes []byte) (Frontmatter, error) {
	var raw map[string]yaml.Node
	if err := yaml.Unmarshal(yamlBytes, &raw); err != nil {
		return Frontmatter{}, fmt.Errorf("vault: parse frontmatter yaml: %w", err)
	}

	var fm Frontmatter
	extra := map[string]string{}

	for key, node := range raw {
		n := node
		var err error
		switch key {
		case "title":
			fm.Title = n.Value
		case "created":
			err = decodeDate(&n, &fm.Created)
		case "updated":
			err = decodeDate(&n, &fm.Updated)
		case "type":
			fm.Type = PageType(n.Value)
		case "tags":
			fm.Tags, err = decodeStringList(&n)
		case "sources":
			fm.Sources, err = decodeStringList(&n)
		case "confidence":
			fm.Confidence = Confidence(n.Value)
		case "contested":
			fm.Contested, err = decodeBool(&n)
		default:
			extra[key] = n.Value
		}
		if err != nil {
			return Frontmatter{}, fmt.Errorf("vault: frontmatter field %q: %w", key, err)
		}
	}

	if len(extra) > 0 {
		fm.Extra = extra
	}
	return fm, nil
}

// decodeDate decodes n into *d, treating an explicit YAML null the same as
// an absent key (d left as the zero Date).
func decodeDate(n *yaml.Node, d *Date) error {
	if isNullNode(n) {
		return nil
	}
	return n.Decode(d)
}

// decodeBool decodes n as a boolean scalar. An explicit YAML null decodes
// to false, matching an absent key.
func decodeBool(n *yaml.Node) (bool, error) {
	if isNullNode(n) {
		return false, nil
	}
	b, err := strconv.ParseBool(n.Value)
	if err != nil {
		return false, fmt.Errorf("not a boolean: %q", n.Value)
	}
	return b, nil
}

// decodeStringList decodes n, a YAML sequence (flow or block style), into a
// slice of its scalar values. A present-but-empty sequence yields a
// non-nil, zero-length slice — the distinction from a wholly absent key
// (nil) is load-bearing for byte-stable round-trips (backbone §2.2).
func decodeStringList(n *yaml.Node) ([]string, error) {
	if isNullNode(n) {
		return nil, nil
	}
	if n.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("expected a list, got yaml kind %d", n.Kind)
	}
	list := make([]string, 0, len(n.Content))
	for _, item := range n.Content {
		list = append(list, item.Value)
	}
	return list, nil
}
