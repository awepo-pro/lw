package markdown

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestRenderFragment pins the fragment entry point (workflow 005 contract
// §1): Render's page shape — frontmatter split, page header, full-cell
// padding, diff gutter — never applies, while wrap, measure, palette and
// plain handling stay exactly the page path's.
func TestRenderFragment(t *testing.T) {
	t.Run("no_page_header", func(t *testing.T) {
		src := []byte("---\ntitle: x\n---\n\n# H\n")
		o := Options{Width: 80, Style: darkStyle}
		r := NewRenderer()

		page, err := r.Render(src, o)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if got := stripGutterAndTrim(page[0]); got != "x" {
			t.Fatalf("Render page[0] = %q, want the pageHeader title row %q", got, "x")
		}

		frag, err := r.RenderFragment(src, o)
		if err != nil {
			t.Fatalf("RenderFragment: %v", err)
		}
		for i, l := range frag {
			if got := strings.TrimSpace(ansi.Strip(l)); got == "x" {
				t.Errorf("fragment line %d is the pageHeader title row %q", i+1, got)
			}
		}
		if !slices.ContainsFunc(frag, func(l string) bool {
			return strings.TrimSpace(ansi.Strip(l)) == "H"
		}) {
			t.Errorf("fragment output %q lost the # H heading text", frag)
		}
	})

	t.Run("frontmatter_is_not_split", func(t *testing.T) {
		src := []byte("---\ntitle: x\n---\n\n# H\n")
		frag, err := NewRenderer().RenderFragment(src, Options{Width: 80, Style: darkStyle})
		if err != nil {
			t.Fatalf("RenderFragment: %v", err)
		}
		found := false
		for _, l := range frag {
			if strings.Contains(ansi.Strip(l), "title: x") {
				found = true
			}
		}
		if !found {
			t.Errorf("the leading --- bytes were consumed as frontmatter: fragment %q has no \"title: x\" content line", frag)
		}
	})

	t.Run("no_padding_to_cell_width", func(t *testing.T) {
		src := []byte("hi")
		o := Options{Width: 80, Style: darkStyle}
		r := NewRenderer()

		frag, err := r.RenderFragment(src, o)
		if err != nil {
			t.Fatalf("RenderFragment: %v", err)
		}
		if len(frag) == 0 {
			t.Fatal("fragment returned no lines")
		}
		if w := ansi.StringWidth(ansi.Strip(frag[0])); w == 0 || w >= 80 {
			t.Errorf("fragment line width = %d, want the word's own width (well under 80)", w)
		}

		page, err := r.Render(src, o)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		for i, l := range page {
			if w := ansi.StringWidth(l); w != 80 {
				t.Errorf("page line %d width = %d, want exactly the padded 80", i+1, w)
			}
		}
	})

	t.Run("width_still_wraps", func(t *testing.T) {
		src := []byte(strings.Repeat("answer word ", 25))
		frag, err := NewRenderer().RenderFragment(src, Options{Width: 40, Style: darkStyle})
		if err != nil {
			t.Fatalf("RenderFragment: %v", err)
		}
		if len(frag) <= 1 {
			t.Fatalf("got %d lines, want the paragraph wrapped onto several", len(frag))
		}
		for i, l := range frag {
			if w := ansi.StringWidth(ansi.Strip(l)); w > 40 {
				t.Errorf("line %d width = %d, exceeds Width 40: %q", i+1, w, l)
			}
		}
	})

	t.Run("changed_is_ignored", func(t *testing.T) {
		changed := "This block was added by the change."
		src := []byte(changed + "\n")
		o := Options{Width: 60, Style: darkStyle, Changed: []string{changed}}
		r := NewRenderer()

		frag, err := r.RenderFragment(src, o)
		if err != nil {
			t.Fatalf("RenderFragment: %v", err)
		}
		if strings.Contains(strings.Join(frag, "\n"), "▎") {
			t.Errorf("fragment emitted a ▎ gutter; Changed must be ignored: %q", frag)
		}

		// Contrast: the same Changed set still gutters the page path.
		page, err := r.Render(src, o)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if !strings.Contains(strings.Join(page, "\n"), "▎") {
			t.Errorf("page lost its ▎ gutter; the contrast is broken: %q", page)
		}
	})

	t.Run("empty_src_is_empty_non_nil", func(t *testing.T) {
		for _, src := range [][]byte{nil, {}} {
			lines, err := NewRenderer().RenderFragment(src, Options{Width: 80, Style: darkStyle})
			if err != nil {
				t.Errorf("RenderFragment(%q) error: %v", src, err)
			}
			if lines == nil {
				t.Errorf("RenderFragment(%q) returned a nil slice, want empty non-nil", src)
			}
			if len(lines) != 0 {
				t.Errorf("RenderFragment(%q) returned %d lines, want 0", src, len(lines))
			}
		}
	})

	t.Run("plain_strips_every_escape", func(t *testing.T) {
		src := []byte("# Head\n\nSome **bold**, *emph* and `code`.\n\n```go\nx := 1\n```\n")
		frag, err := NewRenderer().RenderFragment(src, Options{Width: 60, Style: darkStyle, Plain: true})
		if err != nil {
			t.Fatalf("RenderFragment: %v", err)
		}
		if len(frag) == 0 {
			t.Fatal("no lines returned")
		}
		for i, l := range frag {
			if ansi.Strip(l) != l {
				t.Errorf("line %d keeps an escape: %q", i+1, l)
			}
		}
	})

	t.Run("heading_colour_matches_page", func(t *testing.T) {
		src := []byte("## Heading\n")
		o := Options{Width: 80, Style: darkStyle}
		r := NewRenderer()

		frag, err := r.RenderFragment(src, o)
		if err != nil {
			t.Fatalf("RenderFragment: %v", err)
		}
		page, err := r.Render(src, o)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}

		// The SGR in force on the heading's cells, read the way this
		// package's colour tests read it — not "the sequence appears
		// somewhere". H2 is Accent + bold in both entry points.
		want := roleSGR(darkStyle.Accent, ";1")
		gotFrag := activeSGR(strings.Join(frag, "\n"), "Heading")
		gotPage := activeSGR(strings.Join(page, "\n"), "Heading")
		if gotFrag != want {
			t.Errorf("fragment heading SGR = %q, want %q", gotFrag, want)
		}
		if gotPage != want {
			t.Errorf("page heading SGR = %q, want %q", gotPage, want)
		}
		if gotFrag != gotPage {
			t.Errorf("fragment SGR %q != page SGR %q — two palettes, the bug 005 exists to kill", gotFrag, gotPage)
		}
	})

	t.Run("unset_tokens_never_black", func(t *testing.T) {
		st := Style{Dark: true, Fg: "#D8DDE4"} // Heading and Code deliberately EMPTY
		r := NewRenderer()

		for _, src := range []string{"## H\n", "```go\nx := 1\n```\n"} {
			frag, err := r.RenderFragment([]byte(src), Options{Width: 60, Style: st})
			if err != nil {
				t.Fatalf("RenderFragment(%q): %v", src, err)
			}
			if joined := strings.Join(frag, "\n"); strings.Contains(joined, "38;2;0;0;0") {
				t.Errorf("source %q rendered black somewhere (ORCH-16): %q", src, joined)
			}
		}

		// resolved() degrades the unset Heading token to Fg — a real colour,
		// never black and never unstyled.
		frag, err := r.RenderFragment([]byte("## H\n"), Options{Width: 60, Style: st})
		if err != nil {
			t.Fatalf("RenderFragment: %v", err)
		}
		if active := activeSGR(strings.Join(frag, "\n"), "H"); active != roleSGR(st.Fg, ";1") {
			t.Errorf("unset Heading degraded to %q, want Fg+bold %q", active, roleSGR(st.Fg, ";1"))
		}
	})
}

