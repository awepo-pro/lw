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

// maxDepth bounds the maximum composite recursion depth when parsing unknown YAML blocks.
const maxDepth = 100

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
			var target *yaml.Node
			target, err = resolveAlias(&n)
			if err == nil {
				if target.Kind == yaml.SequenceNode || target.Kind == yaml.MappingNode {
					extraYAML[key], err = emitNodeYAML(target)
				} else {
					extra[key] = target.Value
				}
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

// resolveAlias walks any chain of AliasNodes to their target, detecting alias cycles.
func resolveAlias(n *yaml.Node) (*yaml.Node, error) {
	steps := 0
	for n != nil && n.Kind == yaml.AliasNode && n.Alias != nil {
		if steps > maxDepth {
			return nil, fmt.Errorf("alias cycle")
		}
		steps++
		n = n.Alias
	}
	return n, nil
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
	n, _ = resolveAlias(n)
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
	n, _ = resolveAlias(n)
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

// nodeEmitter walks a yaml.Node and emits canonical YAML text without calling
// any YAML marshal library function (Correction C-3 constraint), tracking alias
// targets on the current recursion path to detect alias cycles and bounding
// recursion depth at 100 levels.
type nodeEmitter struct {
	visited map[*yaml.Node]bool
	depth   int
}

func (e *nodeEmitter) enterNode(n *yaml.Node) (*yaml.Node, func(), error) {
	if e.depth > maxDepth {
		return nil, nil, fmt.Errorf("too deeply nested")
	}
	var err error
	n, err = resolveAlias(n)
	if err != nil {
		return nil, nil, err
	}
	if n == nil {
		return nil, func() {}, nil
	}
	if n.Kind == yaml.SequenceNode || n.Kind == yaml.MappingNode {
		if e.visited[n] {
			return nil, nil, fmt.Errorf("alias cycle")
		}
		e.visited[n] = true
		e.depth++
		return n, func() {
			delete(e.visited, n)
			e.depth--
		}, nil
	}
	return n, func() {}, nil
}

func (e *nodeEmitter) emitNode(n *yaml.Node) (string, error) {
	n, leave, err := e.enterNode(n)
	if err != nil {
		return "", err
	}
	defer leave()

	if n == nil {
		return "null", nil
	}
	if isFlow(n) {
		return e.emitFlow(n)
	}
	switch n.Kind {
	case yaml.SequenceNode:
		return e.emitBlockSequence(n)
	case yaml.MappingNode:
		return e.emitBlockMapping(n)
	default:
		return emitScalarYAML(n), nil
	}
}

func (e *nodeEmitter) emitFlowNode(n *yaml.Node) (string, error) {
	n, leave, err := e.enterNode(n)
	if err != nil {
		return "", err
	}
	defer leave()

	if n == nil {
		return "null", nil
	}
	return e.emitFlow(n)
}

func (e *nodeEmitter) emitFlow(n *yaml.Node) (string, error) {
	switch n.Kind {
	case yaml.SequenceNode:
		return e.emitFlowSequence(n)
	case yaml.MappingNode:
		return e.emitFlowMapping(n)
	default:
		return emitScalarYAML(n), nil
	}
}

// emitFlowSequence emits a sequence in flow style: "[a, b]" or "[]".
func (e *nodeEmitter) emitFlowSequence(n *yaml.Node) (string, error) {
	if len(n.Content) == 0 {
		return "[]", nil
	}
	items := make([]string, len(n.Content))
	for i, it := range n.Content {
		s, err := e.emitFlowNode(it)
		if err != nil {
			return "", err
		}
		items[i] = s
	}
	return "[" + strings.Join(items, ", ") + "]", nil
}

// emitFlowMapping emits a mapping in flow style: "{k: v, k2: v2}" or "{}".
func (e *nodeEmitter) emitFlowMapping(n *yaml.Node) (string, error) {
	if len(n.Content) == 0 {
		return "{}", nil
	}
	entries := make([]string, 0, len(n.Content)/2)
	for i := 0; i < len(n.Content); i += 2 {
		k := emitScalarYAML(n.Content[i])
		v, err := e.emitFlowNode(n.Content[i+1])
		if err != nil {
			return "", err
		}
		entries = append(entries, k+": "+v)
	}
	return "{" + strings.Join(entries, ", ") + "}", nil
}

// emitBlockSequence emits a sequence in block style with "- item" lines.
// Nested block composites appear on following lines indented by 2 spaces.
func (e *nodeEmitter) emitBlockSequence(n *yaml.Node) (string, error) {
	var lines []string
	for _, it := range n.Content {
		target, err := resolveAlias(it)
		if err != nil {
			return "", err
		}
		if isFlow(target) {
			s, err := e.emitFlowNode(target)
			if err != nil {
				return "", err
			}
			lines = append(lines, "- "+s)
		} else {
			nestedText, err := e.emitNode(target)
			if err != nil {
				return "", err
			}
			lines = append(lines, "-")
			for _, line := range strings.Split(nestedText, "\n") {
				lines = append(lines, "  "+line)
			}
		}
	}
	return strings.Join(lines, "\n"), nil
}

// emitBlockMapping emits a mapping in block style with "key: value" lines.
// Nested block composites appear on following lines indented by 2 spaces.
func (e *nodeEmitter) emitBlockMapping(n *yaml.Node) (string, error) {
	var lines []string
	for i := 0; i < len(n.Content); i += 2 {
		k := emitScalarYAML(n.Content[i])
		target, err := resolveAlias(n.Content[i+1])
		if err != nil {
			return "", err
		}
		if isFlow(target) {
			s, err := e.emitFlowNode(target)
			if err != nil {
				return "", err
			}
			lines = append(lines, k+": "+s)
		} else {
			nestedText, err := e.emitNode(target)
			if err != nil {
				return "", err
			}
			lines = append(lines, k+":")
			for _, line := range strings.Split(nestedText, "\n") {
				lines = append(lines, "  "+line)
			}
		}
	}
	return strings.Join(lines, "\n"), nil
}

// emitNodeYAML walks a yaml.Node and emits canonical YAML text without calling
// any YAML marshal library function (Correction C-3 constraint).
func emitNodeYAML(n *yaml.Node) (string, error) {
	e := &nodeEmitter{
		visited: make(map[*yaml.Node]bool),
		depth:   1,
	}
	return e.emitNode(n)
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

// frontmatterBlockKeys are the recognized YAML frontmatter property keys whose
// presence directly after a "---" fence indicates a frontmatter block (FIX-B, FIX-C).
var frontmatterBlockKeys = []string{
	"title",
	"created",
	"updated",
	"type",
	"tags",
	"sources",
	"confidence",
	"contested",
	"aliases",
}

// fenceOpen reports whether line opens a CommonMark fenced code block: 0-3
// leading spaces, then a run of 3 or more identical '`' or '~' characters.
// A backtick fence's info string (everything after the opening run) must
// not itself contain a backtick (CommonMark §4.5); a tilde fence's info
// string has no such restriction. ch and n are the fence character and how
// many of it opened the fence — fenceClose needs both to recognize a valid
// closing line (081 FD.3).
func fenceOpen(line string) (ch byte, n int, ok bool) {
	i, spaces := 0, 0
	for i < len(line) && line[i] == ' ' {
		i++
		spaces++
	}
	if spaces > 3 || i >= len(line) {
		return 0, 0, false
	}
	c := line[i]
	if c != '`' && c != '~' {
		return 0, 0, false
	}
	start := i
	for i < len(line) && line[i] == c {
		i++
	}
	count := i - start
	if count < 3 {
		return 0, 0, false
	}
	if c == '`' && strings.IndexByte(line[i:], '`') >= 0 {
		return 0, 0, false
	}
	return c, count, true
}

// fenceClose reports whether line closes a fence that was opened with
// openCount consecutive ch characters: 0-3 leading spaces, then a run of ch
// at least openCount long, then nothing but trailing spaces, tabs or CR (081 FD.3).
func fenceClose(line string, ch byte, openCount int) bool {
	i, spaces := 0, 0
	for i < len(line) && line[i] == ' ' {
		i++
		spaces++
	}
	if spaces > 3 || i >= len(line) || line[i] != ch {
		return false
	}
	start := i
	for i < len(line) && line[i] == ch {
		i++
	}
	if i-start < openCount {
		return false
	}
	for ; i < len(line); i++ {
		if line[i] != ' ' && line[i] != '\t' && line[i] != '\r' {
			return false
		}
	}
	return true
}

// FindFrontmatterBlock scans text for a frontmatter block: a line that is
// exactly "---" (after trimming trailing spaces, tabs, and CR) immediately
// followed by a line starting with one of the 9 recognized frontmatter keys
// ("title:", "created:", "updated:", "type:", "tags:", "sources:",
// "confidence:", "contested:", "aliases:"). Lines inside fenced code blocks
// (``` or ~~~ fences, closing fence of the same char at least as long as opening,
// per internal/stage/mdsection.go) are ignored (081 FD.3/R2-4). It returns the
// matched key (without the colon), the 1-based line number of that "---" line
// in text, and ok = true, or "", 0, false if no frontmatter block is found.
func FindFrontmatterBlock(text string) (key string, line int, ok bool) {
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines)-1; i++ {
		if ch, n, okFence := fenceOpen(lines[i]); okFence {
			i++
			for i < len(lines) && !fenceClose(lines[i], ch, n) {
				i++
			}
			continue
		}
		if strings.TrimRight(lines[i], " \t\r") == "---" {
			lineNext := lines[i+1]
			for _, k := range frontmatterBlockKeys {
				if strings.HasPrefix(lineNext, k+":") {
					return k, i + 1, true
				}
			}
		}
	}
	return "", 0, false
}
