package markdown

import (
	"sync"
	"testing"

	"github.com/alecthomas/chroma/v2/lexers"
)

// TestLexerCacheMatchesRegistry pins lexersHave to chroma's own answer:
// for every tag in the table the cached value must equal a fresh
// lexers.Get(lang) != nil evaluated outside the cache. This is the test
// that forbids re-implementing chroma's lookup — "GO", "c++", "" and
// "notalang" are each resolved (or not) by Get's exact name, alias,
// lowercase and filename-glob ladder, and only Get's own answer is
// allowed in the map.
func TestLexerCacheMatchesRegistry(t *testing.T) {
	for _, lang := range []string{"go", "python", "js", "", "notalang", "GO", "c++", "sh"} {
		got := lexersHave(lang)
		again := lexersHave(lang) // the hit path must return the same answer
		want := lexers.Get(lang) != nil
		if got != want || again != want {
			t.Errorf("lexersHave(%q) = %v (repeat %v), want lexers.Get(%q) != nil = %v",
				lang, got, again, lang, want)
		}
	}
}

// TestLexerCacheFirstMissThenHit checks the one write trigger: the first
// sight of a language string adds exactly one entry, and every later
// call with the same tag is a read that must not grow the map. The probe
// tag is chosen so no render elsewhere in the package's tests can have
// cached it first.
func TestLexerCacheFirstMissThenHit(t *testing.T) {
	const probe = "zz-lexercache-probe-not-a-language"

	lexerCacheMu.Lock()
	before := len(lexerCache)
	if _, seeded := lexerCache[probe]; seeded {
		lexerCacheMu.Unlock()
		t.Fatalf("probe tag %q already cached; pick a fresh one", probe)
	}
	lexerCacheMu.Unlock()

	first := lexersHave(probe)

	lexerCacheMu.Lock()
	after := len(lexerCache)
	val, present := lexerCache[probe]
	lexerCacheMu.Unlock()

	if after != before+1 || !present {
		t.Fatalf("first lexersHave(%q) grew the cache from %d to %d entries (present=%v); want exactly one new entry",
			probe, before, after, present)
	}
	if val != first {
		t.Fatalf("cached value %v disagrees with returned value %v", val, first)
	}
	if first {
		t.Fatalf("probe tag %q must not resolve to a lexer", probe)
	}

	if second := lexersHave(probe); second != first {
		t.Fatalf("second lexersHave(%q) = %v, want %v", probe, second, first)
	}

	lexerCacheMu.Lock()
	final := len(lexerCache)
	lexerCacheMu.Unlock()
	if final != after {
		t.Fatalf("a hit grew the cache from %d to %d entries; only a first sight may write", after, final)
	}
}

// TestLexerCacheConcurrent exercises the memo from many goroutines at
// once, mixing tags that resolve, tags chroma has no lexer for, and
// first-seen tags unique to this test, so the write path runs while
// other goroutines read. Run under -race this is the test that would
// catch an unguarded cache access (conventions: cache_test.go).
func TestLexerCacheConcurrent(t *testing.T) {
	langs := []string{
		"go", "GO", "txt", "sh", "c++", "notalang",
		"zz-lexercache-concurrent-1", "zz-lexercache-concurrent-2", "zz-lexercache-concurrent-3",
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				lexersHave(langs[(i+j)%len(langs)])
			}
		}(i)
	}
	wg.Wait()

	lexerCacheMu.Lock()
	defer lexerCacheMu.Unlock()
	for _, lang := range langs {
		if _, ok := lexerCache[lang]; !ok {
			t.Errorf("tag %q missing from the cache after the run", lang)
		}
	}
}
