package vault

import (
	"reflect"
	"testing"
)

// TestParseFrontmatterErrors exercises the delimiter contract's failure
// modes.
func TestParseFrontmatterErrors(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{
			name: "missing opening delimiter",
			in:   "title: no leading dashes\n---\n\nbody\n",
		},
		{
			name: "missing closing delimiter",
			in:   "---\ntitle: never closed\n",
		},
		{
			name: "malformed yaml",
			in:   "---\ntitle: [unterminated\n---\n\nbody\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := ParseFrontmatter([]byte(tt.in))
			if err == nil {
				t.Fatalf("ParseFrontmatter(%q) = nil error, want an error", tt.in)
			}
		})
	}
}

// TestParseFrontmatterFirstMatchDelimiter proves that the closing "---" is
// the FIRST such line, not the last — a "---" horizontal rule or fenced
// code block inside the body must not be mistaken for the real delimiter,
// and correspondingly a "---" inside a fenced block in the YAML... no, the
// case that matters is the reverse: nothing after the true closing "---"
// is scanned for a second one.
func TestParseFrontmatterFirstMatchDelimiter(t *testing.T) {
	in := "---\n" +
		"title: Fenced\n" +
		"created: 2026-08-09\n" +
		"updated: 2026-08-09\n" +
		"type: concept\n" +
		"---\n" +
		"\n" +
		"```text\n" +
		"---\n" +
		"a line of dashes inside a fence, not a delimiter\n" +
		"---\n" +
		"```\n"

	fm, body, err := ParseFrontmatter([]byte(in))
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}
	if fm.Title != "Fenced" {
		t.Fatalf("Title = %q, want %q", fm.Title, "Fenced")
	}
	wantBody := "```text\n---\na line of dashes inside a fence, not a delimiter\n---\n```\n"
	if string(body) != wantBody {
		t.Fatalf("body = %q, want %q", body, wantBody)
	}
}

// TestParseFrontmatterPermissive proves ParseFrontmatter accepts the
// non-canonical human-written forms present in
// spec/fixtures/noncanonical/: block-sequence lists, quoted scalars with
// trailing whitespace, reordered keys, and extra blank lines after the
// closing "---".
func TestParseFrontmatterPermissive(t *testing.T) {
	in := "---\n" +
		"type: \"concept\"\n" +
		"title: Reordered\n" +
		"updated: 2026-08-29\n" +
		"created: 2026-08-25\n" +
		"tags:\n" +
		"  - inference\n" +
		"  - decoding\n" +
		"confidence: \"high\"   \n" +
		"---\n" +
		"\n" +
		"\n" +
		"\n" +
		"Body.\n"

	fm, body, err := ParseFrontmatter([]byte(in))
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}
	if fm.Type != TypeConcept {
		t.Fatalf("Type = %q, want %q", fm.Type, TypeConcept)
	}
	if fm.Title != "Reordered" {
		t.Fatalf("Title = %q, want %q", fm.Title, "Reordered")
	}
	if fm.Created.String() != "2026-08-25" || fm.Updated.String() != "2026-08-29" {
		t.Fatalf("Created/Updated = %s/%s, want 2026-08-25/2026-08-29", fm.Created, fm.Updated)
	}
	if want := []string{"inference", "decoding"}; !reflect.DeepEqual(fm.Tags, want) {
		t.Fatalf("Tags = %v, want %v", fm.Tags, want)
	}
	if fm.Confidence != ConfHigh {
		t.Fatalf("Confidence = %q, want %q", fm.Confidence, ConfHigh)
	}
	if string(body) != "Body.\n" {
		t.Fatalf("body = %q, want %q (leading blank lines must be stripped)", body, "Body.\n")
	}
}

