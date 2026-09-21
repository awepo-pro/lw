package markdown

import (
	"fmt"
	"strings"
	"testing"
)

// benchFenceSrc builds the page both render benchmarks use: 100 fenced
// code blocks over a fixed rotation of ten language tags, six code lines
// each, no randomness anywhere. The rotation weights the three fence
// paths by what the lexer memo (lexercache.go) costs them: half the tags
// are "notalang", which has no lexer anywhere in chroma's maps and so
// pays lexers.Get's full filename glob scan (~4.7 ms on this machine,
// against ~0 µs for an alias hit) every fence before the memo — that
// unknown-tag fence page is the measured motivating case, where the
// lookup was ~82% of a cold render. A fifth of the tags are "txt", a
// real tag chroma resolves only via that same extension scan (~3.1 ms);
// the rest are plain alias hits (go, sh, js, makefile) so the
// highlighted-render pipeline still runs. Tags that miss everywhere but
// glamour's fallback never reach chroma's highlighter, which is why the
// memo alone removes their whole lookup cost — quick.Highlight calls
// lexers.Get a second time per highlighted fence, and only that Get is
// left for "txt" after the memo.
func benchFenceSrc() []byte {
	tags := []string{"notalang", "txt", "notalang", "go", "notalang", "sh", "notalang", "js", "notalang", "makefile"}
	var b strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&b, "```%s\n", tags[i%len(tags)])
		for j := 0; j < 6; j++ {
			fmt.Fprintf(&b, "const sample_%d_%d = token(%d, \"x\") // line\n", i, j, j)
		}
		b.WriteString("```\n\n")
	}
	return []byte(b.String())
}

// BenchmarkRenderCold100 measures a cold render of the 100-fence page: a
// fresh Renderer every iteration, so the rendered-lines LRU (cache.go)
// never serves and every op pays the block pipeline — glamour setup,
// chroma highlighting, the layout passes. The lexer memo (lexercache.go)
// is process-global by design, so after the first op every tag is a map
// hit and the reported number is the steady-state cold render cost,
// exactly what a browse view pays for each new page. For the before/after
// contrast run this benchmark on the change's parent commit too. Not a
// CI gate.
func BenchmarkRenderCold100(b *testing.B) {
	src := benchFenceSrc()
	opts := Options{Width: 80, Style: testStyle}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := NewRenderer()
		if _, err := r.Render(src, opts); err != nil {
			b.Fatalf("Render: %v", err)
		}
	}
}

// BenchmarkRenderWarm100 is the floor: one Renderer, the same page every
// iteration, so every op after the first is a rendered-lines LRU hit
// (cache.go) and no block pipeline runs at all. The gap between this and
// BenchmarkRenderCold100 is the cost of rendering itself — which the
// lexer memo narrows (first-sight lookups only) but cannot remove.
func BenchmarkRenderWarm100(b *testing.B) {
	src := benchFenceSrc()
	opts := Options{Width: 80, Style: testStyle}

	r := NewRenderer()
	if _, err := r.Render(src, opts); err != nil {
		b.Fatalf("Render: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := r.Render(src, opts); err != nil {
			b.Fatalf("Render: %v", err)
		}
	}
}
