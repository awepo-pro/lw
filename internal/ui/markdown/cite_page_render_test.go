package markdown

// cite_page_render_test.go is 034 T4's render pin: a paged provenance
// marker — "^[raw/papers/x.md p.12]" — renders in Browse exactly like the
// unpaged form, as the basename plus the page spec in brackets. The marker
// rewrite consumes everything between "^[" and "]" wholesale, so the page
// spec rides along; this pin holds that in place against a future rewrite
// that special-cases the space.

import (
	"strings"
	"testing"
)

func TestRenderPagedProvenance(t *testing.T) {
	src := []byte("Claim.^[raw/papers/x.md p.12]\n")
	r := NewRenderer()
	lines, err := r.Render(src, Options{Width: 60, Plain: true, Style: testStyle})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := normalizeBody(lines)
	if !strings.Contains(got, "[x.md p.12]") {
		t.Errorf("output %q does not contain [x.md p.12]", got)
	}
	if strings.Contains(got, "raw/papers") {
		t.Errorf("output %q still contains the provenance path", got)
	}
	if strings.Contains(got, "^[") {
		t.Errorf("output %q still contains a raw provenance marker", got)
	}
}
