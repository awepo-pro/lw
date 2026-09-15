package markdown

import (
	"container/list"
	"crypto/sha256"
	"strings"
	"sync"
)

// cacheCapacity is the renderer's bounded LRU size (contract §2: Renderer).
const cacheCapacity = 64

// memoKey is Render's cache key (contract §2 note 6): sha256(src), the
// layout inputs that change line width or wrap, Dark and Plain, and
// sha256(join(Changed)) so two different change sets never collide.
type memoKey struct {
	srcHash     [32]byte
	width       int
	measure     int
	dark        bool
	plain       bool
	changedHash [32]byte
}

func newMemoKey(src []byte, o Options) memoKey {
	return memoKey{
		srcHash:     sha256.Sum256(src),
		width:       o.Width,
		measure:     o.Measure,
		dark:        o.Style.Dark,
		plain:       o.Plain,
		changedHash: sha256.Sum256([]byte(strings.Join(o.Changed, "\n"))),
	}
}

// renderCache is a mutex-guarded, bounded LRU of rendered line slices.
// Both get and put copy the slice, so a caller mutating a returned result
// (or a slice it once passed in) never reaches another caller's copy or
// the cache's own entry.
type renderCache struct {
	mu    sync.Mutex
	items map[memoKey]*list.Element
	order *list.List // front = most recently used
}

type cacheEntry struct {
	key   memoKey
	lines []string
}

func newRenderCache() *renderCache {
	return &renderCache{
		items: make(map[memoKey]*list.Element),
		order: list.New(),
	}
}

func (c *renderCache) get(key memoKey) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return cloneLines(el.Value.(*cacheEntry).lines), true
}

func (c *renderCache) put(key memoKey, lines []string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[key]; ok {
		el.Value.(*cacheEntry).lines = cloneLines(lines)
		c.order.MoveToFront(el)
		return
	}

	el := c.order.PushFront(&cacheEntry{key: key, lines: cloneLines(lines)})
	c.items[key] = el

	if c.order.Len() > cacheCapacity {
		oldest := c.order.Back()
		if oldest != nil {
			c.order.Remove(oldest)
			delete(c.items, oldest.Value.(*cacheEntry).key)
		}
	}
}

func cloneLines(lines []string) []string {
	out := make([]string, len(lines))
	copy(out, lines)
	return out
}
