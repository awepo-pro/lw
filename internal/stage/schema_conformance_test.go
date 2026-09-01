package stage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// schemaDoc is a minimal, hand-written JSON-Schema-subset interpreter
// (00-conventions.md §4 allows no JSON-schema dependency). It understands
// exactly the constructs spec/changeset.schema.json uses: $ref into
// $defs, oneOf, object/required/additionalProperties/properties,
// array/items/minItems, string/pattern/enum/minLength, integer/minimum,
// and const. Nothing else in the schema needs interpreting.
type schemaDoc struct {
	defs map[string]any
}

// loadChangesetSchema reads and parses spec/changeset.schema.json, walking
// up from the test's working directory the same way testutil.FixtureRoot
// locates spec/fixtures.
func loadChangesetSchema(t *testing.T) (map[string]any, *schemaDoc) {
	t.Helper()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := wd
	var path string
	for {
		candidate := filepath.Join(dir, "spec", "changeset.schema.json")
		if _, err := os.Stat(candidate); err == nil {
			path = candidate
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no spec/changeset.schema.json found above %s", wd)
		}
		dir = parent
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	defs, _ := root["$defs"].(map[string]any)
	return root, &schemaDoc{defs: defs}
}

// resolve follows a "$ref": "#/$defs/<name>" in node, if present.
func (s *schemaDoc) resolve(node map[string]any) map[string]any {
	ref, ok := node["$ref"].(string)
	if !ok {
		return node
	}
	const prefix = "#/$defs/"
	name := ref
	if len(ref) > len(prefix) && ref[:len(prefix)] == prefix {
		name = ref[len(prefix):]
	}
	resolved, ok := s.defs[name].(map[string]any)
	if !ok {
		return node
	}
	return resolved
}

// validate checks value against node (a schema fragment), returning every
// violation found, each prefixed with the JSON-pointer-ish path it
// occurred at.
func (s *schemaDoc) validate(node map[string]any, value any, path string) []string {
	node = s.resolve(node)
	var errs []string

	if oneOf, ok := node["oneOf"].([]any); ok {
		matches := 0
		var lastErrs []string
		for _, sub := range oneOf {
			subSchema, ok := sub.(map[string]any)
			if !ok {
				continue
			}
			e := s.validate(subSchema, value, path)
			if len(e) == 0 {
				matches++
			} else {
				lastErrs = e
			}
		}
		if matches != 1 {
			errs = append(errs, fmt.Sprintf("%s: matched %d of the oneOf branches (want exactly 1); e.g. %v", path, matches, lastErrs))
		}
		return errs
	}

	if typ, ok := node["type"].(string); ok {
		switch typ {
		case "object":
			errs = append(errs, s.validateObject(node, value, path)...)
		case "array":
			errs = append(errs, s.validateArray(node, value, path)...)
		case "string":
			errs = append(errs, s.validateString(node, value, path)...)
		case "integer":
			errs = append(errs, s.validateInteger(node, value, path)...)
		case "boolean":
			if _, ok := value.(bool); !ok {
				errs = append(errs, fmt.Sprintf("%s: want a boolean", path))
			}
		}
	}

	if c, ok := node["const"].(string); ok {
		if got, ok := value.(string); !ok || got != c {
			errs = append(errs, fmt.Sprintf("%s: want const %q, got %v", path, c, value))
		}
	}

	return errs
}

func (s *schemaDoc) validateObject(node map[string]any, value any, path string) []string {
	obj, ok := value.(map[string]any)
	if !ok {
		return []string{fmt.Sprintf("%s: want an object", path)}
	}

	var errs []string
	if required, ok := node["required"].([]any); ok {
		for _, r := range required {
			key, _ := r.(string)
			if _, present := obj[key]; !present {
				errs = append(errs, fmt.Sprintf("%s: missing required key %q", path, key))
			}
		}
	}

	props, _ := node["properties"].(map[string]any)
	additionalAllowed := true
	if ap, ok := node["additionalProperties"]; ok {
		if b, ok := ap.(bool); ok {
			additionalAllowed = b
		}
	}

	for key, v := range obj {
		subRaw, known := props[key]
		if !known {
			if !additionalAllowed {
				errs = append(errs, fmt.Sprintf("%s: key %q is not a recognized property (additionalProperties:false)", path, key))
			}
			continue
		}
		sub, ok := subRaw.(map[string]any)
		if !ok {
			continue
		}
		errs = append(errs, s.validate(sub, v, path+"."+key)...)
	}
	return errs
}

func (s *schemaDoc) validateArray(node map[string]any, value any, path string) []string {
	arr, ok := value.([]any)
	if !ok {
		return []string{fmt.Sprintf("%s: want an array", path)}
	}
	var errs []string
	if minItems, ok := node["minItems"].(float64); ok && float64(len(arr)) < minItems {
		errs = append(errs, fmt.Sprintf("%s: has %d items, want >= %d", path, len(arr), int(minItems)))
	}
	if items, ok := node["items"].(map[string]any); ok {
		for i, elem := range arr {
			errs = append(errs, s.validate(items, elem, fmt.Sprintf("%s[%d]", path, i))...)
		}
	}
	return errs
}

func (s *schemaDoc) validateString(node map[string]any, value any, path string) []string {
	str, ok := value.(string)
	if !ok {
		return []string{fmt.Sprintf("%s: want a string", path)}
	}
	var errs []string
	if pat, ok := node["pattern"].(string); ok {
		re := regexp.MustCompile(pat)
		if !re.MatchString(str) {
			errs = append(errs, fmt.Sprintf("%s: %q does not match pattern %s", path, str, pat))
		}
	}
	if minLen, ok := node["minLength"].(float64); ok && float64(len(str)) < minLen {
		errs = append(errs, fmt.Sprintf("%s: length %d < minLength %d", path, len(str), int(minLen)))
	}
	if enum, ok := node["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			if es, ok := e.(string); ok && es == str {
				found = true
				break
			}
		}
		if !found {
			errs = append(errs, fmt.Sprintf("%s: %q is not one of %v", path, str, enum))
		}
	}
	return errs
}

