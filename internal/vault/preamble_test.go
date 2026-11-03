package vault

import (
	"strings"
	"testing"
)

// TestPreambleHeading_F2_3 pins workflow 081, F2.3 / R3: vault.PreambleHeading is "(preamble)".
// Page.Section(PreambleHeading) returns Section{Level: 0, Heading: "(preamble)", Start: 0, Body: 0, End: <Start of first heading or len(Body)>}, ok=true.
func TestPreambleHeading_F2_3(t *testing.T) {
	if PreambleHeading != "(preamble)" {
		t.Fatalf("PreambleHeading = %q, want %q", PreambleHeading, "(preamble)")
	}

	t.Run("page with heading has lead region up to first heading", func(t *testing.T) {
		body := "Lead para.\n\n## Abstract\n\nX.\n"
		p := &Page{
			Path:     "wiki/concepts/test.md",
			Body:     body,
			Sections: ParseSections(body),
		}
		if len(p.Sections) != 1 {
			t.Fatalf("len(Sections) = %d, want 1", len(p.Sections))
		}
		sec, ok := p.Section(PreambleHeading)
		if !ok {
			t.Fatalf("p.Section(%q) ok = false, want true", PreambleHeading)
		}
		want := Section{
			Level:   0,
			Heading: "(preamble)",
			Start:   0,
			Body:    0,
			End:     strings.Index(body, "## Abstract"),
		}
		if sec != want {
			t.Fatalf("p.Section(%q) = %#v, want %#v", PreambleHeading, sec, want)
		}
	})

	t.Run("page opening with heading has empty lead region", func(t *testing.T) {
		body := "## Abstract\n\nX.\n"
		p := &Page{
			Path:     "wiki/concepts/test.md",
			Body:     body,
			Sections: ParseSections(body),
		}
		sec, ok := p.Section(PreambleHeading)
		if !ok {
			t.Fatalf("p.Section(%q) ok = false, want true", PreambleHeading)
		}
		want := Section{
			Level:   0,
			Heading: "(preamble)",
			Start:   0,
			Body:    0,
			End:     0,
		}
		if sec != want {
			t.Fatalf("p.Section(%q) = %#v, want %#v", PreambleHeading, sec, want)
		}
	})

	t.Run("page with no headings has lead region covering whole body", func(t *testing.T) {
		body := "Just one lead paragraph, no headings at all.\n"
		p := &Page{
			Path:     "wiki/concepts/test.md",
			Body:     body,
			Sections: ParseSections(body),
		}
		if len(p.Sections) != 0 {
			t.Fatalf("len(Sections) = %d, want 0", len(p.Sections))
		}
		sec, ok := p.Section(PreambleHeading)
		if !ok {
			t.Fatalf("p.Section(%q) ok = false, want true", PreambleHeading)
		}
		want := Section{
			Level:   0,
			Heading: "(preamble)",
			Start:   0,
			Body:    0,
			End:     len(body),
		}
		if sec != want {
			t.Fatalf("p.Section(%q) = %#v, want %#v", PreambleHeading, sec, want)
		}
	})
}

// TestPreambleSectionOps pins workflow 081, R3: section operations work on the preamble
// keeping exactly one blank line between the lead and the first heading.
func TestPreambleSectionOps(t *testing.T) {
	body := "Lead para.\n\n## Abstract\n\nX.\n"
	p := &Page{
		Path:     "wiki/concepts/test.md",
		Body:     body,
		Sections: ParseSections(body),
	}
	sec, ok := p.Section(PreambleHeading)
	if !ok {
		t.Fatalf("p.Section(%q) ok = false", PreambleHeading)
	}

	t.Run("ReplaceSection", func(t *testing.T) {
		got := ReplaceSection(body, sec, "Fresh lead.\n\n")
		want := "Fresh lead.\n\n## Abstract\n\nX.\n"
		if got != want {
			t.Fatalf("ReplaceSection =\n%q\nwant\n%q", got, want)
		}
	})

	t.Run("AppendToSection", func(t *testing.T) {
		got := AppendToSection(body, sec, "More.\n\n")
		want := "Lead para.\n\nMore.\n\n## Abstract\n\nX.\n"
		if got != want {
			t.Fatalf("AppendToSection =\n%q\nwant\n%q", got, want)
		}
	})

	t.Run("InsertAfterSection", func(t *testing.T) {
		got := InsertAfterSection(body, sec, "## Overview\n\nO.")
		want := "Lead para.\n\n## Overview\n\nO.\n\n## Abstract\n\nX.\n"
		if got != want {
			t.Fatalf("InsertAfterSection =\n%q\nwant\n%q", got, want)
		}
	})

	t.Run("RemoveSection", func(t *testing.T) {
		got := RemoveSection(body, sec)
		want := "## Abstract\n\nX.\n"
		if got != want {
			t.Fatalf("RemoveSection =\n%q\nwant\n%q", got, want)
		}
	})
}
