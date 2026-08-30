package vault

import (
	"testing"
)

// TestEncodeUnitCases exercises Encode directly against hand-built
// Frontmatter values, covering the edge cases pinned by MASTER §9 D-U and
// the other explicit cases this subtask calls out: nil vs. non-nil-empty
// tags, unknown keys sorted, a title needing quotes, unicode, and the
// always-omitted-when-zero fields.
func TestEncodeUnitCases(t *testing.T) {
	base := func() Frontmatter {
		created, err := ParseDate("2026-08-01")
		if err != nil {
			t.Fatalf("ParseDate: %v", err)
		}
		return Frontmatter{
			Title:   "Base Page",
			Created: created,
			Updated: created,
			Type:    TypeConcept,
		}
	}

	tests := []struct {
		name string
		fm   func() Frontmatter
		want string
	}{
		{
			name: "nil tags and sources are omitted entirely",
			fm:   base,
			want: "---\ntitle: Base Page\ncreated: 2026-08-01\nupdated: 2026-08-01\ntype: concept\n---\n",
		},
		{
			name: "non-nil empty tags emit as flow-style []",
			fm: func() Frontmatter {
				fm := base()
				fm.Tags = []string{}
				return fm
			},
			want: "---\ntitle: Base Page\ncreated: 2026-08-01\nupdated: 2026-08-01\ntype: concept\ntags: []\n---\n",
		},
		{
			name: "non-empty tags render flow-style, in slice order",
			fm: func() Frontmatter {
				fm := base()
				fm.Tags = []string{"inference", "decoding"}
				return fm
			},
			want: "---\ntitle: Base Page\ncreated: 2026-08-01\nupdated: 2026-08-01\ntype: concept\ntags: [inference, decoding]\n---\n",
		},
		{
			name: "contested false is omitted; contested true is emitted",
			fm: func() Frontmatter {
				fm := base()
				fm.Contested = true
				return fm
			},
			want: "---\ntitle: Base Page\ncreated: 2026-08-01\nupdated: 2026-08-01\ntype: concept\ncontested: true\n---\n",
		},
		{
			name: "empty confidence is omitted",
			fm:   base,
			want: "---\ntitle: Base Page\ncreated: 2026-08-01\nupdated: 2026-08-01\ntype: concept\n---\n",
		},
		{
			name: "set confidence is emitted unquoted",
			fm: func() Frontmatter {
				fm := base()
				fm.Confidence = ConfHigh
				return fm
			},
			want: "---\ntitle: Base Page\ncreated: 2026-08-01\nupdated: 2026-08-01\ntype: concept\nconfidence: high\n---\n",
		},
		{
			name: "unknown Extra keys are emitted sorted, not in insertion order",
			fm: func() Frontmatter {
				fm := base()
				fm.Extra = map[string]string{"zeta": "last", "alpha": "first", "priority": "2"}
				return fm
			},
			want: "---\ntitle: Base Page\ncreated: 2026-08-01\nupdated: 2026-08-01\ntype: concept\nalpha: first\npriority: 2\nzeta: last\n---\n",
		},
		{
			name: "a title containing a colon is double-quoted",
			fm: func() Frontmatter {
				fm := base()
				fm.Title = "Title: With Colon"
				return fm
			},
			want: "---\ntitle: \"Title: With Colon\"\ncreated: 2026-08-01\nupdated: 2026-08-01\ntype: concept\n---\n",
		},
		{
			name: "a title with leading/trailing space is double-quoted",
			fm: func() Frontmatter {
				fm := base()
				fm.Title = " Padded Title "
				return fm
			},
			want: "---\ntitle: \" Padded Title \"\ncreated: 2026-08-01\nupdated: 2026-08-01\ntype: concept\n---\n",
		},
		{
			name: "a unicode title needs no quoting",
			fm: func() Frontmatter {
				fm := base()
				fm.Title = "Café Attention 日本語"
				return fm
			},
			want: "---\ntitle: Café Attention 日本語\ncreated: 2026-08-01\nupdated: 2026-08-01\ntype: concept\n---\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(tt.fm().Encode())
			if got != tt.want {
				t.Fatalf("Encode() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestEncodeNeedsQuote exercises the quoting predicate directly against
// every character in the special set, plus the surrounding-whitespace rule.
func TestEncodeNeedsQuote(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"plain word", "concept", false},
		{"hyphenated word", "kv-cache", false},
		{"contains colon", "a:b", true},
		{"contains hash", "a#b", true},
		{"contains bracket", "a[b", true},
		{"contains brace", "a{b", true},
		{"contains comma", "a,b", true},
		{"contains ampersand", "a&b", true},
		{"contains asterisk", "a*b", true},
		{"contains bang", "a!b", true},
		{"contains pipe", "a|b", true},
		{"contains gt", "a>b", true},
		{"contains single quote", "a'b", true},
		{"contains double quote", `a"b`, true},
		{"contains percent", "a%b", true},
		{"contains at", "a@b", true},
		{"leading space", " ab", true},
		{"trailing space", "ab ", true},
		{"empty string", "", false},
		{"unicode, no special chars", "日本語", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := needsQuote(tt.in); got != tt.want {
				t.Fatalf("needsQuote(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestEncodeValidate exercises the Validate contract: title non-empty; type
// valid; created/updated non-zero and created <= updated; confidence valid
// when set; every tag present in the schema's taxonomy.
func TestEncodeValidate(t *testing.T) {
	schema := &Schema{Tags: []string{"inference", "memory"}}

	valid := func() Frontmatter {
		created, _ := ParseDate("2026-08-01")
		updated, _ := ParseDate("2026-08-02")
		return Frontmatter{
			Title:      "Valid Page",
			Created:    created,
			Updated:    updated,
			Type:       TypeConcept,
			Tags:       []string{"inference"},
			Confidence: ConfHigh,
		}
	}

	tests := []struct {
		name    string
		fm      func() Frontmatter
		wantErr bool
	}{
		{"valid page passes", valid, false},
		{
			name: "empty title fails",
			fm: func() Frontmatter {
				fm := valid()
				fm.Title = ""
				return fm
			},
			wantErr: true,
		},
		{
			name: "invalid type fails",
			fm: func() Frontmatter {
				fm := valid()
				fm.Type = PageType("bogus")
				return fm
			},
			wantErr: true,
		},
		{
			name: "zero created fails",
			fm: func() Frontmatter {
				fm := valid()
				fm.Created = Date{}
				return fm
			},
			wantErr: true,
		},
		{
			name: "zero updated fails",
			fm: func() Frontmatter {
				fm := valid()
				fm.Updated = Date{}
				return fm
			},
			wantErr: true,
		},
		{
			name: "created after updated fails",
			fm: func() Frontmatter {
				fm := valid()
				fm.Created, fm.Updated = fm.Updated, fm.Created
				return fm
			},
			wantErr: true,
		},
		{
			name: "invalid confidence fails",
			fm: func() Frontmatter {
				fm := valid()
				fm.Confidence = Confidence("very-sure")
				return fm
			},
			wantErr: true,
		},
		{
			name: "empty confidence is fine, since it is omittable",
			fm: func() Frontmatter {
				fm := valid()
				fm.Confidence = ""
				return fm
			},
			wantErr: false,
		},
		{
			name: "a tag outside the taxonomy fails",
			fm: func() Frontmatter {
				fm := valid()
				fm.Tags = []string{"inference", "rogue-tag"}
				return fm
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.fm().Validate(schema)
			if tt.wantErr && err == nil {
				t.Fatalf("Validate() = nil, want an error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}
