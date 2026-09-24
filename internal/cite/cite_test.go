package cite

import (
	"strconv"
	"strings"
	"testing"
)

// wantMalformed builds the exact malformed-page Err for inner, the way the
// 034 T1 contract pins it: <i> verbatim, <source> as the literal template.
func wantMalformed(inner string) string {
	return `malformed page citation "^[` + inner + `]": write ^[<source> p.N] or ^[<source> p.N-M]`
}

func TestParse(t *testing.T) {
	rows := []struct {
		inner string
		want  Cite
	}{
		// well-formed: no page, page, range, and a non-raw source without a page
		{"raw/papers/x.md", Cite{Source: "raw/papers/x.md"}},
		{"raw/papers/x.md p.12", Cite{Source: "raw/papers/x.md", From: 12, To: 12}},
		{"raw/papers/x.md p.12-13", Cite{Source: "raw/papers/x.md", From: 12, To: 13}},
		{"wiki/concepts/y.md", Cite{Source: "wiki/concepts/y.md"}},

		// malformed: exact Err, Source still the text before the first space
		{"raw/x.md p. 12", Cite{Source: "raw/x.md", Err: wantMalformed("raw/x.md p. 12")}},
		{"raw/x.md pp.12-13", Cite{Source: "raw/x.md", Err: wantMalformed("raw/x.md pp.12-13")}},
		{"raw/x.md page 12", Cite{Source: "raw/x.md", Err: wantMalformed("raw/x.md page 12")}},
		{"raw/x.md p.0", Cite{Source: "raw/x.md", Err: wantMalformed("raw/x.md p.0")}},
		{"raw/x.md p.12 ", Cite{Source: "raw/x.md", Err: wantMalformed("raw/x.md p.12 ")}},
		{"raw/x.md, p.12", Cite{Source: "raw/x.md,", Err: wantMalformed("raw/x.md, p.12")}},

		// ranges must ascend
		{"raw/x.md p.13-12", Cite{Source: "raw/x.md", Err: "page range p.13-12 must ascend"}},
		{"raw/x.md p.12-12", Cite{Source: "raw/x.md", Err: "page range p.12-12 must ascend"}},

		// pages are raw/-only, and paged sources must end in ".md"
		// (A-034-4): "raw/x.md," and "raw/x.pdf" are prose leakage, reported
		// not guessed at
		{"wiki/y.md p.3", Cite{Source: "wiki/y.md", Err: "only raw/ sources take a page"}},
		{"raw/x.pdf p.3", Cite{Source: "raw/x.pdf", Err: wantMalformed("raw/x.pdf p.3")}},
		{"raw/x.md, p.12", Cite{Source: "raw/x.md,", Err: wantMalformed("raw/x.md, p.12")}},
	}
	for _, r := range rows {
		got := Parse(r.inner)
		if got != r.want {
			t.Errorf("Parse(%q) = %#v, want %#v", r.inner, got, r.want)
		}
	}
}

func TestScanSkipsCode(t *testing.T) {
	text := "before ^[raw/a.md p.1] mid\n" +
		"```go\n" +
		"in fence ^[raw/b.md]\n" +
		"```\n" +
		"after fence ^[raw/c.md]\n" +
		"~~~\n" +
		"tilde fence ^[raw/d.md]\n" +
		"~~~\n" +
		"inline `code ^[raw/e.md]` tail ^[raw/f.md p.2]\n" +
		"unterminated ^[raw/g.md\n" +
		"next ^[raw/h.md]"

	got := Scan(text)
	wantSources := []string{"raw/a.md", "raw/c.md", "raw/f.md", "raw/h.md"}
	if len(got) != len(wantSources) {
		t.Fatalf("Scan returned %d cites %+v, want %d (%v)",
			len(got), got, len(wantSources), wantSources)
	}
	for i, src := range wantSources {
		if got[i].Source != src {
			t.Errorf("Scan[%d].Source = %q, want %q", i, got[i].Source, src)
		}
		if got[i].Raw != "^["+src+"]" && i != 0 && i != 2 {
			t.Errorf("Scan[%d].Raw = %q, want ^[%s]", i, got[i].Raw, src)
		}
		wantOff := strings.Index(text, "^["+src)
		if got[i].Offset != wantOff {
			t.Errorf("Scan[%d] (%s).Offset = %d, want %d", i, src, got[i].Offset, wantOff)
		}
	}
	// Raw is the full marker text, Offset points at the "^"
	if got[0].Raw != "^[raw/a.md p.1]" || got[2].Raw != "^[raw/f.md p.2]" {
		t.Errorf("Raw of paged markers: %q, %q", got[0].Raw, got[2].Raw)
	}
	if got[0].From != 1 || got[0].To != 1 || got[2].From != 2 || got[2].To != 2 {
		t.Errorf("paged cites lost their pages: %+v %+v", got[0], got[2])
	}
}

