package vault

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// dateLayout is the strict on-disk format for every Date: "2006-01-02".
const dateLayout = "2006-01-02"

// Date is a calendar date with no time zone, serialized as YYYY-MM-DD.
type Date struct{ time.Time }

// ParseDate parses s in the strict "2006-01-02" layout. Any deviation —
// missing zero-padding, a time component, trailing text — is an error.
func ParseDate(s string) (Date, error) {
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return Date{}, fmt.Errorf("vault: parse date %q: %w", s, err)
	}
	return Date{Time: t}, nil
}

// String renders d in "2006-01-02" form, or "" for the zero value — never
// time.Time's "0001-01-01".
func (d Date) String() string {
	if d.IsZero() {
		return ""
	}
	return d.Time.Format(dateLayout)
}

// IsZero reports whether d is the zero value.
func (d Date) IsZero() bool {
	return d.Time.IsZero()
}

// UnmarshalYAML decodes a YAML scalar node in "2006-01-02" form into d.
func (d *Date) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("vault: date must be a scalar, got yaml kind %d", n.Kind)
	}
	if isNullNode(n) {
		*d = Date{}
		return nil
	}
	parsed, err := ParseDate(n.Value)
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

// isNullNode reports whether n is an explicit or implicit YAML null, so
// callers can treat "key:" and "key: null" the same as an absent key.
func isNullNode(n *yaml.Node) bool {
	return n.Tag == "!!null"
}

// PageType is the kind of wiki page, which also determines its directory
// under wiki/.
type PageType string

const (
	TypeEntity     PageType = "entity"
	TypeConcept    PageType = "concept"
	TypeComparison PageType = "comparison"
	TypeQuery      PageType = "query"
	TypeSummary    PageType = "summary"
)

// Valid reports whether t is one of the five defined page types.
func (t PageType) Valid() bool {
	switch t {
	case TypeEntity, TypeConcept, TypeComparison, TypeQuery, TypeSummary:
		return true
	}
	return false
}

// Dir returns the plural vault-relative directory that pages of type t live
// under, e.g. TypeConcept.Dir() == "wiki/concepts". Returns "" for an
// invalid type.
func (t PageType) Dir() string {
	switch t {
	case TypeEntity:
		return "wiki/entities"
	case TypeConcept:
		return "wiki/concepts"
	case TypeComparison:
		return "wiki/comparisons"
	case TypeQuery:
		return "wiki/queries"
	case TypeSummary:
		return "wiki/summaries"
	default:
		return ""
	}
}

// Confidence is a page's self-reported confidence in its own claims.
type Confidence string

const (
	ConfHigh   Confidence = "high"
	ConfMedium Confidence = "medium"
	ConfLow    Confidence = "low"
)

// Valid reports whether c is one of the three defined confidence levels.
// The empty string — "unset" — is not itself valid; callers that treat an
// unset confidence as acceptable check c == "" before calling Valid.
func (c Confidence) Valid() bool {
	switch c {
	case ConfHigh, ConfMedium, ConfLow:
		return true
	}
	return false
}
