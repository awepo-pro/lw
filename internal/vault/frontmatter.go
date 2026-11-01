package vault

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"

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
	ExtraYAML  map[string]string // unknown sequence/mapping keys, raw YAML text, emitted sorted
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
// into a Frontmatter, routing known keys into their struct fields,
// unknown scalar keys into Extra, and unknown composite keys into ExtraYAML.
func decodeFrontmatter(yamlBytes []byte) (Frontmatter, error) {
	var raw map[string]yaml.Node
	if err := yaml.Unmarshal(yamlBytes, &raw); err != nil {
		return Frontmatter{}, fmt.Errorf("vault: parse frontmatter yaml: %w", err)
	}

	var fm Frontmatter
	extra := map[string]string{}
	extraYAML := map[string]string{}

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
			target := resolveAlias(&n)
			if target.Kind == yaml.SequenceNode || target.Kind == yaml.MappingNode {
				extraYAML[key] = emitNodeYAML(target)
			} else {
				extra[key] = target.Value
			}
		}
		if err != nil {
			return Frontmatter{}, fmt.Errorf("vault: frontmatter field %q: %w", key, err)
		}
	}

	if len(extra) > 0 {
		fm.Extra = extra
	}
	if len(extraYAML) > 0 {
		fm.ExtraYAML = extraYAML
	}
	return fm, nil
}

// resolveAlias walks any chain of AliasNodes to their target.
func resolveAlias(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	return n
}

var plainStrPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_ ./-]*$`)

// isPlainStr reports whether s may be emitted as an unquoted YAML scalar.
// Plain emission requires a non-empty string starting with a letter or underscore,
// followed only by alphanumeric, underscore, space, dot, slash, or hyphen,
// with no leading or trailing space, and not equal (case-insensitively) to any
// standard YAML boolean or null literal.
func isPlainStr(s string) bool {
	if s == "" || strings.HasSuffix(s, " ") {
		return false
	}
	if !plainStrPattern.MatchString(s) {
		return false
	}
	switch strings.ToLower(s) {
	case "true", "false", "yes", "no", "on", "off", "null", "y", "n":
		return false
	default:
		return true
	}
}

// emitScalarYAML emits a scalar node per rule (d) and (e):
// if Tag is !!str, quote unless isPlainStr is true;
// if Tag is !!int, !!float, !!bool, !!null, !!timestamp, emit Value verbatim
// (with empty !!null emitted as "null");
// otherwise quote with strconv.Quote.
func emitScalarYAML(n *yaml.Node) string {
	n = resolveAlias(n)
	if n == nil {
		return "null"
	}
	switch n.Tag {
	case "!!str":
		if isPlainStr(n.Value) {
			return n.Value
		}
		return strconv.Quote(n.Value)
	case "!!int", "!!float", "!!bool", "!!timestamp":
		return n.Value
	case "!!null":
		if n.Value == "" {
			return "null"
		}
		return n.Value
	default:
		return strconv.Quote(n.Value)
	}
}

// isFlow reports whether n should be emitted on a single flow line.
func isFlow(n *yaml.Node) bool {
	n = resolveAlias(n)
	if n == nil {
		return true
	}
	if n.Kind == yaml.ScalarNode {
		return true
	}
	if n.Kind == yaml.SequenceNode || n.Kind == yaml.MappingNode {
		if len(n.Content) == 0 {
			return true
		}
		return (n.Style & yaml.FlowStyle) != 0
	}
	return false
}

// emitFlowSequence emits a sequence in flow style: "[a, b]" or "[]".
func emitFlowSequence(n *yaml.Node) string {
	if len(n.Content) == 0 {
		return "[]"
	}
	items := make([]string, len(n.Content))
	for i, it := range n.Content {
		items[i] = emitFlowYAML(it)
	}
	return "[" + strings.Join(items, ", ") + "]"
}

// emitFlowMapping emits a mapping in flow style: "{k: v, k2: v2}" or "{}".
func emitFlowMapping(n *yaml.Node) string {
	if len(n.Content) == 0 {
		return "{}"
	}
	entries := make([]string, 0, len(n.Content)/2)
	for i := 0; i < len(n.Content); i += 2 {
		k := emitScalarYAML(n.Content[i])
		v := emitFlowYAML(n.Content[i+1])
		entries = append(entries, k+": "+v)
	}
	return "{" + strings.Join(entries, ", ") + "}"
}

// emitFlowYAML emits n strictly in flow style.
func emitFlowYAML(n *yaml.Node) string {
	n = resolveAlias(n)
	if n == nil {
		return "null"
	}
	switch n.Kind {
	case yaml.SequenceNode:
		return emitFlowSequence(n)
	case yaml.MappingNode:
		return emitFlowMapping(n)
	case yaml.ScalarNode:
		return emitScalarYAML(n)
	default:
		return emitScalarYAML(n)
	}
}

// emitBlockSequence emits a sequence in block style with "- item" lines.
// Nested block composites appear on following lines indented by 2 spaces.
func emitBlockSequence(n *yaml.Node) string {
	var lines []string
	for _, it := range n.Content {
		it = resolveAlias(it)
		if isFlow(it) {
			lines = append(lines, "- "+emitFlowYAML(it))
		} else {
			lines = append(lines, "-")
			nestedText := emitNodeYAML(it)
			for _, line := range strings.Split(nestedText, "\n") {
				lines = append(lines, "  "+line)
			}
		}
	}
	return strings.Join(lines, "\n")
}

// emitBlockMapping emits a mapping in block style with "key: value" lines.
// Nested block composites appear on following lines indented by 2 spaces.
func emitBlockMapping(n *yaml.Node) string {
	var lines []string
	for i := 0; i < len(n.Content); i += 2 {
		k := emitScalarYAML(n.Content[i])
		v := resolveAlias(n.Content[i+1])
		if isFlow(v) {
			lines = append(lines, k+": "+emitFlowYAML(v))
		} else {
			lines = append(lines, k+":")
			nestedText := emitNodeYAML(v)
			for _, line := range strings.Split(nestedText, "\n") {
				lines = append(lines, "  "+line)
			}
		}
	}
	return strings.Join(lines, "\n")
}

// emitNodeYAML walks a yaml.Node and emits canonical YAML text without calling
// any YAML marshal library function (Correction C-3 constraint).
func emitNodeYAML(n *yaml.Node) string {
	n = resolveAlias(n)
	if n == nil {
		return "null"
	}
	if isFlow(n) {
		return emitFlowYAML(n)
	}
	switch n.Kind {
	case yaml.SequenceNode:
		return emitBlockSequence(n)
	case yaml.MappingNode:
		return emitBlockMapping(n)
	default:
		return emitScalarYAML(n)
	}
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
