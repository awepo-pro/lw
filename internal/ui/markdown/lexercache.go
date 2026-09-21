package markdown

import (
	"sync"

	"github.com/alecthomas/chroma/v2/lexers"
)

// lexerCache memoizes, per fence language string, whether chroma has a
// lexer for it — the question withChromaTheme asks (through lexersHave)
// once per fenced code block. It is written only when a language string
// is seen for the first time: no eager refresh, no TTL, no
// invalidation path, and none would be correct — lw registers chroma
// colour styles at runtime (chroma.go) but never lexers, so the
// lang→lexer mapping is static for the process lifetime. If a lexer is
// ever registered at runtime, that registration is the one trigger that
// must reset this map.
//
// The memo stores lexers.Get's whole answer (nil or not) and never a
// re-implementation of chroma's lookup: on an alias miss Get treats its
// input as a filename and glob-matches it against every pattern of all
// ~250 registered lexers, twice (once as an extension, once verbatim) —
// the exact behaviour a lookalike would silently freeze wrong.
var (
	lexerCacheMu sync.Mutex
	lexerCache   = map[string]bool{}
)

// lexersHave reports whether chroma has a lexer for lang, memoized in
// lexerCache. The registry call itself runs outside the mutex: it is
// read-only (chroma v2.20.0 registry Get: map reads + glob scans, no
// registry writes) and already ran concurrently per fence before this memo
// existed, and serializing first-sight misses (~ms each) behind one
// lock would stagger concurrent renders for no correctness gain — two
// goroutines first-seeding the same tag both compute Get's answer and
// store the same value.
func lexersHave(lang string) bool {
	lexerCacheMu.Lock()
	hit, ok := lexerCache[lang]
	lexerCacheMu.Unlock()
	if ok {
		return hit
	}

	hit = lexers.Get(lang) != nil

	lexerCacheMu.Lock()
	lexerCache[lang] = hit
	lexerCacheMu.Unlock()
	return hit
}
