package vault

import (
	"testing"
)

// TestSection_ATXOnly_F2b_1 pins workflow 081, F2b.1: B2 parses to exactly one section,
// ## Abstract; Page.Section("(preamble)") covers "---\ntitle: X\n---\n\nLead.\n\n".
func TestSection_ATXOnly_F2b_1(t *testing.T) {
	const b2 = "---\ntitle: X\n---\n\nLead.\n\n## Abstract\n\nX.\n"
	secs := ParseSections(b2)
	if len(secs) != 1 {
		t.Fatalf("len(ParseSections(b2)) = %d, want 1; sections: %+v", len(secs), secs)
	}
	if secs[0].Heading != "## Abstract" {
		t.Fatalf("heading = %q, want %q", secs[0].Heading, "## Abstract")
	}

	p := &Page{
		Path:     "wiki/concepts/b2.md",
		Body:     b2,
		Sections: secs,
	}
	pre, ok := p.Section("(preamble)")
	if !ok {
		t.Fatalf("p.Section(%q) ok = false, want true", "(preamble)")
	}
	const wantCover = "---\ntitle: X\n---\n\nLead.\n\n"
	if pre.Start != 0 {
		t.Fatalf("pre.Start = %d, want 0", pre.Start)
	}
	if pre.End > len(b2) {
		t.Fatalf("pre.End = %d exceeds len(b2) = %d", pre.End, len(b2))
	}
	gotCover := b2[pre.Start:pre.End]
	if gotCover != wantCover {
		t.Fatalf("preamble cover = %q, want %q", gotCover, wantCover)
	}
}

// TestSection_ATXOnly_F2b_2 pins workflow 081, F2b.2: body Title\n=====\n\ntext\n\n## A\n\nb\n
// parses to exactly one section, "## A".
func TestSection_ATXOnly_F2b_2(t *testing.T) {
	const body = "Title\n=====\n\ntext\n\n## A\n\nb\n"
	secs := ParseSections(body)
	if len(secs) != 1 {
		t.Fatalf("len(ParseSections(body)) = %d, want 1; sections: %+v", len(secs), secs)
	}
	if secs[0].Heading != "## A" {
		t.Fatalf("heading = %q, want %q", secs[0].Heading, "## A")
	}
}

// TestSection_ATXOnly_F2b_5 pins workflow 081, F2b.5: a setext heading whose text starts
// with "#" but is not ATX (body "#tag\n---\n\nx\n\n## A\n\nb\n") parses to exactly one
// section, "## A"; ATX = after 0-3 spaces, 1-6 "#" then a space, a tab or end of line
// (internal/stage/mdsection.go's rule).
func TestSection_ATXOnly_F2b_5(t *testing.T) {
	const body = "#tag\n---\n\nx\n\n## A\n\nb\n"
	secs := ParseSections(body)
	if len(secs) != 1 {
		t.Fatalf("len(ParseSections(body)) = %d, want 1; sections: %+v", len(secs), secs)
	}
	if secs[0].Heading != "## A" {
		t.Fatalf("heading = %q, want %q", secs[0].Heading, "## A")
	}
}
