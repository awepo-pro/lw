package vault

import "testing"

// TestReplaceTextInSection pins 043 T1's byte-offset primitive: the count
// returned is find's non-overlapping occurrences inside sec's range
// [sec.Body, sec.End) — subsections included — and the replacement happens
// only at count exactly 1, so an absent or ambiguous match returns the body
// unchanged and the caller decides. Bytes outside the range are never
// touched even when find occurs there too: a match in a sibling section or
// before the first heading must not leak into a section-scoped edit.
func TestReplaceTextInSection(t *testing.T) {
	body := "head FIND\n" +
		"## S\n" +
		"one FIND two\n" +
		"## T\n" +
		"FIND thrice FIND\n"
	secs := ParseSections(body)
	if len(secs) != 2 {
		t.Fatalf("ParseSections found %d sections, want 2", len(secs))
	}
	s, ts := secs[0], secs[1]

	t.Run("count_zero_returns_body", func(t *testing.T) {
		got, n := ReplaceTextInSection(body, s, "absent", "X")
		if n != 0 {
			t.Fatalf("count = %d, want 0", n)
		}
		if got != body {
			t.Fatalf("count 0 changed the body:\n%s", got)
		}
	})

	t.Run("count_one_replaces_inside_section", func(t *testing.T) {
		got, n := ReplaceTextInSection(body, s, "one FIND two", "one X two")
		if n != 1 {
			t.Fatalf("count = %d, want 1", n)
		}
		want := "head FIND\n## S\none X two\n## T\nFIND thrice FIND\n"
		if got != want {
			t.Fatalf("got:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("count_two_returns_body", func(t *testing.T) {
		got, n := ReplaceTextInSection(body, ts, "FIND", "X")
		if n != 2 {
			t.Fatalf("count = %d, want 2 — strings.Count's non-overlapping scan", n)
		}
		if got != body {
			t.Fatalf("count 2 changed the body:\n%s", got)
		}
	})

	t.Run("match_outside_section_never_touched", func(t *testing.T) {
		// FIND sits before the first heading, once inside S, and twice
		// in T; the section-scoped count is 1 and only S's occurrence
		// may move.
		got, n := ReplaceTextInSection(body, s, "FIND", "X")
		if n != 1 {
			t.Fatalf("count = %d, want 1", n)
		}
		want := "head FIND\n## S\none X two\n## T\nFIND thrice FIND\n"
		if got != want {
			t.Fatalf("got:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("count_is_non_overlapping", func(t *testing.T) {
		// "aa" in "aaaa": an overlapping scan would say 3, a
		// non-overlapping one 2 — and 2 means no replacement.
		overlapped := "## S\naaaa\n"
		sec := ParseSections(overlapped)[0]
		got, n := ReplaceTextInSection(overlapped, sec, "aa", "X")
		if n != 2 {
			t.Fatalf("count = %d, want 2 — non-overlapping, not 3", n)
		}
		if got != overlapped {
			t.Fatalf("count-scan replaced at a non-counted offset:\n%s", got)
		}
	})
}