func TestStringRoundTrip(t *testing.T) {
	inners := []string{
		"raw/papers/x.md",
		"raw/papers/x.md p.12",
		"raw/papers/x.md p.12-13",
		"wiki/concepts/y.md",
	}
	for _, inner := range inners {
		if got := Parse(inner).String(); got != "^["+inner+"]" {
			t.Errorf("Parse(%q).String() = %q, want %q", inner, got, "^["+inner+"]")
		}
	}
	// a Cite with an Err has no canonical form: Raw comes back
	bad := Parse("raw/x.md p.0")
	bad.Raw = "^[raw/x.md p.0]"
	if got := bad.String(); got != "^[raw/x.md p.0]" {
		t.Errorf("Cite{Err}.String() = %q, want the Raw verbatim", got)
	}
}

func TestPagesAndPageAt(t *testing.T) {
	var b strings.Builder
	anchorAt := map[int]int{} // page -> byte offset of its anchor line
	for p := 1; p <= 4; p++ {
		anchorAt[p] = b.Len()
		b.WriteString("<!-- page " + strconv.Itoa(p) + " -->\n\n")
		b.WriteString(strings.Repeat("a", 100))
		b.WriteString("\n")
	}
	body := b.String()

	gotPages := Pages(body)
	if len(gotPages) != 4 || gotPages[0] != 1 || gotPages[1] != 2 || gotPages[2] != 3 || gotPages[3] != 4 {
		t.Errorf("Pages(body) = %v, want [1 2 3 4]", gotPages)
	}
	if got := PageAt(body, 0); got != 1 {
		t.Errorf("PageAt(body, 0) = %d, want 1", got)
	}
	if got := PageAt(body, anchorAt[3]); got != 3 {
		t.Errorf("PageAt(body, start of anchor 3) = %d, want 3", got)
	}
	if got := PageAt(body, len(body)-1); got != 4 {
		t.Errorf("PageAt(body, len-1) = %d, want 4", got)
	}

	bare := "no anchors here\njust prose\n"
	if got := Pages(bare); len(got) != 0 {
		t.Errorf("Pages(bare) = %v, want empty", got)
	}
	if got := PageAt(bare, 5); got != 0 {
		t.Errorf("PageAt(bare, 5) = %d, want 0", got)
	}

	// trailing text on the line: not an anchor
	noisy := "text\n<!-- page 2 --> trailing\nmore\n"
	if got := Pages(noisy); len(got) != 0 {
		t.Errorf("Pages(noisy) = %v, want empty (trailing text breaks the anchor)", got)
	}

	// a CRLF body: the "\r" before "\n" must not hide the anchor; PageAt
	// offsets still count the "\r" bytes
	crlf := "<!-- page 1 -->\r\nbody\r\n<!-- page 2 -->\r\nmore\r\n"
	if gotPages := Pages(crlf); len(gotPages) != 2 || gotPages[0] != 1 || gotPages[1] != 2 {
		t.Errorf("Pages(crlf) = %v, want [1 2]", gotPages)
	}
	if got := PageAt(crlf, len(crlf)-1); got != 2 {
		t.Errorf("PageAt(crlf, end) = %d, want 2", got)
	}
	if got := PageAt(crlf, 0); got != 1 {
		t.Errorf("PageAt(crlf, 0) = %d, want 1", got)
	}
}