// TestFragmentCacheKey pins the cache-key rule (workflow 005 contract §1
// note 3): the fragment/page distinction lives in the key.
func TestFragmentCacheKey(t *testing.T) {
	t.Run("fragment_and_page_do_not_collide", func(t *testing.T) {
		r := NewRenderer()
		src := []byte("# Title\n\nSome body text.\n")
		o := Options{Width: 40, Style: darkStyle}

		page1, err := r.Render(src, o)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		frag, err := r.RenderFragment(src, o)
		if err != nil {
			t.Fatalf("RenderFragment: %v", err)
		}
		page2, err := r.Render(src, o)
		if err != nil {
			t.Fatalf("Render again: %v", err)
		}

		if slices.Equal(page1, frag) {
			t.Fatal("fragment result equals the page result — the two entry points share one cache key")
		}
		if !slices.Equal(page1, page2) {
			t.Fatal("the fragment render evicted or overwrote the page's cache entry")
		}

		// The concrete difference the contract names: the page pads every
		// line to the full cell width, the fragment does not.
		if w := ansi.StringWidth(page1[0]); w != 40 {
			t.Errorf("page line width = %d, want the padded 40", w)
		}
		if w := ansi.StringWidth(ansi.Strip(frag[0])); w >= 40 {
			t.Errorf("fragment line width = %d, want under the 40-cell page width", w)
		}
	})

	t.Run("two_fragments_same_bytes_hit", func(t *testing.T) {
		r := NewRenderer()
		src := []byte("Hello fragment.\n")
		o := Options{Width: 60, Style: darkStyle}

		a, err := r.RenderFragment(src, o)
		if err != nil {
			t.Fatalf("RenderFragment: %v", err)
		}
		b, err := r.RenderFragment(src, o)
		if err != nil {
			t.Fatalf("RenderFragment again: %v", err)
		}
		if len(a) == 0 {
			t.Fatal("no lines returned")
		}
		if !slices.Equal(a, b) {
			t.Errorf("two identical RenderFragment calls differ — the memo is bypassed")
		}
	})
}
