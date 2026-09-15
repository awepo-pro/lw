package markdown

import (
	"sync"
	"testing"
)

// TestRenderConcurrentAccess exercises the mutex-guarded LRU from many
// goroutines at once (contract §2: "Renderer ... memoized"); run under
// -race this is the test that would catch an unguarded cache access. Half
// the goroutines use a second Style whose Heading/Code differ, so the
// per-Style chroma style registration (chroma.go's registry lock) races
// too: distinct palettes must be able to register and render concurrently
// without a data race in chroma's process-global registry.
func TestRenderConcurrentAccess(t *testing.T) {
	other := testStyle
	other.Heading = "#7A45C2" // the light palette's tokens: a distinct
	other.Code = "#17727A"    // chroma style, registered under its own name
	styles := []Style{testStyle, other}

	r := NewRenderer()
	srcs := [][]byte{
		[]byte("# One\n\nBody one.\n"),
		[]byte("# Two\n\nBody two with `code` and [[wikilink]].\n"),
		[]byte("---\ntitle: Three\n---\n# Three\n\nBody three.\n"),
		[]byte("```go\nfunc f() int { return 2 } // c\n```\n"),
	}

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			src := srcs[i%len(srcs)]
			opts := Options{Width: 60 + i%20, Style: styles[i%len(styles)], Plain: i%2 == 0}
			if _, err := r.Render(src, opts); err != nil {
				t.Errorf("Render: %v", err)
			}
		}(i)
	}
	wg.Wait()
}
