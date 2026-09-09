package browse

import (
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// TestTruncateBodyLeavesShortBodyUnchanged: a body at or under
// previewMaxLines is untouched — no marker, no truncation.
func TestTruncateBodyLeavesShortBodyUnchanged(t *testing.T) {
	body := "line1\nline2\nline3\n"
	if got := truncateBody(body); got != body {
		t.Fatalf("truncateBody(%q) = %q, want it unchanged", body, got)
	}
}

// TestTruncateBodyMarksOverflowOnTheLongPageFixture is s4-tui.md S4-T4's
// pinned expectation: "preview of a 250-line page truncates rather than
// blocking", proved against the real fixture — 250 file lines, well past
// previewMaxLines once the frontmatter is stripped.
func TestTruncateBodyMarksOverflowOnTheLongPageFixture(t *testing.T) {
	root := testutil.FixtureRoot(t)
	src := filepath.Join(root, "dirty", "wiki", "concepts", "long-page.md")
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	page, err := vault.ParsePage("wiki/concepts/long-page.md", b)
	if err != nil {
		t.Fatalf("ParsePage: %v", err)
	}

	got := truncateBody(page.Body)
	if !strings.Contains(got, "more lines") {
		t.Fatalf("truncateBody(long-page.md's body) has no truncation marker:\n%s", got)
	}
	gotLines := strings.Split(got, "\n")
	// previewMaxLines kept lines, plus a blank separator and the marker line.
	if len(gotLines) > previewMaxLines+3 {
		t.Fatalf("truncateBody kept %d lines, want at most %d", len(gotLines), previewMaxLines+3)
	}
}

// TestPreviewCacheHitsOnSecondRender is s4-tui.md S4-T4 item 8: a second call
// at the same (path, width) must not invoke renderFn again.
func TestPreviewCacheHitsOnSecondRender(t *testing.T) {
	cache := newPreviewCache()
	var calls int
	renderFn := func(src string) string {
		calls++
		return "RENDERED:" + src
	}

	first := cache.render("wiki/concepts/kv-cache.md", 80, "hello", renderFn)
	second := cache.render("wiki/concepts/kv-cache.md", 80, "hello", renderFn)

	if calls != 1 {
		t.Fatalf("renderFn called %d times across two identical (path,width) renders, want 1", calls)
	}
	if first != second {
		t.Fatalf("render() = %q then %q, want the cached value both times", first, second)
	}
}

// TestPreviewCacheDistinguishesWidth: a different width is a different cache
// entry — reflow depends on width, so this must not be a cache hit.
func TestPreviewCacheDistinguishesWidth(t *testing.T) {
	cache := newPreviewCache()
	var calls int
	renderFn := func(src string) string { calls++; return src }

	cache.render("p", 80, "x", renderFn)
	cache.render("p", 100, "x", renderFn)

	if calls != 2 {
		t.Fatalf("renderFn called %d times for two different widths, want 2", calls)
	}
}

// TestPreviewCacheInvalidate: after invalidate, the next render call misses
// the cache again (s4-tui.md S4-T4 items 6, 8).
func TestPreviewCacheInvalidate(t *testing.T) {
	cache := newPreviewCache()
	var calls int
	renderFn := func(src string) string { calls++; return src }

	cache.render("p", 80, "x", renderFn)
	cache.invalidate()
	cache.render("p", 80, "x", renderFn)

	if calls != 2 {
		t.Fatalf("renderFn called %d times across an invalidate, want 2", calls)
	}
}

// TestGlamourStylePicksDarkOrLight: there is no glamour.WithAutoStyle in v2
// (s4-tui.md S4-T4 item 5) — the caller must choose explicitly.
func TestGlamourStylePicksDarkOrLight(t *testing.T) {
	if got := glamourStyle(true); got != "dark" {
		t.Errorf("glamourStyle(true) = %q, want %q", got, "dark")
	}
	if got := glamourStyle(false); got != "light" {
		t.Errorf("glamourStyle(false) = %q, want %q", got, "light")
	}
}

// TestRenderMarkdownRejectsUnknownStyle exercises the error path
// Model.renderBody's fallback depends on.
func TestRenderMarkdownRejectsUnknownStyle(t *testing.T) {
	if _, err := renderMarkdown("not-a-real-glamour-style", 80, "# hi"); err == nil {
		t.Fatal("renderMarkdown with an unknown style returned no error")
	}
}

// TestRenderBodyTruncatesAndCachesLongPage is s4-tui.md S4-T4's pinned
// expectation, run through the real Model and the real glamour renderer:
// the 250-line dirty page's preview carries the truncation marker, and
// rendering it twice at the same width calls the renderer only once.
func TestRenderBodyTruncatesAndCachesLongPage(t *testing.T) {
	d, engine := newTestDeps(t, "dirty")
	defer engine.Close()

	m := New(d).(*Model)
	page, ok := d.Engine.Vault().Page("wiki/concepts/long-page.md")
	if !ok {
		t.Fatal("wiki/concepts/long-page.md not found in the dirty fixture's vault")
	}

	var calls int
	real := m.renderMarkdownFn
	m.renderMarkdownFn = func(style string, width int, src string) (string, error) {
		calls++
		return real(style, width, src)
	}

	first := m.renderBody(page.Path, 80, page.Body)
	second := m.renderBody(page.Path, 80, page.Body)

	if calls != 1 {
		t.Fatalf("glamour renderer ran %d times for two renderBody calls at the same (path, width), want 1", calls)
	}
	if first != second {
		t.Fatal("renderBody output changed between two calls at the same size, want the cached value both times")
	}
	if !strings.Contains(first, "more") || !strings.Contains(first, "lines") {
		t.Fatalf("renderBody(long-page.md) does not carry the truncation marker:\n%s", first)
	}
}

// TestRenderBodyFallsBackToPlainTextOnRendererError: a broken renderFn must
// never surface as a panic or an empty preview (00-conventions.md §2).
func TestRenderBodyFallsBackToPlainTextOnRendererError(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	m := New(d).(*Model)
	m.renderMarkdownFn = func(style string, width int, src string) (string, error) {
		return "", errBoom
	}

	got := m.renderBody("wiki/concepts/kv-cache.md", 80, "# KV Cache\n\nplain body\n")
	if !strings.Contains(got, "plain body") {
		t.Fatalf("renderBody with a failing renderer = %q, want the plain body preserved", got)
	}
}

// TestBackgroundColorMsgInvalidatesPreviewCache is s4-tui.md S4-T4 item 6
// (C-81): a polarity flip must drop cached renders, since the same body now
// renders with different colors.
func TestBackgroundColorMsgInvalidatesPreviewCache(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	m := New(d).(*Model)
	if !m.selectPath("wiki/concepts/kv-cache.md") {
		t.Fatal("selectPath: kv-cache.md not found")
	}

	var calls int
	real := m.renderMarkdownFn
	m.renderMarkdownFn = func(style string, width int, src string) (string, error) {
		calls++
		return real(style, width, src)
	}

	m.View(80, 24)
	m.View(80, 24)
	if calls != 1 {
		t.Fatalf("calls = %d before any BackgroundColorMsg, want 1", calls)
	}

	wasDark := m.deps.Theme.IsDark
	newColor := color.White
	if wasDark {
		// Flip to the opposite polarity from whatever LoadTheme("") defaulted to.
		newColor = color.White
	} else {
		newColor = color.Black
	}
	next, _ := m.Update(tea.BackgroundColorMsg{Color: newColor})
	m2 := next.(*Model)

	if m2.deps.Theme.IsDark == wasDark {
		t.Fatalf("Theme.IsDark unchanged (%v) after a polarity-flipping BackgroundColorMsg", wasDark)
	}

	m2.View(80, 24)
	if calls != 2 {
		t.Fatalf("calls = %d after the polarity flip, want 2 (cache must be invalidated)", calls)
	}
}

// errBoom is a sentinel used only to exercise renderBody's error fallback.
var errBoom = renderErr("boom")

type renderErr string

func (e renderErr) Error() string { return string(e) }