func (s *schemaDoc) validateInteger(node map[string]any, value any, path string) []string {
	num, ok := value.(float64)
	if !ok {
		return []string{fmt.Sprintf("%s: want an integer", path)}
	}
	var errs []string
	if num != float64(int64(num)) {
		errs = append(errs, fmt.Sprintf("%s: %v is not an integer", path, num))
	}
	if min, ok := node["minimum"].(float64); ok && num < min {
		errs = append(errs, fmt.Sprintf("%s: %v < minimum %v", path, num, min))
	}
	return errs
}

// assertValidatesAgainstSchema marshals v to JSON, unmarshals it back into
// a generic map (so the exact wire bytes are what gets checked), and fails
// t with every violation schemaDoc finds against root.
func assertValidatesAgainstSchema(t *testing.T, root map[string]any, s *schemaDoc, v any) {
	t.Helper()

	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var generic any
	if err := json.Unmarshal(b, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if errs := s.validate(root, generic, "$"); len(errs) > 0 {
		t.Fatalf("schema violations for %s:\n%s", b, joinLines(errs))
	}
}

func joinLines(lines []string) string {
	out := ""
	for _, l := range lines {
		out += "  " + l + "\n"
	}
	return out
}

// TestSchemaConformance marshals several shapes of Changeset and validates
// each against spec/changeset.schema.json's actual, parsed rules — not a
// hand-duplicated copy of them. A test scoped to "one fully populated
// changeset" is exactly what let C-33, C-37 and C-38 through (backbone
// §5, S2-T2 brief item 11), so this covers, at minimum: a freshly-opened
// zero-op changeset, a rename with an empty cascade, and a cascade sub-op
// with no section.
func TestSchemaConformance(t *testing.T) {
	root, s := loadChangesetSchema(t)

	t.Run("zero-op changeset", func(t *testing.T) {
		c := &Changeset{
			ID:       "cs-0000000",
			Intent:   "empty",
			Author:   Author{Kind: "human"},
			OpenedAt: testutil.FixedClock()(),
			Ops:      []Op{},
			Checks:   Checks{Schema: "pass", Lint: "pass", Orphans: 0, BrokenLinks: 0},
		}
		assertValidatesAgainstSchema(t, root, s, c)
	})

	t.Run("rename with empty cascade", func(t *testing.T) {
		c := &Changeset{
			ID:       "cs-1111111",
			Intent:   "rename with no backlinks",
			Author:   Author{Kind: "agent", Model: "m"},
			OpenedAt: testutil.FixedClock()(),
			Ops: []Op{
				{
					ID:    "op1",
					Kind:  OpRenamePage,
					From:  "wiki/concepts/a.md",
					To:    "wiki/concepts/b.md",
					State: StateProposed,
				},
			},
			Checks: Checks{Schema: "pass", Lint: "pass", Orphans: 0, BrokenLinks: 0},
		}
		assertValidatesAgainstSchema(t, root, s, c)
	})

	t.Run("cascade sub-op with no section", func(t *testing.T) {
		c := &Changeset{
			ID:       "cs-2222222",
			Intent:   "rename with a cascade rewrite",
			Author:   Author{Kind: "agent", Model: "m"},
			OpenedAt: testutil.FixedClock()(),
			Ops: []Op{
				{
					ID:    "op1",
					Kind:  OpRenamePage,
					From:  "wiki/entities/gpt-4.md",
					To:    "wiki/entities/gpt-4x.md",
					State: StateProposed,
					Cascade: []Op{
						{
							ID:     "op2",
							Kind:   OpPatchPage,
							Path:   "wiki/concepts/flash-attention.md",
							Before: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
							After:  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
							Hunks: []Hunk{
								{ID: "h1", Path: "wiki/concepts/flash-attention.md",
									Del: []string{"- [[gpt-4]] — a production model."},
									Add: []string{"- [[gpt-4x]] — a production model."}},
							},
							State: StateProposed,
						},
					},
				},
			},
			Checks: Checks{Schema: "pass", Lint: "pass", Orphans: 0, BrokenLinks: 0},
		}
		assertValidatesAgainstSchema(t, root, s, c)
	})
}
