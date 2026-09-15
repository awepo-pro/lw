package markdown

import (
	"sync"
	"testing"
)

// TestRenderConcurrentAccess exercises the mutex-guarded LRU from many
// goroutines at once (contract §2: "Renderer ... memoized"); run under
// -race this is the test that would catch an unguarded cache access.
func TestRenderConcurrentAccess(t *testing.T) {
	r := NewRenderer()
	srcs := [][]byte{
		[]byte("# One\n\nBody one.\n"),
		[]byte("# Two\n\nBody two with `code` and [[wikilink]].\n"),
		[]byte("---\ntitle: Three\n---\n# Three\n\nBody three.\n"),
	}

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			src := srcs[i%len(srcs)]
			opts := Options{Width: 60 + i%20, Style: testStyle, Plain: i%2 == 0}
			if _, err := r.Render(src, opts); err != nil {
				t.Errorf("Render: %v", err)
			}
		}(i)
	}
	wg.Wait()
}