// TestParseFrontmatterNilVsEmptyTags proves the load-bearing distinction
// (MASTER §9 D-U): an absent tags/sources key parses to nil, while an
// explicit "tags: []" parses to a non-nil, empty slice.
func TestParseFrontmatterNilVsEmptyTags(t *testing.T) {
	tests := []struct {
		name string
		line string
		want []string
	}{
		{name: "absent key is nil", line: "", want: nil},
		{name: "explicit empty list is non-nil empty", line: "tags: []\n", want: []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := "---\ntitle: T\ncreated: 2026-08-01\nupdated: 2026-08-01\ntype: concept\n" + tt.line + "---\n\nbody\n"
			fm, _, err := ParseFrontmatter([]byte(in))
			if err != nil {
				t.Fatalf("ParseFrontmatter: %v", err)
			}
			if tt.want == nil {
				if fm.Tags != nil {
					t.Fatalf("Tags = %#v, want nil", fm.Tags)
				}
				return
			}
			if fm.Tags == nil {
				t.Fatalf("Tags = nil, want non-nil empty slice")
			}
			if !reflect.DeepEqual(fm.Tags, tt.want) {
				t.Fatalf("Tags = %#v, want %#v", fm.Tags, tt.want)
			}
		})
	}
}

// TestParseFrontmatterUnknownKeys proves unknown keys land in Extra with
// their raw scalar text, and known keys never leak into it.
func TestParseFrontmatterUnknownKeys(t *testing.T) {
	in := "---\n" +
		"title: T\n" +
		"created: 2026-08-01\n" +
		"updated: 2026-08-01\n" +
		"type: concept\n" +
		"zeta: last\n" +
		"alpha: first\n" +
		"priority: 2\n" +
		"---\n\nbody\n"

	fm, _, err := ParseFrontmatter([]byte(in))
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}
	want := map[string]string{"zeta": "last", "alpha": "first", "priority": "2"}
	if !reflect.DeepEqual(fm.Extra, want) {
		t.Fatalf("Extra = %#v, want %#v", fm.Extra, want)
	}
	for _, known := range []string{"title", "created", "updated", "type"} {
		if _, ok := fm.Extra[known]; ok {
			t.Fatalf("Extra contains known key %q", known)
		}
	}
}

// TestParseFrontmatterEmptyBody proves a body of nothing but blank lines
// after the closing "---" parses to the empty string, not "\n" (MASTER §9
// D-U).
func TestParseFrontmatterEmptyBody(t *testing.T) {
	in := "---\ntitle: T\ncreated: 2026-08-01\nupdated: 2026-08-01\ntype: concept\n---\n\n\n"
	_, body, err := ParseFrontmatter([]byte(in))
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}
	if len(body) != 0 {
		t.Fatalf("body = %q, want empty", body)
	}
}

// TestParseFrontmatterNoTrailingNewlineBody proves a body with no trailing
// newline is returned exactly as written — normalizing it to end with "\n"
// is Page's job (§2.3), not ParseFrontmatter's.
func TestParseFrontmatterNoTrailingNewlineBody(t *testing.T) {
	in := "---\ntitle: T\ncreated: 2026-08-01\nupdated: 2026-08-01\ntype: concept\n---\n\nno trailing newline"
	_, body, err := ParseFrontmatter([]byte(in))
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}
	if string(body) != "no trailing newline" {
		t.Fatalf("body = %q, want %q", body, "no trailing newline")
	}
}

// TestParseFrontmatterContested proves the boolean field parses both
// values and rejects garbage.
func TestParseFrontmatterContested(t *testing.T) {
	tests := []struct {
		value   string
		want    bool
		wantErr bool
	}{
		{value: "true", want: true},
		{value: "false", want: false},
		{value: "not-a-bool", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			in := "---\ntitle: T\ncreated: 2026-08-01\nupdated: 2026-08-01\ntype: concept\ncontested: " + tt.value + "\n---\n\nbody\n"
			fm, _, err := ParseFrontmatter([]byte(in))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseFrontmatter(contested: %s) = nil error, want an error", tt.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseFrontmatter: %v", err)
			}
			if fm.Contested != tt.want {
				t.Fatalf("Contested = %v, want %v", fm.Contested, tt.want)
			}
		})
	}
}

// TestParseFrontmatterMalformedListError proves a non-sequence value for
// tags/sources is rejected rather than silently ignored.
func TestParseFrontmatterMalformedListError(t *testing.T) {
	in := "---\ntitle: T\ncreated: 2026-08-01\nupdated: 2026-08-01\ntype: concept\ntags: not-a-list\n---\n\nbody\n"
	_, _, err := ParseFrontmatter([]byte(in))
	if err == nil {
		t.Fatalf("ParseFrontmatter(tags: not-a-list) = nil error, want an error")
	}
}
