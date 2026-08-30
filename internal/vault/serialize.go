package vault

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
)

// specialScalarChars are the characters whose presence forces a scalar to
// be double-quoted on emit (backbone §2.2 Encode contract).
const specialScalarChars = `:#[]{},&*!|>'"%@`

// Encode renders f as a canonical YAML frontmatter block: "---\n", the known
// keys in struct order, then Extra sorted by key, then "---\n".
//
// Contract (backbone §2.2): title, created, updated, type are always
// emitted, even when zero; tags, sources, confidence, contested are
// emitted only when non-zero — where "zero" for a slice means nil, not
// empty (a non-nil empty slice still emits as "[]"). Lists are flow style.
// Scalars are unquoted unless they contain a character in
// specialScalarChars or have leading/trailing space, in which case they are
// double-quoted with Go's %q escaping. Encode is a hand-rolled writer and
// never delegates to the YAML library's own marshaller, which would reorder
// keys and restyle scalars.
func (f Frontmatter) Encode() []byte {
	var buf bytes.Buffer
	buf.WriteString("---\n")

	writeField(&buf, "title", encodeScalar(f.Title))
	writeField(&buf, "created", encodeScalar(f.Created.String()))
	writeField(&buf, "updated", encodeScalar(f.Updated.String()))
	writeField(&buf, "type", encodeScalar(string(f.Type)))

	if f.Tags != nil {
		writeField(&buf, "tags", encodeFlowList(f.Tags))
	}
	if f.Sources != nil {
		writeField(&buf, "sources", encodeFlowList(f.Sources))
	}
	if f.Confidence != "" {
		writeField(&buf, "confidence", encodeScalar(string(f.Confidence)))
	}
	if f.Contested {
		writeField(&buf, "contested", "true")
	}

	extraKeys := make([]string, 0, len(f.Extra))
	for k := range f.Extra {
		extraKeys = append(extraKeys, k)
	}
	sort.Strings(extraKeys)
	for _, k := range extraKeys {
		writeField(&buf, k, encodeScalar(f.Extra[k]))
	}

	buf.WriteString("---\n")
	return buf.Bytes()
}

// writeField appends one "key: value\n" line to buf.
func writeField(buf *bytes.Buffer, key, value string) {
	buf.WriteString(key)
	buf.WriteString(": ")
	buf.WriteString(value)
	buf.WriteByte('\n')
}

// encodeFlowList renders items as a flow-style YAML sequence, e.g.
// "[inference, decoding]"; an empty (but non-nil) slice renders as "[]".
func encodeFlowList(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = encodeScalar(it)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// encodeScalar renders s unquoted unless it needs quoting, per the Encode
// contract.
func encodeScalar(s string) string {
	if needsQuote(s) {
		return fmt.Sprintf("%q", s)
	}
	return s
}

// needsQuote reports whether s must be double-quoted on emit: it contains a
// character from specialScalarChars, or has leading or trailing whitespace.
func needsQuote(s string) bool {
	if strings.TrimSpace(s) != s {
		return true
	}
	return strings.ContainsAny(s, specialScalarChars)
}

// Validate checks f against s per the §2.2 Validate contract: title
// non-empty; type valid; created/updated non-zero and created <= updated;
// confidence valid when set; every tag present in s's taxonomy.
func (f Frontmatter) Validate(s *Schema) error {
	if f.Title == "" {
		return fmt.Errorf("vault: frontmatter: title is required")
	}
	if !f.Type.Valid() {
		return fmt.Errorf("vault: frontmatter: type %q is invalid", f.Type)
	}
	if f.Created.IsZero() {
		return fmt.Errorf("vault: frontmatter: created is required")
	}
	if f.Updated.IsZero() {
		return fmt.Errorf("vault: frontmatter: updated is required")
	}
	if f.Updated.Time.Before(f.Created.Time) {
		return fmt.Errorf("vault: frontmatter: created (%s) is after updated (%s)", f.Created, f.Updated)
	}
	if f.Confidence != "" && !f.Confidence.Valid() {
		return fmt.Errorf("vault: frontmatter: confidence %q is invalid", f.Confidence)
	}
	if s != nil {
		for _, tag := range f.Tags {
			if !s.HasTag(tag) {
				return fmt.Errorf("vault: frontmatter: tag %q is not in SCHEMA.md's taxonomy; add it to the taxonomy or retag", tag)
			}
		}
	}
	return nil
}
