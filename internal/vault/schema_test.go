package vault

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// TestSchemaParseFixtures proves ParseSchema extracts exactly the 12 tags
// present in both spec/fixtures/minimal/SCHEMA.md and
// spec/fixtures/dirty/SCHEMA.md, sorted and lowercase, plus a non-empty
// Domain.
func TestSchemaParseFixtures(t *testing.T) {
	root := testutil.FixtureRoot(t)

	for _, vault := range []string{"minimal", "dirty"} {
		t.Run(vault, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(root, vault, "SCHEMA.md"))
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}

			s, err := ParseSchema(b)
			if err != nil {
				t.Fatalf("ParseSchema: %v", err)
			}

			if s.Domain == "" {
				t.Fatalf("Domain is empty")
			}
			if got := len(s.Tags); got != 12 {
				t.Fatalf("len(Tags) = %d, want 12: %v", got, s.Tags)
			}
			if !sort.StringsAreSorted(s.Tags) {
				t.Fatalf("Tags is not sorted: %v", s.Tags)
			}
			for _, tag := range s.Tags {
				if tag != strings.ToLower(tag) {
					t.Fatalf("tag %q is not lowercase", tag)
				}
			}
			if len(s.Conventions) == 0 {
				t.Fatalf("Conventions is empty")
			}
		})
	}
}

// TestSchemaHasTag proves HasTag matches taxonomy entries case-insensitively
// and rejects anything outside the taxonomy.
func TestSchemaHasTag(t *testing.T) {
	s := &Schema{Tags: []string{"attention", "memory"}}

	tests := []struct {
		tag  string
		want bool
	}{
		{"memory", true},
		{"MEMORY", true},
		{"Attention", true},
		{"rogue-tag", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			if got := s.HasTag(tt.tag); got != tt.want {
				t.Fatalf("HasTag(%q) = %v, want %v", tt.tag, got, tt.want)
			}
		})
	}
}

// TestSchemaMissingTagsSectionIsError proves a SCHEMA.md with no "## Tags"
// heading is rejected — a vault with no taxonomy cannot validate anything.
func TestSchemaMissingTagsSectionIsError(t *testing.T) {
	in := "# SCHEMA\n\n## Domain\n\nSome domain.\n\n## Conventions\n\n- Do things.\n"
	_, err := ParseSchema([]byte(in))
	if err == nil {
		t.Fatalf("ParseSchema() = nil error, want an error for a missing \"## Tags\" section")
	}
}

// TestSchemaTagExtraction proves the backtick-first, else-first-word rule
// for extracting a tag from its taxonomy bullet.
func TestSchemaTagExtraction(t *testing.T) {
	in := "# SCHEMA\n\n## Tags\n\n" +
		"- `inference` — running a trained model.\n" +
		"- decoding without backticks, still one word.\n" +
		"- `Memory` — mixed case in the backticks.\n"

	s, err := ParseSchema([]byte(in))
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	want := []string{"decoding", "inference", "memory"}
	if len(s.Tags) != len(want) {
		t.Fatalf("Tags = %v, want %v", s.Tags, want)
	}
	for i := range want {
		if s.Tags[i] != want[i] {
			t.Fatalf("Tags = %v, want %v", s.Tags, want)
		}
	}
}
